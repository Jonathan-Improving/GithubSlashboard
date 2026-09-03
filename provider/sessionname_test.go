package provider

import (
	"strings"
	"testing"
)

func TestRandomSuffixFormat(t *testing.T) {
	const n = 6
	s := randomSuffix(n)
	if len(s) != n {
		t.Fatalf("suffix length = %d, want %d", len(s), n)
	}
	for _, r := range s {
		if !strings.ContainsRune(sessionSuffixAlphabet, r) {
			t.Errorf("suffix contains out-of-alphabet rune %q", r)
		}
	}
}

func TestUniqueTmuxSessionNamePrefixAndShape(t *testing.T) {
	// With a tmux binary that does not exist, has-session always fails, so the
	// first candidate is accepted — exercising the happy path without a live
	// tmux server.
	name := uniqueTmuxSessionName("tmux-does-not-exist-gsb-test")
	if !strings.HasPrefix(name, tmuxSessionPrefix) {
		t.Fatalf("name %q missing prefix %q", name, tmuxSessionPrefix)
	}
	suffix := strings.TrimPrefix(name, tmuxSessionPrefix)
	if len(suffix) != sessionSuffixLen {
		t.Errorf("suffix %q length = %d, want %d", suffix, len(suffix), sessionSuffixLen)
	}
	for _, r := range suffix {
		if !strings.ContainsRune(sessionSuffixAlphabet, r) {
			t.Errorf("suffix contains out-of-alphabet rune %q", r)
		}
	}
}

func TestRandomSuffixVariesAcrossCalls(t *testing.T) {
	// Not a strict guarantee, but a collision across many draws of a 6-char
	// 62-symbol suffix is astronomically unlikely; catches a broken RNG wiring.
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		s := randomSuffix(sessionSuffixLen)
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate suffix %q within 100 draws — RNG likely broken", s)
		}
		seen[s] = struct{}{}
	}
}
