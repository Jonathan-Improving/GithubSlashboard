# Plan: Pinned role (operator-forced tracking exception)

## Problem

The tracked PR set is entirely derived from three GitHub searches: `author:`,
`review-requested:`, and `reviewed-by:` (`github.FetchTracked`). A PR the
operator did not author and has never been asked to review — but wants to
follow anyway, under one of the two existing role-split sections — has no way
into the tool today. The operator wants an explicit escape hatch: pin a
specific `owner/repo#number` into either "Submitter: PRs I Authored" or
"Reviewer: PRs I Reviewed", after which it is tracked exactly like any other
PR of that role — crawled, classified, and moved between Open/Stale/Merged (or
Open/Done under Reviewer) by the same rules as a naturally-discovered PR.

### Root cause

Not a defect — the tool's acquisition model has no operator-input surface at
all today. Every tracked item is 100% GitHub-search-derived; the only
operator-set state that survives a run is `OperatorStale`, a mutation of an
*already-tracked* item, not a mechanism for adding one. There is no CLI path
that writes to the store outside the full acquire→classify→persist→render
pipeline.

## Possible approaches

### ❌ 1. New `pinned_role` field on the existing `!pr` document

A `pinned_role` field on `model.PR` (like `OperatorStale`), set by a CLI that
writes a stub `!pr`; acquisition force-fetches any `!pr` carrying it, and the
role is forced to the pinned value.

**Rejected**: conflates operator intent (the pin) with derived state (the
judged PR record) on one document, so a stub `!pr` renders a blank title until
the first crawl fills it, and unpinning has to decide whether to delete the
whole PR record or just the field. Approach 3 keeps the two as separate
documents and avoids both problems.

### ❌ 2. Config-file / env-var list of pins, no store mutation

A `GSB_PINNED_PRS` env var or sidecar file (`owner/repo#number=role` lines)
read by acquisition each run; no CLI, hand-edited.

**Rejected**: puts durable operator intent outside the store, contradicting the
store-is-single-source-of-truth principle (`OperatorStale` already lives *in*
the store) and POLICY's "prefer extending an existing path over adding a
parallel one". A hand-edited file is also worse UX than a CLI flag.

### ✅ 3. New `!pinned-pr` YAML tag, a distinct document type

The pin is its own tagged document (coexisting with `!pr`/`!issue`, per the
established tag-extension pattern) holding only `repo`, `number`, `role`.
Acquisition reads these alongside `!pr` documents and feeds each into the
tracked set as an extra PR to fetch; the normal acquire pass produces a full
`!pr` record from it. The `!pinned-pr` document persists independently of the
`!pr` it produces, so operator intent and derived state stay cleanly separated.
See Recommendation for the settled details.

## Recommendation

**Approach 3, pin-wins.** A distinct `!pinned-pr` tagged document (holding only
`repo`, `number`, `role`) carries the operator's standing instruction,
separate from the derived `!pr` record it produces. This keeps operator intent
and derived state as two honest document shapes and mirrors the existing
`!pr`/`!issue` tag-coexistence pattern. On conflict the pin's role wins
unconditionally (decision 2 below).

### Settled decisions

1. **CLI shape.** `githubslashboard --pin owner/repo#123=submitter` /
   `--pin owner/repo#123=reviewer` / `--unpin owner/repo#123`. Each is a
   standalone store mutation — read, mutate `!pinned-pr` documents, write, exit
   — with no GitHub crawl, no classify, no render. Double-hyphen syntax.

2. **Conflict resolution: pin wins unconditionally.** When a pinned role
   disagrees with what the searches discovered, the pinned role is stamped onto
   the record regardless. The role invariant is therefore *"role is what GitHub
   reports, unless the operator has pinned it otherwise"* — an explicit,
   durable operator override, distinct in kind from the *silent, transient*
   mis-filing that ANTI-PATTERNS #11 guards against (an incomplete `author:`
   page). The override must be logged when it changes a discovered role, so it
   is never silent. A useful consequence: because the pinned role wins whether
   or not a search also found the PR, the "was this independently discovered?"
   question never affects the resulting role — which sidesteps #11's
   incomplete-search hazard for pinned PRs entirely.

3. **Unpin semantics.** `--unpin` only removes the `!pinned-pr` document. If the
   PR is independently discoverable via search, it remains tracked under its
   natural (search-derived) role on the next run; if it was a pure exception
   (Case B), it stops being force-included and its `!pr` record settles/ages
   out like any other no-longer-discovered PR.

4. **No GitHub call at pin time.** `--pin` writes only the tiny `!pinned-pr`
   stub. Acquisition already fetches any tracked PR via `PullRequests.Get` and
   builds its full trail, so the pin just feeds one more `owner/repo#number`
   into that fetch list; the normal acquire pass populates the real `!pr`
   (title, URL, state, judgment) exactly as for a search hit.

