package render

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// Issue section headings and markers. Issues reuse the PR bucket glyphs where
// the bucket means the same thing, so the two halves of the document read
// consistently.
const (
	secIssues        = "🗒"
	secIssueAuthored = "✍"
	secIssuePart     = "💬"
	secIssueStale    = "☠"
	secIssueClosed   = "⛔"

	// markIssueBallWithUs flags an issue awaiting a response from the operator,
	// used only when the model supplied no emoji of its own.
	markIssueBallWithUs = "⏳"
)

// issueCloseReasonEmoji maps each issue close reason to a deterministic glyph.
// Like the PR close-reason map it is not LLM-inferred: the reason is GitHub's own
// state_reason, a finite set, so a fixed emoji per value stays consistent run to
// run (TDD 8.2).
var issueCloseReasonEmoji = map[model.IssueCloseReason]string{
	model.IssueCloseReasonCompleted:  "✅",
	model.IssueCloseReasonNotPlanned: "🚫",
	model.IssueCloseReasonDuplicate:  "♻️",
	model.IssueCloseReasonReopened:   "🔁",
}

// writeIssuesSection emits the Issues half of the document: three sections —
// authored, participating, and closed — with the closed one split by role so an
// archived issue still shows whether the operator opened it or merely took part
// (TDD 8.6). A closed issue appears only under Closed; a stale issue appears in
// its role's active section, flagged by bucket rather than given a section of its
// own.
func writeIssuesSection(b *strings.Builder, issues []model.Issue, now time.Time) {
	b.WriteString(fmt.Sprintf("# %s Issues\n\n", secIssues))

	if len(issues) == 0 {
		b.WriteString("_No issues._\n\n")
		return
	}

	var authoredActive, partActive, staleAuthored, stalePart, closedAuthored, closedPart []model.Issue
	for _, i := range issues {
		switch {
		case i.Bucket == model.IssueBucketClosed && i.Role == model.IssueRoleAuthor:
			closedAuthored = append(closedAuthored, i)
		case i.Bucket == model.IssueBucketClosed:
			closedPart = append(closedPart, i)
		case i.Bucket == model.IssueBucketStale && i.Role == model.IssueRoleAuthor:
			staleAuthored = append(staleAuthored, i)
		case i.Bucket == model.IssueBucketStale:
			stalePart = append(stalePart, i)
		case i.Role == model.IssueRoleAuthor:
			authoredActive = append(authoredActive, i)
		default:
			partActive = append(partActive, i)
		}
	}
	sortIssueRows(authoredActive)
	sortIssueRows(partActive)
	sortIssueRows(staleAuthored)
	sortIssueRows(stalePart)
	sortClosedIssueRows(closedAuthored)
	sortClosedIssueRows(closedPart)

	writeActiveIssueTable(b, fmt.Sprintf("## %s Authored (%d)", secIssueAuthored, len(authoredActive)), authoredActive, now)
	writeActiveIssueTable(b, fmt.Sprintf("## %s Participating (%d)", secIssuePart, len(partActive)), partActive, now)

	b.WriteString(fmt.Sprintf("## %s Stale (%d)\n\n", secIssueStale, len(staleAuthored)+len(stalePart)))
	writeStaleIssueTable(b, fmt.Sprintf("### %s Authored (%d)", secIssueAuthored, len(staleAuthored)), staleAuthored, now)
	writeStaleIssueTable(b, fmt.Sprintf("### %s Participating (%d)", secIssuePart, len(stalePart)), stalePart, now)

	b.WriteString(fmt.Sprintf("## %s Closed (%d)\n\n", secIssueClosed, len(closedAuthored)+len(closedPart)))
	writeClosedIssueTable(b, fmt.Sprintf("### %s Authored (%d)", secIssueAuthored, len(closedAuthored)), closedAuthored)
	writeClosedIssueTable(b, fmt.Sprintf("### %s Participating (%d)", secIssuePart, len(closedPart)), closedPart)
}

