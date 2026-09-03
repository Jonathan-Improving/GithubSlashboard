package classify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/config"
	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// issueRefNow is the fixed reference time for age-based issue assertions.
var issueRefNow = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// failingProvider always errors, so judgment falls through to unverified without
// the test needing a well-formed response.
type failingProvider struct{}

func (failingProvider) Name() string { return "failing" }
func (failingProvider) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	return "", errors.New("provider unavailable")
}

// issueClassifier builds a Classifier over the given provider at the fixed issue
// reference time, using the default thresholds (PR 40 days, issue 120 days) so
// the two are genuinely distinguishable.
func issueClassifier(p provider.Provider) *Classifier {
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.ClassifyWorkers = 2
	cfg.SkipFloorNotes = false
	// One attempt only: these tests assert the unverified fallback, and retries
	// would just repeat an identical rejection.
	cfg.LLMRetryCap = 0
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(p, cfg, log, func() time.Time { return issueRefNow })
}

// openIssue builds an open issue with the given comment count and last activity.
func openIssue(comments int, lastActivity time.Time) model.Issue {
	return model.Issue{
		Repo:         "a/x",
		Number:       7,
		Title:        "Something",
		URL:          "u",
		Role:         model.IssueRoleAuthor,
		Created:      lastActivity,
		CommentCount: comments,
		LastActivity: lastActivity,
		Events: []model.Event{
			{Timestamp: lastActivity, Author: "op", Kind: model.EventStateTransition, Text: "opened"},
		},
	}
}

// TestIssueClosedFloorUsesGitHubStateReason covers TDD 8.2: a closed issue's
// bucket and close reason are hard GitHub facts and no model response may change
// them.
func TestIssueClosedFloorUsesGitHubStateReason(t *testing.T) {
	// The provider proposes an entirely different disposition; it must be ignored
	// for bucket and reason.
	c := issueClassifier(fixedProvider{out: `{"bucket":"open","action":"triage","priority":"neutral","companion":"still being discussed","emoji":"💬"}`})

	iss := openIssue(3, issueRefNow.Add(-24*time.Hour))
	iss.GitHubClosed = true
	iss.CloseReason = model.IssueCloseReasonNotPlanned

	got := c.classifyIssue(context.Background(), iss)

	if got.Bucket != model.IssueBucketClosed {
		t.Errorf("bucket = %q, want closed (TDD 8.2)", got.Bucket)
	}
	if got.CloseReason != model.IssueCloseReasonNotPlanned {
		t.Errorf("close_reason = %q, want not_planned from GitHub state_reason (TDD 8.2)", got.CloseReason)
	}
	if got.Action != "" {
		t.Errorf("action = %q, want empty on a closed issue", got.Action)
	}
}

// TestIssueClosedWithoutStateReasonDefaults covers the schema requirement that a
// closed row carries a reason even when GitHub supplied none (older issues
// predate the field).
func TestIssueClosedWithoutStateReasonDefaults(t *testing.T) {
	c := issueClassifier(failingProvider{})

	iss := openIssue(0, issueRefNow.Add(-24*time.Hour))
	iss.GitHubClosed = true
	iss.CloseReason = ""

	got := c.classifyIssue(context.Background(), iss)

	if got.CloseReason != model.IssueCloseReasonCompleted {
		t.Errorf("close_reason = %q, want completed default", got.CloseReason)
	}
}

// TestIssueWithNoCommentsSkipsProvider covers TDD 8.3: a zero-comment issue is
// classified from hard facts, is not sent to the model, carries no note, and is
// not marked unverified.
func TestIssueWithNoCommentsSkipsProvider(t *testing.T) {
	cp := &countingProvider{out: `{"bucket":"open","action":"awaiting_others","priority":"elevated","companion":"should not be used","emoji":"💬"}`}
	c := issueClassifier(cp)

	got := c.classifyIssue(context.Background(), openIssue(0, issueRefNow.Add(-24*time.Hour)))

	if cp.calls != 0 {
		t.Errorf("provider invoked %d times, want 0 for a zero-comment issue (TDD 8.3)", cp.calls)
	}
	if got.Bucket != model.IssueBucketOpen {
		t.Errorf("bucket = %q, want open", got.Bucket)
	}
	if got.Action != model.IssueActionTriage {
		t.Errorf("action = %q, want triage (no engagement yet) (TDD 8.3)", got.Action)
	}
	if got.Companion != "" || got.Emoji != "" {
		t.Errorf("companion/emoji = %q/%q, want empty — nothing was judged (TDD 8.3)", got.Companion, got.Emoji)
	}
	if got.Unverified {
		t.Error("unverified = true, want false — no judgment was attempted (TDD 8.3)")
	}
}

