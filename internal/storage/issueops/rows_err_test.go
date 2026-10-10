package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var errMidIteration = errors.New("connection lost mid-iteration")

type truncatingDriver struct {
	mu    sync.Mutex
	after int
}

func (d *truncatingDriver) Open(string) (driver.Conn, error) { return &truncatingConn{drv: d}, nil }

type truncatingConn struct{ drv *truncatingDriver }

func (c *truncatingConn) Prepare(query string) (driver.Stmt, error) {
	return &truncatingStmt{drv: c.drv, query: query}, nil
}
func (c *truncatingConn) Close() error              { return nil }
func (c *truncatingConn) Begin() (driver.Tx, error) { return truncatingTx{}, nil }

type truncatingTx struct{}

func (truncatingTx) Commit() error   { return nil }
func (truncatingTx) Rollback() error { return nil }

type truncatingStmt struct {
	drv   *truncatingDriver
	query string
}

func (s *truncatingStmt) Close() error  { return nil }
func (s *truncatingStmt) NumInput() int { return -1 }
func (s *truncatingStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (s *truncatingStmt) Query([]driver.Value) (driver.Rows, error) {
	return &truncatingRows{drv: s.drv, cols: columnsFor(s.query)}, nil
}

// truncatingRows hands back one row and then reports a transport failure, which
// is what a dropped connection looks like to database/sql: Next returns an
// error that only surfaces through Rows.Err.
type truncatingRows struct {
	drv  *truncatingDriver
	cols []string
	sent int
}

func (r *truncatingRows) Columns() []string { return r.cols }
func (r *truncatingRows) Close() error      { return nil }

func (r *truncatingRows) Next(dest []driver.Value) error {
	r.drv.mu.Lock()
	limit := r.drv.after
	r.drv.mu.Unlock()

	if r.sent >= limit {
		return errMidIteration
	}
	r.sent++
	for i := range dest {
		dest[i] = "x"
	}
	return nil
}

// columnsFor gives each query as many string columns as its SELECT list needs.
func columnsFor(query string) []string {
	n := strings.Count(strings.ToLower(query), ",") + 1
	if n > 8 {
		n = 8
	}
	cols := make([]string, n)
	for i := range cols {
		cols[i] = "c"
	}
	return cols
}

// sql.Register panics on a name it has already seen, which a -count=2 run of
// this test would otherwise hit.
var truncatingDriverSeq atomic.Int64

func truncatingDB(t *testing.T, rowsBeforeFailure int) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("truncating-%d", truncatingDriverSeq.Add(1))
	sql.Register(name, &truncatingDriver{after: rowsBeforeFailure})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A transport failure part-way through a result set makes Rows.Next stop
// without reporting anything: the loop ends as if the rows had run out, and
// only Rows.Err tells the two apart. Both readers below return straight after
// their loop, so with the error swallowed they answer from a truncated child
// set and report success.
func TestRowIterationReportsMidIterationFailure(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		call func(tx *sql.Tx) error
	}{
		{
			name: "GetEpicsEligibleForClosureInTx",
			call: func(tx *sql.Tx) error {
				_, err := GetEpicsEligibleForClosureInTx(ctx, tx)
				return err
			},
		},
		{
			name: "GetMoleculeLastActivityInTx",
			call: func(tx *sql.Tx) error {
				_, err := GetMoleculeLastActivityInTx(ctx, tx, "bd-1")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := truncatingDB(t, 1)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()

			err = tt.call(tx)
			if err == nil {
				t.Fatal("iteration failed part-way through and no error was returned: " +
					"the caller cannot tell a truncated result from a complete one")
			}
			if !strings.Contains(err.Error(), errMidIteration.Error()) {
				t.Fatalf("error does not carry the transport failure: %v", err)
			}
		})
	}
}
