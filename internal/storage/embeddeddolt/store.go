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
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// Compile-time interface checks.
var _ storage.DoltStorage = (*EmbeddedDoltStore)(nil)
var _ storage.StoreLocator = (*EmbeddedDoltStore)(nil)
var _ storage.GarbageCollector = (*EmbeddedDoltStore)(nil)
var _ storage.Flattener = (*EmbeddedDoltStore)(nil)
var _ storage.Compactor = (*EmbeddedDoltStore)(nil)
var _ storage.SchemaMigrator = (*EmbeddedDoltStore)(nil)

// EmbeddedDoltStore implements storage.DoltStorage backed by the embedded Dolt engine.
// Each method call opens a short-lived connection, executes within an explicit
// SQL transaction, and closes the connection immediately. This minimizes the
// time the embedded engine's write lock is held, reducing contention when
// multiple processes access the same database concurrently.
//
// The dolthub/driver/v2 handles its own concurrency internally. File-level locking
// is only used during bd init to protect one-time initialization steps.
type EmbeddedDoltStore struct {
	dataDir       string
	beadsDir      string
	database      string
	branch        string
	credentialKey []byte
	closed        atomic.Bool
	// readOnly marks a store opened via OpenReadOnly: open-time mutations
	// (CREATE DATABASE, schema migrations) were skipped and write
	// transactions are refused (bd-6dnrw.32).
	readOnly bool
	// intent records why this store was opened, controlling how lenient
	// initSchema is about pending-migration refusals it would otherwise treat
	// as fatal. Unlike readOnly, a non-strict intent still allows writes
	// (e.g. the post-command autocommit net, or the commit itself) - only the
	// migration step is skipped.
	intent openIntent
}

// openIntent classifies why a store is being opened. openStrict fails the
// open on any pending-migration refusal; the other two intents relax both
// the #4259 remote-migrate gate refusal and the #4566 dirty-table refusal,
// each with its own warning text (see initSchema).
type openIntent int

const (
	// openStrict is the default: any pending-migration refusal fails the
	// open. Used by Open.
	openStrict openIntent = iota
	// openReadOnlyCommand relaxes both refusals for read-only commands: they
	// must keep working on the current schema until the operator makes the
	// migrate-or-adopt decision (bd-578h9.5), and must not be bricked by
	// dirty tables either. Used by OpenForReadOnlyCommand.
	openReadOnlyCommand
	// openWorkingSetReconcile relaxes both refusals for working-set-reconcile
	// commands (bd dolt commit, bd vc commit): their entire purpose is to
	// clear the dirty working set that a migration would otherwise refuse to
	// touch, so failing the open here would deadlock the documented recovery
	// (#4566). Used by OpenForWorkingSetReconcile.
	openWorkingSetReconcile
)

// errClosed is returned when a method is called after Close.
var errClosed = errors.New("embeddeddolt: store is closed")

// errReadOnly is returned when a write is attempted on a read-only store.
var errReadOnly = errors.New("embeddeddolt: store is read-only")

// IsClosed reports whether the store has been closed. Implements
// storage.LifecycleManager so that callers (e.g., maybeAutoCommit) can
// skip operations on a closed store without triggering errClosed.
func (s *EmbeddedDoltStore) IsClosed() bool {
	return s.closed.Load()
}

