package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sync"
	"time"
)

// Journal-writer admission (gastownhall/beads#6117).
//
// In server mode with the events journal on, every journaled write transaction
// bumps the single bd_events_seq row inside its own transaction, so any two
// overlapping journaled writers contend on that one row: Dolt aborts the
// commit-time loser with 1213 and the write path replays the WHOLE body. A
// long body (bd create --deps with many edges) keeps losing to short writers
// until its retry budget runs out and the command fails — more retries cannot
// help, because the conflict is built into the counter.
//
// The fix is one database-scoped SQL advisory lock, taken on the session that
// will run the transaction BEFORE BEGIN: a transaction that began before the
// previous holder committed still reads the old counter, so serializing the
// BEGIN, not the seq UPDATE, is what makes the journal gapless and
// commit-ordered. Writers then wait on the lock instead of replaying work.
//
// This helper is the ONE admission implementation, shared by the direct store
// (internal/storage/dolt) and the unit-of-work provider
// (internal/storage/uow), so every journaled writer on one database queues on
// one name. It uses only database/sql and the server's own GET_LOCK /
// RELEASE_LOCK primitives — the same idiom the delete fence and
// schema.MigrateUpWithLock already use.

const (
	// journalWriterLockPrefix namespaces the journal-writer admission lock
	// within Dolt/MySQL's server-wide GET_LOCK name space (mirrors the delete
	// fence's bd_delete: prefix): two beads databases sharing one dolt
	// sql-server must not queue on each other's writers.
	journalWriterLockPrefix = "bd_events_seq:"
	// journalWriterLockNameMaxLength mirrors MySQL/Dolt's 64-byte GET_LOCK
	// name limit.
	journalWriterLockNameMaxLength = 64
	// JournalWriterLockWait bounds a single server-side GET_LOCK wait. One
	// second keeps a waiter responsive to cancellation and re-checks the
	// admission deadline between slices, instead of parking inside one
	// 15-second server call the caller cannot interrupt. It is also a FLOOR
	// for dolt.pool-read-timeout in server mode with the journal on: a
	// contended GET_LOCK whose wait exceeds the pool's read deadline fails
	// with an i/o timeout instead of queueing, so the clamp below shortens
	// the wait to fit strictly under that deadline.
	JournalWriterLockWait = 1 * time.Second
	// journalWriterReadDeadlineMargin keeps the server-side GET_LOCK wait
	// strictly below the pool read deadline: the answer has to make it back
	// inside the deadline too, not just the question out.
	journalWriterReadDeadlineMargin = 250 * time.Millisecond
	// journalWriterPacedWait is the client-side wait between GET_LOCK(name, 0)
	// attempts when no whole-second server-side slice fits below the read
	// deadline or the remaining admission time. Context-aware, never a busy
	// spin: each sleep is bounded by cancellation and the remaining budget.
	journalWriterPacedWait = 100 * time.Millisecond
	// journalWriterReleaseTimeout bounds the detached RELEASE_LOCK call, run
	// on its own context so a caller-canceled ctx cannot strand the lock
	// held on a pooled session.
	journalWriterReleaseTimeout = 3 * time.Second
	// JournalWriterAdmissionBudget is the default admission window for one
	// journaled write when no route-specific budget applies: the store
	// funnels' server-mode retry budget and uow's DefaultTxRetryMaxElapsed.
	// This bounds WAITING (admission and its scheduling), never the callback's
	// execution: once admitted, the body runs on its original context so a
	// long successful transaction is not newly canceled at 15 seconds.
	JournalWriterAdmissionBudget = 15 * time.Second
)

// ErrJournalWriterBusy is the named admission outcome for a journaled write
// whose admission window expired while another writer held the journal-writer
// lock. The write's body never ran, so a caller retrying it is safe; the
// HTTP surface maps it to 503 busy with the existing contention Retry-After.
var ErrJournalWriterBusy = errors.New("journal writer lock busy")

// JournalWriterLockName returns the advisory-lock name serializing journal
// writers on database, hashing long database names down to the fnv64a form
// when the readable name would not fit the server's 64-byte GET_LOCK limit —
// the same convention the delete fence and schema.MigrationLockName use.
func JournalWriterLockName(database string) string {
	raw := journalWriterLockPrefix + database
	if len(raw) <= journalWriterLockNameMaxLength {
		return raw
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(database))
	return fmt.Sprintf("%s%016x", journalWriterLockPrefix, h.Sum64())
}

