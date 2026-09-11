package render

import (
	"strings"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

var issueNow = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

func sampleIssues() []model.Issue {
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	closed := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	recent := issueNow.Add(-3 * 24 * time.Hour)
	return []model.Issue{
		{
			Repo: "a/x", Number: 1, Title: "Authored open", URL: "ui1",
			Role: model.IssueRoleAuthor, Created: created, LastActivity: recent,
			Bucket: model.IssueBucketOpen, Action: model.IssueActionAwaitingOthers,
			Priority: model.PriorityNeutral, Companion: "waiting on upstream fix", Emoji: "⏳",
		},
		{
			Repo: "a/x", Number: 2, Title: "Authored stale", URL: "ui2",
			Role: model.IssueRoleAuthor, Created: created, LastActivity: created,
			Bucket: model.IssueBucketStale, Priority: model.PriorityNeutral,
		},
		{
			Repo: "a/x", Number: 3, Title: "Participating open", URL: "ui3",
			Role: model.IssueRoleParticipant, Created: created, LastActivity: recent,
			Bucket: model.IssueBucketOpen, Action: model.IssueActionAwaitingResponse,
			Priority: model.PriorityNeutral, Companion: "maintainer asked a question", Emoji: "❓",
		},
		{
			Repo: "a/x", Number: 4, Title: "Authored closed", URL: "ui4",
			Role: model.IssueRoleAuthor, Created: created, ClosedAt: &closed, LastActivity: closed,
			Bucket: model.IssueBucketClosed, CloseReason: model.IssueCloseReasonCompleted,
			Priority: model.PriorityNeutral, Companion: "fixed and released", Emoji: "✅",
		},
		{
			Repo: "a/x", Number: 5, Title: "Participating closed", URL: "ui5",
			Role: model.IssueRoleParticipant, Created: created, ClosedAt: &closed, LastActivity: closed,
			Bucket: model.IssueBucketClosed, CloseReason: model.IssueCloseReasonNotPlanned,
			Priority: model.PriorityNeutral,
		},
	}
}

// TestIssuesSectionStructure covers TDD 8.6: the document ends with an Issues
// section holding three sections, with the closed one split by role.
func TestIssuesSectionStructure(t *testing.T) {
	md := Render(nil, sampleIssues(), "x", issueNow)

	for _, want := range []string{
		"# 🗒 Issues",
		"## ✍ Authored (1)",
		"## 💬 Participating (1)",
		"## ☠ Stale (1)",
		"## ⛔ Closed (2)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("document missing %q (TDD 8.6)\n%s", want, md)
		}
	}

	// Stale and Closed each nest a per-role H3 pair, so no table needs a Role
	// column.
	if strings.Count(md, "### ✍ Authored") != 2 || strings.Count(md, "### 💬 Participating") != 2 {
		t.Errorf("expected per-role H3s under both Stale and Closed (TDD 8.6)\n%s", md)
	}

	// The Issues H1 must come after the reviewer section, separated by a rule.
	if !strings.Contains(md, "---\n\n# 🗒 Issues") {
		t.Error("Issues section is not separated from the PR content by a horizontal rule (TDD 8.6)")
	}
}

// TestIssueTitleRenamed covers TDD 8.6: the document title reflects that more
// than PRs are tracked.
func TestIssueTitleRenamed(t *testing.T) {
	md := Render(nil, nil, "x", issueNow)
	if !strings.HasPrefix(md, "# 📋 GithubSlashboard\n") {
		t.Errorf("document title is not the tool name (TDD 8.6)\n%s", md[:min(80, len(md))])
	}
	if strings.Contains(md, "PR Tracker") {
		t.Error("document still carries the PR-only title (TDD 8.6)")
	}
}

// TestSummaryCarriesIssueCounts covers TDD 8.6: the summary reports issue counts
// alongside PR counts, and the merged column is a dash for issues because an
// issue cannot be merged.
func TestSummaryCarriesIssueCounts(t *testing.T) {
	md := Render(nil, sampleIssues(), "x", issueNow)

	for _, want := range []string{
		"| Author (issue) | 1 | 1 | — | 1 | 3 |",
		"| Participant (issue) | 1 | 0 | — | 1 | 2 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("summary missing %q (TDD 8.6)\n%s", want, md)
		}
	}
}