// writeActiveIssueTable emits one open-issue table.
func writeActiveIssueTable(b *strings.Builder, heading string, issues []model.Issue, now time.Time) {
	b.WriteString(heading + "\n\n")
	if len(issues) == 0 {
		b.WriteString("_None._\n\n")
		return
	}
	b.WriteString("| Repo | Issue | Title | Created | Age | Updated | Status |\n")
	b.WriteString("|------|-------|-------|---------|-----|---------|--------|\n")
	for _, i := range issues {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |\n",
			escapePipes(i.Repo), issueNumLink(i), escapePipes(i.Title),
			i.Created.Format(dateLayout), age(now, i.Created),
			updated(now, i.LastActivity), issueStatusCell(i)))
	}
	b.WriteString("\n")
}

// writeStaleIssueTable emits one stale-issue table. Its shape matches the active
// table — a stale issue is still open upstream, so the same columns apply; the
// Reason column carries whatever note explains the quiet.
func writeStaleIssueTable(b *strings.Builder, heading string, issues []model.Issue, now time.Time) {
	b.WriteString(heading + "\n\n")
	if len(issues) == 0 {
		b.WriteString("_None._\n\n")
		return
	}
	b.WriteString("| Repo | Issue | Title | Created | Age | Updated | Reason |\n")
	b.WriteString("|------|-------|-------|---------|-----|---------|--------|\n")
	for _, i := range issues {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |\n",
			escapePipes(i.Repo), issueNumLink(i), escapePipes(i.Title),
			i.Created.Format(dateLayout), age(now, i.Created),
			updated(now, i.LastActivity), issueNoteCell(i)))
	}
	b.WriteString("\n")
}

// writeClosedIssueTable emits one closed issue table. The outcome carries
// GitHub's own state reason with its deterministic glyph (TDD 8.2).
func writeClosedIssueTable(b *strings.Builder, heading string, issues []model.Issue) {
	b.WriteString(heading + "\n\n")
	if len(issues) == 0 {
		b.WriteString("_None._\n\n")
		return
	}
	b.WriteString("| Repo | Issue | Title | Closed | Outcome | Notes |\n")
	b.WriteString("|------|-------|-------|--------|---------|-------|\n")
	for _, i := range issues {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
			escapePipes(i.Repo), issueNumLink(i), escapePipes(i.Title),
			dateOrDash(i.ClosedAt), issueOutcome(i), issueNoteCell(i)))
	}
	b.WriteString("\n")
}

// issueOutcome renders the closed outcome: GitHub's state reason with its
// deterministic emoji.
func issueOutcome(i model.Issue) string {
	reason := string(i.CloseReason)
	if reason == "" {
		return "🚪 CLOSED"
	}
	if e := issueCloseReasonEmoji[i.CloseReason]; e != "" {
		reason = e + " " + reason
	}
	return "🚪 CLOSED / " + reason
}

// issueStatusCell renders the active-issue Status cell: the inferred note led by
// the model's emoji, or — for an issue with no conversation to judge — the bare
// action, which is a fact rather than a failure and so carries no unavailable
// marker (TDD 8.3).
func issueStatusCell(i model.Issue) string {
	note := i.Companion
	if note == "" {
		// No note: either nothing was judged (zero-comment issue) or judgment
		// failed. Render the disposition itself so the row reads as a fact.
		note = issueActionLabel(i.Action)
	} else {
		note = withIssueEmoji(i, note)
	}
	if i.Emoji == "" && i.Action == model.IssueActionAwaitingResponse {
		// The ball is with us and the model supplied no emoji: lead with the
		// structural pending marker so the row still stands out.
		note = markIssueBallWithUs + " " + note
	}
	if i.Unverified {
		note += " ⚠️ unverified"
	}
	return withIssuePriority(i, note)
}

