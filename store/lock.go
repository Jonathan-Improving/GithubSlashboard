package store

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// LockSuffix is appended to the store path to form the sibling lockfile
// (decision 7: the lock shares the store's directory and volume). A store at
// ".../prs.pr.yaml" locks via ".../prs.pr.yaml.lock".
const LockSuffix = ".lock"

// staleLockAge is how old a lockfile must be before it is treated as abandoned
// and reclaimed. It mirrors the stale-tmux-session sweep in the provider
// package: a lock is released on normal exit and on the graceful SIGTERM/SIGINT
// path, so a lock older than this floor can only have been orphaned by a hard
// kill (SIGKILL) that skipped the release. It is deliberately far longer than
// any plausible single run's critical section (a pipeline holds the lock only
// across its read→write, and a CLI mutation is sub-second) so the reclaim never
// races a run that is merely long.
const staleLockAge = 2 * time.Hour

// lockPollInterval is how often AcquireWait retries while another holder has the
// lock. Short enough that a CLI mutation follows a pipeline release promptly,
// long enough not to spin.
const lockPollInterval = 100 * time.Millisecond

// ErrLockBusy is returned when the lock cannot be acquired: by TryAcquire
// immediately when another holder has it, or by AcquireWait once its bounded
// wait elapses. The CLI surfaces this as "store busy, retry" rather than
// blocking indefinitely (decision 7).
var ErrLockBusy = errors.New("store is locked by another process")

// Lock is a held store lock. Release it once the read→write critical section is
// done; it is also safe to Release on any exit path (including the signal
// handler), and a double Release is a no-op.
type Lock struct {
	path     string
	acquired bool
}

// lockPath returns the sibling lockfile path for a store path.
func lockPath(storePath string) string {
	return storePath + LockSuffix
}

// TryAcquire attempts to take the store lock without waiting. It succeeds only
// if no live lock exists (reclaiming an abandoned one older than staleLockAge
// first). If another process holds a fresh lock, it returns ErrLockBusy
// immediately — the behavior a pipeline wants, so an overrun of a prior
// scheduled run exits without piling up (decision 7).
func TryAcquire(storePath string) (*Lock, error) {
	return acquire(storePath, 0)
}

// AcquireWait takes the store lock, waiting up to timeout for a current holder
// to release it — the behavior a short-lived CLI mutation wants so it does not
// lose to a running pipeline. It reclaims an abandoned lock (older than
// staleLockAge) rather than waiting on it. If the wait elapses with the lock
// still held, it returns ErrLockBusy so the caller can report "store busy,
// retry" instead of blocking forever (decision 7).
func AcquireWait(storePath string, timeout time.Duration) (*Lock, error) {
	return acquire(storePath, timeout)
}

// acquire is the shared implementation. A zero timeout means do-not-wait
// (TryAcquire); a positive timeout bounds the wait (AcquireWait).
func acquire(storePath string, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	path := lockPath(storePath)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			// Record PID and creation time so a later run can judge staleness
			// and a human can see who holds it.
			fmt.Fprintf(f, "%d\n%d\n", os.Getpid(), time.Now().Unix())
			if cerr := f.Close(); cerr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("write lockfile %q: %w", path, cerr)
			}
			return &Lock{path: path, acquired: true}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire store lock %q: %w", path, err)
		}

		// The lock exists. Reclaim it if abandoned, then retry immediately.
		if reclaimed := reclaimIfStale(path); reclaimed {
			continue
		}

		// Held by a live process. Wait (bounded) or give up.
		if timeout <= 0 || time.Now().After(deadline) {
			return nil, ErrLockBusy
		}
		time.Sleep(lockPollInterval)
	}
}

// reclaimIfStale removes the lockfile if it is older than staleLockAge (an
// orphan from a hard-killed process) and reports whether it did. A lock whose
// age cannot be read (missing, or a malformed body) is left alone unless the
// file's own mtime shows it is past the floor — the timestamp in the body is
// the primary signal, the mtime the fallback, so neither a truncated write nor
// a clock quirk wedges the store permanently.
func reclaimIfStale(path string) bool {
	age, ok := lockAge(path)
	if !ok {
		return false
	}
	if age < staleLockAge {
		return false
	}
	// Best-effort removal: if another process reclaimed it first, the retry
	// loop's next OpenFile simply succeeds or contends again.
	return os.Remove(path) == nil
}

// lockAge returns how long ago the lock was taken, preferring the unix
// timestamp written in the lockfile body and falling back to the file's mtime
// when the body is missing or malformed. The bool is false when the file does
// not exist (nothing to reclaim).
func lockAge(path string) (time.Duration, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Since(fi.ModTime()), true
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) >= 2 {
		if unix, perr := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64); perr == nil {
			return time.Since(time.Unix(unix, 0)), true
		}
	}
	return time.Since(fi.ModTime()), true
}

// Release removes the lockfile. It is idempotent — a second call, or a call on
// an unacquired Lock, is a no-op — so it is safe to defer and also call from a
// signal handler.
func (l *Lock) Release() {
	if l == nil || !l.acquired {
		return
	}
	l.acquired = false
	_ = os.Remove(l.path)
}