// TestClosedIssueOnlyUnderClosed covers TDD 8.6: a closed issue appears only in
// the Closed section.
func TestClosedIssueOnlyUnderClosed(t *testing.T) {
	md := Render(nil, sampleIssues(), "x", issueNow)

	closedIdx := strings.Index(md, "## ⛔ Closed")
	if closedIdx < 0 {
		t.Fatal("no Closed section")
	}
	before, archived := md[:closedIdx], md[closedIdx:]

	if strings.Contains(before, "Authored closed") {
		t.Error("a closed issue appears above the Closed section (TDD 8.6)")
	}
	if !strings.Contains(archived, "Authored closed") {
		t.Error("a closed issue is missing from the Closed section (TDD 8.6)")
	}
}

// TestClosedIssuesSortNewestFirst covers TDD 3.6 for issues: the Closed
// section orders by ClosedAt, most recent first, within each role's table.
func TestClosedIssuesSortNewestFirst(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		// Number order (1 < 2) is the deliberate inverse of date order.
		{Repo: "a/x", Number: 1, Title: "Old closed", URL: "ui1", Role: model.IssueRoleAuthor,
			Created: created, ClosedAt: &older, Bucket: model.IssueBucketClosed,
			CloseReason: model.IssueCloseReasonCompleted, Priority: model.PriorityNeutral},
		{Repo: "a/x", Number: 2, Title: "New closed", URL: "ui2", Role: model.IssueRoleAuthor,
			Created: created, ClosedAt: &newer, Bucket: model.IssueBucketClosed,
			CloseReason: model.IssueCloseReasonCompleted, Priority: model.PriorityNeutral},
	}
	md := Render(nil, issues, "x", issueNow)

	iNew := strings.Index(md, "New closed")
	iOld := strings.Index(md, "Old closed")
	if iNew == -1 || iOld == -1 {
		t.Fatalf("expected both closed issue rows present:\n%s", md)
	}
	if iOld < iNew {
		t.Errorf("newer closed issue should precede older one:\n%s", md)
	}
}

func TestStaleIssueInItsOwnSection(t *testing.T) {
	// TDD 8.6: a stale issue is archived under the Stale section, split by
	// role, and does not appear in an active section.
	md := Render(nil, sampleIssues(), "x", issueNow)

	staleIdx := strings.Index(md, "## ☠ Stale")
	closedIdx := strings.Index(md, "## ⛔ Closed")
	if staleIdx < 0 || closedIdx < 0 {
		t.Fatal("missing Stale or Closed section")
	}
	if staleIdx > closedIdx {
		t.Error("Stale should precede Closed — live-ish work before the archive")
	}

	active, staleBlock := md[:staleIdx], md[staleIdx:closedIdx]
	if strings.Contains(active, "Authored stale") {
		t.Error("a stale issue appears in an active section (TDD 8.6)")
	}
	if !strings.Contains(staleBlock, "Authored stale") {
		t.Errorf("a stale issue is missing from the Stale section (TDD 8.6)\n%s", staleBlock)
	}
	// The row is not additionally flagged in place; the section says it.
	if strings.Contains(md, "stale —") {
		t.Error("stale rows should not carry a redundant in-place flag now that they have their own section")
	}
}

// TestNoteLessIssueRowReadsAsFact covers TDD 8.3's rendering half: a row with no
// inferred note renders its disposition as a fact, not as unavailable or failed.
func TestNoteLessIssueRowReadsAsFact(t *testing.T) {
	issues := []model.Issue{{
		Repo: "a/x", Number: 9, Title: "Quiet", URL: "u",
		Role: model.IssueRoleAuthor, Created: issueNow.Add(-48 * time.Hour),
		Bucket: model.IssueBucketOpen, Action: model.IssueActionTriage,
		Priority: model.PriorityNeutral,
	}}
	md := Render(nil, issues, "x", issueNow)

	if !strings.Contains(md, "no engagement yet") {
		t.Errorf("note-less issue row does not state its disposition (TDD 8.3)\n%s", md)
	}
	if strings.Contains(md, unavailableNote) {
		t.Error("note-less issue row renders as unavailable, which reads as a failure (TDD 8.3)")
	}
	if strings.Contains(md, "unverified") {
		t.Error("note-less issue row is marked unverified; nothing was attempted (TDD 8.3)")
	}
}

