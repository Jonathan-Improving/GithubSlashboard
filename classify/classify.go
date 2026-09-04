// Package classify applies the deterministic floors (merged, closed-unmerged)
// and orchestrates provider judgment for open-PR disposition, priority, and
// companion prose. The floors are enforced in code, independent of provider
// output (POLICY; TDD 4.1, 4.2), and stale determination layers operator
// override, an age threshold, and model inference (TDD 5.1–5.4).
package classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/config"
	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// Classifier judges PRs using a provider under a configuration. It holds no
// mutable per-PR state, so PRs may be classified concurrently. prior is the
// store's existing PR records keyed by PR key, and priorIssues the existing
// issue records keyed by issue key, both consulted read-only to detect an
// unchanged open item and skip the provider call for it (TDD 4.13, 8.8); they
// are never written to by the classifier.
type Classifier struct {
	prov provider.Provider
	// fallback is invoked when prov's own retry budget is exhausted (TDD 6.13).
	// Nil means no fallback is configured — exhausting prov's retries marks the
	// row unverified, unchanged from before this feature (TDD 6.12).
	fallback    provider.Provider
	cfg         config.Config
	log         *slog.Logger
	now         func() time.Time
	prior       map[string]model.PR
	priorIssues map[string]model.Issue
}

// New builds a Classifier. fallback may be nil (no fallback configured,
// TDD 6.13). now is injectable for deterministic tests; pass
// time.Now in production. prior is the store's existing PR records keyed by PR
// key (TDD 4.13), and priorIssues the existing issue records keyed by issue key
// (TDD 8.8); pass nil or an empty map for either when there is no prior store
// (e.g. first run) — every item is then treated as first-seen and reaches the
// provider.
func New(prov, fallback provider.Provider, cfg config.Config, log *slog.Logger, now func() time.Time, prior map[string]model.PR, priorIssues map[string]model.Issue) *Classifier {
	if now == nil {
		now = time.Now
	}
	return &Classifier{prov: prov, fallback: fallback, cfg: cfg, log: log, now: now, prior: prior, priorIssues: priorIssues}
}

// ClassifyAll classifies every PR, bounding provider fan-out by the configured
// worker limit (TECH: concurrency model). Each PR's judgment depends only on
// its own event trail, so the calls are independent. Results are returned in
// input order.
func (c *Classifier) ClassifyAll(ctx context.Context, prs []model.PR) []model.PR {
	out := make([]model.PR, len(prs))
	sem := make(chan struct{}, c.cfg.ClassifyWorkers)
	var wg sync.WaitGroup

	for i := range prs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			out[idx] = c.classifyOne(ctx, prs[idx])
		}(i)
	}
	wg.Wait()
	return out
}

// classifyOne classifies a single PR. Deterministic floors are applied first
// and are never overridden by provider output.
func (c *Classifier) classifyOne(ctx context.Context, pr model.PR) model.PR {
	switch pr.GitHubState {
	case model.GitHubStateMerged:
		return c.classifyMerged(ctx, pr)
	case model.GitHubStateClosed:
		return c.classifyClosed(ctx, pr)
	default:
		return c.classifyOpen(ctx, pr)
	}
}

// classifyMerged applies the immutable merged floor (TDD 4.1). Merged supersedes
// closed and every other state; no provider response can reclassify it. The
// provider is still consulted only for a companion note and priority, and even
// those default deterministically if judgment is unavailable.
func (c *Classifier) classifyMerged(ctx context.Context, pr model.PR) model.PR {
	pr.Bucket = model.BucketMerged
	pr.Action = ""
	pr.CloseReason = ""
	// A settled PR's check state is moot (TDD 4.10).
	pr.CIFailing = false
	if c.cfg.SkipFloorNotes {
		// Merged is a hard fact; skip the model. Companion is left empty (TDD
		// 4.5 unavailable marker) and priority defaults neutral. No judgment was
		// attempted, so Provider takes the same deterministic default as
		// Priority/CloseReason elsewhere on this floor-skip path.
		pr.Priority = model.PriorityNeutral
		pr.Companion = ""
		pr.Provider = model.ProviderSourcePrimary
		return pr
	}
	if len(pr.Events) == 0 && pr.Companion != "" {
		// Carried terminal record: no event trail to judge from, and a cached
		// verdict is already present (TDD 1.5). Keep its companion/emoji/priority
		// rather than re-judging an empty trail into an unavailable note. Provider
		// is left as whatever the carried record already has (TDD 6.16 carry-forward
		// via pr already being a copy of the stored record at this call site).
		return pr
	}
	c.applyCompanionAndPriority(ctx, &pr, model.GitHubStateMerged)
	return pr
}

