package uow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

type doltServerTx struct {
	conn *sql.Conn
	done bool
	// clearJournalScope releases the events-journal activation BeginTx bound to
	// conn. It is called from the two places the connection leaves this tx —
	// releaseConn and poisonConn — so the activation entry cannot outlive the
	// transaction it describes, whichever way the transaction ends.
	clearJournalScope func()
	// clearVersionScope is clearJournalScope's counterpart for dual-write
	// issue-version history: same binding at BeginTx, same release at
	// releaseConn/poisonConn.
	clearVersionScope func()
	// clearRecheckScope is the same binding for the dependents this
	// transaction's unblocking writes record for the post-commit
	// blocked-state recheck (gastownhall/beads#6716).
	clearRecheckScope func()
	// unlockJournal releases the journal writer lock BeginTx took on conn (a
	// no-op when it took none). A successful commit runs it before the
	// post-commit recheck; both connection-release paths run it too, before
	// the connection leaves this tx.
	unlockJournal func()
}

var _ Tx = (*doltServerTx)(nil)

func (t *doltServerTx) Runner() db.Runner {
	return t.conn
}

func (t *doltServerTx) Commit(ctx context.Context, message string) error {
	if t.done {
		return errors.New("uow: commit: already done")
	}
	stmt, args, err := t.commitStatement(ctx, message)
	if err != nil {
		if isSerializationError(err) {
			// As below: the server already rolled the transaction back and
			// the caller retries the whole unit of work, so leave the
			// pinned session in place for the retry.
			return err
		}
		// A failed status check leaves the transaction open on the pinned
		// session, exactly like a failed DOLT_COMMIT below: roll back
		// before release, or poison the connection.
		return t.closeOpenTxAfterFailure(ctx, err)
	}
	_, err = t.conn.ExecContext(ctx, stmt, args...)
	if err == nil {
		t.done = true
		// The write is durable, so the journal writer lock has done its job:
		// release it before the recheck, as the dolt store does, so no
		// journaled writer queues behind the recheck's retries. A release
		// that fails discards the session, and the recheck then fails and
		// is reported like any other.
		t.releaseJournalLock()
		// Settle the dependents its unblocking writes recorded on a fresh
		// snapshot before the pinned session goes back to the pool
		// (gastownhall/beads#6716). A recheck failure is logged, never
		// returned: the caller must not read a committed write as failed and
		// replay it.
		if t.recheckBlockedAfterCommit(ctx, issueops.TakeBlockedRecheck(t.conn)) {
			t.releaseConn()
		} else {
			t.poisonConn()
		}
		return nil
	}
	if isSerializationError(err) {
		// Serialization failures guarantee the transaction was already rolled
		// back and the caller retries them, so leave the pinned session in place
		// for the retry rather than tearing it down here.
		return err
	}
	// A non-serialization DOLT_COMMIT failure leaves the transaction open on the
	// pinned session. Roll it back before releasing the connection so the next
	// borrower cannot inherit and implicitly commit the orphaned writes. If the
	// rollback also fails the session state is unknown, so poison the connection
	// and let the pool discard it instead of handing it out again.
	return t.closeOpenTxAfterFailure(ctx, err)
}

const (
	// scopedCommitMaxElapsed bounds the retries of CommitScoped's Dolt commit.
	// It is short for the reason the DoltStore's postTxCommitMaxElapsed is:
	// the failure is logged rather than returned, so a long fight for a commit
	// the next one on the branch carries anyway buys nothing.
	scopedCommitMaxElapsed = 5 * time.Second
	// scopedCommitGrace pads the detached context past the retry budget. The
	// budget only stops scheduling attempts; the deadline is what cancels one
	// already hung in flight.
	scopedCommitGrace = 2 * time.Second
)

