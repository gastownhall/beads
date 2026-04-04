//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// Compile-time interface checks.
var _ storage.DoltStorage = (*EmbeddedDoltStore)(nil)
var _ storage.StoreLocator = (*EmbeddedDoltStore)(nil)
var _ storage.GarbageCollector = (*EmbeddedDoltStore)(nil)
var _ storage.Flattener = (*EmbeddedDoltStore)(nil)
var _ storage.Compactor = (*EmbeddedDoltStore)(nil)

// EmbeddedDoltStore implements storage.DoltStorage backed by the embedded Dolt engine.
// Each method call opens a short-lived connection, executes within an explicit
// SQL transaction, and closes the connection immediately. This minimizes the
// time the embedded engine's write lock is held, reducing contention when
// multiple processes access the same database concurrently.
//
// The store holds an exclusive flock on the data directory for its entire
// lifetime. This prevents concurrent processes from initializing the embedded
// Dolt engine on the same directory, which causes a nil-pointer panic in
// DoltDB.SetCrashOnFatalError (GH#2571).
type EmbeddedDoltStore struct {
	dataDir       string
	beadsDir      string
	database      string
	branch        string
	credentialKey []byte
	closed        atomic.Bool
	lock          Unlocker // exclusive flock held for the store's lifetime
	ownsLock      bool     // true when New acquired the lock (false when caller supplied it via WithLock)
}

// errClosed is returned when a method is called after Close.
var errClosed = errors.New("embeddeddolt: store is closed")

// Option configures optional behavior for New.
type Option func(*options)

type options struct {
	lock Unlocker // pre-acquired lock; nil means New acquires its own
}

// WithLock passes a pre-acquired exclusive lock to New so it does not attempt
// to acquire a second one. The caller retains ownership — Close will NOT
// release a caller-supplied lock. This is used by bd init, which acquires the
// lock earlier to protect pre-initialization steps.
func WithLock(lock Unlocker) Option {
	return func(o *options) { o.lock = lock }
}

// New creates an EmbeddedDoltStore using the embedded Dolt engine.
// beadsDir is the .beads/ root; the data directory is derived as <beadsDir>/embeddeddolt/.
// The database is created automatically if it doesn't exist (initSchema handles this).
//
// An exclusive flock is held on the data directory for the store's entire
// lifetime. If another process already holds the lock, New queues with
// exponential backoff until the lock becomes available or the context is
// canceled, instead of panicking during concurrent engine initialization
// (GH#2571). The lock is released when Close is called, unless a pre-acquired
// lock was supplied via WithLock (in which case the caller is responsible for it).
func New(ctx context.Context, beadsDir, database, branch string, opts ...Option) (*EmbeddedDoltStore, error) {
	var o options
	for _, fn := range opts {
		fn(&o)
	}

	// Resolve to absolute path — the embedded dolt driver resolves file://
	// DSN paths relative to its data directory, so relative paths cause
	// doubled-path errors on subsequent opens.
	absBeadsDir, err := filepath.Abs(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: resolving beads dir: %w", err)
	}
	dataDir := filepath.Join(absBeadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("embeddeddolt: creating data directory: %w", err)
	}

	// Acquire an exclusive flock before initializing the embedded engine.
	// Without this, concurrent processes race through NewConnector →
	// DoltDB.SetCrashOnFatalError → newDatabase → CollectDBs and one of
	// them panics with a nil-pointer dereference (GH#2571).
	lock := o.lock
	ownsLock := lock == nil
	if ownsLock {
		lock, err = WaitLock(ctx, dataDir)
		if err != nil {
			return nil, err
		}
	}

	s := &EmbeddedDoltStore{
		dataDir:  dataDir,
		beadsDir: absBeadsDir,
		database: database,
		branch:   branch,
		lock:     lock,
		ownsLock: ownsLock,
	}

	if err := s.initSchema(ctx); err != nil {
		if ownsLock {
			lock.Unlock()
		}
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
				// Backfill migrations may only create dolt_ignore'd tables (e.g. wisps),
				// leaving nothing staged for commit. This is expected.
				if !strings.Contains(err.Error(), "nothing to commit") {
					return fmt.Errorf("dolt commit after migrations: %w", err)
				}
			}
		}
		return nil
	})
}