// TestIssueCloseReasonEmojiIsDeterministic proves the closed outcome carries a
// fixed per-enum glyph rather than an inferred one (TDD 8.2).
func TestIssueCloseReasonEmojiIsDeterministic(t *testing.T) {
	md := Render(nil, sampleIssues(), "x", issueNow)
	if !strings.Contains(md, "🚪 CLOSED / ✅ completed") {
		t.Errorf("completed outcome missing its deterministic glyph (TDD 8.2)\n%s", md)
	}
	if !strings.Contains(md, "🚪 CLOSED / 🚫 not_planned") {
		t.Errorf("not_planned outcome missing its deterministic glyph (TDD 8.2)\n%s", md)
	}
}

// TestEmptyIssueSetStillRendersSection proves the Issues section is present (and
// explicit about being empty) when nothing is tracked, so the document shape does
// not change run to run.
func TestEmptyIssueSetStillRendersSection(t *testing.T) {
	md := Render(sample(), nil, "x", issueNow)
	if !strings.Contains(md, "# 🗒 Issues") {
		t.Error("Issues section absent when there are no issues")
	}
	if !strings.Contains(md, "_No issues._") {
		t.Error("empty Issues section does not say so")
	}
}

// TestUpdatedColumnOnLiveTables covers the Updated column: live tables report how
// long ago an item last saw activity, and terminal tables do not carry the column
// because their merged/closed date already answers that.
func TestUpdatedColumnOnLiveTables(t *testing.T) {
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	merged := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	prs := []model.PR{
		{
			Repo: "a/x", Number: 1, Title: "Live submitter", URL: "u1",
			Role: model.RoleSubmitter, Created: created,
			LastActivity: issueNow.Add(-5 * 24 * time.Hour),
			Bucket:       model.BucketOpen, Action: model.ActionAuthorActive,
			Priority: model.PriorityNeutral, Companion: "iterating on feedback", Emoji: "🔧",
		},
		{
			Repo: "a/x", Number: 2, Title: "Live reviewer", URL: "u2",
			Role: model.RoleReviewer, Created: created,
			LastActivity: issueNow.Add(-2 * 24 * time.Hour),
			Bucket:       model.BucketOpen, Action: model.ActionAwaitingReview,
			Priority: model.PriorityNeutral, Companion: "awaiting our review", Emoji: "⏳",
		},
		{
			Repo: "a/x", Number: 3, Title: "Merged", URL: "u3",
			Role: model.RoleSubmitter, Created: created, MergedAt: &merged,
			LastActivity: merged,
			Bucket:       model.BucketMerged, Priority: model.PriorityNeutral,
			Companion: "shipped last week", Emoji: "📦",
		},
	}

	md := Render(prs, nil, "x", issueNow)

	if !strings.Contains(md, "| Repo | PR | Title | Created | Age | Updated | Action Needed |") {
		t.Error("submitter Open table is missing the Updated column")
	}
	if !strings.Contains(md, "| Repo | PR | Title | Updated | Our Review |") {
		t.Error("reviewer table is missing the Updated column")
	}
	if !strings.Contains(md, "| Repo | PR | Title | Created | Merged | Notes |") {
		t.Error("Merged table header changed; terminal tables should keep their shape")
	}
	if !strings.Contains(md, "| 5d |") {
		t.Errorf("submitter live row missing its 5d Updated value\n%s", md)
	}
	if !strings.Contains(md, "| 2d |") {
		t.Errorf("reviewer live row missing its 2d Updated value\n%s", md)
	}
}

