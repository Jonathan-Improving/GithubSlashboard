package classify

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/config"
	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// fixedProvider always returns the same raw output.
type fixedProvider struct{ out string }

func (f fixedProvider) Name() string { return "fixed" }
func (f fixedProvider) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	return f.out, nil
}
func (f fixedProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	return f.out, nil
}

func testClassifier(out string, now time.Time) *Classifier {
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.ClassifyWorkers = 2
	// Tests that exercise per-row inference on floor PRs need the full path.
	cfg.SkipFloorNotes = false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// nil prior: every existing test's PR is first-seen, so it must always
	// reach the provider (TDD 4.14) regardless of the new skip behavior.
	return New(fixedProvider{out: out}, nil, cfg, log, func() time.Time { return now }, nil, nil)
}

// countingProvider records how many times it was invoked.
type countingProvider struct {
	out   string
	calls int
}

func (c *countingProvider) Name() string { return "counting" }
func (c *countingProvider) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	c.calls++
	return c.out, nil
}
func (c *countingProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	c.calls++
	return c.out, nil
}

func TestMergedFloorImmutable(t *testing.T) {
	// Provider tries to say "open"; the merged floor must win (TDD 4.1).
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"note about the thing","emoji":"✅"}`, time.Now())
	pr := model.PR{Repo: "o/n", Number: 1, GitHubState: model.GitHubStateMerged, Role: model.RoleSubmitter}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketMerged {
		t.Errorf("merged floor violated: got %q (TDD 4.1)", got.Bucket)
	}
	if got.Action != "" {
		t.Errorf("merged PR should carry no action, got %q", got.Action)
	}
}

func TestClosedFloorImmutableWithReason(t *testing.T) {
	// Provider proposes open bucket + a close_reason; floor keeps it closed and
	// only the reason is honored (TDD 4.2, 4.3).
	c := testClassifier(`{"bucket":"closed","close_reason":"superseded","priority":"neutral","companion":"replaced by newer PR","emoji":"✅"}`, time.Now())
	pr := model.PR{Repo: "o/n", Number: 2, GitHubState: model.GitHubStateClosed, Role: model.RoleSubmitter}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketClosed {
		t.Errorf("closed floor violated: got %q (TDD 4.2)", got.Bucket)
	}
	if got.CloseReason != model.CloseReasonSuperseded {
		t.Errorf("close_reason = %q, want superseded (TDD 4.3)", got.CloseReason)
	}
}

func TestOperatorStaleOverride(t *testing.T) {
	// Provider says open+merge_ready; operator override forces stale (TDD 5.1).
	c := testClassifier(`{"bucket":"open","action":"merge_ready","priority":"elevated","companion":"ready to go now","emoji":"✅"}`, time.Now())
	pr := model.PR{Repo: "o/n", Number: 3, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter, OperatorStale: true}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketStale {
		t.Errorf("operator override ignored: got %q (TDD 5.1)", got.Bucket)
	}
}

func TestAgeThresholdStale(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"✅"}`, now)
	// Last activity is well beyond the default 60-day threshold.
	pr := model.PR{Repo: "o/n", Number: 4, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer,
		Created: now.Add(-200 * 24 * time.Hour), LastActivity: now.Add(-200 * 24 * time.Hour)}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketStale {
		t.Errorf("age threshold not applied: got %q (TDD 5.2)", got.Bucket)
	}
}

