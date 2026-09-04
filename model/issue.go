package model

import "time"

// Issue is the domain record for one tracked GitHub issue. It is a sibling of
// PR, not a generalization of it: an issue carries no merge state, no
// mergeability, and no review machinery, and its close reason is a hard GitHub
// fact rather than an inference (TDD 8.2). Fields the tool derives on each run
// are overwritten on merge; OperatorStale is preserved across runs (TDD 8.7).
//
// The Events trail is used for classification and is not persisted (the store
// holds the judged result, not the raw trail).
type Issue struct {
	Repo    string    `yaml:"repo" json:"repo"`
	Number  int       `yaml:"number" json:"number"`
	Title   string    `yaml:"title" json:"title"`
	URL     string    `yaml:"url" json:"url"`
	Role    IssueRole `yaml:"role" json:"role"`
	Created time.Time `yaml:"created" json:"created"`

	// GitHubClosed is the raw upstream state used to enforce the immutable
	// closed floor during classification (TDD 8.2). Not persisted — the bucket
	// records the judged outcome, and the raw state is recomputed each run.
	GitHubClosed bool `yaml:"-" json:"-"`

	// CommentCount is the number of comments GitHub reports on the issue. It
	// gates the provider call: an issue with no comments has no conversation for
	// a model to read, so it is classified from hard facts alone (TDD 8.3).
	// Recomputed each run; not persisted.
	CommentCount int `yaml:"-" json:"-"`

	Bucket      IssueBucket      `yaml:"bucket" json:"bucket"`
	Action      IssueAction      `yaml:"action,omitempty" json:"action,omitempty"`
	CloseReason IssueCloseReason `yaml:"close_reason,omitempty" json:"close_reason,omitempty"`
	Priority    Priority         `yaml:"priority" json:"priority"`
	Companion   string           `yaml:"companion,omitempty" json:"companion,omitempty"`
	// Emoji is the model-inferred single-glyph summary prefixing the note cell.
	// Absent when the issue had no conversation to judge (TDD 8.3) or when
	// judgment was unavailable.
	Emoji      string `yaml:"emoji,omitempty" json:"emoji,omitempty"`
	Unverified bool   `yaml:"unverified,omitempty" json:"unverified,omitempty"`

	// OperatorStale is the operator's manual Stale override, preserved on merge
	// across runs (TDD 8.7).
	OperatorStale bool `yaml:"operator_stale,omitempty" json:"operator_stale,omitempty"`

	ClosedAt *time.Time `yaml:"closed_at,omitempty" json:"closed_at,omitempty"`

	// LastActivity is the timestamp of the most recent event, used for the issue
	// age threshold in stale determination (TDD 8.5) and rendered as the Updated
	// column. Persisted for the same reason as on a PR: a closed issue is carried
	// forward without a re-crawl, so the stored value is the only activity date
	// such a row has.
	LastActivity time.Time `yaml:"last_activity,omitempty" json:"last_activity,omitempty"`

	// InputFingerprint is an opaque hash of every deterministic input to an open
	// issue's judgment: CommentCount and the most recent trail event. Mirrors
	// model.PR's field for the same reason (TDD 8.8) — it exists solely so a
	// later run can detect "nothing worth re-judging happened" without growing
	// the schema further as new inputs start mattering; only the hash's inputs
	// would change. Never set when Unverified, so a degraded judgment is never
	// mistaken for a cache.
	InputFingerprint string `yaml:"input_fingerprint,omitempty" json:"input_fingerprint,omitempty"`

	// Events is the chronological event trail. Not persisted.
	Events []Event `yaml:"-" json:"-"`

	// WasJudged is true when this run's classification actually reached the
	// provider for a fresh judgment, mirroring model.PR's field for the same
	// reason (TDD 8.8, 8.9, 9.1) — a run-local signal for the notification
	// hook's change collection, never persisted.
	WasJudged bool `yaml:"-" json:"-"`
}

// Key uniquely identifies an issue across the tracked set and the store. It
// carries an "issue" discriminator so an issue and a PR that happen to share a
// repo and number never collide in a keyed map.
func (i Issue) Key() string {
	return "issue:" + i.Repo + "#" + itoa(i.Number)
}
