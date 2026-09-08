# Anti-Patterns

Lessons from costly trial-and-error while building the session provider and its
unattended deployment. Each entry is a trap specific to *this* project; tooling
quirks that would bite any kiro-cli project are surfaced to the operator instead.

| # | Anti-pattern | Severity |
|---|--------------|----------|
| 1 | Sending input to an interactive harness without turn synchronization | High |
| 2 | Typing a large payload into a terminal session instead of handing off a file | High |
| 3 | Writing the output into a macOS-protected or foreign-owned file unattended | High |
| 4 | Over-tight companion word bounds reject valid classifications | Medium |
| 5 | Advertising a status the response vocabulary cannot satisfy | Medium |
| 6 | Trusting unit tests to validate a deterministic fact's plumbing | Medium |
| 7 | Sharing one timeout bound across two structurally different providers | Medium |
| 8 | Deleting a handed-off file before the harness's asynchronous read of it | High |
| 9 | Free-text model output extracted with only TrimSpace, no thinking-trace guard | Medium |
| 10 | A killed (not returned) process orphans its tmux harness session | Medium |

## 1. Sending input to an interactive harness without turn synchronization

**Symptom**: A long classify run slowed, then effectively hung; the harness pane
showed "Compacting conversation…" with dozens of messages queued and context
climbing toward its limit.

**What was tried**:
- Sending `/clear` then the prompt back-to-back between PRs — context never reset.
- Assuming the model was just slow — it wasn't; input was piling up.

**Root cause**: An interactive agent harness only executes a slash command when it
is idle. Sent mid-turn, `/clear` (and the next prompt) is *queued as chat text*,
not executed, so the conversation is never reset and context grows unbounded until
the harness auto-compacts — which is very slow — and the run stalls.

**Resolution**: Gate every between-turn action on a pane-detected idle state
(idle-marker present, busy-marker absent, held stable for a settle window), bounded
by a short timeout so a missed idle fails fast into retry. After collecting the
result, send an interrupt to end the turn deterministically before the next PR.

**Lesson**: Driving an interactive TUI programmatically is a request/response
protocol with no framing — you must detect readiness yourself before every send,
never fire-and-forget.

**Cost**: A ~25-minute run wasted plus several diagnosis iterations before the
queued-input root cause was found — Severity: High.

## 2. Typing a large payload into a terminal session instead of handing off a file

**Symptom**: A subset of PRs — always the ones with long event trails — failed
classification on every retry and fell to `unverified`. First misdiagnosed as an
inference/word-bound problem.

**What was tried**:
- Widening the companion word bounds (helped a little, wrong root cause).
- Retrying — every attempt failed identically for the same PRs.

**Root cause**: `tmux send-keys` rejects a single payload above ~16–20 KB with
"command too long", silently truncating or dropping large event trails so the
harness never received a well-formed request.

**Resolution**: Hand the prompt to the harness by *file*: write it to a temp file
in a directory the harness's read tool is scoped to, and send only a short
instruction to read that path. Keep small prompts inline (one round-trip) below a
conservative size threshold; use the file path only above it. Delete the file after
the verdict.

**Lesson**: A terminal is not a data channel — anything that can grow past a few KB
must be passed out-of-band (a file), not typed into the session.

**Cost**: A full misdiagnosis loop (chased word bounds first) before the
send-keys length limit surfaced in the logs — Severity: High.

## 3. Writing the output into a macOS-protected or foreign-owned file unattended

**Symptom**: The output file never updated. Each full run finished classification
and persisted the store, then the final write to `~/Documents/…` hung for 60+
seconds and failed — after ~20 minutes of work, with an opaque stall, not a clear
error.

**What was tried**:
- Atomic temp-write + rename into the output dir — still stalled.
- Staging the temp file outside the synced tree, then renaming in — still stalled.
- Granting Full Disk Access — new files could be created, but the specific target
  file still could not be replaced.

**Root cause**: macOS TCC guards `~/Documents` (and Desktop/Downloads/iCloud). An
unattended launchd process is denied there, and the denial surfaces as a *hang*,
not a fast error. Worse, the target file was created by another app and carried a
per-file `com.apple.macl` ACL, so it could not be overwritten even with Full Disk
Access.

**Resolution**: Preflight the *actual* output path at startup (open-for-write if it
exists, else create+rename+cleanup), bounded by a short timeout, and fail fast with
a message naming Full Disk Access and the exact binary. Write to a filename this
tool owns rather than one another app created.

**Lesson**: For unattended writes on macOS, verify writability of the real target
up front and fail fast; never assume a protected-folder write will error promptly,
and never plan to overwrite a file another app owns.

**Cost**: Hours — three full ~20-minute runs plus isolated launchd probes before
the TCC-plus-foreign-ACL root cause was pinned down — Severity: High.

