# Test-Driven Development Rubrics

Behavioral contracts for GithubSlashboard. Each rubric describes observable
system behavior in Given/When/Then form, independent of implementation.

Read the table of contents first and load only the sections relevant to the
change in front of you.

| # | Functional area | Rubrics |
|---|-----------------|---------|
| 1 | GitHub data acquisition | 1.1 – 1.6 |
| 2 | YAML store (source of truth) | 2.1 – 2.5 |
| 3 | Markdown rendering (pure sink) | 3.1 – 3.5 |
| 4 | Status classification | 4.1 – 4.14, 4.7a |
| 5 | Stale determination | 5.1 – 5.4 |
| 6 | Provider / LLM hand-off | 6.1 – 6.18 |
| 7 | Execution & portability | 7.1 – 7.2 |
| 8 | Issue tracking | 8.1 – 8.9 |
| 9 | Notification hook | 9.1 – 9.6 |

## Configurable constants referenced below

These are configuration values, not hardcoded literals (see POLICY.md). Rubrics
reference them by name.

- **`companion_words_min` / `companion_words_max`** — bounds on inferred companion
  prose. Default 3 and 14.
- **`llm_retry_cap`** — max self-correcting LLM *re-invocations* (retries) after the
  first attempt before the row is marked unverified. Default 2 — i.e. 3 total
  attempts: one initial call plus two retries.
- **`stale_age_threshold`** — duration of no activity after which a PR is Stale.
- **`issue_stale_age_threshold`** — duration of no activity after which an *issue*
  is Stale. Distinct from the PR threshold because issues legitimately sit idle far
  longer without being abandoned. Default 120 days.
- **`provider`** — the LLM provider invocation (MVP: Kiro CLI).

---

## 1. GitHub data acquisition

### 1.1 Fetch the tracked PR set
- **Given** valid GitHub authentication
- **When** the tool fetches
- **Then** it retrieves every PR the operator authored and every PR the operator
  was requested to review (past or present), scoped as the operator's PR tracking
  is scoped today

### 1.2 Acquisition failure is non-destructive
- **Given** the GitHub API is unreachable or returns an error
- **When** the tool attempts to fetch
- **Then** it aborts without modifying the YAML store or the Markdown output, and
  reports the error

### 1.3 Read-only guarantee
- **Given** any operation the tool performs
- **When** it runs
- **Then** it never issues a state-mutating GitHub call — GithubSlashboard only
  ever reads GitHub

### 1.4 Per-PR event trail is assembled
- **Given** a PR in the tracked set
- **When** its data is acquired
- **Then** a single chronological, timestamped event trail is built for it,
  including issue comments, review submissions (approve / request-changes /
  comment), state transitions (opened, ready/draft, closed, merged), label
  changes, and — for an open PR — a summary of the current CI/check-run outcome
  on its head commit (passing / failing with the failing check names / pending),
  each entry carrying its author and a role indication
- **Note** the CI summary is emitted as the latest authoritative build signal so
  a stale comment mentioning an earlier, since-fixed failure no longer governs
  the inferred status

### 1.5 Terminal PRs already in the store are not re-crawled by default
- **Given** a PR the store already records as merged or closed
- **When** the tool acquires the tracked set with terminal inclusion off (the
  default)
- **Then** that PR is not crawled — no per-PR fetch, comments, reviews, or
  timeline calls are made for it — and its cached record (bucket, close reason,
  priority, companion, emoji) is carried forward unchanged, because a terminal
  PR will not change and its verdict is already cached
- **And** when terminal inclusion is switched on (a deliberate deep run) every
  discovered PR is crawled afresh regardless of its stored state

### 1.6 An open PR's trail carries its unresolved review threads and review decision
- **Given** an open PR whose reviewers have left line comments
- **When** the tool assembles its event trail
- **Then** the trail carries a summary of the PR's **unresolved** review threads
  (those neither resolved nor outdated), naming who raised them, timestamped at
  the newest such thread so it sorts as a current signal
- **And** the trail carries GitHub's overall review decision (approved, changes
  requested, or review still required) as an upstream verdict
- **Note** resolution state is available only from GitHub's GraphQL API, so this
  is the only signal that distinguishes "approved and done" from "approved with
  comments still open"; a fully resolved PR contributes no thread event, and
  terminal PRs are not queried

---

## 2. YAML store (source of truth)

### 2.1 One file, per-PR tagged documents
- **Given** acquired PR data
- **When** persisted
- **Then** it is written to a single YAML file as one document per PR, each tagged
  `!pr`

### 2.2 Operator-set state survives re-runs
- **Given** a PR carrying operator-set state (e.g. an operator Stale override)
- **When** the tool re-runs and re-fetches
- **Then** that operator-set state is preserved and not overwritten by fetched data

### 2.3 Idempotent updates
- **Given** an existing YAML store
- **When** the tool runs and upstream data is unchanged
- **Then** the store is unchanged apart from the run timestamp — no spurious churn

### 2.4 Malformed store is not corrupted
- **Given** a malformed or partially written YAML file
- **When** the tool reads it
- **Then** the tool refuses to proceed rather than silently repairing or
  overwriting it

### 2.5 Scope expansion without restructuring
- **Given** the store, holding `!pr` and `!issue` documents
- **When** a further entity type is introduced (e.g. a `!discussion` document), or
  a store written by a newer build carries a tag this build does not recognize
- **Then** it coexists with the recognized documents and is round-tripped verbatim
  rather than dropped, and the pipeline handles the addition without restructuring
  existing data

