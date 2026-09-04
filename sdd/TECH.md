# Technical Architecture

## System overview

![System overview: a scheduler triggers acquisition, which reads pull requests, issues, and their event trails from GitHub; classification applies deterministic floors and consults an LLM provider subprocess to judge open-item status from the event trail; results persist to a YAML source of truth holding !pr and !issue documents, which a pure renderer turns into the Markdown status document; when at least one item needed a fresh judgment this run, an optional notification hook asks the same provider for a one-sentence summary and delivers a JSON payload to a configured external command.](system-overview.svg)

GithubSlashboard is a single-shot command-line program written in Go. One
invocation runs a linear pipeline — acquire → classify → persist → render →
notify — then exits. The notify step is optional and best-effort: it fires
only when at least one item needed a fresh judgment this run, and a failure
there never fails the pipeline. It holds no long-lived process and no server.
See PRODUCT.md for what the product is and why.

## Third-party components

### Go packages

| Package | Role |
|---------|------|
| `github.com/google/go-github` | Native GitHub REST client for fetching PRs, reviews, comments, and timeline events. |
| `golang.org/x/oauth2` | Bearer-token transport for authenticating the GitHub client. The same authenticated transport also carries hand-rolled read-only GraphQL queries (review-thread resolution state), so no separate GraphQL client is a dependency. |
| `gopkg.in/yaml.v3` | YAML encode/decode with custom `!pr` tag support for the source of truth. |

Exact versions are pinned in `go.mod` (see POLICY.md — dependencies are pinned and
justified). The GitHub token is read from the environment, never stored.

### System dependencies

| Component | Role |
|-----------|------|
| A configured LLM provider | Performs event-trail judgment. Invoked as a subprocess (one-shot) or a reused interactive session, never linked. MVP: the Kiro CLI (`kiro-cli` / `q`). |
| `tmux` | Terminal multiplexer that hosts a session provider's long-lived harness. Required only when the configured provider name resolves to the session invocation kind (currently `kiro`); a one-shot name (currently `ollama`) does not use it. |
| A scheduler | Runs the binary unattended. macOS launchd for the MVP; Linux cron is a supported target with no core changes. |

The provider and scheduler are both external and both isolated behind seams (see
below) so neither is a compile-time dependency of the core.

### Integration boundaries

**GitHub (read-only).** The acquisition layer reads PRs, their review
submissions, issue comments, timeline state-transitions, labels, and — for an
open PR — the current CI/check-run outcome on its head commit plus its
unresolved review threads and overall review decision, and composes
them into one chronological event trail per PR. Everything is fetched over REST
except review-thread resolution state, which GitHub exposes only via GraphQL; a
read-only GraphQL query rides the same authenticated transport. Nothing crosses
this boundary in the write direction — the read-only guarantee (TDD 1.3) is an
architectural invariant, enforced by the acquisition layer exposing no mutating
call. To avoid
spending calls on settled work, acquisition consults the prior store first and,
by default, skips crawling any PR the store already records as terminal
(merged/closed), carrying its cached record forward; a config toggle forces a
full crawl for a deliberate deep run (TDD 1.5).

The same layer acquires the operator's **issues** — authored, commented on, or
assigned — in the same read-only pass (TDD 8.1), assembling each one's trail from
its comments and timeline state transitions. Issues carry none of the PR review
machinery, so nothing here fetches mergeability, check-runs, review threads, or a
review decision for them, and no GraphQL is involved. GitHub's own `state_reason`
is captured for a closed issue, and the comment count is captured because it
decides whether the item is worth a model call at all. Closed issues are subject
to the same terminal-skip.

