package provider

import (
	"os/exec"
	"testing"
	"time"
)

// requireTmux skips the test when tmux is not on PATH, so the suite still
// passes in an environment without it — sweepStaleHarvesterSessions's whole
// job is real tmux subprocess calls (list-sessions, kill-session), which has
// no clean fake the way inputTransport does for the harness's own interactive
// I/O (session_test.go).
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
}

// newTestTmuxSession creates a detached tmux session with a GSB-Harvester-*
// name (so sweepStaleHarvesterSessions's prefix filter matches it) and
// registers its cleanup, returning the session name.
func newTestTmuxSession(t *testing.T) string {
	t.Helper()
	name := tmuxSessionPrefix + randomSuffix(sessionSuffixLen)
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-x", "80", "-y", "24").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	return name
}

// TestSweepStaleHarvesterSessionsKillsOldSession covers ANTI-PATTERNS #10: a
// GSB-Harvester-* session older than the age threshold is killed.
func TestSweepStaleHarvesterSessionsKillsOldSession(t *testing.T) {
	requireTmux(t)
	name := newTestTmuxSession(t)

	// tmux has no option to backdate a session's own creation time, so exercise
	// the real kill path directly at a threshold sweepSessionsOlderThan would
	// treat as stale, rather than waiting staleHarvesterAge for real.
	sweepSessionsOlderThan("tmux", 0)

	if tmuxSessionExists("tmux", name) {
		t.Errorf("session %q should have been killed as stale", name)
	}
}

// TestSweepStaleHarvesterSessionsSparesRecentSession covers the safety margin:
// a session younger than the threshold survives the sweep untouched, so an
// in-progress run's own harness is never at risk.
func TestSweepStaleHarvesterSessionsSparesRecentSession(t *testing.T) {
	requireTmux(t)
	name := newTestTmuxSession(t)

	sweepSessionsOlderThan("tmux", time.Hour)

	if !tmuxSessionExists("tmux", name) {
		t.Errorf("session %q should have survived — it is well under the threshold", name)
	}
}

// TestSweepStaleHarvesterSessionsIgnoresOtherSessions covers the prefix
// filter: a tmux session with no GSB-Harvester- prefix is never touched,
// however old the threshold, so the sweep cannot collide with the operator's
// own unrelated tmux sessions.
func TestSweepStaleHarvesterSessionsIgnoresOtherSessions(t *testing.T) {
	requireTmux(t)
	name := "gsb-test-unrelated-" + randomSuffix(sessionSuffixLen)
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-x", "80", "-y", "24").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	sweepSessionsOlderThan("tmux", 0)

	if !tmuxSessionExists("tmux", name) {
		t.Errorf("session %q has no GSB-Harvester- prefix and must never be swept", name)
	}
}

// TestSweepStaleHarvesterSessionsMissingTmuxIsNoop covers the best-effort
// contract: an invalid tmux binary must not panic or error out to the caller
// — the sweep is upkeep, never a run-blocking precondition.
func TestSweepStaleHarvesterSessionsMissingTmuxIsNoop(t *testing.T) {
	sweepStaleHarvesterSessions("tmux-does-not-exist-gsb-test")
}

// TestSweepStaleHarvesterSessionsDefaultsEmptyBinToTmux covers the same
// empty-string-defaults-to-"tmux" convention every other tmux-invoking
// function in this file follows (tmuxSessionExists, uniqueTmuxSessionName).
func TestSweepStaleHarvesterSessionsDefaultsEmptyBinToTmux(t *testing.T) {
	requireTmux(t)
	name := newTestTmuxSession(t)

	sweepSessionsOlderThan("", 0)

	if tmuxSessionExists("tmux", name) {
		t.Errorf("session %q should have been killed via the default tmux binary", name)
	}
}