// classifyClosed applies the immutable closed-unmerged floor (TDD 4.2). The
// provider may only select the close sub-reason (TDD 4.3); it cannot reclassify
// the PR as open, actionable, or stale-bucket (TDD 5.4).
func (c *Classifier) classifyClosed(ctx context.Context, pr model.PR) model.PR {
	pr.Bucket = model.BucketClosed
	pr.Action = ""
	// A settled PR's check state is moot (TDD 4.10).
	pr.CIFailing = false

	if c.cfg.SkipFloorNotes {
		// Closed-unmerged is a hard fact; skip the model. The close sub-reason
		// is not inferred (TDD 4.3) in this mode — default it deterministically
		// — and the companion is left empty (TDD 4.5 unavailable marker). This
		// is not the unverified fallback: the bucket is authoritative. No
		// judgment was attempted, so Provider takes the same deterministic
		// default as the other floor-skip fields.
		pr.CloseReason = model.CloseReasonCancelled
		pr.Priority = model.PriorityNeutral
		pr.Companion = ""
		pr.Provider = model.ProviderSourcePrimary
		return pr
	}

	if len(pr.Events) == 0 && pr.Companion != "" {
		// Carried terminal record: no event trail to judge from, and a cached
		// verdict — including its close sub-reason and Provider — is already
		// present (TDD 1.5). Keep it as-is rather than re-judging an empty
		// trail. Guard the sub-reason so a carried record missing it still
		// satisfies the schema.
		if !pr.CloseReason.Valid() {
			pr.CloseReason = model.CloseReasonCancelled
		}
		return pr
	}

	res := c.judge(ctx, pr, model.GitHubStateClosed)
	if res.Unverified {
		pr.Unverified = true
		pr.CloseReason = model.CloseReasonCancelled // conservative default flag, not authoritative
		pr.Priority = model.PriorityNeutral
		pr.Companion = ""
		// Neither provider produced a usable verdict; same deterministic
		// default as the open path's unverified branch.
		pr.Provider = model.ProviderSourcePrimary
		c.log.Warn("close classification unverified", "pr", pr.Key(), "attempts", res.Attempts, "reason", res.Err)
		return pr
	}
	// Honor only the close_reason and priority/companion from the response; the
	// bucket is fixed by the floor even if the provider proposed otherwise. The
	// response carries the reason as a plain string (the vocabulary is
	// entity-specific), so it is parsed into the PR enum here — this call site is
	// the boundary at which it becomes typed (POLICY: parsed once at the boundary).
	if reason, err := model.ParseCloseReason(res.Response.CloseReason); err == nil {
		pr.CloseReason = reason
	} else {
		pr.CloseReason = model.CloseReasonCancelled
	}
	pr.Priority = res.Response.Priority
	pr.Companion = res.Response.Companion
	pr.Emoji = res.Response.Emoji
	pr.Provider = providerSourceFor(res)
	return pr
}

