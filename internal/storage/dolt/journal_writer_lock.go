package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Journal-writer admission for the direct store (gastownhall/beads#6117).
//
// The admission lock itself lives at the issueops seam
// (issueops.AcquireJournalWriterLock) so the store and the unit-of-work
// provider queue on ONE name; this file holds the store's two acquisition
// shapes:
//
//   - commitWriteTxOn admits through pinJournalWriteSession, on the session
//     its txBeginner hands it (the shared pool, or the pinned connection a
//     delete fence already holds — fence first, then journal, same session).
//   - the raw journal-scoped writers (wisps, dependencies, slots, comment
//     events) begin through beginJournalWriteTx, one small begin/cleanup
//     helper that pins the session, takes the lock, begins the transaction,
//     and hands back a cleanup that rolls back (a no-op once committed),
//     unlocks, and returns the session.
//
// Both acquire BEFORE BEGIN: a transaction that began before the previous
// holder committed still reads the old bd_events_seq counter, which is the
// exact 1213 conflict the lock exists to prevent. Embedded mode and a
// journal-off store take no lock at all (journalWriterLockRequired).

// journalWriterLockRequired reports whether a write on this store must take
// the journal-writer admission lock: only a server-mode store with the
// journal enabled, running for a route that did not declare itself
// journal-unlocked (a read/ephemeral unit of work, or an audited
// non-emitting write through the NoJournal funnels — such a transaction is
// scoped forbidden to emit instead, so it cannot silently race the admitted
// writers). The embedded engine serializes writers on the counter row
// in-process and does not serve GET_LOCK, and a journal-off store journals
// nothing, so neither queues on anything.
func (s *DoltStore) journalWriterLockRequired(ctx context.Context) bool {
	return s.serverMode && s.eventsJournalEnabled.Load() && !issueops.UnlockedJournalRoute(ctx)
}

// poolReadDeadline is the configured per-I/O read deadline of the store's
// pool (Config.PoolReadTimeout; applyPoolKnobs resolves env/config so it is
// set by the time a store is open). Admission clamps every GET_LOCK wait
// strictly below it so a contended lock answers as lock contention, not an
// i/o timeout. Zero means unknown, and the admission helper then applies no
// clamp.
func (s *DoltStore) poolReadDeadline() time.Duration {
	if s.cfg == nil {
		return 0
	}
	return s.cfg.PoolReadTimeout
}

// acquireJournalWriterLockOn takes the journal-writer admission lock on the
// pinned session conn runs on, with this store's database name, pool read
// deadline, and the route-fixed admission deadline on ctx. The release
// closure is idempotent and detached from ctx.
func (s *DoltStore) acquireJournalWriterLockOn(ctx context.Context, conn *sql.Conn) (func(), error) {
	return issueops.AcquireJournalWriterLock(ctx, conn, s.database,
		issueops.JournalAdmissionDeadline(ctx), s.poolReadDeadline())
}

// pinJournalWriteSession is commitWriteTxOn's admission step. It returns the
// session the transaction must run on — the caller-pinned connection when
// beginner already is one (the delete fence's, so a fenced write takes
// fence then journal on the SAME session, and never needs a second
// store-pool connection), or a connection pinned from the pool — plus the
// admission release and a cleanup that releases the lock and returns the
// session only when this helper owns it.
//
// A nil conn means admission was not required (embedded, journal off, or the
// advisory route found contention — see the caller) and the transaction runs
// on beginner exactly as before.
func (s *DoltStore) pinJournalWriteSession(ctx context.Context, beginner txBeginner) (*sql.Conn, func(), error) {
	if !s.journalWriterLockRequired(ctx) {
		return nil, func() {}, nil
	}
	if conn, ok := beginner.(*sql.Conn); ok {
		release, err := s.acquireJournalWriterLockOn(ctx, conn)
		if err != nil {
			return nil, nil, err
		}
		// The fence owns the session and closes it; only the lock is ours.
		return conn, release, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("journal writer admission: pin session: %w", err)
	}
	release, err := s.acquireJournalWriterLockOn(ctx, conn)
	if err != nil {
		// An uncertain acquisition already discarded the session; a busy or
		// canceled one leaves it clean, and either way the store is done
		// with it here.
		_ = conn.Close()
		return nil, nil, err
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			release()
			_ = conn.Close()
		})
	}
	return conn, cleanup, nil
}

// beginJournalWriteTx begins one raw journal-scoped write transaction under
// the journal-writer admission lock. It is the one begin/cleanup helper the
// raw journal-scoped sites (wisp writes, wisp dependency writes, wisp
// metadata slots, comment events) route through, so none of them can begin
// unlocked while the journal is on.
//
// The returned cleanup is idempotent (safe to call once deferred and once
// explicitly) and rolls the transaction back (a no-op once it has committed),
// releases the admission lock, and returns the pinned session to the pool, in
// that order: the lock must never outlive an open transaction on the session,
// and a session must go back only after its lock state is settled. Sites own
// their journal scoping and their commit statement, and a site that performs a
// post-commit repair which opens another write transaction (the blocked recheck,
// DOLT_ADD/DOLT_COMMIT on a fresh session) calls the cleanup right after its
// SQL commit — the repair admits on its own turn, never nested inside this
// write's lock or pinned session. Deferring the cleanup next to the scope
// cleanup keeps every exit path (error, panic, cancellation) in the same
// order — transaction first, lock second, session third.
//
// When admission is not required the transaction begins on the shared pool
// exactly as before, and the cleanup only rolls back.
func (s *DoltStore) beginJournalWriteTx(ctx context.Context) (*sql.Tx, func(), error) {
	if !s.journalWriterLockRequired(ctx) {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, nil, err
		}
		var once sync.Once
		return tx, func() { once.Do(func() { _ = tx.Rollback() }) }, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	release, err := s.acquireJournalWriterLockOn(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		release()
		_ = conn.Close()
		return nil, nil, err
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = tx.Rollback() // no-op once committed
			release()
			_ = conn.Close()
		})
	}
	return tx, cleanup, nil
}