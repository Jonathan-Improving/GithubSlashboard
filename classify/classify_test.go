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

func testClassifier(out string, now time.Time) *Classifier {
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.ClassifyWorkers = 2
	// Tests that exercise per-row inference on floor PRs need the full path.
	cfg.SkipFloorNotes = false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(fixedProvider{out: out}, cfg, log, func() time.Time { return now })
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
	c := New(cp, cfg, log, func() time.Time { return now })

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
	c := New(cp, cfg, log, func() time.Time { return now })

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
	c := New(cp, cfg, log, func() time.Time { return now })

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
