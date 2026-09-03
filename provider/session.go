package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// SessionProvider drives a heavy CLI agent harness (Kiro, Claude, Grok, …) as
// one long-lived interactive session and reuses it for every request, instead
// of paying the harness's full startup cost on a fresh subprocess per PR
// (TDD 6.7). Before each request it resets the harness context with a
// configurable clear command, submits the prompt, and then waits for the
// harness to hand its verdict back through a responseSink.
//
// The verdict is returned structurally, not scraped from the terminal: the
// harness calls a single provisioned tool whose arguments are the JSON verdict,
// which the sink surfaces as the request's response. This avoids parsing ANSI
// and prompt chrome out of a captured screen and makes completion detection
// exact — the tool call is the completion signal (TDD 6.8).
//
// It is general over any interactive harness: the input transport, the reset
// command, and the harness's agent profile are all supplied by configuration,
// so no core logic is coupled to a specific harness (TDD 6.2). A session holds
// process and server resources, so callers must Close it when classification is
// done. It implements Provider plus Close.
type SessionProvider struct {
	name      string
	transport inputTransport
	sink      responseSink
	clearCmd  string
	settle    time.Duration
	ready     time.Duration
	nudge     string
	nudgeCap  int
	promptDir string

	mu      sync.Mutex // one session serves one request at a time
	started bool
}

// inputTransport delivers text to the harness's interactive session: launch it,
// reset its context/screen, and type a submitted line. It is the write half of
// the session; the read half is the responseSink. Kept an interface so the
// provider can be unit-tested against a scripted fake with no live harness or
// tmux (POLICY: the provider is tested against a fake, never a live model).
type inputTransport interface {
	// Start launches the harness session, bounded by ctx.
	Start(ctx context.Context) error
	// WaitIdle blocks until the harness is idle (not mid-turn) so that a
	// command such as the context reset is accepted rather than queued behind a
	// running turn, or ctx is done.
	WaitIdle(ctx context.Context) error
	// Reset returns the harness to a clean context so each request is judged
	// a priori, with no residue from the previous one.
	Reset() error
	// SendLine types s and submits it (as if the operator pressed Enter).
	SendLine(s string) error
	// Interrupt signals the harness to end/cancel the current turn (e.g. the
	// Escape key), used once the verdict has been collected so the harness
	// returns to idle deterministically rather than lingering in the turn.
	Interrupt() error
	// Close tears the session down and releases its resources.
	Close() error
}

// responseSink is the read half of a session: the harness posts its verdict
// here (out of band from the terminal) and the provider waits for it. Await
// blocks until a verdict arrives for the pending request or ctx expires.
type responseSink interface {
	// Await blocks until the harness posts a verdict or ctx is done. The
	// returned string is the raw JSON verdict, parsed by the shared parser.
	Await(ctx context.Context) (string, error)
	// Drain discards any verdict left pending from a prior turn, so a new
	// request cannot consume a stale one.
	Drain()
	// WaitReady blocks until the harness has connected to the sink (proving the
	// verdict path works), or ctx is done.
	WaitReady(ctx context.Context) error
	// Close releases the sink's resources (e.g. stops the HTTP listener).
	Close() error
	// Endpoint reports where the harness should post verdicts (e.g. an MCP
	// URL), for provisioning the harness profile. Empty when not applicable.
	Endpoint() string
}

// newSessionProvider builds a SessionProvider over an input transport and a
// response sink. clearCmd is the harness's context-reset command (e.g. "/clear"
// for Kiro), sent before every request. It is unexported so construction goes
// through New, keeping a single provider seam.
func newSessionProvider(name string, t inputTransport, sink responseSink, clearCmd string, settle, ready time.Duration) *SessionProvider {
	return &SessionProvider{
		name:      name,
		transport: t,
		sink:      sink,
		clearCmd:  clearCmd,
		settle:    settle,
		ready:     ready,
		nudge:     defaultNudge,
		nudgeCap:  defaultNudgeCap,
	}
}

// defaultNudge steers a harness that ended its turn without calling the verdict
// tool to call it now; defaultNudgeCap bounds how many times to re-nudge before
// giving up and letting the request fall to the unverified path (TDD 6.4).
const (
	defaultNudge    = "You did not submit a verdict. Call the verdict tool now with your classification for the pull request above."
	defaultNudgeCap = 2
)

// Name identifies the provider for logging.
func (s *SessionProvider) Name() string { return s.name }

