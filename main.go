// Command githubslashboard runs the read-only pipeline once and exits
// (TECH: single-shot). It parses flags/config, sequences
// acquire → classify → persist → render, and sets a meaningful exit status. It
// is headless: it never prompts for input, logging and exiting instead
// (TDD 7.1; POLICY debug output).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/classify"
	"github.com/Jonathan-Improving/githubslashboard/config"
	ghacq "github.com/Jonathan-Improving/githubslashboard/github"
	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/notify"
	"github.com/Jonathan-Improving/githubslashboard/provider"
	"github.com/Jonathan-Improving/githubslashboard/render"
	"github.com/Jonathan-Improving/githubslashboard/store"
)

// lastUpdatedLayout is the timestamp format for the Markdown header
// (TDD 3.4: emitted verbatim).
const lastUpdatedLayout = "2006-01-02 15:04:05 MST"

// writeOutputTimeout bounds the final Markdown write. A cloud-synced output
// directory can stall an unattended write; exceeding this fails the run (which
// the scheduler retries) instead of hanging indefinitely.
const writeOutputTimeout = 60 * time.Second

// preflightTimeout bounds the startup writability probe; a protected folder that
// stalls the probe is treated as a TCC access failure.
const preflightTimeout = 10 * time.Second

func main() {
	os.Exit(run())
}

// run executes the pipeline and returns a process exit status (0 success,
// non-zero failure). Splitting run from main keeps os.Exit out of the logic so
// deferred cleanup and testing behave.
func run() int {
	var (
		verbose         = flag.Bool("verbose", false, "enable debug-level logging")
		storePath       = flag.String("store", "", "path to the YAML source of truth (overrides config)")
		outputPath      = flag.String("output", "", "path to the rendered Markdown (overrides config)")
		includeTerminal = flag.Bool("include-terminal", false, "crawl GitHub for PRs the store already records as merged/closed (default: skip them and reuse the cached record)")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Error("configuration error", "err", err)
		return 2
	}
	if *storePath != "" {
		cfg.StorePath = *storePath
	}
	if *outputPath != "" {
		cfg.OutputPath = *outputPath
	}
	// The flag overrides the env/default only when explicitly passed, so an
	// unset flag does not clobber GSB_INCLUDE_TERMINAL=true (flag > env > default).
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "include-terminal" {
			cfg.IncludeTerminal = *includeTerminal
		}
	})

	ctx := context.Background()

	if err := pipeline(ctx, cfg, log); err != nil {
		log.Error("run failed", "err", err)
		return 1
	}
	return 0
}