// issueActionLabel renders an issue action as human-readable prose for a row
// with no inferred note.
func issueActionLabel(a model.IssueAction) string {
	switch a {
	case model.IssueActionAwaitingResponse:
		return "awaiting our response"
	case model.IssueActionAwaitingOthers:
		return "awaiting others"
	case model.IssueActionTriage:
		return "no engagement yet"
	case model.IssueActionResolvedPendingClose:
		return "resolved, pending close"
	}
	return "—"
}

// issueNoteCell renders a plain note cell for an issue. An absent note on a
// closed issue is expected (nothing was judged), so it renders as a dash rather
// than the unavailable marker, which would read as a failure.
func issueNoteCell(i model.Issue) string {
	note := i.Companion
	if note == "" {
		note = "—"
	} else {
		note = withIssueEmoji(i, note)
	}
	if i.Unverified {
		note += " ⚠️ unverified"
	}
	return withIssuePriority(i, note)
}

// withIssueEmoji prefixes the model-inferred emoji to a note when present.
func withIssueEmoji(i model.Issue, note string) string {
	if i.Emoji == "" {
		return note
	}
	return i.Emoji + " " + note
}

// withIssuePriority prefixes the elevated-priority marker so elevated issue rows
// are visibly distinguished, as PR rows are (TDD 3.5).
func withIssuePriority(i model.Issue, cell string) string {
	if i.Priority == model.PriorityElevated {
		return elevatedMarker + cell
	}
	return cell
}

// issueNumLink renders the "[#N](url)" Issue column.
func issueNumLink(i model.Issue) string {
	return fmt.Sprintf("[#%d](%s)", i.Number, i.URL)
}

// sortIssueRows orders issue rows deterministically (elevated first, then repo,
// then number) so rendering stays a pure function of the store (TDD 3.3). It is
// used for the active and stale sections, which have no terminal date.
func sortIssueRows(rows []model.Issue) {
	sort.SliceStable(rows, func(a, b int) bool {
		ea := rows[a].Priority == model.PriorityElevated
		eb := rows[b].Priority == model.PriorityElevated
		if ea != eb {
			return ea
		}
		if rows[a].Repo != rows[b].Repo {
			return rows[a].Repo < rows[b].Repo
		}
		return rows[a].Number < rows[b].Number
	})
}

// sortClosedIssueRows orders the Closed section by most-recent-first ClosedAt
// (TDD 3.6), mirroring sortTerminalRows for PRs. Elevated priority still sorts
// first. Rows sharing a closed date (including two unset dates on
// pre-migration records) fall back to the repo/number order.
func sortClosedIssueRows(rows []model.Issue) {
	sort.SliceStable(rows, func(a, b int) bool {
		ea := rows[a].Priority == model.PriorityElevated
		eb := rows[b].Priority == model.PriorityElevated
		if ea != eb {
			return ea
		}
		ta, tb := issueClosedDate(rows[a]), issueClosedDate(rows[b])
		if !ta.Equal(tb) {
			return ta.After(tb) // most recent first
		}
		if rows[a].Repo != rows[b].Repo {
			return rows[a].Repo < rows[b].Repo
		}
		return rows[a].Number < rows[b].Number
	})
}

// issueClosedDate returns an issue's ClosedAt, or the zero time if unset (a
// record written before the field existed).
func issueClosedDate(i model.Issue) time.Time {
	if i.ClosedAt != nil {
		return *i.ClosedAt
	}
	return time.Time{}
}

// issueBucketCounts counts issues per bucket for the summary table.
func issueBucketCounts(issues []model.Issue) (open, stale, closed int) {
	for _, i := range issues {
		switch i.Bucket {
		case model.IssueBucketOpen:
			open++
		case model.IssueBucketStale:
			stale++
		case model.IssueBucketClosed:
			closed++
		}
	}
	return
}

// filterIssueRole selects issues by role.
func filterIssueRole(issues []model.Issue, role model.IssueRole) []model.Issue {
	var out []model.Issue
	for _, i := range issues {
		if i.Role == role {
			out = append(out, i)
		}
	}
	return out
}
