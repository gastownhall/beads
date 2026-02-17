package dolt

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// History returns the complete version history for an issue.
func (s *DoltStore) History(ctx context.Context, issueID string) ([]*storage.HistoryEntry, error) {
	internal, err := s.getIssueHistory(ctx, issueID)
	if err != nil {
		return nil, err
	}

	// Convert internal representation to interface type
	entries := make([]*storage.HistoryEntry, len(internal))
	for i, h := range internal {
		entries[i] = &storage.HistoryEntry{
			CommitHash: h.CommitHash,
			Committer:  h.Committer,
			CommitDate: h.CommitDate,
			Issue:      h.Issue,
		}
	}
	return entries, nil
}

// AsOf returns the state of an issue at a specific commit hash or branch ref.
// Implements storage.VersionedStorage.
func (s *DoltStore) AsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error) {
	return s.getIssueAsOf(ctx, issueID, ref)
}

// Diff returns changes between two commits/branches.
// Implements storage.VersionedStorage.
func (s *DoltStore) Diff(ctx context.Context, fromRef, toRef string) ([]*storage.DiffEntry, error) {
	// Validate refs to prevent SQL injection
	if err := validateRef(fromRef); err != nil {
		return nil, fmt.Errorf("invalid fromRef: %w", err)
	}
	if err := validateRef(toRef); err != nil {
		return nil, fmt.Errorf("invalid toRef: %w", err)
	}

	// Query issue-level diffs using dolt_diff table function
	// Syntax: dolt_diff(from_ref, to_ref, 'table_name')
	// Note: refs are validated above
	// nolint:gosec // G201: refs validated by validateRef()
	query := fmt.Sprintf(`
		SELECT
			COALESCE(from_id, '') as from_id,
			COALESCE(to_id, '') as to_id,
			diff_type,
			from_title, to_title,
			from_description, to_description,
			from_status, to_status,
			from_priority, to_priority
		FROM dolt_diff('%s', '%s', 'issues')
	`, fromRef, toRef)

	rows, err := s.queryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get diff: %w", err)
	}
	defer rows.Close()

	var entries []*storage.DiffEntry
	for rows.Next() {
		var fromID, toID, diffType string
		var fromTitle, toTitle, fromDesc, toDesc, fromStatus, toStatus *string
		var fromPriority, toPriority *int

		if err := rows.Scan(&fromID, &toID, &diffType,
			&fromTitle, &toTitle,
			&fromDesc, &toDesc,
			&fromStatus, &toStatus,
			&fromPriority, &toPriority); err != nil {
			return nil, fmt.Errorf("failed to scan diff: %w", err)
		}

		entry := &storage.DiffEntry{
			DiffType: diffType,
		}

		// Determine issue ID (use to_id for added, from_id for removed, either for modified)
		if toID != "" {
			entry.IssueID = toID
		} else {
			entry.IssueID = fromID
		}

		// Build old value for modified/removed
		if diffType != "added" && fromID != "" {
			entry.OldValue = &types.Issue{
				ID: fromID,
			}
			if fromTitle != nil {
				entry.OldValue.Title = *fromTitle
			}
			if fromDesc != nil {
				entry.OldValue.Description = *fromDesc
			}
			if fromStatus != nil {
				entry.OldValue.Status = types.Status(*fromStatus)
			}
			if fromPriority != nil {
				entry.OldValue.Priority = *fromPriority
			}
		}

		// Build new value for modified/added
		if diffType != "removed" && toID != "" {
			entry.NewValue = &types.Issue{
				ID: toID,
			}
			if toTitle != nil {
				entry.NewValue.Title = *toTitle
			}
			if toDesc != nil {
				entry.NewValue.Description = *toDesc
			}
			if toStatus != nil {
				entry.NewValue.Status = types.Status(*toStatus)
			}
			if toPriority != nil {
				entry.NewValue.Priority = *toPriority
			}
		}

		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// ListBranches returns the names of all branches.
// Implements storage.VersionedStorage.
func (s *DoltStore) ListBranches(ctx context.Context) ([]string, error) {
	rows, err := s.queryContext(ctx, "SELECT name FROM dolt_branches ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("failed to list branches: %w", err)
	}
	defer rows.Close()

	var branches []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan branch: %w", err)
		}
		branches = append(branches, name)
	}
	return branches, rows.Err()
}

// GetCurrentCommit returns the hash of the current HEAD commit.
// Implements storage.VersionedStorage.
func (s *DoltStore) GetCurrentCommit(ctx context.Context) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&hash)
	if err != nil {
		return "", fmt.Errorf("failed to get current commit: %w", err)
	}
	return hash, nil
}

// GetConflicts returns any merge conflicts in the current state.
// Implements storage.VersionedStorage.
func (s *DoltStore) GetConflicts(ctx context.Context) ([]storage.Conflict, error) {
	internal, err := s.getInternalConflicts(ctx)
	if err != nil {
		return nil, err
	}

	conflicts := make([]storage.Conflict, 0, len(internal))
	for _, c := range internal {
		conflicts = append(conflicts, storage.Conflict{
			Field: c.TableName,
		})
	}
	return conflicts, nil
}

// CommitExists checks whether a commit hash exists in the repository.
// Returns false for empty strings, malformed input, or non-existent commits.
func (s *DoltStore) CommitExists(ctx context.Context, commitHash string) (bool, error) {
	// Empty string is not a valid commit
	if commitHash == "" {
		return false, nil
	}

	// Validate format to reject malformed input
	if err := validateRef(commitHash); err != nil {
		return false, nil
	}

	// Query dolt_log to check if the commit exists.
	// Supports both full hashes and short prefixes (like git's short SHA).
	// The exact match handles full hashes; LIKE handles prefixes.
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_log
		WHERE commit_hash = ? OR commit_hash LIKE ?
	`, commitHash, commitHash+"%").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check commit existence: %w", err)
	}

	return count > 0, nil
}