func TestInferredStale(t *testing.T) {
	now := time.Now()
	c := testClassifier(`{"bucket":"stale","priority":"neutral","companion":"tombstone left open intentionally","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 5, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketStale {
		t.Errorf("inferred stale not honored: got %q (TDD 5.3)", got.Bucket)
	}
}

func TestOpenDisposition(t *testing.T) {
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"blocked_external","priority":"elevated","companion":"blocked on upstream PR","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 6, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketOpen || got.Action != model.ActionBlockedExternal {
		t.Errorf("open disposition wrong: %q/%q (TDD 4.4)", got.Bucket, got.Action)
	}
	if got.Priority != model.PriorityElevated {
		t.Errorf("priority = %q, want elevated (TDD 4.6)", got.Priority)
	}
}

func TestUnverifiedFallback(t *testing.T) {
	now := time.Now()
	c := testClassifier("garbage not json", now)
	pr := model.PR{Repo: "o/n", Number: 7, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now}
	got := c.classifyOne(context.Background(), pr)
	if !got.Unverified {
		t.Errorf("bad provider output should mark row unverified (TDD 6.4)")
	}
}

func TestSkipFloorNotesAvoidsProviderForFloors(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.ClassifyWorkers = 2
	cfg.SkipFloorNotes = true // fast path
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cp := &countingProvider{out: `{"bucket":"merged","priority":"neutral","companion":"three word note","emoji":"✅"}`}
	c := New(cp, nil, cfg, log, func() time.Time { return now }, nil, nil)

	prs := []model.PR{
		{Repo: "o/n", Number: 1, GitHubState: model.GitHubStateMerged, Role: model.RoleSubmitter},
		{Repo: "o/n", Number: 2, GitHubState: model.GitHubStateClosed, Role: model.RoleSubmitter},
	}
	out := c.ClassifyAll(context.Background(), prs)

	if cp.calls != 0 {
		t.Errorf("provider called %d times for floor PRs, want 0 when SkipFloorNotes", cp.calls)
	}
	if out[0].Bucket != model.BucketMerged {
		t.Errorf("merged floor lost: %q", out[0].Bucket)
	}
	if out[1].Bucket != model.BucketClosed || !out[1].CloseReason.Valid() {
		t.Errorf("closed floor/sub-reason wrong: %q/%q", out[1].Bucket, out[1].CloseReason)
	}
	if out[0].Unverified || out[1].Unverified {
		t.Error("floor rows must not be marked unverified when skipped")
	}
}

func TestSkipFloorNotesStillClassifiesOpen(t *testing.T) {
	now := time.Now()
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.ClassifyWorkers = 2
	cfg.SkipFloorNotes = true
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cp := &countingProvider{out: `{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"✅"}`}
	c := New(cp, nil, cfg, log, func() time.Time { return now }, nil, nil)

	pr := model.PR{Repo: "o/n", Number: 1, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer, Created: now, LastActivity: now}
	got := c.classifyOne(context.Background(), pr)
	if cp.calls != 1 {
		t.Errorf("provider called %d times for an open PR, want 1 (open still needs the model)", cp.calls)
	}
	if got.Bucket != model.BucketOpen || got.Action != model.ActionAwaitingReview {
		t.Errorf("open PR misclassified under fast path: %q/%q", got.Bucket, got.Action)
	}
}

// falseBool / trueBool are pointer helpers for the mergeable flag.
func falseBool() *bool { b := false; return &b }
func trueBool() *bool  { b := true; return &b }

func TestReviewFeedbackSubmitterOverride(t *testing.T) {
	// The valkey-glide-ruby#295 case: one reviewer approved while another left
	// line comments that are still open, so the model inferred "conditionally
	// approved / awaiting_review" — which reads as the ball being in someone
	// else's court. Unresolved threads are a hard GitHub fact and must override.
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"conditionally approved, awaiting final blessing","emoji":"⏳"}`, now)
	pr := model.PR{Repo: "o/n", Number: 295, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, UnresolvedThreads: 10}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketOpen {
		t.Errorf("PR should stay open, got %q", got.Bucket)
	}
	if got.Action != model.ActionReviewFeedback {
		t.Errorf("action = %q, want review_feedback (deterministic override)", got.Action)
	}
	// The model's nuance is preserved: only the action is overridden.
	if got.Companion == "" || got.Emoji == "" {
		t.Errorf("override must not clobber the inferred note/emoji: companion=%q emoji=%q", got.Companion, got.Emoji)
	}
}

func TestReviewFeedbackReviewerUnaffected(t *testing.T) {
	// Unresolved comments on a PR the operator only reviews are the other
	// author's to answer; the inferred action must stand.
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"author_active","priority":"neutral","companion":"author is iterating","emoji":"🔧"}`, now)
	pr := model.PR{Repo: "o/n", Number: 296, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer,
		Created: now, LastActivity: now, UnresolvedThreads: 5}
	got := c.classifyOne(context.Background(), pr)
	if got.Action == model.ActionReviewFeedback {
		t.Errorf("reviewer PR must not be marked review_feedback")
	}
}

func TestReviewFeedbackNotAppliedWhenAllResolved(t *testing.T) {
	// Zero unresolved threads: inference stands untouched.
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"merge_ready","priority":"neutral","companion":"approved and ready","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 297, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, UnresolvedThreads: 0}
	got := c.classifyOne(context.Background(), pr)
	if got.Action != model.ActionMergeReady {
		t.Errorf("action = %q, want the inferred merge_ready to stand", got.Action)
	}
}

func TestConflictWinsOverReviewFeedback(t *testing.T) {
	// A merge conflict is the harder blocker: it must be resolved before merge
	// is even possible, so it wins when both hold.
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting","emoji":"⏳"}`, now)
	pr := model.PR{Repo: "o/n", Number: 298, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, UnresolvedThreads: 3, Mergeable: falseBool()}
	got := c.classifyOne(context.Background(), pr)
	if got.Action != model.ActionConflicted {
		t.Errorf("action = %q, want conflicted to win over review_feedback", got.Action)
	}
}

