package github

import (
	"strings"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

func TestCountUnresolvedExcludesResolvedAndOutdated(t *testing.T) {
	threads := []reviewThread{
		{Resolved: false, Outdated: false}, // counts
		{Resolved: true, Outdated: false},  // resolved
		{Resolved: false, Outdated: true},  // outdated: line already rewritten
		{Resolved: true, Outdated: true},   // both
		{Resolved: false, Outdated: false}, // counts
	}
	if got := countUnresolved(threads); got != 2 {
		t.Errorf("countUnresolved = %d, want 2", got)
	}
}

func TestCountUnresolvedEmpty(t *testing.T) {
	if got := countUnresolved(nil); got != 0 {
		t.Errorf("countUnresolved(nil) = %d, want 0", got)
	}
}

func TestSummarizeReviewThreadsNilWhenNothingOutstanding(t *testing.T) {
	if ev := summarizeReviewThreads(nil); ev != nil {
		t.Errorf("expected nil event for no threads, got %+v", ev)
	}
	resolved := []reviewThread{{Resolved: true}, {Resolved: false, Outdated: true}}
	if ev := summarizeReviewThreads(resolved); ev != nil {
		t.Errorf("expected nil event when all threads settled, got %+v", ev)
	}
}

func TestSummarizeReviewThreadsCountsAndAttributes(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	threads := []reviewThread{
		{Author: "Aryex", Created: base},
		{Author: "Aryex", Created: base.Add(time.Hour)},
		{Author: "yipin-chen", Created: base.Add(30 * time.Minute)},
		{Author: "ignored", Created: base.Add(9 * time.Hour), Resolved: true},
	}
	ev := summarizeReviewThreads(threads)
	if ev == nil {
		t.Fatal("expected an event")
	}
	if ev.Kind != model.EventReviewThreads {
		t.Errorf("kind = %q, want %q", ev.Kind, model.EventReviewThreads)
	}
	if !strings.Contains(ev.Text, "3 unresolved review threads") {
		t.Errorf("text should report the count, got %q", ev.Text)
	}
	// Authors are named with counts, sorted deterministically.
	if !strings.Contains(ev.Text, "Aryex (2), yipin-chen") {
		t.Errorf("text should name authors deterministically, got %q", ev.Text)
	}
	// Timestamp is the newest unresolved thread, so it sorts late in the trail.
	if !ev.Timestamp.Equal(base.Add(time.Hour)) {
		t.Errorf("timestamp = %v, want newest unresolved %v", ev.Timestamp, base.Add(time.Hour))
	}
	if ev.Author != "github-review" {
		t.Errorf("author = %q, want github-review", ev.Author)
	}
}

func TestSummarizeReviewThreadsSingularWording(t *testing.T) {
	ev := summarizeReviewThreads([]reviewThread{{Author: "a", Created: time.Now()}})
	if ev == nil {
		t.Fatal("expected an event")
	}
	if !strings.Contains(ev.Text, "1 unresolved review thread outstanding") {
		t.Errorf("expected singular wording, got %q", ev.Text)
	}
}

func TestSummarizeReviewDecision(t *testing.T) {
	at := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		decision string
		want     string
	}{
		{"APPROVED", "approved"},
		{"CHANGES_REQUESTED", "changes requested"},
		{"REVIEW_REQUIRED", "review still required"},
	} {
		ev := summarizeReviewDecision(tc.decision, at)
		if ev == nil {
			t.Fatalf("%s: expected an event", tc.decision)
		}
		if !strings.Contains(ev.Text, tc.want) {
			t.Errorf("%s: text = %q, want it to mention %q", tc.decision, ev.Text, tc.want)
		}
		if ev.Kind != model.EventReviewDecision {
			t.Errorf("%s: kind = %q", tc.decision, ev.Kind)
		}
	}
}

func TestSummarizeReviewDecisionNilWhenAbsent(t *testing.T) {
	// GitHub reports an empty decision when no review has happened yet.
	if ev := summarizeReviewDecision("", time.Now()); ev != nil {
		t.Errorf("expected nil for empty decision, got %+v", ev)
	}
	if ev := summarizeReviewDecision("SOMETHING_NEW", time.Now()); ev != nil {
		t.Errorf("expected nil for unknown decision, got %+v", ev)
	}
}
