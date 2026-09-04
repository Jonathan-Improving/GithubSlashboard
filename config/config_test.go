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
}

func TestLoadBadDuration(t *testing.T) {
	if _, err := Load(fakeEnv(map[string]string{
		EnvGitHubToken:       "tok",
		EnvStaleAgeThreshold: "notaduration",
	})); err == nil {
		t.Error("bad duration should error")
	}
}

func TestProviderModelDefaultAndOverride(t *testing.T) {
	c, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Model != DefaultProviderModel {
		t.Errorf("default model = %q, want %q", c.Model, DefaultProviderModel)
	}

	c, err = Load(fakeEnv(map[string]string{
		EnvGitHubToken:   "tok",
		EnvProviderModel: "glm-4.7-flash",
	}))
	if err != nil {
		t.Fatalf("Load with model override: %v", err)
	}
	if c.Model != "glm-4.7-flash" {
		t.Errorf("model = %q, want glm-4.7-flash", c.Model)
	}
}

func TestFallbackProviderUnsetByDefault(t *testing.T) {
	// TDD 6.13: an unset fallback means today's exhaustion-to-unverified
	// behavior, unchanged.
	c, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.FallbackProvider != "" || c.FallbackModel != "" {
		t.Errorf("fallback should default unset, got provider=%q model=%q", c.FallbackProvider, c.FallbackModel)
	}
}

func TestFallbackProviderOverride(t *testing.T) {
	// TDD 6.12: the fallback slot takes a name and model, symmetric with the
	// primary — the same two knobs, just under the GSB_FALLBACK_* names.
	c, err := Load(fakeEnv(map[string]string{
		EnvGitHubToken:           "tok",
		EnvFallbackProvider:      "ollama",
		EnvFallbackProviderModel: "glm-4.7-flash",
	}))
	if err != nil {
		t.Fatalf("Load with fallback: %v", err)
	}
	if c.FallbackProvider != "ollama" {
		t.Errorf("fallback provider = %q, want ollama", c.FallbackProvider)
	}
	if c.FallbackModel != "glm-4.7-flash" {
		t.Errorf("fallback model = %q, want glm-4.7-flash", c.FallbackModel)
	}
}

func TestFallbackProviderTimeoutDefaultAndOverride(t *testing.T) {
	// TDD 6.17: the fallback gets its own timeout, distinct from and by
	// default longer than the primary's.
	c, err := Load(fakeEnv(map[string]string{EnvGitHubToken: "tok"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.FallbackProviderTimeout != DefaultFallbackProviderTimeout {
		t.Errorf("default fallback provider timeout = %s, want %s", c.FallbackProviderTimeout, DefaultFallbackProviderTimeout)
	}
	if c.FallbackProviderTimeout <= c.ProviderTimeout {
		t.Errorf("default fallback timeout (%s) should exceed the primary's (%s)", c.FallbackProviderTimeout, c.ProviderTimeout)
	}

	c, err = Load(fakeEnv(map[string]string{
		EnvGitHubToken:             "tok",
		EnvFallbackProviderTimeout: "5m",
	}))
	if err != nil {
		t.Fatalf("Load with fallback timeout override: %v", err)
	}
	if c.FallbackProviderTimeout != 5*time.Minute {
		t.Errorf("fallback provider timeout = %s, want 5m", c.FallbackProviderTimeout)
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