func TestReviewFeedbackAppliedOnUnverifiedPath(t *testing.T) {
	// Hard facts must surface even when the model verdict could not be trusted.
	now := time.Now()
	c := testClassifier(`not json at all`, now)
	pr := model.PR{Repo: "o/n", Number: 299, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, UnresolvedThreads: 2}
	got := c.classifyOne(context.Background(), pr)
	if !got.Unverified {
		t.Fatalf("expected the row to be unverified")
	}
	if got.Action != model.ActionReviewFeedback {
		t.Errorf("action = %q, want review_feedback on the unverified path", got.Action)
	}
}

func TestConflictedSubmitterOverride(t *testing.T) {
	// Provider infers a perfectly open PR (merge_ready), but GitHub reports it
	// conflicts with base; the deterministic conflicted action must override the
	// inferred one for a submitter PR.
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"merge_ready","priority":"neutral","companion":"approved and ready","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 10, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, Mergeable: falseBool()}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketOpen {
		t.Errorf("conflicted PR should stay open, got %q", got.Bucket)
	}
	if got.Action != model.ActionConflicted {
		t.Errorf("action = %q, want conflicted (deterministic override)", got.Action)
	}
}

func TestConflictedReviewerUnaffected(t *testing.T) {
	// A conflicted PR the operator only reviews is not the operator's action;
	// the inferred action must stand.
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 11, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer,
		Created: now, LastActivity: now, Mergeable: falseBool()}
	got := c.classifyOne(context.Background(), pr)
	if got.Action == model.ActionConflicted {
		t.Errorf("reviewer PR must not be marked conflicted")
	}
}

