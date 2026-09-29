package provider

import (
	"strings"
	"testing"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TestBuildPromptIncludesOperator verifies the operator login is stated in the
// prompt so the model can identify the operator's own trail events when judging
// whose court the ball is in (SCHEMA § Request operator).
func TestBuildPromptIncludesOperator(t *testing.T) {
	req := Request{
		Entity:      EntityPR,
		Repo:        "o/n",
		Number:      76,
		Role:        "reviewer",
		Operator:    "Jonathan-Improving",
		State:       "open",
		Events:      []model.Event{{Author: "Jonathan-Improving", Kind: model.EventComment, Text: "structure question"}},
		Constraints: ConstraintsFrom(3, 14),
	}
	got, err := buildPrompt(req, "")
	if err != nil {
		t.Fatalf("buildPrompt: %v", err)
	}
	if !strings.Contains(got, "Jonathan-Improving") {
		t.Errorf("prompt should name the operator login so the model can identify their own events:\n%s", got)
	}
	if !strings.Contains(got, "whose court the ball is in") {
		t.Errorf("prompt should instruct the model to use the operator identity for ball-holding:\n%s", got)
	}
}

// TestBuildPromptOmitsOperatorWhenEmpty verifies the operator scaffolding is
// absent when no login is known, so an unknown operator degrades cleanly to the
// role/trail-only prompt.
func TestBuildPromptOmitsOperatorWhenEmpty(t *testing.T) {
	req := Request{
		Entity:      EntityPR,
		Repo:        "o/n",
		Number:      1,
		Role:        "submitter",
		State:       "open",
		Constraints: ConstraintsFrom(3, 14),
	}
	got, err := buildPrompt(req, "")
	if err != nil {
		t.Fatalf("buildPrompt: %v", err)
	}
	if strings.Contains(got, "The operator (the person this dashboard is for)") {
		t.Errorf("no operator login was set, yet the operator scaffolding appeared:\n%s", got)
	}
}
