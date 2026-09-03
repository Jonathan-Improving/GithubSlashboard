package provider

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// fakeTransport records the lines sent and scripts Start behavior.
type fakeTransport struct {
	startErr   error
	started    bool
	startCount int
	resets     int
	lines      []string
	closed     bool
}

func (f *fakeTransport) Start(ctx context.Context) error {
	f.startCount++
	if f.startErr != nil {
		return f.startErr
	}
	f.started = true
	return nil
}
func (f *fakeTransport) Reset() error                       { f.resets++; return nil }
func (f *fakeTransport) WaitIdle(ctx context.Context) error { return nil }
func (f *fakeTransport) SendLine(s string) error            { f.lines = append(f.lines, s); return nil }
func (f *fakeTransport) Interrupt() error                   { return nil }
func (f *fakeTransport) Close() error                       { f.closed = true; return nil }

// fakeSink returns a scripted verdict, or blocks until ctx is done when none.
type fakeSink struct {
	verdict string
	block   bool
	closed  bool
}

func (f *fakeSink) Await(ctx context.Context) (string, error) {
	if f.block {
		<-ctx.Done()
		return "", errors.New("timed out")
	}
	return f.verdict, nil
}
func (f *fakeSink) WaitReady(ctx context.Context) error { return nil }
func (f *fakeSink) Drain()                              {}
func (f *fakeSink) Close() error                        { f.closed = true; return nil }
func (f *fakeSink) Endpoint() string                    { return "http://127.0.0.1:0/mcp" }

func TestSessionInvokeClearsThenPromptsThenReturnsVerdict(t *testing.T) {
	tr := &fakeTransport{}
	sink := &fakeSink{verdict: goodJSON}
	p := newSessionProvider("kiro", tr, sink, "/clear", 0, 0)

	raw, err := p.Invoke(context.Background(), req(), "")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if raw != goodJSON {
		t.Errorf("raw = %q, want the sink verdict", raw)
	}
	if !tr.started {
		t.Error("transport should have started lazily")
	}
	// The clear command must be sent before the prompt (a priori reset, TDD 6.7).
	if len(tr.lines) != 2 {
		t.Fatalf("sent %d lines, want 2 (clear, prompt)", len(tr.lines))
	}
	if tr.lines[0] != "/clear" {
		t.Errorf("first line = %q, want the clear command", tr.lines[0])
	}
	// A small prompt is sent inline (one round-trip), not via a file.
	if strings.Contains(tr.lines[1], "Read the pull request context") {
		t.Errorf("small prompt should be inline, not a file handoff: %q", tr.lines[1])
	}
	if tr.resets != 1 {
		t.Errorf("resets = %d, want 1", tr.resets)
	}
}

func TestSessionInvokeLargePromptUsesFileHandoff(t *testing.T) {
	tr := &fakeTransport{}
	sink := &fakeSink{verdict: goodJSON}
	p := newSessionProvider("kiro", tr, sink, "/clear", 0, 0)

	// A large event trail pushes the prompt past the inline limit -> file handoff.
	var events []model.Event
	for i := 0; i < 400; i++ {
		events = append(events, model.Event{
			Author: "user", Kind: model.EventComment,
			Text: "a fairly long review comment that repeats to inflate the trail well beyond the inline threshold",
		})
	}
	big := Request{Events: events, Constraints: ConstraintsFrom(3, 14)}

	if _, err := p.Invoke(context.Background(), big, ""); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	instr := tr.lines[len(tr.lines)-1]
	if !strings.Contains(instr, "Read the pull request context") {
		t.Fatalf("large prompt should use file handoff, got %q", instr)
	}
	var promptPath string
	for _, f := range strings.Fields(instr) {
		f = strings.TrimRight(f, ".,;")
		if strings.HasSuffix(f, ".txt") {
			promptPath = f
			break
		}
	}
	if promptPath == "" {
		t.Fatalf("no .txt prompt path in: %q", instr)
	}
	if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
		t.Errorf("prompt file %s should be deleted after the verdict", promptPath)
	}
}

func TestSessionInvokeStartsOnce(t *testing.T) {
	tr := &fakeTransport{}
	sink := &fakeSink{verdict: goodJSON}
	p := newSessionProvider("kiro", tr, sink, "/clear", 0, 0)

	for i := 0; i < 3; i++ {
		if _, err := p.Invoke(context.Background(), req(), ""); err != nil {
			t.Fatalf("Invoke %d: %v", i, err)
		}
	}
	// Started lazily and only once across repeated calls.
	if !tr.started {
		t.Error("not started")
	}
}

