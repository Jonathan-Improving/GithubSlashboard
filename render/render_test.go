package render

import (
	"strings"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

var refNow = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

func sample() []model.PR {
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) // 13 days before refNow
	merged := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	closed := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	return []model.PR{
		{Repo: "a/x", Number: 1, Title: "Alpha", URL: "u1", Role: model.RoleSubmitter, Created: created,
			Bucket: model.BucketOpen, Action: model.ActionChangesRequested,
			Priority: model.PriorityElevated, Companion: "mitigating QA findings", Emoji: "🔧"},
		{Repo: "a/x", Number: 2, Title: "Beta", URL: "u2", Role: model.RoleSubmitter, Created: created,
			Bucket: model.BucketMerged, MergedAt: &merged, Priority: model.PriorityNeutral, Companion: "shipped last week", Emoji: "📦"},
		{Repo: "b/y", Number: 3, Title: "Gamma", URL: "u3", Role: model.RoleReviewer, Created: created,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
			Priority: model.PriorityNeutral, Companion: "waiting on the author", Emoji: "⏳"},
		{Repo: "b/y", Number: 4, Title: "Delta", URL: "u4", Role: model.RoleReviewer, Created: created,
			Bucket: model.BucketClosed, CloseReason: model.CloseReasonSuperseded, ClosedAt: &closed,
			Priority: model.PriorityNeutral, Companion: "replaced by PR five", Emoji: "♻️"},
	}
}

func TestSummaryRoleBased(t *testing.T) {
	md := Render(sample(), nil, "2026-08-26 14:00:00 PDT", refNow)
	// Role-based summary: Submitter has 1 open + 1 merged; Reviewer 1 open + 1 closed.
	for _, want := range []string{
		"| Role | 🟢 Open | 🟡 Stale | ✅ Merged | 🔴 Closed | Total |",
		"| Submitter (PR) | 1 | 0 | 1 | 0 | 2 |",
		"| Reviewer (PR) | 1 | 0 | 0 | 1 | 2 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("summary missing %q (TDD 3.2)\n%s", want, md)
		}
	}
}

func TestSubmitterSectionsAndColumns(t *testing.T) {
	md := Render(sample(), nil, "x", refNow)
	for _, want := range []string{
		"# Submitter: PRs I Authored",
		"## 💡 Open (1)",
		"| Repo | PR | Title | Created | Age | Updated | Action Needed |",
		"## 📦 Merged (1)",
		"| Repo | PR | Title | Created | Merged | Notes |",
		"2026-08-13", // created date
		"13d",        // age
		"[#1](u1)",   // PR link form
	} {
		if !strings.Contains(md, want) {
			t.Errorf("submitter section missing %q\n%s", want, md)
		}
	}
}

func TestActionNeededMarkers(t *testing.T) {
	// Structural markers are only rendered when the model inferred no emoji
	// (the inferred glyph otherwise leads on its own), so assert the
	// action->marker mapping on emoji-free rows.
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		action model.Action
		want   string
	}{
		{"changes_requested is back-and-forth", model.ActionChangesRequested, markBackForth},
		{"author_active is back-and-forth", model.ActionAuthorActive, markBackForth},
		{"merge_ready is back-and-forth", model.ActionMergeReady, markBackForth},
		{"awaiting_review is pending", model.ActionAwaitingReview, markPending},
		{"blocked_external is pending", model.ActionBlockedExternal, markPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prs := []model.PR{{Repo: "a/x", Number: 1, Title: "Alpha", URL: "u1", Role: model.RoleSubmitter,
				Created: created, Bucket: model.BucketOpen, Action: tc.action,
				Priority: model.PriorityNeutral, Companion: "note"}}
			md := Render(prs, nil, "x", refNow)
			if !strings.Contains(md, tc.want+" note") {
				t.Errorf("action %s: expected marker %q\n%s", tc.action, tc.want, md)
			}
		})
	}
}

func TestReviewerBallHoldingAndDone(t *testing.T) {
	md := Render(sample(), nil, "x", refNow)
	for _, want := range []string{
		"# Reviewer: PRs I Reviewed",
		"## Awaiting Our Action (1)", // reviewer open awaiting_review -> ball with us
		"| Repo | PR | Title | Updated | Our Review |",
		"⏳",                               // pending marker
		"## ✅ Done - Closed / Merged (1)", // the closed reviewer PR
		"| Repo | PR | Title | Outcome | Notes |",
		outcomeClosed + " / ♻️ superseded", // sub-reason with deterministic emoji, new format (TDD 3.1)
		"♻️ replaced by PR five",           // emoji-prefixed Notes cell on the Done table
	} {
		if !strings.Contains(md, want) {
			t.Errorf("reviewer section missing %q\n%s", want, md)
		}
	}
}

