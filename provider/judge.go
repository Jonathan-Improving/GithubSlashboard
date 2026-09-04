package provider

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// Result is the outcome of judging one PR. When Unverified is true the response
// could not be model-verified after retries or the provider failed/timed out
// (TDD 6.4, 6.5); the caller must not present it as authoritative and instead
// falls back to raw flags.
type Result struct {
	Response   Response
	Unverified bool
	// Err is the last error encountered when Unverified is true (for logging).
	Err error
	// Attempts is the number of provider invocations made against whichever
	// provider ultimately produced (or failed to produce) this Result — the
	// primary's own attempts if it succeeded or no fallback ran, otherwise the
	// fallback's own attempts (TDD 6.13: never a combined count, since the two
	// slots' attempts are never interleaved).
	Attempts int
	// FromFallback is true when Response came from the fallback provider
	// rather than the primary (TDD 6.16). Always false when Unverified, since
	// an unverified result was not produced by either provider.
	FromFallback bool
}

// Judge runs the self-correcting retry loop for one request against primary:
// an initial call plus up to retryCap re-invocations, each quoting the prior
// bad output and the required structure (TDD 6.4). Each call is bounded by
// timeout (TDD 6.5). If every attempt fails to parse/validate or the provider
// errors, and fallback is non-nil, the identical retry loop then runs against
// fallback from a fresh attempt count — never interleaved with primary's own
// attempts (TDD 6.12, 6.13). If fallback is nil, or fallback's own attempts are
// also exhausted, the result is marked unverified rather than presenting a raw
// flag as truth (TDD 6.14). log, when non-nil, records the severity ladder
// (TDD 6.16): WARN when primary is exhausted and a fallback will be tried,
// ERROR when primary is exhausted with no fallback configured, INFO when the
// fallback succeeds, ERROR when the fallback is also exhausted. timeout bounds
// each primary call; fallbackTimeout bounds each fallback call independently
// (TDD 6.17) — a one-shot fallback backed by a local model can legitimately
// need longer per call than a session-based primary, discovered by live
// verification against real prompts rather than assumed in advance.
func Judge(ctx context.Context, primary, fallback Provider, req Request, retryCap int, timeout, fallbackTimeout time.Duration, log *slog.Logger) Result {
	res := judgeOnce(ctx, primary, req, retryCap, timeout)
	if !res.Unverified {
		return res
	}

	if fallback == nil {
		logJudge(log, slog.LevelError, "primary provider exhausted, no fallback configured", primary, req, res.Err)
		return res
	}
	logJudge(log, slog.LevelWarn, "primary provider exhausted, trying fallback", primary, req, res.Err)

	fbRes := judgeOnce(ctx, fallback, req, retryCap, fallbackTimeout)
	if !fbRes.Unverified {
		fbRes.FromFallback = true
		logJudge(log, slog.LevelInfo, "fallback provider produced a verdict", fallback, req, nil)
		return fbRes
	}
	logJudge(log, slog.LevelError, "fallback provider also exhausted", fallback, req, fbRes.Err)
	return fbRes
}

// logJudge is a small helper so Judge's four log call sites stay uniform and
// terse; a nil log is a no-op (Judge is usable without a logger, e.g. in tests).
func logJudge(log *slog.Logger, level slog.Level, msg string, p Provider, req Request, err error) {
	if log == nil {
		return
	}
	args := []any{"provider", p.Name(), "entity", string(req.Entity), "repo", req.Repo, "number", req.Number}
	if err != nil {
		args = append(args, "err", err)
	}
	log.Log(context.Background(), level, msg, args...)
}