**Provider.** Classification hands the provider one item's event trail and
receives a strict, parse-friendly structure back (status, companion prose,
priority, stale/close-reason judgment). The hand-off is generic over the entity
being judged: the response's vocabulary is not fixed by the transport but supplied
per request, so one provider path, parser, and retry loop serve both pull requests
and issues while each entity's enum stays a tight closed set. Two invocation
strategies sit behind the provider interface. A **one-shot** provider delivers the
prompt on a fresh subprocess's stdin and reads its stdout (Ollama-style, and any
cheap-to-start backend). A **session** provider launches one heavy harness (Kiro,
Claude, Grok) as a long-lived interactive process under tmux and reuses it for
every PR, amortizing the harness's large startup cost. Which strategy a provider
uses is fixed by its name — a closed, code-owned mapping, not an independent
configuration axis — so a name can never be paired with the wrong strategy. It
hands the harness each
PR *by file* — the event trail is too large to type into the session, where a
terminal command-length limit would truncate it — writing the prompt to a
temporary file and sending only a short instruction to read that path; the
harness reads it with a read tool scoped to that directory, and the file is
deleted once the verdict is in. It receives the verdict *structurally* — the
harness calls a single provisioned MCP tool (the verdict sink, a loopback
streamable-HTTP server the provider hosts) whose arguments are the JSON verdict,
so no terminal output is scraped. Either way the boundary is bounded by a timeout
and a bounded retry loop (TDD 6.4, 6.5); a provider that fails or times out yields
an `unverified` row rather than blocking the pipeline. The read tool is
path-restricted and the verdict sink is inbound only — neither issues a GitHub
call nor widens the read-only surface.

An optional **fallback** provider, built through the identical construction path
as the primary, is invoked only after the primary's own retry budget is fully
exhausted (TDD 6.11–6.17). It is bounded by its own independently configured
timeout rather than the primary's, since a one-shot fallback backed by a local
model can legitimately need longer per call than a session-based primary. Which
provider slot produced an item's current judgment is recorded on the persisted
record, never left implicit. For
the request/response and verdict-tool field reference, see SCHEMA.md.

**YAML store.** The persistence layer is the only reader and writer of the source
of truth. Each PR is one `!pr`-tagged YAML document and each issue one
`!issue`-tagged document, coexisting in the same stream; the tag is the extension
point that lets further entity types be added without restructuring, and any tag
this build does not recognize is round-tripped verbatim (TDD 2.5, 8.7).
Operator-set fields survive re-runs (TDD 2.2). For the document field reference,
see SCHEMA.md.

**Notification hook (optional, outbound).** After classification, `notify`
collects every open PR/issue that reached the provider for a fresh judgment
this run and, when that set is non-empty, asks the same provider for a short
summary sentence (`Summarize` — a distinct, simpler contract than per-item
judgment, TDD 6.9) and writes a JSON payload to the stdin of a configured
external command (`GSB_NOTIFY_HOOK`), bounded by a timeout. This is the one
outbound integration point beyond GitHub reads and the provider call, and it
is deliberately the last thing the pipeline does — after the status document
is already written, so a failing or slow hook never blocks or corrupts the
dashboard's own output (TDD 9.5). Every model-derived field in the payload is
sanitized to a closed natural-language-plus-emoji character set before it is
used, both when it enters the summary prompt and again at the payload
boundary, so a downstream consumer that blindly forwards the JSON to a shell
cannot be tricked into command injection by adversarial GitHub content
(TDD 9.6). Unconfigured, the mechanism is entirely inert.

## Module responsibilities

