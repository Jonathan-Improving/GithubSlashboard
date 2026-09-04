package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// fakeSummarizer implements the one method notify.Summarize needs from
// provider.Provider, so tests do not need a full Provider fake.
type fakeSummarizer struct {
	out string
	err error
}

func (f fakeSummarizer) Name() string { return "fake" }
func (f fakeSummarizer) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	return "", errors.New("not used")
}
func (f fakeSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	return f.out, f.err
}

func TestChangesFromPRsFiltersByWasJudged(t *testing.T) {
	prs := []model.PR{
		{Repo: "o/n", Number: 1, Role: model.RoleSubmitter, Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, WasJudged: true},
		{Repo: "o/n", Number: 2, Role: model.RoleSubmitter, Bucket: model.BucketOpen, Action: model.ActionMergeReady, WasJudged: false},
		{Repo: "o/n", Number: 3, Role: model.RoleReviewer, Bucket: model.BucketMerged, WasJudged: false},
	}
	got := ChangesFromPRs(prs)
	if len(got) != 1 {
		t.Fatalf("got %d changes, want 1 (only WasJudged=true PRs)", len(got))
	}
	if got[0].Number != 1 || got[0].Entity != entityPR {
		t.Errorf("unexpected change: %+v", got[0])
	}
}

func TestChangesFromIssuesFiltersByWasJudged(t *testing.T) {
	issues := []model.Issue{
		{Repo: "o/n", Number: 10, Role: model.IssueRoleAuthor, Bucket: model.IssueBucketOpen, Action: model.IssueActionTriage, WasJudged: true},
		{Repo: "o/n", Number: 11, Role: model.IssueRoleParticipant, Bucket: model.IssueBucketOpen, WasJudged: false},
	}
	got := ChangesFromIssues(issues)
	if len(got) != 1 {
		t.Fatalf("got %d changes, want 1", len(got))
	}
	if got[0].Number != 10 || got[0].Entity != entityIssue {
		t.Errorf("unexpected change: %+v", got[0])
	}
}

func TestChangesFromPRsEmptyWhenNoneJudged(t *testing.T) {
	prs := []model.PR{
		{Repo: "o/n", Number: 1, WasJudged: false},
	}
	if got := ChangesFromPRs(prs); len(got) != 0 {
		t.Errorf("got %d changes, want 0", len(got))
	}
}

func TestSummarizeReturnsProviderSentence(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{out: `{"summary":"1 PR needs review"}`}
	got := Summarize(context.Background(), p, changed, time.Second)
	if got != "1 PR needs review" {
		t.Errorf("Summarize = %q, want the parsed sentence", got)
	}
}

func TestSummarizeAcceptsRawTextResponse(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{out: "1 PR needs review"}
	got := Summarize(context.Background(), p, changed, time.Second)
	if got != "1 PR needs review" {
		t.Errorf("Summarize = %q, want the raw text trimmed", got)
	}
}

func TestSummarizeFallsBackOnError(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{err: errors.New("provider unavailable")}
	got := Summarize(context.Background(), p, changed, time.Second)
	if got != "o/n#1 changed" {
		t.Errorf("Summarize fallback = %q, want the single-item fallback sentence", got)
	}
}

func TestSummarizeFallsBackOnEmptyResponse(t *testing.T) {
	changed := []Change{
		{Entity: entityPR, Repo: "o/n", Number: 1},
		{Entity: entityIssue, Repo: "o/n", Number: 2},
	}
	p := fakeSummarizer{out: "   "}
	got := Summarize(context.Background(), p, changed, time.Second)
	if got != "2 items changed" {
		t.Errorf("Summarize fallback = %q, want the multi-item fallback sentence", got)
	}
}

func TestSummarizeSkippedWhenNoChanges(t *testing.T) {
	p := fakeSummarizer{out: "should not be called"}
	got := Summarize(context.Background(), p, nil, time.Second)
	if got != "" {
		t.Errorf("Summarize with no changes = %q, want empty (never call the provider for nothing)", got)
	}
}

func TestFireSkippedWhenNoChanges(t *testing.T) {
	// A hook command that would fail loudly if ever invoked, proving Fire never
	// runs it when changed is empty (TDD 9.1).
	if err := Fire(context.Background(), "exit 1", time.Second, nil, "summary"); err != nil {
		t.Errorf("Fire with no changes should no-op, got err: %v", err)
	}
}

func TestFireSkippedWhenHookUnconfigured(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	if err := Fire(context.Background(), "", time.Second, changed, "summary"); err != nil {
		t.Errorf("Fire with empty hook command should no-op, got err: %v", err)
	}
}

func TestFireDeliversJSONOnStdin(t *testing.T) {
	// A real, tiny shell script that captures its stdin to a temp file, so this
	// test proves actual delivery through exec, not just the Go-side marshaling.
	tmp, err := os.CreateTemp("", "gsb-notify-test-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	changed := []Change{
		{Entity: entityPR, Repo: "o/n", Number: 42, URL: "https://github.com/o/n/pull/42", Role: "submitter", Bucket: "open", Action: "review_feedback", Companion: "needs a look", Priority: "elevated"},
	}
	hookCmd := "cat > " + tmp.Name()
	if err := Fire(context.Background(), hookCmd, 2*time.Second, changed, "1 PR needs review"); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	raw, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	var got Payload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("captured stdin did not parse as the payload: %v (raw=%s)", err, raw)
	}
	if got.Summary != "1 PR needs review" {
		t.Errorf("summary = %q", got.Summary)
	}
	if len(got.Changed) != 1 || got.Changed[0].Number != 42 {
		t.Errorf("changed = %+v", got.Changed)
	}
}

func TestFireReturnsErrorOnNonZeroExit(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	err := Fire(context.Background(), "exit 3", time.Second, changed, "summary")
	if err == nil {
		t.Error("Fire should return an error when the hook command exits non-zero")
	}
}

func TestFireReturnsErrorOnTimeout(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	err := Fire(context.Background(), "sleep 5", 20*time.Millisecond, changed, "summary")
	if err == nil {
		t.Error("Fire should return an error when the hook command exceeds the timeout")
	}
}

// TestFireFailureIsNonFatal is a documentation-style test proving the contract
// at the call-site level: Fire returns a plain error rather than panicking or
// calling os.Exit, so the caller (main.go's fireNotifyHook) can log it and
// continue (TDD 9.5). There is nothing to assert beyond "it returns," but that
// is the point — a failing hook must never take down the process.
func TestFireFailureIsNonFatal(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	err := Fire(context.Background(), "exit 1", time.Second, changed, "summary")
	if err == nil {
		t.Fatal("expected an error from the failing hook")
	}
	// Reaching this line at all is the assertion: Fire returned control to the
	// caller instead of aborting the process.
}
