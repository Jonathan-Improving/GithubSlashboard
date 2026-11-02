package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/config"
	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/provider"
)

// scriptedProvider answers every Summarize from a script and counts the calls.
type scriptedProvider struct {
	name  string
	out   string
	err   error
	calls int
}

func (s *scriptedProvider) Name() string { return s.name }
func (s *scriptedProvider) Invoke(ctx context.Context, req provider.Request, correction string) (string, error) {
	return "", errors.New("not used")
}
func (s *scriptedProvider) Summarize(ctx context.Context, prompt string) (string, error) {
	s.calls++
	return s.out, s.err
}

type proseHarness struct {
	cfg     config.Config
	payload string
	log     *bytes.Buffer
	built   int
}

// newProseHarness configures a hook that records its stdin to a file and swaps
// the Prose Diff Checker's provider builder for the given provider.
func newProseHarness(t *testing.T, checker provider.Provider, buildErr error) *proseHarness {
	t.Helper()
	h := &proseHarness{log: &bytes.Buffer{}}
	h.payload = filepath.Join(t.TempDir(), "payload.json")
	h.cfg = config.Config{
		NotifyHook:       "cat > " + h.payload,
		NotifyTimeout:    5 * time.Second,
		ProseDiffTimeout: 5 * time.Second,
	}
	orig := buildProseDiffProvider
	buildProseDiffProvider = func(cfg config.Config, primary provider.Provider) (provider.Provider, func(), error) {
		h.built++
		if buildErr != nil {
			return nil, func() {}, buildErr
		}
		return checker, func() {}, nil
	}
	t.Cleanup(func() { buildProseDiffProvider = orig })
	return h
}

func (h *proseHarness) fire(prs []model.PR) {
	log := slog.New(slog.NewTextHandler(h.log, &slog.HandlerOptions{Level: slog.LevelInfo}))
	summarizer := &scriptedProvider{name: "summary", out: "something moved"}
	fireNotifyHook(context.Background(), h.cfg, summarizer, nil, prs, nil, log)
}

func (h *proseHarness) fired() bool {
	_, err := os.Stat(h.payload)
	return err == nil
}

func heldPR(n int) model.PR {
	return model.PR{
		Repo: "o/n", Number: n, Role: model.RoleSubmitter, Bucket: model.BucketOpen,
		Companion: "new note", Emoji: "🔄",
		ProseHeldBack: &model.ProseChange{OldCompanion: "old note", OldEmoji: "⏳"},
	}
}

// TDD 9.11: a held-back item judged equivalent never notifies.
func TestEquivalentProseDoesNotFireTheHook(t *testing.T) {
	checker := &scriptedProvider{name: "checker", out: "1=NO"}
	h := newProseHarness(t, checker, nil)

	h.fire([]model.PR{heldPR(1)})

	if h.fired() {
		t.Error("hook fired for a note judged equivalent (TDD 9.11)")
	}
	if checker.calls != 1 {
		t.Errorf("checker calls = %d, want 1", checker.calls)
	}
	if !strings.Contains(h.log.String(), "hook not fired") {
		t.Errorf("expected the INFO decision line (TDD 9.8), got %q", h.log.String())
	}
}

func TestMeaningfulProseFiresTheHookWithTheItem(t *testing.T) {
	checker := &scriptedProvider{name: "checker", out: "1=NO 2=YES"}
	h := newProseHarness(t, checker, nil)

	h.fire([]model.PR{heldPR(1), heldPR(2)})

	body, err := os.ReadFile(h.payload)
	if err != nil {
		t.Fatalf("hook did not fire for a meaningfully different note: %v", err)
	}
	if !strings.Contains(string(body), `"number":2`) || strings.Contains(string(body), `"number":1,`) {
		t.Errorf("payload should carry only item 2: %s", body)
	}
	if checker.calls != 1 {
		t.Errorf("checker calls = %d, want one batch for both items (TDD 9.10)", checker.calls)
	}
}

// TDD 9.9: a hard change notifies without the checker and is not sent to it.
func TestHardChangeFiresWithoutConsultingTheChecker(t *testing.T) {
	checker := &scriptedProvider{name: "checker", out: "1=NO"}
	h := newProseHarness(t, checker, nil)
	hard := model.PR{Repo: "o/n", Number: 9, Role: model.RoleSubmitter, Bucket: model.BucketMerged, StatusChanged: true}

	h.fire([]model.PR{hard})

	if !h.fired() {
		t.Error("hook did not fire for a hard-field change")
	}
	if checker.calls != 0 || h.built != 0 {
		t.Errorf("checker calls = %d, built = %d, want 0 and 0 when nothing is held back (TDD 9.10)", checker.calls, h.built)
	}
}

func TestHardAndHeldItemsAreCombined(t *testing.T) {
	checker := &scriptedProvider{name: "checker", out: "1=YES"}
	h := newProseHarness(t, checker, nil)
	hard := model.PR{Repo: "o/n", Number: 9, Role: model.RoleSubmitter, Bucket: model.BucketMerged, StatusChanged: true}

	h.fire([]model.PR{hard, heldPR(1)})

	body, err := os.ReadFile(h.payload)
	if err != nil {
		t.Fatalf("hook did not fire: %v", err)
	}
	if !strings.Contains(string(body), `"number":9`) || !strings.Contains(string(body), `"number":1,`) {
		t.Errorf("payload should carry both the hard change and the meaningful note change: %s", body)
	}
}

// TDD 9.15: a checker that fails everywhere must not hide a change.
func TestCheckerFailureFiresTheHook(t *testing.T) {
	checker := &scriptedProvider{name: "checker", err: errors.New("down")}
	h := newProseHarness(t, checker, nil)

	h.fire([]model.PR{heldPR(1)})

	if !h.fired() {
		t.Error("hook did not fire although the checker failed (TDD 9.15)")
	}
}

func TestCheckerBuildFailureFiresTheHook(t *testing.T) {
	h := newProseHarness(t, nil, errors.New("no such provider"))

	h.fire([]model.PR{heldPR(1)})

	if !h.fired() {
		t.Error("hook did not fire although the checker could not be built (TDD 9.15)")
	}
	if !strings.Contains(h.log.String(), "could not build") {
		t.Errorf("expected a WARN naming the build failure, got %q", h.log.String())
	}
}

// TDD 9.4: with no hook configured the checker is never built or called.
func TestCheckerIsInertWithoutAHook(t *testing.T) {
	checker := &scriptedProvider{name: "checker", out: "1=YES"}
	h := newProseHarness(t, checker, nil)
	h.cfg.NotifyHook = ""

	h.fire([]model.PR{heldPR(1)})

	if h.built != 0 || checker.calls != 0 {
		t.Errorf("built = %d, calls = %d, want 0 and 0 when no hook is configured", h.built, checker.calls)
	}
}

func TestBuildProseDiffProviderReusesTheMatchingPrimary(t *testing.T) {
	cfg := config.Config{Provider: "kiro", Model: config.DefaultProseDiffKiroModel}
	primary := &scriptedProvider{name: "primary"}

	got, closeFn, err := buildProseDiffProvider(cfg, primary)
	if err != nil {
		t.Fatalf("buildProseDiffProvider: %v", err)
	}
	defer closeFn()
	if got != provider.Provider(primary) {
		t.Error("a checker slot identical to the primary must reuse its instance")
	}
}
