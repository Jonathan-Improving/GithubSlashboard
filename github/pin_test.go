package github

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestApplyPinsCaseAForcesRole covers P.2: when a search already discovered a
// PR under one role, a pin under a different role wins — the record carries the
// pinned role. Case A does not crawl (the record is already in the set), so no
// server is needed.
func TestApplyPinsCaseAForcesRole(t *testing.T) {
	c := &Client{login: "op"}
	discovered := []model.PR{{
		Repo: "o/n", Number: 7, Role: model.RoleSubmitter, Bucket: model.BucketOpen,
	}}
	pins := []model.PinnedPR{{Repo: "o/n", Number: 7, Role: model.RoleReviewer}}

	out, err := c.applyPins(t.Context(), discovered, nil, false, pins, discardLog())
	if err != nil {
		t.Fatalf("applyPins: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 PR, got %d", len(out))
	}
	if out[0].Role != model.RoleReviewer {
		t.Errorf("pin must win: role = %v, want reviewer", out[0].Role)
	}
}

// TestApplyPinsRestampsCarriedTerminal covers P.3: a settled pinned PR carried
// forward from the store (terminal-skip) is re-stamped with the pinned role
// each run — the pin, not the carried record, is the source of truth for role.
// The terminal carry-forward path does not crawl.
func TestApplyPinsRestampsCarriedTerminal(t *testing.T) {
	c := &Client{login: "op"}
	prior := map[string]model.PR{
		"o/n#9": {Repo: "o/n", Number: 9, Role: model.RoleSubmitter, Bucket: model.BucketMerged},
	}
	pins := []model.PinnedPR{{Repo: "o/n", Number: 9, Role: model.RoleReviewer}}

	out, err := c.applyPins(t.Context(), nil, prior, false, pins, discardLog())
	if err != nil {
		t.Fatalf("applyPins: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want the carried terminal PR force-included, got %d", len(out))
	}
	if out[0].Role != model.RoleReviewer {
		t.Errorf("carried terminal role must be re-stamped from the pin: got %v", out[0].Role)
	}
	if out[0].GitHubState != model.GitHubStateMerged {
		t.Errorf("carried terminal GitHubState must be restored: got %v", out[0].GitHubState)
	}
}

// TestApplyPinsBadPinSkipped covers P.7: a pin whose PR 404s is skipped, not
// fatal — applyPins returns the rest of the set with no error.
func TestApplyPinsBadPinSkipped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer ts.Close()

	c := newTestSearchClient(t, ts)
	discovered := []model.PR{{Repo: "o/n", Number: 1, Role: model.RoleSubmitter, Bucket: model.BucketOpen}}
	pins := []model.PinnedPR{{Repo: "ghost/repo", Number: 404, Role: model.RoleReviewer}}

	out, err := c.applyPins(t.Context(), discovered, nil, false, pins, discardLog())
	if err != nil {
		t.Fatalf("a 404 pin must be skipped, not fatal: %v", err)
	}
	if len(out) != 1 || out[0].Number != 1 {
		t.Errorf("the good PR must survive and the bad pin be dropped, got %+v", out)
	}
}

// TestApplyPinsCaseBForceIncludes covers P.1: a pin for a PR no search returned
// is fetched and added under its pinned role. A closed PR keeps the crawl to the
// endpoints a minimal fake can serve (no CI/threads/GraphQL, which are open-only).
func TestApplyPinsCaseBForceIncludes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/pulls/42"):
			_, _ = w.Write([]byte(`{
				"number": 42, "title": "Pinned closed PR",
				"html_url": "https://github.com/o/n/pull/42",
				"state": "closed", "merged": false,
				"closed_at": "2026-02-02T00:00:00Z",
				"created_at": "2026-01-01T00:00:00Z"
			}`))
		case strings.Contains(p, "/issues/42/comments"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(p, "/pulls/42/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(p, "/issues/42/timeline"):
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer ts.Close()

	c := newTestSearchClient(t, ts)
	pins := []model.PinnedPR{{Repo: "o/n", Number: 42, Role: model.RoleReviewer}}

	out, err := c.applyPins(t.Context(), nil, nil, false, pins, discardLog())
	if err != nil {
		t.Fatalf("applyPins Case B: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want the pinned PR force-included, got %d", len(out))
	}
	got := out[0]
	if got.Number != 42 || got.Role != model.RoleReviewer {
		t.Errorf("force-included PR wrong: %+v", got)
	}
	if got.Title != "Pinned closed PR" {
		t.Errorf("title should be backfilled from the PR resource, got %q", got.Title)
	}
	if got.GitHubState != model.GitHubStateClosed {
		t.Errorf("state should be closed, got %v", got.GitHubState)
	}
}
