package uow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"time"
)

const (
	// deleteFenceLockPrefix namespaces this fence within Dolt/MySQL's
	// GET_LOCK name space, which is server-wide rather than per-database
	// (mirrors internal/storage/dolt's deleteFenceLockPrefix and
	// schema.MigrationLockName's bd_schema_init: prefix, same reason): two
	// beads databases sharing one Dolt sql-server must not collide on the
	// same issue id.
	deleteFenceLockPrefix = "bd_delete:"
	// deleteFenceLockNameMaxLength mirrors MySQL/Dolt's 64-byte GET_LOCK name limit.
	deleteFenceLockNameMaxLength = 64
	// deleteFenceLockTimeoutSeconds bounds how long a guarded delete waits to
	// enter the fence. It must comfortably exceed RunTxResult's own
	// DefaultTxRetryMaxElapsed (15s): a racer waiting its turn can be queued
	// behind another racer that is itself retrying a serialization failure
	// for up to that long.
	deleteFenceLockTimeoutSeconds = 20
	// deleteFenceReleaseTimeout bounds the RELEASE_LOCK call, run on its own
	// context so a caller-canceled ctx cannot strand the lock held.
	deleteFenceReleaseTimeout = 5 * time.Second
)

// ErrDeleteFenceUnavailable marks a transient failure to acquire the per-row
// delete fence lock (timeout or connection error), distinct from a version
// mismatch: it means this guarded delete never got a chance to check the
// row's version at all.
var ErrDeleteFenceUnavailable = errors.New("delete fence lock unavailable")

// errDeleteFenceUnreachable refuses a guarded delete whose provider chain hides
// the fence, before anything is read or written: a decorator that neither
// unwraps nor answers FenceProvider. It is a wiring defect, not a transient
// one, so it is not ErrDeleteFenceUnavailable. See deleter.Delete.
var errDeleteFenceUnreachable = errors.New("delete fence: no provider on the chain holds the fence; refusing a guarded delete that would run unfenced")

// deleteFenceLockName returns a Dolt/MySQL named-lock key within the 64-byte
// limit, hashing down to fnv64a when the readable form would not fit —
// mirrors internal/storage/dolt's deleteFenceLockName (and, one level further
// back, schema.MigrationLockName).
func deleteFenceLockName(databaseName, id string) string {
	raw := deleteFenceLockPrefix + databaseName + ":" + id
	if len(raw) <= deleteFenceLockNameMaxLength {
		return raw
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(databaseName + ":" + id))
	return fmt.Sprintf("%s%016x", deleteFenceLockPrefix, h.Sum64())
}

// withDeleteFence serializes guarded (ExpectedVersion-checked) deletes for a
// single id against each other on a Dolt sql-server, the proxied-mode /
// bd-serve twin of internal/storage/dolt's (*DoltStore).withDeleteFence
// (mc-zndi7.73).
//
// Dolt's commit-time merge treats two concurrent transactions that each
// delete the same row as identical diffs ("row absent") and lands BOTH with
// no conflict — unlike a concurrent update/close racing the same delete,
// which already collides for real because its row_lock bump is a genuine
// edit the merge must reconcile against the delete. That asymmetry is the
// bug: every same-token deleter exits 0, and there is no schema cell a
// guarded delete can write to manufacture a real conflict, because the row
// it would write to is the row it is erasing.
//
// A session-scoped GET_LOCK, acquired before fn runs and released only after
// fn returns, turns that silent double-delete into a queue instead: the
// loser's unit of work begins only after the winner's has already committed
// and is durable, so its existence probe inside deleteInUOW finds the row
// gone and fails through the ordinary NotFoundError path instead of silently
// "succeeding" a second time. No schema or migration change is needed —
// GET_LOCK/RELEASE_LOCK are built-in Dolt/MySQL session primitives, already
// an established repo idiom (schema.MigrateUpWithLock,
// internal/storage/dolt's own delete fence).
//
// UNLIKE internal/storage/dolt's fence, the lock connection here is NOT the
// connection fn's write runs on: fn is left free to call RunTxResult, which
// draws its own (possibly several, across retries) connection from p.db the
// ordinary way. That is safe for exactly the reason the dolt-package fence's
// doc calls out as the hazard to avoid there: THIS provider's pool is never
// constrained to a single connection, so a dedicated lock connection can
// never starve the write of its own connection. The proxied CLI chain sets
// no pool limits at all. bd serve does (httpapi's servePoolLimits), but keeps
// MaxOpenConns above its in-flight bound, and a request that touches the
// database holds an in-flight slot. A fenced request holds at most one lock
// connection, so lock connections alone can never take the whole pool, and
// some lock holder can always get a connection for its write; holders past
// that headroom wait on the pool, never on each other (TestStartupLines pins
// the headroom). What matters for correctness is not which physical
// connection runs the write but WHEN its transaction begins: fn (and
// therefore every snapshot it establishes, across every retry) starts only
// after GET_LOCK has already returned success, so a queued loser's first read
// observes the winner's commit as already landed and durable.
//
// One case escapes that, and it is the cost of the separate lock session. A
// winner whose context is canceled mid-commit (a client gone, a request
// deadline) returns from fn before the server finishes that commit, and the
// lock is released at once on its own healthy session. A queued racer can then
// begin before the abandoned commit lands, delete the row again, and land a
// second commit. Its caller is still the only one told the delete succeeded
// (the abandoned winner's caller got an error, or no answer at all); the
// history carries one delete too many.
func withDeleteFence(ctx context.Context, p *doltSQLProvider, id string, fn func() error) error {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("delete fence: acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	dbName, err := currentDatabaseName(ctx, conn)
	if err != nil {
		return fmt.Errorf("delete fence: %w", err)
	}

	lockName := deleteFenceLockName(dbName, id)
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, deleteFenceLockTimeoutSeconds).Scan(&locked); err != nil {
		return fmt.Errorf("delete fence: acquire lock: %w: %w", ErrDeleteFenceUnavailable, err)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return fmt.Errorf("delete fence: acquire lock: %w: timed out after %ds", ErrDeleteFenceUnavailable, deleteFenceLockTimeoutSeconds)
	}
	defer releaseDeleteFenceLock(conn, lockName)

	return fn()
}

