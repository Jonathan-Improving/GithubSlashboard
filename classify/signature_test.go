package classify

import (
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TestStatusSignatureCoversDispositionFields proves the PR signature changes
// when any disposition-bearing field changes, and — critically — when only the
// LastActivity timestamp changes, since genuinely new activity is a real status
// change the notification must catch (TDD 9.1).
func TestStatusSignatureCoversDispositionFields(t *testing.T) {
	base := model.PR{
		Repo: "o/n", Number: 1, Role: model.RoleSubmitter, Bucket: model.BucketOpen,
		Action: model.ActionAwaitingReview, Priority: model.PriorityNeutral,
		Companion: "a short note here", Emoji: "⏳", LastActivity: time.Unix(1000, 0),
	}
	sig := statusSignature(base)

	mutations := map[string]func(*model.PR){
		"bucket":       func(p *model.PR) { p.Bucket = model.BucketStale },
		"action":       func(p *model.PR) { p.Action = model.ActionMergeReady },
		"ci_failing":   func(p *model.PR) { p.CIFailing = true },
		"priority":     func(p *model.PR) { p.Priority = model.PriorityElevated },
		"emoji":        func(p *model.PR) { p.Emoji = "✅" },
		"companion":    func(p *model.PR) { p.Companion = "a different note now" },
		"lastActivity": func(p *model.PR) { p.LastActivity = time.Unix(2000, 0) },
	}
	for name, mut := range mutations {
		p := base
		mut(&p)
		if statusSignature(p) == sig {
			t.Errorf("changing %s must change the signature but did not", name)
		}
	}
}

// TestStatusSignatureExcludesNowDerivedValues proves that two records differing
// only in fields the signature must NOT read (the transient acquisition inputs
// that feed Age/Updated indirectly) produce the same signature — the signature
// is over stored disposition, not over now-relative derivations, so mere
// passage of time between runs never fires a notification (TDD 9.1, plan
// Settled decision 3). LastActivity itself IS in the signature; what is
// excluded is everything derived from now at render time, which no field on the
// record even stores — so the guard here is that non-disposition transient
// inputs (e.g. UnresolvedThreads, ReviewRequested) do not leak in.
func TestStatusSignatureExcludesTransientInputs(t *testing.T) {
	base := model.PR{
		Repo: "o/n", Number: 1, Bucket: model.BucketOpen, Action: model.ActionAwaitingReview,
		Priority: model.PriorityNeutral, Companion: "a short note here", Emoji: "⏳",
		LastActivity: time.Unix(1000, 0),
	}
	other := base
	other.UnresolvedThreads = 5
	other.ReviewRequested = true
	other.InputFingerprint = "deadbeef"
	other.Mergeable = new(bool)

	if statusSignature(base) != statusSignature(other) {
		t.Error("transient acquisition inputs that are not part of the disposition must not enter the signature")
	}
}

// TestIssueStatusSignatureCoversDispositionFields mirrors the PR coverage test
// for issues, over the fields an issue actually has (TDD 9.1).
func TestIssueStatusSignatureCoversDispositionFields(t *testing.T) {
	base := model.Issue{
		Repo: "a/x", Number: 1, Role: model.IssueRoleAuthor, Bucket: model.IssueBucketOpen,
		Action: model.IssueActionTriage, Priority: model.PriorityNeutral,
		Companion: "a short note here", Emoji: "⏳", LastActivity: time.Unix(1000, 0),
	}
	sig := issueStatusSignature(base)

	mutations := map[string]func(*model.Issue){
		"bucket":       func(i *model.Issue) { i.Bucket = model.IssueBucketStale },
		"action":       func(i *model.Issue) { i.Action = model.IssueActionAwaitingResponse },
		"priority":     func(i *model.Issue) { i.Priority = model.PriorityElevated },
		"emoji":        func(i *model.Issue) { i.Emoji = "✅" },
		"companion":    func(i *model.Issue) { i.Companion = "a different note now" },
		"closeReason":  func(i *model.Issue) { i.CloseReason = model.IssueCloseReasonCompleted },
		"lastActivity": func(i *model.Issue) { i.LastActivity = time.Unix(2000, 0) },
	}
	for name, mut := range mutations {
		i := base
		mut(&i)
		if issueStatusSignature(i) == sig {
			t.Errorf("changing %s must change the issue signature but did not", name)
		}
	}
}
