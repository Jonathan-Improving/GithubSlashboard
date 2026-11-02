package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// Held is an item whose hard fields match its prior stored record's but whose
// companion note or emoji differs. It is held back from the changed set until
// the Prose Diff Checker judges whether the difference is meaningful
// (TDD 9.9, 9.10). Change carries the item's fresh state, exactly as it would
// appear in the payload if it joins the changed set.
type Held struct {
	Change       Change
	OldCompanion string
	OldEmoji     string
	NewEmoji     string
}

// HeldBackFromPRs returns a Held entry for every PR whose ProseHeldBack is set.
func HeldBackFromPRs(prs []model.PR) []Held {
	var out []Held
	for _, p := range prs {
		if p.ProseHeldBack == nil {
			continue
		}
		out = append(out, Held{
			Change:       prChange(p),
			OldCompanion: p.ProseHeldBack.OldCompanion,
			OldEmoji:     p.ProseHeldBack.OldEmoji,
			NewEmoji:     p.Emoji,
		})
	}
	return out
}

// HeldBackFromIssues returns a Held entry for every issue whose ProseHeldBack
// is set, mirroring HeldBackFromPRs.
func HeldBackFromIssues(issues []model.Issue) []Held {
	var out []Held
	for _, i := range issues {
		if i.ProseHeldBack == nil {
			continue
		}
		out = append(out, Held{
			Change:       issueChange(i),
			OldCompanion: i.ProseHeldBack.OldCompanion,
			OldEmoji:     i.ProseHeldBack.OldEmoji,
			NewEmoji:     i.Emoji,
		})
	}
	return out
}

// noNote stands in for an empty note in the Prose Diff Checker's prompt.
const noNote = "(none)"

// proseDiffPrompt renders every held-back item into one batch prompt for the
// Prose Diff Checker (TDD 9.10). The answer is a single line of ID=YES/ID=NO
// pairs, a shape a tiny local model can follow and a session provider's
// sentence-shaped summary tool can carry. Both notes are sanitized before they
// enter the prompt (TDD 9.16), since they derive from untrusted GitHub content.
func proseDiffPrompt(held []Held) string {
	var b strings.Builder
	b.WriteString("You compare short status notes for GitHub pull requests and issues. ")
	b.WriteString("For each numbered item below, decide whether the NEW note means something meaningfully different from the OLD note: a different state, a different blocker, a different owner of the next move, or a different urgency. ")
	b.WriteString("Rewording, reordering, or a different emphasis of the same meaning is NOT meaningful. ")
	b.WriteString("Your entire answer is a single line of space-separated pairs, one per item, written ID=YES when meaningfully different or ID=NO when the same meaning, for example: 1=NO 2=YES. Write nothing else in the answer.\n\n")
	for n, h := range held {
		fmt.Fprintf(&b, "%d. %s#%d\n   OLD: %s %s\n   NEW: %s %s\n", n+1, h.Change.Repo, h.Change.Number,
			noteOrNone(h.OldCompanion), sanitizeText(h.OldEmoji),
			noteOrNone(h.Change.Companion), sanitizeText(h.NewEmoji))
	}
	return b.String()
}

func noteOrNone(note string) string {
	if cleaned := strings.TrimSpace(sanitizeText(note)); cleaned != "" {
		return cleaned
	}
	return noNote
}

// ansiEscape matches ANSI/VT100 control sequences. Ollama's CLI interleaves
// cursor show/hide codes through its output even when piped, splitting a
// verdict such as "1=NO" across escape sequences, so they are removed before
// the verdicts are read. Duplicated from provider/parse.go deliberately, for the
// same reason as the emoji tables above: notify has no other reason to depend on
// provider's internals.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

var verdictPattern = regexp.MustCompile(`(?i)\b(\d+)\s*[=:]\s*(yes|no)\b`)

