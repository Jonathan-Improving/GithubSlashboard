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
