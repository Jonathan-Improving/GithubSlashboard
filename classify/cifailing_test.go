package classify

import (
	"context"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// ciNow is a fixed reference time; fixtures below sit well inside the stale
// threshold so age-staleness never confounds an action assertion.
var ciNow = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// TestCIFailingFlaggedForSubmitter covers TDD 4.10: the flag comes from GitHub's
// check-run conclusions, not from inference.
func TestCIFailingFlaggedForSubmitter(t *testing.T) {
	// The model says nothing about CI; the flag must still be set.
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"⏳"}`, ciNow)
	pr := model.PR{
		Repo: "o/n", Number: 1, GitHubState: model.GitHubStateOpen,
		Role: model.RoleSubmitter, CIFailing: true,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}

	got := c.classifyOne(context.Background(), pr)

	if !got.CIFailing {
		t.Error("ci_failing = false, want true for a submitter PR with a red build (TDD 4.10)")
	}
}

// TestCIFailingNotFlaggedForReviewer covers TDD 4.10's scoping: another author's
// broken build is not the operator's to fix.
func TestCIFailingNotFlaggedForReviewer(t *testing.T) {
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"⏳"}`, ciNow)
	pr := model.PR{
		Repo: "o/n", Number: 2, GitHubState: model.GitHubStateOpen,
		Role: model.RoleReviewer, CIFailing: true,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}

	got := c.classifyOne(context.Background(), pr)

	if got.CIFailing {
		t.Error("ci_failing = true on a reviewer PR; the flag is submitter-scoped (TDD 4.10)")
	}
}

// TestCIFailingClearedOnFloors covers TDD 4.10: a settled PR's check state is moot.
func TestCIFailingClearedOnFloors(t *testing.T) {
	c := testClassifier(`{"bucket":"merged","priority":"neutral","companion":"shipped last week","emoji":"📦"}`, ciNow)
	merged := model.PR{
		Repo: "o/n", Number: 3, GitHubState: model.GitHubStateMerged,
		Role: model.RoleSubmitter, CIFailing: true,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}
	if got := c.classifyOne(context.Background(), merged); got.CIFailing {
		t.Error("ci_failing = true on a merged PR, want cleared (TDD 4.10)")
	}

	c = testClassifier(`{"bucket":"closed","close_reason":"cancelled","priority":"neutral","companion":"abandoned this approach","emoji":"🗑️"}`, ciNow)
	closed := model.PR{
		Repo: "o/n", Number: 4, GitHubState: model.GitHubStateClosed,
		Role: model.RoleSubmitter, CIFailing: true,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}
	if got := c.classifyOne(context.Background(), closed); got.CIFailing {
		t.Error("ci_failing = true on a closed PR, want cleared (TDD 4.10)")
	}
}

// TestCIFailingClearedOnStale is a regression test from live data: a PR that aged
// into the stale bucket was still carrying ci_failing, which contradicts the
// bucket — stale means nothing further is expected, so a red build is not work
// owed (TDD 4.10). Covers all three routes into stale.
func TestCIFailingClearedOnStale(t *testing.T) {
	longAgo := ciNow.Add(-200 * 24 * time.Hour)

	// Route 1: the age threshold.
	c := testClassifier(`{"bucket":"open","action":"author_active","priority":"neutral","companion":"was iterating long ago","emoji":"🔧"}`, ciNow)
	aged := model.PR{
		Repo: "o/n", Number: 10, GitHubState: model.GitHubStateOpen,
		Role: model.RoleSubmitter, CIFailing: true,
		Created: longAgo, LastActivity: longAgo,
	}
	got := c.classifyOne(context.Background(), aged)
	if got.Bucket != model.BucketStale {
		t.Fatalf("bucket = %q, want stale from the age threshold", got.Bucket)
	}
	if got.CIFailing {
		t.Error("ci_failing = true on an age-stale PR, want cleared (TDD 4.10)")
	}

	// Route 2: the inferred tombstone.
	c = testClassifier(`{"bucket":"stale","priority":"neutral","companion":"abandoned this approach","emoji":"☠"}`, ciNow)
	tombstoned := model.PR{
		Repo: "o/n", Number: 11, GitHubState: model.GitHubStateOpen,
		Role: model.RoleSubmitter, CIFailing: true,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}
	got = c.classifyOne(context.Background(), tombstoned)
	if got.Bucket != model.BucketStale {
		t.Fatalf("bucket = %q, want stale from the inferred tombstone", got.Bucket)
	}
	if got.CIFailing {
		t.Error("ci_failing = true on an inferred-stale PR, want cleared (TDD 4.10)")
	}

	// Route 3: the operator override.
	c = testClassifier(`{"bucket":"open","action":"author_active","priority":"neutral","companion":"still iterating here","emoji":"🔧"}`, ciNow)
	overridden := model.PR{
		Repo: "o/n", Number: 12, GitHubState: model.GitHubStateOpen,
		Role: model.RoleSubmitter, CIFailing: true, OperatorStale: true,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}
	got = c.classifyOne(context.Background(), overridden)
	if got.Bucket != model.BucketStale {
		t.Fatalf("bucket = %q, want stale from the operator override", got.Bucket)
	}
	if got.CIFailing {
		t.Error("ci_failing = true on an operator-stale PR, want cleared (TDD 4.10)")
	}
}