// GetIssue is implemented in get_issue.go.

func (s *EmbeddedDoltStore) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	var id string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		id, err = issueops.GetIssueByExternalRefInTx(ctx, tx, externalRef)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// GetIssuesByIDs is implemented in dependencies.go.

// UpdateIssue is implemented in issues.go.

// CloseIssue is implemented in issues.go.

func (s *EmbeddedDoltStore) DeleteIssue(ctx context.Context, id string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.DeleteIssueInTx(ctx, tx, id)
	})
}

// AddDependency is implemented in dependencies.go.

// RemoveDependency is implemented in dependencies.go.

func (s *EmbeddedDoltStore) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependenciesInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependentsInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// GetDependenciesWithMetadata is implemented in dependencies.go.

// GetDependentsWithMetadata is implemented in dependencies.go.

func (s *EmbeddedDoltStore) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	var result []*types.TreeNode
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependencyTreeInTx(ctx, tx, issueID, maxDepth, showAllPaths, reverse)
		return err
	})
	return result, err
}

// AddLabel is implemented in labels.go.

// RemoveLabel is implemented in labels.go.

// GetLabels is implemented in labels.go.

func (s *EmbeddedDoltStore) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	var ids []string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		ids, err = issueops.GetIssuesByLabelInTx(ctx, tx, label)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssuesByIDs(ctx, ids)
}

// GetReadyWork is implemented in queries.go.

func (s *EmbeddedDoltStore) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	var result []*types.BlockedIssue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetBlockedIssuesInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	var result []*types.EpicStatus
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetEpicsEligibleForClosureInTx(ctx, tx)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	var result *types.Comment
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.AddIssueCommentInTx(ctx, tx, issueID, author, text)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	var result []*types.Comment
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetIssueCommentsInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	var result []*types.Event
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetEventsInTx(ctx, tx, issueID, limit)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error) {
	var result []*types.Event
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetAllEventsSinceInTx(ctx, tx, since)
		return err
	})
	return result, err
}

// RunInTransaction is implemented in transaction.go.

// Close marks the store as closed and releases the exclusive flock on the data
// directory (if the store owns it). Subsequent method calls will return errClosed.
// It is safe to call multiple times. When the lock was supplied by the caller
// via WithLock, Close does NOT release it — the caller retains ownership.
func (s *EmbeddedDoltStore) Close() error {
	// Use CompareAndSwap so we only unlock once even if Close is called
	// multiple times (the Lock.Unlock method panics on double-unlock).
	if s.closed.CompareAndSwap(false, true) {
		if s.lock != nil && s.ownsLock {
			s.lock.Unlock()
		}
	}
	return nil
}

// DoltGC runs Dolt garbage collection to reclaim disk space.
func (s *EmbeddedDoltStore) DoltGC(ctx context.Context) error {
	return s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.DoltGC(ctx, db)
	})
}

// Flatten squashes all Dolt commit history into a single commit.
// Pins a single *sql.Conn for session-scoped stored procedures.
func (s *EmbeddedDoltStore) Flatten(ctx context.Context) error {
	return s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		if pooled, ok := db.(*sql.DB); ok {
			conn, err := pooled.Conn(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			return versioncontrolops.Flatten(ctx, conn)
		}
		return versioncontrolops.Flatten(ctx, db)
	})
}

