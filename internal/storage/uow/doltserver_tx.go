package uow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

type doltServerTx struct {
	conn *sql.Conn
	done bool
}

var _ Tx = (*doltServerTx)(nil)

func (t *doltServerTx) Runner() db.Runner {
	return t.conn
}

func (t *doltServerTx) Commit(ctx context.Context, message string) error {
	if t.done {
		return errors.New("uow: commit: already done")
	}
	// An empty message selects the EPHEMERAL commit form (bd-aq0ql): a plain
	// SQL COMMIT persists the transaction's writes into the working set
	// without minting a Dolt commit or history. This exists for work that
	// touches ONLY dolt_ignored state — today the leases table (bd-lrgn1),
	// whose heartbeats must never create commits — and is only reachable via
	// uow.RunTxEphemeral: RunTx/RunTxResult treat an empty commitMsg as
	// "nothing to commit" and never call Commit at all.
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
		pending, perr := issueops.HasPendingChanges(ctx, t.conn)
		if perr != nil {
			if isSerializationError(perr) {
				// As below: the server already rolled the transaction back and
				// the caller retries the whole unit of work, so leave the
				// pinned session in place for the retry.
				return perr
			}
			// A failed status check leaves the transaction open on the pinned
			// session, exactly like a failed DOLT_COMMIT below: roll back
			// before release, or poison the connection.
			return t.closeOpenTxAfterFailure(ctx, perr)
		}
		if !pending {
			stmt, args = "COMMIT;", nil
		}
	}
	_, err := t.conn.ExecContext(ctx, stmt, args...)
	if err == nil {
		t.done = true
		t.releaseConn()
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
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
}

// poisonConn discards the pinned session instead of returning it to the pool.
// A session whose transaction may still be open must never be reused: because
// go-sql-driver's ResetSession only performs a liveness check (no
// COM_RESET_CONNECTION), the next borrower's implicit START TRANSACTION would
// commit the orphaned writes. Returning driver.ErrBadConn from Raw makes
// database/sql close the connection and drop it from the pool.
func (t *doltServerTx) poisonConn() {
	if t.conn == nil {
		return
	}
	_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = t.conn.Close()
	t.conn = nil
}