// CommitScoped commits the transaction and then makes a Dolt commit of
// exactly tables (plus the versioned-history tables when this transaction
// could have minted) named message: DOLT_ADD per table, then
// DOLT_COMMIT('-m') — never '-Am', so another writer's uncommitted change
// elsewhere in the working set is neither committed nor attributed to this
// message. With nothing staged, or under a deferred version commit
// (dolt.auto-commit batch/off), there is no Dolt commit.
//
// THE DOLT COMMIT RUNS AFTER THE SQL COMMIT, never inside the transaction:
// the ordering contract of the DoltStore funnel (runIssueOperationTxWithMessage,
// LatentLabsSpace/NEXUS#92). DOLT_ADD stages whole tables from the session's
// transaction root, so staging inside the open transaction builds the Dolt
// commit from its BEGIN-time snapshot and can write rows a concurrent writer
// committed in the meantime back to their old values. Committed first, this
// transaction's writes go through Dolt's commit-time merge with those
// writers, and the DOLT_ADD on the same pinned session, now outside any
// transaction, stages the merged state.
//
// A serialization failure of the SQL COMMIT is returned for the caller to redo
// the whole unit of work, as Commit's is. Once the SQL COMMIT succeeds the
// write is durable, so the Dolt commit after it never fails CommitScoped (see
// commitStagedTables).
func (t *doltServerTx) CommitScoped(ctx context.Context, tables []string, message string) error {
	if t.done {
		return errors.New("uow: commit: already done")
	}
	if message == "" || len(tables) == 0 || issueops.VersionCommitDeferred(ctx) {
		return t.Commit(ctx, "")
	}
	// Resolved before the commit, while the versioned-history scope is bound
	// to the pinned session.
	staged := append([]string(nil), tables...)
	if issueops.VersionedHistoryEnabledFor(t.conn) {
		for _, table := range issueops.VersionedHistoryStagedTables() {
			if !slices.Contains(staged, table) {
				staged = append(staged, table)
			}
		}
	}
	if _, err := t.conn.ExecContext(ctx, "COMMIT;"); err != nil {
		if isSerializationError(err) {
			return err
		}
		return t.closeOpenTxAfterFailure(ctx, err)
	}
	t.done = true
	clean := t.commitStagedTables(ctx, staged, message)
	if t.recheckBlockedAfterCommit(ctx, issueops.TakeBlockedRecheck(t.conn)) && clean {
		t.releaseConn()
	} else {
		t.poisonConn()
	}
	return nil
}

// commitStagedTables is CommitScoped's Dolt commit, made on the pinned session
// after the transaction committed. It runs detached from the caller's
// cancellation on its own short budget, so a deadline landing after the SQL
// COMMIT does not skip it, and replays only the conflicts that leave nothing
// behind (serialization failures and Dolt's @@autocommit rollback, which is
// how a conflict surfaces outside a transaction), so re-staging and
// committing again is safe. That is doltAddAndCommitPostTx's retry.
//
// A failure is logged, never returned: the write is durable, and an error
// would make RunTxScoped replay a committed unit of work, which for a
// heartbeat re-arm then reports a refresh. The change rides the next Dolt
// commit on the branch, as a failed post-transaction commit does in the
// DoltStore funnel. Unlike the funnel, this leg counts nothing: uow records
// no metrics, so bd.db.post_tx_commit_dropped, the funnel's counter for the
// same drop, never sees one made here, and the log line is its only signal.
// It reports whether the session may go back to the pool; after a failure its
// state is unknown (a statement cut off mid-flight, or under @@autocommit=0
// the implicit transaction a DOLT_ADD opened), so it may not.
func (t *doltServerTx) commitStagedTables(ctx context.Context, tables []string, message string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scopedCommitMaxElapsed+scopedCommitGrace)
	defer cancel()

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = txRetryInitialInterval
	bo.MaxElapsedTime = scopedCommitMaxElapsed
	var lastErr error
	err := backoff.Retry(func() error {
		err := t.stageAndCommitOnce(ctx, tables, message)
		if err == nil {
			return nil
		}
		lastErr = err
		if isSerializationError(err) || isDoltAutocommitRollbackError(err) {
			return err
		}
		return backoff.Permanent(err)
	}, backoff.WithContext(bo, ctx))
	if err != nil && lastErr != nil && !errors.Is(err, lastErr) {
		// The deadline expiring between attempts returns the bare context
		// error; keep the last real failure, the only diagnostic of a dropped
		// commit.
		err = fmt.Errorf("%w (last attempt: %w)", err, lastErr)
	}
	if err != nil {
		log.Printf("warning: uow: dolt commit %q failed after its SQL commit (data already committed; change rides the next dolt commit): %v", message, err)
		return false
	}
	return true
}