// classifyOpen judges an open PR's disposition, priority, and companion from
// the event trail (TDD 4.4), then layers stale determination (TDD 5).
func (c *Classifier) classifyOpen(ctx context.Context, pr model.PR) model.PR {
	// Normalize the CI flag to its submitter scope up front, independently of any
	// action decision below. It is an orthogonal fact: nothing here reads or
	// writes pr.Action on its behalf, which is precisely what stops a red build
	// from shadowing a reviewer's actionable request (TDD 4.10, 4.11).
	pr.CIFailing = submitterCIFailing(pr)

	// Operator override wins unconditionally and short-circuits inference
	// (TDD 5.1).
	if pr.OperatorStale {
		pr.Bucket = model.BucketStale
		pr.Action = ""
		// Stale means nothing further is expected, so a red build is moot here as
		// on the floors (TDD 4.10).
		pr.CIFailing = false
		c.applyCompanionAndPriority(ctx, &pr, model.GitHubStateOpen)
		return pr
	}

	// Unchanged-since-last-run short-circuit (TDD 4.13). All deterministic
	// inputs to the judgment have already been freshly fetched above (CIFailing
	// just normalized; UnresolvedThreads, Mergeable, ReviewRequested, and the
	// trail itself came from the acquisition pass before classification ever
	// runs) — this only ever skips the provider call, never the fetch that
	// proves nothing changed. A prior record that was itself unverified is never
	// trusted as a cache: its bucket/action/companion may already be the
	// degraded fallback, not a real judgment worth repeating forever.
	fp := fingerprint(pr)
	if old, ok := c.prior[pr.Key()]; ok && !old.Unverified && old.InputFingerprint != "" && old.InputFingerprint == fp {
		pr.Bucket = old.Bucket
		pr.Action = old.Action
		pr.Priority = old.Priority
		pr.Companion = old.Companion
		pr.Emoji = old.Emoji
		pr.Unverified = false
		pr.InputFingerprint = fp
		// Carried forward unchanged: nothing was re-judged this run, so the
		// provenance of the carried verdict is also carried forward rather than
		// re-stamped (TDD 6.16) — obvious once stated: this row's Provider
		// value has not changed just because the run happened to execute.
		pr.Provider = old.Provider
		// Carried forward unchanged: this run never reached the provider for
		// this PR, so it is not part of the notification hook's change set
		// (TDD 9.1). WasJudged already defaults false; left unset here for
		// clarity rather than relying solely on the zero value.
		pr.WasJudged = false
		return pr
	}

	res := c.judge(ctx, pr, model.GitHubStateOpen)
	if res.Unverified {
		pr.Unverified = true
		pr.Bucket = model.BucketOpen
		pr.Action = model.ActionAwaitingReview // raw-flag fallback, marked not authoritative
		// A merge conflict is a hard GitHub fact, not a judgment, so it still
		// surfaces even when the model couldn't be verified: prefer it over the
		// meaningless raw-flag fallback for a submitter PR. The row stays
		// unverified because its disposition was not model-judged.
		if submitterFeedbackPending(pr) {
			pr.Action = model.ActionReviewFeedback
		}
		// A merge conflict is the harder blocker, so it wins over pending
		// review feedback when both hold (same precedence as the verified path).
		if submitterConflict(pr) {
			pr.Action = model.ActionConflicted
		}
		// A pending review request is a hard GitHub fact; surface it even when
		// the model verdict was unverified (reviewer PRs).
		if reviewerAwaiting(pr) {
			pr.Action = model.ActionAwaitingReview
		}
		pr.Priority = model.PriorityNeutral
		pr.Companion = ""
		// Neither provider produced a usable verdict, so there is no real
		// provenance to record; default deterministically to primary, the same
		// pattern as CloseReason/Priority's conservative defaults above — not
		// claiming a fallback rescue that did not happen.
		pr.Provider = model.ProviderSourcePrimary
		c.log.Warn("open classification unverified", "pr", pr.Key(), "attempts", res.Attempts, "reason", res.Err)
		// Deliberately not stamping InputFingerprint here: an unverified result
		// must never be treated as a cached judgment on a later run (TDD 4.13,
		// 4.14), and old.Unverified is already checked above regardless — but
		// leaving the field empty makes the intent explicit rather than relying
		// solely on the Unverified guard.
		// The provider WAS reached this run (that is what produced the
		// unverified result), so this still counts as "changed" for the
		// notification hook (TDD 9.1) — its inputs moved enough to warrant an
		// attempt, even though the attempt did not yield a usable verdict.
		pr.WasJudged = true
		return pr
	}

	pr.Priority = res.Response.Priority
	pr.Companion = res.Response.Companion
	pr.Emoji = res.Response.Emoji
	pr.Provider = providerSourceFor(res)

	// The response carries bucket and action as plain strings, because the legal
	// vocabulary is entity-specific (SCHEMA § Response). Parse them into the PR
	// enums here — this call site is the boundary at which a PR verdict becomes
	// typed. The provider validator already checked membership against the PR
	// vocabulary, so a parse failure here means a contract breach, not a model
	// mistake; fall back to the same raw-flag default the unverified path uses.
	respBucket, bErr := model.ParseBucket(res.Response.Bucket)
	respAction, aErr := model.ParseAction(res.Response.Action)
	if bErr != nil {
		respBucket = model.BucketOpen
	}
	if aErr != nil {
		respAction = model.ActionAwaitingReview
	}

	switch respBucket {
	case model.BucketStale:
		// Inferred tombstone (TDD 5.3).
		pr.Bucket = model.BucketStale
		pr.Action = ""
	case model.BucketOpen:
		pr.Bucket = model.BucketOpen
		pr.Action = respAction
	default:
		// The provider must not move an open PR into merged/closed buckets; the
		// floors own those. Keep it open.
		pr.Bucket = model.BucketOpen
		pr.Action = respAction
	}

	// Deterministic review-feedback override (submitter only). Unresolved review
	// threads are outstanding line comments the author has not answered — a hard
	// GitHub fact. It overrides model inference because an approval can coexist
	// with open comments, which reads as "conditionally approved" and wrongly
	// suggests the ball is elsewhere when it is with the author (TDD 4.9). This
	// is what puts the PR in the submitter "Action Needed" reckoning
	// (render ballWithUs).
	if pr.Bucket == model.BucketOpen && submitterFeedbackPending(pr) {
		pr.Action = model.ActionReviewFeedback
	}

	// Deterministic conflicted override (submitter only). A merge conflict is a
	// hard fact from GitHub, not an inference, and needs the author to act — so
	// it overrides whatever action the model inferred for a still-open PR the
	// operator authored. It is applied after review feedback because a conflict
	// is the harder blocker (it must be resolved before merge is even possible),
	// so it wins when both hold. It does not override stale (a stale PR's
	// conflict is moot) nor the merged/closed floors (never reach here).
	if pr.Bucket == model.BucketOpen && submitterConflict(pr) {
		pr.Action = model.ActionConflicted
	}

	// Deterministic review-requested override (reviewer only). A pending review
	// request on the operator is GitHub's authoritative "please review" signal
	// (the PR surfaced from the review-requested search), a hard fact rather
	// than an inference — so it marks the PR as awaiting our review, overriding
	// whatever the model read from the trail (e.g. an earlier changes_requested
	// the author has since addressed). This is what puts the PR in the reviewer
	// "Awaiting Our Action" table (render ballWithUs).
	if pr.Bucket == model.BucketOpen && reviewerAwaiting(pr) {
		pr.Action = model.ActionAwaitingReview
	}

	// Age-threshold stale applies to open PRs only (TDD 5.2, 5.4). It overrides
	// an open disposition when the PR has seen no activity within the threshold
	// — but a live pending review request means GitHub is actively asking us to
	// act, so such a PR is not stale regardless of age.
	if pr.Bucket == model.BucketOpen && !reviewerAwaiting(pr) && c.staleByAge(pr) {
		pr.Bucket = model.BucketStale
		pr.Action = ""
	}

	// A stale PR's check state is moot for the same reason a settled one's is:
	// nothing further is expected to happen, so flagging a red build as work
	// owed would contradict the bucket (TDD 4.10). This runs after every path
	// that can reach the stale bucket — the inferred tombstone above and the
	// age threshold here.
	if pr.Bucket == model.BucketStale {
		pr.CIFailing = false
	}

	// Stamp the fingerprint of the inputs that produced this judgment, so a
	// later run can detect "unchanged" and skip the provider call (TDD 4.13).
	pr.InputFingerprint = fp
	// The provider was reached, but only an open-bucket outcome counts as
	// "changed" for the notification hook (TDD 9.2) — a PR that landed in
	// Stale this run is a settled fact the operator is not expected to act on
	// further, even though classification consulted the provider to get there.
	pr.WasJudged = pr.Bucket == model.BucketOpen
	return pr
}