// Compact squashes old Dolt commits while preserving recent ones.
// Pins a single *sql.Conn for session-scoped stored procedures.
func (s *EmbeddedDoltStore) Compact(ctx context.Context, initialHash, boundaryHash string, oldCommits int, recentHashes []string) error {
	return s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		// withDBConn returns *sql.DB; pin a single connection for
		// session-scoped operations (checkout, reset, cherry-pick).
		if pooled, ok := db.(*sql.DB); ok {
			conn, err := pooled.Conn(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			return versioncontrolops.Compact(ctx, conn, initialHash, boundaryHash, oldCommits, recentHashes)
		}
		return versioncontrolops.Compact(ctx, db, initialHash, boundaryHash, oldCommits, recentHashes)
	})
}

// Path returns the embedded dolt data directory (.beads/embeddeddolt/).
func (s *EmbeddedDoltStore) Path() string {
	return s.dataDir
}

// CLIDir returns the directory for dolt CLI operations (push/pull/remote).
// This is the actual database directory within the data dir.
func (s *EmbeddedDoltStore) CLIDir() string {
	if s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, s.database)
}

// ---------------------------------------------------------------------------
// storage.VersionControl
// ---------------------------------------------------------------------------

// Branch, Checkout, CurrentBranch, DeleteBranch, ListBranches are
// implemented in version_control.go via versioncontrolops.

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

// CommitExists is implemented in version_control.go via versioncontrolops.

func (s *EmbeddedDoltStore) GetCurrentCommit(ctx context.Context) (string, error) {
	var hash string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&hash)
	})
	return hash, err
}

// Status, Log, Merge, GetConflicts, ResolveConflicts are implemented in
// version_control.go via versioncontrolops.

// ---------------------------------------------------------------------------
// storage.HistoryViewer
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) History(ctx context.Context, issueID string) ([]*storage.HistoryEntry, error) {
	var result []*storage.HistoryEntry
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.HistoryInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) AsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error) {
	var result *types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.AsOfInTx(ctx, tx, issueID, ref)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) Diff(ctx context.Context, fromRef, toRef string) ([]*storage.DiffEntry, error) {
	var result []*storage.DiffEntry
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.DiffInTx(ctx, tx, fromRef, toRef)
		return err
	})
	return result, err
}

// ---------------------------------------------------------------------------
// storage.RemoteStore
// ---------------------------------------------------------------------------

// RemoveRemote, ListRemotes, Push, Pull, ForcePush, Fetch, PushTo, PullFrom
// are implemented in version_control.go via versioncontrolops.

// ---------------------------------------------------------------------------
// storage.SyncStore
// ---------------------------------------------------------------------------

// Sync and SyncStatus are implemented in federation.go.

// ---------------------------------------------------------------------------
// storage.FederationStore
// ---------------------------------------------------------------------------

// AddFederationPeer, GetFederationPeer, ListFederationPeers, RemoveFederationPeer
// are implemented in federation.go via issueops.

// ---------------------------------------------------------------------------
// storage.BulkIssueStore
// ---------------------------------------------------------------------------

// CreateIssuesWithFullOptions is implemented in create_issue.go.

func (s *EmbeddedDoltStore) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	var result *types.DeleteIssuesResult
	err := s.withConn(ctx, !dryRun, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.DeleteIssuesInTx(ctx, tx, ids, cascade, force, dryRun)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) DeleteIssuesBySourceRepo(ctx context.Context, sourceRepo string) (int, error) {
	var count int
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		count, err = issueops.DeleteIssuesBySourceRepoInTx(ctx, tx, sourceRepo)
		return err
	})
	return count, err
}

func (s *EmbeddedDoltStore) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.UpdateIssueIDInTx(ctx, tx, oldID, newID, issue, actor)
	})
}

// ClaimIssue is implemented in issues.go.

func (s *EmbeddedDoltStore) PromoteFromEphemeral(ctx context.Context, id string, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.PromoteFromEphemeralInTx(ctx, tx, id, actor)
	})
}

// GetNextChildID is implemented in child_id.go.

func (s *EmbeddedDoltStore) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return nil // Hash-based IDs don't use counters.
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

// IsBlocked is implemented in issues.go.

// GetNewlyUnblockedByClose is implemented in issues.go.

// DetectCycles is implemented in dependencies.go.

