package versioncontrolops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

// Server-mode commit serialization (GH#8590, dolthub/dolt#12258).
//
// Every bd write on a Dolt sql-server ends in DOLT_ADD + DOLT_COMMIT on some
// session. When another session moves the branch HEAD while that commit is in
// flight, Dolt 3-way-merges the moved HEAD into the committing session's
// staged root and adopts the result without checking it for conflicts, so the
// commit can carry conflict artifacts even though nothing diverged. bd closes
// that window by holding a per-database named lock, CommitLockName(db), from
// just before the first DOLT_ADD until DOLT_COMMIT returns, on every
// server-mode commit plane.
//
// GET_LOCK/RELEASE_LOCK are session-scoped, so the lock is always taken and
// released on one pinned session, and a session whose release cannot be
// confirmed is discarded instead of being returned to the pool, where it
// would stall every other writer until the connection was recycled.
//
// The lock fails open: when it is not granted within the wait (default
// DefaultCommitLockWait), the commit runs unlocked, exactly as it did before
// the lock existed, and the timeout is logged and counted. A query error on
// GET_LOCK is returned like any other publication failure at that point.
const (
	// commitLockPrefix namespaces the lock within Dolt's server-wide GET_LOCK
	// name space (the same reason as schema.MigrationLockName and
	// deleteFenceLockName): databases sharing one sql-server must not
	// serialize against each other.
	commitLockPrefix = "bd_commit:"
	// commitLockNameMaxLength mirrors MySQL/Dolt's 64-byte GET_LOCK name limit.
	commitLockNameMaxLength = 64
	// DefaultCommitLockWait is how long a commit waits for the lock before
	// committing unlocked.
	DefaultCommitLockWait = 5 * time.Second
	// commitLockDeadlineMargin keeps the GET_LOCK wait short of the caller's
	// deadline, so the driver does not kill the connection mid-wait.
	commitLockDeadlineMargin = time.Second
	// commitLockReleaseTimeout bounds RELEASE_LOCK, run on a detached context
	// so a caller-canceled ctx cannot strand the lock.
	commitLockReleaseTimeout = 5 * time.Second
	// commitLockWarnEvery rate-limits the fail-open warning.
	commitLockWarnEvery = time.Minute
)

// CommitLockName returns the named-lock key that serializes Dolt commits on
// database. A "/<branch>" revision suffix is dropped and the name lowercased,
// because Dolt database names are case-insensitive and every plane must agree
// on one string. A name longer than the 64-byte GET_LOCK limit is hashed to
// fnv64a. This string is a cross-version contract: bd processes of different
// versions on one server serialize only if they compute the same name.
func CommitLockName(database string) string {
	base := database
	if i := strings.IndexByte(base, '/'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(base)
	raw := commitLockPrefix + base
	if len(raw) <= commitLockNameMaxLength {
		return raw
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(base))
	return fmt.Sprintf("%s%016x", commitLockPrefix, h.Sum64())
}

// commitLockWaitSeconds is the GET_LOCK timeout argument: wait capped at the
// caller's remaining deadline less a margin, truncated to whole seconds (GMS
// takes integer seconds), never below 0 (0 means try once).
func commitLockWaitSeconds(ctx context.Context, wait time.Duration) int64 {
	if wait <= 0 {
		wait = DefaultCommitLockWait
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) - commitLockDeadlineMargin; remaining < wait {
			wait = remaining
		}
	}
	secs := int64(wait / time.Second)
	if secs < 0 {
		return 0
	}
	return secs
}

// CommitLockQuerier is a handle bound to ONE session: a *sql.Conn, or a
// *sql.Tx (whose statements all run on its connection).
type CommitLockQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// CommitLock is one GET_LOCK attempt on a session.
type CommitLock struct {
	name string
	held bool
	// Waited is how long GET_LOCK took to answer.
	Waited time.Duration
	// TimedOut reports that the lock was not granted within the wait and the
	// commit proceeds unlocked.
	TimedOut bool
}

// Held reports whether the lock was granted.
func (l *CommitLock) Held() bool { return l != nil && l.held }

// Name is the lock key.
func (l *CommitLock) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