func TestConflictedDoesNotOverrideStale(t *testing.T) {
	// A stale PR's conflict is moot; stale wins.
	now := time.Now()
	c := testClassifier(`{"bucket":"stale","priority":"neutral","companion":"tombstone left open intentionally","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 12, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, Mergeable: falseBool()}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketStale {
		t.Errorf("stale should win over conflicted, got bucket %q", got.Bucket)
	}
	if got.Action == model.ActionConflicted {
		t.Errorf("stale PR should carry no action, got conflicted")
	}
}

func TestMergeableTrueKeepsInferredAction(t *testing.T) {
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"merge_ready","priority":"neutral","companion":"approved and ready","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 13, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, Mergeable: trueBool()}
	got := c.classifyOne(context.Background(), pr)
	if got.Action != model.ActionMergeReady {
		t.Errorf("mergeable PR should keep inferred action, got %q", got.Action)
	}
}

func TestConflictedSurfacesWhenUnverified(t *testing.T) {
	// The model fails to produce a verifiable verdict, but GitHub reports a
	// conflict: the row is unverified yet still shows conflicted rather than the
	// meaningless awaiting_review raw-flag fallback.
	now := time.Now()
	c := testClassifier("garbage not json", now)
	pr := model.PR{Repo: "o/n", Number: 14, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, Mergeable: falseBool()}
	got := c.classifyOne(context.Background(), pr)
	if !got.Unverified {
		t.Errorf("expected unverified row (TDD 6.4)")
	}
	if got.Action != model.ActionConflicted {
		t.Errorf("action = %q, want conflicted even when unverified", got.Action)
	}
}

// TestMergeBlockedSubmitterOverride covers TDD 4.7a: GitHub's own
// mergeable_state "blocked" overrides a model-inferred merge_ready — the
// concrete defect (a green ✅ row on a PR GitHub itself reports as blocked)
// that motivated this rubric.
func TestMergeBlockedSubmitterOverride(t *testing.T) {
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"merge_ready","priority":"neutral","companion":"approved and CI passing","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 20, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, Mergeable: trueBool(), MergeableState: "blocked"}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketOpen {
		t.Errorf("merge-blocked PR should stay open, got %q", got.Bucket)
	}
	if got.Action != model.ActionMergeBlocked {
		t.Errorf("action = %q, want merge_blocked (deterministic override)", got.Action)
	}
}

func TestMergeBlockedReviewerUnaffected(t *testing.T) {
	// A merge-blocked PR the operator only reviews is not the operator's to
	// clear; the inferred action must stand.
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 21, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer,
		Created: now, LastActivity: now, Mergeable: trueBool(), MergeableState: "blocked"}
	got := c.classifyOne(context.Background(), pr)
	if got.Action == model.ActionMergeBlocked {
		t.Errorf("reviewer PR must not be marked merge_blocked")
	}
}

func TestMergeBlockedDoesNotOverrideStale(t *testing.T) {
	now := time.Now()
	c := testClassifier(`{"bucket":"stale","priority":"neutral","companion":"tombstone left open intentionally","emoji":"✅"}`, now)
	pr := model.PR{Repo: "o/n", Number: 22, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, Mergeable: trueBool(), MergeableState: "blocked"}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketStale {
		t.Errorf("stale should win over merge_blocked, got bucket %q", got.Bucket)
	}
	if got.Action == model.ActionMergeBlocked {
		t.Errorf("stale PR should carry no action, got merge_blocked")
	}
}

func TestMergeBlockedSurfacesWhenUnverified(t *testing.T) {
	now := time.Now()
	c := testClassifier("garbage not json", now)
	pr := model.PR{Repo: "o/n", Number: 23, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, Mergeable: trueBool(), MergeableState: "blocked"}
	got := c.classifyOne(context.Background(), pr)
	if !got.Unverified {
		t.Errorf("expected unverified row (TDD 6.4)")
	}
	if got.Action != model.ActionMergeBlocked {
		t.Errorf("action = %q, want merge_blocked even when unverified", got.Action)
	}
}

func TestMergeBlockedWinsOverReviewFeedback(t *testing.T) {
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting","emoji":"⏳"}`, now)
	pr := model.PR{Repo: "o/n", Number: 24, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, UnresolvedThreads: 3, Mergeable: trueBool(), MergeableState: "blocked"}
	got := c.classifyOne(context.Background(), pr)
	if got.Action != model.ActionMergeBlocked {
		t.Errorf("action = %q, want merge_blocked to win over review_feedback", got.Action)
	}
}

func TestConflictedWinsOverMergeBlocked(t *testing.T) {
	// dirty and blocked are mutually exclusive states GitHub actually reports,
	// but if a PR were somehow both, conflicted must win as the harder blocker
	// (TDD 4.7a).
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting","emoji":"⏳"}`, now)
	pr := model.PR{Repo: "o/n", Number: 25, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: now, LastActivity: now, Mergeable: falseBool(), MergeableState: "blocked"}
	got := c.classifyOne(context.Background(), pr)
	if got.Action != model.ActionConflicted {
		t.Errorf("action = %q, want conflicted to win over merge_blocked", got.Action)
	}
}

func TestCarriedTerminalPreservesCachedNotesNoProviderCall(t *testing.T) {
	// A carried terminal record has its bucket set, a cached companion/emoji,
	// and NO event trail. classify must keep the cached verdict and must not
	// call the provider (nothing to judge). SkipFloorNotes is false (full path)
	// to prove the no-events guard — not the fast-path skip — is what protects it.
	now := time.Now()
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.ClassifyWorkers = 2
	cfg.SkipFloorNotes = false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cp := &countingProvider{out: `{"bucket":"merged","priority":"neutral","companion":"three word note","emoji":"✅"}`}
	c := New(cp, nil, cfg, log, func() time.Time { return now }, nil, nil)

	merged := model.PR{Repo: "o/n", Number: 1, GitHubState: model.GitHubStateMerged, Role: model.RoleSubmitter,
		Bucket: model.BucketMerged, Priority: model.PriorityElevated, Companion: "shipped in v2", Emoji: "🚀"}
	closed := model.PR{Repo: "o/n", Number: 2, GitHubState: model.GitHubStateClosed, Role: model.RoleSubmitter,
		Bucket: model.BucketClosed, CloseReason: model.CloseReasonSuperseded, Priority: model.PriorityNeutral,
		Companion: "replaced by #3", Emoji: "♻️"}

	out := c.ClassifyAll(context.Background(), []model.PR{merged, closed})

	if cp.calls != 0 {
		t.Errorf("provider called %d times for carried terminal PRs with no events, want 0", cp.calls)
	}
	if out[0].Companion != "shipped in v2" || out[0].Emoji != "🚀" || out[0].Priority != model.PriorityElevated {
		t.Errorf("carried merged verdict not preserved: %+v", out[0])
	}
	if out[1].Companion != "replaced by #3" || out[1].Emoji != "♻️" || out[1].CloseReason != model.CloseReasonSuperseded {
		t.Errorf("carried closed verdict not preserved: %+v", out[1])
	}
}

