package classify

import (
	"context"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// reviewerAwaitingReviewResp is a model verdict that infers awaiting_review — the
// value that, left unchecked on a reviewer PR with no live request, wrongly
// placed it in "Awaiting Our Action" (the regression 4.8a guards).
const reviewerAwaitingReviewResp = `{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"one unresolved thread from operator after approvals","emoji":"🧵"}`

// openPR builds a genuinely-open PR anchored at now, so a zero Created/
// LastActivity does not trip the age-threshold stale path and mask what these
// tests actually exercise.
func openPR(number int, role model.Role, requested bool, now time.Time) model.PR {
	return model.PR{
		Repo: "o/n", Number: number, GitHubState: model.GitHubStateOpen,
		Role: role, ReviewRequested: requested,
		Created: now, LastActivity: now,
		Events: []model.Event{{Timestamp: now, Kind: model.EventComment, Text: "activity"}},
	}
}

// TestReviewerInferredAwaitingReviewDemotedWithoutRequest covers TDD 4.8a: a
// reviewer PR the model calls awaiting_review, but which GitHub is not asking us
// to review (no pending request), must not sit in our court — its action is
// demoted to author_active so it renders under "Open — Review Submitted".
func TestReviewerInferredAwaitingReviewDemotedWithoutRequest(t *testing.T) {
	now := time.Now()
	c := testClassifier(reviewerAwaitingReviewResp, now)
	got := c.classifyOne(context.Background(), openPR(76, model.RoleReviewer, false, now))
	if got.Bucket != model.BucketOpen {
		t.Fatalf("precondition: PR should be open, got bucket %q", got.Bucket)
	}
	if got.Action == model.ActionAwaitingReview {
		t.Errorf("reviewer PR with no live request must not stay awaiting_review (TDD 4.8a); got %q", got.Action)
	}
	if got.Action != model.ActionAuthorActive {
		t.Errorf("expected demotion to author_active, got %q", got.Action)
	}
	// The model's note/emoji must be preserved — only the action is corrected.
	if got.Companion == "" || got.Emoji != "🧵" {
		t.Errorf("model note/emoji should be preserved, got companion=%q emoji=%q", got.Companion, got.Emoji)
	}
}

// TestReviewerAwaitingReviewKeptWithLiveRequest is the other half of 4.8a/4.8: a
// reviewer PR that DOES carry a live pending review request stays
// awaiting_review (in our court), assigned deterministically from the fact —
// even against a model verdict of changes_requested.
func TestReviewerAwaitingReviewKeptWithLiveRequest(t *testing.T) {
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"changes_requested","priority":"neutral","companion":"please take a look now","emoji":"👀"}`, now)
	got := c.classifyOne(context.Background(), openPR(77, model.RoleReviewer, true, now))
	if got.Action != model.ActionAwaitingReview {
		t.Errorf("a live pending request must place the PR in our court (TDD 4.8); got %q", got.Action)
	}
}

// TestReviewerUnverifiedNotDefaultedIntoOurCourt covers the unverified path of
// 4.8a: when judgment is unavailable, a reviewer PR with no live request must
// not fall back into awaiting_review (our court).
func TestReviewerUnverifiedNotDefaultedIntoOurCourt(t *testing.T) {
	now := time.Now()
	// An empty companion fails the word-bound validator, driving unverified.
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"","emoji":""}`, now)
	got := c.classifyOne(context.Background(), openPR(78, model.RoleReviewer, false, now))
	if !got.Unverified {
		t.Fatalf("expected unverified fallback, got verified action=%q", got.Action)
	}
	if got.Action == model.ActionAwaitingReview {
		t.Errorf("unverified reviewer PR with no live request must not default into our court (TDD 4.8a); got %q", got.Action)
	}
}

// TestSubmitterAwaitingReviewUnaffected guards the scope: the demotion is
// reviewer-only. A submitter PR the model calls awaiting_review is untouched.
func TestSubmitterAwaitingReviewUnaffected(t *testing.T) {
	now := time.Now()
	c := testClassifier(reviewerAwaitingReviewResp, now)
	got := c.classifyOne(context.Background(), openPR(79, model.RoleSubmitter, false, now))
	if got.Bucket != model.BucketOpen {
		t.Fatalf("precondition: PR should be open, got bucket %q", got.Bucket)
	}
	if got.Action != model.ActionAwaitingReview {
		t.Errorf("submitter awaiting_review must be unaffected by the reviewer demotion, got %q", got.Action)
	}
}