func (s *EmbeddedDoltStore) FindWispDependentsRecursive(ctx context.Context, ids []string) (map[string]bool, error) {
	var result map[string]bool
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.FindWispDependentsRecursiveInTx(ctx, tx, ids)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.RenameDependencyPrefixInTx(ctx, tx, oldPrefix, newPrefix)
	})
}

// ---------------------------------------------------------------------------
// storage.AnnotationQueryStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.AddCommentEventInTx(ctx, tx, issueID, actor, comment)
	})
}

func (s *EmbeddedDoltStore) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	var result *types.Comment
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.ImportIssueCommentInTx(ctx, tx, issueID, author, text, createdAt)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	var result map[string][]*types.Comment
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetCommentsForIssuesInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
}

// ---------------------------------------------------------------------------
// storage.ConfigMetadataStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) DeleteConfig(ctx context.Context, key string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.DeleteConfigInTx(ctx, tx, key)
	})
}

func (s *EmbeddedDoltStore) GetCustomStatuses(ctx context.Context) ([]string, error) {
	detailed, err := s.GetCustomStatusesDetailed(ctx)
	if err != nil {
		return nil, err
	}
	return types.CustomStatusNames(detailed), nil
}

func (s *EmbeddedDoltStore) GetCustomStatusesDetailed(ctx context.Context) ([]types.CustomStatus, error) {
	var result []types.CustomStatus
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var txErr error
		result, txErr = issueops.ResolveCustomStatusesDetailedInTx(ctx, tx)
		return txErr
	})
	if err != nil {
		// DB unavailable — fall back to config.yaml.
		if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
			return issueops.ParseStatusFallback(yamlStatuses), nil
		}
		return nil, nil
	}
	return result, nil
}

func (s *EmbeddedDoltStore) GetCustomTypes(ctx context.Context) ([]string, error) {
	var result []string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var txErr error
		result, txErr = issueops.ResolveCustomTypesInTx(ctx, tx)
		return txErr
	})
	if err != nil {
		// DB unavailable — fall back to config.yaml.
		if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
			return yamlTypes, nil
		}
		return nil, nil
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// storage.CompactionStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error) {
	var eligible bool
	var reason string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		eligible, reason, err = issueops.CheckEligibilityInTx(ctx, tx, issueID, tier)
		return err
	})
	return eligible, reason, err
}

func (s *EmbeddedDoltStore) ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, _ int, commitHash string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.ApplyCompactionInTx(ctx, tx, issueID, tier, originalSize, commitHash)
	})
}

func (s *EmbeddedDoltStore) GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	var result []*types.CompactionCandidate
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetTier1CandidatesInTx(ctx, tx)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	var result []*types.CompactionCandidate
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetTier2CandidatesInTx(ctx, tx)
		return err
	})
	return result, err
}

// ---------------------------------------------------------------------------
// storage.AdvancedQueryStore
// ---------------------------------------------------------------------------

func (s *EmbeddedDoltStore) GetRepoMtime(ctx context.Context, repoPath string) (int64, error) {
	var result int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetRepoMtimeInTx(ctx, tx, repoPath)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) SetRepoMtime(ctx context.Context, repoPath, jsonlPath string, mtimeNs int64) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.SetRepoMtimeInTx(ctx, tx, repoPath, jsonlPath, mtimeNs)
	})
}

func (s *EmbeddedDoltStore) ClearRepoMtime(ctx context.Context, repoPath string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.ClearRepoMtimeInTx(ctx, tx, repoPath)
	})
}

// GetMoleculeProgress is implemented in queries.go.

func (s *EmbeddedDoltStore) GetMoleculeLastActivity(ctx context.Context, moleculeID string) (*types.MoleculeLastActivity, error) {
	var result *types.MoleculeLastActivity
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetMoleculeLastActivityInTx(ctx, tx, moleculeID)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetStaleIssuesInTx(ctx, tx, filter)
		return err
	})
	return result, err
}
