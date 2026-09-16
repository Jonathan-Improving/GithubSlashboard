package model

// PinnedPR is the operator's standing instruction to track a specific pull
// request under a chosen role, whether or not any GitHub search discovers it
// (SCHEMA § !pinned-pr document). It is a distinct document from the !pr record
// it produces: the pin carries operator intent (repo, number, role) and
// persists independently, while the !pr holds the derived, judged result. The
// two are kept separate so unpinning removes only the intent, never the derived
// record, and a stub !pr never renders a blank title before its first crawl.
//
// It reuses the PR Role enum (submitter/reviewer) rather than minting a
// parallel type: a pin's role is exactly the role its resulting !pr will carry.
type PinnedPR struct {
	Repo   string `yaml:"repo" json:"repo"`
	Number int    `yaml:"number" json:"number"`
	Role   Role   `yaml:"role" json:"role"`
}

// Key uniquely identifies a pin by the PR it targets, matching PR.Key so a pin
// and its resulting !pr record share one key.
func (p PinnedPR) Key() string {
	return p.Repo + "#" + itoa(p.Number)
}
