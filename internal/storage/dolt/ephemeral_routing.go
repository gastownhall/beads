package dolt

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/ephemeral"
	"github.com/steveyegge/beads/internal/types"
)

// SetEphemeralStore is a no-op retained for backward compatibility.
// Wisp routing now uses the Dolt wisps table instead of a separate SQLite store.
// Callers that still call SetEphemeralStore will not break, but the store is ignored.
func (s *DoltStore) SetEphemeralStore(es *ephemeral.Store) {
	// No-op: wisps are now stored in the Dolt wisps table.
	// If the caller passed a non-nil store, close it to release resources.
	if es != nil {
		_ = es.Close()
	}
}

// EphemeralStore returns nil. Retained for backward compatibility.
func (s *DoltStore) EphemeralStore() *ephemeral.Store {
	return nil
}

// HasEphemeralStore returns false. Wisps are now in the Dolt wisps table.
func (s *DoltStore) HasEphemeralStore() bool {
	return false
}

// IsEphemeralID returns true if the ID belongs to an ephemeral issue.
func IsEphemeralID(id string) bool {
	return strings.Contains(id, "-wisp-")
}

// allEphemeral returns true if all IDs in the slice are ephemeral.
func allEphemeral(ids []string) bool {
	for _, id := range ids {
		if !IsEphemeralID(id) {
			return false
		}
	}
	return len(ids) > 0
}

// partitionIDs separates IDs into ephemeral and dolt groups.
func partitionIDs(ids []string) (ephIDs, doltIDs []string) {
	for _, id := range ids {
		if IsEphemeralID(id) {
			ephIDs = append(ephIDs, id)
		} else {
			doltIDs = append(doltIDs, id)
		}
	}
	return
}

// SearchIssuesDoltOnly queries Dolt directly, bypassing ephemeral routing.
// Used by migration to find ephemeral issues still in Dolt.
func (s *DoltStore) SearchIssuesDoltOnly(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	// Clear Ephemeral filter to prevent routing, since we want Dolt results
	filter.Ephemeral = nil
	results, err := s.SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	// Filter to only ephemeral issues
	var ephIssues []*types.Issue
	for _, issue := range results {
		if issue.Ephemeral {
			ephIssues = append(ephIssues, issue)
		}
	}
	return ephIssues, nil
}

// PromoteFromEphemeral copies an issue from the wisps table to the issues table,
// clearing the Ephemeral flag. Used by mol squash to crystallize wisps.
func (s *DoltStore) PromoteFromEphemeral(ctx context.Context, id string, actor string) error {
	issue, err := s.getWisp(ctx, id)
	if err != nil {
		return err
	}
	if issue == nil {
		return nil // Not found in wisps, nothing to promote
	}

	// Clear ephemeral flag for persistent storage
	issue.Ephemeral = false

	// Create in issues table (bypasses ephemeral routing since Ephemeral=false)
	if err := s.CreateIssue(ctx, issue, actor); err != nil {
		return fmt.Errorf("failed to promote wisp to issues: %w", err)
	}

	// Copy labels from wisp_labels to labels
	labels, err := s.getWispLabels(ctx, id)
	if err != nil {
		return err
	}
	for _, label := range labels {
		if err := s.AddLabel(ctx, id, label, actor); err != nil {
			return err
		}
	}

	// Copy dependencies from wisp_dependencies to dependencies
	deps, err := s.getWispDependencyRecords(ctx, id)
	if err != nil {
		return err
	}
	for _, dep := range deps {
		if err := s.AddDependency(ctx, dep, actor); err != nil {
			// Skip if target doesn't exist in Dolt (external ref to other wisp)
			if strings.Contains(err.Error(), "not found") {
				continue
			}
			return err
		}
	}

	// Delete from wisps table
	return s.deleteWisp(ctx, id)
}

// getWispDependencyRecords returns raw dependency records for a wisp from wisp_dependencies.
func (s *DoltStore) getWispDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	rows, err := s.queryContext(ctx, `
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM wisp_dependencies
		WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependency records: %w", err)
	}
	defer rows.Close()

	return scanDependencyRows(rows)
}
