package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

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