// Invoke resets the harness, submits the prompt, and waits for the harness to
// post its verdict through the sink (TDD 6.7, 6.8). The session is started
// lazily on the first call so provider construction stays cheap and does not
// require a live harness. The call is bounded by ctx (TDD 6.5); on timeout it
// returns an error so the caller's retry/unverified path applies.
func (s *SessionProvider) Invoke(ctx context.Context, req Request, correction string) (string, error) {
	// A single session and single-slot sink cannot serve concurrent requests;
	// serialize so classify's fan-out queues here rather than interleaving
	// prompts on one harness.
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		if err := s.transport.Start(ctx); err != nil {
			return "", fmt.Errorf("provider %s: start session: %w", s.name, err)
		}
		// Wait until the harness has connected to the sink, proving the verdict
		// path works before we submit anything (harness-agnostic readiness).
		readyCtx := ctx
		if s.ready > 0 {
			var cancel context.CancelFunc
			readyCtx, cancel = context.WithTimeout(ctx, s.ready)
			defer cancel()
		}
		if err := s.sink.WaitReady(readyCtx); err != nil {
			return "", fmt.Errorf("provider %s: %w", s.name, err)
		}
		s.started = true
	}

	prompt, err := buildPrompt(req, correction)
	if err != nil {
		return "", err
	}

	// Reset context between requests so each PR is judged a priori (TDD 6.7).
	// The harness only accepts a slash command when idle — sent mid-turn it is
	// queued as chat and the conversation never resets, so the context grows
	// unbounded across PRs. Gate every command on an idle harness.
	if s.clearCmd != "" {
		if err := s.waitIdleBounded(ctx); err != nil {
			return "", fmt.Errorf("provider %s: wait idle before clear: %w", s.name, err)
		}
		if err := s.transport.SendLine(s.clearCmd); err != nil {
			return "", fmt.Errorf("provider %s: send clear: %w", s.name, err)
		}
		// Wait for the reset to complete (harness returns to idle) before the
		// prompt, so the prompt is not queued behind the clear.
		if err := s.waitIdleBounded(ctx); err != nil {
			return "", fmt.Errorf("provider %s: wait idle after clear: %w", s.name, err)
		}
	}
	if err := s.transport.Reset(); err != nil {
		return "", fmt.Errorf("provider %s: reset: %w", s.name, err)
	}

	s.sink.Drain()

	// Deliver the prompt. Small prompts are typed straight into the session as
	// one line (a single round-trip, fastest). Large prompts exceed tmux
	// send-keys' command-length limit ("command too long") and would be
	// truncated, so those are written to a file and the harness is told to read
	// it (an extra read round-trip, but correct for any size). The threshold
	// keeps the common short-trail PR on the fast inline path.
	flat := flattenPrompt(prompt)
	if len(flat) <= inlinePromptLimit {
		if err := s.transport.SendLine(flat); err != nil {
			return "", fmt.Errorf("provider %s: send prompt: %w", s.name, err)
		}
	} else {
		promptPath, werr := s.writePromptFile(prompt)
		if werr != nil {
			return "", fmt.Errorf("provider %s: write prompt file: %w", s.name, werr)
		}
		defer func() { _ = os.Remove(promptPath) }()
		instruction := fmt.Sprintf("Read the pull request context and instructions from the file %s, then classify it and return your verdict via the verdict tool.", promptPath)
		if err := s.transport.SendLine(instruction); err != nil {
			return "", fmt.Errorf("provider %s: send prompt instruction: %w", s.name, err)
		}
	}

	// Wait for the harness to post its verdict through the sink; the tool call
	// is the completion signal (TDD 6.8). If the harness ends its turn WITHOUT
	// calling the tool, awaitVerdict re-nudges it rather than blocking until the
	// overall timeout — the deterministic-harness failsafe.
	raw, err := s.awaitVerdict(ctx)
	if err != nil {
		return "", fmt.Errorf("provider %s: await verdict: %w", s.name, err)
	}

	// The verdict tool fires mid-turn; once collected, interrupt to end the
	// turn deterministically (the model may otherwise keep working after the
	// tool result), then wait for idle so the next PR's clear is accepted.
	if err := s.transport.Interrupt(); err != nil {
		return "", fmt.Errorf("provider %s: interrupt after verdict: %w", s.name, err)
	}
	if err := s.waitIdleBounded(ctx); err != nil {
		return "", fmt.Errorf("provider %s: wait idle after verdict: %w", s.name, err)
	}
	return raw, nil
}

