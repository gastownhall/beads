package uow

import (
	"context"
	"database/sql"
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
	t.done = true
	defer t.releaseConn()

	// Skip the no-op COMMIT that floods the Dolt log and burns server CPU when
	// an idempotent write staged nothing — e.g. a same-value REPLACE INTO
	// metadata, or a same-actor re-claim whose CAS UPDATE matched 0 rows. The
	// gascity supervisor's desired-state reconciler re-applies unchanged
	// session-bead state on every tick, so without this guard each such tick
	// issues START TRANSACTION + an empty DOLT_COMMIT('-Am') that Dolt rejects
	// server-side with "nothing to commit" (the work still succeeds; the COMMIT
	// round-trip is pure wasted churn).
	//
	// DOLT_COMMIT('-Am') stages-and-commits the whole working set atomically, so
	// nothing is pre-staged at this point. The correct gate is therefore the
	// global working-set check (HasPendingChanges, which mirrors what '-Am' will
	// sweep up and excludes dolt_ignore'd tables), NOT a staged-set count —
	// that would always read 0 here and wrongly skip every real write. This
	// connection is pinned to a single Dolt session (BeginTx pins t.conn and
	// runs START TRANSACTION), so dolt_status reflects only this UOW's changes;
	// the check is race-free with concurrent sessions.
	pending, err := issueops.HasPendingChanges(ctx, t.conn)
	if err != nil {
		return err
	}
	if !pending {
		return nil
	}

	if _, err := t.conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?);", message); err != nil {
		// Belt-and-suspenders: Dolt can still report "nothing to commit" for a
		// working set that dolt_status counted as dirty (e.g. a row rewritten to
		// its existing value). That is benign — swallow it rather than surface a
		// non-error as an error.
		if issueops.IsNothingToCommitError(err) {
			return nil
		}
		return err
	}
	return nil
}

func (t *doltServerTx) Rollback(ctx context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	defer t.releaseConn()
	_, err := t.conn.ExecContext(ctx, "ROLLBACK;")
	return err
}

func (t *doltServerTx) RollbackUnlessCommitted(ctx context.Context) {
	if !t.done {
		_ = t.Rollback(ctx)
	}
}

func (t *doltServerTx) releaseConn() {
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
}
