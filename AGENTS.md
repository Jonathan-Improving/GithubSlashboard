# AGENTS.md

GithubSlashboard: a read-only Go tool that generates a Markdown status dashboard of
the operator's GitHub pull requests and issues, using an LLM to judge each item's
true status from its history.

## SDD skill

This project uses Spec-Driven Development (SDD). If you have access to the SDD
skill, load it before taking any action on this project — it governs how to read,
write, and maintain all project documentation. If the skill is unavailable, read
the files in `sdd/` directly.

## Documentation

Project documentation lives in the `sdd/` directory.

### If you are exploring this project

Read these files first:
- `sdd/PRODUCT.md` — What this project is, why it exists, and its vocabulary
- `sdd/TDD.md` — Behavioral contract (test rubrics in Given/When/Then format)

### If you are building, running, or testing this project

- `sdd/POLICY.md` — Build commands, toolchain, build pipeline, verification steps,
  and prohibited actions. Read it in full before running anything. Do not infer the
  build from `go.mod` alone — where this project's rules differ from a standard Go
  build, POLICY.md is the only place that says so.

### If you are changing code in this project (fixing, adding, refactoring)

Read all of the above, plus:
- `sdd/TECH.md` — Technical architecture, dependencies, and module responsibilities
- `sdd/SCHEMA.md` — The exact contracts: the `!pr` and `!issue` YAML document
  shapes and the provider request/response structure. Read it before touching the
  store, the renderer, or the provider hand-off.
- `sdd/ANTI-PATTERNS.md` — Costly traps already hit and resolved (scan its TOC;
  read only entries whose titles match your task). Check it before working on the
  session provider, the terminal/harness interaction, or the unattended write path.
- `sdd/ISSUES.md` — Known current limitations. Consult it when a problem feels like
  it might already be known.