// TestIssueWithCommentsGetsInferredNote covers TDD 8.4: an issue with any
// conversation is judged from its trail and carries a note and emoji.
func TestIssueWithCommentsGetsInferredNote(t *testing.T) {
	cp := &countingProvider{out: `{"bucket":"open","action":"awaiting_response","priority":"neutral","companion":"maintainer asked for repro steps","emoji":"❓"}`}
	c := issueClassifier(cp)

	got := c.classifyIssue(context.Background(), openIssue(1, issueRefNow.Add(-24*time.Hour)))

	if cp.calls == 0 {
		t.Error("provider not invoked, want a call for an issue with a comment (TDD 8.4)")
	}
	if got.Action != model.IssueActionAwaitingResponse {
		t.Errorf("action = %q, want awaiting_response (TDD 8.4)", got.Action)
	}
	if got.Companion != "maintainer asked for repro steps" {
		t.Errorf("companion = %q, want the inferred note (TDD 8.4)", got.Companion)
	}
	if got.Emoji != "❓" {
		t.Errorf("emoji = %q, want the inferred glyph (TDD 8.4)", got.Emoji)
	}
}

// TestIssueRejectsPRActionVocabulary proves the issue and PR action sets stay
// separate: a PR action is not a valid issue action, so a response carrying one
// fails validation and drives the unverified fallback rather than being accepted.
func TestIssueRejectsPRActionVocabulary(t *testing.T) {
	c := issueClassifier(fixedProvider{out: `{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"⏳"}`})

	got := c.classifyIssue(context.Background(), openIssue(2, issueRefNow.Add(-24*time.Hour)))

	if !got.Unverified {
		t.Error("unverified = false, want true — awaiting_review is not an issue action")
	}
	if got.Action != model.IssueActionTriage {
		t.Errorf("action = %q, want the conservative triage fallback", got.Action)
	}
}

// TestIssueStaleOnItsOwnThreshold covers TDD 8.5: an issue goes stale on the
// issue threshold, which is independent of the PR threshold.
func TestIssueStaleOnItsOwnThreshold(t *testing.T) {
	c := issueClassifier(fixedProvider{out: `{"bucket":"open","action":"awaiting_others","priority":"neutral","companion":"waiting on upstream fix","emoji":"⏳"}`})

	// The defaults are 40 days for PRs and 120 for issues, so 60 days of quiet is
	// stale for a PR but not for an issue.
	got := c.classifyIssue(context.Background(), openIssue(2, issueRefNow.Add(-60*24*time.Hour)))
	if got.Bucket != model.IssueBucketOpen {
		t.Errorf("bucket = %q at 60 days quiet, want open — the issue threshold is longer than the PR one (TDD 8.5)", got.Bucket)
	}

	got = c.classifyIssue(context.Background(), openIssue(2, issueRefNow.Add(-200*24*time.Hour)))
	if got.Bucket != model.IssueBucketStale {
		t.Errorf("bucket = %q at 200 days quiet, want stale (TDD 8.5)", got.Bucket)
	}
	if got.Action != "" {
		t.Errorf("action = %q, want empty on a stale issue", got.Action)
	}
}

// TestZeroCommentIssueStillAgesIntoStale proves the comment gate does not exempt
// an issue from the age threshold: a never-engaged, long-quiet issue is stale
// even though no model was consulted (TDD 8.3 + 8.5 together).
func TestZeroCommentIssueStillAgesIntoStale(t *testing.T) {
	c := issueClassifier(failingProvider{})

	got := c.classifyIssue(context.Background(), openIssue(0, issueRefNow.Add(-300*24*time.Hour)))

	if got.Bucket != model.IssueBucketStale {
		t.Errorf("bucket = %q, want stale for a 300-day-quiet zero-comment issue", got.Bucket)
	}
}

