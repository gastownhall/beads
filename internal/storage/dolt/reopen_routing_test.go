package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// reopenPromotionDriver models the promotion seam: a pre-transaction probe
// finds an active wisp, while the write transaction sees the same ID only in
// the permanent issues table.
type reopenPromotionDriver struct {
	mu sync.Mutex

	inTx             bool
	preTxWispProbe   bool
	doltAddTables    []string
	doltCommitCalled bool
	issueUpdated     bool
}

func (d *reopenPromotionDriver) Open(string) (driver.Conn, error) {
	return &reopenPromotionConn{driver: d}, nil
}

func (d *reopenPromotionDriver) Connect(context.Context) (driver.Conn, error) {
	return &reopenPromotionConn{driver: d}, nil
}

func (d *reopenPromotionDriver) Driver() driver.Driver { return d }

type reopenPromotionConn struct {
	driver *reopenPromotionDriver
}

func (c *reopenPromotionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("reopen promotion driver does not prepare statements")
}

func (c *reopenPromotionConn) Close() error { return nil }

func (c *reopenPromotionConn) Begin() (driver.Tx, error) {
	c.driver.mu.Lock()
	c.driver.inTx = true
	c.driver.mu.Unlock()
	return &reopenPromotionTx{driver: c.driver}, nil
}

func (c *reopenPromotionConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	if strings.Contains(query, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1") {
		if !c.driver.inTx {
			c.driver.preTxWispProbe = true
			return &reopenPromotionRows{columns: []string{"exists"}, values: [][]driver.Value{{int64(1)}}}, nil
		}
		return &reopenPromotionRows{columns: []string{"exists"}}, nil
	}
	if strings.Contains(query, "SELECT status FROM issues WHERE id = ?") {
		return &reopenPromotionRows{
			columns: []string{"status"},
			values:  [][]driver.Value{{string("closed")}},
		}, nil
	}
	if strings.Contains(query, "FROM dolt_status") {
		// Empty-commit guard (GH#4288 re-port): report one staged row so the
		// commit path under test still reaches DOLT_COMMIT.
		return &reopenPromotionRows{columns: []string{"count"}, values: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(query, "CALL DOLT_ADD") && len(args) == 1 {
		if table, ok := args[0].Value.(string); ok {
			c.driver.doltAddTables = append(c.driver.doltAddTables, table)
		}
	}
	if strings.Contains(query, "CALL DOLT_COMMIT") {
		c.driver.doltCommitCalled = true
	}
	return &reopenPromotionRows{columns: []string{"value"}}, nil
}

func (c *reopenPromotionConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	if strings.Contains(query, "UPDATE issues") && strings.Contains(query, "SET status") {
		c.driver.issueUpdated = true
		return driver.RowsAffected(1), nil
	}
	return driver.RowsAffected(0), nil
}

type reopenPromotionTx struct {
	driver *reopenPromotionDriver
}

func (t *reopenPromotionTx) Commit() error {
	t.driver.mu.Lock()
	t.driver.inTx = false
	t.driver.mu.Unlock()
	return nil
}

func (t *reopenPromotionTx) Rollback() error {
	t.driver.mu.Lock()
	t.driver.inTx = false
	t.driver.mu.Unlock()
	return nil
}

type reopenPromotionRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *reopenPromotionRows) Columns() []string { return r.columns }
func (r *reopenPromotionRows) Close() error      { return nil }
func (r *reopenPromotionRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

var _ driver.Connector = (*reopenPromotionDriver)(nil)

func TestReopenIssuePromotionRaceUsesTransactionRouting(t *testing.T) {
	routingDriver := &reopenPromotionDriver{}
	store := &DoltStore{db: sql.OpenDB(routingDriver)}
	defer func() { _ = store.db.Close() }()

	if err := store.ReopenIssue(context.Background(), "promoted-wisp", "", "tester"); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}

	routingDriver.mu.Lock()
	defer routingDriver.mu.Unlock()
	if routingDriver.preTxWispProbe {
		t.Fatal("reopen probed wisp routing before its write transaction")
	}
	if !routingDriver.issueUpdated {
		t.Fatal("promoted permanent issue was not updated")
	}
	if got, want := routingDriver.doltAddTables, []string{"issues", "events"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DOLT_ADD tables = %v, want %v for promoted permanent issue", got, want)
	}
	if !routingDriver.doltCommitCalled {
		t.Fatal("promoted permanent issue did not create a Dolt commit")
	}
}
