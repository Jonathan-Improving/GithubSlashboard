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

// psNow is a fixed reference time; fixtures sit well inside every stale
// threshold so age-staleness never confounds these assertions.
var psNow = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

const psGoodJSON = `{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"⏳"}`

// classifierWithFallback builds a Classifier whose primary and fallback are
// both explicit, for tests that need to exercise the fallback path directly
// (testClassifier's helper always passes a nil fallback).
func classifierWithFallback(primary, fallback provider.Provider, now time.Time) *Classifier {
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.SkipFloorNotes = false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(primary, fallback, cfg, log, func() time.Time { return now }, nil, nil)
}

// TestProviderSetPrimaryOnSuccessfulOpenJudgment covers TDD 6.16: a normal
// successful primary judgment records "primary".
func TestProviderSetPrimaryOnSuccessfulOpenJudgment(t *testing.T) {
	c := testClassifier(psGoodJSON, psNow)
	pr := model.PR{
		Repo: "o/n", Number: 1, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: psNow.Add(-24 * time.Hour), LastActivity: psNow.Add(-time.Hour),
	}
	got := c.classifyOne(context.Background(), pr)
	if got.Provider != model.ProviderSourcePrimary {
		t.Errorf("provider = %q, want primary", got.Provider)
	}
}

// TestProviderSetFallbackWhenPrimaryExhausted covers TDD 6.13/6.16: when the
// primary's retry budget is exhausted and a fallback is configured and
// succeeds, the row records "fallback".
func TestProviderSetFallbackWhenPrimaryExhausted(t *testing.T) {
	primary := failingProvider{}
	fallback := fixedProvider{out: psGoodJSON}
	c := classifierWithFallback(primary, fallback, psNow)
	pr := model.PR{
		Repo: "o/n", Number: 1, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: psNow.Add(-24 * time.Hour), LastActivity: psNow.Add(-time.Hour),
	}
	got := c.classifyOne(context.Background(), pr)
	if got.Unverified {
		t.Fatalf("should have recovered via fallback, got unverified")
	}
	if got.Provider != model.ProviderSourceFallback {
		t.Errorf("provider = %q, want fallback", got.Provider)
	}
}

// TestProviderDefaultsPrimaryWhenUnverified covers TDD 6.16: when neither
// provider produces a usable verdict, Provider defaults deterministically to
// primary rather than being left blank or claiming a fallback rescue that did
// not happen.
func TestProviderDefaultsPrimaryWhenUnverified(t *testing.T) {
	primary := failingProvider{}
	fallback := failingProvider{}
	c := classifierWithFallback(primary, fallback, psNow)
	pr := model.PR{
		Repo: "o/n", Number: 1, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: psNow.Add(-24 * time.Hour), LastActivity: psNow.Add(-time.Hour),
	}
	got := c.classifyOne(context.Background(), pr)
	if !got.Unverified {
		t.Fatalf("both providers should have exhausted, want unverified")
	}
	if got.Provider != model.ProviderSourcePrimary {
		t.Errorf("provider = %q, want the deterministic primary default on an unverified row", got.Provider)
	}
}

// TestProviderCarriedForwardWhenUnchanged covers TDD 6.16 + 4.13: a row
// skipped via the fingerprint short-circuit keeps whatever provider produced
// its last real judgment, rather than being re-stamped.
func TestProviderCarriedForwardWhenUnchanged(t *testing.T) {
	counting := &countingProvider{out: psGoodJSON}
	c := testClassifier("", psNow)
	c.prov = counting

	pr := model.PR{
		Repo: "o/n", Number: 1, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: psNow.Add(-24 * time.Hour), LastActivity: psNow.Add(-time.Hour),
		Events: []model.Event{{Timestamp: psNow.Add(-time.Hour), Kind: model.EventComment, Text: "hi"}},
	}
	old := c.classifyOne(context.Background(), pr)
	if old.Provider != model.ProviderSourceFallback && old.Provider != model.ProviderSourcePrimary {
		t.Fatalf("precondition: first judgment should have set a provider, got %q", old.Provider)
	}
	// Force a distinguishable provenance on the "prior" record to prove it is
	// actually being read back, not coincidentally re-derived.
	old.Provider = model.ProviderSourceFallback
	old.InputFingerprint = fingerprint(pr)
	c.prior = map[string]model.PR{old.Key(): old}

	callsBefore := counting.calls
	got := c.classifyOne(context.Background(), pr)
	if counting.calls != callsBefore {
		t.Fatalf("provider was called on an unchanged PR; fingerprint skip did not fire (TDD 4.13)")
	}
	if got.Provider != model.ProviderSourceFallback {
		t.Errorf("provider = %q, want carried forward as fallback from the prior record", got.Provider)
	}
}

// TestProviderDefaultsPrimaryOnSkippedFloorNotes covers TDD 6.16: a floor row
// with SkipFloorNotes never reaches the provider, so Provider takes the same
// deterministic default as Priority/CloseReason on that path.
func TestProviderDefaultsPrimaryOnSkippedFloorNotes(t *testing.T) {
	c := testClassifier(psGoodJSON, psNow)
	c.cfg.SkipFloorNotes = true
	pr := model.PR{Repo: "o/n", Number: 1, GitHubState: model.GitHubStateMerged, Role: model.RoleSubmitter}
	got := c.classifyOne(context.Background(), pr)
	if got.Provider != model.ProviderSourcePrimary {
		t.Errorf("provider = %q, want the deterministic primary default when floor notes are skipped", got.Provider)
	}
}
