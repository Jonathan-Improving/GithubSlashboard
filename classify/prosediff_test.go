package classify

import (
	"context"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

const proseOnlyJudgment = `{"bucket":"open","action":"awaiting_review","priority":"neutral","companion":"waiting on a reviewer now","emoji":"🔄"}`

func priorWithProse(lastActivity time.Time, companion, emoji string) model.PR {
	return model.PR{
		Repo: "o/n", Number: 50, Role: model.RoleSubmitter,
		Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, Priority: model.PriorityNeutral,
		Companion: companion, Emoji: emoji, LastActivity: lastActivity,
	}
}

func freshProsePR(lastActivity time.Time) model.PR {
	return model.PR{
		Repo: "o/n", Number: 50, GitHubState: model.GitHubStateOpen, Role: model.RoleSubmitter,
		Created: lastActivity, LastActivity: lastActivity,
		Events: []model.Event{{Timestamp: lastActivity, Kind: model.EventComment, Text: "lgtm"}},
	}
}

// TDD 9.9: a reworded note alone is held back for the Prose Diff Checker, with
// the prior prose attached, and is not counted as a change.
func TestProseOnlyDifferenceIsHeldBackNotChanged(t *testing.T) {
	now := time.Now()
	prior := priorWithProse(now, "an older note", "⏳")
	c, _ := unchangedOpenClassifierWithPrior(proseOnlyJudgment, now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), freshProsePR(now))

	if got.StatusChanged {
		t.Error("StatusChanged = true for a prose-only difference, want false (TDD 9.9)")
	}
	if got.ProseHeldBack == nil {
		t.Fatal("ProseHeldBack = nil, want the prior prose held back for the Prose Diff Checker")
	}
	if got.ProseHeldBack.OldCompanion != "an older note" || got.ProseHeldBack.OldEmoji != "⏳" {
		t.Errorf("held-back prior prose = %+v, want the prior record's note and emoji", got.ProseHeldBack)
	}
	if got.Companion != "waiting on a reviewer now" {
		t.Errorf("companion = %q, want the new note kept (TDD 9.12)", got.Companion)
	}
}

func TestEmojiOnlyDifferenceIsHeldBack(t *testing.T) {
	now := time.Now()
	prior := priorWithProse(now, "waiting on a reviewer now", "⏳")
	c, _ := unchangedOpenClassifierWithPrior(proseOnlyJudgment, now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), freshProsePR(now))

	if got.StatusChanged || got.ProseHeldBack == nil {
		t.Errorf("StatusChanged = %t, ProseHeldBack = %v, want false and non-nil for an emoji-only difference", got.StatusChanged, got.ProseHeldBack)
	}
}

// TDD 9.9: a hard-field difference is counted immediately and never held back,
// even when the prose differs too.
func TestHardFieldDifferenceIsChangedAndNeverHeldBack(t *testing.T) {
	now := time.Now()
	prior := priorWithProse(now, "an older note", "⏳")
	prior.Priority = model.PriorityElevated
	c, _ := unchangedOpenClassifierWithPrior(proseOnlyJudgment, now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), freshProsePR(now))

	if !got.StatusChanged {
		t.Error("StatusChanged = false for a priority change, want true (TDD 9.9)")
	}
	if got.ProseHeldBack != nil {
		t.Errorf("ProseHeldBack = %+v, want nil when a hard field differs", got.ProseHeldBack)
	}
}

func TestIdenticalProseIsNeitherChangedNorHeldBack(t *testing.T) {
	now := time.Now()
	prior := priorWithProse(now, "waiting on a reviewer now", "🔄")
	c, _ := unchangedOpenClassifierWithPrior(proseOnlyJudgment, now, map[string]model.PR{prior.Key(): prior})

	got := c.classifyOne(context.Background(), freshProsePR(now))

	if got.StatusChanged || got.ProseHeldBack != nil {
		t.Errorf("StatusChanged = %t, ProseHeldBack = %v, want false and nil", got.StatusChanged, got.ProseHeldBack)
	}
}

func TestFirstSeenPRIsNeverHeldBack(t *testing.T) {
	now := time.Now()
	c, _ := unchangedOpenClassifierWithPrior(proseOnlyJudgment, now, nil)

	got := c.classifyOne(context.Background(), freshProsePR(now))

	if got.StatusChanged || got.ProseHeldBack != nil {
		t.Errorf("a first-seen PR has nothing to diff against; got StatusChanged = %t, ProseHeldBack = %v", got.StatusChanged, got.ProseHeldBack)
	}
}

const issueProseOnlyJudgment = `{"bucket":"open","action":"awaiting_others","priority":"neutral","companion":"still waiting on others","emoji":"🔄"}`

func proseIssuePair(priorCompanion, priorEmoji string) (model.Issue, model.Issue) {
	prior := model.Issue{
		Repo: "a/x", Number: 73, Role: model.IssueRoleAuthor, Bucket: model.IssueBucketOpen,
		Action: model.IssueActionAwaitingOthers, Priority: model.PriorityNeutral,
		Companion: priorCompanion, Emoji: priorEmoji, LastActivity: issueRefNow,
	}
	fresh := model.Issue{
		Repo: "a/x", Number: 73, Role: model.IssueRoleAuthor, CommentCount: 1,
		Created: issueRefNow, LastActivity: issueRefNow,
		Events: []model.Event{{Timestamp: issueRefNow, Kind: model.EventComment, Text: "still going"}},
	}
	return prior, fresh
}

func TestIssueProseOnlyDifferenceIsHeldBackNotChanged(t *testing.T) {
	prior, fresh := proseIssuePair("an older issue note", "⏳")
	c, _ := issueClassifierWithPrior(issueProseOnlyJudgment, map[string]model.Issue{prior.Key(): prior})

	got := c.classifyIssue(context.Background(), fresh)

	if got.StatusChanged {
		t.Error("StatusChanged = true for a prose-only issue difference, want false (TDD 9.9)")
	}
	if got.ProseHeldBack == nil || got.ProseHeldBack.OldCompanion != "an older issue note" {
		t.Errorf("ProseHeldBack = %+v, want the prior issue prose", got.ProseHeldBack)
	}
}

func TestIssueIdenticalProseIsNotHeldBack(t *testing.T) {
	prior, fresh := proseIssuePair("still waiting on others", "🔄")
	c, _ := issueClassifierWithPrior(issueProseOnlyJudgment, map[string]model.Issue{prior.Key(): prior})

	got := c.classifyIssue(context.Background(), fresh)

	if got.StatusChanged || got.ProseHeldBack != nil {
		t.Errorf("StatusChanged = %t, ProseHeldBack = %v, want false and nil", got.StatusChanged, got.ProseHeldBack)
	}
}
