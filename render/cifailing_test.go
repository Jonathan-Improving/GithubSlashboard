package render

import (
	"strings"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// ciPR builds an open submitter PR with the given action, note, emoji, and CI flag.
func ciPR(action model.Action, companion, emoji string, ciFailing bool) model.PR {
	return model.PR{
		Repo: "a/x", Number: 1, Title: "Something", URL: "u",
		Role: model.RoleSubmitter, Created: issueNow.Add(-10 * 24 * time.Hour),
		LastActivity: issueNow.Add(-time.Hour),
		Bucket:       model.BucketOpen, Action: action,
		Priority: model.PriorityNeutral, Companion: companion, Emoji: emoji,
		CIFailing: ciFailing,
	}
}

// TestCIFailingMarkerAppears covers TDD 4.12: a flagged row is visibly marked.
func TestCIFailingMarkerAppears(t *testing.T) {
	md := Render([]model.PR{ciPR(model.ActionAuthorActive, "iterating on the branch", "🔧", true)}, nil, "x", issueNow)

	if !strings.Contains(md, markCIFailing) {
		t.Errorf("flagged row is missing the %s marker (TDD 4.12)\n%s", markCIFailing, md)
	}
}

// TestCIFailingMarkerAbsentWhenGreen proves the marker is not gratuitous.
func TestCIFailingMarkerAbsentWhenGreen(t *testing.T) {
	md := Render([]model.PR{ciPR(model.ActionAuthorActive, "iterating on the branch", "🔧", false)}, nil, "x", issueNow)

	if strings.Contains(md, markCIFailing) {
		t.Errorf("unflagged row carries the %s marker\n%s", markCIFailing, md)
	}
}

// TestCIMarkerDoesNotShadowTheNote is the rendered half of TDD 4.11: the marker is
// added, so the action-derived content still reaches the reader in full.
func TestCIMarkerDoesNotShadowTheNote(t *testing.T) {
	md := Render([]model.PR{
		ciPR(model.ActionReviewFeedback, "reviewer asked for a rename", "🔴", true),
	}, nil, "x", issueNow)

	if !strings.Contains(md, markCIFailing) {
		t.Error("missing the CI marker")
	}
	// The review-feedback prose and the inferred note must both survive.
	if !strings.Contains(md, "unresolved review feedback") {
		t.Errorf("the action's own prose was lost behind the CI marker (TDD 4.11)\n%s", md)
	}
	if !strings.Contains(md, "reviewer asked for a rename") {
		t.Errorf("the inferred note was lost behind the CI marker (TDD 4.11)\n%s", md)
	}
	if !strings.Contains(md, "🔴") {
		t.Errorf("the inferred emoji was lost behind the CI marker (TDD 4.11)\n%s", md)
	}
}

// TestCIMarkerNotDoubledWhenModelChoseIt proves the marker is suppressed when the
// inferred emoji is already the same glyph, so no row renders 🚧 twice.
func TestCIMarkerNotDoubledWhenModelChoseIt(t *testing.T) {
	md := Render([]model.PR{
		ciPR(model.ActionAuthorActive, "build broken on the new runner", markCIFailing, true),
	}, nil, "x", issueNow)

	if n := strings.Count(md, markCIFailing); n != 1 {
		t.Errorf("marker appears %d times, want exactly 1 — no doubled glyph\n%s", n, md)
	}
}

// TestCIMarkerLeadsTheCell proves the hard fact leads, ahead of the inferred emoji.
func TestCIMarkerLeadsTheCell(t *testing.T) {
	md := Render([]model.PR{
		ciPR(model.ActionAuthorActive, "iterating on the branch", "🔧", true),
	}, nil, "x", issueNow)

	ci := strings.Index(md, markCIFailing)
	emoji := strings.Index(md, "🔧")
	if ci < 0 || emoji < 0 {
		t.Fatalf("expected both glyphs\n%s", md)
	}
	if ci > emoji {
		t.Errorf("the CI marker should lead the cell, ahead of the inferred emoji\n%s", md)
	}
}

// TestCIFlagDoesNotMoveReviewerRows covers the scoping note on TDD 4.12: the flag
// is submitter-scoped, so it never affects the reviewer ball-holding split. A
// reviewer PR whose action says the ball is elsewhere stays in the Review Submitted
// table even if a flag were somehow present.
func TestCIFlagDoesNotMoveReviewerRows(t *testing.T) {
	pr := model.PR{
		Repo: "a/x", Number: 2, Title: "Reviewer PR", URL: "u2",
		Role: model.RoleReviewer, Created: issueNow.Add(-10 * 24 * time.Hour),
		LastActivity: issueNow.Add(-time.Hour),
		Bucket:       model.BucketOpen, Action: model.ActionAuthorActive,
		Priority: model.PriorityNeutral, Companion: "author is iterating", Emoji: "🔧",
		CIFailing: true, // would not be set by classify; asserts render does not react
	}
	md := Render([]model.PR{pr}, nil, "x", issueNow)

	awaitIdx := strings.Index(md, "## Awaiting Our Action")
	submittedIdx := strings.Index(md, "Open — Review Submitted")
	if awaitIdx < 0 || submittedIdx < 0 {
		t.Fatal("missing reviewer sections")
	}
	awaiting := md[awaitIdx:submittedIdx]
	if strings.Contains(awaiting, "Reviewer PR") {
		t.Errorf("a reviewer row moved into Awaiting Our Action on a CI flag; the flag is submitter-scoped (TDD 4.12)\n%s", md)
	}
}
