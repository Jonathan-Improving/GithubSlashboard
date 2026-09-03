package model

import "fmt"

// The issue vocabulary is deliberately its own closed set rather than a reuse of
// the PR enums (SCHEMA § issue vocabularies). Issues are simpler: there is no
// merge, so no merged bucket; the close reason is GitHub's own state_reason (a
// hard fact, never inferred); and the action set is narrower because an issue has
// no review machinery. Keeping the sets separate keeps each one tight — an
// action that is meaningless for an entity is not even parseable for it
// (POLICY: a closed set of values is a typed enum parsed once at the boundary).

// IssueBucket is the top-level classification of an issue. It mirrors the PR
// bucket minus merged, which has no meaning for an issue.
type IssueBucket string

const (
	IssueBucketOpen   IssueBucket = "open"
	IssueBucketStale  IssueBucket = "stale"
	IssueBucketClosed IssueBucket = "closed"
)

// IssueAction is the finite set of dispositions a live (open) issue may await.
type IssueAction string

const (
	// IssueActionAwaitingResponse means someone has asked the operator
	// something: the ball is with us.
	IssueActionAwaitingResponse IssueAction = "awaiting_response"
	// IssueActionAwaitingOthers means the operator is waiting on someone else.
	IssueActionAwaitingOthers IssueAction = "awaiting_others"
	// IssueActionTriage means nobody has engaged with the issue yet — the
	// deterministic disposition of an issue with no conversation (TDD 8.3).
	IssueActionTriage IssueAction = "triage"
	// IssueActionResolvedPendingClose means the substance is settled and the
	// issue is only awaiting a close.
	IssueActionResolvedPendingClose IssueAction = "resolved_pending_close"
)

// IssueCloseReason is GitHub's own state_reason for a closed issue. Unlike a
// PR's close_reason, it is never inferred: GitHub supplies it directly, so it is
// a hard fact recorded verbatim (TDD 8.2).
type IssueCloseReason string

const (
	IssueCloseReasonCompleted  IssueCloseReason = "completed"
	IssueCloseReasonNotPlanned IssueCloseReason = "not_planned"
	IssueCloseReasonDuplicate  IssueCloseReason = "duplicate"
	IssueCloseReasonReopened   IssueCloseReason = "reopened"
)

// IssueRole is the operator's relationship to an issue. Participation covers
// commenting *or* being assigned — an assignment is itself an ask, so it counts
// as engagement even with no comment from the operator (TDD 8.1).
type IssueRole string

const (
	IssueRoleAuthor      IssueRole = "author"
	IssueRoleParticipant IssueRole = "participant"
)

// validIssueBuckets and its siblings are the single source of enum membership
// for issues, used by both parsing and validation.
var (
	validIssueBuckets = map[IssueBucket]struct{}{
		IssueBucketOpen: {}, IssueBucketStale: {}, IssueBucketClosed: {},
	}
	validIssueActions = map[IssueAction]struct{}{
		IssueActionAwaitingResponse: {}, IssueActionAwaitingOthers: {},
		IssueActionTriage: {}, IssueActionResolvedPendingClose: {},
	}
	validIssueCloseReasons = map[IssueCloseReason]struct{}{
		IssueCloseReasonCompleted: {}, IssueCloseReasonNotPlanned: {},
		IssueCloseReasonDuplicate: {}, IssueCloseReasonReopened: {},
	}
	validIssueRoles = map[IssueRole]struct{}{
		IssueRoleAuthor: {}, IssueRoleParticipant: {},
	}
)

// ParseIssueBucket validates s and returns the typed IssueBucket.
func ParseIssueBucket(s string) (IssueBucket, error) {
	b := IssueBucket(s)
	if _, ok := validIssueBuckets[b]; !ok {
		return "", fmt.Errorf("invalid issue bucket %q", s)
	}
	return b, nil
}

// Valid reports whether the IssueBucket is a member of the closed vocabulary.
func (b IssueBucket) Valid() bool { _, ok := validIssueBuckets[b]; return ok }

// ParseIssueAction validates s and returns the typed IssueAction. This is the
// boundary at which a provider-supplied action string becomes a typed issue
// action (the provider response carries it as a plain string, since the valid
// vocabulary depends on the entity being judged).
func ParseIssueAction(s string) (IssueAction, error) {
	a := IssueAction(s)
	if _, ok := validIssueActions[a]; !ok {
		return "", fmt.Errorf("invalid issue action %q", s)
	}
	return a, nil
}

// Valid reports whether the IssueAction is a member of the closed vocabulary.
func (a IssueAction) Valid() bool { _, ok := validIssueActions[a]; return ok }

// ParseIssueCloseReason validates s and returns the typed IssueCloseReason. The
// input is GitHub's state_reason, so an unrecognized value is a genuine parse
// failure rather than a model mistake.
func ParseIssueCloseReason(s string) (IssueCloseReason, error) {
	r := IssueCloseReason(s)
	if _, ok := validIssueCloseReasons[r]; !ok {
		return "", fmt.Errorf("invalid issue close reason %q", s)
	}
	return r, nil
}

// Valid reports whether the IssueCloseReason is a member of the closed
// vocabulary.
func (r IssueCloseReason) Valid() bool { _, ok := validIssueCloseReasons[r]; return ok }

// ParseIssueRole validates s and returns the typed IssueRole.
func ParseIssueRole(s string) (IssueRole, error) {
	r := IssueRole(s)
	if _, ok := validIssueRoles[r]; !ok {
		return "", fmt.Errorf("invalid issue role %q", s)
	}
	return r, nil
}

// Valid reports whether the IssueRole is a member of the closed vocabulary.
func (r IssueRole) Valid() bool { _, ok := validIssueRoles[r]; return ok }

// IssueBucketValues, IssueActionValues, and IssueRoleValues return the closed
// vocabularies as sorted string slices, for supplying to the provider in the
// request constraints (the provider is told the entity's vocabulary rather than
// inferring it).
func IssueBucketValues() []string {
	return []string{
		string(IssueBucketOpen), string(IssueBucketStale), string(IssueBucketClosed),
	}
}

// IssueActionValues returns the open-issue action vocabulary.
func IssueActionValues() []string {
	return []string{
		string(IssueActionAwaitingResponse), string(IssueActionAwaitingOthers),
		string(IssueActionTriage), string(IssueActionResolvedPendingClose),
	}
}
