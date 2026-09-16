package classify

import (
	"fmt"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// statusSignature maps a PR to a value built from exactly the fields that
// constitute its disposition, and from nothing time-relative. It is the single
// definition of "the same status" consumed by the notification gating diff
// (TDD 9.1): the classifier collects an item as changed this run when its
// signature differs from that of its prior stored record.
//
// It folds in Action rather than the reviewer ball-holding derived from it
// (render.ballWithUs), because Action fully determines that derivation — so a
// change the operator would see as "the ball moved to us" is already a change
// in Action, captured here without this package depending on render. It folds
// in LastActivity as its raw timestamp, since genuinely new activity is a real
// status change; it deliberately excludes every now-derived value (Age, the
// rendered "Updated" column), which change every run and would otherwise fire a
// notification on every live item merely because time passed (TDD 9.1, plan
// Settled decision 3).
func statusSignature(p model.PR) string {
	return fmt.Sprintf("%s|%s|%t|%s|%s|%s|%s|%s",
		p.Bucket, p.Action, p.CIFailing, p.Priority, p.Emoji, p.Companion,
		p.CloseReason, p.LastActivity.UTC().Format(time.RFC3339Nano))
}

// issueStatusSignature mirrors statusSignature for an issue, over the fields an
// issue actually has: no CI flag and no PR-shaped close sub-reason machinery,
// but the same shared-definition and time-relative-exclusion properties
// (TDD 9.1). An issue's close reason is a hard GitHub fact and part of its
// disposition, so it is included; Age/Updated are excluded for the same reason
// as on a PR.
func issueStatusSignature(i model.Issue) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s",
		i.Bucket, i.Action, i.Priority, i.Emoji, i.Companion,
		i.CloseReason, i.LastActivity.UTC().Format(time.RFC3339Nano))
}
