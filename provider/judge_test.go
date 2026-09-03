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

const goodJSON = `{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer","emoji":"⏳"}`

func req() Request {
	return Request{Constraints: ConstraintsFrom(3, 8)}
}

func TestJudgeSucceedsFirstTry(t *testing.T) {
	f := &fakeProvider{outputs: []string{goodJSON}}
	res := Judge(context.Background(), f, req(), 2, time.Second)
	if res.Unverified {
		t.Fatal("should be verified")
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", res.Attempts)
	}
}

func TestJudgeSelfCorrects(t *testing.T) {
	f := &fakeProvider{outputs: []string{`{"bucket":"nonsense"}`, goodJSON}}
	res := Judge(context.Background(), f, req(), 2, time.Second)
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
	res := Judge(context.Background(), f, req(), 2, time.Second)
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
	res := Judge(context.Background(), f, req(), 2, time.Second)
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
	res := Judge(context.Background(), f, req(), 2, time.Second)
	if res.Unverified {
		t.Fatalf("should recover after one invocation error, err=%v", res.Err)
	}
}
