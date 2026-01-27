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

// DeleteIssuesResult contains statistics from a batch delete operation.
// Used when deleting multiple issues with cascade/force options.
type DeleteIssuesResult struct {
	DeletedCount      int
	DependenciesCount int
	LabelsCount       int
	EventsCount       int
	OrphanedIssues    []string
}