// judgeOnce runs the self-correcting retry loop against a single provider —
// the shared mechanics behind both the primary and fallback attempts in Judge.
func judgeOnce(ctx context.Context, p Provider, req Request, retryCap int, timeout time.Duration) Result {
	var lastErr error
	correction := ""
	totalAttempts := retryCap + 1
	invocationErrors := 0

	for attempt := 0; attempt < totalAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		raw, err := p.Invoke(callCtx, req, correction)
		cancel()

		if err != nil {
			lastErr = fmt.Errorf("provider invocation failed: %w", err)
			invocationErrors++
			// A hard invocation/timeout error costs a full timeout and rarely
			// recovers on an immediate retry against the same session, so cap
			// these at one retry rather than burning the whole budget (the
			// self-correcting budget is for validation failures, not stalls).
			// Bail to unverified after the second consecutive invocation error.
			if invocationErrors >= 2 {
				break
			}
			correction = ""
			continue
		}
		invocationErrors = 0

		resp, perr := ParseResponse(raw, req.Constraints)
		if perr == nil {
			return Result{Response: resp, Unverified: false, Attempts: attempt + 1}
		}

		lastErr = perr
		correction = buildCorrection(raw, perr, req.Constraints)
	}

	return Result{Unverified: true, Err: lastErr, Attempts: totalAttempts}
}

// buildCorrection composes the steering text for a retry, quoting the bad
// output and restating the required structure (TDD 6.4). The vocabularies come
// from the request's constraints, so the same correction serves either entity.
func buildCorrection(badOutput string, validationErr error, c Constraints) string {
	closeClause := fmt.Sprintf("close_reason (required only if bucket==closed, one of %v), ", c.CloseReasons)
	if len(c.CloseReasons) == 0 {
		// No close-reason vocabulary is offered for this entity (an issue's
		// reason is a hard GitHub fact, not an inference), so do not invite one.
		closeClause = "no close_reason field (it is not inferred for this item), "
	}
	return fmt.Sprintf(
		"Your previous response was rejected: %s\n\n"+
			"You returned:\n%s\n\n"+
			"Return exactly one fenced JSON object with fields: "+
			"bucket (one of %v), action (required only if bucket==open, one of %v), "+
			"%s"+
			"priority (one of %v), companion (a note of %d-%d words), "+
			"emoji (exactly one emoji character summarizing the note). "+
			"Do not include any other fields or prose.",
		validationErr, badOutput,
		c.Buckets, c.Actions, closeClause, c.Priorities,
		c.CompanionWordsMin, c.CompanionWordsMax,
	)
}

// ConstraintsFrom builds the request Constraints for a pull request from the
// word bounds and the canonical PR vocabularies, so the provider is always told
// the exact valid sets (SCHEMA § Request constraints).
//
// conflicted and review_feedback are deliberately absent from the action set:
// they are assigned deterministically in code from hard GitHub facts and are
// not values the model may return (SCHEMA § action).
func ConstraintsFrom(minWords, maxWords int) Constraints {
	return Constraints{
		CompanionWordsMin: minWords,
		CompanionWordsMax: maxWords,
		Buckets:           []string{"open", "stale", "merged", "closed"},
		Actions: []string{
			"awaiting_review", "changes_requested", "author_active",
			"unassigned", "blocked_external", "merge_ready",
		},
		CloseReasons: []string{"superseded", "cancelled", "stale", "invalid"},
		Priorities:   []string{"neutral", "elevated"},
		Roles:        []string{"submitter", "reviewer"},
	}
}

// IssueConstraintsFrom builds the request Constraints for an issue. The issue
// vocabulary is narrower and differently shaped: there is no merged bucket, the
// action set is its own, and no close_reason vocabulary is offered at all
// because a closed issue's reason is GitHub's state_reason — a hard fact the
// classifier records directly, never a model inference (TDD 8.2). Offering an
// empty set means a response that proposes bucket "closed" is rejected, which is
// correct: the closed floor is applied in code and such an issue is never sent
// for open-disposition judgment.
func IssueConstraintsFrom(minWords, maxWords int) Constraints {
	return Constraints{
		CompanionWordsMin: minWords,
		CompanionWordsMax: maxWords,
		Buckets:           model.IssueBucketValues(),
		Actions:           model.IssueActionValues(),
		CloseReasons:      nil,
		Priorities:        []string{"neutral", "elevated"},
		Roles:             []string{string(model.IssueRoleAuthor), string(model.IssueRoleParticipant)},
	}
}