func TestReviewRequestedOverridesInferredAction(t *testing.T) {
	// The model reads the trail as changes_requested (we already reviewed), but
	// GitHub has a live pending review request on us: the deterministic override
	// marks it awaiting our review so it lands in "Awaiting Our Action".
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"changes_requested","priority":"neutral","companion":"reviewer requested rework","emoji":"🔄"}`, now)
	pr := model.PR{Repo: "o/n", Number: 76, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer,
		Created: now, LastActivity: now, ReviewRequested: true}
	got := c.classifyOne(context.Background(), pr)
	if got.Action != model.ActionAwaitingReview {
		t.Errorf("action = %q, want awaiting_review (pending review request is authoritative)", got.Action)
	}
}

func TestReviewRequestedNotStaleDespiteAge(t *testing.T) {
	// A pending review request keeps the PR actionable even past the stale age.
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"⏳"}`, now)
	pr := model.PR{Repo: "o/n", Number: 77, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer,
		Created: now.Add(-200 * 24 * time.Hour), LastActivity: now.Add(-200 * 24 * time.Hour), ReviewRequested: true}
	got := c.classifyOne(context.Background(), pr)
	if got.Bucket != model.BucketOpen || got.Action != model.ActionAwaitingReview {
		t.Errorf("a pending review request should stay open/awaiting despite age, got %q/%q", got.Bucket, got.Action)
	}
}

func TestReviewRequestedSurfacesWhenUnverified(t *testing.T) {
	now := time.Now()
	c := testClassifier("garbage not json", now)
	pr := model.PR{Repo: "o/n", Number: 78, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer,
		Created: now, LastActivity: now, ReviewRequested: true}
	got := c.classifyOne(context.Background(), pr)
	if !got.Unverified {
		t.Errorf("expected unverified row")
	}
	if got.Action != model.ActionAwaitingReview {
		t.Errorf("action = %q, want awaiting_review even when unverified", got.Action)
	}
}

