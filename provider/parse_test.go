package provider

import (
	"testing"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

func TestParseResponseFencedJSON(t *testing.T) {
	raw := "Here is my answer:\n```json\n{\"bucket\":\"open\",\"action\":\"blocked_external\",\"priority\":\"neutral\",\"companion\":\"waiting on John review\",\"emoji\":\"⏳\"}\n```\nThanks!"
	resp, err := ParseResponse(raw, ConstraintsFrom(3, 8))
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Bucket != string(model.BucketOpen) || resp.Action != string(model.ActionBlockedExternal) {
		t.Errorf("parsed wrong: %+v", resp)
	}
}

func TestParseResponseBareObject(t *testing.T) {
	raw := `{"bucket":"merged","priority":"elevated","companion":"shipped last Tuesday afternoon","emoji":"📦"}`
	resp, err := ParseResponse(raw, ConstraintsFrom(3, 8))
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Bucket != string(model.BucketMerged) || resp.Priority != model.PriorityElevated {
		t.Errorf("parsed wrong: %+v", resp)
	}
}

func TestParseResponseInvalidEnum(t *testing.T) {
	raw := `{"bucket":"exploded","priority":"neutral","companion":"three word note here"}`
	if _, err := ParseResponse(raw, ConstraintsFrom(3, 8)); err == nil {
		t.Error("invalid bucket should fail validation (TDD 6.6)")
	}
}

func TestParseResponseWordBounds(t *testing.T) {
	tooShort := `{"bucket":"merged","priority":"neutral","companion":"one two"}`
	if _, err := ParseResponse(tooShort, ConstraintsFrom(3, 8)); err == nil {
		t.Error("companion below min words should fail (TDD 6.4)")
	}
	tooLong := `{"bucket":"merged","priority":"neutral","companion":"one two three four five six seven eight nine"}`
	if _, err := ParseResponse(tooLong, ConstraintsFrom(3, 8)); err == nil {
		t.Error("companion above max words should fail (TDD 6.4)")
	}
}

func TestParseResponseBucketCoupling(t *testing.T) {
	// open must carry a valid action
	if _, err := ParseResponse(`{"bucket":"open","priority":"neutral","companion":"three word note here"}`, ConstraintsFrom(3, 8)); err == nil {
		t.Error("open without action should fail")
	}
	// closed must carry a valid close_reason
	if _, err := ParseResponse(`{"bucket":"closed","priority":"neutral","companion":"three word note here"}`, ConstraintsFrom(3, 8)); err == nil {
		t.Error("closed without close_reason should fail")
	}
	// open must not carry close_reason
	if _, err := ParseResponse(`{"bucket":"open","action":"merge_ready","close_reason":"stale","priority":"neutral","companion":"three word note here"}`, ConstraintsFrom(3, 8)); err == nil {
		t.Error("open with close_reason should fail")
	}
}

func TestParseResponseNoJSON(t *testing.T) {
	if _, err := ParseResponse("I could not decide.", ConstraintsFrom(3, 8)); err == nil {
		t.Error("output with no JSON object should error")
	}
}

func TestWordCount(t *testing.T) {
	if WordCount("  one   two three ") != 3 {
		t.Error("WordCount mishandles whitespace")
	}
}

func TestParseResponseStripsANSI(t *testing.T) {
	// Mimics kiro-cli output: an ANSI-colored "> " prompt prefix around the JSON.
	raw := "\x1b[38;5;141m> \x1b[0m{\"bucket\":\"open\",\"action\":\"awaiting_review\",\"priority\":\"neutral\",\"companion\":\"waiting on a reviewer\",\"emoji\":\"⏳\"}"
	resp, err := ParseResponse(raw, ConstraintsFrom(3, 8))
	if err != nil {
		t.Fatalf("ParseResponse on ANSI-wrapped output: %v", err)
	}
	if resp.Bucket != string(model.BucketOpen) {
		t.Errorf("parsed wrong: %+v", resp)
	}
}

func TestParseResponseRequiresEmoji(t *testing.T) {
	// A response missing the emoji field fails validation and drives a retry.
	raw := `{"bucket":"merged","priority":"neutral","companion":"three word note"}`
	if _, err := ParseResponse(raw, ConstraintsFrom(3, 8)); err == nil {
		t.Error("missing emoji should fail validation")
	}
}

func TestParseResponseRejectsNonEmoji(t *testing.T) {
	raw := `{"bucket":"merged","priority":"neutral","companion":"three word note","emoji":"x"}`
	if _, err := ParseResponse(raw, ConstraintsFrom(3, 8)); err == nil {
		t.Error("a non-emoji character should fail validation")
	}
}

func TestParseResponseRejectsMultipleEmoji(t *testing.T) {
	raw := `{"bucket":"merged","priority":"neutral","companion":"three word note","emoji":"✅🔥"}`
	if _, err := ParseResponse(raw, ConstraintsFrom(3, 8)); err == nil {
		t.Error("two emoji should fail the single-glyph check")
	}
}

func TestValidateEmojiAcceptsGlyphs(t *testing.T) {
	// Includes a ZWJ sequence and a variation-selector emoji as single glyphs.
	for _, ok := range []string{"✅", "🔧", "⏳", "🎉", "▶️", "👍🏽"} {
		if err := validateEmoji(ok); err != nil {
			t.Errorf("validateEmoji(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "x", "ab", "✅✅"} {
		if err := validateEmoji(bad); err == nil {
			t.Errorf("validateEmoji(%q) = nil, want error", bad)
		}
	}
}

// TestParseResponseSkipsThinkingTraceToFinalAnswer covers a real failure mode
// found live against Ollama-served GLM-4.7-Flash (a "thinking" model): its
// reasoning trace can draft/revise a scratch JSON answer, echo the prompt's
// own "respond with a fenced ```json object" instruction back verbatim, and
// write a malformed template example whose internal quoting is not
// pairwise-balanced — any of which can desync a naive brace/quote scanner
// for everything that follows. Ollama's CLI convention for such models emits
// a literal "...done thinking." line before the real answer; extractJSON
// only searches after it when present, sidestepping the reasoning trace's
// unreliable quoting rather than trying to parse through it.
func TestParseResponseSkipsThinkingTraceToFinalAnswer(t *testing.T) {
	raw := "Thinking...\n" +
		// A malformed template example inside the reasoning: an odd number of
		// literal quotes, which would desync a whole-text quote-parity scanner.
		"The events array looks like `{\"timestamp\": \"...\", \"text\": \"\n\"ci: passing\"}]`\n" +
		// A draft/scratch answer the model later revises — must not be picked.
		"Draft:\n```json\n{\"bucket\":\"open\",\"action\":\"changes_requested\",\"priority\":\"elevated\",\"companion\":\"draft note, not final\",\"emoji\":\"🔧\"}\n```\n" +
		"Actually, let me reconsider...\n" +
		"...done thinking.\n\n" +
		"```json\n{\"bucket\":\"open\",\"action\":\"merge_ready\",\"priority\":\"neutral\",\"companion\":\"final answer after reconsidering\",\"emoji\":\"✅\"}\n```\n"

	resp, err := ParseResponse(raw, ConstraintsFrom(3, 14))
	if err != nil {
		t.Fatalf("ParseResponse failed on a thinking-trace transcript: %v", err)
	}
	if resp.Action != string(model.ActionMergeReady) {
		t.Errorf("action = %q, want the post-\"done thinking\" final answer (merge_ready), not the pre-cutoff draft (changes_requested)", resp.Action)
	}
	if resp.Companion != "final answer after reconsidering" {
		t.Errorf("companion = %q, want the final answer's note, not the draft's", resp.Companion)
	}
}

// TestParseResponseWithoutThinkingMarkerUnaffected confirms the fix is
// additive: output with no "done thinking" marker at all (the one-shot Kiro
// path, which never narrates) is scanned exactly as before.
func TestParseResponseWithoutThinkingMarkerUnaffected(t *testing.T) {
	raw := "Here is my answer:\n```json\n{\"bucket\":\"open\",\"action\":\"blocked_external\",\"priority\":\"neutral\",\"companion\":\"waiting on John review\",\"emoji\":\"⏳\"}\n```\nThanks!"
	resp, err := ParseResponse(raw, ConstraintsFrom(3, 8))
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Action != string(model.ActionBlockedExternal) {
		t.Errorf("parsed wrong: %+v", resp)
	}
}
