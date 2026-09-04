// Package notify owns the notification hook: collecting which open PRs and
// issues actually required a fresh provider judgment this run, asking the
// provider for a one-sentence summary, and delivering the resulting JSON
// payload to a configured shell command's stdin (SCHEMA § Notification hook;
// TDD § 9). It is a best-effort, run-local side channel — a failure here never
// fails the run, and it is entirely inert when no hook command is configured.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// Change is one open PR or issue that required a fresh judgment this run
// (SCHEMA § `change`).
type Change struct {
	Entity    string `json:"entity"`
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Role      string `json:"role"`
	Bucket    string `json:"bucket"`
	Action    string `json:"action,omitempty"`
	Companion string `json:"companion,omitempty"`
	Priority  string `json:"priority"`
}

// Payload is the JSON object written to the hook command's stdin
// (SCHEMA § Notification hook § Payload).
type Payload struct {
	Summary string   `json:"summary"`
	Changed []Change `json:"changed"`
}

// entityPR and entityIssue mirror provider.EntityPR/EntityIssue as plain
// strings for the payload, matching the same vocabulary the provider request
// itself uses (SCHEMA § `change` entity).
const (
	entityPR    = "pull_request"
	entityIssue = "issue"
)

// ChangesFromPRs returns the Change entries for every PR whose WasJudged flag
// is set (TDD 9.1, 9.2) — a first-seen, changed, or previously-unverified open
// PR. A carried-forward-unchanged PR, and any merged/closed/stale PR, never
// appears here (WasJudged is false for both, by construction in classify).
func ChangesFromPRs(prs []model.PR) []Change {
	var out []Change
	for _, p := range prs {
		if !p.WasJudged {
			continue
		}
		out = append(out, Change{
			Entity:    entityPR,
			Repo:      p.Repo,
			Number:    p.Number,
			URL:       p.URL,
			Role:      string(p.Role),
			Bucket:    string(p.Bucket),
			Action:    string(p.Action),
			Companion: p.Companion,
			Priority:  string(p.Priority),
		})
	}
	return out
}

// ChangesFromIssues returns the Change entries for every issue whose
// WasJudged flag is set, mirroring ChangesFromPRs (TDD 9.1, 9.2).
func ChangesFromIssues(issues []model.Issue) []Change {
	var out []Change
	for _, i := range issues {
		if !i.WasJudged {
			continue
		}
		out = append(out, Change{
			Entity:    entityIssue,
			Repo:      i.Repo,
			Number:    i.Number,
			URL:       i.URL,
			Role:      string(i.Role),
			Bucket:    string(i.Bucket),
			Action:    string(i.Action),
			Companion: i.Companion,
			Priority:  string(i.Priority),
		})
	}
	return out
}

// fallbackSummary builds a plain, non-model summary sentence for when the
// provider's Summarize call fails or is unavailable — a wrong or missing toast
// sentence is a notification inconvenience, not an authoritative judgment
// (TDD 6.9), so this keeps the hook useful without ever blocking on the model.
func fallbackSummary(changed []Change) string {
	if len(changed) == 1 {
		c := changed[0]
		return fmt.Sprintf("%s#%d changed", c.Repo, c.Number)
	}
	return fmt.Sprintf("%d items changed", len(changed))
}

// summaryPrompt renders the changed set into the plain prompt text handed to
// Provider.Summarize (TDD 6.9): a short instruction plus a compact per-item
// listing, deliberately not the full Request/Events shape classification uses.
func summaryPrompt(changed []Change) string {
	var b strings.Builder
	b.WriteString("Write exactly one short sentence (fit for a desktop notification toast) summarizing the following changes to GitHub pull requests and issues. Do not list every item verbatim; synthesize. Return only the sentence, no quotes or preamble.\n\n")
	for _, c := range changed {
		b.WriteString(fmt.Sprintf("- %s %s#%d: bucket=%s", c.Entity, c.Repo, c.Number, c.Bucket))
		if c.Action != "" {
			b.WriteString(" action=" + c.Action)
		}
		if c.Companion != "" {
			b.WriteString(" note=\"" + c.Companion + "\"")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Summarize asks p for a one-sentence summary of changed, falling back to a
// plain non-model string on any error or empty result (TDD 6.9) — Summarize
// deliberately does not retry, so this is the caller's proportionate response
// to a single failed attempt.
func Summarize(ctx context.Context, p provider.Provider, changed []Change, timeout time.Duration) string {
	if len(changed) == 0 {
		return ""
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := p.Summarize(callCtx, summaryPrompt(changed))
	if err != nil {
		return fallbackSummary(changed)
	}
	sentence := extractSummary(raw)
	if sentence == "" {
		return fallbackSummary(changed)
	}
	return sentence
}

// extractSummary pulls the sentence out of the provider's raw response. A
// session provider's summary tool returns a small JSON object
// ({"summary": "..."}); a one-shot provider simply echoes back whatever prose
// it produced. Both are handled: try JSON first, fall back to the trimmed raw
// text.
func extractSummary(raw string) string {
	var parsed struct {
		Summary string `json:"summary"`
	}
	trimmed := strings.TrimSpace(raw)
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil && parsed.Summary != "" {
		return strings.TrimSpace(parsed.Summary)
	}
	return trimmed
}

// Fire builds the payload from changed and summary, and — only when changed is
// non-empty (TDD 9.1) and hookCmd is non-empty (TDD 9.4) — writes it as JSON to
// the hook command's stdin, bounded by timeout. Any failure (bad command,
// non-zero exit, timeout) is returned as an error for the caller to log; it is
// never treated as fatal to the run (TDD 9.5).
func Fire(ctx context.Context, hookCmd string, timeout time.Duration, changed []Change, summary string) error {
	if len(changed) == 0 || hookCmd == "" {
		return nil
	}
	payload := Payload{Summary: summary, Changed: changed}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal notify payload: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(callCtx, "sh", "-c", hookCmd)
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("notify hook timed out after %s", timeout)
		}
		return fmt.Errorf("notify hook failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
