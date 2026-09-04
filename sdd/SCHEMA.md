# Schema Reference

Field-level contracts for the two boundaries TECH.md points at: the YAML source of
truth, and the provider hand-off. TECH.md describes where these sit in the
architecture; this document defines their exact shapes.

## Enumerated vocabularies

All status-like values are drawn from closed sets, validated before use (TDD 6.6).
An LLM response outside these sets is invalid and drives the retry/unverified path.

### `bucket`

The top-level classification of a PR.

| Value | Meaning |
|-------|---------|
| `open` | Live PR; carries an `action` (see below). |
| `stale` | No further action expected (TDD 5). Open PRs only. |
| `merged` | Immutable ground fact from GitHub (TDD 4.1). |
| `closed` | Closed unmerged; carries a `close_reason` (TDD 4.2). |

### `action` (open PRs)

The finite set of actions a live PR may be waiting on.

| Value | Meaning |
|-------|---------|
| `awaiting_review` | Waiting on a reviewer to act. |
| `changes_requested` | Author must address review feedback. |
| `author_active` | Author is actively iterating. |
| `unassigned` | No reviewer assigned yet. |
| `blocked_external` | Waiting on something outside this PR (another PR, a decision). |
| `merge_ready` | Approved and mergeable; awaiting merge. |
| `conflicted` | The PR conflicts with its base branch and the author must resolve it. |
| `review_feedback` | Reviewers left line comments the author has not yet resolved. |

Unlike every other `action` — which the provider *infers* from the event trail —
`conflicted` and `review_feedback` are assigned **deterministically** from hard
GitHub facts, not from the trail, and only for `role: submitter` PRs (neither a
conflict nor unresolved feedback on a PR the operator merely reviews is theirs to
resolve). Neither is a value the provider may return; the classifier applies them
in code. `review_feedback` comes from the PR's review-thread resolution state —
the count of threads that are unresolved and not outdated — and matters because
an approval can coexist with still-open comments: the model reads that as
"conditionally approved", which wrongly implies the ball is elsewhere when it is
with the author. When both hold, `conflicted` wins, being the harder blocker.
Neither override applies to the `stale` bucket or the merged/closed floors.
See TDD 4.7 (conflicted) and TDD 4.9 (review feedback).

A **failing build is deliberately not in this enum.** It is an equally hard
GitHub fact, but `action` holds one value while CI state and review state are
independent: expressing a red build here would force it to displace whatever
review-derived disposition the PR also carries, so an unaddressable build failure
could conceal a reviewer's request the operator *could* act on. It is therefore
carried as the separate `ci_failing` flag on the `!pr` document, and both facts
render side by side (TDD 4.10–4.12).

`awaiting_review` may likewise be assigned **deterministically** — not only
inferred: for a `role: reviewer` PR that GitHub currently shows a pending review
request on the operator for (the "please review" banner, surfaced by the
review-requested search), the classifier sets `awaiting_review` in code,
overriding whatever the trail implied, and such a PR is never bucketed `stale`.
The provider may still return `awaiting_review` from the trail; the deterministic
path only forces it when the live request is present. See TDD 4.8.

### `close_reason` (closed-unmerged PRs)

| Value | Meaning |
|-------|---------|
| `superseded` | Replaced by another PR or plan. |
| `cancelled` | Contributor or stakeholder discarded it. |
| `stale` | Closed due to inactivity. |
| `invalid` | Opened in error. |

### `priority`

| Value | Meaning |
|-------|---------|
| `neutral` | No urgency signal in the event trail. |
| `elevated` | Event trail signals urgency (TDD 4.6). |

### `role`

| Value | Meaning |
|-------|---------|
| `submitter` | The operator authored the PR. |
| `reviewer` | The operator was requested to review it. |

---

## Issue vocabularies

Issues are a second tracked entity with their own closed sets. The sets are
deliberately separate from the PR ones rather than a widened union: an issue has
no merge, its close reason is a hard GitHub fact rather than an inference, and it
carries none of the review machinery, so an action that is meaningless for one
entity is not even parseable for it.

