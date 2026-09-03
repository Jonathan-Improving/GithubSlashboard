package github

import (
	"testing"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TestIssueCloseReasonMapping covers TDD 8.2's acquisition half: GitHub's
// state_reason maps to the typed issue close reason, and an absent or
// unrecognized value yields the empty reason so a future upstream addition never
// aborts a run (the classifier supplies the default).
func TestIssueCloseReasonMapping(t *testing.T) {
	cases := []struct {
		in   string
		want model.IssueCloseReason
	}{
		{"completed", model.IssueCloseReasonCompleted},
		{"not_planned", model.IssueCloseReasonNotPlanned},
		{"duplicate", model.IssueCloseReasonDuplicate},
		{"reopened", model.IssueCloseReasonReopened},
		{"COMPLETED", model.IssueCloseReasonCompleted},
		{" completed ", model.IssueCloseReasonCompleted},
		{"", ""},
		{"something_new_upstream", ""},
	}

	for _, c := range cases {
		if got := issueCloseReason(c.in); got != c.want {
			t.Errorf("issueCloseReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRepoFromIssueURL proves the shared repo parser handles issue URLs, whose
// path segment is /issues/ rather than /pull/.
func TestRepoFromIssueURL(t *testing.T) {
	got, err := repoFromURL("https://github.com/valkey-io/valkey-glide/issues/1234")
	if err != nil {
		t.Fatalf("repoFromURL: %v", err)
	}
	if got != "valkey-io/valkey-glide" {
		t.Errorf("repo = %q, want valkey-io/valkey-glide", got)
	}
}
