//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Compile-time interface check.
var _ storage.DoltStorage = (*EmbeddedDoltStore)(nil)

// EmbeddedDoltStore implements storage.DoltStorage backed by the embedded Dolt engine.
// Each method call opens a short-lived connection, executes within an explicit
// SQL transaction, and closes the connection immediately. This minimizes the
// time the embedded engine's write lock is held, reducing contention when
// multiple processes access the same database concurrently.
type EmbeddedDoltStore struct {
	dataDir  string
	database string
	branch   string
	closed   atomic.Bool
}

// errClosed is returned when a method is called after Close.
var errClosed = errors.New("embeddeddolt: store is closed")

// New creates an EmbeddedDoltStore using the embedded Dolt engine.
// beadsDir is the .beads/ root; the data directory is derived as <beadsDir>/embeddeddolt/.
// The database is created automatically if it doesn't exist (initSchema handles this).
func New(ctx context.Context, beadsDir, database, branch string) (*EmbeddedDoltStore, error) {
	// Resolve to absolute path — the embedded dolt driver resolves file://
	// DSN paths relative to its data directory, so relative paths cause
	// doubled-path errors on subsequent opens.
	absBeadsDir, err := filepath.Abs(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: resolving beads dir: %w", err)
	}
	dataDir := filepath.Join(absBeadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, fmt.Errorf("embeddeddolt: creating data directory: %w", err)
	}

	s := &EmbeddedDoltStore{
		dataDir:  dataDir,
		database: database,
		branch:   branch,
	}

	if err := s.initSchema(ctx); err != nil {
		return nil, fmt.Errorf("embeddeddolt: init schema: %w", err)
	}

	return s, nil
}

// withRootConn opens a short-lived database connection without selecting any
// database or branch, begins an explicit SQL transaction, and passes it to fn.
// This is used during initialization when the database may not yet exist.
func (s *EmbeddedDoltStore) withRootConn(ctx context.Context, commit bool, fn func(tx *sql.Tx) error) (err error) {
	if s.closed.Load() {
		err = errClosed
		return
	}

	var db *sql.DB
	var cleanup func() error
	db, cleanup, err = OpenSQL(ctx, s.dataDir, "", "")
	if err != nil {
		return
	}

	defer func() {
		err = errors.Join(err, cleanup())
	}()

	var tx *sql.Tx
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		err = fmt.Errorf("embeddeddolt: begin tx: %w", err)
		return
	}

	err = fn(tx)
	if err != nil {
		err = errors.Join(err, tx.Rollback())
		return
	}

	if !commit {
		return tx.Rollback()
	}

	err = tx.Commit()
	return
}

// withConn opens a short-lived database connection configured for the store's
// database and branch, begins an explicit SQL transaction, and passes it to
// fn. If commit is true and fn returns nil, the transaction is committed;
// otherwise it is rolled back. The connection is closed before withConn
// returns regardless of outcome.
//
// The database must already exist (created during initSchema).
func (s *EmbeddedDoltStore) withConn(ctx context.Context, commit bool, fn func(tx *sql.Tx) error) (err error) {
	if s.closed.Load() {
		err = errClosed
		return
	}

	var db *sql.DB
	var cleanup func() error
	db, cleanup, err = OpenSQL(ctx, s.dataDir, s.database, s.branch)
	if err != nil {
		return
	}

	defer func() {
		err = errors.Join(err, cleanup())
	}()

	var tx *sql.Tx
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		err = fmt.Errorf("embeddeddolt: begin tx: %w", err)
		return
	}

	err = fn(tx)
	if err != nil {
		err = errors.Join(err, tx.Rollback())
		return
	}

	if !commit {
		return tx.Rollback()
	}

	err = tx.Commit()
	return
}

// initSchema creates the database (if needed) and runs all pending migrations,
// committing them to Dolt history. Uses withRootConn so the database can be
// created before USE; this avoids running CREATE DATABASE inside withConn,
// which is not safe for concurrent use in the embedded Dolt engine.
func (s *EmbeddedDoltStore) initSchema(ctx context.Context) error {
	return s.withRootConn(ctx, true, func(tx *sql.Tx) error {
		if s.database != "" {
			if !validIdentifier.MatchString(s.database) {
				return fmt.Errorf("embeddeddolt: invalid database name: %q", s.database)
			}
			if _, err := tx.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+s.database+"`"); err != nil {
				return fmt.Errorf("embeddeddolt: creating database: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "USE `"+s.database+"`"); err != nil {
				return fmt.Errorf("embeddeddolt: switching to database: %w", err)
			}
			if s.branch != "" {
				if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET @@%s_head_ref = %s", s.database, sqlStringLiteral(s.branch))); err != nil {
					return fmt.Errorf("embeddeddolt: setting branch: %w", err)
				}
			}
		}

		applied, err := migrateUp(ctx, tx)
		if err != nil {
			return err
		}
		if applied > 0 {
			if _, err := tx.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
				return fmt.Errorf("dolt add after migrations: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'schema: apply migrations')"); err != nil {
				return fmt.Errorf("dolt commit after migrations: %w", err)
			}
		}
		return nil
	})
}

