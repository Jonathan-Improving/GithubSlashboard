package config

import (
	"testing"
	"time"
)

// TestIssueStaleAgeThresholdDefaultAndOverride covers TDD 8.5's configuration
// half: the issue threshold defaults to 120 days, is independent of the PR
// threshold, and is overridable by its own environment variable.
func TestIssueStaleAgeThresholdDefaultAndOverride(t *testing.T) {
	base := map[string]string{EnvGitHubToken: "tok"}
	get := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	c, err := Load(get(base))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.IssueStaleAgeThreshold != DefaultIssueStaleAgeThreshold {
		t.Errorf("default issue threshold = %s, want %s", c.IssueStaleAgeThreshold, DefaultIssueStaleAgeThreshold)
	}
	if c.IssueStaleAgeThreshold == c.StaleAgeThreshold {
		t.Error("issue threshold equals the PR threshold; they must be independent (TDD 8.5)")
	}

	env := map[string]string{EnvGitHubToken: "tok", EnvIssueStaleAgeThreshold: "1440h"}
	c, err = Load(get(env))
	if err != nil {
		t.Fatalf("load with override: %v", err)
	}
	if c.IssueStaleAgeThreshold != 1440*time.Hour {
		t.Errorf("issue threshold = %s, want 1440h from %s", c.IssueStaleAgeThreshold, EnvIssueStaleAgeThreshold)
	}
	if c.StaleAgeThreshold != DefaultStaleAgeThreshold {
		t.Errorf("PR threshold changed to %s; the issue override must not touch it", c.StaleAgeThreshold)
	}

	bad := map[string]string{EnvGitHubToken: "tok", EnvIssueStaleAgeThreshold: "not-a-duration"}
	if _, err := Load(get(bad)); err == nil {
		t.Errorf("expected an error for an invalid %s duration", EnvIssueStaleAgeThreshold)
	}
}