func TestReviewerMergedOutcome(t *testing.T) {
	merged := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	prs := []model.PR{{Repo: "r/z", Number: 9, Title: "M", URL: "u", Role: model.RoleReviewer,
		Bucket: model.BucketMerged, MergedAt: &merged, Priority: model.PriorityNeutral}}
	md := Render(prs, nil, "x", refNow)
	if !strings.Contains(md, outcomeMerged) {
		t.Errorf("reviewer merged PR should show %q\n%s", outcomeMerged, md)
	}
}

func TestAwaitingActionEmojiPrecedence(t *testing.T) {
	// An awaiting-our-action reviewer PR whose inferred emoji is NOT the pending
	// marker: the inferred emoji leads and the structural ⏳ is not also added,
	// so the note is not double-prefixed.
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	prs := []model.PR{{Repo: "r/z", Number: 8, Title: "T", URL: "u", Role: model.RoleReviewer, Created: created,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityNeutral, Companion: "needs our review", Emoji: "👀"}}
	md := Render(prs, nil, "x", refNow)
	if !strings.Contains(md, "👀 needs our review") {
		t.Errorf("inferred emoji should lead the awaiting cell:\n%s", md)
	}
	if strings.Contains(md, markPending+" 👀") {
		t.Errorf("structural marker should not precede the inferred emoji (double glyph):\n%s", md)
	}
}

func TestActionNeededEmojiPrecedence(t *testing.T) {
	// Regression: the submitter Open "Action Needed" cell prepended a
	// structural marker on top of the inferred emoji, so when the model
	// inferred the same glyph as the marker the cell read "⏳ ⏳ ..."
	// (observed on valkey-glide-ruby#295). The inferred emoji must lead alone.
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	prs := []model.PR{{Repo: "r/z", Number: 295, Title: "T", URL: "u", Role: model.RoleSubmitter, Created: created,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityNeutral, Companion: "awaiting final blessing", Emoji: "⏳"}}
	md := Render(prs, nil, "x", refNow)
	if strings.Contains(md, markPending+" "+markPending) {
		t.Errorf("doubled structural+inferred glyph in action-needed cell:\n%s", md)
	}
	if !strings.Contains(md, "⏳ awaiting final blessing") {
		t.Errorf("inferred emoji should lead the action-needed cell:\n%s", md)
	}
}

func TestActionNeededStructuralMarkerWithoutEmoji(t *testing.T) {
	// With no inferred emoji the structural marker still leads, so a row is
	// never left glyphless.
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	prs := []model.PR{{Repo: "r/z", Number: 9, Title: "T", URL: "u", Role: model.RoleSubmitter, Created: created,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityNeutral, Companion: "waiting on review"}}
	md := Render(prs, nil, "x", refNow)
	if !strings.Contains(md, markPending+" waiting on review") {
		t.Errorf("structural marker should lead when no emoji was inferred:\n%s", md)
	}
}

func TestActionNeededConflictPrefixSurvivesEmoji(t *testing.T) {
	// The conflicted prefix must still be surfaced when an emoji is inferred.
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	prs := []model.PR{{Repo: "r/z", Number: 10, Title: "T", URL: "u", Role: model.RoleSubmitter, Created: created,
		Bucket: model.BucketOpen, Action: model.ActionConflicted,
		Priority: model.PriorityNeutral, Companion: "rebase needed", Emoji: "🔧"}}
	md := Render(prs, nil, "x", refNow)
	if !strings.Contains(md, "🔧 merge conflict — rebase needed") {
		t.Errorf("conflict prefix should survive under an inferred emoji:\n%s", md)
	}
	if strings.Contains(md, markBackForth+" 🔧") {
		t.Errorf("structural marker should not precede the inferred emoji:\n%s", md)
	}
}

