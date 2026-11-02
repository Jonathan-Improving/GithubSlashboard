package classify

import (
	"context"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

const mergeableUnknownJudged = `{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"🔄"}`

func mergeabilityPR(events []model.Event, mergeable *bool, state string) model.PR {
	pr := freshUnchangedPR(events, true, 0, mergeable, false)
	pr.MergeableState = state
	pr.Created = events[0].Timestamp
	pr.LastActivity = events[len(events)-1].Timestamp
	return pr
}

func judgedPrior(pr model.PR) model.PR {
	return model.PR{
		Repo: pr.Repo, Number: pr.Number, Role: pr.Role,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityElevated, Companion: "cached note from last run", Emoji: "⏳",
		CIFailing: pr.CIFailing, LastActivity: pr.LastActivity,
		InputFingerprint: fingerprint(pr),
	}
}

func mergeableUnknownEvents(now time.Time) []model.Event {
	return []model.Event{{Timestamp: now.Add(-time.Hour), Kind: model.EventComment, Text: "lgtm"}}
}

// TDD 4.13a: a fetch where GitHub had not finished computing mergeability must
// not count as a change against a record judged with a finished reading.
func TestUnknownMergeabilityDoesNotInvokeProvider(t *testing.T) {
	now := time.Now()
	events := mergeableUnknownEvents(now)
	prior := judgedPrior(mergeabilityPR(events, trueBool(), "blocked"))
	fresh := mergeabilityPR(events, nil, mergeableStateUnknown)

	c, cp := unchangedOpenClassifierWithPrior(mergeableUnknownJudged, now, map[string]model.PR{prior.Key(): prior})
	got := c.classifyOne(context.Background(), fresh)

	if cp.calls != 0 {
		t.Errorf("provider called %d times when only mergeability went unknown, want 0 (TDD 4.13a)", cp.calls)
	}
	if got.Companion != "cached note from last run" || got.Action != model.ActionAwaitingReview {
		t.Errorf("cached verdict not carried forward: %+v", got)
	}
	if got.StatusChanged {
		t.Errorf("StatusChanged = true for a PR whose only difference is an unfinished mergeability reading")
	}
	if got.InputFingerprint != prior.InputFingerprint {
		t.Errorf("fingerprint = %q, want the prior finished-reading fingerprint %q preserved", got.InputFingerprint, prior.InputFingerprint)
	}
}

// The flap seen in production: finished, unfinished, finished again. No run in
// the sequence may reach the provider or report a change.
func TestMergeabilityFlapNeverInvokesProvider(t *testing.T) {
	now := time.Now()
	events := mergeableUnknownEvents(now)
	known := mergeabilityPR(events, trueBool(), "unstable")
	unknown := mergeabilityPR(events, nil, mergeableStateUnknown)

	record := judgedPrior(known)
	for i, fresh := range []model.PR{unknown, known, unknown, unknown, known} {
		c, cp := unchangedOpenClassifierWithPrior(mergeableUnknownJudged, now, map[string]model.PR{record.Key(): record})
		got := c.classifyOne(context.Background(), fresh)
		if cp.calls != 0 || got.StatusChanged {
			t.Fatalf("run %d: provider calls = %d, StatusChanged = %t, want 0 and false (TDD 4.13a)", i, cp.calls, got.StatusChanged)
		}
		record = got
	}
}

// TDD 4.14: a record judged while mergeability was unfinished never masks a
// finished reading that arrives later.
func TestFinishedMergeabilityAfterUnknownInvokesProvider(t *testing.T) {
	now := time.Now()
	events := mergeableUnknownEvents(now)
	prior := judgedPrior(mergeabilityPR(events, nil, mergeableStateUnknown))
	fresh := mergeabilityPR(events, trueBool(), "blocked")

	c, cp := unchangedOpenClassifierWithPrior(mergeableUnknownJudged, now, map[string]model.PR{prior.Key(): prior})
	c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times when a finished reading replaced an unfinished one, want 1 (TDD 4.14)", cp.calls)
	}
}

func TestChangedFinishedMergeabilityStateInvokesProvider(t *testing.T) {
	now := time.Now()
	events := mergeableUnknownEvents(now)
	prior := judgedPrior(mergeabilityPR(events, trueBool(), "clean"))
	fresh := mergeabilityPR(events, trueBool(), "blocked")

	c, cp := unchangedOpenClassifierWithPrior(mergeableUnknownJudged, now, map[string]model.PR{prior.Key(): prior})
	c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times when mergeable_state moved clean to blocked, want 1 (TDD 4.14)", cp.calls)
	}
}

// An unfinished mergeability reading hides nothing else: a different input still
// forces a fresh judgment.
func TestUnknownMergeabilityWithOtherInputChangeInvokesProvider(t *testing.T) {
	now := time.Now()
	priorEvents := mergeableUnknownEvents(now)
	freshEvents := []model.Event{{Timestamp: now, Kind: model.EventComment, Text: "one more thing"}}
	prior := judgedPrior(mergeabilityPR(priorEvents, trueBool(), "blocked"))
	fresh := mergeabilityPR(freshEvents, nil, mergeableStateUnknown)

	c, cp := unchangedOpenClassifierWithPrior(mergeableUnknownJudged, now, map[string]model.PR{prior.Key(): prior})
	c.classifyOne(context.Background(), fresh)

	if cp.calls != 1 {
		t.Errorf("provider called %d times when a new comment landed during an unknown reading, want 1 (TDD 4.14)", cp.calls)
	}
}
