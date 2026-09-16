package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func storeFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "prs.pr.yaml")
}

// TestLockExclusiveThenReleased covers the core of P.9: a held lock blocks a
// second TryAcquire (ErrLockBusy), and releasing it lets the next acquire
// succeed — neither writer proceeds while the other holds the lock.
func TestLockExclusiveThenReleased(t *testing.T) {
	sp := storeFile(t)
	l1, err := TryAcquire(sp)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := TryAcquire(sp); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("second acquire should be busy, got %v", err)
	}
	l1.Release()
	l2, err := TryAcquire(sp)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	l2.Release()
}

// TestAcquireWaitTimesOut covers P.9's bounded-wait branch: a CLI mutation that
// waits for a held lock gives up with ErrLockBusy rather than blocking forever.
func TestAcquireWaitTimesOut(t *testing.T) {
	sp := storeFile(t)
	held, err := TryAcquire(sp)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.Release()

	start := time.Now()
	if _, err := AcquireWait(sp, 250*time.Millisecond); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("bounded wait should time out busy, got %v", err)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Errorf("returned too early (%s); it should have waited out the timeout", waited)
	}
}

// TestAcquireWaitSucceedsAfterRelease proves the wait resolves once the holder
// releases, so neither writer's turn is lost (P.9).
func TestAcquireWaitSucceedsAfterRelease(t *testing.T) {
	sp := storeFile(t)
	held, err := TryAcquire(sp)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		held.Release()
	}()
	l, err := AcquireWait(sp, 2*time.Second)
	if err != nil {
		t.Fatalf("wait should succeed after release, got %v", err)
	}
	l.Release()
}

// TestStaleLockReclaimed covers P.9's stale-reclaim branch: a lockfile left by a
// hard-killed process (older than the age floor) is reclaimed rather than
// wedging the store forever.
func TestStaleLockReclaimed(t *testing.T) {
	sp := storeFile(t)
	lp := lockPath(sp)
	// Forge an orphaned lock whose body timestamp is well past the age floor.
	old := time.Now().Add(-staleLockAge - time.Hour).Unix()
	if err := os.WriteFile(lp, []byte(fmt.Sprintf("99999\n%d\n", old)), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := TryAcquire(sp)
	if err != nil {
		t.Fatalf("stale lock should be reclaimed, got %v", err)
	}
	l.Release()
}

// TestFreshLockNotReclaimed guards the reclaim floor: a lock that is merely
// young (a genuinely running peer) must not be stolen.
func TestFreshLockNotReclaimed(t *testing.T) {
	sp := storeFile(t)
	lp := lockPath(sp)
	if err := os.WriteFile(lp, []byte(fmt.Sprintf("99999\n%d\n", time.Now().Unix())), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquire(sp); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("a fresh peer lock must not be reclaimed, got %v", err)
	}
}