func TestReviewFeedbackCellSignalsAuthorMustAct(t *testing.T) {
	// The #295 failure mode: an inferred "conditionally approved" note must not
	// be presented as waiting on someone else. The cell carries the
	// back-and-forth marker semantics and states the outstanding feedback.
	created := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	prs := []model.PR{{Repo: "r/z", Number: 295, Title: "T", URL: "u", Role: model.RoleSubmitter, Created: created,
		Bucket: model.BucketOpen, Action: model.ActionReviewFeedback,
		Priority: model.PriorityNeutral, Companion: "conditionally approved", Emoji: "⏳"}}
	md := Render(prs, nil, "x", refNow)
	if !strings.Contains(md, "unresolved review feedback — conditionally approved") {
		t.Errorf("cell should state the outstanding feedback:\n%s", md)
	}
	// Without an inferred emoji the structural marker must be the
	// back-and-forth one, never the pending-on-others ⏳.
	prs[0].Emoji = ""
	md = Render(prs, nil, "x", refNow)
	if !strings.Contains(md, markBackForth+" unresolved review feedback") {
		t.Errorf("review_feedback should use the back-and-forth marker:\n%s", md)
	}
	if strings.Contains(md, markPending+" unresolved review feedback") {
		t.Errorf("review_feedback must not read as pending on someone else:\n%s", md)
	}
}

// TestTerminalBucketsSortNewestFirst covers TDD 3.6: Merged and Closed rows
// order by terminal date, most recent first, rather than repo/number.
func TestTerminalBucketsSortNewestFirst(t *testing.T) {
	oldest := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		// Repo/number order (z,10 < z,20 < z,30) is the deliberate inverse of
		// date order, so a passing test proves date order won, not a leftover
		// repo/number coincidence.
		{Repo: "r/z", Number: 10, Title: "Oldest merge", URL: "u10", Role: model.RoleSubmitter,
			Bucket: model.BucketMerged, MergedAt: &oldest, Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 20, Title: "Newest merge", URL: "u20", Role: model.RoleSubmitter,
			Bucket: model.BucketMerged, MergedAt: &newest, Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 30, Title: "Middle merge", URL: "u30", Role: model.RoleSubmitter,
			Bucket: model.BucketMerged, MergedAt: &middle, Priority: model.PriorityNeutral},
	}
	md := Render(prs, nil, "x", refNow)

	iNewest := strings.Index(md, "Newest merge")
	iMiddle := strings.Index(md, "Middle merge")
	iOldest := strings.Index(md, "Oldest merge")
	if iNewest == -1 || iMiddle == -1 || iOldest == -1 {
		t.Fatalf("expected all three merged rows present:\n%s", md)
	}
	if !(iNewest < iMiddle && iMiddle < iOldest) {
		t.Errorf("merged rows should be newest-first (Newest, Middle, Oldest); got order at indices %d, %d, %d\n%s",
			iNewest, iMiddle, iOldest, md)
	}
}

// TestReviewerDoneSortsMergedAndClosedTogetherByDate covers TDD 3.6 for the
// reviewer Done table, which mixes merged and closed PRs and must compare
// MergedAt against ClosedAt as one terminal date.
func TestReviewerDoneSortsMergedAndClosedTogetherByDate(t *testing.T) {
	older := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		{Repo: "r/z", Number: 1, Title: "Old closed", URL: "u1", Role: model.RoleReviewer,
			Bucket: model.BucketClosed, ClosedAt: &older, Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 2, Title: "New merged", URL: "u2", Role: model.RoleReviewer,
			Bucket: model.BucketMerged, MergedAt: &newer, Priority: model.PriorityNeutral},
	}
	md := Render(prs, nil, "x", refNow)

	iNew := strings.Index(md, "New merged")
	iOld := strings.Index(md, "Old closed")
	if iNew == -1 || iOld == -1 {
		t.Fatalf("expected both done rows present:\n%s", md)
	}
	if iOld < iNew {
		t.Errorf("newer merged row should precede older closed row in Done table:\n%s", md)
	}
}

// TestTerminalSortElevatedFirstThenDate covers TDD 3.6's elevated-priority
// carve-out: an elevated row leads even when it is older than a neutral row.
func TestTerminalSortElevatedFirstThenDate(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		{Repo: "r/z", Number: 1, Title: "Neutral newer", URL: "u1", Role: model.RoleSubmitter,
			Bucket: model.BucketClosed, ClosedAt: &newer, Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 2, Title: "Elevated older", URL: "u2", Role: model.RoleSubmitter,
			Bucket: model.BucketClosed, ClosedAt: &older, Priority: model.PriorityElevated},
	}
	md := Render(prs, nil, "x", refNow)

	iElevated := strings.Index(md, "Elevated older")
	iNeutral := strings.Index(md, "Neutral newer")
	if iElevated == -1 || iNeutral == -1 {
		t.Fatalf("expected both closed rows present:\n%s", md)
	}
	if iNeutral < iElevated {
		t.Errorf("elevated row should lead even though it is older:\n%s", md)
	}
}

