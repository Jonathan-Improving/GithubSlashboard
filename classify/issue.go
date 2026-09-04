package classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// minCommentsForJudgment is the number of comments an issue must carry before
// the provider is consulted. An issue with no comments has no conversation for a
// model to read — its trail is just "opened" — so judging it would invent a note
// from nothing rather than summarizing anything (TDD 8.3).
//
// This is deliberately a constant and not a configuration value. POLICY puts
// values carrying domain *policy* in config (word bounds, stale thresholds,
// retry caps); this is a degenerate-input guard, not a policy dial: "an empty
// trail has nothing to judge" is a structural property of the data. Exposing it
// would only allow settings that either waste calls on empty trails or suppress
// notes on genuinely discussed issues.
const minCommentsForJudgment = 1

// ClassifyIssues classifies every issue, bounding provider fan-out by the same
// worker limit the PR path uses (TECH: concurrency model). Each issue's judgment
// depends only on its own event trail, so the calls are independent. Results are
// returned in input order.
func (c *Classifier) ClassifyIssues(ctx context.Context, issues []model.Issue) []model.Issue {
	out := make([]model.Issue, len(issues))
	sem := make(chan struct{}, c.cfg.ClassifyWorkers)
	var wg sync.WaitGroup

	for i := range issues {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			out[idx] = c.classifyIssue(ctx, issues[idx])
		}(i)
	}
	wg.Wait()
	return out
}

// classifyIssue classifies a single issue. The closed floor is deterministic and
// is applied before any inference (TDD 8.2); an open issue's disposition comes
// from the trail only when there is a trail worth reading (TDD 8.3).
func (c *Classifier) classifyIssue(ctx context.Context, iss model.Issue) model.Issue {
	if iss.GitHubClosed {
		return c.classifyIssueClosed(ctx, iss)
	}
	return c.classifyIssueOpen(ctx, iss)
}

// classifyIssueClosed applies the immutable closed floor. Unlike a PR's closed
// sub-reason, an issue's close reason is GitHub's own state_reason — a hard fact
// captured during acquisition — so it is never inferred and no model response
// can change it (TDD 8.2). The provider is consulted only for a note and
// priority, and only when there was a conversation to summarize.
func (c *Classifier) classifyIssueClosed(ctx context.Context, iss model.Issue) model.Issue {
	iss.Bucket = model.IssueBucketClosed
	iss.Action = ""
	// A closed issue GitHub gave no state_reason for (older issues predate the
	// field) is recorded as completed: it was closed deliberately, and the
	// schema requires a reason on a closed row.
	if !iss.CloseReason.Valid() {
		iss.CloseReason = model.IssueCloseReasonCompleted
	}

	if c.cfg.SkipFloorNotes || !c.issueWorthJudging(iss) {
		// Either the operator opted out of floor notes, or there is nothing to
		// judge. Neither is a failure, so the row is not marked unverified — its
		// bucket and reason are hard facts (TDD 8.3).
		iss.Priority = model.PriorityNeutral
		iss.Companion = ""
		iss.Emoji = ""
		return iss
	}

	if len(iss.Events) == 0 && iss.Companion != "" {
		// Carried terminal record: the trail was not re-crawled because the
		// issue is settled, and a cached verdict is already present. Keep it
		// rather than re-judging an empty trail.
		return iss
	}

	c.applyIssueNote(ctx, &iss)
	return iss
}

