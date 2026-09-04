package model

import "time"

// EventKind is the type of an event-trail entry. It is intentionally a plain
// string (not a closed enum) because the trail carries heterogeneous GitHub
// activity and new kinds may appear; the provider reads them as prose.
type EventKind string

const (
	EventComment          EventKind = "comment"
	EventReview           EventKind = "review"
	EventStateTransition  EventKind = "state_transition"
	EventLabel            EventKind = "label"
	EventReviewRequested  EventKind = "review_requested"
	EventReadyForReview   EventKind = "ready_for_review"
	EventConvertedToDraft EventKind = "converted_to_draft"
	// EventCIStatus summarizes the current CI/check-run outcome on the PR's head
	// commit (success / failure / pending), so the model judges from the latest
	// build state rather than a stale comment mentioning an earlier failure.
	EventCIStatus EventKind = "ci_status"
	// EventReviewThreads summarizes the PR's unresolved review threads — the
	// outstanding line comments a submitter must address. GitHub exposes
	// resolution state only via GraphQL, so this is the only signal that
	// distinguishes "approved and done" from "approved but comments still open".
	EventReviewThreads EventKind = "review_threads"
	// EventReviewDecision carries GitHub's overall review decision
	// (APPROVED / CHANGES_REQUESTED / REVIEW_REQUIRED), a hard upstream verdict
	// that contradicts a model guess of "conditionally approved".
	EventReviewDecision EventKind = "review_decision"
)

// Event is one chronological, timestamped entry in a PR's event trail
// (TDD 1.4). Each entry carries its author and a role indication.
type Event struct {
	Timestamp time.Time `yaml:"timestamp" json:"timestamp"`
	Author    string    `yaml:"author" json:"author"`
	RoleHint  string    `yaml:"role_hint,omitempty" json:"role_hint,omitempty"`
	Kind      EventKind `yaml:"kind" json:"kind"`
	Text      string    `yaml:"text,omitempty" json:"text,omitempty"`
}