// newStore creates an EmbeddedDoltStore using the embedded Dolt engine.
// beadsDir is the .beads/ root; the data directory is derived as <beadsDir>/embeddeddolt/.
// The database is created automatically if it doesn't exist (initSchema handles this).
//
// The dolthub/driver/v2 handles its own concurrency internally. File-level locking
// is only used during bd init (via util.TryLock in the init command) to protect
// one-time initialization steps — the store itself does not hold any lock.
func newStore(ctx context.Context, beadsDir, database, branch string, intent openIntent) (*EmbeddedDoltStore, error) {
	if database == "" {
		return nil, fmt.Errorf("embeddeddolt: database name must not be empty (caller should default to %q)", "beads")
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

	s := &EmbeddedDoltStore{
		dataDir:  dataDir,
		beadsDir: absBeadsDir,
		database: database,
		branch:   branch,
		intent:   intent,
	}

	if err := s.initSchema(ctx); err != nil {
		return nil, fmt.Errorf("embeddeddolt: init schema: %w", err)
	}

	return s, nil
}

// OpenReadOnly opens an existing embedded database for read-only access,
// skipping every mutating open-time step: no data-directory creation, no
// CREATE DATABASE, no remote-migrate gate, and no schema migrations
// (bd-6dnrw.32). It is the embedded equivalent of server mode's
// Config.ReadOnly open, used for cross-repo hydration of foreign projects
// (GH#3231) where opening must not write anything — not even a one-time
// migration backfill commit — into the target's history. Drift in either
// direction is checked at open: forward (the database AHEAD of this binary)
// because stale-binary reads fail cryptically, and behind (the database
// BEHIND this binary) because these paths used to auto-migrate and would
// otherwise fail at query time with unknown-column errors (bd-578h9.12).
//
// Read-only stores bypass the Open cache in both directions: they must not be
// handed a future writable Open (which would skip migrations), and writable
// opens of the same directory keep their own lifecycle. Write transactions on
// the returned store are refused.
func OpenReadOnly(ctx context.Context, beadsDir, database, branch string) (*EmbeddedDoltStore, error) {
	if database == "" {
		return nil, fmt.Errorf("embeddeddolt: database name must not be empty (caller should default to %q)", "beads")
	}
	if !validIdentifier.MatchString(database) {
		return nil, fmt.Errorf("embeddeddolt: invalid database name: %q", database)
	}
	absBeadsDir, err := filepath.Abs(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: resolving beads dir: %w", err)
	}
	dataDir := filepath.Join(absBeadsDir, "embeddeddolt")
	if _, err := os.Stat(dataDir); err != nil {
		return nil, fmt.Errorf("embeddeddolt: no embedded database at %s: %w", dataDir, err)
	}

	s := &EmbeddedDoltStore{
		dataDir:  dataDir,
		beadsDir: absBeadsDir,
		database: database,
		branch:   branch,
		readOnly: true,
	}

	db, cleanup, err := OpenSQL(ctx, dataDir, database, branch)
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: open db: %w", err)
	}
	defer func() { _ = cleanup() }()
	if err := schema.CheckForwardDrift(ctx, db); err != nil {
		return nil, err
	}
	if err := schema.CheckBehindDrift(ctx, db); err != nil {
		return nil, err
	}

	return s, nil
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
	if commit && s.readOnly {
		err = errReadOnly
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

	if fnErr := fn(tx); fnErr != nil {
		err = errors.Join(fnErr, tx.Rollback())
		return
	}

	if !commit {
		err = tx.Rollback()
		return
	}

	if cErr := tx.Commit(); cErr != nil {
		err = fmt.Errorf("embeddeddolt: commit tx: %w", cErr)
		return
	}
	return
}

func (s *EmbeddedDoltStore) ApplySchemaMigrations(ctx context.Context) (int, error) {
	if s.closed.Load() {
		return 0, errClosed
	}
	if s.readOnly {
		return 0, errReadOnly
	}
	db, cleanup, err := OpenSQL(ctx, s.dataDir, s.database, s.branch)
	if err != nil {
		return 0, fmt.Errorf("embeddeddolt: open db: %w", err)
	}
	defer func() { _ = cleanup() }()

	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("embeddeddolt: pin connection: %w", err)
	}
	defer conn.Close()

	return schema.MigrateUp(ctx, conn)
}

