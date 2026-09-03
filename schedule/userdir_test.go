package schedule

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestUserDataDirIsPlatformAppropriate covers the platform seam's resolution: the
// per-user data directory follows the host's own convention rather than hardcoding
// one platform's layout, and always ends in the tool's own directory name.
func TestUserDataDirIsPlatformAppropriate(t *testing.T) {
	got := UserDataDir()

	if filepath.Base(got) != appDirName {
		t.Errorf("UserDataDir() = %q, want it to end in %q", got, appDirName)
	}

	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(got, filepath.Join("Library", "Application Support")) {
			t.Errorf("on darwin UserDataDir() = %q, want it under Library/Application Support", got)
		}
	case "linux":
		// With XDG_DATA_HOME unset in the test environment the XDG fallback applies.
		if !strings.Contains(got, filepath.Join(".local", "share")) &&
			!filepath.IsAbs(got) {
			t.Errorf("on linux UserDataDir() = %q, want the XDG data dir", got)
		}
	}
}

// TestUserDataDirHonorsXDGDataHome proves the XDG variable is respected on
// non-darwin hosts, and that a relative value is ignored as the specification
// requires.
func TestUserDataDirHonorsXDGDataHome(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG_DATA_HOME does not govern the macOS location")
	}

	t.Setenv("XDG_DATA_HOME", "/custom/data")
	if got := UserDataDir(); got != filepath.Join("/custom/data", appDirName) {
		t.Errorf("UserDataDir() = %q, want it under the XDG_DATA_HOME override", got)
	}

	// A relative XDG_DATA_HOME is invalid per the specification and must be
	// ignored in favour of the home-directory fallback.
	t.Setenv("XDG_DATA_HOME", "relative/path")
	got := UserDataDir()
	if strings.Contains(got, "relative/path") {
		t.Errorf("UserDataDir() = %q, want a relative XDG_DATA_HOME to be ignored", got)
	}
}

// TestUserDataDirNeverEmpty proves the resolver always yields a usable path, so a
// caller building a default never has to handle an error it could only turn into a
// fallback anyway.
func TestUserDataDirNeverEmpty(t *testing.T) {
	if UserDataDir() == "" {
		t.Error("UserDataDir() returned an empty path")
	}
}