// TestTerminalSortFallsBackToRepoNumberOnTie covers TDD 3.6's tiebreak: rows
// with an identical terminal date (including both unset) fall back to the
// repo/number order.
func TestTerminalSortFallsBackToRepoNumberOnTie(t *testing.T) {
	same := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	prs := []model.PR{
		{Repo: "r/z", Number: 20, Title: "Later number", URL: "u20", Role: model.RoleSubmitter,
			Bucket: model.BucketClosed, ClosedAt: &same, Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 10, Title: "Earlier number", URL: "u10", Role: model.RoleSubmitter,
			Bucket: model.BucketClosed, ClosedAt: &same, Priority: model.PriorityNeutral},
	}
	md := Render(prs, nil, "x", refNow)

	iEarlier := strings.Index(md, "Earlier number")
	iLater := strings.Index(md, "Later number")
	if iEarlier == -1 || iLater == -1 {
		t.Fatalf("expected both rows present:\n%s", md)
	}
	if iLater < iEarlier {
		t.Errorf("tied terminal dates should fall back to number order:\n%s", md)
	}
}

// TestReviewSubmittedSortsByLastActivity covers TDD 3.6a: the reviewer
// Open — Review Submitted table orders by LastActivity, most recent first,
// rather than repo/number.
func TestReviewSubmittedSortsByLastActivity(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		// Number order (10 < 20) is the deliberate inverse of activity order.
		{Repo: "r/z", Number: 10, Title: "Newest activity", URL: "u10", Role: model.RoleReviewer,
			Bucket: model.BucketOpen, Action: model.ActionMergeReady, LastActivity: newer,
			Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 20, Title: "Oldest activity", URL: "u20", Role: model.RoleReviewer,
			Bucket: model.BucketOpen, Action: model.ActionMergeReady, LastActivity: older,
			Priority: model.PriorityNeutral},
	}
	md := Render(prs, nil, "x", refNow)

	iNewest := strings.Index(md, "Newest activity")
	iOldest := strings.Index(md, "Oldest activity")
	if iNewest == -1 || iOldest == -1 {
		t.Fatalf("expected both review-submitted rows present:\n%s", md)
	}
	if iOldest < iNewest {
		t.Errorf("review-submitted rows should be newest-activity-first:\n%s", md)
	}
}

// TestReviewSubmittedElevatedFirstThenActivity covers TDD 3.6a's elevated
// carve-out: an elevated row leads even when it is less recently active.
func TestReviewSubmittedElevatedFirstThenActivity(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		{Repo: "r/z", Number: 1, Title: "Neutral newer", URL: "u1", Role: model.RoleReviewer,
			Bucket: model.BucketOpen, Action: model.ActionMergeReady, LastActivity: newer,
			Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 2, Title: "Elevated older", URL: "u2", Role: model.RoleReviewer,
			Bucket: model.BucketOpen, Action: model.ActionMergeReady, LastActivity: older,
			Priority: model.PriorityElevated},
	}
	md := Render(prs, nil, "x", refNow)

	iElevated := strings.Index(md, "Elevated older")
	iNeutral := strings.Index(md, "Neutral newer")
	if iElevated == -1 || iNeutral == -1 {
		t.Fatalf("expected both rows present:\n%s", md)
	}
	if iNeutral < iElevated {
		t.Errorf("elevated row should lead even though it is less recently active:\n%s", md)
	}
}

// TestReviewSubmittedFallsBackToRepoNumberOnTie covers TDD 3.6a's tiebreak:
// rows with an identical activity date fall back to the repo/number order.
func TestReviewSubmittedFallsBackToRepoNumberOnTie(t *testing.T) {
	same := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	prs := []model.PR{
		{Repo: "r/z", Number: 20, Title: "Later number", URL: "u20", Role: model.RoleReviewer,
			Bucket: model.BucketOpen, Action: model.ActionMergeReady, LastActivity: same,
			Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 10, Title: "Earlier number", URL: "u10", Role: model.RoleReviewer,
			Bucket: model.BucketOpen, Action: model.ActionMergeReady, LastActivity: same,
			Priority: model.PriorityNeutral},
	}
	md := Render(prs, nil, "x", refNow)

	iEarlier := strings.Index(md, "Earlier number")
	iLater := strings.Index(md, "Later number")
	if iEarlier == -1 || iLater == -1 {
		t.Fatalf("expected both rows present:\n%s", md)
	}
	if iLater < iEarlier {
		t.Errorf("tied activity dates should fall back to number order:\n%s", md)
	}
}

