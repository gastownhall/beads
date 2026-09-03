package schema

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/debug"
)

const (
	migrationLockPrefix        = "bd_schema_init:"
	migrationLockNameMaxLength = 64
	// Keep this below the server-mode callers' retry budgets so a contended
	// lock wait can time out and still leave room for a real retry.
	migrationLockAcquireTimeoutSeconds = 5
	migrationLockCleanupTimeout        = 5 * time.Second
)

var (
	// ErrMigrationLockUnavailable marks transient failures to acquire the
	// per-database migration lock.
	ErrMigrationLockUnavailable = errors.New("schema migration lock unavailable")

	// ErrMigrationLockRelease marks uncertain lock cleanup after migrations.
	ErrMigrationLockRelease = errors.New("schema migration lock release failed")
)

// IsMigrationLockError reports whether err came from migration lock acquisition
// or cleanup and should be treated as retryable by callers with a retry budget.
func IsMigrationLockError(err error) bool {
	return errors.Is(err, ErrMigrationLockUnavailable) || errors.Is(err, ErrMigrationLockRelease)
}

// MigrationLockName returns a Dolt/MySQL named-lock key within the documented
// 64-byte limit while preserving readable names when they already fit.
func MigrationLockName(databaseName string) string {
	raw := migrationLockPrefix + databaseName
	if len(raw) <= migrationLockNameMaxLength {
		return raw
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(databaseName))
	return fmt.Sprintf("%s%016x", migrationLockPrefix, h.Sum64())
}

// MigrateLockOption configures MigrateUpWithLock.
type MigrateLockOption func(*migrateLockOptions)

type migrateLockOptions struct {
	freshBootstrapHeal *freshBootstrapHealRequest
	lockedPreparation  *lockedPreparationRequest
	databaseSelector   DatabaseSelector
}

type freshBootstrapHealRequest struct {
	capability *FreshBootstrapHealCapability
	endpoint   string
}

type lockedPreparationRequest struct {
	endpoint string
	fn       LockedPreparation
}

// LockedPreparation performs bootstrap work under MigrateUpWithLock's pinned
// connection and database-scoped migration lock. It may return a fresh
// bootstrap heal capability to enable the existing guarded recovery path.
type LockedPreparation func(context.Context, *sql.Conn) (*FreshBootstrapHealCapability, error)

// FreshBootstrapHealCapability is a one-shot authorization to discard an
// interrupted bootstrap working set. It is bound to the exact endpoint,
// server, database, and initial Dolt commit observed immediately after a
// successful bare CREATE DATABASE. Its fields are intentionally private so a
// caller cannot manufacture authority from a boolean.
//
// A capability may be shared by the server-mode migration retry loop. An
// atomic consumed bit ensures that the whole logical open can attempt at most
// one destructive reset, even when a later transient error causes an outer
// retry.
type FreshBootstrapHealCapability struct {
	endpoint     string
	serverUUID   string
	databaseName string
	initialHead  string
	consumed     atomic.Bool
}

