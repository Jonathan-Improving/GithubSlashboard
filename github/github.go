// Package github owns all GitHub access. It fetches the tracked PR set (PRs the
// operator authored and PRs the operator was requested to review) and assembles
// each PR's chronological event trail (TDD 1.1, 1.4). It exposes read-only
// operations only — no method here issues a state-mutating GitHub call
// (TDD 1.3), which is the architectural enforcement of the read-only guarantee.
package github

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	gh "github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// Client is a read-only GitHub acquisition client. It wraps the go-github REST
// client and deliberately exposes no mutating operation. It also issues
// read-only GraphQL queries (for review-thread resolution state, which the REST
// API does not expose) over the same authenticated transport.
type Client struct {
	rest  *gh.Client
	http  *http.Client
	login string
}

// NewClient builds an authenticated read-only client from a bearer token and
// resolves the authenticated operator's login (used to scope authored vs.
// review-requested queries).
func NewClient(ctx context.Context, token string) (*Client, error) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(ctx, ts)
	rest := gh.NewClient(httpClient)

	user, _, err := rest.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("resolve authenticated user: %w", err)
	}
	if user.Login == nil {
		return nil, fmt.Errorf("authenticated user has no login")
	}
	return &Client{rest: rest, http: httpClient, login: user.GetLogin()}, nil
}

// Login returns the authenticated operator's login.
func (c *Client) Login() string { return c.login }

// FetchTracked retrieves every PR the operator authored and every PR the
// operator was requested to review, past or present (TDD 1.1), and assembles a
// chronological event trail for each (TDD 1.4). Any API error aborts with a
// non-nil error so the caller can leave the store and output untouched
// (TDD 1.2).
//
// prior is the store's existing records keyed by PR key; includeTerminal
// controls the terminal-skip optimization. When includeTerminal is false
// (default), a PR that the store already records as merged or closed is not
// crawled: its judged result is cached in the store and a terminal PR will not
// change, so the prior record is carried forward verbatim and no per-PR GitHub
// calls are spent on it (TDD 1.5). When true, every discovered PR is crawled
// afresh regardless of its stored state (a deliberate deep run).
func (c *Client) FetchTracked(ctx context.Context, prior map[string]model.PR, includeTerminal bool) ([]model.PR, error) {
	authored, err := c.searchPRs(ctx, fmt.Sprintf("is:pr author:%s", c.login), model.RoleSubmitter)
	if err != nil {
		return nil, fmt.Errorf("fetch authored PRs: %w", err)
	}
	// review-requested captures both current and past review requests via the
	// reviewed-by/review-requested qualifiers; review-requested covers pending,
	// reviewed-by covers acted-on.
	reviewReq, err := c.searchPRs(ctx, fmt.Sprintf("is:pr review-requested:%s", c.login), model.RoleReviewer)
	if err != nil {
		return nil, fmt.Errorf("fetch review-requested PRs: %w", err)
	}
	reviewed, err := c.searchPRs(ctx, fmt.Sprintf("is:pr reviewed-by:%s", c.login), model.RoleReviewer)
	if err != nil {
		return nil, fmt.Errorf("fetch reviewed PRs: %w", err)
	}

	// The review-requested search returns exactly the PRs with a pending review
	// request on the operator — GitHub's authoritative "please review" signal.
	// Mark them so classification can deterministically treat them as awaiting
	// our action (a live request is a hard fact, not an inference).
	for i := range reviewReq {
		reviewReq[i].ReviewRequested = true
	}

	// Merge, deduplicating by key. Submitter role wins over reviewer if a PR
	// somehow appears in both (the operator authored it).
	byKey := make(map[string]*model.PR)
	order := []string{}
	add := func(prs []model.PR) {
		for i := range prs {
			p := prs[i]
			if existing, ok := byKey[p.Key()]; ok {
				if existing.Role != model.RoleSubmitter && p.Role == model.RoleSubmitter {
					existing.Role = model.RoleSubmitter
				}
				// Preserve a pending review request seen from any source slice.
				if p.ReviewRequested {
					existing.ReviewRequested = true
				}
				continue
			}
			cp := p
			byKey[p.Key()] = &cp
			order = append(order, p.Key())
		}
	}
	add(authored)
	add(reviewReq)
	add(reviewed)

	out := make([]model.PR, 0, len(order))
	for _, k := range order {
		p := byKey[k]
		// Terminal-skip: if the store already has this PR as merged or closed
		// and we are not doing a deliberate deep run, carry the cached record
		// forward without any per-PR GitHub crawl. The record already holds the
		// judged verdict (bucket, close_reason, priority, companion, emoji) and
		// the terminal state will not change (TDD 1.5).
		if !includeTerminal {
			if old, ok := prior[k]; ok && (old.Bucket == model.BucketMerged || old.Bucket == model.BucketClosed) {
				// Preserve the role we just discovered (submitter wins), but
				// otherwise the prior record is authoritative and skips GitHub.
				carried := old
				// GitHubState is not persisted; restore it from the cached
				// bucket so classification routes the carried record to the
				// correct immutable-floor handler (not the open path).
				if carried.Bucket == model.BucketMerged {
					carried.GitHubState = model.GitHubStateMerged
				} else {
					carried.GitHubState = model.GitHubStateClosed
				}
				out = append(out, carried)
				continue
			}
		}
		if err := c.attachEventTrail(ctx, p); err != nil {
			return nil, fmt.Errorf("assemble event trail for %s: %w", p.Key(), err)
		}
		out = append(out, *p)
	}
	return out, nil
}

