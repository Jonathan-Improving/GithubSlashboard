package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "prs.pr.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadMissingIsEmpty(t *testing.T) {
	s, err := Read(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing file should be empty store, got %v", err)
	}
	if len(s.PRs) != 0 {
		t.Errorf("expected empty store, got %d PRs", len(s.PRs))
	}
}

func TestReadMalformedAborts(t *testing.T) {
	p := writeTemp(t, "--- !pr\nrepo: [unterminated\n")
	if _, err := Read(p); err == nil {
		t.Error("malformed YAML must abort (TDD 2.4)")
	}
}

func TestRoundTripAndTag(t *testing.T) {
	s := &Store{}
	s.Merge([]model.PR{{
		Repo: "o/n", Number: 1, Title: "T", URL: "u",
		Role: model.RoleSubmitter, Bucket: model.BucketOpen,
		Action: model.ActionAwaitingReview, Priority: model.PriorityNeutral,
	}})
	dir := t.TempDir()
	p := filepath.Join(dir, "prs.pr.yaml")
	if err := s.Write(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "!pr") {
		t.Errorf("document not tagged !pr (TDD 2.1):\n%s", data)
	}

	reread, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.PRs) != 1 || reread.PRs[0].Repo != "o/n" {
		t.Errorf("round-trip lost data: %+v", reread.PRs)
	}
}

func TestOperatorStalePreserved(t *testing.T) {
	// Existing store has an operator override.
	prior := &Store{}
	prior.PRs = []model.PR{{
		Repo: "o/n", Number: 1, Title: "T", URL: "u",
		Role: model.RoleSubmitter, Bucket: model.BucketStale,
		Priority: model.PriorityNeutral, OperatorStale: true,
	}}

	// Fresh fetch does NOT carry the override.
	fresh := []model.PR{{
		Repo: "o/n", Number: 1, Title: "T", URL: "u",
		Role: model.RoleSubmitter, Bucket: model.BucketOpen,
		Action: model.ActionAwaitingReview, Priority: model.PriorityNeutral,
	}}
	prior.Merge(fresh)
	if !prior.PRs[0].OperatorStale {
		t.Error("operator_stale must survive re-run (TDD 2.2)")
	}
}

func TestIdempotentWrite(t *testing.T) {
	build := func() *Store {
		s := &Store{}
		s.Merge([]model.PR{
			{Repo: "b/repo", Number: 2, Title: "B", URL: "u2", Role: model.RoleReviewer, Bucket: model.BucketMerged, Priority: model.PriorityNeutral},
			{Repo: "a/repo", Number: 1, Title: "A", URL: "u1", Role: model.RoleSubmitter, Bucket: model.BucketOpen, Action: model.ActionMergeReady, Priority: model.PriorityElevated},
		})
		return s
	}
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.yaml")
	p2 := filepath.Join(dir, "b.yaml")
	if err := build().Write(p1); err != nil {
		t.Fatal(err)
	}
	if err := build().Write(p2); err != nil {
		t.Fatal(err)
	}
	d1, _ := os.ReadFile(p1)
	d2, _ := os.ReadFile(p2)
	if string(d1) != string(d2) {
		t.Errorf("write not deterministic (TDD 2.3):\n---1---\n%s\n---2---\n%s", d1, d2)
	}
}

func TestUnknownDocPreserved(t *testing.T) {
	// !issue is a recognized entity type now, so the stand-in for a *future*
	// entity must be a tag this build genuinely does not know (TDD 2.5).
	content := "--- !pr\nrepo: o/n\nnumber: 1\ntitle: T\nurl: u\nrole: submitter\nbucket: open\naction: awaiting_review\npriority: neutral\n--- !discussion\nrepo: o/n\nnumber: 99\ntitle: some discussion\n"
	p := writeTemp(t, content)
	s, err := Read(p)
	if err != nil {
		t.Fatalf("mixed entity docs should read (TDD 2.5): %v", err)
	}
	if len(s.PRs) != 1 {
		t.Errorf("expected 1 PR, got %d", len(s.PRs))
	}
	out := filepath.Join(t.TempDir(), "out.yaml")
	if err := s.Write(out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "!discussion") || !strings.Contains(string(data), "some discussion") {
		t.Errorf("unknown !discussion doc not preserved (TDD 2.5):\n%s", data)
	}
}
