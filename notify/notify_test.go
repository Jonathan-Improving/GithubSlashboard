package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// fakeSummarizer implements the one method notify.Summarize needs from
// provider.Provider, so tests do not need a full Provider fake.
type fakeSummarizer struct {
	out string
	err error
}

func (f fakeSummarizer) Name() string { return "fake" }
func (f fakeSummarizer) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	return "", errors.New("not used")
}
func (f fakeSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	return f.out, f.err
}

func TestChangesFromPRsFiltersByWasJudged(t *testing.T) {
	prs := []model.PR{
		{Repo: "o/n", Number: 1, Role: model.RoleSubmitter, Bucket: model.BucketOpen, Action: model.ActionAwaitingReview, WasJudged: true},
		{Repo: "o/n", Number: 2, Role: model.RoleSubmitter, Bucket: model.BucketOpen, Action: model.ActionMergeReady, WasJudged: false},
		{Repo: "o/n", Number: 3, Role: model.RoleReviewer, Bucket: model.BucketMerged, WasJudged: false},
	}
	got := ChangesFromPRs(prs)
	if len(got) != 1 {
		t.Fatalf("got %d changes, want 1 (only WasJudged=true PRs)", len(got))
	}
	if got[0].Number != 1 || got[0].Entity != entityPR {
		t.Errorf("unexpected change: %+v", got[0])
	}
}

func TestChangesFromIssuesFiltersByWasJudged(t *testing.T) {
	issues := []model.Issue{
		{Repo: "o/n", Number: 10, Role: model.IssueRoleAuthor, Bucket: model.IssueBucketOpen, Action: model.IssueActionTriage, WasJudged: true},
		{Repo: "o/n", Number: 11, Role: model.IssueRoleParticipant, Bucket: model.IssueBucketOpen, WasJudged: false},
	}
	got := ChangesFromIssues(issues)
	if len(got) != 1 {
		t.Fatalf("got %d changes, want 1", len(got))
	}
	if got[0].Number != 10 || got[0].Entity != entityIssue {
		t.Errorf("unexpected change: %+v", got[0])
	}
}

func TestChangesFromPRsEmptyWhenNoneJudged(t *testing.T) {
	prs := []model.PR{
		{Repo: "o/n", Number: 1, WasJudged: false},
	}
	if got := ChangesFromPRs(prs); len(got) != 0 {
		t.Errorf("got %d changes, want 0", len(got))
	}
}

func TestSummarizeReturnsProviderSentence(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{out: `{"summary":"1 PR needs review"}`}
	got := Summarize(context.Background(), p, nil, changed, time.Second, time.Second, nil)
	if got != "1 PR needs review" {
		t.Errorf("Summarize = %q, want the parsed sentence", got)
	}
}

func TestSummarizeAcceptsRawTextResponse(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{out: "1 PR needs review"}
	got := Summarize(context.Background(), p, nil, changed, time.Second, time.Second, nil)
	if got != "1 PR needs review" {
		t.Errorf("Summarize = %q, want the raw text trimmed", got)
	}
}

func TestSummarizeFallsBackToPlainStringOnError(t *testing.T) {
	// No fallback provider configured (nil): a primary failure with no
	// fallback goes straight to the plain non-model string (TDD 6.9, 6.13).
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{err: errors.New("provider unavailable")}
	got := Summarize(context.Background(), p, nil, changed, time.Second, time.Second, nil)
	if got != "o/n#1 changed" {
		t.Errorf("Summarize fallback = %q, want the single-item fallback sentence", got)
	}
}

func TestSummarizeFallsBackToPlainStringOnEmptyResponse(t *testing.T) {
	changed := []Change{
		{Entity: entityPR, Repo: "o/n", Number: 1},
		{Entity: entityIssue, Repo: "o/n", Number: 2},
	}
	p := fakeSummarizer{out: "   "}
	got := Summarize(context.Background(), p, nil, changed, time.Second, time.Second, nil)
	if got != "2 items changed" {
		t.Errorf("Summarize fallback = %q, want the multi-item fallback sentence", got)
	}
}

