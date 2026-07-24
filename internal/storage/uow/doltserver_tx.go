package uow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/steveyegge/beads/internal/storage/domain/db"
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
	_, err := t.conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?);", message)
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