## 4. Over-tight companion word bounds reject valid classifications

**Symptom**: A steady fraction of PRs came back `unverified` after exhausting all
retries, with correct bucket/action but a rejected response.

**What was tried**:
- Re-running — the same PRs failed each time on the retry cap.
- Reading the retry reason once it was logged: the companion note failed the word
  count, nothing else.

**Root cause**: The companion word bound was 3–8, but the model naturally writes a
one-line note of ~10–12 words. Every retry produced another slightly-too-long note,
so valid classifications rode the retry cap into `unverified`.

**Resolution**: Widen the default bound to 3–14 (config-driven), comfortably
covering a natural one-line note while still keeping it concise.

**Lesson**: A validation bound on model prose must fit what the model actually
produces; too tight a bound turns correct answers into failures, and the cost is
paid on every retry.

**Cost**: One misdiagnosis pass (initially attributed to a deeper systemic failure)
before the logged reason showed it was purely the word count — Severity: Medium.

## 5. Advertising a status the response vocabulary cannot satisfy

**Symptom**: Every settled row of a newly added entity type silently lost its
inferred note. No warning, no `unverified` flag — the rows simply rendered a dash,
which read as "nothing to say" rather than "the model was asked and rejected".

**What was tried**: Nothing, initially — the defect was invisible from the logs and
surfaced only when a live run's output was read row by row against the upstream
data, which showed that items *with* conversation had no note while a genuinely
empty one correctly had none.

**Root cause**: Two independently reasonable rules combined into a contradiction.
The status vocabulary offered to the model listed a terminal status as a legal
choice, while the coupling rule for that status demanded a sub-reason from a
vocabulary that was deliberately empty (the sub-reason being a hard upstream fact
the code fills in, never an inference). So the model was invited to choose a status
it could never validly return: every response proposing it failed validation, rode
out the retry budget, and degraded to the no-note fallback. The fallback did not
log, because on that path the row's status is a hard fact and *is* authoritative —
so the failure was correctly non-fatal and therefore completely silent.

**Resolution**: Make the coupling conditional on the vocabulary actually being
offered — require the sub-reason only when there is a set to draw it from, and
reject one when there is not. Separately, log the degraded path even though the row
stays authoritative, so a systematic failure cannot hide again.

**Lesson**: When a validator's required-field rules and the vocabulary it advertises
are configured independently, they can contradict each other and produce a status
that is offered but unreachable. Cross-check them: any value you advertise must be
satisfiable under the same constraints. And a fallback that is deliberately silent
because its result is still correct is exactly where a systematic fault hides — log
it anyway.

**Cost**: Caught by end-to-end verification rather than by tests, which all passed;
one diagnostic pass to trace the silent degradation to the coupling rule. Had the
live output not been read closely, it would have shipped — Severity: Medium.

## 6. Trusting unit tests to validate a deterministic fact's plumbing

**Symptom**: A newly added deterministic fact (one sourced from GitHub and applied
in code rather than inferred) passed a full green test suite, then behaved wrongly
on the first real run — once by silently degrading a whole class of rows, once by
being set on rows whose bucket made it meaningless.

**What was tried**: Relying on the unit tests written alongside the feature. They
covered each rule in isolation and all passed, because each rule *was* individually
correct; the defect lived in how a rule combined with state the test fixtures did
not reproduce.

**Root cause**: A deterministic fact threads through acquisition, classification,
persistence, and render, and its correctness depends on which real-world
combinations actually occur — a settled item with conversation, an item that aged
into a different bucket, a record written by an earlier build. Fixtures encode the
combinations the author thought of, so they systematically miss the ones they did
not; and because these facts are deliberately non-fatal (a wrong flag or a missing
note does not fail anything), nothing surfaces the miss.

**Resolution**: Treat an end-to-end run against real data as a required gate for
any deterministic fact, not an optional extra, and read the rendered output row by
row against upstream rather than scanning it. Both defects were found that way and
neither would have been found otherwise. Then add the missing combination as a
regression test, so the fixture set grows toward reality.

**Lesson**: Unit tests prove each rule right in isolation; only real data proves
the combinations right. For a fact that is plumbed across every stage and fails
silently by design, a green suite is evidence of nothing much — verify live before
believing it, and mine each live defect for the fixture it reveals.

**Cost**: Two separate defects, each costing a ~5-minute live run to detect plus a
diagnostic pass, both after a fully green suite. Each would have shipped
undetected — Severity: Medium.

## 7. Sharing one timeout bound across two structurally different providers

**Symptom**: A live run with a fallback provider configured behaved exactly as
designed — primary exhausted, fallback invoked fresh — but every fallback attempt
still ended in `unverified`, logged as a timeout rather than a validation failure.

