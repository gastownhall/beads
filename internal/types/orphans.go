// Package types defines core data structures for the bd issue tracker.
package types

import "context"

// IssueProvider abstracts issue storage for orphan detection.
// Implementations may be backed by Dolt or mocks.
type IssueProvider interface {
	// GetOpenIssues returns issues that are open or in_progress.
	// Should return empty slice (not error) if no issues exist.
	GetOpenIssues(ctx context.Context) ([]*Issue, error)

	// GetIssuePrefix returns the configured prefix (e.g., "bd", "TEST").
	// Should return "bd" as default if not configured.
	GetIssuePrefix() string

	// GetParentedOpenIssueIDs returns the set of open or in_progress issue
	// IDs (same candidate set GetOpenIssues considers) that carry a
	// parent-child dependency. Orphan detection excludes these: an issue
	// governed by a parent is never orphaned.
	GetParentedOpenIssueIDs(ctx context.Context) (map[string]bool, error)
}