### `bucket` (issues)

| Value | Meaning |
|-------|---------|
| `open` | Live issue; carries an `action` (see below). |
| `stale` | No further action expected; set by operator override or the issue age threshold (TDD 8.5). |
| `closed` | Immutable ground fact from GitHub; carries a `close_reason` (TDD 8.2). |

There is no `merged` bucket — an issue cannot be merged.

### `action` (open issues)

| Value | Meaning |
|-------|---------|
| `awaiting_response` | Someone has asked the operator something; the ball is with us. |
| `awaiting_others` | The operator is waiting on someone else. |
| `triage` | No engagement yet. Also the deterministic disposition of an issue with no comments (TDD 8.3) and the conservative fallback when judgment is unavailable. |
| `resolved_pending_close` | The substance is settled; only the close remains. |

### `close_reason` (closed issues)

Taken **verbatim from GitHub's own `state_reason`** — a hard fact, never inferred
(TDD 8.2). This is the meaningful simplification over the PR path, where the
sub-reason is a model judgment.

| Value | Meaning |
|-------|---------|
| `completed` | Closed as done. |
| `not_planned` | Closed as won't-do. |
| `duplicate` | Closed as a duplicate of another issue. |
| `reopened` | GitHub reports the issue as having been reopened. |

A closed issue for which GitHub supplies no `state_reason` (older issues predate
the field) is recorded as `completed`: it was closed deliberately, and the schema
requires a reason on a closed row.

### `role` (issues)

| Value | Meaning |
|-------|---------|
| `author` | The operator opened the issue. |
| `participant` | The operator commented on it **or** was assigned it — an assignment is itself an ask, so it counts as engagement even with no comment (TDD 8.1). |

`priority` is shared with the PR vocabulary unchanged.

---

## YAML store

The source of truth is one YAML file containing a stream of documents, each tagged
with its entity type. Pull requests are tagged `!pr` and issues `!issue`; the tag
is the extension point that lets further entity types be added later without
restructuring existing data, and a document whose tag this build does not
recognize is round-tripped verbatim rather than dropped (TDD 2.5).