// TestSessionReusesOneHarnessAcrossPRs covers TDD 6.7: one harness is launched
// once and reused for every PR, and each PR is reset (a priori) beforehand.
func TestSessionReusesOneHarnessAcrossPRs(t *testing.T) {
	tr := &fakeTransport{}
	sink := &fakeSink{verdict: goodJSON}
	p := newSessionProvider("kiro", tr, sink, "/clear", 0, 0)

	const n = 5
	for i := 0; i < n; i++ {
		if _, err := p.Invoke(context.Background(), req(), ""); err != nil {
			t.Fatalf("Invoke %d: %v", i, err)
		}
	}
	if tr.startCount != 1 {
		t.Errorf("harness started %d times, want exactly 1 (reused across PRs)", tr.startCount)
	}
	if tr.resets != n {
		t.Errorf("context reset %d times, want %d (once per PR, a priori)", tr.resets, n)
	}
}

func TestSessionInvokeTimesOut(t *testing.T) {
	tr := &fakeTransport{}
	sink := &fakeSink{block: true}
	p := newSessionProvider("kiro", tr, sink, "/clear", 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Invoke(ctx, req(), ""); err == nil {
		t.Error("Invoke should error when the sink never receives a verdict (TDD 6.5)")
	}
}

func TestSessionStartErrorPropagates(t *testing.T) {
	tr := &fakeTransport{startErr: errors.New("no tmux")}
	sink := &fakeSink{verdict: goodJSON}
	p := newSessionProvider("kiro", tr, sink, "/clear", 0, 0)

	if _, err := p.Invoke(context.Background(), req(), ""); err == nil {
		t.Error("start error should propagate")
	}
}

func TestSessionCloseClosesBoth(t *testing.T) {
	tr := &fakeTransport{}
	sink := &fakeSink{verdict: goodJSON}
	p := newSessionProvider("kiro", tr, sink, "/clear", 0, 0)
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !tr.closed || !sink.closed {
		t.Error("Close should close both transport and sink")
	}
}

func TestNewFromOptionsSelectsOneShot(t *testing.T) {
	p, err := NewFromOptions(Options{Name: "kiro", Kind: KindOneShot})
	if err != nil {
		t.Fatalf("NewFromOptions oneshot: %v", err)
	}
	if _, ok := p.(*CommandProvider); !ok {
		t.Errorf("oneshot should yield a CommandProvider, got %T", p)
	}
}

func TestNewFromOptionsUnknownKind(t *testing.T) {
	if _, err := NewFromOptions(Options{Name: "kiro", Kind: Kind("bogus")}); err == nil {
		t.Error("unknown kind should error")
	}
}

// nudgeSink yields a verdict only after the provider has nudged (i.e. sent a
// line after the initial prompt). It models a harness that ended its turn
// without calling the verdict tool until reminded.
type nudgeSink struct {
	verdict  string
	nudged   *bool
	released bool
}

func (n *nudgeSink) Await(ctx context.Context) (string, error) {
	// Block until nudged, then return the verdict.
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		if *n.nudged {
			return n.verdict, nil
		}
		time.Sleep(2 * time.Millisecond)
	}
}
func (n *nudgeSink) Drain()                              {}
func (n *nudgeSink) WaitReady(ctx context.Context) error { return nil }
func (n *nudgeSink) Close() error                        { return nil }
func (n *nudgeSink) Endpoint() string                    { return "" }

// idleThenBusyTransport reports idle once (triggering a nudge) and records the
// nudge by flipping a flag when a line is sent after the prompt.
type nudgeTransport struct {
	lines  []string
	nudged *bool
}

func (t *nudgeTransport) Start(ctx context.Context) error    { return nil }
func (t *nudgeTransport) WaitIdle(ctx context.Context) error { return nil }
func (t *nudgeTransport) Reset() error                       { return nil }
func (t *nudgeTransport) SendLine(s string) error {
	t.lines = append(t.lines, s)
	// The first line is the prompt; any subsequent line is a nudge.
	if len(t.lines) > 1 {
		*t.nudged = true
	}
	return nil
}
func (t *nudgeTransport) Interrupt() error { return nil }
func (t *nudgeTransport) Close() error     { return nil }

func TestSessionNudgesWhenVerdictMissing(t *testing.T) {
	nudged := false
	tr := &nudgeTransport{nudged: &nudged}
	sink := &nudgeSink{verdict: goodJSON, nudged: &nudged}
	p := newSessionProvider("kiro", tr, sink, "/clear", 10*time.Millisecond, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := p.Invoke(ctx, req(), "")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if raw != goodJSON {
		t.Errorf("raw = %q, want verdict after nudge", raw)
	}
	if !nudged {
		t.Error("provider should have nudged the harness when the turn ended without a verdict")
	}
}