// pipeline runs acquire → classify → persist → render in order. Any acquisition
// error aborts before the store or output is touched (TDD 1.2).
func pipeline(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	// Preflight: prove the output directory is writable BEFORE the expensive
	// acquire/classify phases. On macOS a launchd/unattended process is denied
	// access to protected folders (~/Documents, ~/Desktop, ~/Downloads) by TCC
	// unless the binary has Full Disk Access — and that denial otherwise
	// surfaces only at the final write, after ~20 minutes of work, as an opaque
	// stall. Catching it here fails in milliseconds with the exact fix.
	if err := preflightOutput(cfg.OutputPath, filepath.Dir(cfg.StorePath)); err != nil {
		return err
	}

	// Read the source of truth up front. Acquisition consults it to skip
	// re-crawling PRs already recorded as terminal (TDD 1.5), and persist reuses
	// it to merge fresh results over prior operator-set state. Reading here also
	// means a malformed store aborts before the expensive fetch (TDD 2.4).
	existing, err := store.Read(cfg.StorePath)
	if err != nil {
		return fmt.Errorf("read store: %w", err)
	}
	prior := make(map[string]model.PR, len(existing.PRs))
	for _, p := range existing.PRs {
		prior[p.Key()] = p
	}
	priorIssues := make(map[string]model.Issue, len(existing.Issues))
	for _, i := range existing.Issues {
		priorIssues[i.Key()] = i
	}

	// Acquire (read-only GitHub).
	client, err := ghacq.NewClient(ctx, cfg.GitHubToken)
	if err != nil {
		return fmt.Errorf("github client: %w", err)
	}
	log.Info("acquiring tracked PRs", "operator", client.Login(), "include_terminal", cfg.IncludeTerminal)

	acquireStart := time.Now()
	fetched, err := client.FetchTracked(ctx, prior, cfg.IncludeTerminal)
	if err != nil {
		// Acquisition failure is non-destructive (TDD 1.2): return before any
		// store or output write.
		return fmt.Errorf("acquire: %w", err)
	}
	log.Info("acquired PRs", "count", len(fetched), "phase", "acquire", "took", time.Since(acquireStart).String())

	// Issues are a second tracked entity acquired in the same read-only pass
	// (TDD 8.1). Their cost is small next to the PR set: three extra searches
	// plus a per-issue fetch, with closed issues skipped from the cache.
	issuesStart := time.Now()
	fetchedIssues, err := client.FetchIssues(ctx, priorIssues, cfg.IncludeTerminal)
	if err != nil {
		return fmt.Errorf("acquire issues: %w", err)
	}
	log.Info("acquired issues", "count", len(fetchedIssues), "phase", "acquire", "took", time.Since(issuesStart).String())

	// Classify (deterministic floors + provider judgment).
	prov, err := provider.NewFromOptions(provider.Options{
		Name:         cfg.Provider,
		Model:        cfg.Model,
		ReadyTimeout: cfg.ProviderReadyTimeout,
		Settle:       cfg.ProviderIdleSettle,
		PoolSize:     cfg.ClassifyWorkers,
	})
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	// A session provider holds a long-lived harness and server; close it once
	// classification is done (TDD 6.7). One-shot providers no-op.
	if closer, ok := prov.(interface{ Close() error }); ok {
		defer func() {
			if cerr := closer.Close(); cerr != nil {
				log.Warn("provider close", "err", cerr)
			}
		}()
	}

	// The fallback provider, when configured, is built through the identical
	// path as the primary (TDD 6.12) — same Options shape, just a different
	// name/model. It gets a single instance (PoolSize 1) rather than the
	// primary's classify-worker fan-out: fallback is invoked only after the
	// primary's own retry budget is exhausted (TDD 6.13), a rare path, so
	// paying for ClassifyWorkers idle fallback sessions would be wasted cost.
	var fallback provider.Provider
	if cfg.FallbackProvider != "" {
		fallback, err = provider.NewFromOptions(provider.Options{
			Name:         cfg.FallbackProvider,
			Model:        cfg.FallbackModel,
			ReadyTimeout: cfg.ProviderReadyTimeout,
			Settle:       cfg.ProviderIdleSettle,
			PoolSize:     1,
		})
		if err != nil {
			return fmt.Errorf("fallback provider: %w", err)
		}
		if closer, ok := fallback.(interface{ Close() error }); ok {
			defer func() {
				if cerr := closer.Close(); cerr != nil {
					log.Warn("fallback provider close", "err", cerr)
				}
			}()
		}
	}

	classifier := classify.New(prov, fallback, cfg, log, time.Now, prior, priorIssues)
	classifyStart := time.Now()
	classified := classifier.ClassifyAll(ctx, fetched)
	log.Info("classified PRs", "count", len(classified), "phase", "classify", "took", time.Since(classifyStart).String(), "workers", cfg.ClassifyWorkers)

	classifyIssuesStart := time.Now()
	classifiedIssues := classifier.ClassifyIssues(ctx, fetchedIssues)
	log.Info("classified issues", "count", len(classifiedIssues), "phase", "classify", "took", time.Since(classifyIssuesStart).String())

	// Persist (merge fresh over the store read up front, preserving operator
	// state, then write).
	persistStart := time.Now()
	existing.Merge(classified)
	existing.MergeIssues(classifiedIssues)
	if err := existing.Write(cfg.StorePath); err != nil {
		return fmt.Errorf("write store: %w", err)
	}
	log.Info("persisted store", "path", cfg.StorePath, "prs", len(existing.PRs), "issues", len(existing.Issues), "phase", "persist", "took", time.Since(persistStart).String())

	// Render (pure function of the store).
	renderStart := time.Now()
	renderNow := time.Now()
	lastUpdated := renderNow.Format(lastUpdatedLayout)
	md := render.Render(existing.PRs, existing.Issues, lastUpdated, renderNow)
	if err := writeOutput(cfg.OutputPath, md, filepath.Dir(cfg.StorePath)); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	log.Info("rendered status document", "path", cfg.OutputPath, "phase", "render", "took", time.Since(renderStart).String())

	// Notification hook (TDD § 9). Deliberately last: the status document is
	// already written by this point, so a failing or slow hook can never delay
	// or block the dashboard output itself (TDD 9.5). Entirely inert when no
	// hook command is configured (TDD 9.4).
	fireNotifyHook(ctx, cfg, prov, fallback, classified, classifiedIssues, log)

	return nil
}

