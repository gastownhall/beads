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
// commitWriteTxWith for the withRetryTx/withWriteTx/commitWriteTx funnels,
// runDoltTransactionRecording for RunInTransaction, and beginJournalScopedTx
// for the raw wisp, slot, dependency and comment transactions. Each releases
// it once its SQL transaction has ended and before any post-commit blocked
// recheck, which is itself a journaled write and takes its own admission.
// Embedded stores and a journal-off store take no lock.

// journalTxMode is what a write transaction declares about the journal.
type journalTxMode int

const (
	// journalTxWriter: the body may journal, so the transaction is admitted
	// as a journal writer.
	journalTxWriter journalTxMode = iota
	// journalTxNoJournal: the body is declared journal-free. It takes no lock,
	// and an emit inside it is refused (withRetryTxNoJournal).
	journalTxNoJournal
)

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

type journalAdmitByKey struct{}

// withJournalAdmitBy fixes one admission deadline, from budget, for every
// attempt of a setup-retrying transaction route (RunInTransaction,
// RunInIssueLifecycleTransaction): a retried attempt does not restart it.
func withJournalAdmitBy(ctx context.Context, budget time.Duration) context.Context {
	return context.WithValue(ctx, journalAdmitByKey{}, journalAdmitBy(ctx, budget))
}

// journalAdmitByFor is the deadline withJournalAdmitBy fixed on ctx, or one
// fixed now from budget when ctx carries none.
func journalAdmitByFor(ctx context.Context, budget time.Duration) time.Time {
	if admitBy, ok := ctx.Value(journalAdmitByKey{}).(time.Time); ok {
		return admitBy
	}
	return journalAdmitBy(ctx, budget)
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
// Call it only when no transaction is open on the session. Idempotent and
// nil-safe.
func (a *journalWriterAdmission) release() {
	a.end(true)
}

// end gives the admission up once the session's transaction is over. ended
// reports that it committed or rolled back: the lock is released and the
// session kept. Otherwise the transaction may still be open, and a
// RELEASE_LOCK would let the next writer in before it ends, so the session is
// discarded and the server drops the lock with it. Either way a pinned session
// is then returned. Idempotent and nil-safe.
func (a *journalWriterAdmission) end(ended bool) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		if ended {
			a.lock.Release()
		} else {
			a.lock.Abandon()
		}
		if a.owned {
			_ = a.conn.Close()
		}
	})
}

// sqlTxEnded reports whether a transaction whose Commit returned err has
// ended: it committed, or the server refused it with a rollback guaranteed.
// Any other failure leaves the session's transaction state unknown.
func sqlTxEnded(err error) bool {
	return err == nil || isSerializationError(err) || isDoltAutocommitRollbackError(err)
}

// admitJournalWriter takes the journal-writer lock for a transaction that will
// begin on beginner. A *sql.Conn is already the transaction's session (the
// delete fence pins one) and takes the lock itself; a *sql.DB is pinned to one
// session first, and that acquisition counts against admitBy. The returned
// admission's conn is the session to begin the transaction on.
func (s *DoltStore) admitJournalWriter(ctx context.Context, beginner txBeginner, admitBy time.Time) (*journalWriterAdmission, error) {
	adm := &journalWriterAdmission{}
	switch b := beginner.(type) {
	case *sql.Conn:
		adm.conn = b
	case *sql.DB:
		conn, err := issueops.PinJournalWriterSession(ctx, b, s.database, admitBy)
		if err != nil {
			if errors.Is(err, issueops.ErrJournalWriterBusy) {
				return nil, err
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

// beginJournalScopedTx begins a raw write transaction with the journal scoped
// on it, admitted as a journal writer first when the store locks them. done
// rolls the transaction back unless it committed, clears the scope, ends the
// admission, and returns the session; defer it. commitSQLTx ends the
// admission as soon as the transaction commits, so a recheck that follows the
// commit never runs while this transaction still holds the lock.
func (s *DoltStore) beginJournalScopedTx(ctx context.Context) (tx *sql.Tx, done func(), err error) {
	journalOn := s.eventsJournalEnabled.Load()
	var beginner txBeginner = s.db
	var adm *journalWriterAdmission
	if s.locksJournalWriters(journalOn) {
		adm, err = s.admitJournalWriter(ctx, s.db, journalAdmitBy(ctx, issueops.JournalWriterAdmissionBudget))
		if err != nil {
			return nil, nil, err
		}
		beginner = adm.conn
	}
	tx, err = beginner.BeginTx(ctx, nil)
	if err != nil {
		adm.release()
		return nil, nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	if adm != nil {
		s.journalAdmissions.Store(tx, adm)
	}
	clearJournalScope := issueops.ScopeEventsJournalTransaction(tx, journalOn)
	return tx, func() {
		// After commitSQLTx the admission is already gone, so this
		// rollback's ErrTxDone ends nothing.
		rollbackErr := tx.Rollback()
		clearJournalScope()
		s.endJournalAdmission(tx, rollbackErr == nil)
	}, nil
}

// endJournalAdmission ends the admission beginJournalScopedTx bound to tx, if
// any; ended reports that tx committed or rolled back
// (journalWriterAdmission.end).
func (s *DoltStore) endJournalAdmission(tx *sql.Tx, ended bool) {
	if adm, ok := s.journalAdmissions.LoadAndDelete(tx); ok {
		adm.(*journalWriterAdmission).end(ended)
	}
}
