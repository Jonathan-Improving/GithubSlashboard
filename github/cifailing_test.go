package github

import (
	"strings"
	"testing"

	gh "github.com/google/go-github/v66/github"
)

func plainRun(name, status, conclusion string) *gh.CheckRun {
	return &gh.CheckRun{
		Name:       gh.String(name),
		Status:     gh.String(status),
		Conclusion: gh.String(conclusion),
	}
}

// TestSummarizeChecksReportsFailing covers the acquisition half of TDD 4.10: the
// failing determination is surfaced as a fact alongside the trail event, so the
// classifier can act on it deterministically instead of relying on the model to
// read it out of the prose.
func TestSummarizeChecksReportsFailing(t *testing.T) {
	cases := []struct {
		name        string
		runs        []*gh.CheckRun
		wantFailing bool
	}{
		{"no runs", nil, false},
		{"all passing", []*gh.CheckRun{
			plainRun("build", "completed", "success"),
			plainRun("lint", "completed", "success"),
		}, false},
		{"one failure among passes", []*gh.CheckRun{
			plainRun("build", "completed", "success"),
			plainRun("lint", "completed", "failure"),
		}, true},
		{"timed out counts as failing", []*gh.CheckRun{
			plainRun("e2e", "completed", "timed_out"),
		}, true},
		{"action required counts as failing", []*gh.CheckRun{
			plainRun("approval", "completed", "action_required"),
		}, true},
		{"startup failure counts as failing", []*gh.CheckRun{
			plainRun("workflow", "completed", "startup_failure"),
		}, true},
		{"pending is not failing", []*gh.CheckRun{
			plainRun("build", "in_progress", ""),
		}, false},
		{"skipped and neutral are not failing", []*gh.CheckRun{
			plainRun("optional", "completed", "skipped"),
			plainRun("advisory", "completed", "neutral"),
		}, false},
		{"pending alongside a failure is still failing", []*gh.CheckRun{
			plainRun("build", "in_progress", ""),
			plainRun("lint", "completed", "failure"),
		}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, failing := summarizeChecks(c.runs)
			if failing != c.wantFailing {
				t.Errorf("failing = %v, want %v", failing, c.wantFailing)
			}
		})
	}
}

// TestSummarizeChecksFailingAgreesWithEventText proves the flag and the trail
// event cannot disagree: they are the same determination, so a row can never be
// flagged while its trail says CI passes, or vice versa.
func TestSummarizeChecksFailingAgreesWithEventText(t *testing.T) {
	runs := []*gh.CheckRun{
		plainRun("build", "completed", "success"),
		plainRun("pecl", "completed", "failure"),
	}
	ev, failing := summarizeChecks(runs)
	if ev == nil {
		t.Fatal("expected an event")
	}
	if !failing {
		t.Error("failing = false, want true")
	}
	if want := "ci: failing"; !strings.Contains(ev.Text, want) {
		t.Errorf("event text %q does not report a failure", ev.Text)
	}
	if !strings.Contains(ev.Text, "pecl") {
		t.Errorf("event text %q does not name the failing check", ev.Text)
	}
}