// CaptureFreshBootstrapHealCapability records the identity of a database that
// the caller has just created with an exact, successful bare CREATE DATABASE.
// The database must still be pristine: one initial commit and an empty working
// set. Any uncertainty fails closed instead of issuing reset authority.
func CaptureFreshBootstrapHealCapability(
	ctx context.Context,
	conn *sql.Conn,
	endpoint string,
	expectedDatabase string,
) (*FreshBootstrapHealCapability, error) {
	if endpoint == "" {
		return nil, errors.New("schema: capture fresh-bootstrap identity: empty endpoint")
	}
	if expectedDatabase == "" {
		return nil, errors.New("schema: capture fresh-bootstrap identity: empty database name")
	}

	var databaseName, serverUUID, initialHead string
	if err := conn.QueryRowContext(ctx,
		"SELECT DATABASE(), @@server_uuid, DOLT_HASHOF('HEAD')",
	).Scan(&databaseName, &serverUUID, &initialHead); err != nil {
		return nil, fmt.Errorf("schema: capture fresh-bootstrap identity: %w", err)
	}
	if databaseName != expectedDatabase {
		return nil, fmt.Errorf("schema: capture fresh-bootstrap identity: database is %q, want %q", databaseName, expectedDatabase)
	}
	if serverUUID == "" {
		return nil, errors.New("schema: capture fresh-bootstrap identity: empty server UUID")
	}
	if initialHead == "" {
		return nil, errors.New("schema: capture fresh-bootstrap identity: empty initial HEAD")
	}

	var commitCount, dirtyCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commitCount); err != nil {
		return nil, fmt.Errorf("schema: verify fresh-bootstrap history: %w", err)
	}
	if commitCount != 1 {
		return nil, fmt.Errorf("schema: verify fresh-bootstrap history: got %d commits, want 1", commitCount)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirtyCount); err != nil {
		return nil, fmt.Errorf("schema: verify fresh-bootstrap working set: %w", err)
	}
	if dirtyCount != 0 {
		return nil, fmt.Errorf("schema: verify fresh-bootstrap working set: got %d dirty entries, want 0", dirtyCount)
	}

	return &FreshBootstrapHealCapability{
		endpoint:     endpoint,
		serverUUID:   serverUUID,
		databaseName: databaseName,
		initialHead:  initialHead,
	}, nil
}

// WithFreshBootstrapHeal enables the fresh-bootstrap self-heal for the #4566
// dirty-table guard (gastownhall/beads#5012). The capability must have been
// captured immediately after this logical open's exact CREATE DATABASE, and
// endpoint must identify the connection pool used for migration. Before any
// DOLT_RESET, MigrateUpWithLock revalidates the capability against the pinned,
// locked session and consumes it atomically.
func WithFreshBootstrapHeal(capability *FreshBootstrapHealCapability, endpoint string) MigrateLockOption {
	return func(o *migrateLockOptions) {
		o.freshBootstrapHeal = &freshBootstrapHealRequest{
			capability: capability,
			endpoint:   endpoint,
		}
	}
}

// WithLockedPreparation configures bootstrap work that must execute after the
// migration lock is acquired and before migrations run. A non-nil capability
// returned by fn enables the existing fresh-bootstrap recovery path.
func WithLockedPreparation(endpoint string, fn LockedPreparation) MigrateLockOption {
	return func(o *migrateLockOptions) {
		o.lockedPreparation = &lockedPreparationRequest{endpoint: endpoint, fn: fn}
	}
}

// DatabaseSelector puts a pinned session on databaseName for the pre-lock
// convergence probe and returns the identifier-quoted name the probe uses to
// schema-qualify its reads. Returning an error, like everything else in the
// probe, fails closed onto the locked path.
//
// It is injected rather than implemented here on purpose. Selecting a database
// and quoting an identifier belong to the DDL repository in
// internal/storage/domain/db — but that package's own test suite migrates a
// scratch database with this package, so a schema -> domain/db import compiles
// as a library and then fails the domain/db TEST build with "import cycle not
// allowed in test". Inverting the dependency lets the one caller that needs
// the probe to reach an unselected session hand in the repository's real
// implementation, so the fast path still issues preparation's exact statement
// without this package depending on it.
type DatabaseSelector func(ctx context.Context, conn DBConn, databaseName string) (quotedName string, err error)

// WithDatabaseSelector lets the convergence probe put the pinned session on
// the target database itself.
//
// Without it the probe cannot issue USE, so it declines unless the session is
// already on databaseName — correct for callers whose pool DSN already names
// the database (internal/storage/dolt). The proxied CLI open is the caller
// that needs it: it pins its schema-init pool with an EMPTY DSN database and
// USEs only inside the locked preparation, so an uninjected probe would read
// NULL from DATABASE() and decline on every single invocation, which is
// exactly the defect this option exists to close.
func WithDatabaseSelector(fn DatabaseSelector) MigrateLockOption {
	return func(o *migrateLockOptions) {
		o.databaseSelector = fn
	}
}

