// Package model owns the domain types: the PR record, the closed-set enums
// (bucket, action, close_reason, priority, role), and the event-trail types.
// Enums are parsed once at the boundary that produces them (POLICY: no magic
// strings; a closed set of values is a typed enum).
package model

import "fmt"

// Bucket is the top-level classification of a PR (SCHEMA: bucket).
type Bucket string

const (
	BucketOpen   Bucket = "open"
	BucketStale  Bucket = "stale"
	BucketMerged Bucket = "merged"
	BucketClosed Bucket = "closed"
)

// Action is the finite set of dispositions a live (open) PR may await
// (SCHEMA: action).
type Action string

const (
	ActionAwaitingReview   Action = "awaiting_review"
	ActionChangesRequested Action = "changes_requested"
	ActionAuthorActive     Action = "author_active"
	ActionUnassigned       Action = "unassigned"
	ActionBlockedExternal  Action = "blocked_external"
	ActionMergeReady       Action = "merge_ready"
	// ActionConflicted marks a submitter's open PR that conflicts with its base
	// branch. Set deterministically from GitHub's mergeable flag (not inferred),
	// since a merge conflict is a hard fact and, like changes_requested, needs
	// the author to act.
	ActionConflicted Action = "conflicted"
	// ActionMergeBlocked marks a submitter's open PR GitHub reports as
	// mergeable_state "blocked": an unmet branch-protection requirement
	// (required review, required check, required signature, or any other rule
	// GitHub does not detail in this call) is preventing merge. Set
	// deterministically (not inferred), distinct from ActionConflicted — dirty
	// and blocked are mutually exclusive states GitHub reports (TDD 4.7a).
	ActionMergeBlocked Action = "merge_blocked"
	// ActionReviewFeedback marks a submitter's open PR carrying unresolved
	// review threads: reviewers have left line comments the author has not yet
	// resolved. Set deterministically from GitHub's review-thread resolution
	// state (not inferred), because an approval can coexist with still-open
	// comments — the ball is with the author either way.
	ActionReviewFeedback Action = "review_feedback"
)

// CloseReason is the finite set of reasons a closed-unmerged PR carries
// (SCHEMA: close_reason).
type CloseReason string

const (
	CloseReasonSuperseded CloseReason = "superseded"
	CloseReasonCancelled  CloseReason = "cancelled"
	CloseReasonStale      CloseReason = "stale"
	CloseReasonInvalid    CloseReason = "invalid"
)

// Priority is a PR's inferred urgency (SCHEMA: priority).
type Priority string

const (
	PriorityNeutral  Priority = "neutral"
	PriorityElevated Priority = "elevated"
)

// Role is the operator's relationship to a PR (SCHEMA: role).
type Role string

const (
	RoleSubmitter Role = "submitter"
	RoleReviewer  Role = "reviewer"
)

// GitHubState is the raw GitHub state sent to the provider so it honors the
// immutable floors (SCHEMA: request github_state).
type GitHubState string

const (
	GitHubStateOpen   GitHubState = "open"
	GitHubStateClosed GitHubState = "closed"
	GitHubStateMerged GitHubState = "merged"
)

// ProviderSource records which provider slot produced an item's current
// judgment (SCHEMA: provider; TDD 6.16). Shared between PR and Issue — unlike
// Bucket/Action, provenance is not entity-specific, so one type serves both
// rather than being duplicated into issueenums.go. Deliberately always set,
// never omitted: the operator chose explicit disclosure over an omitempty
// convention (unlike CIFailing/Unverified), so a reader of the raw YAML never
// needs to know an absence convention to tell primary from fallback.
type ProviderSource string

const (
	ProviderSourcePrimary  ProviderSource = "primary"
	ProviderSourceFallback ProviderSource = "fallback"
)

// validBuckets and its siblings are the single source of enum membership,
// used by both parsing and validation so the closed set is defined once.
var (
	validBuckets = map[Bucket]struct{}{
		BucketOpen: {}, BucketStale: {}, BucketMerged: {}, BucketClosed: {},
	}
	validActions = map[Action]struct{}{
		ActionAwaitingReview: {}, ActionChangesRequested: {}, ActionAuthorActive: {},
		ActionUnassigned: {}, ActionBlockedExternal: {}, ActionMergeReady: {},
		ActionConflicted: {}, ActionMergeBlocked: {}, ActionReviewFeedback: {},
	}
	validCloseReasons = map[CloseReason]struct{}{
		CloseReasonSuperseded: {}, CloseReasonCancelled: {},
		CloseReasonStale: {}, CloseReasonInvalid: {},
	}
	validPriorities = map[Priority]struct{}{
		PriorityNeutral: {}, PriorityElevated: {},
	}
	validRoles = map[Role]struct{}{
		RoleSubmitter: {}, RoleReviewer: {},
	}
	validProviderSources = map[ProviderSource]struct{}{
		ProviderSourcePrimary: {}, ProviderSourceFallback: {},
	}
)

// ParseBucket validates s and returns the typed Bucket, or an error if s is
// not a member of the closed vocabulary.
func ParseBucket(s string) (Bucket, error) {
	b := Bucket(s)
	if _, ok := validBuckets[b]; !ok {
		return "", fmt.Errorf("invalid bucket %q", s)
	}
	return b, nil
}

// Valid reports whether the Bucket is a member of the closed vocabulary.
func (b Bucket) Valid() bool { _, ok := validBuckets[b]; return ok }

// ParseAction validates s and returns the typed Action.
func ParseAction(s string) (Action, error) {
	a := Action(s)
	if _, ok := validActions[a]; !ok {
		return "", fmt.Errorf("invalid action %q", s)
	}
	return a, nil
}

// Valid reports whether the Action is a member of the closed vocabulary.
func (a Action) Valid() bool { _, ok := validActions[a]; return ok }

// ParseCloseReason validates s and returns the typed CloseReason.
func ParseCloseReason(s string) (CloseReason, error) {
	c := CloseReason(s)
	if _, ok := validCloseReasons[c]; !ok {
		return "", fmt.Errorf("invalid close_reason %q", s)
	}
	return c, nil
}

// Valid reports whether the CloseReason is a member of the closed vocabulary.
func (c CloseReason) Valid() bool { _, ok := validCloseReasons[c]; return ok }

// ParsePriority validates s and returns the typed Priority.
func ParsePriority(s string) (Priority, error) {
	p := Priority(s)
	if _, ok := validPriorities[p]; !ok {
		return "", fmt.Errorf("invalid priority %q", s)
	}
	return p, nil
}

// Valid reports whether the Priority is a member of the closed vocabulary.
func (p Priority) Valid() bool { _, ok := validPriorities[p]; return ok }

// ParseRole validates s and returns the typed Role.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if _, ok := validRoles[r]; !ok {
		return "", fmt.Errorf("invalid role %q", s)
	}
	return r, nil
}

// Valid reports whether the Role is a member of the closed vocabulary.
func (r Role) Valid() bool { _, ok := validRoles[r]; return ok }

// ParseProviderSource validates s and returns the typed ProviderSource.
func ParseProviderSource(s string) (ProviderSource, error) {
	p := ProviderSource(s)
	if _, ok := validProviderSources[p]; !ok {
		return "", fmt.Errorf("invalid provider source %q", s)
	}
	return p, nil
}

// Valid reports whether the ProviderSource is a member of the closed vocabulary.
func (p ProviderSource) Valid() bool { _, ok := validProviderSources[p]; return ok }
