package github

import (
	"testing"
	"time"

	gh "github.com/google/go-github/v66/github"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

func run(name, status, conclusion string, completed time.Time) *gh.CheckRun {
	r := &gh.CheckRun{Name: gh.String(name), Status: gh.String(status)}
	if conclusion != "" {
		r.Conclusion = gh.String(conclusion)
	}
	if !completed.IsZero() {
		r.CompletedAt = &gh.Timestamp{Time: completed}
	}
	return r
}

func TestSummarizeChecksNilWhenNoRuns(t *testing.T) {
	if ev, failing := summarizeChecks(nil); ev != nil || failing {
		t.Error("no runs should summarize to nil (nothing to report)")
	}
}

func TestSummarizeChecksPassing(t *testing.T) {
	e, _ := summarizeChecks([]*gh.CheckRun{
		run("build", "completed", "success", time.Now()),
		run("lint", "completed", "success", time.Now()),
	})
	if e == nil || e.Kind != model.EventCIStatus {
		t.Fatalf("expected a ci_status event, got %+v", e)
	}
	if e.Text != "ci: passing" {
		t.Errorf("text = %q, want 'ci: passing'", e.Text)
	}
}

func TestSummarizeChecksFailingNamesTheCheck(t *testing.T) {
	e, _ := summarizeChecks([]*gh.CheckRun{
		run("build", "completed", "success", time.Now()),
		run("linker", "completed", "failure", time.Now()),
	})
	if e == nil {
		t.Fatal("expected an event")
	}
	if want := "ci: failing — linker"; e.Text != want {
		t.Errorf("text = %q, want %q", e.Text, want)
	}
}

func TestSummarizeChecksPendingWhenIncomplete(t *testing.T) {
	e, _ := summarizeChecks([]*gh.CheckRun{
		run("build", "completed", "success", time.Now()),
		run("e2e", "in_progress", "", time.Time{}),
	})
	if e == nil || e.Text != "ci: pending" {
		t.Errorf("text = %q, want 'ci: pending'", func() string {
			if e == nil {
				return "<nil>"
			}
			return e.Text
		}())
	}
}

func TestSummarizeChecksFailureBeatsPending(t *testing.T) {
	// A failure is reported even if another run is still pending — the failing
	// signal is the actionable one.
	e, _ := summarizeChecks([]*gh.CheckRun{
		run("e2e", "in_progress", "", time.Time{}),
		run("build", "completed", "failure", time.Now()),
	})
	if e == nil || e.Text != "ci: failing — build" {
		t.Errorf("failure should take precedence over pending, got %+v", e)
	}
}

func TestSummarizeChecksTimestampIsLatestCompletion(t *testing.T) {
	early := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	e, _ := summarizeChecks([]*gh.CheckRun{
		run("a", "completed", "success", early),
		run("b", "completed", "success", late),
	})
	if !e.Timestamp.Equal(late) {
		t.Errorf("timestamp = %s, want latest completion %s", e.Timestamp, late)
	}
}
