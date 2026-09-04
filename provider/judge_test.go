package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeProvider returns a scripted sequence of outputs/errors, one per Invoke.
type fakeProvider struct {
	outputs []string
	errs    []error
	calls   int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Invoke(ctx context.Context, req Request, correction string) (string, error) {
	i := f.calls
	f.calls++
	var out string
	var err error
	if i < len(f.outputs) {
		out = f.outputs[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return out, err
}

// Summarize is not exercised by Judge tests, but is required to satisfy
// Provider; it mirrors Invoke's scripted-output behavior for completeness.
func (f *fakeProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	return f.Invoke(ctx, Request{}, "")
}

const goodJSON = `{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"⏳"}`

func req() Request {
	return Request{Constraints: ConstraintsFrom(3, 8)}
}

func TestJudgeSucceedsFirstTry(t *testing.T) {
	f := &fakeProvider{outputs: []string{goodJSON}}
	res := Judge(context.Background(), f, nil, req(), 2, time.Second, time.Second, nil)
	if res.Unverified {
		t.Fatal("should be verified")
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", res.Attempts)
	}
	if res.FromFallback {
		t.Error("a primary success must not be marked FromFallback")
	}
}

func TestJudgeSelfCorrects(t *testing.T) {
	f := &fakeProvider{outputs: []string{`{"bucket":"nonsense"}`, goodJSON}}
	res := Judge(context.Background(), f, nil, req(), 2, time.Second, time.Second, nil)
	if res.Unverified {
		t.Fatalf("should recover on retry, err=%v", res.Err)
	}
	if res.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", res.Attempts)
	}
}

func TestJudgeUnverifiedAfterRetryCap(t *testing.T) {
	// retryCap=2 -> 3 total attempts, all bad.
	f := &fakeProvider{outputs: []string{"junk", "junk", "junk"}}
	res := Judge(context.Background(), f, nil, req(), 2, time.Second, time.Second, nil)
	if !res.Unverified {
		t.Fatal("should be unverified after exhausting retries (TDD 6.4)")
	}
	if res.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", res.Attempts)
	}
	if f.calls != 3 {
		t.Errorf("provider called %d times, want 3", f.calls)
	}
}

func TestJudgeUnverifiedOnProviderError(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeProvider{errs: []error{boom, boom, boom}}
	res := Judge(context.Background(), f, nil, req(), 2, time.Second, time.Second, nil)
	if !res.Unverified {
		t.Fatal("provider errors should yield unverified (TDD 6.5)")
	}
	// Hard invocation errors fast-fail after one retry (2 calls), not the full
	// retry cap, because each stall costs a full provider timeout.
	if f.calls != 2 {
		t.Errorf("provider called %d times on repeated invocation error, want 2 (fast-fail)", f.calls)
	}
}

func TestJudgeRecoversAfterOneInvocationError(t *testing.T) {
	// A single invocation error followed by a good response still succeeds.
	f := &fakeProvider{
		errs:    []error{errors.New("transient")},
		outputs: []string{"", goodJSON},
	}
	res := Judge(context.Background(), f, nil, req(), 2, time.Second, time.Second, nil)
	if res.Unverified {
		t.Fatalf("should recover after one invocation error, err=%v", res.Err)
	}
}

// --- Fallback provider tests (TDD 6.11-6.16) ---

func TestJudgeFallsBackAfterPrimaryExhausted(t *testing.T) {
	// TDD 6.13: primary exhausts its own retry budget with no valid result;
	// fallback then gets its own fresh attempt and succeeds.
	primary := &fakeProvider{outputs: []string{"junk", "junk", "junk"}}
	fallback := &fakeProvider{outputs: []string{goodJSON}}
	res := Judge(context.Background(), primary, fallback, req(), 2, time.Second, time.Second, nil)
	if res.Unverified {
		t.Fatalf("should recover via fallback, err=%v", res.Err)
	}
	if !res.FromFallback {
		t.Error("a fallback-produced result must be marked FromFallback (TDD 6.16)")
	}
	if primary.calls != 3 {
		t.Errorf("primary calls = %d, want 3 (full retry budget exhausted before fallback)", primary.calls)
	}
	if fallback.calls != 1 {
		t.Errorf("fallback calls = %d, want 1 (fresh attempt count, not inheriting primary's)", fallback.calls)
	}
}

func TestJudgeFallbackAlsoExhaustedStillUnverified(t *testing.T) {
	// TDD 6.14: fallback's own exhaustion still lands on unverified, with no
	// further fallback chain.
	primary := &fakeProvider{outputs: []string{"junk", "junk", "junk"}}
	fallback := &fakeProvider{outputs: []string{"junk", "junk", "junk"}}
	res := Judge(context.Background(), primary, fallback, req(), 2, time.Second, time.Second, nil)
	if !res.Unverified {
		t.Fatal("should be unverified when both primary and fallback exhaust their retries")
	}
	if res.FromFallback {
		t.Error("an unverified result must never be marked FromFallback")
	}
	if fallback.calls != 3 {
		t.Errorf("fallback calls = %d, want 3 (its own full retry budget, not primary's)", fallback.calls)
	}
}

func TestJudgeNoFallbackConfiguredBehavesAsBefore(t *testing.T) {
	// TDD 6.13: nil fallback preserves today's behavior exactly.
	f := &fakeProvider{outputs: []string{"junk", "junk", "junk"}}
	res := Judge(context.Background(), f, nil, req(), 2, time.Second, time.Second, nil)
	if !res.Unverified {
		t.Fatal("should be unverified with no fallback configured")
	}
	if res.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", res.Attempts)
	}
}

