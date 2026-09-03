package model

import "testing"

func TestParseBucket(t *testing.T) {
	for _, s := range []string{"open", "stale", "merged", "closed"} {
		if b, err := ParseBucket(s); err != nil || string(b) != s {
			t.Errorf("ParseBucket(%q) = %q, %v", s, b, err)
		}
	}
	if _, err := ParseBucket("nonsense"); err == nil {
		t.Error("ParseBucket(nonsense) should error")
	}
}

func TestValidHelpers(t *testing.T) {
	if !BucketOpen.Valid() || Bucket("x").Valid() {
		t.Error("Bucket.Valid mismatch")
	}
	if !ActionMergeReady.Valid() || Action("x").Valid() {
		t.Error("Action.Valid mismatch")
	}
	if !CloseReasonSuperseded.Valid() || CloseReason("x").Valid() {
		t.Error("CloseReason.Valid mismatch")
	}
	if !PriorityElevated.Valid() || Priority("x").Valid() {
		t.Error("Priority.Valid mismatch")
	}
	if !RoleSubmitter.Valid() || Role("x").Valid() {
		t.Error("Role.Valid mismatch")
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	if _, err := ParseAction(""); err == nil {
		t.Error("ParseAction(\"\") should error")
	}
	if _, err := ParseCloseReason(""); err == nil {
		t.Error("ParseCloseReason(\"\") should error")
	}
}

func TestKey(t *testing.T) {
	p := PR{Repo: "owner/name", Number: 42}
	if got := p.Key(); got != "owner/name#42" {
		t.Errorf("Key() = %q, want owner/name#42", got)
	}
}