// TestAwaitingActionSortsByLastActivity covers TDD 3.6a's extension to the
// Awaiting Our Action table: it sorts by LastActivity like every other live
// PR table, not by repo/number.
func TestAwaitingActionSortsByLastActivity(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		// Number order (10 < 20) is the deliberate inverse of activity order.
		{Repo: "r/z", Number: 10, Title: "Newest activity", URL: "u10", Role: model.RoleReviewer,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, LastActivity: newer,
			Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 20, Title: "Oldest activity", URL: "u20", Role: model.RoleReviewer,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, LastActivity: older,
			Priority: model.PriorityNeutral},
	}
	md := Render(prs, nil, "x", refNow)

	iNewest := strings.Index(md, "Newest activity")
	iOldest := strings.Index(md, "Oldest activity")
	if iNewest == -1 || iOldest == -1 {
		t.Fatalf("expected both awaiting-action rows present:\n%s", md)
	}
	if iOldest < iNewest {
		t.Errorf("Awaiting Our Action should be newest-activity-first:\n%s", md)
	}
}

// TestSubmitterOpenSortsByLastActivity covers TDD 3.6a's extension to the
// submitter Open table.
func TestSubmitterOpenSortsByLastActivity(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		{Repo: "r/z", Number: 10, Title: "Newest activity", URL: "u10", Role: model.RoleSubmitter,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, LastActivity: newer,
			Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 20, Title: "Oldest activity", URL: "u20", Role: model.RoleSubmitter,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, LastActivity: older,
			Priority: model.PriorityNeutral},
	}
	md := Render(prs, nil, "x", refNow)

	iNewest := strings.Index(md, "Newest activity")
	iOldest := strings.Index(md, "Oldest activity")
	if iNewest == -1 || iOldest == -1 {
		t.Fatalf("expected both open rows present:\n%s", md)
	}
	if iOldest < iNewest {
		t.Errorf("submitter Open should be newest-activity-first:\n%s", md)
	}
}

// TestSubmitterOpenElevatedLeadsDespiteOlderActivity covers TDD 3.6a's
// elevated-precedes-activity clarification: an elevated PR leads even when a
// neutral PR in the same table has more recent activity — this is the exact
// scenario the operator flagged as looking wrong before confirming it is the
// intended design (elevated is a higher-precedence sort key than activity,
// not a tiebreak within it).
func TestSubmitterOpenElevatedLeadsDespiteOlderActivity(t *testing.T) {
	olderElevated := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newerNeutral := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		{Repo: "r/z", Number: 1, Title: "Neutral touched today", URL: "u1", Role: model.RoleSubmitter,
			Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, LastActivity: newerNeutral,
			Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 2, Title: "Elevated touched 2 days ago", URL: "u2", Role: model.RoleSubmitter,
			Bucket: model.BucketOpen, Action: model.ActionBlockedExternal, LastActivity: olderElevated,
			Priority: model.PriorityElevated},
	}
	md := Render(prs, nil, "x", refNow)

	iElevated := strings.Index(md, "Elevated touched 2 days ago")
	iNeutral := strings.Index(md, "Neutral touched today")
	if iElevated == -1 || iNeutral == -1 {
		t.Fatalf("expected both open rows present:\n%s", md)
	}
	if iNeutral < iElevated {
		t.Errorf("elevated row should lead the submitter Open table even though a neutral row was touched more recently:\n%s", md)
	}
}

// TestSubmitterStaleSortsByLastActivity covers TDD 3.6a's extension to the
// submitter Stale table.
func TestSubmitterStaleSortsByLastActivity(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	prs := []model.PR{
		{Repo: "r/z", Number: 10, Title: "Newest activity", URL: "u10", Role: model.RoleSubmitter,
			Bucket: model.BucketStale, LastActivity: newer, Priority: model.PriorityNeutral},
		{Repo: "r/z", Number: 20, Title: "Oldest activity", URL: "u20", Role: model.RoleSubmitter,
			Bucket: model.BucketStale, LastActivity: older, Priority: model.PriorityNeutral},
	}
	md := Render(prs, nil, "x", refNow)

	iNewest := strings.Index(md, "Newest activity")
	iOldest := strings.Index(md, "Oldest activity")
	if iNewest == -1 || iOldest == -1 {
		t.Fatalf("expected both stale rows present:\n%s", md)
	}
	if iOldest < iNewest {
		t.Errorf("submitter Stale should be newest-activity-first:\n%s", md)
	}
}