// GetIssue is implemented in get_issue.go.

func (s *EmbeddedDoltStore) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	panic("embeddeddolt: GetIssueByExternalRef not implemented")
}

func (s *EmbeddedDoltStore) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetIssuesByIDs not implemented")
}

func (s *EmbeddedDoltStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	panic("embeddeddolt: UpdateIssue not implemented")
}

func (s *EmbeddedDoltStore) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	panic("embeddeddolt: CloseIssue not implemented")
}

func (s *EmbeddedDoltStore) DeleteIssue(ctx context.Context, id string) error {
	panic("embeddeddolt: DeleteIssue not implemented")
}

// AddDependency is implemented in dependencies.go.

func (s *EmbeddedDoltStore) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	panic("embeddeddolt: RemoveDependency not implemented")
}

func (s *EmbeddedDoltStore) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetDependencies not implemented")
}

func (s *EmbeddedDoltStore) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetDependents not implemented")
}

func (s *EmbeddedDoltStore) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	panic("embeddeddolt: GetDependenciesWithMetadata not implemented")
}

func (s *EmbeddedDoltStore) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	panic("embeddeddolt: GetDependentsWithMetadata not implemented")
}

func (s *EmbeddedDoltStore) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	panic("embeddeddolt: GetDependencyTree not implemented")
}

// AddLabel is implemented in labels.go.

func (s *EmbeddedDoltStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	panic("embeddeddolt: RemoveLabel not implemented")
}

// GetLabels is implemented in labels.go.

func (s *EmbeddedDoltStore) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetIssuesByLabel not implemented")
}

func (s *EmbeddedDoltStore) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	panic("embeddeddolt: GetReadyWork not implemented")
}

func (s *EmbeddedDoltStore) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	panic("embeddeddolt: GetBlockedIssues not implemented")
}

func (s *EmbeddedDoltStore) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	panic("embeddeddolt: GetEpicsEligibleForClosure not implemented")
}

func (s *EmbeddedDoltStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	panic("embeddeddolt: AddIssueComment not implemented")
}

func (s *EmbeddedDoltStore) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	panic("embeddeddolt: GetIssueComments not implemented")
}

func (s *EmbeddedDoltStore) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	panic("embeddeddolt: GetEvents not implemented")
}

func (s *EmbeddedDoltStore) GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error) {
	panic("embeddeddolt: GetAllEventsSince not implemented")
}

func (s *EmbeddedDoltStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	panic("embeddeddolt: RunInTransaction not implemented")
}

// Close marks the store as closed. Subsequent method calls will return errClosed.
// It is safe to call multiple times.
func (s *EmbeddedDoltStore) Close() error {
	s.closed.Store(true)
	return nil
}

// ---------------------------------------------------------------------------
// storage.VersionControl
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) Branch(ctx context.Context, name string) error {
	panic("embeddeddolt: Branch not implemented")
}

func (s *EmbeddedDoltStore) Checkout(ctx context.Context, branch string) error {
	panic("embeddeddolt: Checkout not implemented")
}

func (s *EmbeddedDoltStore) CurrentBranch(ctx context.Context) (string, error) {
	panic("embeddeddolt: CurrentBranch not implemented")
}

func (s *EmbeddedDoltStore) DeleteBranch(ctx context.Context, branch string) error {
	panic("embeddeddolt: DeleteBranch not implemented")
}

func (s *EmbeddedDoltStore) ListBranches(ctx context.Context) ([]string, error) {
	panic("embeddeddolt: ListBranches not implemented")
}

func (s *EmbeddedDoltStore) CommitPending(ctx context.Context, actor string) (bool, error) {
	var hasPending bool
	var msg string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		hasPending, err = issueops.HasPendingChanges(ctx, tx)
		if err != nil {
			return err
		}
		if hasPending {
			msg = issueops.BuildBatchCommitMessage(ctx, tx, actor)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if !hasPending {
		return false, nil
	}

	if err := s.Commit(ctx, msg); err != nil {
		if issueops.IsNothingToCommitError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *EmbeddedDoltStore) CommitExists(ctx context.Context, commitHash string) (bool, error) {
	panic("embeddeddolt: CommitExists not implemented")
}

func (s *EmbeddedDoltStore) GetCurrentCommit(ctx context.Context) (string, error) {
	var hash string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&hash)
	})
	return hash, err
}