// fireNotifyHook collects every open PR/issue that reached the provider for a
// fresh judgment this run (TDD 9.1, 9.2), and — only when that set is
// non-empty and a hook command is configured — asks the provider for a short
// summary and delivers the JSON payload to the hook's stdin (TDD 9.3, 9.4).
// fallback (may be nil) is tried if the primary's Summarize attempt fails
// (TDD 6.15). Any failure is logged and never propagated: a broken
// notification integration must never be the thing that fails the run
// (TDD 9.5).
func fireNotifyHook(ctx context.Context, cfg config.Config, prov, fallback provider.Provider, prs []model.PR, issues []model.Issue, log *slog.Logger) {
	changed := append(notify.ChangesFromPRs(prs), notify.ChangesFromIssues(issues)...)
	if len(changed) == 0 {
		log.Debug("notify hook: nothing changed this run, skipping")
		return
	}
	if cfg.NotifyHook == "" {
		log.Debug("notify hook: not configured, skipping", "changed", len(changed))
		return
	}

	// The fallback Summarize attempt gets more headroom than NotifyTimeout
	// (TDD 6.17's rationale: a one-shot local-model fallback can legitimately
	// be slower per call), but deliberately not the full classification
	// FallbackProviderTimeout — TDD 9.4/9.5 keeps the whole notify mechanism
	// short by design so a slow hook never meaningfully delays the next
	// scheduled run, and that intent should hold for its fallback path too.
	summarizeFallbackTimeout := cfg.NotifyTimeout * 3
	summary := notify.Summarize(ctx, prov, fallback, changed, cfg.NotifyTimeout, summarizeFallbackTimeout, log)
	if err := notify.Fire(ctx, cfg.NotifyHook, cfg.NotifyTimeout, changed, summary); err != nil {
		log.Warn("notify hook failed", "err", err, "changed", len(changed))
		return
	}
	log.Info("notify hook fired", "changed", len(changed))
}

// writeOutput writes the Markdown document to path, creating parent dirs.
// writeOutput writes the Markdown document to path atomically and within a
// bounded time. It stages the file in stagingDir — a directory the caller keeps
// outside any cloud-synced tree (the store directory) — then renames it into
// place. Writing the bytes outside the synced tree avoids the stall a
// cloud-sync agent (e.g. iCloud) can impose while it watches its own directory
// during an in-place write; the rename is an atomic metadata operation on the
// same volume, so the visible path flips over instantly and no reader sees a
// half-written document. The operation is bounded by a timeout so a stall fails
// the run — which the scheduler retries — rather than hanging indefinitely and
// holding the provider session. When stagingDir is empty, or on a different
// volume than path (a rename would fail cross-device), it stages in the output
// directory itself.
func writeOutput(path, content, stagingDir string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stage := stagingDir
	if stage == "" || !sameVolume(stage, dir) {
		stage = dir
	}

	done := make(chan error, 1)
	go func() {
		tmp, err := os.CreateTemp(stage, "."+filepath.Base(path)+".tmp-*")
		if err != nil {
			done <- err
			return
		}
		tmpName := tmp.Name()
		if _, err := tmp.WriteString(content); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			done <- err
			return
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			done <- err
			return
		}
		_ = os.Chmod(tmpName, 0o644)
		if err := os.Rename(tmpName, path); err != nil {
			_ = os.Remove(tmpName)
			done <- err
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil && isPermissionDenied(err) {
			return tccRemediation(dir, err)
		}
		return err
	case <-time.After(writeOutputTimeout):
		return fmt.Errorf("writing output to %s timed out after %s (a protected/cloud-synced directory may be stalling the write; if it is a protected folder, see Full Disk Access — run once interactively to surface the exact message)", path, writeOutputTimeout)
	}
}