func TestJudgeNeverInterleavesPrimaryAndFallbackAttempts(t *testing.T) {
	// TDD 6.13: fallback must not be touched at all while primary still has
	// budget remaining — prove it by giving primary a script that succeeds on
	// its second attempt and asserting fallback.calls stays 0.
	primary := &fakeProvider{outputs: []string{"junk", goodJSON}}
	fallback := &fakeProvider{outputs: []string{goodJSON}}
	res := Judge(context.Background(), primary, fallback, req(), 2, time.Second, time.Second, nil)
	if res.Unverified {
		t.Fatalf("primary should recover on its own retry, err=%v", res.Err)
	}
	if res.FromFallback {
		t.Error("a primary recovery must not be attributed to fallback")
	}
	if fallback.calls != 0 {
		t.Errorf("fallback calls = %d, want 0 (never invoked while primary still had budget)", fallback.calls)
	}
}

// slowProvider blocks on Invoke until its context is done (deadline or
// cancellation), then reports a timeout-shaped error — simulating a provider
// call that takes longer than some timeout to fail, without actually sleeping
// past the longer of the two timeouts under test.
type slowProvider struct{ calls int }

func (s *slowProvider) Name() string { return "slow" }
func (s *slowProvider) Invoke(ctx context.Context, req Request, correction string) (string, error) {
	s.calls++
	<-ctx.Done()
	return "", ctx.Err()
}
func (s *slowProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	return "", ctx.Err()
}

func TestJudgeFallbackTimeoutIsIndependentOfPrimarys(t *testing.T) {
	// TDD 6.17: the fallback gets its own timeout, not the primary's. Give the
	// fallback a timeout comfortably longer than a slow call actually needs
	// (10ms), while the primary's timeout is tiny (1ms) so its own exhaustion
	// is fast — proving the two bounds are read from distinct parameters, not
	// one shared value silently reused for both slots.
	primary := &slowProvider{}
	fallback := &fakeProvider{outputs: []string{goodJSON}}
	start := time.Now()
	res := Judge(context.Background(), primary, fallback, req(), 0, time.Millisecond, 200*time.Millisecond, nil)
	elapsed := time.Since(start)
	if res.Unverified {
		t.Fatalf("fallback should have succeeded, err=%v", res.Err)
	}
	if !res.FromFallback {
		t.Error("result should be attributed to fallback")
	}
	// The primary's own 1ms timeout must have governed its exhaustion, not the
	// fallback's 200ms value — the whole call should finish in well under
	// 200ms plus fallback's own near-instant fakeProvider response.
	if elapsed > 100*time.Millisecond {
		t.Errorf("took %s; primary's short timeout does not appear to have been honored independently of fallback's longer one", elapsed)
	}
}