// reviewerAwaiting reports whether pr is a reviewer PR with a pending review
// request on the operator — the deterministic condition (a live GitHub fact)
// for treating it as awaiting our action.
func reviewerAwaiting(pr model.PR) bool {
	return pr.Role == model.RoleReviewer && pr.ReviewRequested
}

// submitterFeedbackPending reports whether pr is a submitter-authored PR with
// unresolved review threads. This is the deterministic condition for the
// review-feedback action: a hard fact, submitter-scoped (unresolved comments on
// a PR the operator only reviews are the other author's to answer), and counted
// excluding outdated threads, which point at already-rewritten lines.
func submitterFeedbackPending(pr model.PR) bool {
	return pr.Role == model.RoleSubmitter && pr.UnresolvedThreads > 0
}

// submitterCIFailing reports whether pr is a submitter-authored PR whose head
// commit has failing checks. Submitter-scoped for the same reason as the other
// deterministic predicates: another author's broken build is not the operator's to
// fix. Unlike them it drives a *flag* rather than the action, because CI state is
// orthogonal to review state (TDD 4.10, 4.11).
func submitterCIFailing(pr model.PR) bool {
	return pr.Role == model.RoleSubmitter && pr.CIFailing
}

// submitterConflict reports whether pr is a submitter-authored PR that GitHub
// reports as conflicting with its base branch. This is the deterministic
// condition for the conflicted action: a hard fact, submitter-scoped (a
// conflict on a PR the operator only reviews is not the operator's to fix), and
// only meaningful while Mergeable is known (nil = GitHub had not computed it).
func submitterConflict(pr model.PR) bool {
	return pr.Role == model.RoleSubmitter && pr.Mergeable != nil && !*pr.Mergeable
}