**What was tried**:
- Hypothesized the primary itself was timing out and simply needed a longer bound
  applied everywhere. Checked the log first: no primary timeout occurred: all of
  the primary's failures were content-validation errors, answered well within
  budget. Only the fallback timed out, on every attempt.
- Ran the fallback model directly against a prompt smaller than a real event
  trail. It took 77 seconds wall-clock — already close to the shared 90-second
  bound before accounting for a full-size real prompt.

**Root cause**: The primary (a session-based harness) and the fallback (a
one-shot local model) were bound by the same single timeout value. The value was
sized for the primary's response profile; the fallback's genuinely different
profile — slower per call on this hardware/model — had no bound of its own to be
sized correctly, so it inherited a number that was never chosen with it in mind.

**Resolution**: Give the fallback its own independently configured timeout,
distinct from the primary's, defaulting to a multiple of it. Leave the primary's
own bound untouched, so an ordinary hung primary session still fails over at its
existing speed.

**Lesson**: When two structurally different backends sit behind one interface,
a shared numeric bound (timeout, retry cap, size limit) is an assumption that one
of them was sized for — verify it against both, not just the one it was
originally tuned for, before trusting it to serve either.

**Cost**: One live run's classify phase (~5 minutes) spent entirely on repeated
fallback timeouts, plus a round of log analysis and a direct manual timing test to
separate "primary hung" from "fallback is just slower than budgeted" before the
real, narrower fix was implemented and re-verified live — Severity: Medium.

## 8. Deleting a handed-off file before the harness's asynchronous read of it

**Symptom**: A large fraction of open PRs across many runs came back with
malformed or empty classification fields from the primary provider — an empty
`action`, an invalid enum value — misattributed at first to model unreliability
or session degradation, since the responses looked like ordinary bad LLM
output rather than a systematic bug.

