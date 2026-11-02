package uow

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Journal-writer admission for the proxied provider (gastownhall/beads#6117):
// the same database-scoped lock the server store takes (issueops.
// AcquireJournalWriter), so direct and proxied writers on one database queue
// on one name. doltSQLProvider.BeginTx takes it on the pinned session before
// START TRANSACTION, for every unit of work whose context does not declare it
// journal-free, and doltServerTx gives it up once the transaction has ended.

// journalWriterMode is what a unit of work's context says about the journal.
type journalWriterMode int

const (
	// journalWriterQueue: the unit of work may journal, so it queues on the
	// lock until its admission deadline. An unmarked context is this mode.
	journalWriterQueue journalWriterMode = iota
	// journalWriterUnlocked: the unit of work is declared journal-free (a read,
	// an ephemeral write). It takes no lock, and an emit inside it is refused.
	journalWriterUnlocked
)

type journalWriterKey struct{}

type journalWriterMarker struct {
	mode    journalWriterMode
	admitBy time.Time
}

// withJournalWriter marks ctx for units of work that queue on the lock until
// the deadline fixed now from budget. A retry loop marks once, before its
// first attempt, so its attempts share that one deadline.
func withJournalWriter(ctx context.Context, budget time.Duration) context.Context {
	return context.WithValue(ctx, journalWriterKey{}, journalWriterMarker{mode: journalWriterQueue, admitBy: journalAdmitBy(ctx, budget)})
}

// withoutJournalWriter marks ctx for journal-free units of work.
func withoutJournalWriter(ctx context.Context) context.Context {
	return context.WithValue(ctx, journalWriterKey{}, journalWriterMarker{mode: journalWriterUnlocked})
}

// journalWriterFor reads ctx's marker. An unmarked context is a writer with a
// fresh JournalWriterAdmissionBudget, the conservative reading of a direct
// NewUOW.
func journalWriterFor(ctx context.Context) journalWriterMarker {
	if m, ok := ctx.Value(journalWriterKey{}).(journalWriterMarker); ok {
		return m
	}
	return journalWriterMarker{mode: journalWriterQueue, admitBy: journalAdmitBy(ctx, issueops.JournalWriterAdmissionBudget)}
}

// journalAdmitBy is the earlier of now+budget and ctx's own deadline.
func journalAdmitBy(ctx context.Context, budget time.Duration) time.Time {
	admitBy := time.Now().Add(budget)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(admitBy) {
		return deadline
	}
	return admitBy
}

// admitJournalWriter takes database's lock on conn by the given deadline. The
// proxied pool sets no read deadline, so every GET_LOCK slice fits.
func admitJournalWriter(ctx context.Context, conn *sql.Conn, database string, admitBy time.Time) (*issueops.JournalWriterLock, error) {
	return issueops.AcquireJournalWriter(ctx, conn, issueops.JournalWriterAdmission{
		Database: database,
		AdmitBy:  admitBy,
	})
}