// not touch Action, so whatever the trail implied still stands. Were CI routed
// through Action instead, this inferred disposition would be destroyed.
// TestCIFailingDoesNotShadowInferredAction is the heart of TDD 4.11: the flag must
// not touch Action, so whatever the trail implied still stands. Were CI routed
// through Action instead, this inferred disposition would be destroyed.
func TestCIFailingDoesNotShadowInferredAction(t *testing.T) {
	c := testClassifier(`{"bucket":"open","action":"blocked_external","priority":"neutral","companion":"waiting on upstream release","emoji":"⏳"}`, ciNow)
	pr := model.PR{
		Repo: "o/n", Number: 5, GitHubState: model.GitHubStateOpen,
		Role: model.RoleSubmitter, CIFailing: true,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}

	got := c.classifyOne(context.Background(), pr)

	if got.Action != model.ActionBlockedExternal {
		t.Errorf("action = %q, want blocked_external preserved — the CI flag must not overwrite it (TDD 4.11)", got.Action)
	}
	if !got.CIFailing {
		t.Error("ci_failing = false, want both facts recorded (TDD 4.11)")
	}
	if got.Companion != "waiting on upstream release" {
		t.Errorf("companion = %q, want the inferred note preserved", got.Companion)
	}
}

// TestCIFailingCoexistsWithReviewFeedback covers TDD 4.11 against the deterministic
// action override too, not just an inferred one: a reviewer's outstanding request
// and a red build are both the operator's, and both must survive.
func TestCIFailingCoexistsWithReviewFeedback(t *testing.T) {
	c := testClassifier(`{"bucket":"open","action":"merge_ready","priority":"neutral","companion":"approved and ready","emoji":"✅"}`, ciNow)
	pr := model.PR{
		Repo: "o/n", Number: 6, GitHubState: model.GitHubStateOpen,
		Role: model.RoleSubmitter, CIFailing: true, UnresolvedThreads: 3,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}

	got := c.classifyOne(context.Background(), pr)

	if got.Action != model.ActionReviewFeedback {
		t.Errorf("action = %q, want review_feedback from the deterministic override (TDD 4.9)", got.Action)
	}
	if !got.CIFailing {
		t.Error("ci_failing = false; the review-feedback override must not clear it (TDD 4.11)")
	}
}

// TestCIFailingCoexistsWithConflict proves the flag also survives the conflicted
// override, which is the strongest action override there is.
func TestCIFailingCoexistsWithConflict(t *testing.T) {
	c := testClassifier(`{"bucket":"open","action":"author_active","priority":"neutral","companion":"iterating on the branch","emoji":"🔧"}`, ciNow)
	no := false
	pr := model.PR{
		Repo: "o/n", Number: 7, GitHubState: model.GitHubStateOpen,
		Role: model.RoleSubmitter, CIFailing: true, Mergeable: &no,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}

	got := c.classifyOne(context.Background(), pr)

	if got.Action != model.ActionConflicted {
		t.Errorf("action = %q, want conflicted (TDD 4.7)", got.Action)
	}
	if !got.CIFailing {
		t.Error("ci_failing = false; the conflicted override must not clear it (TDD 4.11)")
	}
}

// TestCIFailingSurvivesUnverified proves the flag reaches the reader even when the
// model verdict could not be obtained — it is a hard fact, independent of judgment.
func TestCIFailingSurvivesUnverified(t *testing.T) {
	c := testClassifier(`not json at all`, ciNow)
	pr := model.PR{
		Repo: "o/n", Number: 8, GitHubState: model.GitHubStateOpen,
		Role: model.RoleSubmitter, CIFailing: true,
		Created: ciNow.Add(-24 * time.Hour), LastActivity: ciNow.Add(-time.Hour),
	}

	got := c.classifyOne(context.Background(), pr)

	if !got.Unverified {
		t.Fatal("expected the row to be unverified")
	}
	if !got.CIFailing {
		t.Error("ci_failing = false on an unverified row; it is a fact, not a judgment (TDD 4.10)")
	}
}
