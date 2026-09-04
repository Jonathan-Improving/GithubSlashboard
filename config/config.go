// Package config loads and validates configuration and owns the named
// constants that carry domain meaning (POLICY: word bounds, retry cap, stale
// age threshold, provider selection live in config, never as literals in
// logic). Rubrics reference these by name (TDD § Configurable constants).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/schedule"
)

// Default constant values (TDD § Configurable constants referenced below).
const (
	// DefaultCompanionWordsMin / Max bound inferred companion prose (TDD 4.5).
	// Max is set with headroom (not a tight 8) because the model naturally
	// writes a one-line note of ~10-12 words; too tight a cap makes valid
	// classifications fail the word-bound check on every retry and fall to
	// unverified — an over-constraint, not an inference failure.
	DefaultCompanionWordsMin = 3
	DefaultCompanionWordsMax = 14

	// DefaultLLMRetryCap is the max self-correcting re-invocations after the
	// first attempt before a row is marked unverified (TDD 6.4). Default 2 =
	// 3 total attempts.
	DefaultLLMRetryCap = 2

	// DefaultStaleAgeThreshold is the no-activity duration after which a PR is
	// Stale (TDD 5.2).
	DefaultStaleAgeThreshold = 40 * 24 * time.Hour // 40 days

	// DefaultIssueStaleAgeThreshold is the no-activity duration after which an
	// issue is Stale (TDD 8.5). It is deliberately far longer than the PR
	// threshold: an issue legitimately sits idle for months without being
	// abandoned, whereas a PR that quiet has usually been dropped.
	DefaultIssueStaleAgeThreshold = 120 * 24 * time.Hour // 120 days

	// DefaultProviderTimeout bounds a single provider subprocess call (TDD 6.5).
	DefaultProviderTimeout = 90 * time.Second

	// DefaultFallbackProviderTimeout bounds a single fallback provider call
	// independently of the primary's own timeout (TDD 6.17). Set higher than
	// DefaultProviderTimeout: live verification against real prompts (not
	// synthetic ones) showed a one-shot local-model fallback (Ollama) can
	// legitimately take well over 90s per call, while the primary's own
	// timeout must stay tight to fail over promptly on a genuinely hung
	// session. Three times the primary's default gives real headroom without
	// making a stuck fallback block a run indefinitely.
	DefaultFallbackProviderTimeout = 3 * DefaultProviderTimeout

	// DefaultProvider is the MVP provider selection (TDD 6.1).
	DefaultProvider = "kiro"

	// DefaultProviderModel is used when neither GSB_PROVIDER_MODEL nor
	// GSB_FALLBACK_PROVIDER_MODEL is set, preserving today's pinned model for a
	// deployment that does not configure one explicitly (TDD 6.12).
	DefaultProviderModel = "glm-5"

	// DefaultProviderReadyTimeout bounds how long a session provider waits for
	// the harness to become interactive after launch before giving up.
	DefaultProviderReadyTimeout = 90 * time.Second

	// DefaultProviderIdleSettle is the quiet period a session provider waits for
	// after a complete response appears in the pane, to confirm the harness has
	// finished emitting before the capture is taken as final.
	DefaultProviderIdleSettle = 800 * time.Millisecond

	// DefaultClassifySkipFloorNotes controls whether the provider is skipped for
	// immutable-floor PRs (merged, closed-unmerged). Their bucket is a hard fact
	// from GitHub, but the model still adds value on those rows: a companion
	// note and, for closed PRs, an inferred close sub-reason. The default is
	// false — classify every PR so all rows carry inferential detail — because a
	// dashboard whose terminal rows read "(unavailable)" is of little use. Set
	// GSB_CLASSIFY_FLOOR_NOTES=false to skip floor PRs when run time matters more
	// than notes on settled rows.
	DefaultClassifySkipFloorNotes = false

	// DefaultClassifyWorkers bounds parallel provider fan-out in classify
	// (TECH: concurrency model). A session provider serializes on one session,
	// so fan-out applies to one-shot providers; see ClassifyWorkers use.
	DefaultClassifyWorkers = 4

	// DefaultNotifyTimeout bounds the notification hook subprocess (TDD 9.4,
	// 9.5). Short by design: the hook is a fire-and-forget desktop notification
	// trigger, not a task the run should wait long on, and a slow/hung hook
	// must not meaningfully delay the next scheduled run.
	DefaultNotifyTimeout = 10 * time.Second

	// StoreFileName / OutputFileName are the artifact filenames. They are fixed,
	// not configurable: the *location* is the operator's choice (see
	// DefaultStorePath / DefaultOutputPath and the path overrides), while the
	// names identify the tool's own artifacts and gain nothing from varying.
	StoreFileName  = "prs.pr.yaml"
	OutputFileName = "GSB-SlashBoard.md"
)