// FenceProvider is what a per-request wrapper around a provider answers so that a
// role built over the wrapper can still reach the provider's fence.
//
// It is a separate question from ProviderUnwrapper, on purpose. Unwrap hands a
// caller the provider beneath a decorator to work through (bd serve peels the
// hook layer with it), and the point of a per-request wrapper is that nothing
// works beneath it: bd serve's timing wrapper builds every role over itself so
// that each unit of work lands in the request's uow_ms, with the
// external-dependency policy beneath it. FenceProvider answers one narrower
// question, where to keep looking for the provider that holds the GET_LOCK
// fence, and the answer is used for the lock alone: the guarded write still
// opens its units of work through the wrapper the role was built over.
//
// Until bd serve's wrapper answered it, the wrapper was neither, so a guarded
// delete arriving on v0 never found the fence: every same-token racer answered
// 200 (GH#7243).
type FenceProvider interface {
	FenceProvider() UnitOfWorkProvider
}

// fenceTarget walks a provider chain down to the provider that holds the fence.
func fenceTarget(p UnitOfWorkProvider) (*doltSQLProvider, bool) {
	for {
		switch next := p.(type) {
		case *doltSQLProvider:
			return next, true
		case FenceProvider:
			p = next.FenceProvider()
		case ProviderUnwrapper:
			p = next.Unwrap()
		default:
			return nil, false
		}
	}
}

// currentDatabaseName reads the database conn's session is attached to.
// doltSQLProvider carries no database field of its own (the name lives only
// in the DSN each pool connection was opened with), and every connection
// drawn from p.db is already attached to it — buildDSN always includes the
// database component — so SELECT DATABASE() reliably reports the same name
// internal/storage/dolt's fence reads from its store's own field.
func currentDatabaseName(ctx context.Context, conn *sql.Conn) (string, error) {
	var name sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
		return "", fmt.Errorf("select database(): %w", err)
	}
	return name.String, nil
}

// releaseDeleteFenceLock releases a lock acquired by withDeleteFence from the
// same session, after fn has returned. It runs on its own short-lived context
// so a caller-canceled ctx cannot strand the lock held past this call.
//
// An unexpected result discards the physical connection (same defensive
// pattern internal/storage/dolt's releaseDeleteFenceLock and schema's
// discardConn use) rather than returning it to the pool: the lock's true
// state on that session is now unknown, and handing it to another caller
// could let a future guarded delete believe it holds the fence when it does
// not.
func releaseDeleteFenceLock(conn *sql.Conn, lockName string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), deleteFenceReleaseTimeout)
	defer cancel()

	var released sql.NullInt64
	err := conn.QueryRowContext(cleanupCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released)
	if err != nil || !released.Valid || released.Int64 != 1 {
		_ = conn.Raw(func(driverConn any) error {
			return driver.ErrBadConn
		})
	}
}
