package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ansiEscape matches ANSI/VT100 control sequences. CLI-based providers (e.g.
// Kiro) color and cursor-control their output; these bytes are stripped before
// JSON extraction so a valid object is not broken by escape codes.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// ParseResponse extracts the single fenced JSON object from raw provider output
// and validates it against the vocabularies and word bounds the request
// supplied (TDD 6.4, 6.6). A non-nil error is a validation failure suitable for
// quoting back into a self-correcting retry.
//
// Membership is checked against c rather than against a fixed enum because the
// legal vocabulary is entity-specific (SCHEMA § Response): a PR and an issue
// share this transport but not their action sets.
func ParseResponse(raw string, c Constraints) (Response, error) {
	jsonText, err := extractJSON(ansiEscape.ReplaceAllString(raw, ""))
	if err != nil {
		return Response{}, err
	}

	var resp Response
	dec := json.NewDecoder(strings.NewReader(jsonText))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("response is not valid JSON: %w", err)
	}

	if err := validate(resp, c); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// extractJSON pulls the JSON object body out of raw provider output. It accepts
// a ```json fenced block, a bare ``` fenced block, or a plain object, and takes
// the first complete brace-balanced object it finds so surrounding prose does
// not break parsing.
func extractJSON(raw string) (string, error) {
	s := raw
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		// Skip an optional language tag on the fence line.
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			firstLine := strings.TrimSpace(rest[:nl])
			if firstLine == "" || isLangTag(firstLine) {
				rest = rest[nl+1:]
			}
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			rest = rest[:end]
		}
		s = rest
	}

	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", fmt.Errorf("no JSON object found in provider output")
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("no complete JSON object found in provider output")
}