func (s *EmbeddedDoltStore) initSchema(ctx context.Context) error {
	db, cleanup, err := OpenSQL(ctx, s.dataDir, "", "")
	if err != nil {
		return fmt.Errorf("embeddeddolt: open db: %w", err)
	}
	defer func() { _ = cleanup() }()

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("embeddeddolt: pin connection: %w", err)
	}
	defer conn.Close()

	if s.database != "" {
		if !validIdentifier.MatchString(s.database) {
			msg := fmt.Sprintf("embeddeddolt: invalid database name: %q", s.database)
			if strings.ContainsRune(s.database, '-') {
				msg += "; hyphens are not allowed in embedded mode — replace with underscores in .beads/metadata.json dolt_database field, or run 'bd doctor'"
			}
			return errors.New(msg)
		}
		if _, err := conn.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+s.database+"`"); err != nil {
			return fmt.Errorf("embeddeddolt: creating database: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "USE `"+s.database+"`"); err != nil {
			return fmt.Errorf("embeddeddolt: switching to database: %w", err)
		}
		if s.branch != "" {
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET @@%s_head_ref = %s", s.database, sqlStringLiteral(s.branch))); err != nil {
				return fmt.Errorf("embeddeddolt: setting branch: %w", err)
			}
		}
	}

	// Forward-drift guard: if this database's schema is AHEAD of the binary,
	// fail fast with a clear "upgrade bd" message before MigrateUp no-ops and a
	// later query dies on a dropped/renamed column. Embedded mode is the mode
	// the stale-binary incident (#4135/#4137) was observed in. The read-only
	// embedded open (OpenReadOnly) already guards this; the writable open did
	// not. Runs after the USE switch so the version read resolves against the
	// target database.
	if err := schema.CheckForwardDrift(ctx, conn); err != nil {
		return err
	}

	// #4259: refuse to silently apply pending migrations to a remote-backed,
	// already-initialized database — independently migrating each clone forks the
	// schema. Embedded mode (the mode the original report was filed against) syncs
	// via Dolt remotes too, so it needs the same gate as server mode.
	//
	// adopt injects the driver-side fast-forward ancestry primitives
	// (mybd-ae1i) so the smart gate can distinguish a losslessly
	// fast-forwardable remote-ahead case (smartAdoptFastForward) from the
	// plain destructive adopt, and auto-execute it: CheckRemoteMigrateGate*
	// calls FastForward and returns nil (proceed, nothing pending) once HEAD
	// has actually advanced; any execution failure (dirty working set raced
	// in, non-fast-forward, concurrent writer) falls back to the plain
	// destructive adopt directive instead of forcing the write.
	adopt := &schema.FastForwardAdopter{
		IsStrictAncestor: func(ctx context.Context, db schema.DBConn, ref string) (bool, error) {
			return versioncontrolops.LocalIsStrictAncestorOf(ctx, db, ref)
		},
		WorkingSetClean: func(ctx context.Context, db schema.DBConn) (bool, error) {
			return versioncontrolops.WorkingSetClean(ctx, db)
		},
		FastForward: func(ctx context.Context, db schema.DBConn, ref string) error {
			return versioncontrolops.FastForwardAdopt(ctx, db, ref)
		},
		// ReadOnly is deliberately left unset (false) here: this initSchema
		// path is only ever reached via newStore (openStrict,
		// openReadOnlyCommand, or openWorkingSetReconcile intents), all of
		// which perform a writable open — s.readOnly is never true for any
		// of them. The genuinely read-only embedded open, OpenReadOnly,
		// skips initSchema (and this gate) entirely, so there is no
		// read-only signal to plumb through at this injection site the way
		// server mode's cfg.ReadOnly is (dolt/store.go initSchema). If that
		// ever changes — e.g. initSchema starts running on a store that can
		// report readOnly true — wire it here too.
	}
	if err := schema.CheckRemoteMigrateGateWithAdopt(ctx, conn, adopt); err != nil {
		var gateErr *schema.RemoteMigrateGateError
		if s.intent != openStrict && errors.As(err, &gateErr) {
			// The gate exists to stop in-place migration on a remote-backed,
			// already-initialized database (#4259), not to block reads or a
			// working-set commit. Warn and continue on the current schema;
			// a plain (openStrict) open still fails with the full
			// migrate-or-adopt guidance.
			const sharedGuidance = "  This is a\n" +
				"  coordination decision, not an auto-fix - do NOT run a migration unless\n" +
				"  you are the single designated migrator (only ONE clone may migrate a\n" +
				"  shared remote, else the schema forks; #4259):\n" +
				"    • designated migrator (only ONE machine): bd migrate --force && bd dolt push\n" +
				"    • every other clone (another already migrated): bd bootstrap\n" +
				"    • several machines: only ONE migrates; sync each other clone and run\n" +
				"      bd dolt pull after the migrator pushes, before upgrading it\n"
			switch s.intent {
			case openWorkingSetReconcile:
				fmt.Fprintf(os.Stderr,
					"Warning: %[1]v\n"+
						"  Working-set reconcile command: continuing on schema v%[2]d without\n"+
						"  migrating; the commit applies to the working set at the current\n"+
						"  schema."+sharedGuidance,
					gateErr, gateErr.CurrentVersion)
			default: // openReadOnlyCommand
				fmt.Fprintf(os.Stderr,
					"Warning: %[1]v\n"+
						"  Read-only command: continuing on schema v%[2]d without migrating.\n"+
						"  Writes are blocked until the schema is reconciled."+sharedGuidance,
					gateErr, gateErr.CurrentVersion)
			}
			return nil
		}
		return err
	}

	// Embedded mode relies on the dolthub/driver/v2's local file/concurrency
	// controls; schema.MigrateUpWithLock requires a sql-server session lock.
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		var dirtyErr *schema.DirtyTablesError
		if s.intent != openStrict && errors.As(err, &dirtyErr) {
			// The guard exists to keep dirty user data from being entangled
			// with a migration, but its documented recovery - committing the
			// working set - also opens the store and would otherwise hit
			// this same refusal before it ever runs, deadlocking (#4566). A
			// read-only command must not be bricked by dirty tables either,
			// so both non-strict intents warn and continue on the current
			// schema instead of failing the open.
			switch s.intent {
			case openWorkingSetReconcile:
				fmt.Fprintf(os.Stderr,
					"Warning: %v\n"+
						"  Committing the working set at the current schema; when it completes,\n"+
						"  re-run 'bd migrate'.\n",
					dirtyErr)
			default: // openReadOnlyCommand
				fmt.Fprintf(os.Stderr,
					"Warning: %v\n"+
						"  Continuing without migrating. Run 'bd dolt commit' to commit the\n"+
						"  working set at the current schema, then re-run 'bd migrate'.\n",
					dirtyErr)
			}
			return nil
		}
		return fmt.Errorf("embeddeddolt: migrate: %w", err)
	}

	return nil
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