// searchPRs runs a read-only issue search and maps each result into a base PR
// record (without its event trail).
func (c *Client) searchPRs(ctx context.Context, query string, role model.Role) ([]model.PR, error) {
	opts := &gh.SearchOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var out []model.PR
	for {
		res, resp, err := c.rest.Search.Issues(ctx, query, opts)
		if err != nil {
			return nil, err
		}
		for _, issue := range res.Issues {
			if issue.PullRequestLinks == nil {
				continue // not a PR
			}
			pr, err := prFromIssue(issue, role)
			if err != nil {
				return nil, err
			}
			out = append(out, pr)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// prFromIssue maps a search result issue (representing a PR) into a base PR
// record. The raw GitHub state (open/closed/merged) is recorded so the
// classifier can enforce the immutable floors.
func prFromIssue(issue *gh.Issue, role model.Role) (model.PR, error) {
	repo, err := repoFromURL(issue.GetHTMLURL())
	if err != nil {
		return model.PR{}, err
	}
	p := model.PR{
		Repo:    repo,
		Number:  issue.GetNumber(),
		Title:   issue.GetTitle(),
		URL:     issue.GetHTMLURL(),
		Role:    role,
		Created: issue.GetCreatedAt().Time,
	}

	// Determine raw state. The issue search reports state open/closed; merged is
	// distinguished by the pull-request links' merged-at.
	switch {
	case issue.PullRequestLinks != nil && issue.PullRequestLinks.MergedAt != nil:
		p.GitHubState = model.GitHubStateMerged
		t := issue.PullRequestLinks.GetMergedAt().Time
		p.MergedAt = &t
	case strings.EqualFold(issue.GetState(), "closed"):
		p.GitHubState = model.GitHubStateClosed
		t := issue.GetClosedAt().Time
		p.ClosedAt = &t
	default:
		p.GitHubState = model.GitHubStateOpen
	}
	return p, nil
}

// repoFromURL extracts "owner/name" from a PR HTML URL of the form
// https://github.com/owner/name/pull/N.
func repoFromURL(htmlURL string) (string, error) {
	const marker = "github.com/"
	i := strings.Index(htmlURL, marker)
	if i < 0 {
		return "", fmt.Errorf("cannot parse repo from URL %q", htmlURL)
	}
	rest := htmlURL[i+len(marker):]
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("cannot parse repo from URL %q", htmlURL)
	}
	return parts[0] + "/" + parts[1], nil
}

// attachEventTrail assembles the PR's chronological, timestamped event trail
// (TDD 1.4): issue comments, review submissions, state transitions, and label
// changes, each carrying its author and a role hint. It also records the raw
// merged/closed state from the PR resource as authoritative.
func (c *Client) attachEventTrail(ctx context.Context, p *model.PR) error {
	owner, name, err := splitRepo(p.Repo)
	if err != nil {
		return err
	}

	// Authoritative PR resource: refine the raw state (merged supersedes closed).
	pull, _, err := c.rest.PullRequests.Get(ctx, owner, name, p.Number)
	if err != nil {
		return fmt.Errorf("get PR: %w", err)
	}
	if pull.GetMerged() {
		p.GitHubState = model.GitHubStateMerged
		if pull.MergedAt != nil {
			t := pull.GetMergedAt().Time
			p.MergedAt = &t
		}
	} else if strings.EqualFold(pull.GetState(), "closed") {
		p.GitHubState = model.GitHubStateClosed
		if pull.ClosedAt != nil {
			t := pull.GetClosedAt().Time
			p.ClosedAt = &t
		}
	} else {
		p.GitHubState = model.GitHubStateOpen
		// Mergeable is populated by the single-PR Get (not by search); it is
		// nil while GitHub is still computing it. false means the PR conflicts
		// with its base branch, which drives the conflicted action for
		// submitter PRs (classify). Only meaningful for open PRs.
		if pull.Mergeable != nil {
			m := pull.GetMergeable()
			p.Mergeable = &m
		}
	}

	var events []model.Event
	events = append(events, model.Event{
		Timestamp: p.Created,
		Author:    pull.GetUser().GetLogin(),
		RoleHint:  "author",
		Kind:      model.EventStateTransition,
		Text:      "opened",
	})

	comments, err := c.collectComments(ctx, owner, name, p.Number)
	if err != nil {
		return err
	}
	events = append(events, comments...)

	reviews, err := c.collectReviews(ctx, owner, name, p.Number)
	if err != nil {
		return err
	}
	events = append(events, reviews...)

	timeline, err := c.collectTimeline(ctx, owner, name, p.Number)
	if err != nil {
		return err
	}
	events = append(events, timeline...)

	// CI status: only meaningful for an open PR, and only when GitHub gives us a
	// head SHA to query. Summarized as one latest-authoritative event so a stale
	// comment mentioning an earlier failure no longer governs (TDD 1.4).
	if p.GitHubState == model.GitHubStateOpen {
		if sha := pull.GetHead().GetSHA(); sha != "" {
			ciEvent, failing, err := c.collectCIStatus(ctx, owner, name, sha)
			if err != nil {
				return err
			}
			// Record the failing state as a fact in its own right, not only as
			// trail prose: the classifier acts on it deterministically rather
			// than trusting the model to read it out of the trail (TDD 4.10).
			p.CIFailing = failing
			if ciEvent != nil {
				events = append(events, *ciEvent)
			}
		}
	}

	// Review threads and the overall review decision: only meaningful for an
	// open PR. Unresolved threads are outstanding line comments the author must
	// address, and GitHub exposes their resolution state only via GraphQL — an
	// approval can coexist with open comments, so without this the trail cannot
	// tell "approved and done" from "approved, feedback pending" (TDD 1.6).
	if p.GitHubState == model.GitHubStateOpen {
		res, err := c.collectReviewThreads(ctx, owner, name, p.Number)
		if err != nil {
			return err
		}
		p.UnresolvedThreads = countUnresolved(res.Threads)
		if ev := summarizeReviewThreads(res.Threads); ev != nil {
			events = append(events, *ev)
		}
		if ev := summarizeReviewDecision(res.Decision, pull.GetUpdatedAt().Time); ev != nil {
			events = append(events, *ev)
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	p.Events = events
	if len(events) > 0 {
		p.LastActivity = events[len(events)-1].Timestamp
	} else {
		p.LastActivity = p.Created
	}
	return nil
}

func (c *Client) collectComments(ctx context.Context, owner, name string, number int) ([]model.Event, error) {
	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var out []model.Event
	for {
		comments, resp, err := c.rest.Issues.ListComments(ctx, owner, name, number, opts)
		if err != nil {
			return nil, fmt.Errorf("list comments: %w", err)
		}
		for _, cm := range comments {
			out = append(out, model.Event{
				Timestamp: cm.GetCreatedAt().Time,
				Author:    cm.GetUser().GetLogin(),
				Kind:      model.EventComment,
				Text:      cm.GetBody(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (c *Client) collectReviews(ctx context.Context, owner, name string, number int) ([]model.Event, error) {
	opts := &gh.ListOptions{PerPage: 100}
	var out []model.Event
	for {
		reviews, resp, err := c.rest.PullRequests.ListReviews(ctx, owner, name, number, opts)
		if err != nil {
			return nil, fmt.Errorf("list reviews: %w", err)
		}
		for _, rv := range reviews {
			out = append(out, model.Event{
				Timestamp: rv.GetSubmittedAt().Time,
				Author:    rv.GetUser().GetLogin(),
				RoleHint:  "reviewer",
				Kind:      model.EventReview,
				Text:      fmt.Sprintf("%s: %s", strings.ToLower(rv.GetState()), rv.GetBody()),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// collectCIStatus fetches the check-runs on the given head SHA and summarizes
// them into a single latest-authoritative CI event, plus whether any check is
// currently failing (which drives the deterministic CI flag, TDD 4.10). The event
// is nil when the head has no check-runs (nothing to report). It is read-only.
func (c *Client) collectCIStatus(ctx context.Context, owner, name, sha string) (*model.Event, bool, error) {
	opts := &gh.ListCheckRunsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var runs []*gh.CheckRun
	for {
		res, resp, err := c.rest.Checks.ListCheckRunsForRef(ctx, owner, name, sha, opts)
		if err != nil {
			return nil, false, fmt.Errorf("list check runs: %w", err)
		}
		runs = append(runs, res.CheckRuns...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	ev, failing := summarizeChecks(runs)
	return ev, failing, nil
}

// summarizeChecks reduces a set of check-runs to one CI event and a failing flag.
// Overall outcome: failure if any completed run failed
// (failure/timed_out/action_required), else pending if any run is not yet
// completed, else success. The event text names the failing checks so the model
// can judge relevance, and the timestamp is the latest completion (or zero-safe
// fallback) so it sorts as the most recent authoritative signal in the trail. The
// returned bool is true exactly when at least one check failed — the same
// determination, surfaced separately so the classifier can act on it as a fact
// rather than relying on the model to read it out of the trail. Returns a nil
// event when there are no runs.
func summarizeChecks(runs []*gh.CheckRun) (*model.Event, bool) {
	if len(runs) == 0 {
		return nil, false
	}
	var failing []string
	anyPending := false
	var latest time.Time
	for _, r := range runs {
		status := strings.ToLower(r.GetStatus())
		concl := strings.ToLower(r.GetConclusion())
		if r.CompletedAt != nil && r.GetCompletedAt().Time.After(latest) {
			latest = r.GetCompletedAt().Time
		}
		if status != "completed" {
			anyPending = true
			continue
		}
		switch concl {
		case "failure", "timed_out", "action_required", "startup_failure":
			failing = append(failing, r.GetName())
		}
	}

	var text string
	switch {
	case len(failing) > 0:
		text = "ci: failing — " + strings.Join(failing, ", ")
	case anyPending:
		text = "ci: pending"
	default:
		text = "ci: passing"
	}
	if latest.IsZero() {
		latest = time.Now()
	}
	return &model.Event{
		Timestamp: latest,
		Author:    "github-ci",
		RoleHint:  "ci",
		Kind:      model.EventCIStatus,
		Text:      text,
	}, len(failing) > 0
}

func (c *Client) collectTimeline(ctx context.Context, owner, name string, number int) ([]model.Event, error) {
	opts := &gh.ListOptions{PerPage: 100}
	var out []model.Event
	for {
		timeline, resp, err := c.rest.Issues.ListIssueTimeline(ctx, owner, name, number, opts)
		if err != nil {
			return nil, fmt.Errorf("list timeline: %w", err)
		}
		for _, ev := range timeline {
			e, ok := timelineEvent(ev)
			if ok {
				out = append(out, e)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// timelineEvent maps a timeline entry to a trail event, keeping only the state
// transitions and label changes relevant to classification (TDD 1.4).
func timelineEvent(ev *gh.Timeline) (model.Event, bool) {
	kind := ev.GetEvent()
	at := timelineTime(ev)
	base := model.Event{Timestamp: at, Author: timelineActor(ev)}

	switch kind {
	case "closed":
		base.Kind = model.EventStateTransition
		base.Text = "closed"
	case "merged":
		base.Kind = model.EventStateTransition
		base.Text = "merged"
	case "reopened":
		base.Kind = model.EventStateTransition
		base.Text = "reopened"
	case "ready_for_review":
		base.Kind = model.EventReadyForReview
		base.Text = "ready_for_review"
	case "convert_to_draft", "converted_to_draft":
		base.Kind = model.EventConvertedToDraft
		base.Text = "converted_to_draft"
	case "review_requested":
		base.Kind = model.EventReviewRequested
		base.Text = "review_requested"
	case "labeled":
		base.Kind = model.EventLabel
		base.Text = "labeled: " + ev.GetLabel().GetName()
	case "unlabeled":
		base.Kind = model.EventLabel
		base.Text = "unlabeled: " + ev.GetLabel().GetName()
	default:
		return model.Event{}, false
	}
	return base, true
}

func timelineTime(ev *gh.Timeline) time.Time {
	if ev.CreatedAt != nil {
		return ev.GetCreatedAt().Time
	}
	if ev.SubmittedAt != nil {
		return ev.GetSubmittedAt().Time
	}
	return time.Time{}
}

func timelineActor(ev *gh.Timeline) string {
	if ev.Actor != nil {
		return ev.Actor.GetLogin()
	}
	if ev.User != nil {
		return ev.User.GetLogin()
	}
	return ""
}

func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q, want owner/name", repo)
	}
	return parts[0], parts[1], nil
}