// isLangTag reports whether a fence's first line looks like a language tag
// (e.g. "json") rather than the start of the object.
func isLangTag(s string) bool {
	if s == "" || strings.ContainsAny(s, "{}\"") {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

// Bucket names that carry coupled fields. They are the same words in both
// entity vocabularies, so the coupling rule is shared even though the action and
// close-reason sets are not (SCHEMA § Response).
const (
	bucketOpen   = "open"
	bucketClosed = "closed"
)

// validate enforces the vocabularies the request supplied, the companion word
// bounds, and the bucket/action/close_reason coupling rules (SCHEMA § Response).
// Checks run in a fixed order — bucket, priority, coupling, companion, emoji —
// so a response with several problems always reports the most structural one
// first, which makes the self-correcting retry's steering text specific.
func validate(resp Response, c Constraints) error {
	if !inVocabulary(resp.Bucket, c.Buckets) {
		return fmt.Errorf("invalid bucket %q, want one of %v", resp.Bucket, c.Buckets)
	}
	if !resp.Priority.Valid() {
		return fmt.Errorf("invalid priority %q", resp.Priority)
	}

	switch resp.Bucket {
	case bucketOpen:
		if !inVocabulary(resp.Action, c.Actions) {
			return fmt.Errorf("bucket open requires an action from %v, got %q", c.Actions, resp.Action)
		}
		if resp.CloseReason != "" {
			return fmt.Errorf("bucket open must not carry close_reason %q", resp.CloseReason)
		}
	case bucketClosed:
		// A close reason is required only when the entity actually infers one. An
		// issue's reason is GitHub's state_reason — a hard fact the classifier
		// records directly — so no vocabulary is offered and none may be
		// returned. Demanding one anyway would make a closed issue impossible to
		// judge at all: the model would be told "closed" is a legal bucket, then
		// rejected for every reason it could supply, and the note would silently
		// degrade to nothing.
		if len(c.CloseReasons) == 0 {
			if resp.CloseReason != "" {
				return fmt.Errorf("close_reason %q is not inferred for this item", resp.CloseReason)
			}
		} else if !inVocabulary(resp.CloseReason, c.CloseReasons) {
			return fmt.Errorf("bucket closed requires a close_reason from %v, got %q", c.CloseReasons, resp.CloseReason)
		}
		if resp.Action != "" {
			return fmt.Errorf("bucket closed must not carry action %q", resp.Action)
		}
	default: // stale, merged
		if resp.Action != "" {
			return fmt.Errorf("bucket %q must not carry action %q", resp.Bucket, resp.Action)
		}
		if resp.CloseReason != "" {
			return fmt.Errorf("bucket %q must not carry close_reason %q", resp.Bucket, resp.CloseReason)
		}
	}

	n := WordCount(resp.Companion)
	if n < c.CompanionWordsMin || n > c.CompanionWordsMax {
		return fmt.Errorf("companion %q has %d words, want %d-%d",
			resp.Companion, n, c.CompanionWordsMin, c.CompanionWordsMax)
	}

	if err := validateEmoji(resp.Emoji); err != nil {
		return err
	}
	return nil
}

// inVocabulary reports whether v is a member of the supplied closed set. An
// empty vocabulary admits nothing, so a request that forgot to name a set fails
// loudly rather than silently accepting anything.
func inVocabulary(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// validateEmoji enforces that emoji is exactly one emoji glyph. It is not a
// closed enum (the model chooses freely), so validation is loose: the value
// must be non-empty, must contain at least one rune in an emoji Unicode range,
// and must be a single visual glyph — i.e. one base emoji rune plus only
// combining/joining companions (variation selectors, skin-tone modifiers, ZWJ
// sequences), not two independent emoji or arbitrary text.
func validateEmoji(s string) error {
	if s == "" {
		return fmt.Errorf("emoji is required")
	}
	base := 0
	hasEmoji := false
	for _, r := range s {
		if isEmojiJoiner(r) {
			continue // variation selector, ZWJ, skin-tone modifier — part of one glyph
		}
		base++
		if isEmojiRune(r) {
			hasEmoji = true
		}
	}
	if !hasEmoji {
		return fmt.Errorf("emoji %q contains no emoji character", s)
	}
	if base > 1 {
		return fmt.Errorf("emoji %q must be a single glyph, got %d base characters", s, base)
	}
	return nil
}

// isEmojiJoiner reports whether r is a combining/joining companion that forms
// part of a single emoji glyph rather than a separate glyph.
func isEmojiJoiner(r rune) bool {
	switch {
	case r == 0x200D: // zero-width joiner
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF: // skin-tone modifiers
		return true
	}
	return false
}

// isEmojiRune reports whether r falls in a common emoji/pictograph Unicode
// range. This is a pragmatic set covering the emoji a model realistically
// returns, not the exhaustive Unicode emoji property (which would need a
// generated table); it is deliberately permissive since the value is decorative.
func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF: // Misc Symbols & Pictographs, Emoticons, Transport, Supplemental, Symbols & Pictographs Extended-A
		return true
	case r >= 0x2600 && r <= 0x27BF: // Misc Symbols + Dingbats (☀ ✅ ✂ …)
		return true
	case r >= 0x2300 && r <= 0x23FF: // Misc Technical (⌚ ⌛ ⏰ ⏳ ⏸ ⏯ …)
		return true
	case r >= 0x25A0 && r <= 0x25FF: // Geometric Shapes (▶ ◀ ● … emoji with VS16)
		return true
	case r >= 0x2190 && r <= 0x21FF: // Arrows (↔ ⤴ rendered as emoji with VS16)
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // Misc Symbols and Arrows (⭐ ⬆ …)
		return true
	case r == 0x203C || r == 0x2049: // ‼ ⁉
		return true
	case r >= 0x2122 && r <= 0x2139: // ™ ℹ
		return true
	case r >= 0x1F000 && r <= 0x1F02F: // Mahjong/dominoes-adjacent pictographs
		return true
	case r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicators (flags)
		return true
	}
	return false
}

// WordCount counts whitespace-separated words in s.
func WordCount(s string) int {
	return len(strings.Fields(s))
}
