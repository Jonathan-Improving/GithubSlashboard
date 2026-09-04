//go:build live

package provider

import (
	"context"
	"testing"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TestLiveKiroSessionVerdict drives the real session provider end to end: it
// launches kiro-cli under tmux with the provisioned verdict-tool profile,
// submits one synthetic PR, and asserts kiro returns a parseable verdict via
// the MCP sink. Run explicitly:
//
//	go test -tags live -run TestLiveKiroSessionVerdict -v ./provider/
//
// It is build-tagged so the default suite stays offline (POLICY: unit tests use
// a fake, never a live model).
func TestLiveKiroSessionVerdict(t *testing.T) {
	prov, err := NewFromOptions(Options{
		Name:         "kiro",
		ReadyTimeout: 90 * time.Second,
		Settle:       800 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFromOptions session: %v", err)
	}
	if closer, ok := prov.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	r := Request{
		Repo:        "octo/example",
		Number:      1,
		Role:        model.RoleReviewer,
		GitHubState: model.GitHubStateOpen,
		Events: []model.Event{
			{Timestamp: time.Now().Add(-48 * time.Hour), Author: "alice", RoleHint: "author", Kind: model.EventStateTransition, Text: "opened"},
			{Timestamp: time.Now().Add(-24 * time.Hour), Author: "bob", RoleHint: "reviewer", Kind: model.EventReview, Text: "changes_requested: please add tests"},
		},
		Constraints: ConstraintsFrom(3, 14),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	raw, err := prov.Invoke(ctx, r, "")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	resp, perr := ParseResponse(raw, 3, 14)
	if perr != nil {
		t.Fatalf("verdict did not parse: %v (raw=%q)", perr, raw)
	}
	t.Logf("live verdict: bucket=%s action=%s priority=%s companion=%q",
		resp.Bucket, resp.Action, resp.Priority, resp.Companion)
}

// TestLiveKiroSessionReuse confirms one session is reused across requests and
// startup is amortized: the first Invoke pays harness startup, later ones are
// much faster (TDD 6.7).
// TestLiveKiroLargeTrail probes the systemic unverified hypothesis: an open PR
// with a large event trail. If large trails are truncated on the way to the
// harness (or overflow it), the verdict will fail to parse repeatedly. Run:
//
//	go test -tags live -run TestLiveKiroLargeTrail -v ./provider/
//
// TestLiveKiroSparseReviewerPR probes the actual failing profile: an open PR
// the operator only reviews, with a minimal trail. It runs several times to see
// whether the verdict is consistently rejected and why (the failing rows in the
// live run were all open reviewer PRs hitting the retry cap).
func TestLiveKiroSparseReviewerPR(t *testing.T) {
	prov, err := NewFromOptions(Options{
		Name:         "kiro",
		ReadyTimeout: 90 * time.Second, Settle: 800 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFromOptions: %v", err)
	}
	if closer, ok := prov.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	// Sparse: just the opening event, operator is a reviewer, no reviews yet.
	r := Request{
		Repo: "someorg/somerepo", Number: 1, Role: model.RoleReviewer,
		GitHubState: model.GitHubStateOpen,
		Events: []model.Event{
			{Timestamp: time.Now().Add(-3 * 24 * time.Hour), Author: "author1", RoleHint: "author", Kind: model.EventStateTransition, Text: "opened"},
		},
		Constraints: ConstraintsFrom(3, 14),
	}

	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		raw, err := prov.Invoke(ctx, r, "")
		cancel()
		if err != nil {
			t.Logf("attempt %d: Invoke error: %v", i, err)
			continue
		}
		resp, perr := ParseResponse(raw, 3, 14)
		if perr != nil {
			t.Logf("attempt %d: REJECTED (%v) raw=%q", i, perr, raw)
		} else {
			t.Logf("attempt %d: OK bucket=%s action=%s companion=%q", i, resp.Bucket, resp.Action, resp.Companion)
		}
	}
}

func TestLiveKiroLargeTrail(t *testing.T) {
	prov, err := NewFromOptions(Options{
		Name:         "kiro",
		ReadyTimeout: 90 * time.Second, Settle: 800 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFromOptions: %v", err)
	}
	if closer, ok := prov.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	// Build a large, realistic review trail (many comments/reviews).
	var events []model.Event
	base := time.Now().Add(-30 * 24 * time.Hour)
	for i := 0; i < 40; i++ {
		events = append(events, model.Event{
			Timestamp: base.Add(time.Duration(i) * time.Hour),
			Author:    "reviewer" + string(rune('a'+i%5)),
			RoleHint:  "reviewer",
			Kind:      model.EventComment,
			Text:      "This is review comment number " + string(rune('0'+i%10)) + " discussing implementation details, edge cases, and requesting specific changes to the approach taken in this pull request.",
		})
	}
	r := Request{
		Repo: "someorg/somerepo", Number: 1, Role: model.RoleReviewer,
		GitHubState: model.GitHubStateOpen, Events: events, Constraints: ConstraintsFrom(3, 14),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	raw, err := prov.Invoke(ctx, r, "")
	if err != nil {
		t.Fatalf("Invoke (large trail): %v", err)
	}
	t.Logf("raw verdict (%d bytes): %q", len(raw), raw)
	resp, perr := ParseResponse(raw, 3, 14)
	if perr != nil {
		t.Fatalf("large-trail verdict did not parse: %v", perr)
	}
	t.Logf("parsed OK: bucket=%s action=%s", resp.Bucket, resp.Action)
}

func TestLiveKiroSessionReuse(t *testing.T) {
	prov, err := NewFromOptions(Options{
		Name:         "kiro",
		ReadyTimeout: 90 * time.Second,
		Settle:       800 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewFromOptions session: %v", err)
	}
	if closer, ok := prov.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	mk := func(n int) Request {
		return Request{
			Repo: "octo/example", Number: n, Role: model.RoleReviewer, GitHubState: model.GitHubStateOpen,
			Events: []model.Event{
				{Timestamp: time.Now().Add(-48 * time.Hour), Author: "alice", RoleHint: "author", Kind: model.EventStateTransition, Text: "opened"},
				{Timestamp: time.Now().Add(-1 * time.Hour), Author: "carol", RoleHint: "reviewer", Kind: model.EventReview, Text: "approved: lgtm"},
			},
			Constraints: ConstraintsFrom(3, 14),
		}
	}

	for i := 1; i <= 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		start := time.Now()
		raw, err := prov.Invoke(ctx, mk(i), "")
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			t.Fatalf("Invoke %d: %v", i, err)
		}
		if _, perr := ParseResponse(raw, 3, 14); perr != nil {
			t.Fatalf("Invoke %d verdict did not parse: %v (raw=%q)", i, perr, raw)
		}
		t.Logf("Invoke %d took %s", i, elapsed.Round(time.Millisecond))
	}
}