// DefaultStorePath and DefaultOutputPath are the local artifact paths, resolved
// per platform under the host's conventional per-user application-data directory
// (XDG on Linux, Application Support on macOS). They are functions rather than
// constants because the location depends on the running host.
//
// The platform knowledge itself lives in the schedule package — the codebase's
// platform seam — so this package stays free of OS branching (TECH: the core is
// platform-independent).
func DefaultStorePath() string {
	return filepath.Join(schedule.UserDataDir(), StoreFileName)
}

// DefaultOutputPath returns the default rendered-document path.
func DefaultOutputPath() string {
	return filepath.Join(schedule.UserDataDir(), OutputFileName)
}

// Environment variable names (no magic strings in logic).
const (
	EnvGitHubToken             = "GITHUB_TOKEN"
	EnvProvider                = "GSB_PROVIDER"
	EnvProviderModel           = "GSB_PROVIDER_MODEL"
	EnvFallbackProvider        = "GSB_FALLBACK_PROVIDER"
	EnvFallbackProviderModel   = "GSB_FALLBACK_PROVIDER_MODEL"
	EnvFallbackProviderTimeout = "GSB_FALLBACK_PROVIDER_TIMEOUT"
	EnvStorePath               = "GSB_STORE_PATH"
	EnvOutputPath              = "GSB_OUTPUT_PATH"
	EnvStaleAgeThreshold       = "GSB_STALE_AGE_THRESHOLD"
	EnvIssueStaleAgeThreshold  = "GSB_ISSUE_STALE_AGE_THRESHOLD"
	EnvClassifyFloorNotes      = "GSB_CLASSIFY_FLOOR_NOTES"
	EnvIncludeTerminal         = "GSB_INCLUDE_TERMINAL"
	EnvNotifyHook              = "GSB_NOTIFY_HOOK"
	EnvNotifyTimeout           = "GSB_NOTIFY_TIMEOUT"
)

