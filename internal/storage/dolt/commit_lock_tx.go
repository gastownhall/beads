package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// The transaction-scoped commit lock (GH#8590).
//
// Three server-mode planes — doltAddAndCommitInTx (update/claim/close/... via
// commitWriteTxOn), the role deleter and the tier sweeper — commit INSIDE the
// open SQL transaction: their DOLT_ADD/DOLT_COMMIT run through the *sql.Tx, on
// the session that transaction is pinned to. GET_LOCK is session-scoped, so
// acquiring and releasing through that same tx is the only correct shape, and
// it needs help from commitWriteTxOn: once the transaction has ended nothing
// can reach — or discard — its session through the *sql.Tx anymore, and a
// release whose result was lost in flight must discard the session rather
// than hand a lock-suspect connection back to the pool.
//
// commitWriteTxOn therefore pins the pool connection behind every tx it mints
// (the pool is the only beginner whose conn is otherwise anonymous) and binds
// a commitLockSlot to the *sql.Tx. holdCommitLockInTx, called by the three
// planes just before their first in-tx DOLT_ADD, acquires on the tx's session
// and returns a release closure; the slot is the fallback that releases on
// the pinned session after the transaction has committed or rolled back,
// discarding it when even that release is unconfirmed. A transaction begun
// outside commitWriteTxOn has no slot — its session cannot be reached after
// the fact — so it takes no lock, logged at debug level: none of the three
// planes is reachable that way today.

// commitLockSlot is the tx-bound commit-lock bookkeeping commitWriteTxOn
// binds for the write body's holdCommitLockInTx call.
type commitLockSlot struct {
	// conn is the pinned session the transaction runs on. Nil when the slot
	// exists but its beginner's connection is not pinnable here (a store
	// pool beginner other than s.db's, a test double): no lock is taken
	// then, because no session handle would survive the transaction.
	conn *sql.Conn
	// lock is the granted acquisition, nil until then.
	lock *versioncontrolops.CommitLock
	// unconfirmed records that the body's in-tx release could not confirm
	// the release: the transaction's session is lock-suspect, and
	// settleCommitLockSlot discards it once the tx has ended.
	unconfirmed bool
}

// discardSuspectConn marks conn unusable so Close returns it to the pool as
// dead, the delete-fence/schema discard pattern: the server drops the
// session's locks on disconnect, so a possibly-still-held commit lock cannot
// silently serialize the next writer for the pool's connection lifetime.
func discardSuspectConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

// settleCommitLockSlot is commitWriteTxOn's post-transaction cleanup for the
// commit lock: release on the pinned session a lock the body's own release did
// not (a panic unwound past its defer), and discard that session when any
// release through the transaction ended unconfirmed. It runs after the
// transaction has fully ended.
func (s *DoltStore) settleCommitLockSlot(tx *sql.Tx, slot *commitLockSlot, conn *sql.Conn) {
	s.commitLockSlots.Delete(tx)
	if slot == nil || conn == nil {
		return
	}
	if slot.lock != nil && slot.lock.Held() {
		if !slot.lock.Release(conn) {
			slot.unconfirmed = true
		}
	}
	if slot.unconfirmed {
		discardSuspectConn(conn)
	}
}

// holdCommitLockInTx takes the per-database commit lock on the transaction's
// own session, for the in-tx commit planes (doltAddAndCommitInTx, the role
// deleter, the tier sweeper), and returns the release closure that releases
// it through the same transaction on every return path of the critical
// section. The lock is taken only for transactions commitWriteTxOn minted
// with a pinnable session (see commitLockSlots): an unbound transaction takes
// no lock rather than risk a release it cannot clean up after.
//
// Fail-open matches the other commit planes: a not-obtained lock (timeout,
// NULL) is counted, warned rate-limited, and NOT an error — the commit
// proceeds unlocked. A GET_LOCK query error IS an error, handled by the
// caller exactly like a DOLT_ADD failure there.
func (s *DoltStore) holdCommitLockInTx(ctx context.Context, tx *sql.Tx) (release func(), err error) {
	slotAny, ok := s.commitLockSlots.Load(tx)
	if !ok {
		debug.Logf("dolt: commit lock skipped: transaction not minted by commitWriteTxOn, no session to release on\n")
		return func() {}, nil
	}
	slot, _ := slotAny.(*commitLockSlot)
	if slot == nil || slot.conn == nil {
		debug.Logf("dolt: commit lock skipped: transaction's beginner session not pinnable\n")
		return func() {}, nil
	}
	lock, err := versioncontrolops.AcquireCommitLock(ctx, tx, s.database, s.commitLockWait)
	if err != nil {
		return nil, err
	}
	if !lock.Held() {
		return func() {}, nil
	}
	slot.lock = lock
	return func() {
		// The transaction is still open here: release through it, on the
		// detached context Release uses, so a caller-canceled ctx cannot
		// strand the lock.
		if !lock.Release(tx) {
			slot.unconfirmed = true
		}
	}, nil
}

// pinCommitWriteTxConn resolves the pinned session and beginner for one
// commitWriteTxOn attempt: a caller-pinned *sql.Conn beginner is used as-is,
// and the store's shared pool is pinned per attempt so the transaction's
// session survives it as a handle. When the beginner is anything else (a
// test double), conn is nil and no commit lock is taken for that transaction.
// closePinned reports whether the caller owns closing the pinned session.
func (s *DoltStore) pinCommitWriteTxConn(ctx context.Context, beginner txBeginner) (conn *sql.Conn, effective txBeginner, closePinned bool, err error) {
	switch b := beginner.(type) {
	case *sql.Conn:
		// withDeleteFence's pinned session: the caller owns its lifetime and
		// already holds the per-id delete fence lock, taken BEFORE this
		// commit lock — never the reverse order anywhere (see withDeleteFence).
		return b, b, false, nil
	case *sql.DB:
		if b != s.db {
			return nil, beginner, false, nil
		}
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return nil, nil, false, fmt.Errorf("begin write tx: %w", err)
		}
		return conn, conn, true, nil
	default:
		return nil, beginner, false, nil
	}
}

// holdCommitLockOn takes this store's per-database commit lock on the pinned
// conn for one post-transaction commit critical section (the publication
// funnel and the explicit commit planes), bounded by the store's own wait.
func (s *DoltStore) holdCommitLockOn(ctx context.Context, conn *sql.Conn) (func(), error) {
	return versioncontrolops.HoldCommitLockOnConn(ctx, conn, s.database, s.commitLockWait)
}