func TestNoReviewRequestKeepsInferredReviewerAction(t *testing.T) {
	// A reviewer PR without a pending request keeps the model's action (ball on
	// the author), so it is not force-placed in Awaiting Our Action.
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"changes_requested","priority":"neutral","companion":"we requested changes","emoji":"🔄"}`, now)
	pr := model.PR{Repo: "o/n", Number: 79, GitHubState: model.GitHubStateOpen, Role: model.RoleReviewer,
		Created: now, LastActivity: now, ReviewRequested: false}
	got := c.classifyOne(context.Background(), pr)
	if got.Action != model.ActionChangesRequested {
		t.Errorf("action = %q, want changes_requested preserved when no pending request", got.Action)
	}
}

func TestClassifyAllPreservesOrder(t *testing.T) {
	now := time.Now()
	c := testClassifier(`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"✅"}`, now)
	in := []model.PR{
		{Repo: "o/n", Number: 1, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter, Created: now, LastActivity: now},
		{Repo: "o/n", Number: 2, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter, Created: now, LastActivity: now},
		{Repo: "o/n", Number: 3, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter, Created: now, LastActivity: now},
	}
	out := c.ClassifyAll(context.Background(), in)
	for i := range in {
		if out[i].Number != in[i].Number {
			t.Errorf("order not preserved at %d: got %d", i, out[i].Number)
		}
	}
}

// unchangedOpenClassifierWithPrior builds a Classifier with the given prior
// store map, backed by a countingProvider so tests can assert whether the
// provider was actually invoked (TDD 4.13, 4.14).
func unchangedOpenClassifierWithPrior(out string, now time.Time, prior map[string]model.PR) (*Classifier, *countingProvider) {
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.ClassifyWorkers = 2
	cfg.SkipFloorNotes = false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cp := &countingProvider{out: out}
	return New(cp, nil, cfg, log, func() time.Time { return now }, prior, nil), cp
}

// freshUnchangedPR and priorUnchangedPR build a matching first-run/second-run
// pair: the fresh PR carries the same deterministic inputs the prior record's
// InputFingerprint was computed from, and the prior record carries a settled,
// verified verdict a second run should be able to reuse untouched.
func priorUnchangedPR(events []model.Event, ciFailing bool, unresolved int, mergeable *bool, reviewRequested bool) model.PR {
	base := model.PR{
		Repo: "o/n", Number: 50, Role: model.RoleSubmitter,
		Events: events, CIFailing: ciFailing, UnresolvedThreads: unresolved,
		Mergeable: mergeable, ReviewRequested: reviewRequested,
	}
	fp := fingerprint(base)
	return model.PR{
		Repo: "o/n", Number: 50, Role: model.RoleSubmitter,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityElevated, Companion: "cached note from last run", Emoji: "⏳",
		Unverified: false, InputFingerprint: fp,
	}
}

func freshUnchangedPR(events []model.Event, ciFailing bool, unresolved int, mergeable *bool, reviewRequested bool) model.PR {
	return model.PR{
		Repo: "o/n", Number: 50, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: time.Now(), LastActivity: time.Now(),
		Events: events, CIFailing: ciFailing, UnresolvedThreads: unresolved,
		Mergeable: mergeable, ReviewRequested: reviewRequested,
	}
}

func TestUnchangedOpenPRSkipsProvider(t *testing.T) {
	now := time.Now()
	events := []model.Event{{Timestamp: now.Add(-time.Hour), Kind: model.EventComment, Text: "lgtm"}}
	prior := priorUnchangedPR(events, false, 0, nil, false)
	fresh := freshUnchangedPR(events, false, 0, nil, false)

	c, cp := unchangedOpenClassifierWithPrior(
		`{"bucket":"open","action":"changes_requested","priority":"neutral","companion":"provider should never be asked","emoji":"❌"}`,
		now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 0 {
		t.Errorf("provider called %d times for an unchanged PR, want 0 (TDD 4.13)", cp.calls)
	}
	if got.Action != model.ActionAwaitingReview || got.Companion != "cached note from last run" || got.Emoji != "⏳" {
		t.Errorf("cached verdict not carried forward verbatim: %+v", got)
	}
	if got.Priority != model.PriorityElevated {
		t.Errorf("priority = %q, want the cached elevated value preserved", got.Priority)
	}
	if got.InputFingerprint == "" {
		t.Errorf("fingerprint must be stamped on the carried-forward record too")
	}
}

func TestChangedLastEventStillInvokesProvider(t *testing.T) {
	now := time.Now()
	priorEvents := []model.Event{{Timestamp: now.Add(-2 * time.Hour), Kind: model.EventComment, Text: "lgtm"}}
	freshEvents := []model.Event{{Timestamp: now.Add(-time.Hour), Kind: model.EventComment, Text: "actually, one more thing"}}
	prior := priorUnchangedPR(priorEvents, false, 0, nil, false)
	fresh := freshUnchangedPR(freshEvents, false, 0, nil, false)

	c, cp := unchangedOpenClassifierWithPrior(
		`{"bucket":"open","action":"changes_requested","priority":"neutral","companion":"new comment changes things","emoji":"🔄"}`,
		now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times for a PR with a new trail event, want 1 (TDD 4.14)", cp.calls)
	}
	if got.Action != model.ActionChangesRequested {
		t.Errorf("action = %q, want the freshly judged value, not the stale cache", got.Action)
	}
}

func TestChangedCIFailingStillInvokesProvider(t *testing.T) {
	now := time.Now()
	events := []model.Event{{Timestamp: now.Add(-time.Hour), Kind: model.EventComment, Text: "lgtm"}}
	prior := priorUnchangedPR(events, false, 0, nil, false)
	fresh := freshUnchangedPR(events, true, 0, nil, false) // CI flipped to failing

	c, cp := unchangedOpenClassifierWithPrior(
		`{"bucket":"open","action":"changes_requested","priority":"neutral","companion":"ci just started failing","emoji":"🚧"}`,
		now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times when CIFailing changed, want 1 (TDD 4.14) — this is the exact csharp#514 scenario, where a check-run cycle completed without moving any other signal", cp.calls)
	}
	if !got.CIFailing {
		t.Errorf("fresh CIFailing=true must be preserved on the judged result")
	}
}

func TestChangedUnresolvedThreadsStillInvokesProvider(t *testing.T) {
	now := time.Now()
	events := []model.Event{{Timestamp: now.Add(-time.Hour), Kind: model.EventComment, Text: "lgtm"}}
	prior := priorUnchangedPR(events, false, 0, nil, false)
	fresh := freshUnchangedPR(events, false, 2, nil, false) // new unresolved threads appeared

	c, cp := unchangedOpenClassifierWithPrior(
		`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"new line comments","emoji":"💬"}`,
		now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times when UnresolvedThreads changed, want 1 (TDD 4.14)", cp.calls)
	}
	// The deterministic review-feedback override still applies on top of the
	// freshly judged result, same as any other classify run.
	if got.Action != model.ActionReviewFeedback {
		t.Errorf("action = %q, want review_feedback override applied on the fresh judgment", got.Action)
	}
}

func TestChangedMergeableStillInvokesProvider(t *testing.T) {
	now := time.Now()
	events := []model.Event{{Timestamp: now.Add(-time.Hour), Kind: model.EventComment, Text: "lgtm"}}
	prior := priorUnchangedPR(events, false, 0, trueBool(), false)
	fresh := freshUnchangedPR(events, false, 0, falseBool(), false) // base branch moved, now conflicting

	c, cp := unchangedOpenClassifierWithPrior(
		`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"now conflicts with base","emoji":"⚠️"}`,
		now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times when Mergeable changed, want 1 (TDD 4.14)", cp.calls)
	}
	if got.Action != model.ActionConflicted {
		t.Errorf("action = %q, want conflicted override applied on the fresh judgment", got.Action)
	}
}

func TestChangedReviewRequestedStillInvokesProvider(t *testing.T) {
	now := time.Now()
	events := []model.Event{{Timestamp: now.Add(-time.Hour), Kind: model.EventComment, Text: "lgtm"}}
	prior := priorUnchangedPR(events, false, 0, nil, false)
	fresh := freshUnchangedPR(events, false, 0, nil, true) // a fresh review request landed
	fresh.Role = model.RoleReviewer
	prior.Role = model.RoleReviewer

	c, cp := unchangedOpenClassifierWithPrior(
		`{"bucket":"open","action":"changes_requested","priority":"neutral","companion":"we already reviewed this","emoji":"🔄"}`,
		now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times when ReviewRequested changed, want 1 (TDD 4.14)", cp.calls)
	}
	if got.Action != model.ActionAwaitingReview {
		t.Errorf("action = %q, want awaiting_review override applied on the fresh judgment", got.Action)
	}
}

func TestFirstSeenPRAlwaysInvokesProvider(t *testing.T) {
	now := time.Now()
	fresh := freshUnchangedPR([]model.Event{{Timestamp: now, Kind: model.EventComment, Text: "first comment"}}, false, 0, nil, false)

	// Empty prior map: no record for this PR at all.
	c, cp := unchangedOpenClassifierWithPrior(
		`{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"brand new PR","emoji":"✅"}`,
		now, map[string]model.PR{})

	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times for a first-seen PR, want 1 (TDD 4.14)", cp.calls)
	}
	if got.Companion != "brand new PR" {
		t.Errorf("expected the freshly judged companion, got %q", got.Companion)
	}
}

func TestPreviouslyUnverifiedPriorAlwaysInvokesProvider(t *testing.T) {
	now := time.Now()
	events := []model.Event{{Timestamp: now.Add(-time.Hour), Kind: model.EventComment, Text: "lgtm"}}
	prior := priorUnchangedPR(events, false, 0, nil, false)
	prior.Unverified = true // last run's judgment was degraded, never a real cache
	prior.InputFingerprint = ""
	fresh := freshUnchangedPR(events, false, 0, nil, false) // identical inputs otherwise

	c, cp := unchangedOpenClassifierWithPrior(
		`{"bucket":"open","action":"merge_ready","priority":"neutral","companion":"now judged for real","emoji":"✅"}`,
		now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times when the prior record was unverified, want 1 (TDD 4.14) — an unverified result must never be cached forward", cp.calls)
	}
	if got.Action != model.ActionMergeReady {
		t.Errorf("expected the freshly judged action, got %q", got.Action)
	}
}