### `!pr` document

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repo` | `string` | yes | `owner/name` of the repository. |
| `number` | `int` | yes | PR number within the repo. |
| `title` | `string` | yes | PR title. |
| `url` | `string` | yes | Canonical PR URL. |
| `role` | `role` | yes | Whether the operator is submitter or reviewer. |
| `created` | `date` | yes | PR creation date (ISO-8601). |
| `bucket` | `bucket` | yes | Classification. |
| `action` | `action` | no | Present iff `bucket: open`. |
| `close_reason` | `close_reason` | no | Present iff `bucket: closed`. |
| `priority` | `priority` | yes | Inferred urgency. |
| `companion` | `string` | no | Inferred note, 3–14 words (config bounds). Absent when `unverified`. |
| `emoji` | `string` | no | Model-inferred single-glyph summary that prefixes the note cell when rendered. Absent when `unverified` or notes were skipped. |
| `unverified` | `bool` | no | `true` when model judgment could not be obtained (TDD 6.4/6.5); status then reflects raw flags and is not authoritative. |
| `ci_failing` | `bool` | no | `true` when an open submitter PR's head commit has one or more failing checks. A hard fact from GitHub's check-run conclusions, never inferred. It is a **flag rather than an `action` value** because CI state and review state are orthogonal and `action` is single-valued — routing a red build through `action` would force it to shadow the PR's review-derived disposition (TDD 4.10, 4.11). Never set for `role: reviewer` or on the merged/closed floors. |
| `operator_stale` | `bool` | no | Operator's manual Stale override; preserved across runs (TDD 2.2). |
| `last_activity` | `date-time` | no | Timestamp of the PR's most recent trail event. Persisted (unlike the trail it derives from) because a terminal PR is carried forward without a re-crawl, so the stored value is the only activity date such a row has; rendered as the Updated column on live tables. Absent on a record written before the field existed, in which case it is backfilled from `merged_at`/`closed_at` for a terminal PR and rendered as a dash otherwise. |
| `input_fingerprint` | `string` | no | Opaque hash of every deterministic input to an open PR's judgment (the most recent trail event, `ci_failing`, unresolved-thread count, mergeability, and pending-review-request state). Not itself meaningful — used only for equality comparison on the next run to detect that nothing worth re-judging happened, so the provider call is skipped and the row's `bucket`/`action`/`priority`/`companion`/`emoji` are carried forward verbatim (TDD 4.13). Never set when `unverified`, so a degraded judgment is never mistaken for a cache. Absent on a record written before the field existed or on a merged/closed/stale PR, for which the provider call is either skipped for other reasons or not this comparison's concern. |
| `merged_at` | `date` | no | Present iff `bucket: merged`. |
| `closed_at` | `date` | no | Present iff `bucket: closed`. |

Fields the tool derives on each run are overwritten; `operator_stale` (and any
future operator-set field) is preserved on merge.

**Example:**

```yaml
--- !pr
repo: valkey-io/valkey-glide-ruby
number: 100
title: "feat(ruby): structured search builder API"
url: https://github.com/valkey-io/valkey-glide-ruby/pull/100
role: submitter
created: 2026-08-21
bucket: open
action: changes_requested
priority: elevated
companion: mitigating QA findings
emoji: 🔧
```

### `!issue` document

An issue is its own tagged document in the same stream (TDD 8.7). A store written
by a build that predates issues preserves these documents rather than dropping
them, and vice versa (TDD 2.5).

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `repo` | `string` | yes | `owner/name` of the repository. |
| `number` | `int` | yes | Issue number within the repo. |
| `title` | `string` | yes | Issue title. |
| `url` | `string` | yes | Canonical issue URL. |
| `role` | `role` (issue) | yes | Whether the operator authored or participates. |
| `created` | `date` | yes | Issue creation date (ISO-8601). |
| `bucket` | `bucket` (issue) | yes | Classification. |
| `action` | `action` (issue) | no | Present iff `bucket: open`. |
| `close_reason` | `close_reason` (issue) | no | Present iff `bucket: closed`; GitHub's `state_reason`. |
| `priority` | `priority` | yes | Inferred urgency. |
| `companion` | `string` | no | Inferred note, 3–14 words (config bounds). Absent when the issue had no conversation to judge (TDD 8.3) or judgment was unavailable. |
| `emoji` | `string` | no | Model-inferred single-glyph summary prefixing the note cell. Absent whenever `companion` is. |
| `unverified` | `bool` | no | `true` when model judgment was attempted and could not be obtained. A note-less zero-comment issue is **not** unverified — nothing was attempted (TDD 8.3). |
| `operator_stale` | `bool` | no | Operator's manual Stale override; preserved across runs (TDD 8.7). |
| `last_activity` | `date-time` | no | Timestamp of the issue's most recent trail event; persisted and rendered as the Updated column, for the same reason as on a `!pr` document. Backfilled from `closed_at` for a closed issue when absent. |
| `closed_at` | `date` | no | Present iff `bucket: closed`. |

**Example:**

```yaml
--- !issue
repo: valkey-io/valkey-glide
number: 1234
title: "Ruby client: FT.SEARCH returns stale cursor"
url: https://github.com/valkey-io/valkey-glide/issues/1234
role: participant
created: 2026-07-14
bucket: open
action: awaiting_response
priority: neutral
companion: maintainer asked for repro steps
emoji: ❓
```

An issue with no conversation carries no note at all:

```yaml
--- !issue
repo: valkey-io/valkey-samples
number: 12
title: "Add a Ruby example for cluster mode"
url: https://github.com/valkey-io/valkey-samples/issues/12
role: author
created: 2026-08-02
bucket: open
action: triage
priority: neutral
```

---

## Provider hand-off

Classification hands the provider one item's event trail and expects one
structured response. Transport is the provider subprocess's stdin/stdout, or the
verdict tool for a session provider (TECH.md § Provider).

The hand-off is **generic over entity**. One request shape, one parser, and one
retry loop serve both pull requests and issues; what differs is the vocabulary,
which travels *in the request* rather than being fixed by the transport. That is
why `bucket`, `action`, and `close_reason` are plain strings in the response
below: membership is checked against the vocabulary this request supplied, and the
value is then parsed into the entity's own typed enum by the classifier
(`ParseAction` for a PR, `ParseIssueAction` for an issue). Keeping them strings at
the boundary is what lets each entity's enum stay a tight closed set instead of a
union of both.

### Request (to provider)

The provider is given the item's identifying context and its chronological event
trail. Deterministic flags are not sent separately — flag changes appear as events
in the trail, so the latest authoritative event governs (TDD 4.4).

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `entity` | `"pull_request" \| "issue"` | yes | What is being judged, so the prompt and the response vocabulary match the item's own rules. |
| `repo` | `string` | yes | `owner/name`. |
| `number` | `int` | yes | PR or issue number. |
| `role` | `string` | yes | Operator's relationship to the item. Entity-specific: `submitter`/`reviewer` for a PR, `author`/`participant` for an issue. Valid values are named in `constraints.roles`. |
| `github_state` | `string` | yes | Raw upstream state, so the model honors the immutable floors. `open`/`closed`/`merged` for a PR (draft is not a state here — it is an open-PR sub-attribute that flows through the trail as ready/draft transitions, TDD 4.4); `open`/`closed` for an issue, which has no merged state. |
| `events` | `event[]` | yes | Chronological event trail. |
| `constraints` | `object` | yes | The word bounds and the valid enum vocabularies **this entity's** response must satisfy. |

### `constraints`

| Field | Type | Description |
|-------|------|-------------|
| `companion_words_min` / `companion_words_max` | `int` | Word bounds on the inferred note. |
| `buckets` | `string[]` | Legal `bucket` values for this entity. |
| `actions` | `string[]` | Legal `action` values for this entity. Excludes any value the classifier assigns deterministically (for a PR: `conflicted`, `review_feedback`). |
| `close_reasons` | `string[]` | Legal `close_reason` values. **Empty for an issue**, whose reason is GitHub's `state_reason` and is never inferred (TDD 8.2) — so a response proposing `bucket: closed` for an issue is rejected. |
| `priorities` | `string[]` | Legal `priority` values. |
| `roles` | `string[]` | Legal `role` values for this entity, so the model can read the `role` field. |

An empty vocabulary admits nothing: a request that omits a set fails validation
loudly rather than silently accepting any value.

### `event`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `timestamp` | `date-time` | yes | ISO-8601 event time. |
| `author` | `string` | yes | Actor login. |
| `role_hint` | `string` | no | Indication of whether the author is a stakeholder. |
| `kind` | `string` | yes | Event type: comment, review (with decision), state transition, label, ci_status (a head-commit CI/check-run summary, open PRs only), review_threads (a summary of unresolved review threads, open PRs only), review_decision (GitHub's overall review verdict, open PRs only). |
| `text` | `string` | no | Body/summary for comment and review events. |

### Response (from provider)

A single fenced JSON object. Validated against the request's supplied
vocabularies and word bounds before use (TDD 6.6); an invalid response triggers
the self-correcting retry (TDD 6.4).

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `bucket` | `string` | yes | Proposed classification; must be a member of `constraints.buckets` and may not contradict the `github_state` floors. |
| `action` | `string` | no | Required iff `bucket: open`; must be a member of `constraints.actions`. A PR's `conflicted` and `review_feedback` are never returned by the model — the classifier assigns them deterministically (see the `action` enum note above). |
| `close_reason` | `string` | no | Required iff `bucket: closed`; must be a member of `constraints.close_reasons`. An issue offers no close-reason vocabulary at all, so an issue response must not carry this field (TDD 8.2). |
| `priority` | `priority` | yes | Inferred urgency. |
| `companion` | `string` | yes | 3–14 words (config bounds). |
| `emoji` | `string` | yes | Exactly one emoji glyph summarizing the note; prefixes the rendered note cell. Not a closed enum — validated loosely (must be a single emoji character), and an invalid value drives the same retry/unverified path. |

**Example response (pull request):**

```json
{
  "bucket": "open",
  "action": "blocked_external",
  "priority": "neutral",
  "companion": "waiting on John Doe review",
  "emoji": "⏳"
}
```

**Example response (issue):**

```json
{
  "bucket": "open",
  "action": "awaiting_response",
  "priority": "neutral",
  "companion": "maintainer asked for repro steps",
  "emoji": "❓"
}
```

### Verdict tool (session provider transport)

The Response above is the contract regardless of provider strategy. A one-shot
provider returns it as a fenced JSON object on stdout. A **session** provider
receives the identical structure through a single Model Context Protocol tool the
harness calls — the verdict sink — rather than from terminal output. The tool call
*is* the completion signal.

**Input delivery (session provider).** The Request JSON is not typed into the
session (a large event trail exceeds the terminal command-length limit and would
be truncated). It is written to a temporary file; the harness is sent only a short
instruction to read that path, reads it with a read tool scoped to the prompt
directory, and the file is deleted once the verdict is collected.

**Tool:** `submit_verdict` (name is config-driven; this is the default).

**Transport:** streamable-HTTP MCP on a loopback address the provider hosts. The
harness's provisioned agent profile enables exactly two tools — this verdict sink
and a path-scoped read tool — and points the sink at its URL. The sink handles
`initialize` (returns `protocolVersion` and sets the
`Mcp-Session-Id` header), `tools/list` (advertises this one tool), `tools/call`
(receives the verdict), `ping`, and the `notifications/initialized` lifecycle
notification.

**`submit_verdict` arguments** — the tool's `arguments` object is the verdict; its
fields are exactly the Response fields above:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `bucket` | `string` | yes | Proposed classification; must be a member of the request's `constraints.buckets` and may not contradict the `github_state` floors. |
| `action` | `string` | no | Required iff `bucket: open`; a member of `constraints.actions`. A PR's `conflicted` and `review_feedback` are assigned deterministically by the classifier, never returned here. |
| `close_reason` | `string` | no | Required iff `bucket: closed` and the request offers a close-reason vocabulary. An issue offers none (TDD 8.2). |
| `priority` | `priority` | yes | Inferred urgency. |
| `companion` | `string` | yes | 3–14 words (config bounds). |
| `emoji` | `string` | yes | Exactly one emoji glyph summarizing the note; prefixes the rendered note cell. Not a closed enum — validated loosely (must be a single emoji character), and an invalid value drives the same retry/unverified path. |

The tool's advertised JSON schema carries no `enum` for `bucket`, `action`, or
`close_reason`, precisely because those sets are entity-specific and arrive per
request in the prompt file's `constraints`. The sink forwards the arguments
verbatim to the same parser and validator the one-shot path uses, so the
closed-vocabulary and word-bound checks (TDD 6.6) and the self-correcting retry
(TDD 6.4) apply identically. The sink is inbound only — it never issues a GitHub
call.

**Example `tools/call` params:**

```json
{
  "name": "submit_verdict",
  "arguments": {
    "bucket": "open",
    "action": "changes_requested",
    "priority": "elevated",
    "companion": "address review feedback",
    "emoji": "🔧"
  }
}
```

---

## Notification hook

After classification, the tool identifies which open PRs and issues actually
required a provider judgment this run (TDD 4.13/8.8 — a fresh judgment happens
for a first-seen item, one whose deterministic inputs changed, or one whose
prior record was unverified; an item carried forward unchanged is deliberately
excluded). When that set is non-empty, the tool asks the provider for one short
summary sentence via a distinct, simpler contract (`Summarize`, TDD 6.9 — not
the classification `Request`/`Response` shape above), then writes a single JSON
object to the stdin of the configured hook command (`GSB_NOTIFY_HOOK`) and waits
up to `GSB_NOTIFY_TIMEOUT` for it to exit.

The hook exists to prompt the operator to look at the status document, not to
replace it — the payload carries only what changed and one human-readable line,
not a full diff or the item's history.

**Scope**: only open PRs and open issues can appear in `changed`. A merged,
closed, or stale item is a settled fact the operator is not expected to act on
further, so it never triggers the hook even though its floor-note call also
touches the provider — that call is not a judgment about whether anything
*changed* (TDD 4.13's fingerprint concept has no floor/stale equivalent), and a
settled item newly relaying its own settledness on every run is not news
(TDD 9.2).

**Firing condition**: the hook is invoked only when `changed` is non-empty. A
run where every item was carried forward unchanged produces no notification —
silence is the expected common case at a 20-minute cadence, not an error
(TDD 9.1).

**Failure handling**: the hook is best-effort. A non-zero exit, a timeout, or a
failure to start is logged and does not fail the run — the status document has
already been written by this point, and a broken notification integration must
never be the thing that breaks the dashboard (TDD 9.5).

### Payload (to the hook's stdin)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `summary` | `string` | yes | One short, model-generated sentence summarizing everything in `changed`, sized for a desktop notification toast rather than the document — a distinct provider call from per-item classification, since no single item's companion note is written with "synthesize across N items" in mind. |
| `changed` | `change[]` | yes | Every open PR/issue that required a fresh judgment this run, in the order classification produced them. Never empty — the hook is not invoked otherwise. |

### `change`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `entity` | `"pull_request" \| "issue"` | yes | Matches the provider request's own `entity` (SCHEMA § Request), so a consumer can key off the same value used elsewhere. |
| `repo` | `string` | yes | `owner/name`. |
| `number` | `int` | yes | PR or issue number. |
| `url` | `string` | yes | Canonical GitHub URL, so a hook can build a clickable notification. |
| `role` | `string` | yes | Operator's relationship to the item (entity-specific vocabulary, SCHEMA § Request `role`). |
| `bucket` | `string` | yes | The item's freshly judged bucket. |
| `action` | `string` | no | Present iff `bucket: open`. |
| `companion` | `string` | no | The freshly inferred note. Absent if this run's judgment came back unverified — an unverified item still counts as "changed" (its inputs moved, prompting the judgment attempt) even though the judgment itself did not produce a usable note. |
| `priority` | `string` | yes | The item's freshly judged priority. |

Only the item's **new** state is carried; there is no `previous_*` field. The
hook's purpose is to say *this needs a look*, not to reconstruct history — the
status document remains the record of what changed and why.

**Example payload:**

```json
{
  "summary": "3 PRs need review, valkey-glide-ruby#295 flagged elevated",
  "changed": [
    {
      "entity": "pull_request",
      "repo": "valkey-io/valkey-glide-ruby",
      "number": 295,
      "url": "https://github.com/valkey-io/valkey-glide-ruby/pull/295",
      "role": "submitter",
      "bucket": "open",
      "action": "review_feedback",
      "companion": "reviewer left unresolved comments",
      "priority": "elevated"
    },
    {
      "entity": "issue",
      "repo": "valkey-io/valkey-glide-ruby",
      "number": 234,
      "url": "https://github.com/valkey-io/valkey-glide-ruby/issues/234",
      "role": "participant",
      "bucket": "open",
      "action": "awaiting_others",
      "companion": "maintainer asked a follow-up question",
      "priority": "neutral"
    }
  ]
}
```

### Summarize contract (provider)

Distinct from classification's `Request`/`Response` (TDD 6.9): a plain prompt
string in, a plain string out, no vocabulary to validate against, no
self-correcting retry loop. A failed attempt falls back to a plain non-model
string rather than retrying — a wrong or missing toast sentence is a
notification inconvenience, not a misjudged PR status.

For the session provider, the harness's classification and summarization tools
are mutually exclusive **per turn** (TDD 6.10): the sink advertises exactly one
of `submit_verdict` or `submit_summary` in `tools/list` depending on which kind
of request is in flight, so the model is never offered two callable tools at
once and left to guess which one this turn wants. The agent profile trusts both
tool names from session construction (a static pre-approval list), so toggling
which one is advertised never triggers an interactive trust prompt mid-session.

**`submit_summary` tool schema:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `summary` | `string` | yes | One short sentence, the same value that lands in the payload's top-level `summary` field. |
