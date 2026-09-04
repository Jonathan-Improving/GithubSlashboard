package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TestIssuesAndPRsCoexist covers TDD 8.7: an issue is its own tagged document
// alongside the PR documents, and a store holding both round-trips intact.
func TestIssuesAndPRsCoexist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prs.pr.yaml")

	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := &Store{}
	s.Merge([]model.PR{{
		Repo: "a/x", Number: 1, Title: "PR", URL: "u1",
		Role: model.RoleSubmitter, Created: created,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityNeutral,
	}})
	s.MergeIssues([]model.Issue{{
		Repo: "a/x", Number: 2, Title: "Issue", URL: "u2",
		Role: model.IssueRoleAuthor, Created: created,
		Bucket: model.IssueBucketOpen, Action: model.IssueActionTriage,
		Priority: model.PriorityNeutral,
	}})

	if err := s.Write(path); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(raw), "!pr") {
		t.Error("store is missing the !pr tag (TDD 2.1)")
	}
	if !strings.Contains(string(raw), "!issue") {
		t.Error("store is missing the !issue tag (TDD 8.7)")
	}

	back, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(back.PRs) != 1 {
		t.Fatalf("got %d PRs, want 1", len(back.PRs))
	}
	if len(back.Issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(back.Issues))
	}
	if back.Issues[0].Action != model.IssueActionTriage {
		t.Errorf("issue action = %q, want triage after round-trip", back.Issues[0].Action)
	}
}

// TestLastActivityPersists proves last_activity survives a write/read round trip.
// It must, because a terminal item is carried forward without a re-crawl, so the
// stored value is the only activity date the render (a pure function of the store)
// will ever see for it.
func TestLastActivityPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prs.pr.yaml")

	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	activity := time.Date(2026, 8, 25, 9, 30, 0, 0, time.UTC)

	s := &Store{}
	s.Merge([]model.PR{{
		Repo: "a/x", Number: 1, Title: "PR", URL: "u1",
		Role: model.RoleSubmitter, Created: created, LastActivity: activity,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityNeutral,
	}})
	s.MergeIssues([]model.Issue{{
		Repo: "a/x", Number: 2, Title: "Issue", URL: "u2",
		Role: model.IssueRoleAuthor, Created: created, LastActivity: activity,
		Bucket: model.IssueBucketOpen, Action: model.IssueActionTriage,
		Priority: model.PriorityNeutral,
	}})

	if err := s.Write(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !back.PRs[0].LastActivity.Equal(activity) {
		t.Errorf("PR last_activity = %s, want %s after round trip", back.PRs[0].LastActivity, activity)
	}
	if !back.Issues[0].LastActivity.Equal(activity) {
		t.Errorf("issue last_activity = %s, want %s after round trip", back.Issues[0].LastActivity, activity)
	}
}

// TestLastActivityCarriedForwardOnMerge proves a fresh record with no computed
// activity date inherits the stored one — the terminal-skip case, where the item
// was never crawled so its trail (and derived date) is empty.
func TestLastActivityCarriedForwardOnMerge(t *testing.T) {
	activity := time.Date(2026, 8, 25, 9, 30, 0, 0, time.UTC)
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mergedAt := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)

	s := &Store{PRs: []model.PR{{
		Repo: "a/x", Number: 1, Role: model.RoleSubmitter, Created: created,
		LastActivity: activity, Bucket: model.BucketMerged, Priority: model.PriorityNeutral,
	}}}

	s.Merge([]model.PR{{
		Repo: "a/x", Number: 1, Role: model.RoleSubmitter, Created: created,
		MergedAt: &mergedAt, Bucket: model.BucketMerged, Priority: model.PriorityNeutral,
	}})

	if !s.PRs[0].LastActivity.Equal(activity) {
		t.Errorf("last_activity = %s, want the carried-forward %s", s.PRs[0].LastActivity, activity)
	}
}

// TestLastActivityBackfilledFromTerminalDate proves a terminal record with no
// activity date at all — a store written before the field was persisted — is
// backfilled from its merge/close date rather than left blank forever, since
// nothing will re-crawl it.
func TestLastActivityBackfilledFromTerminalDate(t *testing.T) {
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mergedAt := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	closedAt := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	s := &Store{}
	s.Merge([]model.PR{
		{
			Repo: "a/x", Number: 1, Role: model.RoleSubmitter, Created: created,
			MergedAt: &mergedAt, Bucket: model.BucketMerged, Priority: model.PriorityNeutral,
		},
		{
			Repo: "a/x", Number: 2, Role: model.RoleSubmitter, Created: created,
			ClosedAt: &closedAt, Bucket: model.BucketClosed,
			CloseReason: model.CloseReasonCancelled, Priority: model.PriorityNeutral,
		},
		{
			// A live PR with no activity date stays blank: there is no honest
			// proxy for it, and it will be recomputed on the next crawl.
			Repo: "a/x", Number: 3, Role: model.RoleSubmitter, Created: created,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
			Priority: model.PriorityNeutral,
		},
	})

	if !s.PRs[0].LastActivity.Equal(mergedAt) {
		t.Errorf("merged PR last_activity = %s, want the merge date %s", s.PRs[0].LastActivity, mergedAt)
	}
	if !s.PRs[1].LastActivity.Equal(closedAt) {
		t.Errorf("closed PR last_activity = %s, want the close date %s", s.PRs[1].LastActivity, closedAt)
	}
	if !s.PRs[2].LastActivity.IsZero() {
		t.Errorf("live PR last_activity = %s, want zero — no proxy should be invented", s.PRs[2].LastActivity)
	}

	closedIssue := closedAt
	s.MergeIssues([]model.Issue{{
		Repo: "a/x", Number: 9, Role: model.IssueRoleAuthor, Created: created,
		ClosedAt: &closedIssue, Bucket: model.IssueBucketClosed,
		CloseReason: model.IssueCloseReasonCompleted, Priority: model.PriorityNeutral,
	}})
	if !s.Issues[0].LastActivity.Equal(closedAt) {
		t.Errorf("closed issue last_activity = %s, want the close date %s", s.Issues[0].LastActivity, closedAt)
	}
}

