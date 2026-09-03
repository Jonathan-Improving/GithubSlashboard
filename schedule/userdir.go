package schedule

import (
	"os"
	"path/filepath"
	"runtime"
)

// appDirName is the per-user directory this tool keeps its artifacts in, appended
// to whichever base directory the host platform designates for application data.
const appDirName = "github-slashboard"

// UserDataDir returns the platform's conventional per-user application-data
// directory for this tool, already suffixed with the tool's own directory name.
//
// It lives in this package because this is the platform seam: the pipeline
// packages stay free of OS-specific knowledge, so `config` asks for a resolved
// path rather than branching on the host itself (TECH: the core is
// platform-independent; OS-specific concerns are isolated here).
//
// Resolution per platform:
//
//	Linux and other Unix — $XDG_DATA_HOME/github-slashboard, falling back to
//	                       ~/.local/share/github-slashboard per the XDG Base
//	                       Directory specification.
//	macOS                — ~/Library/Application Support/github-slashboard.
//
// When the home directory cannot be determined — an unusual environment with no
// HOME set — it returns a relative directory so callers still get a usable path
// instead of an error they would only be able to turn into a default anyway.
func UserDataDir() string {
	if dir, ok := userDataBase(); ok {
		return filepath.Join(dir, appDirName)
	}
	// Last resort: keep artifacts beside the working directory rather than
	// failing. Relative, so it is valid on every platform.
	return appDirName
}

// userDataBase returns the platform's base application-data directory and whether
// it could be determined.
func userDataBase() (string, bool) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		return filepath.Join(home, "Library", "Application Support"), true
	}

	// Unix and everything else: honor XDG_DATA_HOME when it is set to an
	// absolute path, as the specification requires (a relative value is
	// explicitly to be ignored).
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		return xdg, true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".local", "share"), true
}
