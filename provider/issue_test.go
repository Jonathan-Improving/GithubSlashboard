package provider

import (
	"strings"
	"testing"
)

// TestIssueConstraintsCarryIssueVocabulary proves the issue constraints advertise
// the issue vocabulary and offer no close-reason set at all: an issue's reason is
// GitHub's state_reason, never a model inference (SCHEMA § Issue vocabularies).
func TestIssueConstraintsCarryIssueVocabulary(t *testing.T) {
	c := IssueConstraintsFrom(3, 14)

	if len(c.CloseReasons) != 0 {
		t.Errorf("issue close_reasons = %v, want empty — the reason is a hard GitHub fact", c.CloseReasons)
	}
	if inVocabulary("merged", c.Buckets) {
		t.Error("issue buckets include merged; an issue cannot be merged")
	}
	if !inVocabulary("awaiting_response", c.Actions) {
		t.Errorf("issue actions %v missing awaiting_response", c.Actions)
	}
	if inVocabulary("awaiting_review", c.Actions) {
		t.Error("issue actions include the PR action awaiting_review; the sets must stay separate")
	}
	if !inVocabulary("author", c.Roles) || !inVocabulary("participant", c.Roles) {
		t.Errorf("issue roles = %v, want author and participant", c.Roles)
	}
}

// TestPRConstraintsExcludeDeterministicActions proves the PR action vocabulary
// withholds the values the classifier assigns in code, so the model cannot return
// them (SCHEMA § action).
func TestPRConstraintsExcludeDeterministicActions(t *testing.T) {
	c := ConstraintsFrom(3, 14)
	for _, deterministic := range []string{"conflicted", "review_feedback"} {
		if inVocabulary(deterministic, c.Actions) {
			t.Errorf("PR actions offer %q, which the classifier assigns deterministically", deterministic)
		}
	}
}

// TestClosedBucketRequiresReasonOnlyWhenOffered is a regression test for the
// coupling rule. A PR must supply a close reason; an issue must not, and must not
// be rejected for omitting one — the first implementation demanded one regardless,
// which made a closed issue impossible to judge and silently dropped its note.
func TestClosedBucketRequiresReasonOnlyWhenOffered(t *testing.T) {
	const closedNoReason = `{"bucket":"closed","priority":"neutral","companion":"three word note here","emoji":"✅"}`

	if _, err := ParseResponse(closedNoReason, ConstraintsFrom(3, 8)); err == nil {
		t.Error("a closed PR response with no close_reason should be rejected")
	}
	if _, err := ParseResponse(closedNoReason, IssueConstraintsFrom(3, 8)); err != nil {
		t.Errorf("a closed issue response with no close_reason should be accepted, got: %v", err)
	}

	const closedWithReason = `{"bucket":"closed","close_reason":"duplicate","priority":"neutral","companion":"three word note here","emoji":"♻️"}`
	if _, err := ParseResponse(closedWithReason, IssueConstraintsFrom(3, 8)); err == nil {
		t.Error("an issue response inventing a close_reason should be rejected — it is a hard GitHub fact")
	}
}

// TestIssueActionRejectedForPRAndViceVersa proves each entity's action set is
// enforced against its own vocabulary, which is the whole point of carrying the
// vocabulary in the request.
func TestIssueActionRejectedForPRAndViceVersa(t *testing.T) {
	prAction := `{"bucket":"open","action":"merge_ready","priority":"neutral","companion":"three word note here","emoji":"✅"}`
	issueAction := `{"bucket":"open","action":"triage","priority":"neutral","companion":"three word note here","emoji":"🔍"}`

	if _, err := ParseResponse(prAction, ConstraintsFrom(3, 8)); err != nil {
		t.Errorf("merge_ready should be valid for a PR: %v", err)
	}
	if _, err := ParseResponse(prAction, IssueConstraintsFrom(3, 8)); err == nil {
		t.Error("merge_ready should be invalid for an issue")
	}
	if _, err := ParseResponse(issueAction, IssueConstraintsFrom(3, 8)); err != nil {
		t.Errorf("triage should be valid for an issue: %v", err)
	}
	if _, err := ParseResponse(issueAction, ConstraintsFrom(3, 8)); err == nil {
		t.Error("triage should be invalid for a PR")
	}
}

// TestEmptyVocabularyAdmitsNothing proves a request that forgot to name a set
// fails loudly rather than silently accepting any value.
func TestEmptyVocabularyAdmitsNothing(t *testing.T) {
	bare := Constraints{CompanionWordsMin: 3, CompanionWordsMax: 8}
	raw := `{"bucket":"open","action":"triage","priority":"neutral","companion":"three word note here","emoji":"🔍"}`
	if _, err := ParseResponse(raw, bare); err == nil {
		t.Error("a response validated against empty vocabularies should be rejected")
	}
}

// TestIssuePromptNamesTheEntity proves the one-shot prompt is entity-aware, so an
// issue is not described to the model as a pull request.
func TestIssuePromptNamesTheEntity(t *testing.T) {
	issueReq := Request{Entity: EntityIssue, Repo: "a/x", Number: 1, Role: "author", State: "open",
		Constraints: IssueConstraintsFrom(3, 14)}
	got, err := buildPrompt(issueReq, "")
	if err != nil {
		t.Fatalf("buildPrompt: %v", err)
	}
	if !strings.Contains(got, "GitHub issue") {
		t.Errorf("issue prompt does not describe the item as an issue:\n%s", got)
	}
	if strings.Contains(got, "immutable floors: if github_state is \"merged\"") {
		t.Error("issue prompt carries the PR merged-floor instruction")
	}

	prReq := Request{Entity: EntityPR, Repo: "a/x", Number: 1, Role: "submitter", State: "open",
		Constraints: ConstraintsFrom(3, 14)}
	got, err = buildPrompt(prReq, "")
	if err != nil {
		t.Fatalf("buildPrompt: %v", err)
	}
	if !strings.Contains(got, "GitHub pull request") {
		t.Errorf("PR prompt does not describe the item as a pull request:\n%s", got)
	}
}