func (s *EmbeddedDoltStore) Status(ctx context.Context) (*storage.Status, error) {
	panic("embeddeddolt: Status not implemented")
}

func (s *EmbeddedDoltStore) Log(ctx context.Context, limit int) ([]storage.CommitInfo, error) {
	panic("embeddeddolt: Log not implemented")
}

func (s *EmbeddedDoltStore) Merge(ctx context.Context, branch string) ([]storage.Conflict, error) {
	panic("embeddeddolt: Merge not implemented")
}

func (s *EmbeddedDoltStore) GetConflicts(ctx context.Context) ([]storage.Conflict, error) {
	panic("embeddeddolt: GetConflicts not implemented")
}

func (s *EmbeddedDoltStore) ResolveConflicts(ctx context.Context, table string, strategy string) error {
	panic("embeddeddolt: ResolveConflicts not implemented")
}

// ---------------------------------------------------------------------------
// storage.HistoryViewer
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) History(ctx context.Context, issueID string) ([]*storage.HistoryEntry, error) {
	panic("embeddeddolt: History not implemented")
}

func (s *EmbeddedDoltStore) AsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error) {
	panic("embeddeddolt: AsOf not implemented")
}

func (s *EmbeddedDoltStore) Diff(ctx context.Context, fromRef, toRef string) ([]*storage.DiffEntry, error) {
	panic("embeddeddolt: Diff not implemented")
}

// ---------------------------------------------------------------------------
// storage.RemoteStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) RemoveRemote(ctx context.Context, name string) error {
	panic("embeddeddolt: RemoveRemote not implemented")
}

func (s *EmbeddedDoltStore) ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error) {
	panic("embeddeddolt: ListRemotes not implemented")
}

func (s *EmbeddedDoltStore) Push(ctx context.Context) error {
	panic("embeddeddolt: Push not implemented")
}

func (s *EmbeddedDoltStore) Pull(ctx context.Context) error {
	panic("embeddeddolt: Pull not implemented")
}

func (s *EmbeddedDoltStore) ForcePush(ctx context.Context) error {
	panic("embeddeddolt: ForcePush not implemented")
}

func (s *EmbeddedDoltStore) Fetch(ctx context.Context, peer string) error {
	panic("embeddeddolt: Fetch not implemented")
}

func (s *EmbeddedDoltStore) PushTo(ctx context.Context, peer string) error {
	panic("embeddeddolt: PushTo not implemented")
}

func (s *EmbeddedDoltStore) PullFrom(ctx context.Context, peer string) ([]storage.Conflict, error) {
	panic("embeddeddolt: PullFrom not implemented")
}

// ---------------------------------------------------------------------------
// storage.SyncStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) Sync(ctx context.Context, peer string, strategy string) (*storage.SyncResult, error) {
	panic("embeddeddolt: Sync not implemented")
}

func (s *EmbeddedDoltStore) SyncStatus(ctx context.Context, peer string) (*storage.SyncStatus, error) {
	panic("embeddeddolt: SyncStatus not implemented")
}

// ---------------------------------------------------------------------------
// storage.FederationStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) AddFederationPeer(ctx context.Context, peer *storage.FederationPeer) error {
	panic("embeddeddolt: AddFederationPeer not implemented")
}

func (s *EmbeddedDoltStore) GetFederationPeer(ctx context.Context, name string) (*storage.FederationPeer, error) {
	panic("embeddeddolt: GetFederationPeer not implemented")
}

func (s *EmbeddedDoltStore) ListFederationPeers(ctx context.Context) ([]*storage.FederationPeer, error) {
	panic("embeddeddolt: ListFederationPeers not implemented")
}

func (s *EmbeddedDoltStore) RemoveFederationPeer(ctx context.Context, name string) error {
	panic("embeddeddolt: RemoveFederationPeer not implemented")
}

// ---------------------------------------------------------------------------
// storage.BulkIssueStore
// ---------------------------------------------------------------------------

// CreateIssuesWithFullOptions is implemented in create_issue.go.

func (s *EmbeddedDoltStore) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	panic("embeddeddolt: DeleteIssues not implemented")
}

func (s *EmbeddedDoltStore) DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error) {
	panic("embeddeddolt: DeleteIssuesBySourceRepo not implemented")
}

