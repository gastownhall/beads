package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"time"
)

// Journal-writer admission (gastownhall/beads#6117).
//
// Every journaled write transaction draws its seq from the one bd_events_seq
// row (nextEventSeq). On a Dolt sql-server two transactions that overlap both
// bump that row, so whichever commits second is aborted with a serialization
// failure and its whole body is replayed: a long body (bd create --deps with
// many edges) keeps losing to short writers until its retry budget runs out.
// Retrying cannot help, because the conflict is built into the counter.
//
// Writers that may journal therefore queue on one database-scoped SQL advisory
// lock, taken on the session that will run the transaction BEFORE it begins:
// a transaction that began before the previous holder committed would still
// read the old counter and conflict anyway. The counter and its conflict stay
// as the backstop (a writer that does not cooperate, such as an older binary,
// still loses with 1213 and is replayed as before); the lock only turns the
// cooperating writers' replay storm into a queue.
//
// The lock is a GET_LOCK, which the server holds per session, not per
// transaction. That is why the session must stay pinned from admission to
// release, why release comes after the transaction ends and before the
// connection goes back to the pool, and why a session whose lock state is
// unknown is discarded rather than pooled.

const (
	journalWriterLockPrefix = "bd_events_seq:"
	// journalWriterLockNameMaxLength mirrors MySQL/Dolt's 64-byte GET_LOCK name
	// limit, like schema.MigrationLockName and the delete fence.
	journalWriterLockNameMaxLength = 64

	// JournalWriterAdmissionBudget bounds how long a raw or at-most-once
	// journaled write waits for admission. Routes with a retry loop use that
	// loop's own budget instead (the store's withRetryTx, uow's
	// RunTxResultWithin), so the lock never waits longer than the conflicts it
	// replaces were retried.
	JournalWriterAdmissionBudget = 15 * time.Second

	// journalWriterWaitSlice is the longest single GET_LOCK wait. It is kept
	// short so one blocked statement never outlives the remaining admission
	// time or the pool's per-read deadline (see journalWriterWaitSeconds).
	journalWriterWaitSlice = time.Second
	// journalWriterPollPace spaces the zero-wait attempts used when a whole
	// second does not fit.
	journalWriterPollPace = 50 * time.Millisecond
	// journalWriterReleaseTimeout bounds RELEASE_LOCK, which runs detached from
	// the caller's context so a canceled caller cannot strand the lock.
	journalWriterReleaseTimeout = 3 * time.Second
)

// ErrJournalWriterBusy reports that a journaled write was not admitted within
// its admission budget because other writers held the journal-writer lock. The
// write's body never ran on this attempt; the request is retryable.
var ErrJournalWriterBusy = errors.New("journal writer lock busy")

// ErrJournalUnlockedTx reports a journal emit inside a transaction that was
// declared journal-free (a NoJournal write, a read, an ephemeral write) while
// the journal is on. Such a transaction took no journal-writer lock, so letting
// it allocate a seq would race the admitted writers. The emit is refused before
// any seq is allocated and the transaction cannot commit.
var ErrJournalUnlockedTx = errors.New("journal emit in a transaction that did not take the journal writer lock")

// JournalWriterBusyError is the ErrJournalWriterBusy failure: it names the lock,
// the database, and how long this writer waited. The wait says nothing about
// how long any holder has held the lock.
type JournalWriterBusyError struct {
	Lock     string
	Database string
	Waited   time.Duration
}

func (e *JournalWriterBusyError) Error() string {
	return fmt.Sprintf("journal writer lock %q for database %q not granted after %s: %v",
		e.Lock, e.Database, e.Waited.Round(time.Millisecond), ErrJournalWriterBusy)
}

// Is makes errors.Is(err, ErrJournalWriterBusy) match.
func (e *JournalWriterBusyError) Is(target error) bool {
	return target == ErrJournalWriterBusy
}

// JournalWriterLockName returns the journal-writer lock name for database,
// hashing down to fnv64a when the readable form would pass the 64-byte limit.
// GET_LOCK names are server-wide, so the database is part of the name: two
// beads databases on one server do not queue on each other.
func JournalWriterLockName(database string) string {
	raw := journalWriterLockPrefix + database
	if len(raw) <= journalWriterLockNameMaxLength {
		return raw
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(database))
	return fmt.Sprintf("%s%016x", journalWriterLockPrefix, h.Sum64())
}

// JournalWriterAdmission describes one admission attempt.
type JournalWriterAdmission struct {
	// Database is the database whose journal the writer appends to.
	Database string
	// AdmitBy is the absolute admission deadline, fixed once when the outer
	// operation started: retries of that operation do not restart it.
	AdmitBy time.Time
	// ReadTimeout is the per-read deadline of the pool the session came from,
	// zero when it has none. A GET_LOCK wait is kept strictly below it so a
	// contended wait is never cut off as an i/o timeout.
	ReadTimeout time.Duration
}