// JournalAdmissionDeadline resolves the absolute admission deadline for one
// journaled operation on ctx: the route-fixed deadline when the entry point
// attached one (withRetryTxOn, uow.RunTxResultWithin — retries must not
// restart the budget), else the earlier of the caller's own deadline and the
// default admission budget. Pool acquisition and every admission wait count
// against it.
func JournalAdmissionDeadline(ctx context.Context) time.Time {
	if d, ok := ctx.Value(journalAdmissionDeadlineKey{}).(time.Time); ok {
		return d
	}
	if d, ok := ctx.Deadline(); ok && time.Until(d) < JournalWriterAdmissionBudget {
		return d
	}
	return time.Now().Add(JournalWriterAdmissionBudget)
}

type journalAdmissionDeadlineKey struct{}

// WithJournalAdmissionDeadline fixes the absolute admission deadline for a
// journaled operation and every retry it makes. Route entry points call it
// once at the top so retries share one budget; admission sites read it back
// with JournalAdmissionDeadline.
func WithJournalAdmissionDeadline(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, journalAdmissionDeadlineKey{}, deadline)
}

// WithRouteJournalAdmission fixes the admission deadline for a route whose
// retry budget is budget: the earlier of the caller's own deadline and
// now+budget, so an operation scheduled close to its deadline never queues for
// a lock it cannot finish under. The store's write funnels and the uow's
// RunTxResultWithin/RunTxScoped call this at entry, once per operation.
func WithRouteJournalAdmission(ctx context.Context, budget time.Duration) context.Context {
	deadline := time.Now().Add(budget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	return WithJournalAdmissionDeadline(ctx, deadline)
}

type unlockedJournalRouteKey struct{}

// WithUnlockedJournalRoute declares ctx a route whose transactions must not
// take the journal-writer admission lock: uow.RunTxRead and uow.RunTxEphemeral
// (reads and dolt_ignored-only writes). With the journal on, such a
// transaction is scoped journal-FORBIDDEN instead (see
// ScopeUnlockedJournalTransaction): an unexpected emit fails before any SQL
// rather than silently racing the admitted writers.
func WithUnlockedJournalRoute(ctx context.Context) context.Context {
	return context.WithValue(ctx, unlockedJournalRouteKey{}, struct{}{})
}

// UnlockedJournalRoute reports whether ctx declared itself a route that must
// not take the journal-writer admission lock.
func UnlockedJournalRoute(ctx context.Context) bool {
	_, ok := ctx.Value(unlockedJournalRouteKey{}).(struct{})
	return ok
}

type advisoryJournalAdmissionKey struct{}

// WithAdvisoryJournalAdmission marks ctx as an admission that must not queue:
// one nonblocking GET_LOCK(name, 0) attempt, and contention answers the named
// busy outcome at once. The defer-wake sweep ahead of ready/claim reads uses
// it, so a ready listing never waits behind a long journaled write — a later
// uncontended sweep performs the wake.
func WithAdvisoryJournalAdmission(ctx context.Context) context.Context {
	return context.WithValue(ctx, advisoryJournalAdmissionKey{}, struct{}{})
}

// AdvisoryJournalAdmission reports whether ctx asked for nonblocking
// journal-writer admission.
func AdvisoryJournalAdmission(ctx context.Context) bool {
	_, ok := ctx.Value(advisoryJournalAdmissionKey{}).(struct{})
	return ok
}

// AcquireJournalWriterLock takes the journal-writer admission lock for
// database on conn — the pinned session that will BEGIN the transaction —
// and returns the release closure. Admission queues with bounded waits until
// deadline (JournalAdmissionDeadline), so a waiter either enters before its
// route's budget expires or fails with ErrJournalWriterBusy; it never
// proceeds unlocked. readTimeout is the owning pool's configured read
// deadline (0 when the pool sets none, as the proxied uow pool does); each
// server-side wait is clamped strictly below it so a contended GET_LOCK
// cannot surface as an i/o timeout.
//
// The lock must precede BEGIN: a transaction that began before the previous
// holder committed still reads the old counter, which is the exact conflict
// this lock exists to prevent. On an uncertain acquisition (query failure or
// NULL) the physical session is DISCARDED through driver.ErrBadConn rather
// than returned to the pool: its lock state is unknown, and a pooled
// pseudo-owner would keep every writer out. Cancellation stays cancellation:
// a ctx that dies during the wait returns its own error, and a grant racing
// cancellation is released before the body can begin.
func AcquireJournalWriterLock(ctx context.Context, conn *sql.Conn, database string, deadline time.Time, readTimeout time.Duration) (func(), error) {
	if AdvisoryJournalAdmission(ctx) {
		return tryAcquireJournalWriterLock(ctx, conn, database)
	}
	lockName := JournalWriterLockName(database)
	start := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, journalWriterBusyError(lockName, database, start)
		}
		wait := min(JournalWriterLockWait, remaining)
		serverSide := time.Duration(0)
		if readTimeout <= 0 || wait+journalWriterReadDeadlineMargin < readTimeout {
			// GET_LOCK's timeout is whole seconds: use one bounded slice when
			// it fits below the read deadline and the remaining budget.
			serverSide = time.Duration(math.Floor(wait.Seconds())) * time.Second
		}
		// Otherwise (or when the slice would round to zero) fall back to
		// GET_LOCK(name, 0) with a paced client-side wait between attempts.
		granted, err := journalWriterGetLock(ctx, conn, lockName, serverSide)
		if err != nil {
			return nil, err
		}
		if !granted {
			if serverSide == 0 {
				pacedWait(ctx, min(journalWriterPacedWait, remaining))
			}
			continue
		}
		// The grant answered, but the caller may have been canceled while the
		// server held the request: re-check before the body can begin, and
		// never convert that cancellation into an unlocked write.
		if err := ctx.Err(); err != nil {
			ReleaseJournalWriterLock(conn, lockName)
			return nil, err
		}
		return journalWriterReleaseOnce(conn, lockName), nil
	}
}

