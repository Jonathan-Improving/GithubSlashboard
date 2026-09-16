package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/store"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestParsePinTarget(t *testing.T) {
	pin, err := parsePinTarget("valkey-io/valkey-glide#295=reviewer")
	if err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
	if pin.Repo != "valkey-io/valkey-glide" || pin.Number != 295 || pin.Role != model.RoleReviewer {
		t.Errorf("parsed wrong: %+v", pin)
	}

	bad := []string{
		"noequalssign",
		"owner/repo=submitter",   // missing #number
		"ownerrepo#1=submitter",  // repo not owner/name
		"owner/repo#0=submitter", // non-positive
		"owner/repo#x=submitter", // number not int
		"owner/repo#1=boss",      // role not in vocabulary
	}
	for _, b := range bad {
		if _, err := parsePinTarget(b); err == nil {
			t.Errorf("malformed target %q should be rejected", b)
		}
	}
}

// TestRunPinWritesOnlyPin covers P.4: --pin adds exactly one pin, touches no
// !pr/!issue, and exits zero; a malformed target exits non-zero with no write.
func TestRunPinWritesOnlyPin(t *testing.T) {
	sp := filepath.Join(t.TempDir(), "prs.pr.yaml")
	// Seed an existing !pr so we can prove it is left untouched.
	seed := &store.Store{PRs: []model.PR{{
		Repo: "o/n", Number: 1, Title: "T", URL: "u", Role: model.RoleSubmitter,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, Priority: model.PriorityNeutral,
	}}}
	if err := seed.Write(sp); err != nil {
		t.Fatal(err)
	}

	if code := runPin(sp, "o/n#5=submitter", quietLog()); code != 0 {
		t.Fatalf("runPin exit = %d, want 0", code)
	}
	s, err := store.Read(sp)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Pinned) != 1 || s.Pinned[0].Key() != "o/n#5" {
		t.Errorf("want one pin o/n#5, got %+v", s.Pinned)
	}
	if len(s.PRs) != 1 || s.PRs[0].Number != 1 {
		t.Errorf("existing !pr must be untouched, got %+v", s.PRs)
	}

	// Malformed target: non-zero exit, no new pin.
	if code := runPin(sp, "garbage", quietLog()); code == 0 {
		t.Error("malformed --pin target must exit non-zero")
	}
	s2, _ := store.Read(sp)
	if len(s2.Pinned) != 1 {
		t.Errorf("malformed pin must not write, pins now %+v", s2.Pinned)
	}
}

// TestRunUnpinRemovesOnlyPin covers P.5: --unpin removes the pin, leaves !pr
// untouched, and is idempotent on an absent pin.
func TestRunUnpinRemovesOnlyPin(t *testing.T) {
	sp := filepath.Join(t.TempDir(), "prs.pr.yaml")
	seed := &store.Store{
		PRs:    []model.PR{{Repo: "o/n", Number: 5, Title: "T", URL: "u", Role: model.RoleSubmitter, Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, Priority: model.PriorityNeutral}},
		Pinned: []model.PinnedPR{{Repo: "o/n", Number: 5, Role: model.RoleSubmitter}},
	}
	if err := seed.Write(sp); err != nil {
		t.Fatal(err)
	}

	if code := runUnpin(sp, "o/n#5", quietLog()); code != 0 {
		t.Fatalf("runUnpin exit = %d, want 0", code)
	}
	s, _ := store.Read(sp)
	if len(s.Pinned) != 0 {
		t.Errorf("pin not removed: %+v", s.Pinned)
	}
	if len(s.PRs) != 1 {
		t.Errorf("!pr must be untouched by unpin, got %+v", s.PRs)
	}

	// Idempotent: unpinning again exits clean.
	if code := runUnpin(sp, "o/n#5", quietLog()); code != 0 {
		t.Errorf("idempotent unpin should exit 0, got %d", code)
	}
	// Malformed target exits non-zero.
	if code := runUnpin(sp, "bad", quietLog()); code == 0 {
		t.Error("malformed --unpin target must exit non-zero")
	}
}
