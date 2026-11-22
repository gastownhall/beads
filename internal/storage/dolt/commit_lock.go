package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// Server-mode commit serialization (GH#8590). Every commit plane of a
// server-mode store holds versioncontrolops.CommitLockName(s.database) from
// just before its first DOLT_ADD until its DOLT_COMMIT returns; see
// versioncontrolops/commit_lock.go for why and for the fail-open contract.
//
// Lock order: a guarded delete takes its bd_delete:<db>:<id> fence before the
// commit lock, never the reverse, and migration commits under bd_schema_init
// never take it, so the named locks cannot form a cycle.

// commitLockConfig is the store's commit-lock configuration, with the
// observers that feed its metrics.
func (s *DoltStore) commitLockConfig() versioncontrolops.CommitLockConfig {
	return versioncontrolops.CommitLockConfig{
		Database: s.database,
		Wait:     s.commitLockWait,
		OnAcquire: func(ctx context.Context, l *versioncontrolops.CommitLock) {
			doltMetrics.commitLockWait.Record(ctx, float64(l.Waited.Milliseconds()))
			if l.TimedOut {
				s.commitLockTimeouts.Add(1)
				doltMetrics.commitLockTimeouts.Add(ctx, 1)
			}
		},
		OnReleaseUnconfirmed: func() {
			doltMetrics.commitLockReleaseUnconfirmed.Add(context.Background(), 1)
		},
	}
}

// holdCommitLockOnConn takes the commit lock on conn, a connection the
// caller pinned for its whole add+commit, and returns its release. Outside
// server mode it takes nothing: an embedded engine has no concurrent
// sessions to serialize.
func (s *DoltStore) holdCommitLockOnConn(ctx context.Context, conn *sql.Conn) (func(), error) {
	if !s.serverMode {
		return func() {}, nil
	}
	return versioncontrolops.HoldCommitLockOnConn(ctx, conn, s.commitLockConfig())
}

// stageAndCommitSerialized is versioncontrolops.StageAndCommitSerialized in
// server mode and plain StageAndCommit otherwise.
func (s *DoltStore) stageAndCommitSerialized(ctx context.Context, conn *sql.Conn, dirtyTables map[string]bool, commitMsg, author string) error {
	if !s.serverMode {
		return versioncontrolops.StageAndCommit(ctx, conn, dirtyTables, commitMsg, author)
	}
	return versioncontrolops.StageAndCommitSerialized(ctx, conn, s.commitLockConfig(), dirtyTables, commitMsg, author)
}

// afterInTxCommitLockGrant runs right after holdCommitLockInTx records its
// GET_LOCK attempt. It is a no-op outside tests, which cancel the write there
// to prove the fallback release (bindCommitLockSlot) frees the lock.
var afterInTxCommitLockGrant = func() {}

// txCommitLock is the commit-lock slot of one write transaction begun by
// commitWriteTxOn: the pinned connection the transaction runs on, and the
// lock taken through the transaction, if any.
type txCommitLock struct {
	conn *sql.Conn
	lock *versioncontrolops.CommitLock
}

// bindCommitLockSlot binds tx, begun on the pinned conn, to a commit-lock
// slot. The returned function runs after tx has committed or rolled back: it
// releases a lock the in-transaction commit did not release itself (a
// canceled ctx, an error between grant and release) on conn, discarding
// conn when that release is unconfirmed, and unbinds the slot.
func (s *DoltStore) bindCommitLockSlot(tx *sql.Tx, conn *sql.Conn) func() {
	slot := &txCommitLock{conn: conn}
	s.commitLockTx.Store(tx, slot)
	return func() {
		s.commitLockTx.Delete(tx)
		if slot.lock.Held() {
			s.commitLockConfig().ReleaseOrDiscard(slot.lock, conn, func() { versioncontrolops.DiscardConn(conn) })
		}
	}
}

// holdCommitLockInTx takes the commit lock through tx (the transaction's own
// session) right before an in-transaction DOLT_ADD, and returns the release
// to run right after DOLT_COMMIT returns. DOLT_COMMIT publishes HEAD inside
// the statement, so releasing after it returns closes the commit's window. If
// the release does not run or is not confirmed, commitWriteTxOn's fallback
// releases on the pinned connection once tx has ended.
//
// A transaction begun outside commitWriteTxOn has no slot and takes no lock.
func (s *DoltStore) holdCommitLockInTx(ctx context.Context, tx *sql.Tx) (func(), error) {
	if !s.serverMode {
		return func() {}, nil
	}
	v, ok := s.commitLockTx.Load(tx)
	if !ok {
		// No slot: the transaction was not begun by commitWriteTxOn, so there
		// is no pinned session to fall back on. None of the in-tx commit
		// sites is reachable that way today.
		return func() {}, nil
	}
	slot := v.(*txCommitLock)
	cfg := s.commitLockConfig()
	l, err := cfg.Acquire(ctx, tx)
	if err != nil {
		return nil, err
	}
	slot.lock = l
	afterInTxCommitLockGrant()
	// An unconfirmed release leaves l marked held, and the slot's fallback
	// retries it on the pinned connection after tx ends.
	return func() { _ = l.Release(tx) }, nil
}
