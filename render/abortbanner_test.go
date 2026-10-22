package render

import (
	"strings"
	"testing"
)

// TestAbortBannerContainsReasonAndTime verifies the banner surfaces the failure
// reason and the run time, and is bracketed by its sentinels (TDD 1.2, 2.4).
func TestAbortBannerContainsReasonAndTime(t *testing.T) {
	b := AbortBanner("acquire: github unreachable", "2026-10-22 09:15:00 MST")
	for _, want := range []string{
		abortBannerStart, abortBannerEnd,
		"acquire: github unreachable",
		"2026-10-22 09:15:00 MST",
		"🛑",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q\n%s", want, b)
		}
	}
}

// TestPrependAbortBannerOntoExisting puts the banner above prior content and
// keeps that content intact.
func TestPrependAbortBannerOntoExisting(t *testing.T) {
	existing := "# 📋 GithubSlashboard\n\nprior body\n"
	banner := AbortBanner("boom", "t0")
	got := PrependAbortBanner(existing, banner)
	if !strings.HasPrefix(got, abortBannerStart) {
		t.Errorf("banner is not at the very top:\n%s", got)
	}
	if !strings.Contains(got, "prior body") {
		t.Errorf("prior content was dropped:\n%s", got)
	}
}

// TestPrependAbortBannerReplacesNotStacks ensures a second abort replaces the
// first banner rather than stacking a pile of them (TDD 1.2).
func TestPrependAbortBannerReplacesNotStacks(t *testing.T) {
	existing := "# 📋 GithubSlashboard\n\nprior body\n"
	once := PrependAbortBanner(existing, AbortBanner("first", "t0"))
	twice := PrependAbortBanner(once, AbortBanner("second", "t1"))
	if n := strings.Count(twice, abortBannerStart); n != 1 {
		t.Errorf("expected exactly one banner after a second abort, got %d\n%s", n, twice)
	}
	if strings.Contains(twice, "first") {
		t.Errorf("stale reason from the first abort should be gone:\n%s", twice)
	}
	if !strings.Contains(twice, "second") {
		t.Errorf("latest reason missing:\n%s", twice)
	}
	if !strings.Contains(twice, "prior body") {
		t.Errorf("prior content lost across replacement:\n%s", twice)
	}
}

// TestPrependAbortBannerOntoEmpty yields a readable placeholder when there is
// no prior document at all.
func TestPrependAbortBannerOntoEmpty(t *testing.T) {
	got := PrependAbortBanner("", AbortBanner("boom", "t0"))
	if !strings.HasPrefix(got, abortBannerStart) {
		t.Errorf("banner not at top:\n%s", got)
	}
	if !strings.Contains(got, "GithubSlashboard") {
		t.Errorf("placeholder heading missing:\n%s", got)
	}
}

// TestStripAbortBannerRemovesOnlyBanner confirms a successful render's full
// overwrite is clean, and that stripping leaves non-banner content untouched.
func TestStripAbortBannerRemovesOnlyBanner(t *testing.T) {
	body := "# 📋 GithubSlashboard\n\nbody\n"
	bannered := PrependAbortBanner(body, AbortBanner("boom", "t0"))
	stripped := StripAbortBanner(bannered)
	if stripped != body {
		t.Errorf("strip did not restore original body.\n got: %q\nwant: %q", stripped, body)
	}
	// Stripping a banner-free document is a no-op.
	if StripAbortBanner(body) != body {
		t.Errorf("strip mutated a banner-free document")
	}
}