// JournalWriterLock is a held journal-writer lock on one pinned session.
type JournalWriterLock struct {
	conn *sql.Conn
	name string
	held bool
}

// AcquireJournalWriter takes the journal-writer lock on conn, waiting in short
// GET_LOCK slices until adm.AdmitBy. It returns a *JournalWriterBusyError when
// the deadline passes first, the caller's context error when ctx ends first,
// and any query failure as itself. A failed or NULL GET_LOCK leaves the
// session's lock state unknown, so the session is discarded: the caller must
// close conn and not reuse it.
func AcquireJournalWriter(ctx context.Context, conn *sql.Conn, adm JournalWriterAdmission) (*JournalWriterLock, error) {
	name := JournalWriterLockName(adm.Database)
	start := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("journal writer lock %q: %w", name, err)
		}
		remaining := time.Until(adm.AdmitBy)
		if remaining <= 0 {
			if deadline, ok := ctx.Deadline(); ok && !deadline.After(adm.AdmitBy) {
				// The caller's own deadline is the bound that ran out.
				return nil, fmt.Errorf("journal writer lock %q: %w", name, context.DeadlineExceeded)
			}
			return nil, &JournalWriterBusyError{Lock: name, Database: adm.Database, Waited: time.Since(start)}
		}
		wait := journalWriterWaitSeconds(remaining, adm.ReadTimeout)
		granted, err := getLock(ctx, conn, name, wait)
		if err != nil {
			return nil, err
		}
		if granted {
			lock := &JournalWriterLock{conn: conn, name: name, held: true}
			// A grant that raced the caller's cancellation must not run a body
			// the caller has given up on.
			if err := ctx.Err(); err != nil {
				lock.Release()
				return nil, fmt.Errorf("journal writer lock %q: %w", name, err)
			}
			return lock, nil
		}
		if wait == 0 {
			pace := journalWriterPollPace
			if remaining < pace {
				pace = remaining
			}
			timer := time.NewTimer(pace)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}

// TryJournalWriter makes one non-waiting attempt at the journal-writer lock on
// conn, for advisory work that should be skipped rather than queued when
// another writer holds it. ok is false, with a nil error, on contention.
func TryJournalWriter(ctx context.Context, conn *sql.Conn, database string) (lock *JournalWriterLock, ok bool, err error) {
	name := JournalWriterLockName(database)
	granted, err := getLock(ctx, conn, name, 0)
	if err != nil || !granted {
		return nil, false, err
	}
	return &JournalWriterLock{conn: conn, name: name, held: true}, true, nil
}

// journalWriterWaitSeconds picks the GET_LOCK wait for one attempt: one second
// when a whole second fits strictly inside both the remaining admission time
// and the pool's read deadline (with a second of headroom for the round trip),
// zero otherwise. Zero is an immediate try; the caller paces those attempts.
func journalWriterWaitSeconds(remaining, readTimeout time.Duration) int {
	if remaining <= journalWriterWaitSlice {
		return 0
	}
	if readTimeout > 0 && readTimeout <= 2*journalWriterWaitSlice {
		return 0
	}
	return int(journalWriterWaitSlice / time.Second)
}

// getLock runs one GET_LOCK. 1 is a grant and 0 is contention; NULL or a failed
// query leaves the session's state unknown, so it is discarded.
func getLock(ctx context.Context, conn *sql.Conn, name string, waitSeconds int) (bool, error) {
	var granted sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, waitSeconds).Scan(&granted); err != nil {
		discardSession(conn)
		return false, fmt.Errorf("journal writer lock %q: acquire: %w", name, err)
	}
	if !granted.Valid {
		discardSession(conn)
		return false, fmt.Errorf("journal writer lock %q: acquire: GET_LOCK returned NULL", name)
	}
	return granted.Int64 == 1, nil
}

// Release releases the lock on its session. It is idempotent and nil-safe,
// runs on a detached bounded context, and discards the session unless
// RELEASE_LOCK answers 1: GET_LOCK locks are per session and reentrant, so a
// pooled session that might still own the lock would keep every writer out.
// Call it only once the session's transaction has committed or rolled back.
func (l *JournalWriterLock) Release() {
	if l == nil || !l.held {
		return
	}
	l.held = false
	ctx, cancel := context.WithTimeout(context.Background(), journalWriterReleaseTimeout)
	defer cancel()
	var released sql.NullInt64
	err := l.conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", l.name).Scan(&released)
	if err != nil || !released.Valid || released.Int64 != 1 {
		discardSession(l.conn)
	}
}

// Abandon gives the lock up without a RELEASE_LOCK by discarding its session,
// for a caller that cannot prove the session's transaction has ended: the
// server drops a session's locks with the session. Idempotent and nil-safe.
func (l *JournalWriterLock) Abandon() {
	if l == nil || !l.held {
		return
	}
	l.held = false
	discardSession(l.conn)
}

// discardSession marks conn's physical connection bad so database/sql closes it
// instead of returning it to the pool (the delete fence's pattern).
func discardSession(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}