// awaitVerdict waits for the harness to call the verdict tool. It races the
// verdict against the harness returning to idle: if the turn ends with no
// verdict, the model forgot to call the tool, so it nudges the harness to call
// it now and waits again — up to nudgeCap times — instead of stalling until the
// overall ctx deadline. This is the provider-side stand-in for a harness
// stop-hook (kiro-cli's agent schema exposes only userPromptSubmit/agentSpawn
// hooks, no turn-stop hook, so the recovery is driven here).
func (s *SessionProvider) awaitVerdict(ctx context.Context) (string, error) {
	for attempt := 0; ; attempt++ {
		verdictCh := make(chan string, 1)
		errCh := make(chan error, 1)
		waitCtx, cancel := context.WithCancel(ctx)
		go func() {
			v, e := s.sink.Await(waitCtx)
			if e != nil {
				errCh <- e
				return
			}
			verdictCh <- v
		}()

		// Watch for the harness returning to idle (turn ended) in parallel.
		idleCh := make(chan struct{}, 1)
		go func() {
			if err := s.transport.WaitIdle(waitCtx); err == nil {
				idleCh <- struct{}{}
			}
		}()

		select {
		case <-ctx.Done():
			cancel()
			return "", fmt.Errorf("timed out waiting for verdict")
		case v := <-verdictCh:
			cancel()
			return v, nil
		case <-errCh:
			cancel()
			return "", fmt.Errorf("timed out waiting for verdict")
		case <-idleCh:
			// Turn ended with no verdict yet. The tool call and turn-end can
			// race (the tool fires just before idle), so give the verdict a
			// bounded grace window before concluding the model forgot.
			grace := s.settle
			if grace <= 0 {
				grace = 500 * time.Millisecond
			}
			select {
			case v := <-verdictCh:
				cancel()
				return v, nil
			case <-time.After(grace):
			case <-ctx.Done():
				cancel()
				return "", fmt.Errorf("timed out waiting for verdict")
			}
			cancel()
			if attempt >= s.nudgeCap {
				return "", fmt.Errorf("harness ended turn without calling the verdict tool after %d nudges", s.nudgeCap)
			}
			s.sink.Drain()
			if err := s.transport.SendLine(s.nudge); err != nil {
				return "", fmt.Errorf("send nudge: %w", err)
			}
		}
	}
}

// Close tears down the sink and the underlying session. Safe to call when the
// session was never started.
func (s *SessionProvider) Close() error {
	var firstErr error
	if s.sink != nil {
		if err := s.sink.Close(); err != nil {
			firstErr = err
		}
	}
	if s.transport != nil {
		if err := s.transport.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// inlinePromptLimit is the max prompt size (bytes) sent inline via one
// send-keys; tmux rejects a single send-keys payload around 16-20 KB with
// "command too long", so a conservative margin below that keeps the common
// short-trail PR on the fast inline path while larger trails use the file.
const inlinePromptLimit = 12000

// flattenPrompt collapses a multi-line prompt into a single line so an
// interactive harness receives it as one submitted turn.
func flattenPrompt(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, "\r\n", " "), "\n", " ")
}

// betweenTurnIdleTimeout bounds a between-turn idle wait (before /clear, after
// /clear, after a verdict). Between turns the harness returns to idle within
// seconds, so a wait that can't confirm idle in this window is a missed
// detection, not a legitimately long turn — fail fast into the retry path
// rather than burn the whole per-call budget. (The in-turn idle race inside
// awaitVerdict is NOT bounded by this; a turn may legitimately run to the call
// timeout.)
const betweenTurnIdleTimeout = 25 * time.Second

// waitIdleBounded waits for the harness to be idle, bounded by the shorter of
// the caller's ctx and betweenTurnIdleTimeout.
func (s *SessionProvider) waitIdleBounded(ctx context.Context) error {
	bctx, cancel := context.WithTimeout(ctx, betweenTurnIdleTimeout)
	defer cancel()
	return s.transport.WaitIdle(bctx)
}

// writePromptFile writes the full prompt to a temporary file the harness reads,
// avoiding the terminal command-length limit of typing a large event trail into
// the session. The file lives in promptDir (a location the harness's read tool
// is scoped to); the caller removes it once the verdict is collected.
func (s *SessionProvider) writePromptFile(prompt string) (string, error) {
	dir := s.promptDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "gsb-prompt-*.txt")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(prompt); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// --- tmux input transport ---------------------------------------------------

