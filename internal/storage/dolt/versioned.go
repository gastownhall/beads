package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
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

// ChangedIssueIDs returns the set of issue IDs whose data differs between
// fromCommit and toCommit, derived from dolt_diff over the issues, labels,
// dependencies, and comments tables. An issue is reported under Removed
// only when the issues-table row itself was deleted; label/dep/comment row
// deletions for surviving issues fall under Upserted.
//
// Implements storage.DiffStore.
func (s *DoltStore) ChangedIssueIDs(ctx context.Context, fromCommit, toCommit string) (storage.ChangedIssueIDs, error) {
	var out storage.ChangedIssueIDs
	if fromCommit == "" || toCommit == "" {
		return out, fmt.Errorf("ChangedIssueIDs: fromCommit and toCommit required")
	}
	// Dolt's dolt_diff() table function does not accept prepared-statement
	// bind parameters — its arguments must be SQL string literals. That
	// makes us inline the commit hashes into the query, so validate them
	// aggressively to keep the path free of injection risk.
	if !isSafeCommitRef(fromCommit) {
		return out, fmt.Errorf("ChangedIssueIDs: fromCommit %q is not a valid commit ref", fromCommit)
	}
	if !isSafeCommitRef(toCommit) {
		return out, fmt.Errorf("ChangedIssueIDs: toCommit %q is not a valid commit ref", toCommit)
	}

	// dolt_diff returns from_<col>/to_<col> for every column plus diff_type.
	// COALESCE(to_id, from_id) extracts the row's identifying value regardless
	// of whether the row was added, modified, or removed.
	//
	// We UNION results from the four tables that contribute to auto-export's
	// per-issue JSON record. Any change to any of them means the issue's
	// export representation has changed.
	//
	//nolint:gosec // G201: fromCommit/toCommit are constrained to [A-Za-z0-9]
	// by isSafeCommitRef above; dolt_diff() does not accept bind parameters.
	q := fmt.Sprintf(`
		SELECT id, MAX(is_removed) AS is_removed
		FROM (
			SELECT COALESCE(to_id, from_id) AS id,
			       CASE WHEN diff_type = 'removed' THEN 1 ELSE 0 END AS is_removed
			FROM dolt_diff('%[1]s', '%[2]s', 'issues')
			UNION ALL
			SELECT COALESCE(to_issue_id, from_issue_id) AS id, 0 AS is_removed
			FROM dolt_diff('%[1]s', '%[2]s', 'labels')
			UNION ALL
			SELECT COALESCE(to_issue_id, from_issue_id) AS id, 0 AS is_removed
			FROM dolt_diff('%[1]s', '%[2]s', 'dependencies')
			UNION ALL
			SELECT COALESCE(to_issue_id, from_issue_id) AS id, 0 AS is_removed
			FROM dolt_diff('%[1]s', '%[2]s', 'comments')
		) AS u
		WHERE id IS NOT NULL AND id <> ''
		GROUP BY id
	`, fromCommit, toCommit)
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return out, fmt.Errorf("dolt_diff query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var removed int
		if err := rows.Scan(&id, &removed); err != nil {
			return out, fmt.Errorf("scan dolt_diff row: %w", err)
		}
		// An issue row that was removed in 'issues' and then re-added in a
		// later commit in the same range shows up once here with
		// is_removed=1 for the removal and 0 for the re-add; MAX collapses
		// to 1. That's a corner case we accept — a removed-then-readded
		// issue simply gets re-exported as removed and the next export
		// cycle will pick up the new row.
		if removed == 1 {
			out.Removed = append(out.Removed, id)
		} else {
			out.Upserted = append(out.Upserted, id)
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("iterate dolt_diff rows: %w", err)
	}
	return out, nil
}

// ListBranches returns the names of all branches.
// Implements storage.VersionedStorage.
func (s *DoltStore) ListBranches(ctx context.Context) ([]string, error) {
	return versioncontrolops.ListBranches(ctx, s.db)
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
	return versioncontrolops.GetConflicts(ctx, s.db)
}

// CommitExists checks whether a commit hash exists in the repository.
// Returns false for empty strings, malformed input, or non-existent commits.
func (s *DoltStore) CommitExists(ctx context.Context, commitHash string) (bool, error) {
	return versioncontrolops.CommitExists(ctx, s.db, commitHash)
}

// isSafeCommitRef reports whether s may be inlined into a dolt_diff()
// SQL literal without exposing an injection surface. Dolt commit hashes
// are ASCII base32 (a-z + digits); we also allow an explicit-length cap
// to catch accidental truncation or giant inputs.
func isSafeCommitRef(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}
