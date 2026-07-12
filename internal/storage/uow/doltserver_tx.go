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
	if err != nil {
		// Leave the transaction open so Close can roll it back with its bounded,
		// cancellation-independent cleanup context. Fresh-UOW retries always open
		// a new unit of work; commit-only retry on this session is not used.
		return err
	}
	t.done = true
	t.releaseConn()
	return nil
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

// discardSQLConn marks a pinned database/sql connection bad before closing its
// handle. Returning driver.ErrBadConn from Raw makes database/sql close the
// physical driver connection instead of putting a potentially dirty session
// back in the pool.
func discardSQLConn(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}