func TestSummarizeSkippedWhenNoChanges(t *testing.T) {
	p := fakeSummarizer{out: "should not be called"}
	got := Summarize(context.Background(), p, nil, nil, time.Second, time.Second, nil)
	if got != "" {
		t.Errorf("Summarize with no changes = %q, want empty (never call the provider for nothing)", got)
	}
}

func TestSummarizeTriesFallbackWhenPrimaryFails(t *testing.T) {
	// TDD 6.15: a Summarize fallback uses the same trigger rule as
	// classification, applied to Summarize's own single-attempt shape.
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{err: errors.New("primary unavailable")}
	fb := fakeSummarizer{out: "1 PR needs review"}
	got := Summarize(context.Background(), p, fb, changed, time.Second, time.Second, nil)
	if got != "1 PR needs review" {
		t.Errorf("Summarize = %q, want the fallback's sentence", got)
	}
}

func TestSummarizeFallsBackToPlainStringWhenFallbackAlsoFails(t *testing.T) {
	// TDD 6.14 (Summarize's equivalent): both attempts fail, so the plain
	// non-model string is used, exactly as if no fallback were configured.
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{err: errors.New("primary unavailable")}
	fb := fakeSummarizer{err: errors.New("fallback unavailable too")}
	got := Summarize(context.Background(), p, fb, changed, time.Second, time.Second, nil)
	if got != "o/n#1 changed" {
		t.Errorf("Summarize = %q, want the plain fallback sentence", got)
	}
}

func TestSummarizeNeverTriesFallbackWhenPrimarySucceeds(t *testing.T) {
	// A configured fallback must not be touched at all when the primary
	// succeeds — prove it by making the fallback panic if called.
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	p := fakeSummarizer{out: "1 PR needs review"}
	fb := panicSummarizer{}
	got := Summarize(context.Background(), p, fb, changed, time.Second, time.Second, nil)
	if got != "1 PR needs review" {
		t.Errorf("Summarize = %q, want the primary's sentence", got)
	}
}

// panicSummarizer fails the test loudly if Summarize is ever called on it,
// proving a configured fallback is never touched while the primary succeeds.
type panicSummarizer struct{}

func (panicSummarizer) Name() string { return "panic" }
func (panicSummarizer) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	panic("Invoke should never be called on panicSummarizer")
}
func (panicSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	panic("Summarize should never be called when the primary already succeeded")
}

func TestFireSkippedWhenNoChanges(t *testing.T) {
	// A hook command that would fail loudly if ever invoked, proving Fire never
	// runs it when changed is empty (TDD 9.1).
	if err := Fire(context.Background(), "exit 1", time.Second, nil, "summary"); err != nil {
		t.Errorf("Fire with no changes should no-op, got err: %v", err)
	}
}

func TestFireSkippedWhenHookUnconfigured(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	if err := Fire(context.Background(), "", time.Second, changed, "summary"); err != nil {
		t.Errorf("Fire with empty hook command should no-op, got err: %v", err)
	}
}

func TestFireDeliversJSONOnStdin(t *testing.T) {
	// A real, tiny shell script that captures its stdin to a temp file, so this
	// test proves actual delivery through exec, not just the Go-side marshaling.
	tmp, err := os.CreateTemp("", "gsb-notify-test-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	changed := []Change{
		{Entity: entityPR, Repo: "o/n", Number: 42, URL: "https://github.com/o/n/pull/42", Role: "submitter", Bucket: "open", Action: "review_feedback", Companion: "needs a look", Priority: "elevated"},
	}
	hookCmd := "cat > " + tmp.Name()
	if err := Fire(context.Background(), hookCmd, 2*time.Second, changed, "1 PR needs review"); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	raw, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	var got Payload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("captured stdin did not parse as the payload: %v (raw=%s)", err, raw)
	}
	if got.Summary != "1 PR needs review" {
		t.Errorf("summary = %q", got.Summary)
	}
	if len(got.Changed) != 1 || got.Changed[0].Number != 42 {
		t.Errorf("changed = %+v", got.Changed)
	}
}

