// Package types defines core data structures for the bd issue tracker.
package types

import "time"

// CompactionCandidate represents an issue eligible for compaction.
// Used by the compact subsystem to identify and process closed issues
// that can have their description/notes summarized to save space.
type CompactionCandidate struct {
	IssueID        string
	ClosedAt       time.Time
	OriginalSize   int
	EstimatedSize  int
	DependentCount int
}

// IssueSnapshot is a pre-compaction copy of an issue's mutable text content,
// archived (into compaction_snapshots) before compaction destructively
// overwrites it. It is the source of truth for `bd restore`. The JSON tags are
// the on-disk snapshot_json wire format and must not be renamed: older archived
// rows are decoded with these names.
type IssueSnapshot struct {
	// CompactionLevel is the tier the issue was being compacted *to* when this
	// snapshot was taken, i.e. the level whose pre-compaction content this row
	// preserves. It is stored in its own column, not in snapshot_json.
	CompactionLevel    int    `json:"-"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	Design             string `json:"design"`
	Notes              string `json:"notes"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}

// DeleteIssuesResult contains statistics from a batch delete operation.
// Used when deleting multiple issues with cascade/force options.
type DeleteIssuesResult struct {
	DeletedCount      int
	DependenciesCount int
	LabelsCount       int
	EventsCount       int
	OrphanedIssues    []string
}
