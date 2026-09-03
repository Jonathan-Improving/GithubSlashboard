// Package store owns the YAML source of truth: read, validate, merge (preserving
// operator-set state), and write !pr documents (TDD 2.1–2.5). It is the only
// reader and writer of the source of truth (POLICY). Each PR is one !pr-tagged
// YAML document; the tag is the extension point that lets other entity types
// (e.g. !issue) coexist later (TDD 2.5).
package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// TagPR is the YAML tag identifying a pull-request document; TagIssue identifies
// an issue document. Dispatching on the tag is what lets entity types coexist in
// one file without restructuring existing data (TDD 2.5, 8.7).
const (
	TagPR    = "!pr"
	TagIssue = "!issue"
)

// Store is a decoded source of truth: the tracked PRs and issues plus any
// documents whose tags this build does not recognize, preserved verbatim so
// scope expansion does not lose data (TDD 2.5).
type Store struct {
	PRs    []model.PR
	Issues []model.Issue
	// unknown holds raw documents with tags this build does not recognize,
	// round-tripped unchanged so a newer store written by a future build is
	// never dropped.
	unknown []*yaml.Node
}

// Read loads and validates the store at path. A missing file yields an empty
// store (first run). A malformed file aborts with an error; the tool never
// silently repairs or overwrites a store it cannot parse (TDD 2.4).
func Read(path string) (*Store, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open store %q: %w", path, err)
	}
	defer f.Close()

	return decode(f)
}

// decode parses a YAML document stream, dispatching each document on its tag.
func decode(r io.Reader) (*Store, error) {
	dec := yaml.NewDecoder(r)
	s := &Store{}
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Malformed YAML: refuse to proceed (TDD 2.4).
			return nil, fmt.Errorf("malformed store: %w", err)
		}
		if err := s.ingest(&doc); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ingest routes one decoded document by its tag.
func (s *Store) ingest(doc *yaml.Node) error {
	// A document node wraps a single content node carrying the tag.
	content := doc
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) != 1 {
			return fmt.Errorf("malformed store: document has %d root nodes", len(doc.Content))
		}
		content = doc.Content[0]
	}

	switch content.Tag {
	case TagPR:
		var pr model.PR
		if err := content.Decode(&pr); err != nil {
			return fmt.Errorf("malformed !pr document: %w", err)
		}
		if err := validatePR(pr); err != nil {
			return fmt.Errorf("invalid !pr document: %w", err)
		}
		s.PRs = append(s.PRs, pr)
	case TagIssue:
		var iss model.Issue
		if err := content.Decode(&iss); err != nil {
			return fmt.Errorf("malformed !issue document: %w", err)
		}
		if err := validateIssue(iss); err != nil {
			return fmt.Errorf("invalid !issue document: %w", err)
		}
		s.Issues = append(s.Issues, iss)
	default:
		// Preserve unrecognized entity documents verbatim (TDD 2.5).
		clone := *content
		s.unknown = append(s.unknown, &clone)
	}
	return nil
}

// validatePR enforces the field contract for a persisted !pr document (SCHEMA).
func validatePR(pr model.PR) error {
	if pr.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	if pr.Number <= 0 {
		return fmt.Errorf("number must be positive, got %d", pr.Number)
	}
	if !pr.Bucket.Valid() {
		return fmt.Errorf("invalid bucket %q", pr.Bucket)
	}
	if !pr.Role.Valid() {
		return fmt.Errorf("invalid role %q", pr.Role)
	}
	if !pr.Priority.Valid() {
		return fmt.Errorf("invalid priority %q", pr.Priority)
	}
	switch pr.Bucket {
	case model.BucketOpen:
		if pr.Action != "" && !pr.Action.Valid() {
			return fmt.Errorf("invalid action %q", pr.Action)
		}
	case model.BucketClosed:
		if pr.CloseReason != "" && !pr.CloseReason.Valid() {
			return fmt.Errorf("invalid close_reason %q", pr.CloseReason)
		}
	}
	return nil
}

// validateIssue enforces the field contract for a persisted !issue document
// (SCHEMA § !issue document). It mirrors validatePR against the issue enums; the
// coupling differs because an issue has no merged bucket.
func validateIssue(iss model.Issue) error {
	if iss.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	if iss.Number <= 0 {
		return fmt.Errorf("number must be positive, got %d", iss.Number)
	}
	if !iss.Bucket.Valid() {
		return fmt.Errorf("invalid bucket %q", iss.Bucket)
	}
	if !iss.Role.Valid() {
		return fmt.Errorf("invalid role %q", iss.Role)
	}
	if !iss.Priority.Valid() {
		return fmt.Errorf("invalid priority %q", iss.Priority)
	}
	switch iss.Bucket {
	case model.IssueBucketOpen:
		if iss.Action != "" && !iss.Action.Valid() {
			return fmt.Errorf("invalid action %q", iss.Action)
		}
	case model.IssueBucketClosed:
		if iss.CloseReason != "" && !iss.CloseReason.Valid() {
			return fmt.Errorf("invalid close_reason %q", iss.CloseReason)
		}
	}
	return nil
}

