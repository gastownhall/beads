package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// History returns the complete version history for an issue.
func (s *DoltStore) History(ctx context.Context, issueID string) ([]*storage.HistoryEntry, error) {
	var result []*storage.HistoryEntry
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.HistoryInTx(ctx, tx, issueID)
		if err != nil {
			return wrapQueryError("get issue history", err)
		}
		return nil
	})
	return result, err
}

// AsOf returns the state of an issue at a specific commit hash or branch ref.
// Implements storage.VersionedStorage.
func (s *DoltStore) AsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error) {
	return s.getIssueAsOf(ctx, issueID, ref)
}

// Diff returns changes between two commits/branches.
// Implements storage.VersionedStorage.
func (s *DoltStore) Diff(ctx context.Context, fromRef, toRef string) ([]*storage.DiffEntry, error) {
	var result []*storage.DiffEntry
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.DiffInTx(ctx, tx, fromRef, toRef)
		return err
	})
	return result, err
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
		return nil, wrapQueryError("get conflicts", err)
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