// classifyIssueOpen judges an open issue's disposition, priority, and note from
// its comment trail, then layers stale determination on the issue's own
// threshold (TDD 8.4, 8.5).
func (c *Classifier) classifyIssueOpen(ctx context.Context, iss model.Issue) model.Issue {
	// Operator override wins unconditionally and short-circuits inference, as it
	// does for PRs (TDD 5.1, 8.7).
	if iss.OperatorStale {
		iss.Bucket = model.IssueBucketStale
		iss.Action = ""
		iss.CloseReason = ""
		c.applyIssueNote(ctx, &iss)
		return iss
	}

	iss.CloseReason = ""

	if !c.issueWorthJudging(iss) {
		// No conversation: classify from hard facts alone. Nobody has engaged,
		// which is precisely what triage means, and the row carries no note. It
		// is not unverified — no judgment was attempted, so there is nothing to
		// doubt (TDD 8.3).
		iss.Bucket = model.IssueBucketOpen
		iss.Action = model.IssueActionTriage
		iss.Priority = model.PriorityNeutral
		iss.Companion = ""
		iss.Emoji = ""
		// A quiet, never-engaged issue still ages into stale on the issue
		// threshold (TDD 8.5).
		if c.issueStaleByAge(iss) {
			iss.Bucket = model.IssueBucketStale
			iss.Action = ""
		}
		return iss
	}

	// Unchanged-since-last-run short-circuit (TDD 8.8), mirroring the PR path
	// (TDD 4.13). An issue's only deterministic inputs are its comment count and
	// most recent trail event — no CI, mergeability, or review-thread concepts
	// exist for an issue — so the fingerprint is simpler than a PR's.
	fp := issueFingerprint(iss)
	if old, ok := c.priorIssues[iss.Key()]; ok && !old.Unverified && old.InputFingerprint != "" && old.InputFingerprint == fp {
		iss.Bucket = old.Bucket
		iss.Action = old.Action
		iss.Priority = old.Priority
		iss.Companion = old.Companion
		iss.Emoji = old.Emoji
		iss.Unverified = false
		iss.InputFingerprint = fp
		// Carried forward unchanged: not part of the notification hook's
		// change set this run (TDD 9.1).
		iss.WasJudged = false
		return iss
	}

	res := c.judgeIssue(ctx, iss)
	if res.Unverified {
		iss.Unverified = true
		iss.Bucket = model.IssueBucketOpen
		iss.Action = model.IssueActionTriage // conservative fallback, not authoritative
		iss.Priority = model.PriorityNeutral
		iss.Companion = ""
		iss.Emoji = ""
		c.log.Warn("issue classification unverified", "issue", iss.Key(), "attempts", res.Attempts, "reason", res.Err)
		// Deliberately not stamping InputFingerprint here: an unverified result
		// must never be treated as a cached judgment on a later run (TDD 8.8, 8.9).
		// The provider WAS reached this run, so this still counts as "changed"
		// for the notification hook (TDD 9.1) even without a usable verdict.
		iss.WasJudged = true
		return iss
	}

	iss.Priority = res.Response.Priority
	iss.Companion = res.Response.Companion
	iss.Emoji = res.Response.Emoji

	// The response carries bucket and action as plain strings, because the legal
	// vocabulary is entity-specific (SCHEMA § Response). This call site is the
	// boundary at which an issue verdict becomes typed. The provider validator
	// already checked membership against the issue vocabulary, so a parse failure
	// means a contract breach, not a model mistake.
	bucket, bErr := model.ParseIssueBucket(res.Response.Bucket)
	action, aErr := model.ParseIssueAction(res.Response.Action)
	if bErr != nil {
		bucket = model.IssueBucketOpen
	}
	if aErr != nil {
		action = model.IssueActionTriage
	}

	switch bucket {
	case model.IssueBucketStale:
		iss.Bucket = model.IssueBucketStale
		iss.Action = ""
	default:
		// The provider may not move an open issue into the closed bucket; that
		// floor is owned by GitHub's state (TDD 8.2). Keep it open.
		iss.Bucket = model.IssueBucketOpen
		iss.Action = action
	}

	// Age-threshold stale on the issue's own schedule (TDD 8.5).
	if iss.Bucket == model.IssueBucketOpen && c.issueStaleByAge(iss) {
		iss.Bucket = model.IssueBucketStale
		iss.Action = ""
	}

	// Stamp the fingerprint of the inputs that produced this judgment, so a
	// later run can detect "unchanged" and skip the provider call (TDD 8.8).
	iss.InputFingerprint = issueFingerprint(iss)
	// The provider was reached, but only an open-bucket outcome counts as
	// "changed" for the notification hook (TDD 9.2) — an issue that landed in
	// Stale this run is a settled fact the operator is not expected to act on
	// further, even though classification consulted the provider to get there.
	iss.WasJudged = iss.Bucket == model.IssueBucketOpen
	return iss
}