| Module | Responsibility |
|--------|----------------|
| `main` | Entry point; parses flags/config, sequences the acquire→classify→persist→render pipeline, sets the exit status. |
| `config` | Loads and validates configuration; owns the named constants (word bounds, retry cap, the PR and issue age thresholds, provider selection). |
| `github` | Owns all GitHub access; fetches the tracked PR and issue sets and assembles each item's event trail (REST, plus a read-only GraphQL query for PR review-thread resolution state), skipping the crawl for items the prior store already records as terminal (TDD 1.5, 8.1). Exposes read-only operations only. |
| `model` | Owns the domain types: the PR and issue records, each entity's closed-set enums, and the shared event-trail types. |
| `classify` | Applies the deterministic floors (PR merged/closed-unmerged, issue closed) and orchestrates provider judgment for open-item disposition, priority, and companion prose. Skips the provider entirely for an issue with no conversation to judge (TDD 8.3), and for an open PR whose deterministic inputs are unchanged since the prior stored record, which it is constructed with (TDD 4.13). |
| `provider` | Owns the provider interface, response parsing, and the self-correcting retry loop. The request/response contract is generic over entity, with the valid vocabulary supplied per request. Two invocation strategies live behind the interface: a one-shot subprocess provider (prompt on stdin) and a session provider that reuses one long-lived harness under tmux and receives the structured verdict through a local MCP sink; which strategy a given provider name uses is a closed, code-owned mapping, not a separately configured value. An optional fallback provider — built through the same construction path as the primary — is invoked only once the primary's own retry budget is exhausted, bounded by its own independent timeout. A second, simpler contract (`Summarize`) produces the notification hook's summary sentence — a plain prompt in, a plain string out, no vocabulary to validate, no retry loop (TDD 6.9); the session provider's sink offers its verdict and summary tools mutually exclusively per turn, switching which one is advertised before each call rather than exposing both at once (TDD 6.10). Kiro CLI is the MVP session harness; Ollama is the MVP one-shot backend. |
| `store` | Owns the YAML source of truth: read, validate, merge (preserving operator-set state), and write `!pr` and `!issue` documents, preserving unrecognized tags verbatim. |
| `render` | Pure function from the store to the Markdown status document; owns the layout that reproduces the established structure and the issues sections appended below it. |
| `notify` | Owns the notification hook: collects the open PRs/issues that reached the provider for a fresh judgment this run, asks the provider (via `Summarize`) for a one-sentence summary, and delivers the resulting JSON payload to a configured shell command's stdin. Sanitizes every model-derived text field before it is used, both preventatively (before it enters the summary prompt) and at the payload boundary (TDD 9.6), so a downstream consumer cannot be tricked into command injection by GitHub-sourced content. Best-effort and optional: inert when unconfigured, non-fatal on failure. |
| `schedule` | Platform seam for unattended execution (launchd/cron artifacts) and for resolving the host's conventional per-user data directory; isolated so the core carries no OS-specific dependency. |

## Concurrency model

The pipeline is sequential by default: one process, one pass, then exit. The
store's prior records are read once, up front, and flow into two independent
consumers: `github`'s acquisition pass (to skip re-crawling a terminal item, TDD
1.5, 8.1) and `classify`'s construction (to skip the provider call for an open
PR whose deterministic inputs are unchanged, TDD 4.13) — both read-only uses of
the same map, never written to by either stage. The only concurrency is optional
fan-out within the `classify` stage — independent PRs may
have their provider calls issued in parallel, since each PR's judgment depends only
on its own event trail and shares no mutable state. Any such parallelism is bounded
by a worker limit and every provider call is individually timeout-bounded; results
are collected before the single-threaded persist and render stages run.

How that fan-out is served depends on the provider strategy. The one-shot provider
simply spawns concurrent subprocesses. The session provider cannot — one harness
serves one turn at a time — so it runs a **pool** of that many independent harness
sessions (each its own tmux session and verdict sink), and each concurrent
classification checks out a free session, uses it, and returns it. A single session
serializes its own turns (a mutex), while the pool provides the cross-session
parallelism; this is what keeps a large classify within its scheduled window.
A configured fallback provider is a wholly separate instance from the primary's
pool — its own harness/subprocess, sized to one instance rather than the worker
count, since it is invoked only on primary exhaustion rather than on every item.
There is no shared mutable state across the pipeline stages — each stage consumes the
prior stage's output and produces the next stage's input. The notification
hook, when it fires, runs after render as one final sequential step — it is
not part of the classify stage's fan-out and makes at most one additional
provider call (the summary) plus one hook subprocess, both bounded by their
own timeouts.
