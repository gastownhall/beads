package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// History returns the complete version history for an issue.
//
// Uses withReadTxLongTimeout rather than withReadTx: the underlying
// dolt_history_issues scan can take several seconds to tens of seconds on
// issues with many revisions, which exceeds the shared pool's 10s
// ReadTimeout and otherwise surfaces as an intermittent MySQL i/o timeout
// (ga-ahnxx).
func (s *DoltStore) History(ctx context.Context, issueID string) ([]*storage.HistoryEntry, error) {
	var result []*storage.HistoryEntry
	err := s.withReadTxLongTimeout(ctx, func(tx *sql.Tx) error {
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

// PreviousExternalRef returns the external_ref value recorded for issueID
// as of the most recent commit at or before asOf.
// Implements storage.ExternalRefHistoryQuerier.
func (s *DoltStore) PreviousExternalRef(ctx context.Context, issueID string, asOf time.Time) (string, bool, error) {
	var ref string
	var found bool
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		ref, found, err = issueops.PreviousExternalRefInTx(ctx, tx, issueID, asOf)
		if err != nil {
			return wrapQueryError("get previous external ref", err)
		}
		return nil
	})
	return ref, found, err
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

// GetStateHash returns a hash of the entire database including the working
// set. Unlike GetCurrentCommit it moves on uncommitted writes, so callers can
// detect changes even when dolt auto-commit is off (SQL-server mode, where
// writes land in the working set and HEAD does not advance).
// Implements storage.StateHasher.
func (s *DoltStore) GetStateHash(ctx context.Context) (string, error) {
	var hash string
	if err := s.db.QueryRowContext(ctx, "SELECT DOLT_HASHOF_DB()").Scan(&hash); err == nil {
		return hash, nil
	}
	// Older servers predate DOLT_HASHOF_DB; degrade to HEAD-based detection.
	return s.GetCurrentCommit(ctx)
}

// GetConflicts returns any merge conflicts in the current state.
// Implements storage.VersionedStorage.
func (s *DoltStore) GetConflicts(ctx context.Context) ([]storage.Conflict, error) {
	return versioncontrolops.GetConflicts(ctx, s.db)
}

// The CLI reaches these two methods through storage.UnwrapStore, so the
// assertion must keep holding on the concrete store.
var _ storage.ConflictInspector = (*DoltStore)(nil)

// GetConflictRows returns the live conflicted rows of table, per field.
// Implements storage.ConflictInspector (backs `bd conflicts list|show`).
func (s *DoltStore) GetConflictRows(ctx context.Context, table string) ([]storage.ConflictRow, error) {
	return versioncontrolops.GetConflictRows(ctx, s.db, table)
}

// The CLI reaches this through storage.UnwrapStore too.
var _ storage.MergeBlockerInspector = (*DoltStore)(nil)

// GetMergeBlockers reports schema conflicts, constraint violations, and
// whether a merge is open. Implements storage.MergeBlockerInspector.
func (s *DoltStore) GetMergeBlockers(ctx context.Context) (storage.MergeBlockers, error) {
	return versioncontrolops.GetMergeBlockers(ctx, s.db)
}

// ResolveConflictRows resolves individual conflicted rows of table by key.
// Implements storage.ConflictInspector (backs `bd conflicts resolve <id>`).
//
// It runs on a pinned connection, not the pool: the resolution sets dolt's
// conflict-tolerance session flags, and on *sql.DB a follow-up statement could
// land on a different connection that never saw them.
// The flags it sets are session-scoped, so they are reset before the
// connection goes back to the pool; if the reset fails the connection is
// discarded rather than returned dirty (the discipline
// autoResolveConflictsAfterCLIPull already follows, store.go:3323).
func (s *DoltStore) ResolveConflictRows(ctx context.Context, table string, keys []string, strategy string) (int, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection for conflict resolution: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(ctx,
			"SET @@dolt_allow_commit_conflicts = 0, @@dolt_force_transaction_commit = 0"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}()
	return versioncontrolops.ResolveConflictRows(ctx, conn, table, keys, strategy)
}

// CommitExists checks whether a commit hash exists in the repository.
// Returns false for empty strings, malformed input, or non-existent commits.
func (s *DoltStore) CommitExists(ctx context.Context, commitHash string) (bool, error) {
	return versioncontrolops.CommitExists(ctx, s.db, commitHash)
}
