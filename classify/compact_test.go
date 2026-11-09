package classify

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/config"
	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// requestRecorder keeps the last Request handed to the provider so a test can
// inspect exactly what the model would have been sent.
type requestRecorder struct {
	mu   sync.Mutex
	last provider.Request
}

func (r *requestRecorder) Name() string { return "recorder" }
func (r *requestRecorder) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	r.mu.Lock()
	r.last = req
	r.mu.Unlock()
	if req.Entity == provider.EntityIssue {
		return issueGoodJSON, nil
	}
	return psGoodJSON, nil
}
func (r *requestRecorder) Summarize(ctx context.Context, prompt string) (string, error) {
	return "", nil
}

const issueGoodJSON = `{"bucket":"open","action":"awaiting_others","priority":"neutral","companion":"waiting on others to reply","emoji":"⏳"}`

const compactFixtureHTML = `<!-- tracking --><p>Please <strong>rebase</strong>.</p><picture><img src="https://x/a.svg"></picture>` +
	`<details><summary>Walkthrough</summary><p>restated walkthrough body</p></details>`

func recordingClassifier(rec *requestRecorder) *Classifier {
	cfg := config.Default()
	cfg.GitHubToken = "tok"
	cfg.ClassifyWorkers = 1
	cfg.SkipFloorNotes = false
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(rec, nil, cfg, log, func() time.Time { return psNow }, nil, nil, "")
}

func assertCompacted(t *testing.T, sent []model.Event) {
	t.Helper()
	if len(sent) == 0 {
		t.Fatal("no events reached the provider")
	}
	got := sent[len(sent)-1].Text
	if strings.Contains(got, "<") || strings.Contains(got, "tracking") || strings.Contains(got, "restated walkthrough body") {
		t.Errorf("provider received uncompacted text: %q", got)
	}
	if !strings.Contains(got, "**rebase**") {
		t.Errorf("provider text lost its content: %q", got)
	}
}

// TestPRJudgmentSendsCompactedEventsAndLeavesTrailIntact covers TDD 6.19: the
// model is sent the converted, de-wasted text, while the PR's own trail (which
// feeds the unchanged-input fingerprint) is not modified.
func TestPRJudgmentSendsCompactedEventsAndLeavesTrailIntact(t *testing.T) {
	rec := &requestRecorder{}
	c := recordingClassifier(rec)
	pr := model.PR{
		Repo: "o/n", Number: 1, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: psNow.Add(-24 * time.Hour), LastActivity: psNow.Add(-time.Hour),
		Events: []model.Event{{Timestamp: psNow.Add(-time.Hour), Author: "coderabbitai[bot]", Kind: model.EventComment, Text: compactFixtureHTML}},
	}
	before := fingerprint(pr)

	c.classifyOne(context.Background(), pr)

	assertCompacted(t, rec.last.Events)
	if pr.Events[0].Text != compactFixtureHTML {
		t.Errorf("the PR's own trail was modified: %q", pr.Events[0].Text)
	}
	if fingerprint(pr) != before {
		t.Error("fingerprint changed after judgment; it must hash the uncompacted trail")
	}
}

// TestIssueJudgmentSendsCompactedEventsAndLeavesTrailIntact covers TDD 6.19 for
// the issue path.
func TestIssueJudgmentSendsCompactedEventsAndLeavesTrailIntact(t *testing.T) {
	rec := &requestRecorder{}
	c := recordingClassifier(rec)
	iss := model.Issue{
		Repo: "o/n", Number: 2, Role: model.IssueRoleAuthor,
		Created: issueRefNow.Add(-24 * time.Hour), LastActivity: issueRefNow.Add(-time.Hour), CommentCount: 1,
		Events: []model.Event{{Timestamp: issueRefNow.Add(-time.Hour), Author: "coderabbitai[bot]", Kind: model.EventComment, Text: compactFixtureHTML}},
	}
	before := issueFingerprint(iss)

	c.classifyIssue(context.Background(), iss)

	assertCompacted(t, rec.last.Events)
	if iss.Events[0].Text != compactFixtureHTML {
		t.Errorf("the issue's own trail was modified: %q", iss.Events[0].Text)
	}
	if issueFingerprint(iss) != before {
		t.Error("issue fingerprint changed after judgment")
	}
}