var commitLockLastWarn atomic.Int64

// AcquireCommitLock runs GET_LOCK(CommitLockName(database), wait) on q's
// session. A 0 or NULL result is not an error: the returned lock is not held
// (TimedOut) and the caller commits unlocked. A query error is returned.
func AcquireCommitLock(ctx context.Context, q CommitLockQuerier, database string, wait time.Duration) (*CommitLock, error) {
	l := &CommitLock{name: CommitLockName(database)}
	start := time.Now()
	var got sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", l.name, commitLockWaitSeconds(ctx, wait)).Scan(&got)
	l.Waited = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("acquire commit lock: %w", err)
	}
	if got.Valid && got.Int64 == 1 {
		l.held = true
		return l, nil
	}
	l.TimedOut = true
	now := time.Now().UnixNano()
	if last := commitLockLastWarn.Load(); now-last >= int64(commitLockWarnEvery) && commitLockLastWarn.CompareAndSwap(last, now) {
		log.Printf("dolt: commit lock %q not granted within %s; committing unlocked (GH#8590)", l.name, l.Waited.Round(time.Millisecond))
	}
	return l, nil
}

// Release runs RELEASE_LOCK on q's session on a detached context. It is a
// no-op returning true when the lock was never granted. It returns false
// when the release could not be confirmed, leaving the lock marked held; the
// caller must then retry on the same session or discard the session rather
// than return it to a pool.
func (l *CommitLock) Release(q CommitLockQuerier) (confirmed bool) {
	if !l.Held() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), commitLockReleaseTimeout)
	defer cancel()
	var released sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", l.name).Scan(&released); err != nil {
		return false
	}
	if !released.Valid || released.Int64 != 1 {
		return false
	}
	// Only a confirmed release clears held, so a caller with a fallback
	// session (see the DoltStore in-transaction planes) can retry there.
	l.held = false
	return true
}

// CommitLockConfig configures how a server-mode commit plane takes the lock.
type CommitLockConfig struct {
	// Database is the store's database; the lock key is CommitLockName(Database).
	Database string
	// Wait bounds GET_LOCK; zero means DefaultCommitLockWait.
	Wait time.Duration
	// OnAcquire, when set, observes each granted or timed-out attempt (metrics).
	OnAcquire func(ctx context.Context, l *CommitLock)
	// OnReleaseUnconfirmed, when set, observes a release that could not be
	// confirmed (the session is discarded).
	OnReleaseUnconfirmed func()
}

// Acquire is AcquireCommitLock with c's database, wait and observer.
func (c CommitLockConfig) Acquire(ctx context.Context, q CommitLockQuerier) (*CommitLock, error) {
	l, err := AcquireCommitLock(ctx, q, c.Database, c.Wait)
	if err != nil {
		return nil, err
	}
	if c.OnAcquire != nil {
		c.OnAcquire(ctx, l)
	}
	return l, nil
}

// ReleaseOrDiscard releases l on q's session and, when the release is not
// confirmed, reports it and calls discard so the session never returns to a
// pool still holding the lock.
func (c CommitLockConfig) ReleaseOrDiscard(l *CommitLock, q CommitLockQuerier, discard func()) {
	if l.Release(q) {
		return
	}
	if c.OnReleaseUnconfirmed != nil {
		c.OnReleaseUnconfirmed()
	}
	if discard != nil {
		discard()
	}
}

// DiscardConn makes database/sql close conn's physical session instead of
// returning it to the pool; the server then drops the session's locks.
func DiscardConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

// HoldCommitLockOnConn takes the commit lock on conn, a session the caller
// has pinned, and returns the function that releases it on the same session
// (discarding conn when the release is unconfirmed). release is safe to call
// when the lock timed out. A release outcome never changes a commit's result.
func HoldCommitLockOnConn(ctx context.Context, conn *sql.Conn, cfg CommitLockConfig) (release func(), err error) {
	l, err := cfg.Acquire(ctx, conn)
	if err != nil {
		return nil, err
	}
	return func() { cfg.ReleaseOrDiscard(l, conn, func() { DiscardConn(conn) }) }, nil
}
