# Known Issues

| ID | Issue | Severity |
|----|-------|----------|
| B | Acquisition is broad and bounded by GitHub's search rate limit | Low |
| C | The CI-failing flag counts any failing check, not just required ones | Low |
| D | An inferred note can outlive the fact it asserts | Medium |
| E | The unchanged-input fingerprint cannot see a same-count thread swap | Low |

## B. Acquisition is broad and bounded by GitHub's search rate limit

**Severity**: Low (a scheduled run completes comfortably within its window; this is
a characteristic of the acquisition breadth, not a failure)

Acquisition fans out three searches (authored / review-requested / reviewed-by) and
then, per PR, issues a PR fetch plus paginated comments, reviews, and timeline
calls. GitHub's search endpoint is capped at 30 requests/minute, so acquisition
cannot be sped up by issuing more searches in parallel. As of the terminal-skip
optimization, PRs the store already records as merged/closed are no longer
re-crawled by default (their per-PR calls are skipped and the cached record is
carried forward — TDD 1.5), so the remaining per-PR cost is the *open* PRs, which
must still be re-crawled each run because their state can advance — the fetch, not
just the provider call, is what proves nothing changed (TDD 4.13), so this cost is
not avoidable by any input-comparison scheme. Each open PR additionally pays one
check-runs call (TDD 1.4) and one GraphQL review-threads query (TDD 1.6), so open
PRs dominate the acquisition budget.

Issue acquisition adds three more searches (authored / commented / assigned) plus a
per-issue fetch, comments, and timeline. Measured against a live account this is
negligible next to the PR set — single-digit issue counts, under ten seconds — and
closed issues are subject to the same terminal-skip, so issues are not a
contributor to this limitation.

This entry previously also covered the provider (LLM) cost of a full classify —
that half is now resolved: an open PR whose deterministic inputs are unchanged
since the prior stored record skips the provider call entirely and carries the
cached verdict forward (TDD 4.13), using an opaque input fingerprint rather than
`updated_at` alone, because live GitHub data showed a full CI check-run cycle can
complete without moving a PR's own last-modified signal (see ANTI-PATTERNS #6).
What remains open here is the GitHub-side acquisition cost alone.

**Remaining / further mitigation options**:
- Scope acquisition to a smaller working set (e.g. open PRs plus those updated
  within a recent window), which TDD 1.1 permits; this cuts acquisition time.
- Accept it: the acquisition cost in an unattended run that has a wide window is
  not a practical constraint.

## C. The CI-failing flag counts any failing check, not just required ones

**Severity**: Low (it over-reports rather than under-reports, so no failing build is
ever missed; the cost is an occasional row flagged for a check that would not block
a merge)

The deterministic CI-failing flag (TDD 4.10) is derived from the head commit's
check-runs, treating any conclusion of failure, timed_out, action_required, or
startup_failure as failing. It does not distinguish a **required** check from an
advisory one, because the check-runs response does not say which are required —
that is branch-protection configuration, a separate API call per repository.

So a repository with an optional linter, a nightly benchmark, or an experimental
matrix leg that fails routinely will show its PRs flagged even though the failure
does not block merging. The row is not wrong about the build, but it overstates the
consequence.

**Mitigation options**:
- Fetch branch protection per repository and intersect the failing set with the
  required contexts. Most accurate; costs one extra call per distinct repository
  (cacheable within a run, since the required set is per-branch not per-PR) and
  needs a token permitted to read protection settings, which a read-only token on a
  repository the operator does not administer may not be.
- Approximate with `mergeable_state`: GitHub reports `blocked` when required checks
  have not succeeded. Cheap, since it is already in the PR payload, but conflates
  several causes (missing approvals, out-of-date branch) and so is not a clean
  signal for CI specifically.
- Accept it: over-reporting a red build is the safe direction for a dashboard whose
  purpose is to stop failures being overlooked.

## D. An inferred note can outlive the fact it asserts

**Severity**: Medium (a row can read as self-contradictory — a cleared status
alongside prose still asserting the problem — and the reader has no way to tell
which half is current)

Deterministic facts are re-derived from GitHub on every run, so they self-correct:
when a build goes green the CI-failing flag clears itself, and when review threads
are resolved the review-feedback action clears itself. The model's **companion
note** has no such guarantee. It is prose written once from the event trail, and it
persists until a later run happens to produce different prose.

The two therefore drift. A note reading "CI failing, author must fix tests" can
survive on a row whose flag has already cleared, and nothing reconciles them. The
window is usually short, because a run that re-derives the fact normally re-judges
the note in the same pass — but not always: if classification falls to `unverified`
(TDD 6.4), or if floor notes are skipped, the fact updates while the prose does not.

This is the same shape as an earlier defect in which a note kept reporting a linker
error the trail had long since superseded; that instance was addressed by feeding CI
state into the trail, which improves the model's *input* but does not make its
*output* self-correcting.

**Mitigation options**:
- Have the renderer suppress or qualify note text that contradicts a deterministic
  flag. Reliable only for phrasings the renderer can recognize, so it is pattern
  matching on prose — brittle by nature.
- Record the head SHA (or the trail's last-event timestamp) that a note was inferred
  from, and mark the note stale in the render when the current value differs. Honest
  and self-maintaining, at the cost of one persisted field and a visible "note may
  be out of date" state.
- Re-judge unconditionally whenever a deterministic fact changes between runs,
  rather than relying on the ordinary refresh. Closes the window but spends model
  calls precisely on the rows that change most often.
- Accept it: the refresh interval bounds the drift, and the deterministic marks are
  the authoritative half of the row.

## E. The unchanged-input fingerprint cannot see a same-count thread swap

**Severity**: Low (requires two independent events to land in the same run with no
other trail activity in between; the count itself is always caught)

The unchanged-input fingerprint (TDD 4.13) includes `UnresolvedThreads` as a count,
not as the identity of which threads are open. A run in which one thread is
resolved and a different one is newly opened, with the count landing on the same
number and no other trail event in between, would fingerprint identically to the
prior run and be skipped — even though which comment the author must answer has
changed.

In practice this is narrower than it first appears: opening a new review thread is
itself a comment event, which changes the "most recent trail event" component of
the fingerprint and would already force a re-judgment. The gap is real only when
the swap produces no trail event of its own in the same run, which live probing
during this feature's development could not produce or confirm a concrete case
for — the review-thread-resolution GraphQL query was found to have no accompanying
timeline entry, but every resolution observed also carried a comment.

**Mitigation options**:
- Fold the resolved thread-node IDs (not just the count) into the fingerprint via
  the GraphQL review-threads query already being made (TDD 1.6, `collectReviewThreads`
  equivalent). Closes the gap fully; costs nothing extra since the query already
  runs.
- Accept it: the scenario requires a same-run count coincidence with no other
  qualifying trail activity, which is narrower than most of the other known
  limitations in this document.