5. **Settling behavior: permanent.** A pinned PR rides the same bucket
   transitions as any tracked PR, including into Merged/Closed, and does not
   auto-unpin or disappear once settled. The `!pinned-pr` document persists
   independently and keeps re-including the PR every run until `--unpin`.

6. **Bad pin is skipped, not fatal.** A pin whose PR returns 404 on fetch
   (typo'd or deleted `owner/repo#number`) is logged loudly and skipped; the
   rest of acquisition proceeds and the `!pinned-pr` document is left in place
   for the operator to fix or `--unpin`. A single fat-fingered pin must not take
   down the unattended dashboard. This is a deliberate exception to the
   otherwise-strict acquisition-failure-is-fatal rule (TDD 1.2), narrow to the
   per-pin fetch and justified by the pin being operator-supplied rather than a
   systemic API failure.

7. **Store lockfile serializes the CLI and the pipeline.** `--pin`/`--unpin` and
   a pipeline run both acquire an exclusive lock on the store before their
   read→mutate/render→write, so a pin edit can never be clobbered by a
   concurrent crawl's write (nor vice versa). Sub-decisions, to close the gaps a
   lock introduces:
   - **Location**: a sibling lockfile next to the store path
     (`<store>.lock`), so it shares the store's directory and volume.
   - **Scope**: held across the whole read-then-write critical section of each
     writer, not just the write, so the read a mutation is based on cannot go
     stale under it.
   - **Contention**: the short-lived CLI mutation waits (bounded) for a running
     pipeline to release; a pipeline that finds the lock held by another
     pipeline (an overrun of the prior scheduled run) exits without waiting, as
     it does today for an overlapping run. The CLI's bounded wait then fails
     with a clear "store busy, retry" message rather than blocking forever.
   - **Stale lock**: a lock is released on normal exit and on the SIGTERM/SIGINT
     graceful-shutdown path `main` already installs; a lock left by a hard kill
     is reclaimed by an age floor (a lock older than a generous bound is treated
     as abandoned), mirroring the existing stale-tmux-session sweep pattern.

8. **Pinned PRs render with a 📌 marker.** A rendered PR that originates from a
   `!pinned-pr` carries a pin marker so the operator can tell an explicit
   exception from a naturally-tracked PR. This makes render aware of which `!pr`
   keys have a matching `!pinned-pr` — a new render input (the set of pinned
   keys) alongside the PR/issue slices.

## Technical details preserved

- `github.Client.FetchTracked` currently merges three search results with an
  explicit submitter-wins rule (`existing.Role != model.RoleSubmitter &&
  p.Role == model.RoleSubmitter`). The pin layer runs *after* this
  search-merge: for each `!pinned-pr`, force `Role = pin.role` on the matching
  record (Case A) or fetch-and-create it (Case B). Role is decided in this one
  place; the search-merge and its #11 incomplete-search abort still run first,
  unchanged, on the search-derived set.
- The `!pinned-pr` document is read from the store at the start of the run
  (alongside `!pr`/`!issue`) and fed into acquisition as extra
  `owner/repo#number` entries to fetch. It is a *separate* document from the
  `!pr` it produces: the pin persists independently and is only removed by
  `--unpin`, so a settled (merged/closed) pinned PR keeps being re-included.
- The terminal-skip carry-forward path (`if !includeTerminal { if old, ok :=
  prior[k]; ok && (old.Bucket == ... merged/closed) ... }`) already carries a
  record forward without a fresh crawl; a settled pinned PR flows through this
  same path once classified. The pinned role must be re-stamped on the carried
  record each run (the pin, not the carried `!pr`, is the source of truth for
  role), so the pin layer applies after carry-forward too.
- `store` gains a `TagPinnedPR = "!pinned-pr"` constant, an `ingest` case, a
  `validatePinnedPR` (require `repo`, positive `number`, a valid `model.Role`),
  a `pinnedPRNode` writer, and a merge/preserve path. Unrecognized tags are
  still round-tripped verbatim (TDD 2.5). The pin document reuses `model.Role`
  (already a closed submitter/reviewer enum) rather than minting a parallel
  type.
- The pin/unpin CLI mutation and the pipeline both write the store via the same
  atomic temp-file + rename (`store.atomicWrite`); on top of that, both acquire
  an exclusive `<store>.lock` across their read→write critical section
  (decision 7) so a concurrent edit is serialized, not merely uncorrupted. The
  lock is released on normal exit and on the existing SIGTERM/SIGINT graceful
  path, and reclaimed by an age floor if a hard kill orphans it — mirroring the
  stale-tmux-session sweep already in `provider`.
- A direct single-PR fetch already exists and is proven: `attachEventTrail`
  calls `c.rest.PullRequests.Get(ctx, owner, name, p.Number)`. The Case-B
  pinned-PR acquisition path is the same call, reached from a store-recorded
  pin rather than a search hit; a 404 there is skipped-with-log per decision 6,
  not fatal.
- Render gains a new input: the set of `!pr` keys that have a matching
  `!pinned-pr`, so a pinned row can carry the 📌 marker (decision 8). The store
  already holds both document types, so this is derived at render-call time from
  the same store, not a new persisted field.

## Proposed TDD Rubrics

Ratified by completion of this plan; they move into TDD.md (store §2,
acquisition §1, a new pin-CLI area) as the behaviour is built. Rubrics flagged
**(contingent Qn)** depend on the matching open question above — settle the Q in
the editor and the rubric's branch is fixed.

### P.1 A pinned PR is force-included in the tracked set
- **Given** a `!pinned-pr` document for `owner/repo#N` with a role
- **When** the tool acquires the tracked set
- **Then** `owner/repo#N` is fetched and classified as a tracked PR even when
  none of the `author:` / `review-requested:` / `reviewed-by:` searches return
  it, and it renders in the role-split section for its pinned role

### P.2 A pinned role wins over the discovered role
- **Given** a `!pinned-pr` for `owner/repo#N` pinned as one role
- **And** the searches independently discover `owner/repo#N` under a different
  role (e.g. pinned `reviewer` but the operator authored it)
- **When** the tool classifies and renders
- **Then** the record carries the **pinned** role, and the override is logged
  (the decision, the PR identifier, discovered-vs-pinned role) so it is never
  silent
- **Note** this is the deliberate, durable operator override — distinct from the
  silent transient mis-filing ANTI-PATTERNS #11 guards against; the search-merge
  and its incomplete-results abort still run first, unchanged

### P.3 A pinned PR settles like any tracked PR and stays pinned
- **Given** a pinned PR that has since merged or closed
- **When** the tool re-runs
- **Then** it flows through the normal terminal-skip carry-forward and renders in
  the Merged/Closed (or reviewer Done) table, and the `!pinned-pr` document
  persists — the PR is not auto-unpinned and does not disappear
- **And** its pinned role is re-stamped from the pin each run (the pin, not the
  carried record, is the source of truth for role)

### P.4 `--pin` writes only the pin document, with no GitHub call
- **Given** the CLI invoked as `--pin owner/repo#N=submitter` (or `=reviewer`)
- **When** it runs
- **Then** it reads the store, adds (or updates) exactly one `!pinned-pr`
  document for `owner/repo#N` with that role, writes the store, and exits —
  making no GitHub request and touching no `!pr`/`!issue` document
- **And** a malformed target (`owner/repo#N` not parseable, or a role outside
  {submitter, reviewer}) is rejected with a non-zero exit and no store write

### P.5 `--unpin` removes only the pin document
- **Given** a `!pinned-pr` for `owner/repo#N` in the store
- **When** the CLI is invoked as `--unpin owner/repo#N`
- **Then** that `!pinned-pr` document is removed and the store written; any `!pr`
  document for `owner/repo#N` is left untouched
- **And** on the next pipeline run the PR is tracked only if a search
  independently discovers it (under its natural role); otherwise it is no longer
  force-included
- **And** `--unpin` of a PR that has no pin document exits cleanly (idempotent),
  reporting nothing to remove

### P.6 The pin document coexists and round-trips
- **Given** a store holding `!pr`, `!issue`, and `!pinned-pr` documents
- **When** the tool reads and rewrites it with upstream unchanged
- **Then** all three document types are preserved, the `!pinned-pr` documents
  validate (require repo, positive number, a valid role), and an unrecognized
  tag is still round-tripped verbatim (extends 2.5)

### P.7 A bad pin is skipped, not fatal
- **Given** a `!pinned-pr` for a PR that returns 404 on fetch (typo'd or deleted)
- **When** the tool acquires
- **Then** the bad pin is logged loudly and skipped, the rest of acquisition
  proceeds normally, and the `!pinned-pr` document is left in place for the
  operator to fix or `--unpin`
- **Note** deliberate narrow exception to 1.2 (acquisition-failure-is-fatal),
  scoped to the per-pin fetch because a pin is operator-supplied input, not a
  systemic API failure

### P.8 Pinned PRs render with a 📌 marker
- **Given** a rendered PR that originates from a `!pinned-pr`
- **When** the Markdown is rendered
- **Then** its row carries the pin marker (📌), distinguishing it from a
  naturally-tracked PR of the same role/bucket
- **And** a PR with no matching `!pinned-pr` renders without the marker,
  unchanged from today

### P.9 The store lock serializes the CLI and the pipeline
- **Given** a pipeline run holding the store lock
- **When** `--pin`/`--unpin` is invoked concurrently
- **Then** the CLI waits (bounded) for the lock, performs its mutation once the
  pipeline releases, and neither writer's changes are lost
- **And** if the bounded wait elapses, the CLI exits non-zero with a "store
  busy, retry" message rather than blocking indefinitely or clobbering
- **And** a lock left behind by a hard-killed process is reclaimed once older
  than a generous age floor, so a crash cannot wedge the store permanently
