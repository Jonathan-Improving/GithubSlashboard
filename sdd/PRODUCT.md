# Product Definition

## What is GithubSlashboard?

GithubSlashboard is a personal, read-only GitHub dashboard generator. It gathers
the pull requests you author and the ones you are asked to review, plus the issues
you open and take part in, records their state in a local YAML file, and renders
that file into a Markdown status document. Where an item's true status cannot be
read from GitHub's flags alone, it consults a language model to judge the status
from the item's history and to add a short human-readable note.

Issues are deliberately treated as the simpler entity they are: there is no merge,
their close reason is GitHub's own state reason rather than an inference, and an
issue with no conversation is reported from hard facts alone rather than having a
note invented for it.

## What problem does it solve?

Keeping a personal picture of "what is going on with all my PRs and issues" is
tedious and easy to get wrong. GitHub's own flags routinely lie by omission — a
reviewer requests changes, then says "actually, hold off," and never clears the
flag — so a purely mechanical summary misrepresents reality. The prior approach, a
long-lived conversational agent poked on a schedule, proved heavy and fragile.

- **Scattered status**: state is spread across repos, roles (author vs. reviewer,
  author vs. participant), and tabs; there is no single at-a-glance view.
- **Stale flags**: GitHub's review-decision and draft flags often lag the real
  situation recorded in later comments.
- **Manual upkeep**: Hand-maintaining a tracker document is repetitive work that
  should be mechanical.
- **Fragile automation**: Driving a full interactive agent on a timer is
  resource-heavy and unreliable for what is mostly a deterministic task.

## Who is it for?

- **A developer** who authors and reviews many PRs, and opens and takes part in
  issues, across multiple repositories, and wants one current status document.
- **An operator** who wants that document refreshed unattended on a schedule
  without babysitting an agent.
- **A downstream automation** that can consume the YAML source of truth, since
  every status is a value drawn from a finite, machine-readable vocabulary.

## Why does it exist?

- **GitHub's web UI and notifications** show activity, but not a consolidated,
  role-split, status-bucketed summary you can keep as a document.
- **`gh` CLI queries** return raw flags, but no judgment — they cannot tell that a
  "changes requested" PR was verbally stood down in a later comment.
- **A conversational tracking agent** can reason about status, but is expensive,
  non-deterministic in its output shape, and heavy to run on a schedule.

GithubSlashboard fills the gap: a lightweight, deterministic generator that treats
GitHub as read-only, keeps its own auditable source of truth, and uses a language
model only for the narrow judgment that genuinely needs one — reading the PR's
paper trail to determine its real status.

## How is it used?

1. On a schedule, the tool fetches your authored and review-requested PRs and your
   authored and participated-in issues from GitHub, and assembles each item's
   chronological event trail.
2. It classifies each item — deterministic facts (a PR merged or closed, an issue
   closed and why) are honored as ground truth; a live item's real disposition, its
   priority, and any short note are judged from the event trail by the configured
   model. An issue with no conversation is reported from hard facts alone, with no
   model call and no invented note.
3. It writes the result to the YAML source of truth, preserving any state you set
   by hand (such as a manual stale override).
4. It renders the YAML into the Markdown status document: a summary table covering
   both entities, then role-split, status-bucketed PR sections, then the issue
   sections.

The language-model provider is configurable (a cheap model suffices). Pull requests
were the tool's first tracked entity and issues its second; the store's tagged
documents and the provider's entity-generic hand-off are what let a further GitHub
entity be added without restructuring.

## Vocabulary

| Term | Definition |
|------|-----------|
| **source of truth** | The local YAML file; the authoritative record from which the Markdown is rendered. |
| **status document** | The rendered Markdown output — a pure view of the source of truth, never hand-edited. |
| **tracked entity** | A kind of GitHub item the tool follows. Currently pull requests and issues, each with its own status vocabulary and its own tagged document type in the source of truth. |
| **event trail** | An item's chronological, timestamped history of comments, reviews, state changes, labels, and — for an open PR — its current CI outcome, unresolved review threads, and overall review decision, used to judge its real status. An issue's trail is just its comments and state changes; it has no review machinery. |
| **status** | An item's disposition, drawn from a finite vocabulary (e.g. an Action Needed value, a Closed sub-reason, or Stale). Each entity has its own vocabulary rather than sharing one. |
| **unresolved review thread** | A reviewer's line comment the author has not yet resolved (and which is not outdated). Outstanding threads mean the author owes the next move, even when another reviewer has already approved. |
| **failing checks** | A red build on the operator's own pull request — work they owe regardless of what else the item is waiting on. Recorded as a fact separate from the item's status, precisely because the two are independent: a build failure must never hide a reviewer's request, nor the reverse. |
| **companion** | The short (3–14 word) human-readable note that accompanies a status, inferred from the event trail. |
| **emoji** | A single glyph the model infers to summarize an item's note at a glance; it prefixes the note in the rendered document. |
| **priority** | An item's urgency, inferred from the event trail; currently neutral or elevated. |
| **stale** | An item that will see no further action — set by operator override, an age threshold, or model inference. Issues use a far longer age threshold than PRs, because they legitimately sit idle without being abandoned. |
| **state reason** | GitHub's own recorded reason a closed issue was closed (completed, not planned, duplicate, reopened). A hard fact the tool records verbatim, in contrast to a PR's closed sub-reason, which is inferred. |
| **provider** | The configured language-model backend that performs inference (Kiro CLI for the MVP). |
| **unverified** | A row whose status could not be model-verified after retries or provider failure, and so is not presented as authoritative. Distinct from a row that was deliberately never judged (an issue with no conversation), which is a fact, not a failure. |
| **operator** | The single user the tool runs for. |
