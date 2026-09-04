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
	"log/slog"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// Change is one open PR or issue that required a fresh judgment this run
// (SCHEMA § `change`).
//
// Repo/Number/URL/Role/Bucket/Action/Priority are deterministic, code-derived
// values. Companion is model-generated text ultimately derived from
// GitHub-sourced content the operator does not control (PR titles,
// descriptions, comments, issue text — some written by other people). It is
// sanitized (sanitizeText) before Fire builds the payload, stripping the
// characters that enable command substitution or chaining — a downstream
// consumer that blindly forwards this field to a shell or `eval` cannot use
// it to run an arbitrary command. Quotes are preserved as ordinary prose, so
// a consumer building its own notification call is still expected to quote
// this field correctly (SCHEMA § Notification hook, trust boundary).
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
// (SCHEMA § Notification hook § Payload). Summary is model-generated text
// subject to the same untrusted-string-data caution as Change.Companion — see
// Change's doc comment.
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
//
// Each item's Companion note is sanitized (sanitizeText) before it goes into
// the prompt, not just at the payload boundary in Fire — the same
// GitHub-sourced content that could carry command-injection metacharacters
// could equally carry prompt-injection text aimed at steering the summary
// call itself, so the dangerous characters are removed on the way in as well
// as on the way out. This is a preventative measure layered on top of Fire's
// output sanitization (TDD 9.6), not a substitute for it: Fire still
// sanitizes summary/companion again regardless of what the provider returns,
// since a provider is free to reintroduce characters this prompt never had.
func summaryPrompt(changed []Change) string {
	var b strings.Builder
	b.WriteString("Write exactly one short sentence (fit for a desktop notification toast) summarizing the following changes to GitHub pull requests and issues. Do not list every item verbatim; synthesize. Return only the sentence, no quotes or preamble.\n\n")
	for _, c := range changed {
		b.WriteString(fmt.Sprintf("- %s %s#%d: bucket=%s", c.Entity, c.Repo, c.Number, c.Bucket))
		if c.Action != "" {
			b.WriteString(" action=" + c.Action)
		}
		if c.Companion != "" {
			b.WriteString(" note=\"" + sanitizeText(c.Companion) + "\"")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Summarize asks p for a one-sentence summary of changed, falling back to a
// plain non-model string on any error or empty result (TDD 6.9) — Summarize
// deliberately does not retry, so this is the caller's proportionate response
// to a single failed attempt. fallback (may be nil), when p's own single
// attempt fails, gets its own single attempt before the plain-string fallback
// (TDD 6.15) — the same trigger rule as classification's Judge, applied to
// Summarize's own simpler, non-retrying shape. timeout bounds p's call;
// fallbackTimeout bounds fallback's call independently (TDD 6.17), mirroring
// Judge's own primary/fallback timeout split. log, when non-nil, records the
// outcome (TDD 6.16): WARN when p fails and fallback will be tried, INFO when
// fallback succeeds, ERROR when fallback also fails (or none is configured).
func Summarize(ctx context.Context, p, fallback provider.Provider, changed []Change, timeout, fallbackTimeout time.Duration, log *slog.Logger) string {
	if len(changed) == 0 {
		return ""
	}
	prompt := summaryPrompt(changed)

	if sentence, ok := summarizeOnce(ctx, p, prompt, timeout); ok {
		return sentence
	}

	if fallback == nil {
		logSummarize(log, slog.LevelError, "summarize: primary failed, no fallback configured", p)
		return fallbackSummary(changed)
	}
	logSummarize(log, slog.LevelWarn, "summarize: primary failed, trying fallback", p)

	if sentence, ok := summarizeOnce(ctx, fallback, prompt, fallbackTimeout); ok {
		logSummarize(log, slog.LevelInfo, "summarize: fallback succeeded", fallback)
		return sentence
	}
	logSummarize(log, slog.LevelError, "summarize: fallback also failed", fallback)
	return fallbackSummary(changed)
}

// summarizeOnce makes p's single bounded Summarize attempt, reporting whether
// it produced a usable sentence.
func summarizeOnce(ctx context.Context, p provider.Provider, prompt string, timeout time.Duration) (string, bool) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := p.Summarize(callCtx, prompt)
	if err != nil {
		return "", false
	}
	sentence := extractSummary(raw)
	if sentence == "" || !plausibleSummary(sentence) {
		return "", false
	}
	return sentence, true
}

// maxPlausibleSummaryLen is a generous cap on a "one short sentence, fit for
// a desktop notification toast" (TDD 6.9's own phrasing). It exists as a
// backstop behind extractSummary's "done thinking." cutoff, not a substitute
// for it: a thinking model that omits the marker, or keeps narrating past it,
// must never have that narration forwarded to the hook as if it were the
// summary — the prompt and the model's reasoning about the prompt are for the
// model's own inference, never for the notification payload (TDD 9.7). A
// genuine toast sentence is always far under this; a multi-paragraph
// reasoning trace never is.
const maxPlausibleSummaryLen = 280

// promptEchoFingerprints are substrings that only appear in Summarize's own
// instruction text (summaryPrompt) or in a model's narration *about* that
// instruction, never in a genuine summary sentence about PRs and issues. They
// are a defense-in-depth check, not the primary defense (the length cap and
// the "done thinking." cutoff carry that): a real answer has no reason to
// contain the literal word "constraint" or to talk about "the prompt" itself.
var promptEchoFingerprints = []string{
	"desktop notification toast",
	"constraint 1",
	"constraint 2",
	"the prompt asks",
	"the prompt says",
	"analyze the request",
	"analyzing the request",
}

// plausibleSummary reports whether sentence looks like a genuine one-sentence
// summary rather than a model's leaked reasoning trace or a verbatim echo of
// summaryPrompt's own instructions (TDD 9.7). It is intentionally permissive
// — a false negative here only costs a fallback to the plain non-model
// string, never a wrong classification — so it need not be exhaustive, only
// enough to catch the shapes a thinking model's narration actually takes.
func plausibleSummary(sentence string) bool {
	if len(sentence) > maxPlausibleSummaryLen {
		return false
	}
	lower := strings.ToLower(sentence)
	for _, fp := range promptEchoFingerprints {
		if strings.Contains(lower, fp) {
			return false
		}
	}
	return true
}

// logSummarize is Summarize's small logging helper, mirroring provider.Judge's
// (a nil log is a no-op).
func logSummarize(log *slog.Logger, level slog.Level, msg string, p provider.Provider) {
	if log == nil {
		return
	}
	log.Log(context.Background(), level, msg, "provider", p.Name())
}

// extractSummary pulls the sentence out of the provider's raw response. A
// session provider's summary tool returns a small JSON object
// ({"summary": "..."}); a one-shot provider simply echoes back whatever prose
// it produced. Both are handled: try JSON first, fall back to the trimmed raw
// text.
//
// A "thinking" one-shot model (e.g. GLM-4.7-Flash under Ollama, notify's
// fallback slot) narrates its reasoning before answering — drafting and
// revising candidate sentences while echoing the prompt's own instructions
// back verbatim ("Constraint 1: exactly one short sentence...") — so that
// narration must never reach the hook payload as if it were the summary
// itself. This mirrors extractJSON's identical concern on the classification
// path (provider/parse.go): Ollama's CLI convention emits a literal
// "...done thinking." line before the real answer, so only the text after it
// is considered; its absence (a non-thinking model, or the session/Kiro path,
// which never narrates) falls back to the whole raw text, unchanged from
// before.
func extractSummary(raw string) string {
	if i := strings.LastIndex(raw, "done thinking."); i >= 0 {
		raw = raw[i+len("done thinking."):]
	}

	var parsed struct {
		Summary string `json:"summary"`
	}
	trimmed := strings.TrimSpace(raw)
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil && parsed.Summary != "" {
		return strings.TrimSpace(parsed.Summary)
	}
	return trimmed
}

// sanitizeText strips every character outside the known-safe shape of a
// hook-payload text field — natural language plus emoji — before the value
// ever reaches JSON, so a downstream consumer that blindly forwards the
// payload to a shell, `eval`, or another interpreter cannot be tricked into
// command injection (CWE-78) no matter how carelessly it handles the string.
// Documenting the trust boundary (SCHEMA § Notification hook) is necessary but
// not sufficient — this function is what actually removes the dangerous
// characters, deterministically, because both fields it applies to have a
// known, closed shape: model-generated natural-language prose, optionally
// with emoji, never code of any kind.
//
// The allowlist is Unicode letters, marks (for accented/combining scripts),
// digits, spaces, ordinary prose punctuation (via unicode.IsPunct, MINUS a
// fixed denylist of the characters that actually enable command injection —
// backtick, $, backslash, semicolon, pipe, ampersand, angle brackets, and
// every bracket/brace/parenthesis), and emoji (including their joiner/modifier
// companions). Single and double quotes are deliberately NOT stripped: they
// are ordinary, extremely common prose characters (contractions, quoted
// phrases, possessives), and on their own — with command substitution,
// chaining, and redirection already removed — a lone quote cannot invoke a
// command; it can only break out of a downstream consumer's own quoting if
// that consumer built a second shell string carelessly, which is a defect in
// that consumer's own quoting discipline, not something this sanitizer can or
// should paper over. A rune outside the allowlist is dropped, not replaced or
// escaped, so the result can never reconstruct a metacharacter through
// escaping tricks.
func sanitizeText(s string) string {
	const dangerousPunctuation = "`$\\;|&<>(){}[]"
	var b strings.Builder
	for _, r := range s {
		switch {
		case isEmojiJoiner(r), isEmojiRune(r):
			// Emoji first and unconditionally: several emoji ranges overlap
			// Unicode's Symbol/Punctuation categories, so classifying emoji
			// before the general punctuation/symbol check below means an
			// emoji is never accidentally caught by the dangerous-set test.
			b.WriteRune(r)
		case unicode.IsLetter(r), unicode.IsMark(r), unicode.IsDigit(r), unicode.IsSpace(r):
			b.WriteRune(r)
		case (unicode.IsPunct(r) || unicode.IsSymbol(r)) && !strings.ContainsRune(dangerousPunctuation, r):
			b.WriteRune(r)
		}
		// Anything else — control characters, the dangerous punctuation set,
		// and any rune not covered above — is silently dropped, not escaped:
		// an escaped metacharacter is still a metacharacter to a second
		// interpreter that applies its own unescaping.
	}
	return b.String()
}

// isEmojiJoiner and isEmojiRune classify the combining/joining companions and
// base glyphs of a single emoji, mirroring provider's emoji validation
// (provider/parse.go) — duplicated here deliberately rather than exported,
// since this is a small, stable Unicode range table and notify has no other
// reason to depend on provider's internals.
func isEmojiJoiner(r rune) bool {
	switch {
	case r == 0x200D: // zero-width joiner
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF: // skin-tone modifiers
		return true
	default:
		return false
	}
}

func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF: // Misc Symbols & Pictographs, Emoticons, Transport, etc.
		return true
	case r >= 0x2600 && r <= 0x27BF: // Misc Symbols + Dingbats
		return true
	case r >= 0x2300 && r <= 0x23FF: // Misc Technical
		return true
	case r >= 0x25A0 && r <= 0x25FF: // Geometric Shapes
		return true
	case r >= 0x2190 && r <= 0x21FF: // Arrows
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // Misc Symbols and Arrows
		return true
	case r == 0x203C || r == 0x2049:
		return true
	case r >= 0x2122 && r <= 0x2139:
		return true
	case r >= 0x1F000 && r <= 0x1F02F: // Mahjong/dominoes-adjacent pictographs
		return true
	case r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicators (flags)
		return true
	default:
		return false
	}
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
	// Sanitize every free-text field at the payload boundary, deterministically
	// removing shell/code metacharacters (TDD 9.6) — this is what makes the
	// guarantee real for a downstream consumer that blindly forwards these
	// values, not just documentation asking implementers to be careful.
	sanitized := make([]Change, len(changed))
	for i, c := range changed {
		c.Companion = sanitizeText(c.Companion)
		sanitized[i] = c
	}
	payload := Payload{Summary: sanitizeText(summary), Changed: sanitized}
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
