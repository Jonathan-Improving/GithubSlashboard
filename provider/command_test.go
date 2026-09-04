package provider

import (
	"context"
	"strings"
	"testing"
	"time"
)

// catArgv is a real, trivial subprocess that echoes stdin to stdout, letting
// these tests exercise the actual exec.Command plumbing (POLICY: test against
// a fake process, never a live model — cat is not a model, just an echo).
var catArgv = []string{"cat"}

func TestCommandProviderInvokeReturnsStdout(t *testing.T) {
	p, err := NewCommandProvider("test", catArgv)
	if err != nil {
		t.Fatalf("NewCommandProvider: %v", err)
	}
	raw, err := p.Invoke(context.Background(), req(), "")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	// Invoke sends the rendered prompt on stdin; cat echoes it back, so the
	// raw response must contain the prompt's own instructions/marker text.
	if !strings.Contains(raw, "Respond with exactly one fenced") {
		t.Errorf("Invoke did not deliver the built prompt to the subprocess: %q", raw)
	}
}

func TestCommandProviderSummarizeReturnsStdout(t *testing.T) {
	p, err := NewCommandProvider("test", catArgv)
	if err != nil {
		t.Fatalf("NewCommandProvider: %v", err)
	}
	raw, err := p.Summarize(context.Background(), "summarize: 3 PRs changed")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if raw != "summarize: 3 PRs changed" {
		t.Errorf("Summarize = %q, want the plain prompt echoed back verbatim (TDD 6.9: no Request shape, no rendering)", raw)
	}
}

func TestCommandProviderSummarizeUsesPlainPromptNotRequestShape(t *testing.T) {
	// TDD 6.9: Summarize must not go through buildPrompt/Request at all — a
	// plain string in, a plain string out. Prove it by checking the echoed
	// prompt carries none of buildPrompt's classification-specific scaffolding.
	p, err := NewCommandProvider("test", catArgv)
	if err != nil {
		t.Fatalf("NewCommandProvider: %v", err)
	}
	raw, err := p.Summarize(context.Background(), "a short prompt")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	for _, marker := range []string{"github_state", "constraints", "companion", "bucket"} {
		if strings.Contains(raw, marker) {
			t.Errorf("Summarize's prompt unexpectedly contains classification scaffolding %q: %q", marker, raw)
		}
	}
}

func TestCommandProviderInvokeTimesOut(t *testing.T) {
	// sleep outlives the context deadline, so Invoke must report a timeout
	// rather than hanging (TDD 6.5) — the same bound applies to Summarize since
	// both share the run() subprocess mechanics.
	p, err := NewCommandProvider("test", []string{"sleep", "5"})
	if err != nil {
		t.Fatalf("NewCommandProvider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Invoke(ctx, req(), ""); err == nil {
		t.Error("Invoke should error when the subprocess exceeds the context deadline")
	}
}

func TestCommandProviderSummarizeTimesOut(t *testing.T) {
	p, err := NewCommandProvider("test", []string{"sleep", "5"})
	if err != nil {
		t.Fatalf("NewCommandProvider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Summarize(ctx, "prompt"); err == nil {
		t.Error("Summarize should error when the subprocess exceeds the context deadline")
	}
}

func TestNewCommandProviderRejectsEmptyArgv(t *testing.T) {
	if _, err := NewCommandProvider("test", nil); err == nil {
		t.Error("empty argv should be rejected")
	}
}