// for issues (TDD 8.7 referencing 2.3): a fresh classification that does not
// carry the override must not erase it.
// TestCIFailingPersists proves the ci_failing flag survives a round trip. It must,
// because the render is a pure function of the store: an unpersisted flag would
// vanish from the document on any run that did not re-derive it (TDD 4.10).
func TestCIFailingPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prs.pr.yaml")
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	s := &Store{}
	s.Merge([]model.PR{
		{
			Repo: "a/x", Number: 1, Title: "Red build", URL: "u1",
			Role: model.RoleSubmitter, Created: created, LastActivity: created,
			Bucket: model.BucketOpen, Action: model.ActionReviewFeedback,
			Priority: model.PriorityNeutral, CIFailing: true,
		},
		{
			Repo: "a/x", Number: 2, Title: "Green build", URL: "u2",
			Role: model.RoleSubmitter, Created: created, LastActivity: created,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
			Priority: model.PriorityNeutral, CIFailing: false,
		},
	})

	if err := s.Write(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !back.PRs[0].CIFailing {
		t.Error("ci_failing = false after round trip, want true")
	}
	if back.PRs[1].CIFailing {
		t.Error("ci_failing = true on the green PR after round trip")
	}
	// The flag and the action are independent columns of the record, so the
	// action must be intact alongside it (TDD 4.11).
	if back.PRs[0].Action != model.ActionReviewFeedback {
		t.Errorf("action = %q, want review_feedback preserved alongside the flag", back.PRs[0].Action)
	}
}

// TestProviderPersists covers TDD 6.16: the provenance field always round-trips
// through the store, distinguishing a primary-judged record from a
// fallback-judged one exactly as CIFailing distinguishes a red build from a
// green one.
func TestProviderPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prs.pr.yaml")
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	s := &Store{}
	s.Merge([]model.PR{
		{
			Repo: "a/x", Number: 1, Title: "Judged by primary", URL: "u1",
			Role: model.RoleSubmitter, Created: created, LastActivity: created,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
			Priority: model.PriorityNeutral, Provider: model.ProviderSourcePrimary,
		},
		{
			Repo: "a/x", Number: 2, Title: "Judged by fallback", URL: "u2",
			Role: model.RoleSubmitter, Created: created, LastActivity: created,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
			Priority: model.PriorityNeutral, Provider: model.ProviderSourceFallback,
		},
	})

	if err := s.Write(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if back.PRs[0].Provider != model.ProviderSourcePrimary {
		t.Errorf("provider = %q, want primary", back.PRs[0].Provider)
	}
	if back.PRs[1].Provider != model.ProviderSourceFallback {
		t.Errorf("provider = %q, want fallback", back.PRs[1].Provider)
	}

	// The field must appear explicitly in the raw YAML — never omitted, unlike
	// ci_failing/unverified's omitempty convention (TDD 6.16: Option B, always
	// disclose).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if got := strings.Count(string(raw), "provider:"); got != 2 {
		t.Errorf("found %d \"provider:\" lines in the raw store, want 2 (one per PR, never omitted)", got)
	}
}

// TestProviderRejectsInvalidValue covers the flip side of the pre-migration
// tolerance: an empty provider is accepted (a record written before the field
// existed), but a non-empty value outside the closed set is not.
func TestProviderRejectsInvalidValue(t *testing.T) {
	pr := model.PR{
		Repo: "a/x", Number: 1, Role: model.RoleSubmitter,
		Bucket: model.BucketOpen, Priority: model.PriorityNeutral,
		Provider: model.ProviderSource("bogus"),
	}
	if err := validatePR(pr); err == nil {
		t.Error("validatePR should reject an out-of-set provider value")
	}

	pr.Provider = ""
	if err := validatePR(pr); err != nil {
		t.Errorf("validatePR should accept an empty provider (pre-migration record), got %v", err)
	}
}

// TestIssueOperatorStaleSurvivesRefresh covers the operator-set state guarantee
// for issues (TDD 8.7 referencing 2.3): a fresh classification that does not
// carry the override must not erase it.
func TestIssueOperatorStaleSurvivesRefresh(t *testing.T) {
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := &Store{Issues: []model.Issue{{
		Repo: "a/x", Number: 2, Title: "Issue", URL: "u2",
		Role: model.IssueRoleAuthor, Created: created,
		Bucket: model.IssueBucketStale, Priority: model.PriorityNeutral,
		OperatorStale: true,
	}}}

	// The fresh record has the flag cleared, as an acquisition pass would.
	s.MergeIssues([]model.Issue{{
		Repo: "a/x", Number: 2, Title: "Issue", URL: "u2",
		Role: model.IssueRoleAuthor, Created: created,
		Bucket: model.IssueBucketOpen, Action: model.IssueActionTriage,
		Priority: model.PriorityNeutral,
	}})

	if !s.Issues[0].OperatorStale {
		t.Error("operator_stale was lost on merge (TDD 8.7)")
	}
}
