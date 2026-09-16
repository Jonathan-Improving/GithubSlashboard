package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TestPinnedPRCoexistsAndRoundTrips covers P.6: a store holding !pr, !issue, and
// !pinned-pr documents preserves all three, the pin validates, and an
// unrecognized tag is still round-tripped verbatim (extends 2.5).
func TestPinnedPRCoexistsAndRoundTrips(t *testing.T) {
	src := "--- !pr\n" +
		"repo: o/n\nnumber: 1\ntitle: T\nurl: u\nrole: submitter\n" +
		"bucket: open\naction: awaiting_review\npriority: neutral\nprovider: primary\n" +
		"--- !issue\n" +
		"repo: o/n\nnumber: 2\ntitle: I\nurl: iu\nrole: author\n" +
		"bucket: open\naction: triage\npriority: neutral\nprovider: primary\n" +
		"--- !pinned-pr\n" +
		"repo: o/n\nnumber: 3\nrole: reviewer\n" +
		"--- !future\n" +
		"repo: o/n\nnumber: 4\nsomething: new\n"

	p := writeTemp(t, src)
	s, err := Read(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(s.PRs) != 1 || len(s.Issues) != 1 || len(s.Pinned) != 1 {
		t.Fatalf("want 1 pr/1 issue/1 pin, got %d/%d/%d", len(s.PRs), len(s.Issues), len(s.Pinned))
	}
	if s.Pinned[0].Repo != "o/n" || s.Pinned[0].Number != 3 || s.Pinned[0].Role != model.RoleReviewer {
		t.Errorf("pin decoded wrong: %+v", s.Pinned[0])
	}

	out := filepath.Join(t.TempDir(), "prs.pr.yaml")
	if err := s.Write(out); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(out)
	text := string(data)
	for _, want := range []string{"!pr", "!issue", "!pinned-pr", "!future"} {
		if !strings.Contains(text, want) {
			t.Errorf("rewritten store lost %s tag:\n%s", want, text)
		}
	}

	reread, err := Read(out)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if len(reread.Pinned) != 1 || reread.Pinned[0].Number != 3 {
		t.Errorf("pin did not survive round-trip: %+v", reread.Pinned)
	}
}

func TestValidatePinnedPRRejectsBad(t *testing.T) {
	cases := []struct {
		name string
		pin  model.PinnedPR
	}{
		{"no repo", model.PinnedPR{Number: 1, Role: model.RoleSubmitter}},
		{"non-positive number", model.PinnedPR{Repo: "o/n", Number: 0, Role: model.RoleSubmitter}},
		{"empty role", model.PinnedPR{Repo: "o/n", Number: 1}},
		{"bad role", model.PinnedPR{Repo: "o/n", Number: 1, Role: model.Role("boss")}},
	}
	for _, c := range cases {
		if err := validatePinnedPR(c.pin); err == nil {
			t.Errorf("%s: expected validation error", c.name)
		}
	}
}

// TestPinAddThenUpdate covers the store side of P.4: a first pin is added, and a
// re-pin of the same PR under a different role updates in place rather than
// duplicating.
func TestPinAddThenUpdate(t *testing.T) {
	s := &Store{}
	added, err := s.Pin(model.PinnedPR{Repo: "o/n", Number: 5, Role: model.RoleSubmitter})
	if err != nil || !added {
		t.Fatalf("first pin should add: added=%v err=%v", added, err)
	}
	added, err = s.Pin(model.PinnedPR{Repo: "o/n", Number: 5, Role: model.RoleReviewer})
	if err != nil {
		t.Fatalf("re-pin err: %v", err)
	}
	if added {
		t.Error("re-pin of same PR should update, not add")
	}
	if len(s.Pinned) != 1 || s.Pinned[0].Role != model.RoleReviewer {
		t.Errorf("want single pin with updated role reviewer, got %+v", s.Pinned)
	}
}

func TestPinRejectsInvalid(t *testing.T) {
	s := &Store{}
	if _, err := s.Pin(model.PinnedPR{Repo: "o/n", Number: 1, Role: model.Role("x")}); err == nil {
		t.Error("Pin must reject an invalid role")
	}
	if len(s.Pinned) != 0 {
		t.Error("invalid pin must not be recorded")
	}
}

// TestUnpinRemovesOnlyPin covers the store side of P.5: Unpin removes the
// matching pin and leaves !pr/!issue untouched; unpinning an absent pin is a
// clean no-op.
// TestWriteEmptyStore is the regression for a bug live smoke-testing found (not
// the unit suite): unpinning the last document leaves an empty store, and
// writing it must produce an empty file rather than erroring on the yaml
// encoder's STREAM-START. It re-reads as an empty store.
func TestWriteEmptyStore(t *testing.T) {
	sp := filepath.Join(t.TempDir(), "prs.pr.yaml")
	s := &Store{}
	if err := s.Write(sp); err != nil {
		t.Fatalf("writing an empty store must succeed, got %v", err)
	}
	re, err := Read(sp)
	if err != nil {
		t.Fatalf("reread empty store: %v", err)
	}
	if len(re.PRs) != 0 || len(re.Issues) != 0 || len(re.Pinned) != 0 {
		t.Errorf("empty store did not round-trip empty: %+v", re)
	}
}

func TestUnpinRemovesOnlyPin(t *testing.T) {
	s := &Store{
		PRs:    []model.PR{{Repo: "o/n", Number: 5}},
		Pinned: []model.PinnedPR{{Repo: "o/n", Number: 5, Role: model.RoleSubmitter}},
	}
	if removed := s.Unpin("o/n#5"); !removed {
		t.Error("expected pin removal")
	}
	if len(s.Pinned) != 0 {
		t.Error("pin not removed")
	}
	if len(s.PRs) != 1 {
		t.Error("Unpin must not touch !pr documents")
	}
	if removed := s.Unpin("o/n#5"); removed {
		t.Error("unpin of absent pin must be a no-op (idempotent)")
	}
}
