# Development Policy

## Build

- Build with `go build ./...`. Requires a current Go toolchain (see `go.mod` for
  the minimum version).
- The deliverable is a single self-contained binary. It must run from a fixed path
  with no interpreter, virtual environment, or runtime alongside it.
- Do not require CGO. Build must succeed with `CGO_ENABLED=0` so the binary is
  statically linked and portable across the target platforms.

## Build pipeline

Run these in order after every change; a change is not complete until all pass:

1. `gofmt -l .` — must report no files (formatting is clean).
2. `go vet ./...` — must be clean.
3. `go build ./...` — must compile.
4. `go test ./...` — all tests pass.

## Testing

### Unit tests

- The deterministic core must be unit-tested without network or provider access:
  classification floors, close-sub-reason and priority enum handling, the YAML
  read/merge/write (including operator-state preservation), the Markdown render,
  and the provider-response parser and word-bound validator.
- The provider interface is tested against a fake implementation — never a live
  model — so tests are deterministic and offline.
- Run with `go test ./...`.

### Manual integration testing

- Integration tests trace back to TDD.md rubrics — review the rubric before writing
  or changing a test; a test that maps to no rubric is probably a unit-test concern.
- After changes to acquisition, classification, or the provider hand-off, verify
  end-to-end against a real GitHub account and the configured provider, and confirm
  the rendered Markdown matches the established structure.

## Input and output sanitization

- Treat all GitHub API responses and all provider responses as untrusted input.
- Every provider response is validated before use: status, close-reason, and
  priority must be members of their finite enums, and companion prose must satisfy
  the configured word bounds (TDD 6.4, 6.6). An invalid response drives the
  self-correcting retry, then the `unverified` fallback — it is never rendered as-is.
- Reading a malformed YAML store aborts the run; the tool never overwrites or
  "repairs" a store it cannot parse (TDD 2.4).

## Code style

- Format with `gofmt`; keep `go vet` clean. Handle every error explicitly — no
  discarded error returns outside genuinely infallible calls.
- No magic numbers or magic strings. A value carrying domain meaning is a named
  constant or a config value; a closed set of values is a typed enum parsed once at
  the boundary that produces it (status, close-reason, priority).
- Configuration values — word bounds, retry cap, stale age threshold, provider
  selection — live in config, never as literals in logic.
- Logging goes through one structured logging facility. Log the decision, the PR
  identifier, and counts — never PR body text, comment contents, or credentials.

## Dependencies

- Do not add a dependency without justification. Check TECH.md's package table for
  something that already covers the need first. Pin versions in `go.mod`.
- Existing dependencies and their roles are listed in TECH.md § Third-party
  components. GitHub access uses those clients — do not shell out to the `gh` CLI
  for data acquisition.

## Architecture rules

- **GitHub is read-only.** No code path may issue a state-mutating GitHub call. The
  acquisition layer exposes no mutating operation (TDD 1.3).
- **The YAML store is the single source of truth.** The Markdown is a pure render
  of it and is never hand-edited or read back as input (TDD 3.3). All store access
  goes through the `store` module.
- **Merged and closed-unmerged are immutable floors.** No provider response or
  inference may reclassify a merged or closed-unmerged PR (TDD 4.1, 4.2). Enforce
  this in code, independent of provider output.
- **The provider is pluggable and never core.** All model access goes through the
  provider interface. Two invocation strategies exist behind it: a **one-shot**
  provider spawns a fresh subprocess per request and delivers the prompt on stdin
  (Ollama-style, and any cheap-to-start backend); a **session** provider keeps one
  long-lived interactive harness and reuses it across all PRs, amortizing the
  harness's heavy startup cost (Kiro, Claude, Grok). Which strategy a provider
  uses is fixed by its name in one closed, code-owned mapping — never an
  independently configured value, so a name can never be paired with the wrong
  strategy. There is no raw-command override for either strategy: the argv shape
  per name is fixed in code, parameterized only by the selected model, so adding
  a provider means adding a mapping entry and an argv builder, not exposing a new
  escape hatch. A **fallback** provider slot, when configured, is built through
  the identical path as the primary and invoked only once the primary's own retry
  budget is exhausted, bounded by its own independently configured timeout (TDD
  6.11–6.17).
- **The classification session enables exactly two deliberately-introduced,
  tightly-scoped tools — and no others.** A session provider hands the harness one
  PR at a time via a file (the event trail is too large to type into the session:
  a terminal's command-length limit truncates it), so the session profile enables
  precisely: (1) a **read tool scoped to the prompt directory only**, so the
  harness can read that per-PR input file and nothing else; and (2) the **verdict
  MCP sink**, an inbound HTTP tool through which the harness returns its structured
  classification instead of the output being scraped from the terminal. Both are
  ratified by design and both are read-only with respect to GitHub — the read tool
  is path-restricted to a temporary local directory, and the verdict sink only
  receives the verdict; neither issues a GitHub call or widens the read-only
  surface. No other tool (no write, no shell, no network, no unrelated MCP server)
  may be enabled on the classification session, and the prompt file is deleted once
  the verdict is collected.
- **The core is platform-independent.** OS-specific concerns (scheduler
  integration, paths) are isolated in the `schedule` seam. No macOS-only dependency
  may reach the core; the same binary must run under Linux cron unchanged.
- **Prefer extending an existing path over adding a parallel one.** Before adding a
  new function or pipeline pass, look for one that already does the equivalent work.

## Prohibited actions

- Do not use `sudo` in build, test, or run commands — the agent cannot enter a
  password and the command will hang.
- Do not issue any mutating GitHub request (comment, review, label, merge, close),
  under any circumstance — this tool only reads.
- Do not log credentials or document/comment content (see Code style).
- Do not commit to `main` on the agent's own initiative; the operator performs
  commits.

## Deployment

- Deploy the single built binary to a fixed path and invoke it on a schedule. The
  MVP scheduler is macOS launchd; Linux cron is a supported target.
- The GitHub token and provider configuration are supplied via environment and
  config; no secret is committed or embedded in the binary.

## Debug output

- Use the one structured logging facility for all output; control verbosity by a
  flag/level, not by adding and removing print statements.
- The tool runs unattended, so it must never prompt for input; it logs and exits
  with a meaningful status instead.
