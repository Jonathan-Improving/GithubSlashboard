package render

import (
	"strings"
	"testing"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TestPinMarkerOnlyOnPinnedRows covers P.8: a PR whose key matches a pin carries
// the 📌 marker in its PR column, and a PR with no matching pin renders without
// it.
func TestPinMarkerOnlyOnPinnedRows(t *testing.T) {
	prs := sample() // a/x#1 (submitter open), b/y#3 (reviewer open), plus terminal rows
	pinned := []model.PinnedPR{{Repo: "a/x", Number: 1, Role: model.RoleSubmitter}}

	md := Render(prs, nil, pinned, "x", refNow)

	// The pinned submitter PR's PR-column link must carry the marker.
	if !strings.Contains(md, markPinned+" [#1]") {
		t.Errorf("pinned PR a/x#1 missing %s marker on its PR column:\n%s", markPinned, md)
	}
	// An unpinned PR must not carry it on its link.
	if strings.Contains(md, markPinned+" [#3]") {
		t.Errorf("unpinned PR b/y#3 should not carry the %s marker", markPinned)
	}
}

// TestNoPinsNoMarker proves the feature is inert when nothing is pinned: the
// output is unchanged from before pins existed (P.8 second clause).
func TestNoPinsNoMarker(t *testing.T) {
	md := Render(sample(), nil, nil, "x", refNow)
	if strings.Contains(md, markPinned) {
		t.Errorf("no pins configured, yet a %s marker was rendered:\n%s", markPinned, md)
	}
}

// TestPinMarkerOnReviewerRow proves the marker reaches a reviewer-side table too
// (the PR column is in every PR table, not a submitter-only concern).
func TestPinMarkerOnReviewerRow(t *testing.T) {
	prs := sample()
	pinned := []model.PinnedPR{{Repo: "b/y", Number: 3, Role: model.RoleReviewer}}
	md := Render(prs, nil, pinned, "x", refNow)
	if !strings.Contains(md, markPinned+" [#3]") {
		t.Errorf("pinned reviewer PR b/y#3 missing %s marker on its PR column:\n%s", markPinned, md)
	}
}
