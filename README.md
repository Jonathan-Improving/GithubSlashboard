# GithubSlashboard

A read-only GitHub dashboard generator that turns your pull requests and issues
into a single, always-current Markdown status document — using a language model to
read each item's history and report its *real* status, not just its stale flags.

## What it does

GithubSlashboard fetches the pull requests you author and the ones you're asked to
review, plus the issues you open and take part in, records them in a local YAML
file, and renders that file into a clean Markdown status document — a summary table
plus role-split, status-bucketed detail sections. Because GitHub's own flags often
lag reality (a reviewer requests changes, then says "hold off" and never clears the
flag), it hands each item's chronological event trail to a language model to judge
the true status, add a short note, and flag anything urgent. It runs unattended on
a schedule and never writes to GitHub.

Issues are treated as the simpler entity they are: no merge, a close reason taken
straight from GitHub's own state reason rather than inferred, a longer staleness
horizon, and no model call at all for an issue nobody has commented on — there is
nothing there for a model to read.

## Features

- **True status, not stale flags**: reads each item's comment and review history to
  determine what's actually going on, overriding flags nobody remembered to clear.
- **Pull requests and issues in one document**: PRs split by role and status, then
  issues split by whether you opened them or joined in, with settled issues
  archived under a Closed section.
- **YAML source of truth**: all state lives in one auditable YAML file; the Markdown
  is a pure render of it, regenerable and diffable.
- **Read-only by design**: only ever reads GitHub — it can never comment, review,
  label, close, or merge.
- **Deterministic where it counts**: statuses come from finite, machine-readable
  vocabularies, so the YAML can feed other tooling; merged and closed states, an
  issue's close reason, and a failing build on your own PR are hard facts a model
  can't override or forget to mention.
- **Pluggable model provider**: uses a cheap local model via CLI (Kiro CLI for the
  MVP); Grok-, Claude-, and Ollama-style providers drop in without code changes.
- **Priority flagging**: highlights items where the history signals urgency.

## Requirements

| Requirement | Details |
|-------------|---------|
| OS | macOS (MVP, via launchd). Linux (cron) is a supported target. |
| Runtime | A single self-contained Go binary — no interpreter or virtualenv. |
| GitHub | A token with read access to the relevant repositories. |
| Provider | A configured LLM provider CLI (Kiro CLI for the MVP). |

Windows is not a target. The tool writes only to its local YAML store and the
Markdown output — never to GitHub.

## Quickstart

```bash
git clone <repo-url> GithubSlashboard
cd GithubSlashboard
go build ./...
export GITHUB_TOKEN=...        # read-only token
./githubslashboard             # fetch, classify, render
```

Schedule it (launchd on macOS, cron on Linux) to refresh the status document
unattended.

## Configuration

Paths and behavior are set by environment variables, with command-line flags
overriding them for a single run (flag > environment > default).

`<data dir>` is your platform's conventional per-user application-data directory,
resolved at run time — `$XDG_DATA_HOME/github-slashboard` (falling back to
`~/.local/share/github-slashboard`) on Linux, and
`~/Library/Application Support/github-slashboard` on macOS. Missing parent
directories are created automatically. The filenames are fixed; point the settings
below at any location you prefer.

| Setting | Environment variable | Flag | Default |
|---------|----------------------|------|---------|
| Rendered Markdown path | `GSB_OUTPUT_PATH` | `-output` | `<data dir>/GSB-SlashBoard.md` |
| YAML store path | `GSB_STORE_PATH` | `-store` | `<data dir>/prs.pr.yaml` |
| Read-only GitHub token | `GITHUB_TOKEN` | — | _(required)_ |
| Provider selection | `GSB_PROVIDER` | — | `kiro` |
| Provider strategy | `GSB_PROVIDER_KIND` | — | `session` |
| Infer notes for settled items | `GSB_CLASSIFY_FLOOR_NOTES` | — | `true` (merged/closed PRs and closed issues get notes) |
| Re-crawl settled items already in the store | `GSB_INCLUDE_TERMINAL` | `-include-terminal` | `false` (skip them, reuse cached record) |
| PR stale age threshold | `GSB_STALE_AGE_THRESHOLD` | — | `960h` (40 days) |
| Issue stale age threshold | `GSB_ISSUE_STALE_AGE_THRESHOLD` | — | `2880h` (120 days) |
| Verbose logging | — | `-verbose` | off |

For example, to write the status document to a location of your choosing:

```bash
export GITHUB_TOKEN=...        # read-only token
export GSB_OUTPUT_PATH="$HOME/reports/github-status.md"
./githubslashboard
```

By default the tool infers a note (and, for closed PRs, a close sub-reason) for
every tracked item, including settled ones, so no row reads "unavailable". This
makes an unattended run longer, since every item consults the model. Set
`GSB_CLASSIFY_FLOOR_NOTES=false` to skip the model for merged and closed PRs and
closed issues — their bucket is a settled fact — classifying only live items for a
faster run at the cost of notes on those terminal rows.

One exception is not configurable: an issue with **no comments** is never sent to
the model, whatever this setting says. Its trail is just "opened", so there is
nothing for a model to read and a note would be invented rather than summarized.
Such a row reports its state as a plain fact and is not marked unverified.

## Examples

The [`examples/`](examples/) directory has a rendered
[sample document](examples/sample-output.md) showing what the tool produces, a
[provider setup guide](examples/README.md) covering the two invocation strategies,
and [scheduling artifacts](examples/scheduling/) for launchd and cron.

## Documentation

Detailed documentation lives in the `sdd/` directory:

- `sdd/PRODUCT.md` — Product definition, rationale, and vocabulary
- `sdd/TECH.md` — Technical architecture and system diagram
- `sdd/TDD.md` — Behavioral test rubrics
- `sdd/POLICY.md` — Development rules and constraints

## License

Licensed under the [MIT License](LICENSE).