// fingerprint hashes every deterministic input to an open PR's judgment: the
// most recent trail event (its timestamp, kind, and text — not the whole
// trail, since only the latest authoritative event governs classification per
// TDD 4.4), CIFailing, UnresolvedThreads, Mergeable, and ReviewRequested. Two
// calls with the same inputs always produce the same fingerprint, so comparing
// it against a prior stored value detects "nothing worth re-judging happened"
// without trusting any single field (e.g. LastActivity) as a proxy — live
// GitHub data showed a full CI check-run cycle can complete without moving a
// PR's own last-modified signal (TDD 4.13). The result is opaque; it has no
// meaning beyond equality comparison.
func fingerprint(pr model.PR) string {
	var last model.Event
	if n := len(pr.Events); n > 0 {
		last = pr.Events[n-1]
	}
	mergeable := "unknown"
	if pr.Mergeable != nil {
		if *pr.Mergeable {
			mergeable = "true"
		} else {
			mergeable = "false"
		}
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%t|%d|%s|%t",
		last.Timestamp.UTC().Format(time.RFC3339Nano), last.Kind, last.Text,
		pr.CIFailing, pr.UnresolvedThreads, mergeable, pr.ReviewRequested)
	return hex.EncodeToString(h.Sum(nil))
}

// staleByAge reports whether the PR has had no activity within the configured
// threshold (TDD 5.2).
func (c *Classifier) staleByAge(pr model.PR) bool {
	last := pr.LastActivity
	if last.IsZero() {
		last = pr.Created
	}
	return c.now().Sub(last) >= c.cfg.StaleAgeThreshold
}

// applyCompanionAndPriority consults the provider for a note and priority on a
// PR whose bucket is already fixed (merged, or operator-stale). Failure yields a
// neutral, note-less result rather than blocking; it does not mark the row
// unverified because the authoritative bucket is a hard fact, not a judgment.
func (c *Classifier) applyCompanionAndPriority(ctx context.Context, pr *model.PR, state model.GitHubState) {
	res := c.judge(ctx, *pr, state)
	if res.Unverified {
		pr.Priority = model.PriorityNeutral
		pr.Companion = ""
		pr.Emoji = ""
		// Neither provider produced a usable verdict; same deterministic
		// default used everywhere else on an unverified/skipped path.
		pr.Provider = model.ProviderSourcePrimary
		return
	}
	pr.Priority = res.Response.Priority
	pr.Companion = res.Response.Companion
	pr.Emoji = res.Response.Emoji
	pr.Provider = providerSourceFor(res)
}

// providerSourceFor translates a Result's FromFallback flag into the typed
// ProviderSource persisted on a PR or issue (TDD 6.16). Shared by every
// successful-judgment call site in this package so the translation happens in
// exactly one place.
func providerSourceFor(res provider.Result) model.ProviderSource {
	if res.FromFallback {
		return model.ProviderSourceFallback
	}
	return model.ProviderSourcePrimary
}

// judge builds the provider request and runs the bounded, self-correcting retry
// loop (TDD 6.4, 6.5).
func (c *Classifier) judge(ctx context.Context, pr model.PR, state model.GitHubState) provider.Result {
	req := provider.Request{
		Entity:      provider.EntityPR,
		Repo:        pr.Repo,
		Number:      pr.Number,
		Role:        string(pr.Role),
		State:       string(state),
		Events:      pr.Events,
		Constraints: provider.ConstraintsFrom(c.cfg.CompanionWordsMin, c.cfg.CompanionWordsMax),
	}
	return provider.Judge(ctx, c.prov, c.fallback, req, c.cfg.LLMRetryCap, c.cfg.ProviderTimeout, c.cfg.FallbackProviderTimeout, c.log)
}
