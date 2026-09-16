package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/config"
	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TestSignalNotifyContextCancelsOnSIGTERM covers the graceful-shutdown wiring
// added alongside sweepStaleHarvesterSessions (ANTI-PATTERNS #10): a SIGTERM
// — the signal `launchctl bootout` sends a still-running job, e.g. when the
// operator reloads the launchd agent mid-run to pick up a config change —
// cancels run's context rather than the process dying outright with no chance
// for the session provider's deferred Close() to tear down its tmux
// session(s). This exercises the exact wiring run() uses
// (signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)) against this
// test process's own PID, rather than re-deriving the assertion against
// stdlib documentation alone.
func TestSignalNotifyContextCancelsOnSIGTERM(t *testing.T) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to self: %v", err)
	}

	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("ctx.Err() = %v, want context.Canceled", ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("context was not cancelled within 5s of SIGTERM")
	}
}

func TestWriteOutputCreatesFileAndParents(t *testing.T) {
	dir := t.TempDir()
	// A nested, not-yet-existing parent directory.
	path := filepath.Join(dir, "nested", "sub", "GSB-SlashBoard.md")
	content := "# GithubSlashboard\n\n_Last Updated: 2026-01-01 00:00:00 UTC_\n"

	if err := writeOutput(path, content, ""); err != nil {
		t.Fatalf("writeOutput: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != content {
		t.Errorf("content mismatch:\n got %q\nwant %q", got, content)
	}

	// No leftover temp files in the directory.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if e.Name() != "GSB-SlashBoard.md" {
			t.Errorf("unexpected leftover file: %s", e.Name())
		}
	}
}

func TestWriteOutputOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.md")

	if err := writeOutput(path, "first", ""); err != nil {
		t.Fatalf("writeOutput 1: %v", err)
	}
	if err := writeOutput(path, "second", ""); err != nil {
		t.Fatalf("writeOutput 2: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("overwrite failed: got %q", got)
	}
}

func TestWriteOutputStagesElsewhere(t *testing.T) {
	// Staging dir and output dir are separate but on the same volume (both under
	// the test's temp root), mirroring store-dir staging into an iCloud output.
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	outDir := filepath.Join(root, "out")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outDir, "GSB-SlashBoard.md")

	if err := writeOutput(path, "staged content", staging); err != nil {
		t.Fatalf("writeOutput: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "staged content" {
		t.Errorf("content = %q", got)
	}
	// No temp left behind in the staging dir.
	entries, _ := os.ReadDir(staging)
	if len(entries) != 0 {
		t.Errorf("staging dir not clean: %v", entries)
	}
}

func TestPreflightOutputPassesOnWritableDir(t *testing.T) {
	dir := t.TempDir()
	if err := preflightOutput(filepath.Join(dir, "GSB-SlashBoard.md"), dir); err != nil {
		t.Fatalf("preflight should pass on a writable dir: %v", err)
	}
}

func TestPreflightOutputFailsFastOnUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil { // r-x, no write
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	err := preflightOutput(filepath.Join(locked, "GSB-SlashBoard.md"), root)
	if err == nil {
		t.Fatal("preflight should fail on an unwritable output dir")
	}
	// The error must be actionable: name Full Disk Access and the alternative.
	if !strings.Contains(err.Error(), "Full Disk Access") ||
		!strings.Contains(err.Error(), "GSB_OUTPUT_PATH") {
		t.Errorf("preflight error not actionable enough:\n%v", err)
	}
}

func TestIsPermissionDenied(t *testing.T) {
	if !isPermissionDenied(syscall.EPERM) || !isPermissionDenied(syscall.EACCES) {
		t.Error("EPERM/EACCES should be recognized as permission denied")
	}
	if !isPermissionDenied(os.ErrPermission) {
		t.Error("os.ErrPermission should be recognized")
	}
	if isPermissionDenied(errors.New("some other error")) {
		t.Error("unrelated error should not be a permission denial")
	}
}

// TestFireNotifyHookLogsDecisionAtInfo covers TDD 9.8: the per-run notify
// decision is visible at the default INFO level, so a run that correctly
// stayed quiet is distinguishable in the log from one that silently swallowed
// a change (ANTI-PATTERNS #13, whose defect hid because the skip logged at
// Debug). Both no-fire branches — nothing changed, and changed-but-inert
// because no hook is configured — return before any provider call, so this
// stays fully offline with a nil provider.
func TestFireNotifyHookLogsDecisionAtInfo(t *testing.T) {
	infoOnly := &slog.HandlerOptions{Level: slog.LevelInfo}

	t.Run("nothing changed", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, infoOnly))
		cfg := config.Config{NotifyHook: "true", NotifyTimeout: time.Second}
		prs := []model.PR{{Repo: "o/n", Number: 1, StatusChanged: false}}

		fireNotifyHook(context.Background(), cfg, nil, nil, prs, nil, log)

		out := buf.String()
		if !strings.Contains(out, "no status changed") {
			t.Errorf("expected an INFO decision line for a quiet run, got: %q", out)
		}
	})

	t.Run("changed but hook not configured", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, infoOnly))
		cfg := config.Config{NotifyHook: "", NotifyTimeout: time.Second}
		prs := []model.PR{{
			Repo: "o/n", Number: 2, Role: model.RoleSubmitter,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, StatusChanged: true,
		}}

		fireNotifyHook(context.Background(), cfg, nil, nil, prs, nil, log)

		out := buf.String()
		if !strings.Contains(out, "hook not configured") {
			t.Errorf("expected an INFO decision line for an inert run, got: %q", out)
		}
		if !strings.Contains(out, "changed=1") {
			t.Errorf("expected the changed count in the INFO line, got: %q", out)
		}
	})
}
