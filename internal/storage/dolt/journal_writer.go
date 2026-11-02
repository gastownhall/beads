package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Journal-writer admission for the server store (gastownhall/beads#6117); the
// lock itself and why it exists are documented at issueops.AcquireJournalWriter.
//
// Every write transaction that scopes the journal on a server-mode store with
// the journal on takes the lock on the session that will run it, before BEGIN:
// commitWriteTxWith for the withRetryTx/withWriteTx/commitWriteTx funnels and
// runDoltTransactionRecording for RunInTransaction. Each releases
// it once its SQL transaction has ended and before any post-commit blocked
// recheck, which is itself a journaled write and takes its own admission.
// Embedded stores and a journal-off store take no lock.

// locksJournalWriters reports whether a write transaction whose journal
// decision is journalOn must take the journal-writer lock.
func (s *DoltStore) locksJournalWriters(journalOn bool) bool {
	return journalOn && s.serverMode
}

// journalAdmitBy fixes the absolute admission deadline for an operation that
// starts now with the given budget: the earlier of now+budget and ctx's own
// deadline.
func journalAdmitBy(ctx context.Context, budget time.Duration) time.Time {
	admitBy := time.Now().Add(budget)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(admitBy) {
		return deadline
	}
	return admitBy
}

// poolReadTimeout is the per-read deadline of the store's pool, zero when its
// DSN sets none. A GET_LOCK wait must stay below it.
func (s *DoltStore) poolReadTimeout() time.Duration {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return 0
	}
	return cfg.ReadTimeout
}

// journalWriterAdmission is one hold of the journal-writer lock on a session,
// and of that session when this admission pinned it from the pool.
type journalWriterAdmission struct {
	conn  *sql.Conn
	lock  *issueops.JournalWriterLock
	owned bool // conn was pinned for this admission and is closed with it
	once  sync.Once
}

// release unlocks and, when this admission pinned the session, returns it.
// Call it only once the transaction on the session has committed or rolled
// back. Idempotent and nil-safe.
func (a *journalWriterAdmission) release() {
	if a == nil {
		return
	}
	a.once.Do(func() {
		a.lock.Release()
		if a.owned {
			_ = a.conn.Close()
		}
	})
}

// admitJournalWriter takes the journal-writer lock for a transaction that will
// begin on beginner. A *sql.Conn is already the transaction's session (the
// delete fence pins one) and takes the lock itself; a *sql.DB is pinned to one
// session first, and that acquisition counts against admitBy. The returned
// admission's conn is the session to begin the transaction on.
func (s *DoltStore) admitJournalWriter(ctx context.Context, beginner txBeginner, admitBy time.Time) (*journalWriterAdmission, error) {
	adm := &journalWriterAdmission{}
	start := time.Now()
	switch b := beginner.(type) {
	case *sql.Conn:
		adm.conn = b
	case *sql.DB:
		pinCtx, cancel := context.WithDeadline(ctx, admitBy)
		conn, err := b.Conn(pinCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				name := issueops.JournalWriterLockName(s.database)
				return nil, &issueops.JournalWriterBusyError{Lock: name, Database: s.database, Waited: time.Since(start)}
			}
			return nil, fmt.Errorf("journal writer: acquire connection: %w", err)
		}
		adm.conn, adm.owned = conn, true
	default:
		return nil, fmt.Errorf("journal writer: cannot pin a session from %T", beginner)
	}
	lock, err := issueops.AcquireJournalWriter(ctx, adm.conn, issueops.JournalWriterAdmission{
		Database:    s.database,
		AdmitBy:     admitBy,
		ReadTimeout: s.poolReadTimeout(),
	})
	if err != nil {
		if adm.owned {
			_ = adm.conn.Close()
		}
		return nil, err
	}
	adm.lock = lock
	return adm, nil
}