// Config is the validated runtime configuration for one invocation.
type Config struct {
	// GitHubToken is read from the environment and never stored (TECH). It is
	// carried in memory only for the duration of the run.
	GitHubToken string

	// Provider selects the primary LLM backend by name (e.g. "kiro", "ollama").
	// Model selects the model within that name's fixed kind (TDD 6.11, 6.12) —
	// there is no ProviderKind or raw-command field: the invocation kind is
	// derived solely from Provider's name (provider.KindForName), and the
	// harness/subprocess shape is fixed per name, parameterized only by Model.
	Provider string
	Model    string

	// FallbackProvider and FallbackModel describe the fallback slot, built
	// through the identical construction path as the primary (TDD 6.12).
	// FallbackProvider empty means no fallback is configured — the current
	// unverified-after-exhaustion behavior is unchanged (TDD 6.13).
	FallbackProvider string
	FallbackModel    string

	ProviderTimeout time.Duration
	// FallbackProviderTimeout bounds a fallback call independently of the
	// primary's own ProviderTimeout (TDD 6.17) — see DefaultFallbackProviderTimeout
	// for why the two must not share one value.
	FallbackProviderTimeout time.Duration

	// ProviderReadyTimeout bounds startup of a session-kind harness. Applies to
	// whichever slot (primary or fallback) is session kind by name.
	// ProviderIdleSettle is the quiet window that confirms a session response
	// is complete.
	ProviderReadyTimeout time.Duration
	ProviderIdleSettle   time.Duration

	CompanionWordsMin int
	CompanionWordsMax int
	LLMRetryCap       int
	StaleAgeThreshold time.Duration
	// IssueStaleAgeThreshold is the issue counterpart of StaleAgeThreshold and
	// is independently configurable, because issues legitimately sit idle far
	// longer than PRs without being abandoned (TDD 8.5).
	IssueStaleAgeThreshold time.Duration
	ClassifyWorkers        int

	// SkipFloorNotes, when true, skips the provider call for immutable-floor
	// PRs (merged, closed-unmerged): their bucket is a hard fact, so the model
	// is consulted only for a cosmetic companion and (for closed) a sub-reason.
	// Skipping trades those for run time (see DefaultClassifySkipFloorNotes).
	SkipFloorNotes bool

	// IncludeTerminal, when true, forces a full GitHub crawl even for PRs the
	// store already records as merged or closed. By default (false) such PRs are
	// skipped during acquisition — their judged result is already cached in the
	// store and a terminal PR will not change — so no GitHub calls are spent
	// re-crawling them. Set for a one-off deliberate deep run.
	IncludeTerminal bool

	StorePath  string
	OutputPath string

	// NotifyHook, when non-empty, is a shell command the tool writes a JSON
	// change payload to on stdin after a run in which at least one open PR or
	// issue required a fresh provider judgment (TDD 9.1, 9.4). Empty (the
	// default) leaves the notification mechanism entirely inert — no process is
	// spawned, no summary is requested (TDD 9.4).
	NotifyHook string
	// NotifyTimeout bounds how long the hook command is given to exit (TDD 9.4,
	// 9.5). Exceeding it, a non-zero exit, or a failure to start are all
	// logged and non-fatal to the run.
	NotifyTimeout time.Duration
}

// Default returns a Config populated with the default constants. The GitHub
// token and any environment overrides are layered on by Load.
func Default() Config {
	return Config{
		Provider:                DefaultProvider,
		Model:                   DefaultProviderModel,
		ProviderTimeout:         DefaultProviderTimeout,
		ProviderReadyTimeout:    DefaultProviderReadyTimeout,
		ProviderIdleSettle:      DefaultProviderIdleSettle,
		CompanionWordsMin:       DefaultCompanionWordsMin,
		CompanionWordsMax:       DefaultCompanionWordsMax,
		LLMRetryCap:             DefaultLLMRetryCap,
		StaleAgeThreshold:       DefaultStaleAgeThreshold,
		IssueStaleAgeThreshold:  DefaultIssueStaleAgeThreshold,
		ClassifyWorkers:         DefaultClassifyWorkers,
		SkipFloorNotes:          DefaultClassifySkipFloorNotes,
		StorePath:               DefaultStorePath(),
		OutputPath:              DefaultOutputPath(),
		NotifyTimeout:           DefaultNotifyTimeout,
		FallbackProviderTimeout: DefaultFallbackProviderTimeout,
	}
}

