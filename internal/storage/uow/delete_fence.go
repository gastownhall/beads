package uow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"sync/atomic"
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
// What matters for the fence's correctness is WHEN fn's transaction begins:
// fn (and therefore every snapshot it establishes, across every retry) starts
// only after GET_LOCK has already returned success, so a queued loser's first
// read can only ever observe the winner's commit as already landed and
// durable. fn is free to call RunTxResult, and its units of work run on the
// fence's own session, lent to them through the ctx fn is handed
// (withLentSession), as internal/storage/dolt's fence runs its write on its
// pinned connection: the write's journal-writer admission is then taken on
// the session that already holds the fence, fence before journal, so a
// fenced delete waiting for the journal holds no second pool session
// (gastownhall/beads#6117).
func withDeleteFence(ctx context.Context, p *doltSQLProvider, id string, fn func(ctx context.Context) error) error {
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

	return fn(withLentSession(ctx, conn))
}

// lentSession is a session its holder lends, through a context, to the units
// of work begun under it (doltSQLProvider.BeginTx), one at a time: a unit of
// work begun while another has it borrowed draws its own session from the
// pool, because a second START TRANSACTION on the session would commit the
// first. A borrower never closes it; the lender does.
type lentSession struct {
	conn     *sql.Conn
	borrowed atomic.Bool
}

type lentSessionKey struct{}

// withLentSession lends conn to the units of work begun under the returned
// context.
func withLentSession(ctx context.Context, conn *sql.Conn) context.Context {
	return context.WithValue(ctx, lentSessionKey{}, &lentSession{conn: conn})
}

// borrowLentSession borrows ctx's lent session, or returns nil when ctx lends
// none or another unit of work has it.
func borrowLentSession(ctx context.Context) *lentSession {
	ls, ok := ctx.Value(lentSessionKey{}).(*lentSession)
	if !ok || !ls.borrowed.CompareAndSwap(false, true) {
		return nil
	}
	return ls
}

// giveBack returns a borrowed session to its lender, or closes conn, which
// the borrower pinned from the pool itself, when ls is nil.
func (ls *lentSession) giveBack(conn *sql.Conn) {
	if ls == nil {
		_ = conn.Close()
		return
	}
	ls.borrowed.Store(false)
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
