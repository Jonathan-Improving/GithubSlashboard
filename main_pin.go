package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/config"
	"github.com/Jonathan-Improving/githubslashboard/model"
	"github.com/Jonathan-Improving/githubslashboard/store"
)

// cliLockWait bounds how long a --pin/--unpin mutation waits for a running
// pipeline to release the store lock before giving up (decision 7). It is
// generous enough to outlast a pipeline's short read→write critical section
// (the pipeline holds the lock only across persist, not across the whole
// acquire/classify phase) yet bounded so the CLI never blocks a human
// indefinitely — a wedged store reports "busy, retry" instead.
const cliLockWait = 30 * time.Second

// resolveStorePath resolves the store path for a standalone mutation using the
// same flag > env > default precedence as the pipeline, without requiring the
// full config (which mandates a GitHub token a pin edit does not need). An
// explicit -store flag wins; otherwise GSB_STORE_PATH; otherwise the
// platform-resolved default.
func resolveStorePath(flagStore string) string {
	if flagStore != "" {
		return flagStore
	}
	if env := os.Getenv(config.EnvStorePath); env != "" {
		return env
	}
	return config.DefaultStorePath()
}

// runPin parses the pin target, then reads → mutates → writes the store under
// the exclusive lock and exits, making no GitHub call and touching no
// !pr/!issue document (P.4). A malformed target is rejected non-zero with no
// store write.
func runPin(storePath, target string, log *slog.Logger) int {
	pin, err := parsePinTarget(target)
	if err != nil {
		log.Error("invalid --pin target", "target", target, "err", err,
			"want", "owner/repo#N=submitter|reviewer")
		return 2
	}

	lock, err := store.AcquireWait(storePath, cliLockWait)
	if err != nil {
		log.Error("cannot acquire store lock", "err", err, "hint", "store busy, retry")
		return 1
	}
	defer lock.Release()

	s, err := store.Read(storePath)
	if err != nil {
		log.Error("read store", "err", err)
		return 1
	}
	added, err := s.Pin(pin)
	if err != nil {
		log.Error("invalid pin", "err", err)
		return 2
	}
	if err := s.Write(storePath); err != nil {
		log.Error("write store", "err", err)
		return 1
	}
	verb := "updated"
	if added {
		verb = "added"
	}
	log.Info("pin "+verb, "pr", pin.Key(), "role", pin.Role, "store", storePath)
	return 0
}

// runUnpin parses the target key, then removes only the matching !pinned-pr
// document under the lock, leaving every !pr/!issue untouched (P.5). An unpin
// of a PR with no pin exits cleanly (idempotent).
func runUnpin(storePath, target string, log *slog.Logger) int {
	key, err := parseUnpinTarget(target)
	if err != nil {
		log.Error("invalid --unpin target", "target", target, "err", err,
			"want", "owner/repo#N")
		return 2
	}

	lock, err := store.AcquireWait(storePath, cliLockWait)
	if err != nil {
		log.Error("cannot acquire store lock", "err", err, "hint", "store busy, retry")
		return 1
	}
	defer lock.Release()

	s, err := store.Read(storePath)
	if err != nil {
		log.Error("read store", "err", err)
		return 1
	}
	removed := s.Unpin(key)
	if err := s.Write(storePath); err != nil {
		log.Error("write store", "err", err)
		return 1
	}
	if removed {
		log.Info("pin removed", "pr", key, "store", storePath)
	} else {
		log.Info("no pin to remove", "pr", key)
	}
	return 0
}

// parsePinTarget parses "owner/repo#N=role" into a validated PinnedPR. The role
// must be one of the closed PR role vocabulary (submitter, reviewer).
func parsePinTarget(target string) (model.PinnedPR, error) {
	eq := strings.LastIndex(target, "=")
	if eq < 0 {
		return model.PinnedPR{}, fmt.Errorf("missing =role")
	}
	prPart, rolePart := target[:eq], target[eq+1:]
	repo, number, err := parseRepoNumber(prPart)
	if err != nil {
		return model.PinnedPR{}, err
	}
	role, err := model.ParseRole(strings.TrimSpace(rolePart))
	if err != nil {
		return model.PinnedPR{}, err
	}
	return model.PinnedPR{Repo: repo, Number: number, Role: role}, nil
}

// parseUnpinTarget parses "owner/repo#N" into the store key it identifies.
func parseUnpinTarget(target string) (string, error) {
	repo, number, err := parseRepoNumber(target)
	if err != nil {
		return "", err
	}
	return model.PinnedPR{Repo: repo, Number: number}.Key(), nil
}

// parseRepoNumber parses "owner/repo#N" into its repo and positive number.
func parseRepoNumber(s string) (repo string, number int, err error) {
	s = strings.TrimSpace(s)
	hash := strings.LastIndex(s, "#")
	if hash < 0 {
		return "", 0, fmt.Errorf("missing #number")
	}
	repo = s[:hash]
	numStr := s[hash+1:]
	if !strings.Contains(repo, "/") || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", 0, fmt.Errorf("repo must be owner/name, got %q", repo)
	}
	number, err = strconv.Atoi(numStr)
	if err != nil {
		return "", 0, fmt.Errorf("number %q is not an integer", numStr)
	}
	if number <= 0 {
		return "", 0, fmt.Errorf("number must be positive, got %d", number)
	}
	return repo, number, nil
}
