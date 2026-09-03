package github

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gh "github.com/google/go-github/v66/github"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// FetchIssues retrieves every issue the operator authored, commented on, or was
// assigned, deduplicated, and assembles a chronological event trail for each
// (TDD 8.1). Any API error aborts with a non-nil error so the caller can leave
// the store and output untouched (TDD 1.2).
//
// prior is the store's existing issue records keyed by issue key;
// includeTerminal controls the terminal-skip optimization. When includeTerminal
// is false (default), an issue the store already records as closed is not
// crawled: a closed issue is settled, so the cached record is carried forward and
// no per-issue GitHub calls are spent on it (TDD 1.5 applied to issues).
//
// Issues carry none of the PR review machinery, so there is no mergeability,
// check-run, review-thread, or review-decision fetch here — hence no GraphQL.
func (c *Client) FetchIssues(ctx context.Context, prior map[string]model.Issue, includeTerminal bool) ([]model.Issue, error) {
	authored, err := c.searchIssues(ctx, fmt.Sprintf("is:issue author:%s", c.login), model.IssueRoleAuthor)
	if err != nil {
		return nil, fmt.Errorf("fetch authored issues: %w", err)
	}
	// Commented-on and assigned issues the operator did not open are
	// participation. -author excludes self-authored so the author search stays
	// the authoritative source for those.
	commented, err := c.searchIssues(ctx,
		fmt.Sprintf("is:issue commenter:%s -author:%s", c.login, c.login), model.IssueRoleParticipant)
	if err != nil {
		return nil, fmt.Errorf("fetch commented issues: %w", err)
	}
	// Being assigned is itself an ask, so an assigned issue counts as
	// participation even with no comment from the operator (TDD 8.1).
	assigned, err := c.searchIssues(ctx,
		fmt.Sprintf("is:issue assignee:%s -author:%s", c.login, c.login), model.IssueRoleParticipant)
	if err != nil {
		return nil, fmt.Errorf("fetch assigned issues: %w", err)
	}

	// Merge, deduplicating by key. The author role wins over participant if an
	// issue somehow appears in both (the operator opened it).
	byKey := make(map[string]*model.Issue)
	var order []string
	add := func(issues []model.Issue) {
		for i := range issues {
			iss := issues[i]
			if existing, ok := byKey[iss.Key()]; ok {
				if existing.Role != model.IssueRoleAuthor && iss.Role == model.IssueRoleAuthor {
					existing.Role = model.IssueRoleAuthor
				}
				continue
			}
			cp := iss
			byKey[iss.Key()] = &cp
			order = append(order, iss.Key())
		}
	}
	add(authored)
	add(commented)
	add(assigned)

	out := make([]model.Issue, 0, len(order))
	for _, k := range order {
		iss := byKey[k]
		if !includeTerminal {
			if old, ok := prior[k]; ok && old.Bucket == model.IssueBucketClosed {
				// Carry the cached record forward: it already holds the judged
				// verdict and a closed issue will not change. GitHubClosed is not
				// persisted, so restore it from the cached bucket, or the carried
				// record would be routed to the open path on classify.
				carried := old
				carried.GitHubClosed = true
				out = append(out, carried)
				continue
			}
		}
		if err := c.attachIssueTrail(ctx, iss); err != nil {
			return nil, fmt.Errorf("assemble event trail for %s: %w", iss.Key(), err)
		}
		out = append(out, *iss)
	}
	return out, nil
}

// searchIssues runs a read-only issue search and maps each result into a base
// issue record (without its event trail). Pull requests are excluded: the search
// API returns both from the same endpoint, and `is:issue` is honored server-side,
// but the PullRequestLinks check makes the exclusion explicit rather than
// trusting the qualifier alone.
func (c *Client) searchIssues(ctx context.Context, query string, role model.IssueRole) ([]model.Issue, error) {
	opts := &gh.SearchOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var out []model.Issue
	for {
		res, resp, err := c.rest.Search.Issues(ctx, query, opts)
		if err != nil {
			return nil, err
		}
		for _, issue := range res.Issues {
			if issue.PullRequestLinks != nil {
				continue // a PR, not an issue
			}
			iss, err := issueFromSearch(issue, role)
			if err != nil {
				return nil, err
			}
			out = append(out, iss)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// issueFromSearch maps a search result into a base issue record. The closed
// state and GitHub's own state_reason are captured here because they are hard
// facts that drive the deterministic closed floor (TDD 8.2), and the comment
// count is captured because it gates whether the provider is consulted at all
// (TDD 8.3).
func issueFromSearch(issue *gh.Issue, role model.IssueRole) (model.Issue, error) {
	repo, err := repoFromURL(issue.GetHTMLURL())
	if err != nil {
		return model.Issue{}, err
	}
	iss := model.Issue{
		Repo:         repo,
		Number:       issue.GetNumber(),
		Title:        issue.GetTitle(),
		URL:          issue.GetHTMLURL(),
		Role:         role,
		Created:      issue.GetCreatedAt().Time,
		CommentCount: issue.GetComments(),
	}
	if strings.EqualFold(issue.GetState(), "closed") {
		iss.GitHubClosed = true
		t := issue.GetClosedAt().Time
		iss.ClosedAt = &t
		iss.CloseReason = issueCloseReason(issue.GetStateReason())
	}
	return iss, nil
}

// issueCloseReason maps GitHub's state_reason to the typed issue close reason.
// GitHub spells not-planned with an underscore already, but older issues report
// no reason at all; an unrecognized or absent value yields the empty reason and
// the classifier supplies the default, so an upstream addition to the vocabulary
// never aborts a run.
func issueCloseReason(stateReason string) model.IssueCloseReason {
	r, err := model.ParseIssueCloseReason(strings.ToLower(strings.TrimSpace(stateReason)))
	if err != nil {
		return ""
	}
	return r
}

// attachIssueTrail assembles the issue's chronological, timestamped event trail:
// the opening, its comments, and the state transitions and label changes from its
// timeline. Issue timelines carry no review, mergeable, check-run, or
// review-thread events, so none of the PR determinism helpers apply here.
func (c *Client) attachIssueTrail(ctx context.Context, iss *model.Issue) error {
	owner, name, err := splitRepo(iss.Repo)
	if err != nil {
		return err
	}

	// Authoritative issue resource: the search result's state can lag, and the
	// comment count and state_reason both gate deterministic behavior.
	issue, _, err := c.rest.Issues.Get(ctx, owner, name, iss.Number)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}
	iss.CommentCount = issue.GetComments()
	if strings.EqualFold(issue.GetState(), "closed") {
		iss.GitHubClosed = true
		if issue.ClosedAt != nil {
			t := issue.GetClosedAt().Time
			iss.ClosedAt = &t
		}
		iss.CloseReason = issueCloseReason(issue.GetStateReason())
	} else {
		iss.GitHubClosed = false
		iss.ClosedAt = nil
		iss.CloseReason = ""
	}

	events := []model.Event{{
		Timestamp: iss.Created,
		Author:    issue.GetUser().GetLogin(),
		RoleHint:  "author",
		Kind:      model.EventStateTransition,
		Text:      "opened",
	}}

	comments, err := c.collectComments(ctx, owner, name, iss.Number)
	if err != nil {
		return err
	}
	events = append(events, comments...)

	timeline, err := c.collectTimeline(ctx, owner, name, iss.Number)
	if err != nil {
		return err
	}
	events = append(events, timeline...)

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	iss.Events = events
	iss.LastActivity = events[len(events)-1].Timestamp
	return nil
}