// PR is the domain record for one tracked pull request. Fields the tool derives
// on each run are overwritten on merge; OperatorStale (and any future
// operator-set field) is preserved across runs (SCHEMA; TDD 2.2).
//
// The Events trail is used for classification and is not persisted to the store
// (the store holds the judged result, not the raw trail).
type PR struct {
	Repo    string    `yaml:"repo" json:"repo"`
	Number  int       `yaml:"number" json:"number"`
	Title   string    `yaml:"title" json:"title"`
	URL     string    `yaml:"url" json:"url"`
	Role    Role      `yaml:"role" json:"role"`
	Created time.Time `yaml:"created" json:"created"`

	// GitHubState is the raw upstream state used to enforce the immutable
	// floors during classification. Not persisted.
	GitHubState GitHubState `yaml:"-" json:"-"`

	// Mergeable carries GitHub's mergeability flag for an open PR: false means
	// it conflicts with its base branch. nil when unknown/not applicable
	// (closed/merged, or GitHub had not yet computed it). Drives the
	// deterministic conflicted action for submitter PRs. Not persisted.
	Mergeable *bool `yaml:"-" json:"-"`

	// ReviewRequested is true when GitHub currently has a pending review request
	// on the operator for this PR (the PR surfaced from the review-requested
	// search). It is a live GitHub fact — the "please review" banner — that
	// deterministically marks a reviewer PR as awaiting our action, overriding
	// model inference. Recomputed each run; not persisted.
	ReviewRequested bool `yaml:"-" json:"-"`

	// UnresolvedThreads is the count of open (unresolved, non-outdated) review
	// threads on an open PR — outstanding line comments the submitter still has
	// to answer. It is a live GitHub fact available only via GraphQL, and a
	// non-zero count deterministically puts the ball in the submitter's court
	// even when a reviewer has already approved. Recomputed each run; not
	// persisted.
	UnresolvedThreads int `yaml:"-" json:"-"`

	Bucket      Bucket      `yaml:"bucket" json:"bucket"`
	Action      Action      `yaml:"action,omitempty" json:"action,omitempty"`
	CloseReason CloseReason `yaml:"close_reason,omitempty" json:"close_reason,omitempty"`
	Priority    Priority    `yaml:"priority" json:"priority"`
	Companion   string      `yaml:"companion,omitempty" json:"companion,omitempty"`
	// Emoji is the model-inferred single-glyph summary that prefixes the PR's
	// note cell in the rendered document. Absent when unverified or when notes
	// were skipped. Persisted so the render stays a pure function of the store.
	Emoji      string `yaml:"emoji,omitempty" json:"emoji,omitempty"`
	Unverified bool   `yaml:"unverified,omitempty" json:"unverified,omitempty"`

	// CIFailing is true when the open PR's head commit has one or more failing
	// checks. It is a deterministic fact from GitHub's check-run conclusions, set
	// only for submitter PRs (another author's broken build is not the operator's
	// to fix) and never for the merged/closed floors (TDD 4.10).
	//
	// It is a flag rather than an Action value on purpose: CI state and review
	// state are orthogonal, and Action is single-valued, so routing a red build
	// through Action would force it to shadow whatever review-derived disposition
	// the PR also carries — hiding a reviewer's actionable request behind a build
	// failure the operator may not be able to address (TDD 4.11). Persisted so the
	// render stays a pure function of the store.
	CIFailing bool `yaml:"ci_failing,omitempty" json:"ci_failing,omitempty"`

	// OperatorStale is the operator's manual Stale override; preserved on merge
	// across runs (TDD 2.2, 5.1).
	OperatorStale bool `yaml:"operator_stale,omitempty" json:"operator_stale,omitempty"`

	MergedAt *time.Time `yaml:"merged_at,omitempty" json:"merged_at,omitempty"`
	ClosedAt *time.Time `yaml:"closed_at,omitempty" json:"closed_at,omitempty"`

	// LastActivity is the timestamp of the most recent event, used for the age
	// threshold in stale determination (TDD 5.2) and rendered as the Updated
	// column. It is persisted — unlike the trail it is derived from — because a
	// terminal PR is carried forward without being re-crawled (TDD 1.5), so
	// without a stored value such a row would have no activity date at all and
	// the render (a pure function of the store) could not show one.
	LastActivity time.Time `yaml:"last_activity,omitempty" json:"last_activity,omitempty"`

	// InputFingerprint is an opaque hash of every deterministic input that could
	// change an open PR's judgment: the most recent trail event, CIFailing,
	// UnresolvedThreads, Mergeable, and ReviewRequested. It exists solely so a
	// later run can detect "nothing worth re-judging happened" without trusting
	// LastActivity alone, which live GitHub data proved insufficient — a full
	// check-run cycle can complete without moving a PR's own last-modified
	// signal (TDD 4.13). Persisted so the comparison survives across runs;
	// opaque (not itself meaningful) so it never needs to grow the schema again
	// when a new input starts mattering — only the hash's inputs change.
	InputFingerprint string `yaml:"input_fingerprint,omitempty" json:"input_fingerprint,omitempty"`

	// Events is the chronological event trail (TDD 1.4). Not persisted.
	Events []Event `yaml:"-" json:"-"`

	// WasJudged is true when this run's classification actually reached the
	// provider for a fresh judgment — a first-seen PR, one whose deterministic
	// inputs changed, or one whose prior record was unverified (TDD 4.13,
	// 4.14) — and false when the unchanged-input skip carried the prior verdict
	// forward, or the item is not open at all (merged/closed/stale items never
	// participate in the fingerprint concept). It exists solely to drive the
	// notification hook's change collection (TDD 9.1, 9.2); it is a run-local
	// signal, not a fact about the PR, so it is never persisted.
	WasJudged bool `yaml:"-" json:"-"`
}

// Key uniquely identifies a PR across the tracked set and the store.
func (p PR) Key() string {
	return p.Repo + "#" + itoa(p.Number)
}

// itoa is a tiny dependency-free int-to-string for keys.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