// Close decrements the reference count if this store was opened via Open (the
// process-scoped cache). When other references remain, Close is a no-op — the
// store stays alive for the remaining callers. When the last reference calls
// Close (or if the store was created directly via newStore), the underlying
// resources are released.
//
// It is safe to call multiple times.
func (s *EmbeddedDoltStore) Close() error {
	if closeCached(s) {
		return nil
	}
	if s.closed.CompareAndSwap(false, true) {
		s.cleanGitRemoteCacheGarbage()
	}
	return nil
}

// DoltGC runs Dolt garbage collection to reclaim disk space.
func (s *EmbeddedDoltStore) DoltGC(ctx context.Context) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return versioncontrolops.DoltGC(ctx, db)
	})
}

// ImportJSONLData atomically checks if the database is empty and, if so,
// imports parsed issues and config key/value pairs in a single transaction.
// Returns the count of issues imported, or 0 if the database was not empty.
// Does NOT issue DOLT_COMMIT — the caller is responsible for committing
// (e.g. via the PersistentPostRun auto-commit hook).
func (s *EmbeddedDoltStore) ImportJSONLData(
	ctx context.Context,
	issues []*types.Issue,
	configEntries map[string]string,
	actor string,
) (int, error) {
	var imported int
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		// Atomically check: is the database empty?
		stats := &types.Statistics{}
		if err := issueops.ScanIssueCountsInTx(ctx, tx, stats); err != nil {
			return fmt.Errorf("checking issue count: %w", err)
		}
		if stats.TotalIssues > 0 {
			return nil // database is not empty — skip import
		}

		// Import config entries (memories, etc.)
		for key, value := range configEntries {
			if err := issueops.SetConfigInTx(ctx, tx, key, value); err != nil {
				return fmt.Errorf("importing config %q: %w", key, err)
			}
		}

		if len(issues) == 0 {
			return nil
		}

		// Auto-detect prefix from first issue if not already provided
		if _, hasPrefix := configEntries["issue_prefix"]; !hasPrefix {
			firstPrefix := utils.ExtractIssuePrefix(issues[0].ID)
			if firstPrefix != "" {
				if err := issueops.SetConfigInTx(ctx, tx, "issue_prefix", firstPrefix); err != nil {
					return fmt.Errorf("setting issue_prefix: %w", err)
				}
			}
		}

		// Create all issues in the same transaction
		if err := issueops.CreateIssuesInTx(ctx, tx, issues, actor, storage.BatchCreateOptions{
			OrphanHandling:       storage.OrphanAllow,
			SkipPrefixValidation: true,
			// Defense-in-depth (GH#3955): the embedded fast-path is the primary
			// auto-import route for 1.0+ users and is gated by the in-transaction
			// emptiness check above. Make it insert-if-new too so a regression in
			// that check cannot clobber live rows — matching the server-mode
			// fallback's conflict-skip behavior.
			ConflictSkip: true,
		}); err != nil {
			return err
		}

		imported = len(issues)
		return nil
	})
	return imported, err
}

// Flatten squashes all Dolt commit history into a single commit.
// Pins a single *sql.Conn for session-scoped stored procedures.
func (s *EmbeddedDoltStore) Flatten(ctx context.Context) error {
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
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
	return s.withMutatingDBConn(ctx, func(db versioncontrolops.DBConn) error {
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
	msg := fmt.Sprintf("bd: commit pending changes by %s", actor)
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
		return nil, err
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

func (s *EmbeddedDoltStore) SnapshotIssue(ctx context.Context, issueID string, tier int) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.SnapshotIssueInTx(ctx, tx, issueID, tier)
	})
}

func (s *EmbeddedDoltStore) GetCompactionSnapshot(ctx context.Context, issueID string) (*types.IssueSnapshot, error) {
	var snap *types.IssueSnapshot
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		snap, err = issueops.GetLatestSnapshotInTx(ctx, tx, issueID)
		return err
	})
	return snap, err
}

func (s *EmbeddedDoltStore) RestoreFromSnapshot(ctx context.Context, issueID string) (*types.IssueSnapshot, error) {
	var snap *types.IssueSnapshot
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		snap, err = issueops.RestoreFromSnapshotInTx(ctx, tx, issueID)
		return err
	})
	return snap, err
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