// TestUpdatedUnknownRendersDash proves a record with no stored activity date
// renders a dash rather than an absurd age computed from a zero timestamp — the
// case for a terminal row in a store written before last_activity was persisted.
func TestUpdatedUnknownRendersDash(t *testing.T) {
	prs := []model.PR{{
		Repo: "a/x", Number: 1, Title: "No activity date", URL: "u1",
		Role: model.RoleSubmitter, Created: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		Bucket: model.BucketOpen, Action: model.ActionAuthorActive,
		Priority: model.PriorityNeutral, Companion: "iterating on feedback", Emoji: "🔧",
	}}

	md := Render(prs, nil, "x", issueNow)

	if strings.Contains(md, "20353d") || strings.Contains(md, "739") {
		t.Errorf("a zero activity date produced a computed age\n%s", md)
	}
	if !strings.Contains(md, "| — |") {
		t.Errorf("unknown Updated value should render as a dash\n%s", md)
	}
}

// TestStaleIssueTableShape proves the stale issue tables carry the same columns as
// the active ones, since a stale issue is still open upstream.
// TestActiveIssuesSortByLastActivity covers TDD 3.6a's extension to the issue
// Authored/Participating active sections: they sort by LastActivity, most
// recent first, rather than repo/number.
func TestActiveIssuesSortByLastActivity(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		// Number order (10 < 20) is the deliberate inverse of activity order.
		{Repo: "a/x", Number: 10, Title: "Newest activity", URL: "ui10", Role: model.IssueRoleAuthor,
			Created: created, LastActivity: newer, Bucket: model.IssueBucketOpen,
			Action: model.IssueActionAwaitingOthers, Priority: model.PriorityNeutral},
		{Repo: "a/x", Number: 20, Title: "Oldest activity", URL: "ui20", Role: model.IssueRoleAuthor,
			Created: created, LastActivity: older, Bucket: model.IssueBucketOpen,
			Action: model.IssueActionAwaitingOthers, Priority: model.PriorityNeutral},
	}
	md := Render(nil, issues, "x", issueNow)

	iNewest := strings.Index(md, "Newest activity")
	iOldest := strings.Index(md, "Oldest activity")
	if iNewest == -1 || iOldest == -1 {
		t.Fatalf("expected both authored-active rows present:\n%s", md)
	}
	if iOldest < iNewest {
		t.Errorf("active issues should be newest-activity-first:\n%s", md)
	}
}

// TestStaleIssuesSortByLastActivity covers TDD 3.6a's extension to the issue
// Stale sections: they sort by LastActivity, most recent first.
func TestStaleIssuesSortByLastActivity(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	issues := []model.Issue{
		{Repo: "a/x", Number: 10, Title: "Newest activity", URL: "ui10", Role: model.IssueRoleAuthor,
			Created: created, LastActivity: newer, Bucket: model.IssueBucketStale,
			Priority: model.PriorityNeutral},
		{Repo: "a/x", Number: 20, Title: "Oldest activity", URL: "ui20", Role: model.IssueRoleAuthor,
			Created: created, LastActivity: older, Bucket: model.IssueBucketStale,
			Priority: model.PriorityNeutral},
	}
	md := Render(nil, issues, "x", issueNow)

	iNewest := strings.Index(md, "Newest activity")
	iOldest := strings.Index(md, "Oldest activity")
	if iNewest == -1 || iOldest == -1 {
		t.Fatalf("expected both stale-authored rows present:\n%s", md)
	}
	if iOldest < iNewest {
		t.Errorf("stale issues should be newest-activity-first:\n%s", md)
	}
}

func TestStaleIssueTableShape(t *testing.T) {
	md := Render(nil, sampleIssues(), "x", issueNow)
	if !strings.Contains(md, "| Repo | Issue | Title | Created | Age | Updated | Reason |") {
		t.Errorf("stale issue table header missing or wrong shape\n%s", md)
	}
	if !strings.Contains(md, "| Repo | Issue | Title | Created | Age | Updated | Status |") {
		t.Errorf("active issue table header missing the Updated column\n%s", md)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