// parseProseVerdicts reads the Prose Diff Checker's answer into a map of
// 1-based item number to "meaningfully different". It mirrors extractSummary's
// handling of the two response shapes: a session provider returns its text
// wrapped as {"summary": "..."}, and a thinking one-shot model narrates before
// a literal "...done thinking." line, so only the text after the last such
// line is read. Where an id appears more than once, the last answer wins.
func parseProseVerdicts(raw string) map[int]bool {
	raw = ansiEscape.ReplaceAllString(raw, "")
	if i := strings.LastIndex(raw, "done thinking."); i >= 0 {
		raw = raw[i+len("done thinking."):]
	}
	var wrapped struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &wrapped); err == nil && wrapped.Summary != "" {
		raw = wrapped.Summary
	}
	verdicts := map[int]bool{}
	for _, m := range verdictPattern.FindAllStringSubmatch(raw, -1) {
		id, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		verdicts[id] = strings.EqualFold(m[2], "yes")
	}
	return verdicts
}

// proseDiffOnce makes p's single bounded attempt at the batch, reporting
// whether it produced at least one parseable verdict. A nil p is a failed
// attempt, so a slot that could not be constructed still reaches the fallback.
func proseDiffOnce(ctx context.Context, p provider.Provider, prompt string, timeout time.Duration) (map[int]bool, bool) {
	if p == nil {
		return nil, false
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := p.Summarize(callCtx, prompt)
	if err != nil {
		return nil, false
	}
	verdicts := parseProseVerdicts(raw)
	return verdicts, len(verdicts) > 0
}

// CheckProse asks the Prose Diff Checker whether each held-back item's new note
// differs in meaning from its old one, in a single batch call (TDD 9.10), and
// returns the Change entries to add to the changed set: every item judged
// meaningfully different (TDD 9.11), plus every item left without a verdict,
// since a failed check must not hide a change (TDD 9.15). p (may be nil) gets
// one attempt bounded by timeout; when that fails, fallback (may be nil) gets
// one attempt bounded by fallbackTimeout (TDD 9.14). Outcomes are logged at the
// levels of TDD 6.16 (log may be nil).
func CheckProse(ctx context.Context, p, fallback provider.Provider, held []Held, timeout, fallbackTimeout time.Duration, log *slog.Logger) []Change {
	if len(held) == 0 {
		return nil
	}
	prompt := proseDiffPrompt(held)

	verdicts, ok := proseDiffOnce(ctx, p, prompt, timeout)
	if !ok {
		verdicts, ok = proseDiffFallback(ctx, p, fallback, prompt, fallbackTimeout, log)
	}

	var out []Change
	unjudged := 0
	for n, h := range held {
		meaningful, judged := verdicts[n+1]
		if !judged {
			unjudged++
		}
		if !judged || meaningful {
			out = append(out, h.Change)
		}
	}
	if unjudged > 0 && log != nil {
		log.Warn("prose diff: items without a verdict counted as changed", "items", unjudged, "batch", len(held))
	}
	return out
}

// proseDiffFallback runs the fallback attempt after the primary's failed,
// logging the outcome (TDD 9.14).
func proseDiffFallback(ctx context.Context, p, fallback provider.Provider, prompt string, timeout time.Duration, log *slog.Logger) (map[int]bool, bool) {
	if fallback == nil {
		logProseDiff(log, slog.LevelError, "prose diff: primary failed, no fallback configured", p)
		return nil, false
	}
	logProseDiff(log, slog.LevelWarn, "prose diff: primary failed, trying fallback", p)
	verdicts, ok := proseDiffOnce(ctx, fallback, prompt, timeout)
	if ok {
		logProseDiff(log, slog.LevelInfo, "prose diff: fallback succeeded", fallback)
		return verdicts, true
	}
	logProseDiff(log, slog.LevelError, "prose diff: fallback also failed", fallback)
	return nil, false
}

func logProseDiff(log *slog.Logger, level slog.Level, msg string, p provider.Provider) {
	if log == nil {
		return
	}
	name := "unavailable"
	if p != nil {
		name = p.Name()
	}
	log.Log(context.Background(), level, msg, "provider", name)
}