// issueWorthJudging reports whether the issue has enough conversation for a
// model call to produce signal (TDD 8.3).
func (c *Classifier) issueWorthJudging(iss model.Issue) bool {
	return iss.CommentCount >= minCommentsForJudgment
}

// issueStaleByAge reports whether the issue has had no activity within the
// configured issue threshold, which is independent of the PR threshold
// (TDD 8.5).
func (c *Classifier) issueStaleByAge(iss model.Issue) bool {
	last := iss.LastActivity
	if last.IsZero() {
		last = iss.Created
	}
	return c.now().Sub(last) >= c.cfg.IssueStaleAgeThreshold
}

// issueFingerprint hashes every deterministic input to an open issue's
// judgment: CommentCount and the most recent trail event (timestamp, kind, and
// text). Mirrors classify.fingerprint for PRs (TDD 4.13) with the simpler set
// of inputs an issue actually has — no CI, mergeability, or review-thread
// concepts exist for an issue. Two calls with the same inputs always produce
// the same fingerprint; the result is opaque and has no meaning beyond
// equality comparison.
func issueFingerprint(iss model.Issue) string {
	var last model.Event
	if n := len(iss.Events); n > 0 {
		last = iss.Events[n-1]
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%d",
		last.Timestamp.UTC().Format(time.RFC3339Nano), last.Kind, last.Text, iss.CommentCount)
	return hex.EncodeToString(h.Sum(nil))
}

// applyIssueNote consults the provider for a note and priority on an issue whose
// bucket is already fixed (closed, or operator-stale). Failure yields a neutral,
// note-less result rather than blocking; it does not mark the row unverified
// because the authoritative bucket is a hard fact, not a judgment.
func (c *Classifier) applyIssueNote(ctx context.Context, iss *model.Issue) {
	res := c.judgeIssue(ctx, *iss)
	if res.Unverified {
		iss.Priority = model.PriorityNeutral
		iss.Companion = ""
		iss.Emoji = ""
		// Log it: the row is still authoritative (its bucket is a hard fact), so
		// it is not marked unverified — which means a systematic failure here
		// would otherwise be invisible, showing up only as every settled row
		// quietly losing its note.
		c.log.Warn("issue note unavailable", "issue", iss.Key(), "attempts", res.Attempts, "reason", res.Err)
		return
	}
	iss.Priority = res.Response.Priority
	iss.Companion = res.Response.Companion
	iss.Emoji = res.Response.Emoji
}

// judgeIssue builds the provider request for an issue and runs the same bounded,
// self-correcting retry loop the PR path uses (TDD 6.4, 6.5). The issue
// vocabulary travels in the request constraints, so one provider path serves
// both entities without either enum being widened (SCHEMA § Request).
func (c *Classifier) judgeIssue(ctx context.Context, iss model.Issue) provider.Result {
	state := "open"
	if iss.GitHubClosed {
		state = "closed"
	}
	req := provider.Request{
		Entity:      provider.EntityIssue,
		Repo:        iss.Repo,
		Number:      iss.Number,
		Role:        string(iss.Role),
		State:       state,
		Events:      iss.Events,
		Constraints: provider.IssueConstraintsFrom(c.cfg.CompanionWordsMin, c.cfg.CompanionWordsMax),
	}
	return provider.Judge(ctx, c.prov, req, c.cfg.LLMRetryCap, c.cfg.ProviderTimeout)
}