// TestIssueOperatorStaleWins covers the operator override on issues (TDD 8.7
// operator-set state), which short-circuits inference exactly as it does for PRs.
func TestIssueOperatorStaleWins(t *testing.T) {
	c := issueClassifier(fixedProvider{out: `{"bucket":"open","action":"awaiting_response","priority":"neutral","companion":"maintainer asked a question","emoji":"❓"}`})

	iss := openIssue(3, issueRefNow.Add(-time.Hour))
	iss.OperatorStale = true

	got := c.classifyIssue(context.Background(), iss)

	if got.Bucket != model.IssueBucketStale {
		t.Errorf("bucket = %q, want stale from the operator override", got.Bucket)
	}
	if got.Action != "" {
		t.Errorf("action = %q, want empty on a stale issue", got.Action)
	}
}

// TestClosedIssueWithConversationGetsNote is a regression test: a closed issue is
// still worth a note when it had conversation, and the note must actually land.
// The first implementation offered "closed" as a proposable bucket while offering
// no close-reason vocabulary, so every response proposing it was rejected and the
// note silently degraded to nothing on every settled row.
func TestClosedIssueWithConversationGetsNote(t *testing.T) {
	c := issueClassifier(fixedProvider{out: `{"bucket":"closed","priority":"neutral","companion":"implemented upstream and released","emoji":"✅"}`})

	iss := openIssue(2, issueRefNow.Add(-30*24*time.Hour))
	iss.GitHubClosed = true
	iss.CloseReason = model.IssueCloseReasonCompleted

	got := c.classifyIssue(context.Background(), iss)

	if got.Companion != "implemented upstream and released" {
		t.Errorf("companion = %q, want the inferred note on a discussed closed issue", got.Companion)
	}
	if got.Emoji != "✅" {
		t.Errorf("emoji = %q, want the inferred glyph", got.Emoji)
	}
	// The hard facts still win.
	if got.Bucket != model.IssueBucketClosed || got.CloseReason != model.IssueCloseReasonCompleted {
		t.Errorf("bucket/reason = %q/%q, want closed/completed (TDD 8.2)", got.Bucket, got.CloseReason)
	}
}

// TestClosedIssueRejectsInventedCloseReason proves the reason stays a hard fact:
// a model that supplies one anyway is rejected, so it can never overwrite
// GitHub's state_reason.
func TestClosedIssueRejectsInventedCloseReason(t *testing.T) {
	c := issueClassifier(fixedProvider{out: `{"bucket":"closed","close_reason":"duplicate","priority":"neutral","companion":"looks like a duplicate","emoji":"♻️"}`})

	iss := openIssue(2, issueRefNow.Add(-30*24*time.Hour))
	iss.GitHubClosed = true
	iss.CloseReason = model.IssueCloseReasonCompleted

	got := c.classifyIssue(context.Background(), iss)

	if got.CloseReason != model.IssueCloseReasonCompleted {
		t.Errorf("close_reason = %q, want completed — GitHub's state_reason is authoritative (TDD 8.2)", got.CloseReason)
	}
	if got.Companion != "" {
		t.Errorf("companion = %q, want empty — the response was rejected", got.Companion)
	}
}

// TestClassifyIssuesPreservesOrder proves the fan-out returns results in input
// order, as ClassifyAll does for PRs.
func TestClassifyIssuesPreservesOrder(t *testing.T) {
	c := issueClassifier(failingProvider{})

	in := make([]model.Issue, 5)
	for i := range in {
		in[i] = openIssue(0, issueRefNow.Add(-time.Hour))
		in[i].Number = i + 1
	}

	got := c.ClassifyIssues(context.Background(), in)

	if len(got) != len(in) {
		t.Fatalf("got %d results, want %d", len(got), len(in))
	}
	for i := range got {
		if got[i].Number != in[i].Number {
			t.Errorf("result %d has number %d, want %d — order not preserved", i, got[i].Number, in[i].Number)
		}
	}
}