// tmuxTransport implements inputTransport using tmux: it launches the harness
// in a detached tmux session and types into it with send-keys. Reading the
// verdict is the sink's job, not tmux's, so this transport never captures the
// pane. Keeping tmux behind the interface means the core provider is not
// coupled to tmux and can be tested without it.
type tmuxTransport struct {
	tmuxBin    string
	session    string
	argv       []string // harness executable and its arguments
	ready      time.Duration
	workDir    string // working directory for the harness (agent resolution)
	cleanup    func() // optional teardown (e.g. remove provisioned profile)
	idleMarker string // pane substring shown when the harness is idle
	busyMarker string // pane substring shown when the harness is mid-turn
	settle     time.Duration
	poll       time.Duration
}

// newTmuxTransport builds a tmux transport for the given harness argv. session
// is the tmux session name to create; it must be unique to this run.
func newTmuxTransport(tmuxBin, session string, argv []string, ready time.Duration) *tmuxTransport {
	return &tmuxTransport{tmuxBin: tmuxBin, session: session, argv: argv, ready: ready, poll: 300 * time.Millisecond}
}

// WaitIdle blocks until the harness pane shows the idle marker and not the busy
// marker, held stable for the settle window, or ctx expires. When no idle
// marker is configured it falls back to a fixed settle wait. This is how the
// provider avoids sending a slash command mid-turn (which the harness would
// queue as chat, leaving the context un-reset).
func (t *tmuxTransport) WaitIdle(ctx context.Context) error {
	settle := t.settle
	if settle <= 0 {
		settle = 800 * time.Millisecond
	}
	poll := t.poll
	if poll <= 0 {
		poll = 300 * time.Millisecond
	}

	// Without an idle marker there is nothing to detect; wait the settle window.
	if t.idleMarker == "" {
		timer := time.NewTimer(settle)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var idleSince time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		screen, err := t.capturePane()
		if err != nil {
			return fmt.Errorf("tmux wait-idle capture: %w", err)
		}
		clean := stripANSI(screen)
		idle := strings.Contains(clean, t.idleMarker) &&
			(t.busyMarker == "" || !strings.Contains(clean, t.busyMarker))
		if !idle {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
		}
		if time.Since(idleSince) >= settle {
			return nil
		}
	}
}

func (t *tmuxTransport) Start(ctx context.Context) error {
	if len(t.argv) == 0 {
		return fmt.Errorf("tmux transport: empty harness argv")
	}
	args := []string{"new-session", "-d", "-s", t.session}
	if t.workDir != "" {
		args = append(args, "-c", t.workDir)
	}
	args = append(args, t.argv...)
	if out, err := t.run(ctx, args...); err != nil {
		return fmt.Errorf("tmux new-session: %w (%s)", err, out)
	}

	// Confirm the harness process is up by waiting for the pane to produce any
	// output. The authoritative readiness gate — that the harness connected to
	// the verdict sink — is checked by the session provider, not here.
	deadline := time.Now().Add(t.ready)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		out, err := t.capturePane()
		if err == nil && strings.TrimSpace(stripANSI(out)) != "" {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("tmux transport: harness pane produced no output within %s", t.ready)
}

func (t *tmuxTransport) Reset() error {
	if out, err := t.run(context.Background(), "clear-history", "-t", t.session); err != nil {
		return fmt.Errorf("tmux clear-history: %w (%s)", err, out)
	}
	return nil
}

func (t *tmuxTransport) SendLine(s string) error {
	// Send the literal text, then a separate Enter, so tmux does not interpret
	// the payload as key names.
	if out, err := t.run(context.Background(), "send-keys", "-t", t.session, "-l", s); err != nil {
		return fmt.Errorf("tmux send-keys text: %w (%s)", err, out)
	}
	if out, err := t.run(context.Background(), "send-keys", "-t", t.session, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys enter: %w (%s)", err, out)
	}
	return nil
}

// Interrupt sends the Escape key to cancel the current turn.
func (t *tmuxTransport) Interrupt() error {
	if out, err := t.run(context.Background(), "send-keys", "-t", t.session, "Escape"); err != nil {
		return fmt.Errorf("tmux send-keys escape: %w (%s)", err, out)
	}
	return nil
}

func (t *tmuxTransport) Close() error {
	_, _ = t.run(context.Background(), "kill-session", "-t", t.session)
	if t.cleanup != nil {
		t.cleanup()
	}
	return nil
}

func (t *tmuxTransport) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, t.tmuxBin, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// capturePane returns the current visible pane contents. It is used only for
// readiness detection and diagnostics, never to read the verdict (the verdict
// comes structurally through the sink).
func (t *tmuxTransport) capturePane() (string, error) {
	return t.run(context.Background(), "capture-pane", "-p", "-t", t.session)
}

// stripANSI removes ANSI escape sequences for readiness checks.
func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}
