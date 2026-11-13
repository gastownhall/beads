package dolt

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
	// (mirrors schema.MigrationLockName's bd_schema_init: prefix, same
	// reason): two beads databases sharing one dolt sql-server must not
	// collide on the same issue id.
	deleteFenceLockPrefix = "bd_delete:"
	// deleteFenceLockNameMaxLength mirrors MySQL/Dolt's 64-byte GET_LOCK name limit.
	deleteFenceLockNameMaxLength = 64
	// deleteFenceLockTimeoutSeconds bounds how long a guarded delete waits to
	// enter the fence. It must comfortably exceed withRetryTx's own
	// server-mode MaxElapsedTime (15s): a racer waiting its turn can be
	// queued behind another racer that is itself retrying a serialization
	// failure for up to that long.
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
// mirrors schema.MigrationLockName.
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
// single id against each other on a Dolt sql-server (mc-zndi7.73).
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
// A session-scoped GET_LOCK, held on one pinned connection across the whole
// guarded write (acquired before BeginTx, released only after Commit), turns
// that silent double-delete into a queue instead: the loser's transaction
// begins only after the winner's has already committed and is durable, so
// its existence probe inside DeleteInTx finds the row gone and fails through
// the ordinary NotFoundError path instead of silently "succeeding" a second
// time. No schema or migration change is needed — GET_LOCK/RELEASE_LOCK are
// built-in Dolt/MySQL session primitives, already an established repo idiom
// (schema.MigrateUpWithLock).
//
// conn stays pinned for the ENTIRE operation on purpose: GET_LOCK and
// RELEASE_LOCK are session-scoped, not transaction-scoped, so acquiring on
// one connection and running the write on another would not serialize
// anything — a second racer could then acquire the lock and, under
// repeatable-read isolation, still observe the pre-delete row because the
// first racer's delete was not yet durable. Running GET_LOCK, the retried
// write, and RELEASE_LOCK all on one *sql.Conn (via withRetryTxOn) is the
// same contract schema.MigrateUpWithLock documents for migrations.
//
// This also means withDeleteFence must never be called when the store's pool
// could be sized to a single connection (see dolt_test.go's setupTestStore,
// which pins MaxOpenConns to 1 because DOLT_CHECKOUT is session-level): a
// second, pool-sourced connection for the write would then never be
// grantable while this fence holds the only one. Pinning ONE connection for
// both the lock and the write (rather than a lock connection plus a
// pool-sourced write connection) avoids that deadlock by construction —
// the fenced write never needs a second connection at all.
func (s *DoltStore) withDeleteFence(ctx context.Context, id string, fn func(tx *sql.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("delete fence: acquire connection: %w", err)
	}
	defer conn.Close()

	lockName := deleteFenceLockName(s.database, id)
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, deleteFenceLockTimeoutSeconds).Scan(&locked); err != nil {
		return fmt.Errorf("delete fence: acquire lock: %w: %w", ErrDeleteFenceUnavailable, err)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return fmt.Errorf("delete fence: acquire lock: %w: timed out after %ds", ErrDeleteFenceUnavailable, deleteFenceLockTimeoutSeconds)
	}
	defer releaseDeleteFenceLock(conn, lockName)

	return s.withRetryTxOn(ctx, conn, fn)
}

// releaseDeleteFenceLock releases a lock acquired by withDeleteFence from the
// same pinned session, after the guarded write's Commit has returned. It runs
// on its own short-lived context so a caller-canceled ctx cannot strand the
// lock held past this call.
//
// An unexpected result discards the physical connection (same defensive
// pattern as schema's discardConn) rather than returning it to the pool: the
// lock's true state on that session is now unknown, and handing it to
// another caller could let a future guarded delete believe it holds the
// fence when it does not.
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
