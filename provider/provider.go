// Package provider owns the LLM provider interface and its subprocess
// invocation, timeout, response parsing, self-correcting retry loop, and the
// unverified fallback. The provider is pluggable and never core (POLICY): all
// model access goes through the Provider interface, invoked as a bounded
// subprocess. Kiro CLI is one implementation.
package provider

import (
	"context"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// Entity names the kind of GitHub item a request judges. The provider hand-off
// is generic over entity: the response vocabulary is not fixed by the transport
// but supplied per request in Constraints, because a pull request and an issue
// have genuinely different action sets (SCHEMA § Request). It is a closed set,
// parsed once here.
type Entity string

const (
	EntityPR    Entity = "pull_request"
	EntityIssue Entity = "issue"
)

// Valid reports whether e is a member of the closed set.
func (e Entity) Valid() bool { return e == EntityPR || e == EntityIssue }

// Request is the structured input handed to the provider for one entity
// (SCHEMA § Request). Deterministic flags are not sent separately — flag
// changes appear as events in the trail so the latest authoritative event
// governs (TDD 4.4).
type Request struct {
	// Entity names what is being judged, so the prompt and the response
	// vocabulary match the item's own rules.
	Entity Entity `json:"entity"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Role is the operator's relationship to the item, carried as a plain string
	// because the role vocabulary is entity-specific (submitter/reviewer for a
	// PR, author/participant for an issue). The valid values are named in
	// Constraints.
	Role string `json:"role"`
	// State is the raw upstream state, so the model honors the immutable floors.
	// Carried as a plain string for the same reason as Role: an issue has no
	// merged state.
	State       string        `json:"github_state"`
	Events      []model.Event `json:"events"`
	Constraints Constraints   `json:"constraints"`
}

// Constraints carries the word bounds and the valid enum vocabularies the
// response must satisfy for this request's entity (SCHEMA § Request
// constraints). The vocabularies are supplied rather than assumed so one
// provider path serves both entities without either one's enum being widened to
// accommodate the other.
type Constraints struct {
	CompanionWordsMin int      `json:"companion_words_min"`
	CompanionWordsMax int      `json:"companion_words_max"`
	Buckets           []string `json:"buckets"`
	Actions           []string `json:"actions"`
	CloseReasons      []string `json:"close_reasons"`
	Priorities        []string `json:"priorities"`
	Roles             []string `json:"roles"`
}

// Response is the strict structure the provider returns for one entity
// (SCHEMA § Response). Validated against the request's supplied vocabularies
// and word bounds before use (TDD 6.6); an invalid response drives the
// self-correcting retry (TDD 6.4).
//
// Bucket, Action, and CloseReason are plain strings here, not typed enums: the
// legal vocabulary depends on the entity, so membership is checked against the
// request's Constraints at this boundary and the value is parsed into the
// entity's own typed enum by the caller (model.ParseAction for a PR,
// model.ParseIssueAction for an issue). That keeps each entity's enum a tight
// closed set instead of a union of both.
type Response struct {
	Bucket      string         `json:"bucket"`
	Action      string         `json:"action,omitempty"`
	CloseReason string         `json:"close_reason,omitempty"`
	Priority    model.Priority `json:"priority"`
	Companion   string         `json:"companion"`
	// Emoji is a single glyph the model infers to visually summarize the item's
	// note; it prefixes the note cell in the rendered document. Not a closed
	// enum — validated loosely as exactly one emoji grapheme (SCHEMA § Response).
	Emoji string `json:"emoji"`
}

// Provider performs event-trail judgment for a single PR. Implementations are
// invoked as bounded subprocesses (TDD 6.1) and must not couple core logic to
// any specific model (TDD 6.2). The raw string return is the provider's
// unparsed stdout; parsing and validation are the caller's responsibility so
// the retry loop can quote bad output back (TDD 6.4).
type Provider interface {
	// Name identifies the provider for logging.
	Name() string

	// Invoke runs the provider for one request and returns its raw textual
	// response. The context bounds the call with a timeout (TDD 6.5). A
	// correction, when non-empty, is appended to steer a retry with the prior
	// bad output and the required structure quoted.
	Invoke(ctx context.Context, req Request, correction string) (string, error)
}