func TestFireReturnsErrorOnNonZeroExit(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	err := Fire(context.Background(), "exit 3", time.Second, changed, "summary")
	if err == nil {
		t.Error("Fire should return an error when the hook command exits non-zero")
	}
}

func TestFireReturnsErrorOnTimeout(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	err := Fire(context.Background(), "sleep 5", 20*time.Millisecond, changed, "summary")
	if err == nil {
		t.Error("Fire should return an error when the hook command exceeds the timeout")
	}
}

// TestFireFailureIsNonFatal is a documentation-style test proving the contract
// at the call-site level: Fire returns a plain error rather than panicking or
// calling os.Exit, so the caller (main.go's fireNotifyHook) can log it and
// continue (TDD 9.5). There is nothing to assert beyond "it returns," but that
// is the point — a failing hook must never take down the process.
func TestFireFailureIsNonFatal(t *testing.T) {
	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1}}
	err := Fire(context.Background(), "exit 1", time.Second, changed, "summary")
	if err == nil {
		t.Fatal("expected an error from the failing hook")
	}
	// Reaching this line at all is the assertion: Fire returned control to the
	// caller instead of aborting the process.
}

// TestFireDeliversAdversarialContentAsDataNotCode covers TDD 9.6: a companion
// note and summary containing shell metacharacters, command substitution, and
// quote-breaking sequences — the shape of text a maliciously crafted GitHub PR
// comment could produce once fed through the model — must be stripped of
// those metacharacters before the payload is ever built, so even a downstream
// consumer that blindly forwards the JSON to a shell/eval cannot be tricked
// into command injection. The hook command here is deliberately a fixed,
// harmless probe; if the adversarial content ever reached the command line
// instead of being sanitized away, it would execute and the captured
// proof-file would exist.
func TestFireDeliversAdversarialContentAsDataNotCode(t *testing.T) {
	proof, err := os.CreateTemp("", "gsb-notify-proof-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	proofPath := proof.Name()
	proof.Close()
	os.Remove(proofPath) // must NOT be recreated by the adversarial payload

	capture, err := os.CreateTemp("", "gsb-notify-capture-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	capture.Close()
	defer os.Remove(capture.Name())

	adversarial := "note`touch " + proofPath + "` and $(touch " + proofPath + ") and \"; touch " + proofPath + " #"
	changed := []Change{
		{Entity: entityPR, Repo: "o/n", Number: 1, Companion: adversarial},
	}
	summary := "summary`touch " + proofPath + "`"

	hookCmd := "cat > " + capture.Name()
	if err := Fire(context.Background(), hookCmd, 2*time.Second, changed, summary); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	if _, err := os.Stat(proofPath); err == nil {
		t.Fatal("adversarial content in Companion/summary executed as a command — sanitization must remove metacharacters before the payload is built (TDD 9.6)")
	}

	raw, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	var got Payload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("captured stdin did not parse as JSON — adversarial content corrupted the payload: %v (raw=%s)", err, raw)
	}
	// The sanitized companion/summary must contain none of the dangerous
	// characters from the original adversarial string.
	for _, dangerous := range []string{"`", "$", "(", ")", ";"} {
		if strings.Contains(got.Changed[0].Companion, dangerous) {
			t.Errorf("sanitized companion %q still contains dangerous character %q", got.Changed[0].Companion, dangerous)
		}
		if strings.Contains(got.Summary, dangerous) {
			t.Errorf("sanitized summary %q still contains dangerous character %q", got.Summary, dangerous)
		}
	}
	// The safe prose content must still be present.
	if !strings.Contains(got.Changed[0].Companion, "note") || !strings.Contains(got.Changed[0].Companion, "and") {
		t.Errorf("sanitized companion %q lost its safe prose content", got.Changed[0].Companion)
	}
}

func TestSanitizeTextPreservesNaturalLanguageAndEmoji(t *testing.T) {
	in := "3 PRs need review 🚀 — café résumé naïve, done!"
	got := sanitizeText(in)
	if got != in {
		t.Errorf("sanitizeText altered safe natural-language text (accented letters and em dash should pass): got %q, want %q", got, in)
	}
}

func TestSanitizeTextStripsShellMetacharacters(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a`b`c", "abc"},
		{"a$(b)c", "abc"},
		{"a;b|c&d", "abcd"},
		{"a<b>c", "abc"},
		{`a\b`, "ab"},
		{"a{b}c[d]e", "abcde"},
	}
	for _, tc := range cases {
		got := sanitizeText(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeTextPreservesQuotesAndApostrophes(t *testing.T) {
	// Quotes are ordinary prose (contractions, quoted phrases, possessives)
	// and are not stripped: on their own, with command substitution/chaining
	// already removed, a lone quote cannot invoke a command — see
	// sanitizeText's doc comment for the reasoning.
	in := `don't say "hello" — it's fine`
	got := sanitizeText(in)
	if got != in {
		t.Errorf("sanitizeText altered quotes/apostrophes: got %q, want %q", got, in)
	}
}

func TestSanitizeTextStripsControlCharacters(t *testing.T) {
	in := "hello\x00\x01\x1bworld"
	got := sanitizeText(in)
	if got != "helloworld" {
		t.Errorf("sanitizeText did not strip control characters: got %q", got)
	}
}

func TestFireSanitizesBeforeBuildingPayload(t *testing.T) {
	// A more direct unit check than the adversarial-execution test above:
	// confirms the specific field-level transformation Fire applies.
	tmp, err := os.CreateTemp("", "gsb-notify-sanitize-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	changed := []Change{{Entity: entityPR, Repo: "o/n", Number: 1, Companion: "fix `rm -rf /`"}}
	hookCmd := "cat > " + tmp.Name()
	if err := Fire(context.Background(), hookCmd, time.Second, changed, "ok $(whoami)"); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	raw, _ := os.ReadFile(tmp.Name())
	var got Payload
	json.Unmarshal(raw, &got)
	if got.Changed[0].Companion != "fix rm -rf /" {
		t.Errorf("companion = %q, want sanitized %q", got.Changed[0].Companion, "fix rm -rf /")
	}
	if got.Summary != "ok whoami" {
		t.Errorf("summary = %q, want sanitized %q", got.Summary, "ok whoami")
	}
}

// TestSummaryPromptSanitizesCompanionOnTheWayIn covers the preventative half
// of TDD 9.6: a companion note carrying dangerous characters is sanitized
// before it ever becomes part of the prompt text sent to the provider, not
// only sanitized after the provider responds. This reduces the chance the
// model's own response needs sanitizing at all, and narrows the
// prompt-injection surface a crafted GitHub comment could exploit to steer
// the summary call.
func TestSummaryPromptSanitizesCompanionOnTheWayIn(t *testing.T) {
	changed := []Change{
		{Entity: entityPR, Repo: "o/n", Number: 1, Bucket: "open", Companion: "note`rm -rf /`and $(whoami)"},
	}
	prompt := summaryPrompt(changed)
	// Scope the check to the per-item line the Companion was interpolated
	// into, not the whole prompt — the fixed instructional preamble is
	// developer-authored trusted text and legitimately contains parentheses.
	lines := strings.Split(prompt, "\n")
	var itemLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "- ") {
			itemLine = l
			break
		}
	}
	if itemLine == "" {
		t.Fatalf("no item line found in prompt: %s", prompt)
	}
	for _, dangerous := range []string{"`", "$", "(", ")"} {
		if strings.Contains(itemLine, dangerous) {
			t.Errorf("item line still contains dangerous character %q: %s", dangerous, itemLine)
		}
	}
	if !strings.Contains(itemLine, "note") || !strings.Contains(itemLine, "and") {
		t.Errorf("item line lost the safe prose content: %s", itemLine)
	}
}