---

## 3. Markdown rendering (pure sink)

### 3.1 Reproduce the established structure
- **Given** a populated YAML store
- **When** the Markdown is rendered
- **Then** it reproduces the established document structure, with no embedded
  agent instructions:
  - a header block (including a notice that the document is generated and manual
    edits are overwritten) and a **role-based summary table** — one row per PR
    role (Submitter, Reviewer) with counts per bucket under 🟢 Open / 🟡 Stale /
    ✅ Merged / 🔴 Closed and a Total, followed by the issue rows described in 8.6
  - a **Submitter section** (`# Submitter: PRs I Authored`) split into four
    mutually-exclusive tables — `💡 Open` (Repo | PR | Title | Created | Age |
    Updated | Action Needed), `☠ Stale` (… | Reason), `📦 Merged` (… | Created |
    Merged | Notes), `⛔ Closed` (… | Created | Closed | Reason)
  - a **Reviewer section** (`# Reviewer: PRs I Reviewed`), preceded by a
    horizontal rule separating it from the Submitter section, split by
    ball-holding —
    `Awaiting Our Action` and `💡 Open — Review Submitted` (Repo | PR | Title |
    Updated | Our Review), and `✅ Done - Closed / Merged` (Repo | PR | Title |
    Outcome | Notes)
  - an **Updated column** on every table whose rows are still live (submitter
    Open and Stale, both reviewer open tables, and the issue active and stale
    tables), reporting whole days since the item's last trail activity in the
    same form as Age (e.g. `13d`). The terminal tables omit it: their Merged or
    Closed date already says when the item last mattered. An item with no
    recorded activity date renders `—` rather than an age computed from a zero
    timestamp
  - **note cells** (Action Needed, Reason, Notes, Our Review) are prefixed with
    the PR's model-inferred emoji when one is present; where a cell would also
    carry a structural marker (the ⏳/🔄 on submitter Action Needed, the ⏳ on
    reviewer Awaiting Our Action), the inferred emoji takes precedence and the
    structural marker is omitted rather than doubling the glyph — this applies
    to **every** marker-bearing cell, so no row ever renders two leading glyphs
    (e.g. never `⏳ ⏳ …`); the structural marker appears only when no emoji was
    inferred
  - **cell markers**: ⏳ for an action pending on someone other than us, 🔄 for
    active back-and-forth, 🚧 for a submitter PR whose build is failing, and the
    Done outcome as 🎉 MERGED or 🚪 CLOSED — a
    closed outcome carrying its close sub-reason when known, formatted
    `🚪 CLOSED / <emoji> <sub-reason>` with a deterministic per-enum emoji (not
    LLM-inferred), so a superseded reviewer PR is distinguishable from a plainly
    closed one
  - the 🚧 CI mark is the one marker that is **additive rather than exclusive**:
    it reports a fact orthogonal to the action, so it leads the cell *alongside*
    the action's own mark or inferred emoji instead of replacing it (TDD 4.11).
    It is suppressed only when the inferred emoji is already the same glyph, so no
    row renders it twice

### 3.2 Summary agrees with detail
- **Given** the store
- **When** rendered
- **Then** every count in the summary table equals the actual number of detail rows
  in the corresponding bucket

### 3.3 Rendering is a pure function of the store
- **Given** an unchanged store and a fixed run-time reference (the Last Updated
  value and the reference time used to compute PR ages)
- **When** the Markdown is regenerated
- **Then** the output is byte-identical except for the Last Updated value — the
  Markdown is never itself a source of truth

### 3.4 Last Updated is verbatim
- **Given** a Last Updated value supplied at run time
- **When** rendered
- **Then** it appears verbatim in the header

### 3.5 Elevated priority is visible
- **Given** a PR whose priority is elevated
- **When** rendered
- **Then** its row is visibly distinguished from neutral-priority rows

### 3.6 Terminal rows sort newest-first by completion date
- **Given** a bucket of settled items — submitter Merged, submitter Closed,
  reviewer Done (which mixes merged and closed), or the issue Closed section
- **When** rendered
- **Then** rows are ordered by the date the item became terminal (`MergedAt` for
  a merged PR, `ClosedAt` for a closed PR or a closed issue) from most recent to
  least recent, not by repo/number
- **And** elevated priority still sorts before everything else within the
  bucket, matching every other table (3.5) — the date order applies among rows
  of the same priority
- **And** rows sharing an identical terminal date, including two records that
  both lack one (a pre-migration store), fall back to the repo/number order
  used by the live tables, so the sort stays total and deterministic
- **Note** every other table (Open, Stale, the reviewer ball-holding split, and
  the issue active/stale sections) keeps the existing repo-then-number order:
  those items have no completion date to sort by, and stability there was never
  in question

---

## 4. Status classification

### 4.1 Merged is immutable and supersedes all
- **Given** GitHub reports a PR as merged
- **When** classified
- **Then** it is Merged, and no timeline event or LLM inference can reclassify it —
  merged supersedes closed and every other state

### 4.2 Closed-unmerged is immutable-closed
- **Given** GitHub reports a PR as closed and not merged
- **When** classified
- **Then** it is Closed; the LLM may only select the close sub-reason from the
  event trail and cannot reclassify it as open, actionable, or stale-bucket

