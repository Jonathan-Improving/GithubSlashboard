package github

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gh "github.com/google/go-github/v66/github"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// newTestSearchClient builds a Client whose REST search calls hit ts instead
// of real GitHub, so searchPRs/searchIssues can be exercised against a canned
// response without any network access.
func newTestSearchClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	rest := gh.NewClient(ts.Client())
	base, err := url.Parse(ts.URL + "/")
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	rest.BaseURL = base
	return &Client{rest: rest, login: "test-operator"}
}

// searchIssuesResponse renders a minimal GitHub search/issues JSON body.
// incomplete signals the incomplete_results flag a server-side search
// timeout sets while still returning 200 (ANTI-PATTERNS #11).
func searchIssuesResponse(incomplete bool, prNumbers ...int) string {
	var items []string
	for _, n := range prNumbers {
		items = append(items, fmt.Sprintf(`{
			"number": %d,
			"title": "test PR %d",
			"html_url": "https://github.com/octo/example/pull/%d",
			"pull_request": {"url": "https://api.github.com/repos/octo/example/pulls/%d"},
			"state": "open",
			"created_at": "2026-01-01T00:00:00Z"
		}`, n, n, n, n))
	}
	return fmt.Sprintf(`{"total_count": %d, "incomplete_results": %t, "items": [%s]}`,
		len(prNumbers), incomplete, strings.Join(items, ","))
}

func TestSearchPRsErrorsOnIncompleteResults(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchIssuesResponse(true, 295)))
	}))
	defer ts.Close()

	c := newTestSearchClient(t, ts)
	_, err := c.searchPRs(t.Context(), "is:pr author:test-operator", model.RoleSubmitter)
	if err == nil {
		t.Fatal("expected an error on incomplete_results, got nil")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error %q should mention incomplete results", err)
	}
}

func TestSearchPRsSucceedsOnCompleteResults(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchIssuesResponse(false, 295)))
	}))
	defer ts.Close()

	c := newTestSearchClient(t, ts)
	prs, err := c.searchPRs(t.Context(), "is:pr author:test-operator", model.RoleSubmitter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prs) != 1 || prs[0].Number != 295 {
		t.Fatalf("prs = %+v, want exactly PR #295", prs)
	}
	if prs[0].Role != model.RoleSubmitter {
		t.Errorf("role = %v, want submitter", prs[0].Role)
	}
}

func TestSearchIssuesErrorsOnIncompleteResults(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count": 1, "incomplete_results": true, "items": [{
			"number": 10,
			"title": "test issue",
			"html_url": "https://github.com/octo/example/issues/10",
			"state": "open",
			"created_at": "2026-01-01T00:00:00Z",
			"comments": 1
		}]}`))
	}))
	defer ts.Close()

	c := newTestSearchClient(t, ts)
	_, err := c.searchIssues(t.Context(), "is:issue author:test-operator", model.IssueRoleAuthor)
	if err == nil {
		t.Fatal("expected an error on incomplete_results, got nil")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error %q should mention incomplete results", err)
	}
}

// TestFetchTrackedNeverMisassignsRoleOnIncompleteAuthoredSearch is the
// end-to-end regression for the actual observed defect: a self-authored PR
// (valkey-io/valkey-glide-ruby#295) was persisted with role=reviewer after
// the author: search returned an incomplete page mid-timeout, silently
// omitting a PR the operator had in fact authored, so it fell through to the
// reviewed-by: search and was inserted fresh as reviewer with no error
// anywhere in the run (ANTI-PATTERNS #11). FetchTracked must now abort
// instead of persisting that wrong role.
func TestFetchTrackedNeverMisassignsRoleOnIncompleteAuthoredSearch(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls++
		q := r.URL.Query().Get("q")
		switch {
		case strings.Contains(q, "author:"):
			// The author: search times out mid-scan: incomplete, and misses
			// the PR the operator actually authored.
			_, _ = w.Write([]byte(searchIssuesResponse(true)))
		default:
			// review-requested: / reviewed-by: search comes back complete and
			// (incorrectly, from the operator's real authorship) includes it.
			_, _ = w.Write([]byte(searchIssuesResponse(false, 295)))
		}
	}))
	defer ts.Close()

	c := newTestSearchClient(t, ts)
	_, err := c.FetchTracked(t.Context(), nil, false, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("expected FetchTracked to abort on an incomplete authored-search page, got nil error")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error %q should mention incomplete results", err)
	}
}

// TestFetchTrackedRestampsRoleOnCarriedTerminal is the regression for the
// residual half of the ANTI-PATTERNS #11 defect: once a terminal (merged or
// closed) PR is persisted with the wrong role, the terminal-skip carry-forward
// must still re-derive role from the fresh searches rather than carry the stale
// stored value forever. Here the store holds octo/example#295 as
// reviewer/merged, while the fresh author: search correctly returns it — so the
// submitter-wins merge resolves submitter, and the carried record must come out
// as submitter, not the stored reviewer. No per-PR crawl happens on this path,
// so the search server alone is enough.
func TestFetchTrackedRestampsRoleOnCarriedTerminal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("q")
		switch {
		case strings.Contains(q, "author:"):
			_, _ = w.Write([]byte(searchIssuesResponse(false, 295)))
		default:
			_, _ = w.Write([]byte(searchIssuesResponse(false)))
		}
	}))
	defer ts.Close()

	c := newTestSearchClient(t, ts)
	prior := map[string]model.PR{
		"octo/example#295": {
			Repo: "octo/example", Number: 295,
			Role: model.RoleReviewer, Bucket: model.BucketMerged,
		},
	}

	out, err := c.FetchTracked(t.Context(), prior, false, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("FetchTracked: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want the single carried PR, got %d", len(out))
	}
	got := out[0]
	if got.Number != 295 {
		t.Fatalf("wrong PR carried: %+v", got)
	}
	if got.Role != model.RoleSubmitter {
		t.Errorf("carried terminal role must be re-derived to submitter, got %v", got.Role)
	}
	if got.Bucket != model.BucketMerged {
		t.Errorf("carried terminal bucket must be preserved as merged, got %v", got.Bucket)
	}
	if got.GitHubState != model.GitHubStateMerged {
		t.Errorf("carried terminal GitHubState must be restored to merged, got %v", got.GitHubState)
	}
}
