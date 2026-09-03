package config

import (
	"testing"
	"time"
)

func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.CompanionWordsMin != DefaultCompanionWordsMin || c.CompanionWordsMax != DefaultCompanionWordsMax {
		t.Errorf("word bounds = %d-%d", c.CompanionWordsMin, c.CompanionWordsMax)
	}
	if c.LLMRetryCap != DefaultLLMRetryCap {
		t.Errorf("retry cap = %d", c.LLMRetryCap)
	}
	if c.Provider != DefaultProvider {
		t.Errorf("provider = %q", c.Provider)
	}
}

func TestLoadRequiresToken(t *testing.T) {
	if _, err := Load(fakeEnv(map[string]string{})); err == nil {
		t.Error("Load without token should error")
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(fakeEnv(map[string]string{
		EnvGitHubToken:       "tok",
		EnvProvider:          "grok",
		EnvStorePath:         "/tmp/s.yaml",
		EnvOutputPath:        "/tmp/o.md",
		EnvStaleAgeThreshold: "24h",
		EnvProviderCommand:   "grok chat --stdin",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Provider != "grok" || c.StorePath != "/tmp/s.yaml" || c.OutputPath != "/tmp/o.md" {
		t.Errorf("overrides not applied: %+v", c)
	}
	if c.StaleAgeThreshold != 24*time.Hour {
		t.Errorf("stale threshold = %s", c.StaleAgeThreshold)
	}
	if len(c.ProviderCommand) != 3 || c.ProviderCommand[0] != "grok" {
		t.Errorf("provider command = %v", c.ProviderCommand)
	}
}

func TestLoadBadDuration(t *testing.T) {
	if _, err := Load(fakeEnv(map[string]string{
		EnvGitHubToken:       "tok",
		EnvStaleAgeThreshold: "notaduration",
	})); err == nil {
		t.Error("bad duration should error")
	}
}

func TestProviderKindDefaultAndOverride(t *testing.T) {
	c, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ProviderKind != DefaultProviderKind {
		t.Errorf("default provider kind = %q, want %q", c.ProviderKind, DefaultProviderKind)
	}

	c, err = Load(fakeEnv(map[string]string{
		EnvGitHubToken:  "tok",
		EnvProviderKind: string(ProviderKindOneShot),
	}))
	if err != nil {
		t.Fatalf("Load oneshot: %v", err)
	}
	if c.ProviderKind != ProviderKindOneShot {
		t.Errorf("provider kind = %q, want oneshot", c.ProviderKind)
	}
}

func TestProviderKindInvalid(t *testing.T) {
	if _, err := Load(fakeEnv(map[string]string{
		EnvGitHubToken:  "tok",
		EnvProviderKind: "bogus",
	})); err == nil {
		t.Error("invalid provider kind should error")
	}
	if _, err := ParseProviderKind("bogus"); err == nil {
		t.Error("ParseProviderKind should reject an out-of-set value")
	}
}

func TestClassifyFloorNotesToggle(t *testing.T) {
	// Default: skip floor notes (fast).
	c, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SkipFloorNotes != DefaultClassifySkipFloorNotes {
		t.Errorf("SkipFloorNotes default = %v, want %v", c.SkipFloorNotes, DefaultClassifySkipFloorNotes)
	}
	// Env names the positive behavior (produce floor notes) -> SkipFloorNotes false.
	c, err = Load(fakeEnv(map[string]string{EnvGitHubToken: "tok", EnvClassifyFloorNotes: "true"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SkipFloorNotes {
		t.Error("GSB_CLASSIFY_FLOOR_NOTES=true should set SkipFloorNotes=false")
	}
	// The opposite override: false means skip floor notes (favor run time).
	c, err = Load(fakeEnv(map[string]string{EnvGitHubToken: "tok", EnvClassifyFloorNotes: "false"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.SkipFloorNotes {
		t.Error("GSB_CLASSIFY_FLOOR_NOTES=false should set SkipFloorNotes=true")
	}
	if _, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok", EnvClassifyFloorNotes: "maybe"})); err == nil {
		t.Error("invalid bool should error")
	}
}

func TestIncludeTerminalToggle(t *testing.T) {
	c, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.IncludeTerminal {
		t.Error("IncludeTerminal should default false (skip terminal PRs)")
	}
	c, err = Load(fakeEnv(map[string]string{EnvGitHubToken: "tok", EnvIncludeTerminal: "true"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.IncludeTerminal {
		t.Error("GSB_INCLUDE_TERMINAL=true should set IncludeTerminal")
	}
	if _, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok", EnvIncludeTerminal: "nope"})); err == nil {
		t.Error("invalid bool should error")
	}
}

func TestValidate(t *testing.T) {
	c := Default()
	c.GitHubToken = "tok"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	bad := c
	bad.CompanionWordsMax = 1
	bad.CompanionWordsMin = 5
	if err := bad.Validate(); err == nil {
		t.Error("max<min should error")
	}

	bad = c
	bad.StaleAgeThreshold = 0
	if err := bad.Validate(); err == nil {
		t.Error("zero stale threshold should error")
	}
}