### 4.3 Close sub-reason is a finite enum
- **Given** a closed-unmerged PR
- **When** its sub-reason is assigned
- **Then** it is exactly one of: Superseded | Cancelled | Stale | Invalid
- **Note** the sub-reason is *inferred* by the provider only when `classify_floor_notes`
  is enabled; in the default fast mode the provider is skipped for immutable-floor
  PRs and the sub-reason takes a deterministic default (the bucket is a hard fact
  regardless)

### 4.4 Open-PR disposition comes from the event trail
- **Given** an open PR
- **When** classified
- **Then** its Action Needed value is chosen from a finite enum by reading the
  event trail, and a later authoritative event supersedes an earlier uncleared flag
  (e.g. a "request changes" flag followed by a stakeholder comment standing it down)

### 4.5 Every status cell pairs enum with bounded companion
- **Given** a classified PR
- **When** rendered
- **Then** its status cell carries the deterministic enum value plus an inferred
  companion string of `companion_words_min`–`companion_words_max` words, prefixed
  by a single model-inferred emoji summarizing the note, or an explicit
  unavailable marker when no companion could be produced
- **Note** the emoji is inferred by the provider alongside the companion and
  validated as exactly one emoji glyph (an invalid emoji drives the same
  retry/unverified path as any other bad field); in the default fast mode the
  provider is skipped for immutable-floor PRs (merged, closed), so those rows
  carry neither companion nor emoji (the unavailable marker), unless
  `classify_floor_notes` is enabled

### 4.6 Priority is an inferred enum
- **Given** a PR's event trail
- **When** classified
- **Then** the tool assigns a priority of neutral or elevated, inferred from urgency
  signals in the trail

### 4.7 A submitter's conflicted PR is flagged deterministically
- **Given** an open PR the operator authored that GitHub reports as conflicting
  with its base branch