// sameVolume reports whether two directories are on the same filesystem, so a
// rename between them is atomic rather than a cross-device failure.
func sameVolume(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	sa, ok1 := fa.Sys().(*syscall.Stat_t)
	sb, ok2 := fb.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	return sa.Dev == sb.Dev
}

// preflightOutput verifies the output directory is writable before the pipeline
// does any expensive work. It creates the directory if needed, then writes and
// renames a tiny probe file exactly as the real write would (staging on the
// store volume, renaming into the output directory), all bounded by a short
// timeout. A permission denial is reported with the precise remediation; a
// stall (the probe not completing in time) is reported as a likely protected
// folder needing access, since on macOS a TCC denial can manifest as a hang.
func preflightOutput(outputPath, stagingDir string) error {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if isPermissionDenied(err) {
			return tccRemediation(dir, err)
		}
		return fmt.Errorf("output directory %s is not usable: %w", dir, err)
	}

	done := make(chan error, 1)
	go func() {
		// If the target already exists, the operative question is whether THIS
		// process can replace THAT specific file — a file created by another app
		// can carry a per-file TCC ACL (com.apple.macl) that blocks replacement
		// even when the directory is writable and Full Disk Access is granted.
		// Probe by opening the existing file for write; that surfaces the denial
		// without destroying it.
		if _, statErr := os.Stat(outputPath); statErr == nil {
			f, err := os.OpenFile(outputPath, os.O_WRONLY, 0o644)
			if err != nil {
				done <- err
				return
			}
			_ = f.Close()
			done <- nil
			return
		}
		// Target does not exist: mirror the real write — stage then rename into
		// the output dir under the real filename, then clean up.
		stage := stagingDir
		if stage == "" || !sameVolume(stage, dir) {
			stage = dir
		}
		tmp, err := os.CreateTemp(stage, ".gsb-preflight-*")
		if err != nil {
			done <- err
			return
		}
		name := tmp.Name()
		_, _ = tmp.WriteString("preflight")
		_ = tmp.Close()
		if err := os.Rename(name, outputPath); err != nil {
			_ = os.Remove(name)
			done <- err
			return
		}
		_ = os.Remove(outputPath)
		done <- nil
	}()

	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		if isPermissionDenied(err) {
			return tccRemediation(dir, fmt.Errorf("cannot replace %s: %w", outputPath, err))
		}
		return fmt.Errorf("output path %s is not writable: %w", outputPath, err)
	case <-time.After(preflightTimeout):
		// A hang writing into a protected folder is the TCC failure mode too.
		return tccRemediation(dir, fmt.Errorf("write probe for %s stalled after %s", outputPath, preflightTimeout))
	}
}

// isPermissionDenied reports whether err is an OS permission denial (EPERM or
// EACCES) — the macOS TCC "Operation not permitted" for a protected folder.
func isPermissionDenied(err error) bool {
	return errors.Is(err, os.ErrPermission) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EACCES)
}

// tccRemediation builds an actionable error for a denied write to a protected
// directory, naming the exact fix so re-granting access is quick. It resolves
// the running executable so the operator knows precisely what to grant.
func tccRemediation(dir string, cause error) error {
	exe, e := os.Executable()
	if e != nil || exe == "" {
		exe = "the githubslashboard binary"
	}
	return fmt.Errorf(
		"cannot write to %s: %w.\n"+
			"This is macOS access control (TCC): an unattended/launchd process is denied "+
			"access to protected folders (Documents, Desktop, Downloads, iCloud Drive), and "+
			"a file created by another app can carry a per-file ACL (com.apple.macl) that blocks "+
			"replacement even with Full Disk Access.\n"+
			"FIX 1 (folder access): System Settings > Privacy & Security > Full Disk Access > enable "+
			"(or add with +) this binary, then reload the launchd job (launchctl kickstart) and re-run:\n    %s\n"+
			"FIX 2 (file owned by another app): if the target already exists and was written by a "+
			"different tool, either point GSB_OUTPUT_PATH at a distinct filename it does not own, or "+
			"remove that file once from an app that has access.\n"+
			"ALTERNATIVE: set GSB_OUTPUT_PATH to a non-protected location (e.g. under ~/.local/share).",
		dir, cause, exe)
}