// MigrateUpWithLock serializes schema migrations for a single Dolt sql-server
// database. conn must be a pinned *sql.Conn because MySQL/Dolt named locks are
// session-scoped; GET_LOCK, migrations, and RELEASE_LOCK must run on the same
// server session. Embedded Dolt intentionally uses bare MigrateUp because it
// relies on the embedded driver's file/concurrency controls instead of
// sql-server session locks.
func MigrateUpWithLock(ctx context.Context, conn *sql.Conn, databaseName string, opts ...MigrateLockOption) (applied int, err error) {
	var o migrateLockOptions
	for _, opt := range opts {
		opt(&o)
	}

	// Steady-state fast path. In the overwhelmingly common case the database
	// is already at this binary's schema and the whole locked pass is a probe
	// that finds nothing to do — but running that probe under the
	// database-scoped GET_LOCK serialized every bd invocation on a shared Dolt
	// server behind every other one (96.7% lock saturation on an 18-seat rig;
	// see alreadyConverged). Answer the same question first, without the lock,
	// and take the lock only on the path that can actually migrate.
	//
	// Skipped when the caller carries fresh-bootstrap heal authority: that
	// capability is issued only to the init that just created the database, so
	// there is nothing converged to detect and the bootstrap path stays
	// exactly as it was. A caller's locked preparation is skipped only once
	// alreadyConverged has proved the database already existed and put the
	// session on it — preparation's CREATE DATABASE would then have failed
	// with "database exists" and captured no authority, and its USE is the
	// statement alreadyConverged itself issued.
	if o.freshBootstrapHeal == nil {
		converged, convergedErr := alreadyConverged(ctx, conn, databaseName, o.databaseSelector)
		switch {
		case convergedErr != nil:
			// Advisory only: the probe fails closed onto the locked path
			// below, so this is never fatal. But a silently discarded error is
			// a fast path that has quietly stopped firing — the exact failure
			// mode that leaves the GET_LOCK saturation this exists to remove
			// looking like a mystery. Say so where BD_DEBUG/-v can see it.
			debug.Logf("schema: convergence fast path unavailable for %q, taking the migration lock: %v\n",
				databaseName, convergedErr)
		case converged:
			return 0, nil
		}
	}

	lockName := MigrationLockName(databaseName)
	if err := AcquireMigrationLock(ctx, conn, lockName); err != nil {
		return 0, err
	}
	defer func() {
		if releaseErr := ReleaseMigrationLock(conn, lockName); releaseErr != nil {
			if err != nil {
				// Two %w verbs keep errors.Is/As working for both errors
				// without errors.Join's separator newline, primary first.
				err = fmt.Errorf("%w (lock release also failed: %w)", err, releaseErr)
			} else {
				err = errors.Join(err, releaseErr)
			}
		}
	}()
	if o.lockedPreparation != nil && o.lockedPreparation.fn != nil {
		capability, preparationErr := o.lockedPreparation.fn(ctx, conn)
		if preparationErr != nil {
			return 0, preparationErr
		}
		if capability != nil {
			o.freshBootstrapHeal = &freshBootstrapHealRequest{
				capability: capability,
				endpoint:   o.lockedPreparation.endpoint,
			}
		}
	}

	applied, err = MigrateUp(ctx, conn)
	var dirtyErr *DirtyTablesError
	if err != nil && o.freshBootstrapHeal != nil && errors.As(err, &dirtyErr) {
		// Authorization is checked after the dirty guard fires and while the
		// database-scoped migration lock is still held. A mismatch, missing
		// ancestor, probe error, or previously consumed capability returns the
		// original DirtyTablesError without attempting a destructive reset.
		if !o.freshBootstrapHeal.capability.consumeIfCurrentIncarnation(
			ctx, conn, databaseName, o.freshBootstrapHeal.endpoint,
		) {
			return applied, err
		}

		// Consume occurs before the reset call. Thus a reset error or a
		// transient failure in the following migration pass cannot re-arm a
		// second reset in the caller's outer retry loop.
		fmt.Fprintf(stderr, "Discarding interrupted-bootstrap working set (%s) and re-running migrations…\n",
			strings.Join(dirtyErr.Tables, ", "))
		// Drained, not Exec'd: the very next thing this path does is re-run the
		// whole MigrateUp pass on this same pinned connection, so an
		// undrained proc result set here would poison every statement of it.
		if resetErr := DrainCall(ctx, conn, "CALL DOLT_RESET('--hard')"); resetErr != nil {
			return applied, errors.Join(err, fmt.Errorf("schema: fresh-bootstrap reset: %w", resetErr))
		}
		applied, err = MigrateUp(ctx, conn)
	}
	return applied, err
}