// TryAcquireJournalWriterLock is AcquireJournalWriterLock without queueing:
// one GET_LOCK(name, 0) attempt, granted or busy at once. The advisory
// defer-wake sweep uses it so a ready read never waits on a journaled write.
func TryAcquireJournalWriterLock(ctx context.Context, conn *sql.Conn, database string) (func(), error) {
	return tryAcquireJournalWriterLock(ctx, conn, database)
}

func tryAcquireJournalWriterLock(ctx context.Context, conn *sql.Conn, database string) (func(), error) {
	lockName := JournalWriterLockName(database)
	start := time.Now()
	granted, err := journalWriterGetLock(ctx, conn, lockName, 0)
	if err != nil {
		return nil, err
	}
	if !granted {
		return nil, journalWriterBusyError(lockName, database, start)
	}
	if err := ctx.Err(); err != nil {
		ReleaseJournalWriterLock(conn, lockName)
		return nil, err
	}
	return journalWriterReleaseOnce(conn, lockName), nil
}

// journalWriterGetLock runs one GET_LOCK slice and classifies the answer:
// 1 grants admission, 0 means contention (caller decides to re-wait or go
// busy), and NULL or a query failure is an uncertain acquisition — the
// session is discarded and the error returned, never a possible lock owner
// handed back to the pool.
func journalWriterGetLock(ctx context.Context, conn *sql.Conn, lockName string, wait time.Duration) (bool, error) {
	var granted sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, int(wait.Seconds())).Scan(&granted); err != nil {
		discardSession(conn)
		return false, fmt.Errorf("journal: get writer lock %q: %w", lockName, err)
	}
	if !granted.Valid {
		discardSession(conn)
		return false, fmt.Errorf("journal: get writer lock %q: server returned NULL", lockName)
	}
	return granted.Int64 == 1, nil
}

// ReleaseJournalWriterLock releases a lock acquired on conn's session. It is
// idempotent, runs detached from the caller's ctx on its own bounded deadline,
// and requires RELEASE_LOCK to answer 1: any other result leaves the lock's
// state unknown, so the session is discarded through the driver.ErrBadConn
// disposal pattern (a GET_LOCK lock is session-scoped, and a pooled session
// with an unknown lock would keep every future writer out). A release
// failure is never a write failure: the durable commit already landed, and
// the discarded session is the whole cost.
func ReleaseJournalWriterLock(conn *sql.Conn, lockName string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), journalWriterReleaseTimeout)
	defer cancel()
	var released sql.NullInt64
	err := conn.QueryRowContext(cleanupCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released)
	if err != nil || !released.Valid || released.Int64 != 1 {
		discardSession(conn)
	}
}

// journalWriterReleaseOnce wraps ReleaseJournalWriterLock in a once so every
// exit path of an admitted transaction can call the release closure without
// ordering discipline.
func journalWriterReleaseOnce(conn *sql.Conn, lockName string) func() {
	var once sync.Once
	return func() { once.Do(func() { ReleaseJournalWriterLock(conn, lockName) }) }
}

// discardSession marks the physical connection bad so database/sql closes it
// instead of returning it to the pool. Used whenever the session's lock state
// is unknown (uncertain acquisition, failed release): a pooled session that
// may still hold a GET_LOCK would silently block every future writer.
func discardSession(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

// pacedWait sleeps for at most d or until ctx is done — the context-aware
// wait between GET_LOCK(name, 0) attempts, never a busy spin.
func pacedWait(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func journalWriterBusyError(lockName, database string, start time.Time) error {
	return fmt.Errorf("%w: journal writer lock %q on database %q after waiting %s",
		ErrJournalWriterBusy, lockName, database, time.Since(start).Round(time.Millisecond))
}