func TestDoneOutcomeSubReasonFormat(t *testing.T) {
	closed := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	prs := []model.PR{{Repo: "r/z", Number: 9, Title: "T", URL: "u", Role: model.RoleReviewer,
		Bucket: model.BucketClosed, CloseReason: model.CloseReasonCancelled, ClosedAt: &closed,
		Priority: model.PriorityNeutral, Companion: "dropped by author", Emoji: "🗑️"}}
	md := Render(prs, nil, "x", refNow)
	if !strings.Contains(md, outcomeClosed+" / 🗑️ cancelled") {
		t.Errorf("outcome should be 'PRIMARY / <emoji> sub-reason':\n%s", md)
	}
	if strings.Contains(md, "(cancelled)") {
		t.Errorf("old parenthesized format should be gone:\n%s", md)
	}
}

func TestNoteCellsCarryEmoji(t *testing.T) {
	md := Render(sample(), nil, "x", refNow)
	for _, want := range []string{
		"🔧 mitigating QA findings", // submitter Open Action Needed
		"📦 shipped last week",      // submitter Merged Notes
		"⏳ waiting on the author",  // reviewer Awaiting Our Action
		"♻️ replaced by PR five",   // reviewer Done Notes
	} {
		if !strings.Contains(md, want) {
			t.Errorf("expected emoji-prefixed note %q\n%s", want, md)
		}
	}
}

func TestRoleSectionsSeparatedByRule(t *testing.T) {
	md := Render(sample(), nil, "x", refNow)
	sub := strings.Index(md, "# Submitter: PRs I Authored")
	rule := strings.Index(md, "\n---\n")
	rev := strings.Index(md, "# Reviewer: PRs I Reviewed")
	if sub < 0 || rule < 0 || rev < 0 {
		t.Fatalf("missing section or rule (sub=%d rule=%d rev=%d)\n%s", sub, rule, rev, md)
	}
	if !(sub < rule && rule < rev) {
		t.Errorf("horizontal rule should sit between the Submitter and Reviewer sections (sub=%d rule=%d rev=%d)", sub, rule, rev)
	}
}

func TestGeneratedNoticePresent(t *testing.T) {
	md := Render(sample(), nil, "x", refNow)
	if !strings.Contains(md, generatedNotice) {
		t.Errorf("generated-file notice missing:\n%s", md)
	}
	// It must sit near the top: before the first section heading.
	notice := strings.Index(md, generatedNotice)
	firstSection := strings.Index(md, "# Submitter")
	if notice < 0 || firstSection < 0 || notice > firstSection {
		t.Errorf("notice not in the header block (notice=%d, firstSection=%d)", notice, firstSection)
	}
}

func TestLastUpdatedVerbatim(t *testing.T) {
	stamp := "SOME EXACT STAMP 123"
	md := Render(sample(), nil, stamp, refNow)
	if !strings.Contains(md, "_Last Updated: "+stamp+"_") {
		t.Errorf("Last Updated not verbatim (TDD 3.4)")
	}
}

func TestElevatedVisible(t *testing.T) {
	md := Render(sample(), nil, "x", refNow)
	if !strings.Contains(md, strings.TrimSpace(elevatedMarker)) {
		t.Errorf("elevated priority not distinguished (TDD 3.5)")
	}
}

func TestPureFunctionExceptTimestamp(t *testing.T) {
	a := Render(sample(), nil, "STAMP-A", refNow)
	b := Render(sample(), nil, "STAMP-B", refNow)
	na := strings.Replace(a, "STAMP-A", "X", 1)
	nb := strings.Replace(b, "STAMP-B", "X", 1)
	if na != nb {
		t.Errorf("render not pure apart from Last Updated (TDD 3.3)")
	}
}

func TestUnverifiedMarked(t *testing.T) {
	prs := []model.PR{{Repo: "a/x", Number: 1, Title: "T", URL: "u", Role: model.RoleSubmitter,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityNeutral, Unverified: true}}
	md := Render(prs, nil, "x", refNow)
	if !strings.Contains(md, "unverified") {
		t.Errorf("unverified row not marked:\n%s", md)
	}
}