// consumeIfCurrentIncarnation validates and atomically consumes c. All probes
// run on the same pinned session while MigrateUpWithLock holds the migration
// lock. Query errors deliberately collapse to false so callers preserve and
// return the original DirtyTablesError.
func (c *FreshBootstrapHealCapability) consumeIfCurrentIncarnation(
	ctx context.Context,
	conn *sql.Conn,
	databaseName string,
	endpoint string,
) bool {
	if c == nil || c.consumed.Load() {
		return false
	}
	if endpoint == "" || endpoint != c.endpoint || databaseName != c.databaseName {
		return false
	}

	var currentDatabase, currentServerUUID string
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE(), @@server_uuid").
		Scan(&currentDatabase, &currentServerUUID); err != nil {
		return false
	}
	if currentDatabase != databaseName || currentDatabase != c.databaseName || currentServerUUID != c.serverUUID {
		return false
	}

	var ancestorCount int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", c.initialHead,
	).Scan(&ancestorCount); err != nil {
		return false
	}
	if ancestorCount != 1 {
		return false
	}

	return c.consumed.CompareAndSwap(false, true)
}

// AcquireMigrationLock acquires the named schema migration lock on the pinned
// connection's current Dolt/MySQL session.
func AcquireMigrationLock(ctx context.Context, conn *sql.Conn, lockName string) error {
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, migrationLockAcquireTimeoutSeconds).Scan(&locked); err != nil {
		return fmt.Errorf("schema: acquire migration lock: %w: %w", ErrMigrationLockUnavailable, err)
	}
	if !locked.Valid {
		return fmt.Errorf("schema: acquire migration lock: %w: returned NULL", ErrMigrationLockUnavailable)
	}
	if locked.Int64 != 1 {
		return fmt.Errorf("schema: acquire migration lock: %w: timeout", ErrMigrationLockUnavailable)
	}
	return nil
}

// ReleaseMigrationLock releases the named schema migration lock from the same
// pinned Dolt/MySQL session used to acquire it.
func ReleaseMigrationLock(conn *sql.Conn, lockName string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationLockCleanupTimeout)
	defer cancel()

	var released sql.NullInt64
	if err := conn.QueryRowContext(cleanupCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil {
		discardConn(conn)
		return fmt.Errorf("schema: release migration lock: %w: %w", ErrMigrationLockRelease, err)
	}
	if !released.Valid {
		discardConn(conn)
		return fmt.Errorf("schema: release migration lock: %w: returned NULL", ErrMigrationLockRelease)
	}
	if released.Int64 != 1 {
		discardConn(conn)
		return fmt.Errorf("schema: release migration lock: %w: returned %d", ErrMigrationLockRelease, released.Int64)
	}
	return nil
}

func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(driverConn any) error {
		return driver.ErrBadConn
	})
}