// Merge overlays freshly classified PRs onto the existing store, preserving
// operator-set state (TDD 2.2). Derived fields are overwritten by the fresh
// data; OperatorStale is carried forward from the prior record when the caller
// did not set it. The merged set is sorted deterministically so an unchanged
// upstream produces an unchanged store (TDD 2.3).
func (s *Store) Merge(fresh []model.PR) {
	prior := make(map[string]model.PR, len(s.PRs))
	for _, p := range s.PRs {
		prior[p.Key()] = p
	}

	merged := make([]model.PR, 0, len(fresh))
	for _, p := range fresh {
		if old, ok := prior[p.Key()]; ok {
			// Preserve the operator override if it was set previously and the
			// fresh record does not already carry it.
			if old.OperatorStale {
				p.OperatorStale = true
			}
			// Carry forward a known activity date when this run produced none.
			// A terminal PR is not re-crawled (TDD 1.5), so it arrives with an
			// empty trail and no computed LastActivity; the stored value is the
			// only one it will ever have.
			if p.LastActivity.IsZero() {
				p.LastActivity = old.LastActivity
			}
		}
		// Backfill from the terminal timestamp for a record that still has no
		// activity date — a store written before last_activity was persisted.
		// When a PR merged or closed is the last thing that happened to it, so
		// it is a faithful proxy rather than a guess, and it spares every
		// pre-existing terminal row from rendering a blank Updated column
		// forever (nothing will ever re-crawl them).
		if p.LastActivity.IsZero() {
			p.LastActivity = terminalTime(p)
		}
		merged = append(merged, p)
	}

	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Repo != merged[j].Repo {
			return merged[i].Repo < merged[j].Repo
		}
		return merged[i].Number < merged[j].Number
	})
	s.PRs = merged
}

// terminalTime returns the timestamp at which a PR reached its terminal state, or
// the zero time for a live PR. Merged wins over closed: a merged PR is also
// closed, and the merge is the later, more meaningful event.
func terminalTime(p model.PR) time.Time {
	if p.MergedAt != nil {
		return *p.MergedAt
	}
	if p.ClosedAt != nil {
		return *p.ClosedAt
	}
	return time.Time{}
}

// MergeIssues overlays freshly classified issues onto the existing store,
// preserving operator-set state exactly as Merge does for PRs (TDD 8.7). The
// merged set is sorted deterministically so an unchanged upstream produces an
// unchanged store (TDD 2.3).
func (s *Store) MergeIssues(fresh []model.Issue) {
	prior := make(map[string]model.Issue, len(s.Issues))
	for _, i := range s.Issues {
		prior[i.Key()] = i
	}

	merged := make([]model.Issue, 0, len(fresh))
	for _, i := range fresh {
		if old, ok := prior[i.Key()]; ok {
			if old.OperatorStale {
				i.OperatorStale = true
			}
			// A closed issue is carried forward without a re-crawl, so it has no
			// computed activity date; the stored value is the only one it has.
			if i.LastActivity.IsZero() {
				i.LastActivity = old.LastActivity
			}
		}
		// Backfill a closed issue from its close date for a store written before
		// last_activity was persisted (see terminalTime on the PR path).
		if i.LastActivity.IsZero() && i.ClosedAt != nil {
			i.LastActivity = *i.ClosedAt
		}
		merged = append(merged, i)
	}

	sort.SliceStable(merged, func(a, b int) bool {
		if merged[a].Repo != merged[b].Repo {
			return merged[a].Repo < merged[b].Repo
		}
		return merged[a].Number < merged[b].Number
	})
	s.Issues = merged
}

// Write serializes the store to path as a stream of !pr documents (plus any
// preserved unknown documents), atomically via a temp file + rename so a
// partial write never corrupts the source of truth. Output is deterministic for
// a given store so re-runs with unchanged data produce byte-identical files
// (TDD 2.3).
func (s *Store) Write(path string) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	for i := range s.PRs {
		node, err := prNode(s.PRs[i])
		if err != nil {
			enc.Close()
			return err
		}
		if err := enc.Encode(node); err != nil {
			enc.Close()
			return fmt.Errorf("encode !pr document: %w", err)
		}
	}
	for i := range s.Issues {
		node, err := issueNode(s.Issues[i])
		if err != nil {
			enc.Close()
			return err
		}
		if err := enc.Encode(node); err != nil {
			enc.Close()
			return fmt.Errorf("encode !issue document: %w", err)
		}
	}
	for _, u := range s.unknown {
		if err := enc.Encode(u); err != nil {
			enc.Close()
			return fmt.Errorf("encode preserved document: %w", err)
		}
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close encoder: %w", err)
	}

	return atomicWrite(path, buf.Bytes())
}

// prNode builds a tagged YAML node for a PR so each document carries the !pr
// tag (TDD 2.1).
func prNode(pr model.PR) (*yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(pr); err != nil {
		return nil, fmt.Errorf("encode PR to node: %w", err)
	}
	node.Tag = TagPR
	node.Style = 0
	return &node, nil
}

// issueNode builds a tagged YAML node for an issue so each document carries the
// !issue tag, letting issues and PRs coexist in one stream (TDD 8.7).
func issueNode(iss model.Issue) (*yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(iss); err != nil {
		return nil, fmt.Errorf("encode issue to node: %w", err)
	}
	node.Tag = TagIssue
	node.Style = 0
	return &node, nil
}

// atomicWrite writes data to path via a sibling temp file and a rename.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create store dir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".prs-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file into place: %w", err)
	}
	return nil
}
