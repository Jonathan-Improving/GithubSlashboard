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