**What was tried**:
- Investigated as a fallback-timeout problem (ANTI-PATTERNS #7) — real, but
  independent, and fixing it did not stop the primary's own failures.
- Assumed the failures were the known cost of a heavyweight session harness
  degrading mid-run, since the tool already has machinery (retry, fallback,
  `unverified`) built to absorb exactly that.
- Root cause was found only by attaching directly to the harness's own `tmux`
  session and reading its transcript: it showed the harness's `Read` tool
  reporting the prompt file did not exist, moments after being told to read
  it — first evidence this was a file-handoff defect, not a model judgment
  problem.

**Root cause**: The prompt file was deleted by a `defer os.Remove(...)` scoped
to the function that *sends* the file-handoff instruction, which returns as
soon as the instruction is typed into the terminal — not once the harness has
actually processed the turn. But the harness's own read of that file happens
asynchronously afterward, as the model works through the turn at its own
pace. So the file was reliably gone by the time the harness got around to
reading it, and the harness reported a fact ("no such file") the code then
treated as an ordinary bad classification.

**Resolution**: Move the file's removal out of the send step entirely. The
send step now returns the file's path instead of deleting it; the caller
removes it only after the turn has fully concluded — the verdict/summary was
collected or the turn was given up on — so the file survives for exactly as
long as the harness might still be reading it.

**Lesson**: When one step hands a resource to an asynchronous consumer and a
later, separate step is what proves the consumer is done with it, tie the
resource's cleanup to that later step — never to the hand-off step's own
return, which only proves the hand-off was *sent*, not *consumed*. A `defer`
placed for tidiness at the point something is created is exactly how this
kind of premature cleanup hides: it reads as correct locally and only fails
against the real asynchronous timing.

**Cost**: The defect was live for the whole session before discovery — every
live-verification run in that window mischaracterized its own primary
failures as model/session unreliability rather than a plumbing bug, which in
turn justified building and tuning the fallback feature's trigger conditions
around a partly wrong picture of why primary was failing. Found only by an
operator directly inspecting the harness's raw transcript, not by any test or
log analysis — the logged error was truthful ("file not found") and gave no
hint that the caller's own code had deleted the file that fast — Severity:
High.

## 9. Free-text model output extracted with only TrimSpace, no thinking-trace guard

**Symptom**: A live desktop notification showed a chunk of a model's step-by-step
reasoning about the notification prompt itself — restating its own constraints
("Constraint 1: exactly one short sentence...") — instead of a one-line summary.
Reported by the operator as "sending the prompt to me as the notification."

**What was tried**: Nothing needed trying — the mechanism was reproducible in one
shot by piping the exact prompt text notify.go sends into the fallback model
(`ollama run glm-4.7-flash:latest`) and reading its raw stdout directly.

**Root cause**: notify.Summarize's fallback slot is the same one-shot "thinking"
model whose narration-before-answering behavior provider/parse.go's `extractJSON`
already has dedicated handling for on the classification path (search for Ollama's
own `...done thinking.` convention). `notify.extractSummary` was written
independently and never got that handling — it only tried a JSON parse, then fell
back to `strings.TrimSpace` on the *entire* raw response. Every earlier Summarize
test used a clean canned string, so nothing exercised what a real thinking model's
stdout actually looks like.

**Resolution**: Give `extractSummary` the same `...done thinking.` cutoff
`extractJSON` uses, and add a `plausibleSummary` backstop behind it (length cap,
plus a small set of prompt-echo fingerprint substrings) so a candidate that still
looks like leaked reasoning — marker present but the model kept narrating past it,
or the marker absent entirely — is rejected as a failed attempt rather than
returned, falling through to the fallback provider or the plain non-model string
exactly as any other Summarize failure does.

**Lesson**: A one-shot "thinking" model's raw-stdout quirk is a property of the
*model*, not of the call site — every extraction function reading that model's
output needs the same guard, and adding a second call site (here, a summary
prompt, distinct from the classification prompt `extractJSON` was built for)
without carrying the guard along re-opens a bug that was already fixed once
elsewhere in the same codebase. When two functions parse output from the same
kind of model, that handling belongs in one place both can reach, or each new
call site needs an explicit reminder to check whether the existing handling
already covers its case.

**Cost**: Live in production notifications for an unknown number of prior runs
before the operator noticed and reported it (the log evidence shows the fallback
path — and therefore this extraction code — firing on essentially every run this
session, each one a chance to leak). Severity: Medium (cosmetic/confusing, not a
data-integrity defect — the underlying classification and store were never
affected, only the best-effort notification text).

## 10. A killed (not returned) process orphans its tmux harness session

**Symptom**: `tmux list-sessions` showed `GSB-Harvester-*` sessions days old — one
run's worth, all created within the same second, matching a `ClassifyWorkers`
pool. The tool itself was not running at the time (`launchctl print` showed
`state = not running`); nothing currently alive had any relationship to those
sessions.

**What was tried**: Nothing needed trying to find them — `tmux list-sessions`
plus each session's own creation timestamp was enough to see they predated the
current run by days. The real work was tracing *why* the pool's own `Close()`
(TDD 6.7), which does correctly `tmux kill-session` every member, had not run
for that particular invocation. Checking the operator's own action log for that
timestamp found a `launchctl bootout` issued minutes into that run, to reload
the launchd agent after an unrelated schedule-config edit.

**Root cause**: `defer`-based cleanup — `main`'s `defer closer.Close()` on the
provider, and the pool's own concurrent `Close()` on each member — only runs
when the function it is scoped to actually returns. `launchctl bootout` on a
still-running job sends it a kill signal; the process had no signal handling of
any kind, so it died immediately wherever it happened to be (mid-classification,
in this case), and every `defer` between that point and `main` never got a
chance to run. The tmux sessions those `defer`s would have killed were left
exactly as they were, with no other code anywhere aware they existed once their
owning process was gone.

**Resolution**: Two layers, aimed at the two different ways a process can stop
without returning normally.
1. `main` now installs `signal.NotifyContext` for SIGTERM/SIGINT, cancelling the
   run's context instead of leaving the process to die on the signal outright —
   a caught signal becomes a normal (if early) return through the same path
   that already reaches every `defer`, `launchctl bootout` included.
2. SIGKILL cannot be caught by any program, so as a backstop, a fresh
   session-kind provider now sweeps and kills any pre-existing
   `GSB-Harvester-*` tmux session older than a generous age floor before
   creating its own (this tool is single-shot and not designed to run two
   instances at once, so any such session already alive at that point cannot
   legitimately belong to a still-active run).

**Lesson**: `defer`-based cleanup is only as reliable as the assumption that the
function it is attached to gets to return — true for a normal error path, false
for anything that kills the process out from under it, and an unattended
scheduled job is exactly the context where an operator (or the scheduler
itself, reloading its own config) is most likely to do that without warning.
A resource whose lifetime is tied to an external, unmanaged process (a tmux
session, in this case) needs either signal handling to make "killed" behave
like "returned," or an independent sweep that can clean up after the case
signal handling cannot cover — ideally both, since neither alone is complete.

**Cost**: Found by operator inspection of `tmux list-sessions`, not by any log
or test — the orphaned sessions produced no error, no log line, and no visible
symptom in the tool's own output; they were simply idle processes consuming
resources indefinitely until someone happened to list tmux sessions and notice
the stale timestamps. Severity: Medium (resource leak, not a data-integrity or
classification-correctness defect — but unbounded over time on a job that runs
every 20 minutes for hours a day).