// Load builds a Config from defaults, then applies environment overrides, then
// validates. The GitHub token is required. getenv is injectable for testing;
// pass os.Getenv in production (see LoadFromEnv).
func Load(getenv func(string) string) (Config, error) {
	c := Default()

	c.GitHubToken = getenv(EnvGitHubToken)

	if v := getenv(EnvProvider); v != "" {
		c.Provider = v
	}
	if v := getenv(EnvProviderModel); v != "" {
		c.Model = v
	}
	if v := getenv(EnvFallbackProvider); v != "" {
		c.FallbackProvider = v
	}
	if v := getenv(EnvFallbackProviderModel); v != "" {
		c.FallbackModel = v
	}
	if v := getenv(EnvFallbackProviderTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q: %w", EnvFallbackProviderTimeout, v, err)
		}
		c.FallbackProviderTimeout = d
	}
	if v := getenv(EnvStorePath); v != "" {
		c.StorePath = v
	}
	if v := getenv(EnvOutputPath); v != "" {
		c.OutputPath = v
	}
	if v := getenv(EnvStaleAgeThreshold); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q: %w", EnvStaleAgeThreshold, v, err)
		}
		c.StaleAgeThreshold = d
	}
	if v := getenv(EnvIssueStaleAgeThreshold); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q: %w", EnvIssueStaleAgeThreshold, v, err)
		}
		c.IssueStaleAgeThreshold = d
	}
	if v := getenv(EnvClassifyFloorNotes); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid bool %q: %w", EnvClassifyFloorNotes, v, err)
		}
		// The env names the positive behavior (produce floor notes); the config
		// field names the skip, so invert.
		c.SkipFloorNotes = !b
	}
	if v := getenv(EnvIncludeTerminal); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid bool %q: %w", EnvIncludeTerminal, v, err)
		}
		c.IncludeTerminal = b
	}
	if v := getenv(EnvNotifyHook); v != "" {
		c.NotifyHook = v
	}
	if v := getenv(EnvNotifyTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q: %w", EnvNotifyTimeout, v, err)
		}
		c.NotifyTimeout = d
	}

	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// LoadFromEnv loads configuration from the process environment.
func LoadFromEnv() (Config, error) {
	return Load(os.Getenv)
}

// Validate checks the configuration is internally consistent and usable.
func (c Config) Validate() error {
	if c.GitHubToken == "" {
		return fmt.Errorf("%s is required", EnvGitHubToken)
	}
	if c.Provider == "" {
		return fmt.Errorf("provider selection is empty")
	}
	if c.ProviderReadyTimeout <= 0 {
		return fmt.Errorf("provider ready timeout must be positive, got %s", c.ProviderReadyTimeout)
	}
	if c.ProviderIdleSettle <= 0 {
		return fmt.Errorf("provider idle settle must be positive, got %s", c.ProviderIdleSettle)
	}
	if c.CompanionWordsMin < 1 {
		return fmt.Errorf("companion_words_min must be >= 1, got %d", c.CompanionWordsMin)
	}
	if c.CompanionWordsMax < c.CompanionWordsMin {
		return fmt.Errorf("companion_words_max (%d) must be >= companion_words_min (%d)",
			c.CompanionWordsMax, c.CompanionWordsMin)
	}
	if c.LLMRetryCap < 0 {
		return fmt.Errorf("llm_retry_cap must be >= 0, got %d", c.LLMRetryCap)
	}
	if c.StaleAgeThreshold <= 0 {
		return fmt.Errorf("stale_age_threshold must be positive, got %s", c.StaleAgeThreshold)
	}
	if c.IssueStaleAgeThreshold <= 0 {
		return fmt.Errorf("issue_stale_age_threshold must be positive, got %s", c.IssueStaleAgeThreshold)
	}
	if c.ProviderTimeout <= 0 {
		return fmt.Errorf("provider timeout must be positive, got %s", c.ProviderTimeout)
	}
	if c.FallbackProviderTimeout <= 0 {
		return fmt.Errorf("fallback provider timeout must be positive, got %s", c.FallbackProviderTimeout)
	}
	if c.ClassifyWorkers < 1 {
		return fmt.Errorf("classify workers must be >= 1, got %d", c.ClassifyWorkers)
	}
	if c.StorePath == "" {
		return fmt.Errorf("store path is empty")
	}
	if c.OutputPath == "" {
		return fmt.Errorf("output path is empty")
	}
	if c.NotifyHook != "" && c.NotifyTimeout <= 0 {
		return fmt.Errorf("notify timeout must be positive when a notify hook is configured, got %s", c.NotifyTimeout)
	}
	return nil
}
