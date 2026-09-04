# Examples

Worked examples for getting GithubSlashboard running.

| File | What it is |
|------|-----------|
| [`sample-output.md`](sample-output.md) | A rendered status document, produced by the real renderer from fictional data. Start here to see what the tool produces. |
| [`scheduling/run-githubslashboard.sh`](scheduling/run-githubslashboard.sh) | Wrapper script that supplies the environment and fetches the token at run time. |
| [`scheduling/com.example.githubslashboard.plist`](scheduling/com.example.githubslashboard.plist) | launchd agent for macOS. |
| [`scheduling/crontab.example`](scheduling/crontab.example) | crontab entries for Linux. |
| [`notify-hook/notify-desktop.sh`](notify-hook/notify-desktop.sh) | `GSB_NOTIFY_HOOK` target that turns a notification payload into a real desktop notification. |

---

## Choosing and configuring a provider

The tool consults a language model to judge each item's real status from its event
trail. Everything else — fetching, the immutable merged/closed floors, the
deterministic flags, persistence, rendering — is plain code that runs without a
model. The provider is only asked for the judgment that genuinely needs one.

Two invocation strategies sit behind one interface, selected with
`GSB_PROVIDER_KIND`.

### `oneshot` — start here

A fresh subprocess per item. The prompt goes in on stdin, the reply comes back on
stdout. Any CLI that reads a prompt and prints a response works, which makes this
the easiest way to get a first run out of the tool.

```bash
export GSB_PROVIDER_KIND=oneshot
export GSB_PROVIDER_CMD="ollama run llama3.2"
```

`GSB_PROVIDER_CMD` is split on whitespace; the first word is the executable and the
rest are fixed leading arguments. There is no shell involved, so quoting and pipes
are not interpreted.

The trade-off is process startup: every item pays it. For a local model that starts
in milliseconds this is irrelevant. For a heavyweight agent harness that takes
several seconds to boot, it dominates the run.

### `session` — for heavyweight harnesses

One long-lived interactive harness, reused across every item, so its startup cost is
paid once. This is the default, because the reference provider is an agent CLI with
exactly that cost profile.

```bash
export GSB_PROVIDER_KIND=session   # the default
export GSB_PROVIDER=kiro
```

A session provider is more involved than a one-shot, and worth understanding before
choosing it:

- It hosts the harness inside a **`tmux`** session, so `tmux` must be installed.
- Each item's event trail is handed over **by file**, not typed into the session — a
  large trail would exceed the terminal's command-length limit and be silently
  truncated.
- The verdict comes back **structurally**, through a Model Context Protocol tool
  the provider hosts on a loopback address, rather than by scraping terminal
  output. A second, separate tool returns a notification summary sentence
  (below) — the two are mutually exclusive per turn, so the harness is never
  offered a choice between them.
- The harness is given exactly three capabilities: a read tool scoped to the
  prompt directory, the verdict tool, and the summary tool. Nothing else — no
  shell, no network, no unrelated servers.

Concurrency works by running a pool of independent sessions, since one harness
serves one request at a time. The pool is sized to the classifier's worker limit,
which is a compiled default rather than a configurable setting.

### Provider-related settings

| Setting | Environment variable | Default |
|---------|----------------------|---------|
| Provider selection | `GSB_PROVIDER` | `kiro` |
| Invocation strategy | `GSB_PROVIDER_KIND` | `session` |
| Explicit command (argv) | `GSB_PROVIDER_CMD` | provider default |

Whatever the strategy, every call is bounded by a timeout and a bounded
self-correcting retry: a response that fails validation is quoted back to the model
once or twice, and a call that keeps failing or times out yields a row marked
`⚠️ unverified` rather than stalling the run or inventing a status.

### Writing your own provider

The interface has two methods: classify one item (take a request, return raw
text) and summarize a set of changes (take a plain prompt, return a plain
sentence — see [Notification hook](#notification-hook) below). Adding a
backend means supplying an argv, not writing code. If a CLI can read a prompt
and print a JSON object, `GSB_PROVIDER_CMD` is all it needs. See
`sdd/SCHEMA.md` for the exact request and response contracts, and
`sdd/TECH.md` for where the boundary sits.

---

## Notification hook

Beyond the status document, the tool can prompt you to look at it: after a run
in which at least one open PR or issue actually changed enough to need a fresh
judgment, it asks the provider for a one-sentence summary and writes a small
JSON payload to a command of your choosing.

```bash
export GSB_NOTIFY_HOOK="$PWD/examples/notify-hook/notify-desktop.sh"
```

Unset (the default), the mechanism is entirely inert — no summary is
requested, no process is spawned. See `sdd/SCHEMA.md`'s Notification hook
section for the exact payload shape, and
[`notify-hook/notify-desktop.sh`](notify-hook/notify-desktop.sh) for a worked,
runnable example that turns the payload into a real desktop notification
(`terminal-notifier` on macOS, `notify-send` on Linux).

---

## First run

```bash
go build ./...
export GITHUB_TOKEN=...              # read-only token
export GSB_PROVIDER_KIND=oneshot
export GSB_PROVIDER_CMD="ollama run llama3.2"
./githubslashboard -verbose
```

`-verbose` is worth using the first time: it logs each phase and its duration, so
you can see where the time goes before scheduling anything.

Two things to know about a first run:

- **It reads only.** No code path issues a mutating GitHub call, so nothing can be
  commented, labelled, closed, or merged by accident.
- **It can take a while.** Every tracked item is fetched and, by default, classified.
  Set `GSB_CLASSIFY_FLOOR_NOTES=false` to skip the model for settled items — their
  status is a hard fact either way — which trades notes on those rows for a faster
  run.

Once a run produces a document you are happy with, use the scheduling examples above
to have it refresh unattended.