// stageAndCommitOnce is one attempt at commitStagedTables: stage each table,
// then commit the staged set alone.
func (t *doltServerTx) stageAndCommitOnce(ctx context.Context, tables []string, message string) error {
	for _, table := range tables {
		if err := schema.DrainCall(ctx, t.conn, "CALL DOLT_ADD(?)", table); err != nil {
			return fmt.Errorf("dolt add %s: %w", table, err)
		}
	}
	// Sessions on one branch share the working set, so a concurrent writer's
	// Dolt commit can already have taken this transaction's rows under its
	// own message, leaving nothing staged here: skip the empty commit Dolt
	// would refuse. The same race between this check and the commit surfaces
	// as "nothing to commit", which here means exactly that and not a lost
	// write (the SQL COMMIT has landed), so it is no failure either; it is
	// logged, as the DoltStore funnel logs it, so the missing audit line is
	// explicable.
	staged, err := issueops.HasStagedChanges(ctx, t.conn)
	if err != nil {
		return err
	}
	if !staged {
		return nil
	}
	err = schema.DrainCall(ctx, t.conn, "CALL DOLT_COMMIT('-m', ?)", message)
	if err != nil {
		if !issueops.IsNothingToCommitError(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		log.Printf("uow: dolt commit %q made no dolt commit (nothing to commit): the staged change was most likely absorbed into a concurrent writer's commit (data already committed)", message)
	}
	return nil
}

// commitStatement picks the statement that ends the open transaction on the
// pinned session for a commit named message.
func (t *doltServerTx) commitStatement(ctx context.Context, message string) (string, []interface{}, error) {
	// dolt.auto-commit=batch/off (GH#4995): defer the Dolt version commit to a
	// later explicit commit point (bd dolt commit), the same as the embedded
	// and direct SQL-server backends already do at their own commit sites
	// (bd-4wamg) — persist the write into the working set via a plain SQL
	// COMMIT below, without minting Dolt history, regardless of what message
	// the caller named. issueOpsContext sets this on the context before the
	// proxied CLI dispatch opens its unit of work.
	if message != "" && issueops.VersionCommitDeferred(ctx) {
		message = ""
	}
	// An empty message selects the EPHEMERAL commit form (bd-aq0ql): a plain
	// SQL COMMIT persists the transaction's writes into the working set
	// without minting a Dolt commit or history. It has these entry points:
	//
	//   - uow.RunTxEphemeral, for work that touches ONLY dolt_ignored state.
	//     RunTx/RunTxResult treat an empty commitMsg as "nothing to commit" and
	//     never call Commit at all, so they cannot reach it that way.
	//   - uow.RunTxScoped, for an attempt that names no tables — a lease
	//     refresh (bd-lrgn1), which writes only the dolt_ignored leases table
	//     and must never create a commit. A heartbeat that re-arms names its
	//     tables instead and takes the scoped commit.
	//   - RunTx/RunTxResult under a deferred-version-commit context, via the
	//     blanking just above (GH#4995). There the writes are ordinary versioned
	//     rows and the ONLY-dolt_ignored-state rationale does not apply: the
	//     working set is meant to hold them until the explicit flush point.
	stmt, args := "CALL DOLT_COMMIT('-Am', ?);", []interface{}{message}
	if message == "" {
		stmt, args = "COMMIT;", nil
	} else {
		// Skip the guaranteed-empty DOLT_COMMIT when an idempotent write staged
		// nothing — e.g. a same-value REPLACE INTO metadata, or a re-claim whose
		// CAS UPDATE matched 0 rows, re-applied per-tick by orchestrators
		// converging desired state. Dolt rejects such empty commits server-side
		// with "nothing to commit", flooding the server log (observed ~99% of
		// log lines on a busy coordination DB) and burning CPU evaluating them.
		//
		// DOLT_COMMIT('-Am') stages-and-commits the whole working set, so
		// nothing is pre-staged at this point: the correct gate is the global
		// working-set check (HasPendingChanges, which mirrors what '-Am' sweeps
		// up and excludes dolt_ignore'd tables), NOT a staged-set count — that
		// would always read 0 here and wrongly skip every real write. This
		// connection is pinned to a single Dolt session (BeginTx pins t.conn
		// and runs START TRANSACTION), so dolt_status reflects only this UOW's
		// changes.
		//
		// The skip must still CLOSE the open transaction before the pinned
		// connection is released — releasing with START TRANSACTION open hands
		// the next borrower an implicit commit of orphaned state — so it
		// demotes the statement to the plain-COMMIT (ephemeral) form above
		// rather than returning early: the SQL transaction commits, persisting
		// any dolt_ignored writes into the working set, without minting a Dolt
		// commit.
		//
		// Deliberately NOT ported from the original fix (#4348, 3bd52c27f): the
		// swallow of a residual "nothing to commit" error from DOLT_COMMIT
		// itself. RunTx already swallows that at the call site (tx.go), and the
		// uow layer lets it surface as a lost-update signal
		// (lostupdate_dolt_test.go) — swallowing it a layer down could mask a
		// silently lost write.
		pending, err := issueops.HasPendingChanges(ctx, t.conn)
		if err != nil {
			return "", nil, err
		}
		if !pending {
			stmt, args = "COMMIT;", nil
		}
	}
	return stmt, args, nil
}

// closeOpenTxAfterFailure finishes a Commit attempt that failed with the
// transaction still open on the pinned session: roll it back before releasing
// the connection so the next borrower cannot inherit and implicitly commit the
// orphaned writes, and poison the connection (pool discard) when even the
// rollback fails and the session state is unknown. Serialization failures are
// the caller's to handle first — those guarantee a server-side rollback and
// must leave the pinned session in place for the retry.
func (t *doltServerTx) closeOpenTxAfterFailure(ctx context.Context, err error) error {
	t.done = true
	if rbErr := t.rollbackConn(ctx); rbErr != nil {
		t.poisonConn()
	} else {
		t.releaseConn()
	}
	return err
}

func (t *doltServerTx) Rollback(ctx context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	err := t.rollbackConn(ctx)
	if err != nil {
		t.poisonConn()
	} else {
		t.releaseConn()
	}
	return err
}

func (t *doltServerTx) RollbackUnlessCommitted(ctx context.Context) {
	if !t.done {
		_ = t.Rollback(ctx)
	}
}

func (t *doltServerTx) rollbackConn(ctx context.Context) error {
	if t.conn == nil {
		return nil
	}
	_, err := t.conn.ExecContext(ctx, "ROLLBACK;")
	return err
}

func (t *doltServerTx) releaseConn() {
	t.releaseJournalScope()
	t.releaseVersionScope()
	t.releaseRecheckScope()
	t.releaseJournalLock()
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
}

// releaseJournalScope drops this transaction's events-journal activation entry.
// Idempotent: both connection-release paths call it, and Rollback may follow a
// failed Commit that already released.
func (t *doltServerTx) releaseJournalScope() {
	if t.clearJournalScope != nil {
		t.clearJournalScope()
		t.clearJournalScope = nil
	}
}

// releaseVersionScope is releaseJournalScope's counterpart for dual-write
// issue-version history. Idempotent for the same reason.
func (t *doltServerTx) releaseVersionScope() {
	if t.clearVersionScope != nil {
		t.clearVersionScope()
		t.clearVersionScope = nil
	}
}

// releaseRecheckScope is releaseJournalScope's counterpart for the
// blocked-recheck scope. Idempotent for the same reason.
func (t *doltServerTx) releaseRecheckScope() {
	if t.clearRecheckScope != nil {
		t.clearRecheckScope()
		t.clearRecheckScope = nil
	}
}

// releaseJournalLock releases the journal writer lock, if BeginTx took one.
// Idempotent for the same reason as releaseJournalScope. The release is
// bounded, and when it fails or times out it discards the session itself
// (issueops.LockJournalWriter), so a session still owning the lock never goes
// back to the pool; closing the session releases its locks server-side.
func (t *doltServerTx) releaseJournalLock() {
	if t.unlockJournal != nil {
		t.unlockJournal()
		t.unlockJournal = nil
	}
}

// poisonConn discards the pinned session instead of returning it to the pool.
// A session whose transaction may still be open must never be reused: because
// go-sql-driver's ResetSession only performs a liveness check (no
// COM_RESET_CONNECTION), the next borrower's implicit START TRANSACTION would
// commit the orphaned writes. Returning driver.ErrBadConn from Raw makes
// database/sql close the connection and drop it from the pool.
func (t *doltServerTx) poisonConn() {
	t.releaseJournalScope()
	t.releaseVersionScope()
	t.releaseRecheckScope()
	t.releaseJournalLock()
	if t.conn == nil {
		return
	}
	_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = t.conn.Close()
	t.conn = nil
}