func (s *EmbeddedDoltStore) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	panic("embeddeddolt: UpdateIssueID not implemented")
}

func (s *EmbeddedDoltStore) ClaimIssue(ctx context.Context, id string, actor string) error {
	panic("embeddeddolt: ClaimIssue not implemented")
}

func (s *EmbeddedDoltStore) PromoteFromEphemeral(ctx context.Context, id string, actor string) error {
	panic("embeddeddolt: PromoteFromEphemeral not implemented")
}

// GetNextChildID is implemented in child_id.go.

func (s *EmbeddedDoltStore) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	panic("embeddeddolt: RenameCounterPrefix not implemented")
}

// ---------------------------------------------------------------------------
// storage.DependencyQueryStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	var result []*types.Dependency
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		m, err := issueops.GetDependencyRecordsForIssuesInTx(ctx, tx, []string{issueID})
		if err != nil {
			return err
		}
		result = m[issueID]
		return nil
	})
	return result, err
}

func (s *EmbeddedDoltStore) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	panic("embeddeddolt: IsBlocked not implemented")
}

func (s *EmbeddedDoltStore) GetNewlyUnblockedByClose(ctx context.Context, closedIssueID string) ([]*types.Issue, error) {
	panic("embeddeddolt: GetNewlyUnblockedByClose not implemented")
}

func (s *EmbeddedDoltStore) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	panic("embeddeddolt: DetectCycles not implemented")
}

func (s *EmbeddedDoltStore) FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error) {
	panic("embeddeddolt: FindWispDependentsRecursive not implemented")
}

func (s *EmbeddedDoltStore) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	panic("embeddeddolt: RenameDependencyPrefix not implemented")
}

// ---------------------------------------------------------------------------
// storage.AnnotationQueryStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) AddComment(ctx context.Context, issueID, actor, comment string) error {
	panic("embeddeddolt: AddComment not implemented")
}

func (s *EmbeddedDoltStore) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	panic("embeddeddolt: ImportIssueComment not implemented")
}

func (s *EmbeddedDoltStore) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	panic("embeddeddolt: GetCommentsForIssues not implemented")
}

// ---------------------------------------------------------------------------
// storage.ConfigMetadataStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) DeleteConfig(ctx context.Context, key string) error {
	panic("embeddeddolt: DeleteConfig not implemented")
}

func (s *EmbeddedDoltStore) GetCustomStatuses(ctx context.Context) ([]string, error) {
	var result []string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetCustomStatusesTx(ctx, tx)
		return err
	})
	if err != nil || len(result) == 0 {
		return config.GetCustomStatusesFromYAML(), nil
	}
	return result, nil
}

func (s *EmbeddedDoltStore) GetCustomTypes(ctx context.Context) ([]string, error) {
	var result []string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetCustomTypesTx(ctx, tx)
		return err
	})
	if err != nil || len(result) == 0 {
		return config.GetCustomTypesFromYAML(), nil
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// storage.CompactionStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error) {
	panic("embeddeddolt: CheckEligibility not implemented")
}

func (s *EmbeddedDoltStore) ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, compactedSize int, commitHash string) error {
	panic("embeddeddolt: ApplyCompaction not implemented")
}

func (s *EmbeddedDoltStore) GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	panic("embeddeddolt: GetTier1Candidates not implemented")
}

func (s *EmbeddedDoltStore) GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	panic("embeddeddolt: GetTier2Candidates not implemented")
}

// ---------------------------------------------------------------------------
// storage.AdvancedQueryStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) GetRepoMtime(ctx context.Context, repoPath string) (int64, error) {
	panic("embeddeddolt: GetRepoMtime not implemented")
}

func (s *EmbeddedDoltStore) SetRepoMtime(ctx context.Context, repoPath, jsonlPath string, mtimeNs int64) error {
	panic("embeddeddolt: SetRepoMtime not implemented")
}

func (s *EmbeddedDoltStore) ClearRepoMtime(ctx context.Context, repoPath string) error {
	panic("embeddeddolt: ClearRepoMtime not implemented")
}

func (s *EmbeddedDoltStore) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	panic("embeddeddolt: GetMoleculeProgress not implemented")
}

func (s *EmbeddedDoltStore) GetMoleculeLastActivity(ctx context.Context, moleculeID string) (*types.MoleculeLastActivity, error) {
	panic("embeddeddolt: GetMoleculeLastActivity not implemented")
}

func (s *EmbeddedDoltStore) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	panic("embeddeddolt: GetStaleIssues not implemented")
}