- **When** classified
- **Then** its Action Needed value is Conflicted — assigned deterministically from
  GitHub's mergeability, not inferred from the event trail — and this overrides
  whatever open-PR action the model inferred, because the author must resolve the
  conflict; but it never overrides the Stale bucket (a stale PR's conflict is moot)
  nor the immutable merged/closed floors, and a conflicted PR the operator only
  reviews is not flagged (the conflict is not theirs to resolve)
- **Note** because the conflict is a hard fact rather than a judgment, it surfaces
  even on a row that fell to `unverified` (TDD 6.4); the row stays unverified since
  its disposition was not model-judged

### 4.7a A submitter's merge-blocked PR is flagged deterministically
- **Given** an open PR the operator authored that GitHub reports as
  `mergeable_state: blocked` — a branch-protection requirement is unmet (a
  required review, a required status check, a required commit signature, or
  any other rule the tool cannot see the specifics of) — and the PR is not
  already conflicted (TDD 4.7)
- **When** classified
- **Then** its Action Needed value is Merge Blocked — assigned deterministically
  from GitHub's own mergeable-state computation, not inferred from the event
  trail — and this overrides whatever open-PR action the model inferred, including
  a model-inferred `merge_ready`, because GitHub itself is the authority on
  whether a PR can actually merge
- **And** the tool does not attempt to name *why* it is blocked: GitHub does not
  return that detail in the same call, and the branch-protection endpoint that
  would (ISSUES § C) is frequently unavailable to a read-only token on a
  repository the operator does not administer — the row states the fact
  ("blocked from merging") without guessing a cause
- **And** this is distinct from Conflicted (TDD 4.7): `dirty` (a textual
  conflict) and `blocked` (an unmet branch-protection rule) are mutually
  exclusive states GitHub reports, so a PR is flagged as one or the other, never
  both — Conflicted takes precedence when GitHub reports the PR as both
  unmergeable and dirty, since a conflict must be resolved before a
  protection-rule block can even be evaluated
- **And** it never overrides the Stale bucket nor the immutable merged/closed
  floors, and a merge-blocked PR the operator only reviews is not flagged (the
  block is not theirs to clear)
- **Note** because the block is a hard fact rather than a judgment, it surfaces
  even on a row that fell to `unverified`; a model-inferred `merge_ready` sitting
  on a row GitHub itself reports as blocked was the concrete defect motivating
  this rubric (found live, not by any test — see ANTI-PATTERNS #9)

### 4.8 A reviewer PR with a pending review request awaits our action
- **Given** an open PR the operator was asked to review that GitHub currently
  shows a pending review request on the operator for (the "please review" banner)
- **When** classified
- **Then** its Action Needed value is Awaiting Review — assigned deterministically
  from that live GitHub request, not inferred from the event trail — so the PR is
  placed in the reviewer "Awaiting Our Action" table; this overrides whatever
  open-PR action the model read from the trail (e.g. an earlier changes_requested
  the author has since addressed), and such a PR is never bucketed Stale regardless
  of age, since GitHub is actively asking us to act
- **Note** the request is a hard fact, so it surfaces even on a row that fell to
  `unverified`; a reviewer PR with no pending request keeps the model's inferred
  action (the ball may rightly be on the author)

### 4.9 A submitter PR with unresolved review threads needs the author to act
- **Given** an open PR the operator authored that carries one or more review
  threads which are neither resolved nor outdated
- **When** classified
- **Then** its Action Needed value is Review Feedback — assigned deterministically
  from GitHub's review-thread resolution state, not inferred from the event trail
  — and the rendered cell states the outstanding feedback and reads as the author
  owing the next move, never as pending on somebody else
- **And** this overrides whatever open-PR action the model read from the trail,
  because a reviewer's approval can coexist with still-open comments, which the
  model reports as "conditionally approved" and which would otherwise give the
  false impression that the ball is in someone else's court
- **Note** the model's inferred note and emoji are preserved — only the action is
  overridden, so any nuance the model found still reaches the reader; the override
  is submitter-scoped (unresolved comments on a PR the operator merely reviews are
  the other author's to answer), surfaces even on a row that fell to `unverified`,
  loses to a merge conflict when both hold (the harder blocker), and does not
  override the Stale bucket or the merged/closed floors

### 4.10 A submitter's failing CI is flagged deterministically
- **Given** an open PR the operator authored whose head commit has one or more
  failing checks
- **When** classified
- **Then** it is flagged as having failing CI, assigned deterministically from
  GitHub's check-run conclusions rather than inferred from the event trail
- **And** the flag is recorded independently of the Action Needed value, so it
  never replaces or suppresses the PR's review-derived disposition
- **And** the flag is not applied to a PR the operator merely reviews — another
  author's broken build is not the operator's to fix
- **And** a merged, closed, or stale PR is never flagged: each means nothing
  further is expected of the PR, so its check state is moot and flagging work as
  owed would contradict the bucket
- **Note** CI state and review state are *orthogonal* facts, which is why this is
  a flag rather than an Action Needed value. The action field is single-valued, so
  expressing a red build through it would force one fact to shadow the other (4.11)

### 4.11 Failing CI and a pending review request coexist without shadowing
- **Given** an open submitter PR that has both failing CI and some other
  outstanding action — unresolved review threads, or any disposition the trail
  implies
- **When** classified and rendered
- **Then** both facts appear on the row: the CI flag alongside the Action Needed
  value, each expressed by its own mark
- **And** neither fact is expressed by discarding the other, so a build failure the
  operator cannot address immediately (a flaky runner, an upstream breakage, a
  dependency not yet merged) can never conceal a reviewer's request the operator
  *could* act on now
- **And** the tool does not attempt to judge whether a failure is addressable —
  that would substitute inference for a fact; any such nuance a stakeholder stated
  in the thread reaches the reader through the inferred note instead

### 4.12 Failing CI puts the ball with the operator
- **Given** an open submitter PR flagged with failing CI
- **When** the document is rendered
- **Then** its row is visibly marked as owing the operator work whatever its Action
  Needed value, because a broken build on a PR they authored is theirs to fix
- **And** the mark is driven by the flag alone, requiring no particular Action
  Needed value, and appears in addition to (never instead of) the action's own mark
- **Note** the submitter section is bucketed by status, not split by who owes the
  next move, so this is expressed as a mark on the row rather than by moving the
  row to a different table. The flag is submitter-scoped, so it never affects the
  reviewer section's ball-holding split

### 4.13 An unchanged open PR skips the provider call
- **Given** an open PR whose deterministic inputs — its most recent trail event,
  CI failing state, unresolved-review-thread count, and mergeability — are
  identical to what they were when the stored record was last classified, and
  that stored record was not itself unverified
- **When** the tool classifies the PR on a subsequent run
- **Then** it never calls the provider for this PR: the stored bucket, action,
  priority, companion, and emoji are carried forward verbatim
- **And** the GitHub fetch (including check-runs, review threads, and
  mergeability) still runs in full beforehand — only the provider call is
  skipped, never the fact-gathering that would detect a change
- **Note** this exists because the provider call is the expensive step (real
  token cost against a real budget), while the deterministic fetch is what
  proves nothing worth re-judging occurred; skipping the fetch instead would
  make the "unchanged" determination itself untrustworthy (see the CI check-run
  timing evidence behind 4.10 — a check-run cycle can complete without moving a
  PR's own last-modified signal, so the comparison must be built from freshly
  fetched deterministic facts, not a lighter proxy for them)

### 4.14 A first-seen, changed, or previously-unverified PR always reaches the provider
- **Given** an open PR that either has no prior stored record, or has a prior
  record whose deterministic inputs (4.13) differ from the freshly fetched
  ones, or has a prior record marked unverified
- **When** the tool classifies the PR
- **Then** the provider is called as normal — the skip in 4.13 never applies, so
  a real change is never mistaken for a repeat, and a previously degraded
  judgment is never cached forward as if it were settled

---

## 5. Stale determination

### 5.1 Operator override
- **Given** an operator Stale override on a PR
- **When** classified
- **Then** the PR is Stale regardless of activity or inference

### 5.2 Age threshold
- **Given** a PR with no activity since `stale_age_threshold`
- **When** classified
- **Then** the PR is Stale

### 5.3 Inferred tombstone
- **Given** the event trail carries clear evidence that no further action will be
  taken (e.g. a stakeholder "leave it open as a tombstone" comment)
- **When** classified
- **Then** the PR may be marked Stale on the LLM's inference

### 5.4 Stale applies to open PRs only
- **Given** a merged or closed-unmerged PR
- **When** classified
- **Then** it is never placed in the Stale bucket — those states own their own
  buckets (Merged; Closed with sub-reason)

---

## 6. Provider / LLM hand-off

### 6.1 MVP provider is the Kiro CLI
- **Given** the MVP configuration
- **When** an inference is requested
- **Then** it is performed by invoking the Kiro CLI as a bounded subprocess, not an
  HTTP API

### 6.2 Custom providers via one interface
- **Given** a custom provider configured (e.g. a Grok, Claude, or Ollama-style
  command)
- **When** selected
- **Then** it is invoked through the same provider interface with no change to core
  logic

### 6.3 The LLM receives the event trail and returns strict structure
- **Given** a PR's chronological event trail
- **When** handed to the provider
- **Then** the provider is asked to return a strict, parse-friendly structure
  carrying the status enum, the bounded companion prose, a single summarizing
  emoji, the priority enum, and any stale or close-reason judgment

### 6.4 Self-correcting retry, then unverified fallback
- **Given** an LLM response that fails to parse, violates the status/priority
  vocabulary, or breaks the companion word bounds
- **When** received
- **Then** the tool re-invokes the provider preserving context, quoting the bad
  output and the required structure, for up to `llm_retry_cap` retries after the
  initial attempt, and if still invalid marks the row unverified rather than
  presenting a raw flag as truth

### 6.5 Hanging provider is bounded
- **Given** a provider subprocess that hangs
- **When** invoked
- **Then** a timeout bounds it and the run continues, the affected row marked
  unverified
- **Note** a hard invocation/timeout error fast-fails to unverified after a single
  retry rather than consuming the full `llm_retry_cap`: a stall costs a whole
  timeout and rarely recovers on immediate retry, so the self-correcting budget is
  reserved for *validation* failures (6.4), not stalls

### 6.6 Output is validated against a closed vocabulary
- **Given** any provider response
- **When** consumed
- **Then** its status, close-reason, and priority values are validated against their
  finite enums before use

### 6.7 A session provider reuses one harness across PRs
- **Given** a provider configured as a session (a heavy CLI harness such as the
  Kiro CLI)
- **When** many PRs are classified in one run
- **Then** a single harness process is launched once and reused for every PR — the
  harness's startup cost is not paid per PR — and each PR is judged from a reset,
  a priori context with no residue from the previous PR

### 6.7a A harness's tmux session never outlives its owning process
- **Given** a session provider's tmux harness, launched under a
  `GSB-Harvester-<random>` session name
- **When** the owning run ends
- **Then** that tmux session is killed as part of ending — either normally (the
  pool's deferred `Close()`, TDD 6.7) or, when the process instead receives
  SIGTERM or SIGINT (e.g. `launchctl bootout` on a still-running job, or a
  manual Ctrl-C), by that signal cancelling the run's context so the pipeline
  unwinds through its ordinary return path and still reaches the same deferred
  `Close()` — a signal is handled as a request to shut down gracefully, not
  treated as a raw process kill
- **And** as a backstop for the one case neither of those can cover — SIGKILL,
  which no program can catch — a fresh session-kind provider sweeps and kills
  any pre-existing `GSB-Harvester-*` tmux session older than a generous age
  floor before creating its own, since this tool runs single-shot and is not
  designed to run two instances concurrently (TECH), so any such session
  already alive when a new run starts building its provider cannot legitimately
  belong to a still-active run
- **And** the sweep's age floor is generous enough that it can never mistake an
  in-progress run's own session for a stale one, even under that single-
  instance assumption already ruling out the alternative
- **Note** this is ANTI-PATTERNS #10: without any of the above, a session
  survives its owning process for as long as the machine stays up, since
  nothing else on the system knows the session exists or is safe to remove

### 6.8 A session provider returns its verdict through the verdict tool, with a failsafe
- **Given** a session harness classifying a PR
- **When** it produces its verdict
- **Then** it returns the verdict by calling the single provisioned verdict tool
  (the structured classification), not as free terminal text; and if a turn ends
  without the tool being called, the provider re-invites the harness to call it up
  to a bounded number of times before marking the row unverified (TDD 6.4) rather
  than stalling

### 6.9 Summarization is a distinct, simpler provider contract
- **Given** a request for a short free-text summary (not a classification)
- **When** the provider is asked to produce it
- **Then** it goes through a separate method from per-item classification (no
  `Request`/`Constraints` shape, no bucket/action/priority vocabulary to
  validate against, no self-correcting retry loop) — a plain prompt in, a plain
  string out
- **And** failure is handled proportionately to the stakes: since a summary is a
  notification convenience rather than an authoritative judgment, a single
  failed attempt does not retry with a quoted correction the way a
  classification does; the caller falls back to a plain non-model string
- **Note** this is Option B from the design discussion: forcing summarization
  through the classification `Request` shape would mean empty/meaningless
  `Repo`/`Number`/`Role`/`State` fields and synthetic non-GitHub entries stuffed
  into `Events`, stretching a contract that is otherwise clean and
  entity-generic in a principled way

### 6.10 A session provider's two tools are mutually exclusive per turn
- **Given** a session harness that can be asked either for a classification
  verdict or for a summary
- **When** either kind of turn begins
- **Then** the harness is offered exactly one tool for that turn — the one
  matching what was actually asked — never both at once
- **And** which tool is offered is switched by the provider itself before the
  turn starts, not left to the harness to infer from prompt wording alone,
  because two simultaneously-callable tools invite the model to guess wrong
  about which one this turn wants
- **And** the underlying agent profile trusts both tool names from the start (a
  static pre-approval list, set once at session construction), so switching
  which one is offered never triggers an interactive trust prompt mid-session

### 6.11 A provider's kind is fixed by its name, never independently configured
- **Given** a provider name (`kiro`, `ollama`, or any future name)
- **When** a provider is constructed for either the primary or fallback slot
- **Then** its invocation kind (session or one-shot) is determined solely by
  that name, from one closed mapping in code — not by a separate `_KIND`
  configuration value
- **And** there is no configuration surface, environment variable, or code path
  capable of pairing a name with a kind other than its fixed one (e.g. `ollama`
  as session, `kiro` as one-shot) — the pairing is unrepresentable, not merely
  rejected at validation time
- **Note** this replaces the general `provider_kind` concept described in
  POLICY.md's Architecture rules; POLICY.md needs a matching update when this
  lands

### 6.12 Primary and fallback are the same kind of slot
- **Given** the tool is configured with a primary provider and, optionally, a
  fallback provider
- **When** either slot is constructed
- **Then** both go through the identical construction path, taking a provider
  name and a model, differing only in which configuration values feed them and
  when each is invoked
- **And** no code path treats one slot as inherently the "real" provider and
  the other as a special case — either slot may hold any supported provider
  name

### 6.13 Fallback fires only after the primary's retry budget is exhausted
- **Given** a fallback provider is configured
- **When** the primary provider's response fails validation or the primary
  times out, repeated through `llm_retry_cap` retries with no valid result
- **Then** the tool invokes the fallback provider for that same request, from
  a fresh attempt (not itself inheriting the primary's exhausted retry count)
- **And** the fallback is never invoked before the primary's full retry budget
  (including the fast-fail timeout path of 6.5) has been exhausted — attempts
  are never interleaved between the two providers
- **And** when no fallback is configured, behavior is unchanged from today:
  exhausting the primary's retries marks the row unverified (6.4)

### 6.14 Fallback exhaustion still falls back to unverified
- **Given** a fallback provider is configured and invoked (6.13)
- **When** the fallback's own response also fails validation or times out
  through its own retry budget
- **Then** the row is marked unverified, exactly as if no fallback had been
  configured — the fallback does not get a separate, larger retry budget or a
  further fallback of its own

### 6.15 Fallback applies to both classification and summarization
- **Given** a fallback provider is configured
- **When** either a classification (`Invoke`) or a notification summary
  (`Summarize`) call to the primary provider fails
- **Then** the same fallback provider is invoked for that call, using the same
  trigger rule (6.13) appropriate to that call's own retry shape (`Invoke`'s
  multi-retry loop per 6.4, `Summarize`'s single-attempt-then-plain-fallback
  per 6.9)

### 6.16 Every exhaustion and fallback outcome is logged at a level matching its severity, and a fallback verdict is persisted with provenance
- **Given** the primary provider's retry budget is exhausted (6.13)
- **When** the tool decides what to do next
- **Then** it logs that exhaustion — at WARN level when a fallback is
  configured (degraded but recoverable: another attempt is about to be made),
  or at ERROR level when no fallback is configured (the row is going straight
  to unverified)
- **And** when a fallback is then invoked and produces a valid result, the tool
  logs that outcome at INFO level, identifying the item and naming the
  fallback provider that produced it — succeeding via fallback is a normal,
  expected recovery, not a warning-worthy condition
- **And** when the fallback's own retry budget is also exhausted (6.14), the
  tool logs that at ERROR level — the row is now going to unverified with no
  further recourse
- **And** a classification result (`Invoke`) that came from the fallback
  provider is, in addition to being logged, recorded in the persisted record
  (`model.PR` / `model.Issue`): a field carries that this item's current
  judgment came from the fallback provider, round-tripped through the YAML
  store like any other field
- **And** that field is always written explicitly — every persisted item
  states plainly whether its current judgment came from the primary or the
  fallback provider; the normal primary case is never expressed by omitting
  the field, so a reader of the raw YAML never has to know an absence
  convention to tell the two apart
- **Note** a `Summarize` fallback (6.15) is not persisted — the notification
  hook payload is transient, not stored — so the persistence half of this
  rubric covers `Invoke` results only; the logging half applies to both
  `Invoke` and `Summarize` fallback attempts alike

### 6.17 The fallback provider is bounded by its own, independently configured timeout
- **Given** a fallback provider is configured
- **When** the fallback is invoked (6.13, 6.15)
- **Then** each fallback call is bounded by its own timeout value, distinct
  from the primary's `provider_timeout` (TDD 6.5) — not the same shared value
  applied to both slots
- **And** the primary's own timeout is never affected by the fallback's
  configuration; a hung primary still fails over at its existing bound
- **Note** discovered necessary by live verification, not assumed in advance
  (ANTI-PATTERNS #6): a one-shot fallback backed by a local model can
  legitimately take longer per call than a session-based primary, so forcing
  both slots through one shared timeout would either make the fallback
  unusably tight or make every ordinary primary hang take proportionally
  longer to detect

### 6.18 A session provider's prompt file survives until the harness's turn is done
- **Given** a session provider hands an item to the harness by file (TDD 6.7,
  large event trails exceed the terminal command-length limit)
- **When** the instruction naming the file's path has been sent to the
  harness
- **Then** the file is not removed until the harness's turn has actually
  concluded — either a verdict/summary was collected or the turn was given up
  on — never immediately after the instruction is sent
- **And** the harness's own asynchronous processing of the turn (reading the
  file, reasoning about it, calling the result tool) always has the file
  available to read, however long that takes within the call's own timeout
- **Note** discovered by live inspection of a harness session rather than
  from any test: sending the instruction only proves the keystrokes were
  typed, not that the harness has read the file yet, so deleting the file as
  soon as the instruction is sent races the harness's own read and can (did)
  make every large-trail item fail with "file not found" — indistinguishable
  from a model classification error until the harness's own transcript was
  read directly

---

## 7. Execution & portability

### 7.1 Headless end-to-end run
- **Given** a scheduled invocation
- **When** the binary runs without a terminal
- **Then** it completes fetch → persist → render → write with no interactive input

### 7.2 Platform-independent core
- **Given** the same binary on Linux
- **When** run under cron
- **Then** it operates with no macOS-specific dependency in its core — scheduler
  integration (launchd, cron) is isolated from the rest of the system
- **And** any host-specific path convention is resolved through that same seam
  rather than branched on in the core: the default artifact locations follow the
  running platform's own convention for per-user application data, while the
  artifact filenames stay fixed
- **And** the operator may override those locations, so no default path is load
  bearing

---

## 8. Issue tracking

Issues are a second tracked entity alongside pull requests, deliberately simpler:
there is no merge, the close reason is a hard GitHub fact rather than an inference,
and an issue with no conversation is never sent to the model.

### 8.1 Issues the operator authored or participates in are tracked
- **Given** the operator has authored GitHub issues, commented on issues others
  opened, or been assigned issues
- **When** the tool acquires the tracked set
- **Then** all three kinds are recorded, deduplicated, each carrying its role —
  *author* for issues the operator opened, *participant* for issues they only
  commented on or were assigned
- **And** being assigned counts as participation even when the operator has left no
  comment, since an assignment is itself an ask

### 8.2 A closed issue's disposition is a hard fact, never inferred
- **Given** an issue GitHub reports as closed
- **When** classified
- **Then** its bucket is Closed and its close reason is taken directly from
  GitHub's own state reason (completed, not planned, duplicate, reopened)
- **And** no model inference may reclassify it or override that reason — Closed is
  an immutable floor for issues, as Merged and Closed are for PRs (4.1, 4.2)

### 8.3 An issue with no conversation is not sent to the model
- **Given** an open issue with no comments
- **When** classified
- **Then** it is classified from hard GitHub facts alone and carries no inferred
  note — there is no discussion for a model to read
- **And** its row renders as a clean fact-only row, not as a failed or unavailable
  one; it is not marked `unverified`, since nothing was attempted

### 8.4 An issue with any conversation gets an inferred note
- **Given** an open issue with one or more comments
- **When** classified
- **Then** the model judges its disposition from the comment trail and supplies a
  short note and emoji, exactly as it does for a PR

### 8.5 A long-quiet open issue becomes stale on its own schedule
- **Given** an open issue with no activity for longer than
  `issue_stale_age_threshold`
- **When** classified
- **Then** it is bucketed Stale
- **And** that threshold is independent of the PR `stale_age_threshold`, because
  issues legitimately sit idle far longer without being abandoned

### 8.6 The status document presents issues separately from pull requests
- **Given** a store containing both PRs and issues
- **When** the document is rendered
- **Then** it ends with an Issues section, separated from the PR content by a
  horizontal rule, holding four sections: issues the operator authored, issues
  they participate in, stale issues, and closed issues
- **And** the stale and closed sections are each split by role, so an archived
  issue still shows whether the operator opened it or merely took part; the two
  active sections are already per-role, so no table needs a Role column
- **And** each bucket appears in exactly one place: a closed issue only under
  Closed, a stale issue only under Stale, and neither in an active section
- **And** the top-of-document summary reports issue counts alongside PR counts, so
  it remains a single at-a-glance view of everything tracked
- **And** the document title reflects that the document tracks more than PRs

### 8.7 Issues and pull requests coexist in one source of truth
- **Given** the store
- **When** issues and PRs are both persisted
- **Then** each issue is its own tagged document alongside the PR documents, and
  operator-set state on either survives a refresh (2.3)
- **And** a store written by a build that does not recognize issues preserves them
  rather than dropping them (2.5)

### 8.8 An unchanged open issue skips the provider call
- **Given** an open issue whose deterministic inputs — its comment count and its
  most recent trail event — are identical to what they were when the stored
  record was last classified, and that stored record was not itself unverified
- **When** the tool classifies the issue on a subsequent run
- **Then** it never calls the provider for this issue: the stored bucket, action,
  priority, companion, and emoji are carried forward verbatim
- **And** the GitHub fetch still runs in full beforehand — only the provider call
  is skipped, never the fact-gathering that would detect a change
- **Note** this mirrors 4.13 for the simpler issue trail, which has no CI,
  mergeability, or review-thread concepts — an issue's disposition depends only
  on how much has been said and when, so those are the only two inputs that need
  to match

### 8.9 A first-seen, changed, or previously-unverified issue always reaches the provider
- **Given** an open issue that either has no prior stored record, or has a prior
  record whose deterministic inputs (8.8) differ from the freshly fetched ones,
  or has a prior record marked unverified
- **When** the tool classifies the issue
- **Then** the provider is called as normal — the skip in 8.8 never applies, so a
  real change (a new comment, most obviously) is never mistaken for a repeat, and
  a previously degraded judgment is never cached forward as if it were settled
- **Note** an issue that was never worth judging in the first place (8.3, no
  conversation) has no fingerprint to compare — it is classified from hard facts
  alone either way, so 8.8/8.9 do not apply to it

---

## 9. Notification hook

### 9.1 A hook fires only when something actually needed judging
- **Given** a completed classification pass over open PRs and issues
- **When** the tool determines whether to notify
- **Then** it collects exactly the open items that reached the provider this run
  — first-seen, changed (4.13, 8.8), or previously unverified — and fires the
  hook only when that set is non-empty
- **And** an item carried forward unchanged never appears in the set, and a run
  where every item was unchanged produces no notification at all
- **Note** this reuses the same population the fingerprint skip already
  distinguishes; no second change-detection mechanism is introduced

### 9.2 Merged, closed, and stale items never trigger the hook
- **Given** a PR that merged or closed, or any item (PR or issue) that aged or
  was judged into the stale bucket this run
- **When** the tool determines whether to notify
- **Then** that item never appears in the hook's payload, even though its floor
  note may have consulted the provider this run (TDD 4.1, 4.2, 8.2)
- **Note** a settled item is not something the operator is expected to act on
  further; the fingerprint concept (9.1) has no floor/stale equivalent, and a
  terminal item's own floor-note call is not a "change" in that sense

### 9.3 The hook payload carries the new state, not a diff
- **Given** a non-empty set of changed items (9.1)
- **When** the payload is built
- **Then** each entry carries the item's identity and its freshly judged
  bucket/action/companion/priority — never the item's previous state — because
  the hook exists to prompt a look at the status document, not to reconstruct
  history the document already is the record of
- **And** the payload also carries one short model-generated summary sentence
  covering the whole set, sized for a desktop notification rather than the
  document itself

### 9.4 The hook is a configured shell command receiving JSON on stdin
- **Given** `GSB_NOTIFY_HOOK` configured to a shell command
- **When** the hook fires (9.1)
- **Then** the tool writes the JSON payload (9.3) to that command's stdin and
  waits up to `GSB_NOTIFY_TIMEOUT` for it to exit
- **And** when `GSB_NOTIFY_HOOK` is unset, the hook mechanism is inert — no
  process is spawned, no summary is requested, and classification is unaffected

### 9.5 A failing or slow hook never fails the run
- **Given** the hook command exits non-zero, cannot be started, or exceeds
  `GSB_NOTIFY_TIMEOUT`
- **When** this happens
- **Then** the tool logs it and continues — the status document has already
  been written by this point, and a broken notification integration must never
  be the thing that breaks the dashboard

### 9.6 The payload's model-derived text is sanitized to natural language, never left as potential code
- **Given** the notification payload (9.3), whose `summary` and every
  `change.companion` are model-generated text ultimately traceable back to
  GitHub content the operator does not control (a PR title, description,
  comment, or issue body, potentially written by someone else)
- **When** the tool builds the payload
- **Then** both fields are stripped of every character outside natural-language
  prose and emoji before the payload is marshaled — Unicode letters, marks,
  digits, spaces, and ordinary punctuation (including quotes and apostrophes)
  are kept; the characters that actually enable command substitution or
  chaining (backtick, `$`, `\`, `;`, `|`, `&`, angle brackets, every
  bracket/brace/parenthesis) and control characters are dropped, not escaped
- **And** quotes are deliberately preserved rather than stripped: they are
  ordinary prose, and with substitution/chaining already removed, a lone
  quote cannot invoke a command on its own — a downstream consumer is
  expected to quote these fields correctly when building its own notification
  call, standard shell-scripting discipline rather than a special burden
- **And** this makes the guarantee real for the characters that matter most:
  a downstream consumer that blindly forwards the JSON to a shell, `eval`, or
  another interpreter cannot be tricked into command substitution or command
  chaining, because those characters are structurally absent from the string
- **And** the same sanitization is applied preventatively on the way in, too:
  a companion note is sanitized before it becomes part of the prompt text
  sent to the provider for the summary call, not only after the provider
  responds — narrowing the prompt-injection surface a crafted GitHub comment
  could exploit to steer that call, on top of (not instead of) sanitizing
  whatever the provider ultimately returns
- **And** `GSB_NOTIFY_HOOK` itself is trusted operator configuration, invoked
  as a fixed command; sanitization exists for the untrusted content flowing
  through that trusted command, not for the command itself (SCHEMA §
  Notification hook, trust boundary)

### 9.7 A leaked reasoning trace never reaches the hook as the summary
- **Given** a Summarize call answered by a "thinking" one-shot model (e.g.
  GLM-4.7-Flash under Ollama, the fallback slot) that narrates its reasoning
  before answering — drafting and revising candidate sentences, and
  potentially echoing summaryPrompt's own instructions back verbatim
  ("Constraint 1: exactly one short sentence...")
- **When** the response is extracted
- **Then** Ollama's own CLI convention — a literal `...done thinking.` line
  preceding the real answer — is used to discard everything before it, the
  same handling extractJSON already applies on the classification path
  (provider/parse.go); a response with no such marker (a non-thinking model,
  or the session/Kiro path, which never narrates) is considered in full,
  unchanged from before
- **And** as a backstop behind that cutoff, an extracted candidate is rejected
  — treated as a failed attempt, not returned — when it is far longer than a
  genuine "one short sentence... fit for a desktop notification toast" could
  plausibly be, or when it contains one of a small set of substrings that only
  appear in the prompt's own instructions or in a model talking about the
  prompt (e.g. "desktop notification toast", "constraint 1", "the prompt
  asks") — a real summary sentence has no reason to contain either
- **And** a rejected candidate degrades exactly like any other failed
  Summarize attempt (6.9): the fallback provider gets its own attempt if one
  is configured, and the plain non-model string is used if not — the prompt
  and a model's reasoning about the prompt are for the model's own inference
  only, never for the delivered notification, under any failure mode
- **Note** this is the live-verification-only defect class again
  (ANTI-PATTERNS #6): notify's extraction had no equivalent of
  provider/parse.go's thinking-trace handling, and every existing Summarize
  test used a clean canned response, so the gap passed a fully green suite
  and was only caught by reading an actual delivered desktop notification
