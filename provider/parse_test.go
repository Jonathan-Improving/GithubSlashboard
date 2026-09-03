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
