package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// publicationOrderDriver is a statement-recording SQL driver for the
// publication-ordering contract (GH#5752). It records every BEGIN, statement
// (with its arguments), COMMIT and ROLLBACK together with the driver
// connection it ran on and whether that connection had an open SQL
// transaction at the time, so a test can prove, over a REAL store method,
// that the method's DOLT_ADD/DOLT_COMMIT ran after the mutation's SQL COMMIT
// and outside any open transaction.
//
// It serves just enough canned rows for the legacy write paths to run: one
// issue row (status/assignee configurable), its claim state, an empty label
// and config set, and a dolt_status count for the staged-set guard. Tests add
// anything else through rows/exec.
type publicationOrderDriver struct {
	mu sync.Mutex

	nextConn int
	open     map[int]bool
	events   []pubEvent

	// issueStatus/issueAssignee shape every canned issue row.
	issueStatus   types.Status
	issueAssignee string
	issueMetadata string
	// wispExists makes every wisp probe find the id (an active wisp).
	wispExists bool
	// stagedCount is what the staged-set guard reads from dolt_status.
	stagedCount int64

	// rows, when set, serves a canned result for a matching query before the
	// defaults.
	rows func(query string, args []driver.NamedValue) (driver.Rows, bool)
	// queryErr, when set, fails a matching query/CALL after it is recorded.
	queryErr func(query string) error
	// execErr, when set, fails a matching Exec after it is recorded.
	execErr func(query string) error
	// commitErr, when set, fails the nth (1-based) SQL COMMIT after it is
	// recorded.
	commitErr func(n int) error
	commits   int
}

type pubEvent struct {
	conn  int
	kind  string // BEGIN, COMMIT, ROLLBACK, EXEC, QUERY
	query string
	args  []any
	inTx  bool
}

func newPublicationOrderDriver() *publicationOrderDriver {
	return &publicationOrderDriver{
		open:        map[int]bool{},
		issueStatus: types.StatusOpen,
		stagedCount: 1,
	}
}

func (d *publicationOrderDriver) store() *DoltStore {
	return &DoltStore{db: sql.OpenDB(d)}
}

func (d *publicationOrderDriver) Connect(context.Context) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextConn++
	return &pubConn{d: d, id: d.nextConn}, nil
}

func (d *publicationOrderDriver) Driver() driver.Driver { return pubDriverShim{} }

type pubDriverShim struct{}

func (pubDriverShim) Open(string) (driver.Conn, error) { return nil, driver.ErrSkip }

func (d *publicationOrderDriver) record(conn int, kind, query string, args []driver.NamedValue) {
	vals := make([]any, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	d.events = append(d.events, pubEvent{conn: conn, kind: kind, query: query, args: vals, inTx: d.open[conn]})
}

type pubConn struct {
	d  *publicationOrderDriver
	id int
}

func (c *pubConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *pubConn) Close() error                        { return nil }
func (c *pubConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *pubConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.record(c.id, "BEGIN", "", nil)
	c.d.open[c.id] = true
	return &pubTx{c: c}, nil
}

func (c *pubConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.record(c.id, "EXEC", query, args)
	if c.d.execErr != nil {
		if err := c.d.execErr(query); err != nil {
			return nil, err
		}
	}
	if strings.Contains(query, "is_blocked") {
		// Derived blocked-state maintenance is a fixpoint loop: report no
		// change so it converges.
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func (c *pubConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.record(c.id, "QUERY", query, args)
	if c.d.queryErr != nil {
		if err := c.d.queryErr(query); err != nil {
			return nil, err
		}
	}
	if c.d.rows != nil {
		if rows, ok := c.d.rows(query, args); ok {
			return rows, nil
		}
	}
	return c.d.defaultRows(query, args), nil
}

func (d *publicationOrderDriver) defaultRows(query string, args []driver.NamedValue) driver.Rows {
	id := "pub-1"
	if len(args) > 0 {
		if s, ok := args[0].Value.(string); ok && s != "" {
			id = s
		}
	}
	switch {
	case strings.Contains(query, "FROM dolt_status"):
		return &valRows{cols: []string{"count"}, vals: [][]driver.Value{{d.stagedCount}}}
	case strings.Contains(query, "SELECT 1 FROM wisps"):
		if d.wispExists {
			return &valRows{cols: []string{"exists"}, vals: [][]driver.Value{{int64(1)}}}
		}
		return &valRows{cols: []string{"exists"}}
	case strings.Contains(query, "SELECT metadata FROM issues WHERE id"):
		metadata := d.issueMetadata
		if metadata == "" {
			metadata = "{}"
		}
		return &valRows{cols: []string{"metadata"}, vals: [][]driver.Value{{metadata}}}
	case strings.Contains(query, "SELECT assignee, status FROM issues"):
		return &valRows{cols: []string{"assignee", "status"}, vals: [][]driver.Value{{d.issueAssignee, string(d.issueStatus)}}}
	case strings.Contains(query, "SELECT is_template, pinned, status, assignee FROM issues"):
		return &valRows{cols: []string{"is_template", "pinned", "status", "assignee"},
			vals: [][]driver.Value{{false, false, string(d.issueStatus), d.issueAssignee}}}
	case strings.Contains(query, "SELECT status FROM issues WHERE id"):
		return &valRows{cols: []string{"status"}, vals: [][]driver.Value{{string(d.issueStatus)}}}
	case strings.Contains(query, "FROM issues") && strings.Contains(query, "LEFT JOIN leases"):
		return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{d.issueValues(id)}}
	case strings.Contains(strings.ToUpper(query), "SELECT COUNT("):
		// Guard counts (open children, open blockers, ...) report none: one
		// zero per aggregate, so multi-COUNT reads scan too.
		n := strings.Count(strings.ToUpper(query), "COUNT(")
		cols := make([]string, n)
		zeros := make([]driver.Value, n)
		for i := range cols {
			cols[i] = fmt.Sprintf("count%d", i)
			zeros[i] = int64(0)
		}
		return &valRows{cols: cols, vals: [][]driver.Value{zeros}}
	case strings.Contains(query, "CALL DOLT_"):
		return &valRows{cols: []string{"status"}}
	default:
		return &seqRows{}
	}
}

func (d *publicationOrderDriver) issueValues(id string) []driver.Value {
	values := claimBoundaryIssueValues(id)
	for i, column := range claimBoundaryIssueColumns() {
		switch column {
		case "status":
			values[i] = string(d.issueStatus)
		case "assignee":
			if d.issueAssignee != "" {
				values[i] = d.issueAssignee
			}
		case "metadata":
			if d.issueMetadata != "" {
				values[i] = d.issueMetadata
			}
		}
	}
	return values
}

type pubTx struct{ c *pubConn }

func (t *pubTx) Commit() error {
	d := t.c.d
	d.mu.Lock()
	defer d.mu.Unlock()
	d.record(t.c.id, "COMMIT", "", nil)
	d.open[t.c.id] = false
	d.commits++
	if d.commitErr != nil {
		return d.commitErr(d.commits)
	}
	return nil
}

func (t *pubTx) Rollback() error {
	d := t.c.d
	d.mu.Lock()
	defer d.mu.Unlock()
	d.record(t.c.id, "ROLLBACK", "", nil)
	d.open[t.c.id] = false
	return nil
}

func (d *publicationOrderDriver) snapshot() []pubEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.events)
}

func (d *publicationOrderDriver) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = nil
	d.commits = 0
}

func isDoltAdd(ev pubEvent) bool    { return strings.Contains(ev.query, "DOLT_ADD") }
func isDoltCommit(ev pubEvent) bool { return strings.Contains(ev.query, "DOLT_COMMIT") }
func isPublication(ev pubEvent) bool {
	return isDoltAdd(ev) || isDoltCommit(ev) || strings.Contains(ev.query, "FROM dolt_status")
}

func formatPubEvents(evs []pubEvent) string {
	var b strings.Builder
	for i, ev := range evs {
		q := ev.query
		if len(q) > 90 {
			q = q[:90] + "…"
		}
		q = strings.Join(strings.Fields(q), " ")
		fmt.Fprintf(&b, "\n  %3d c%d tx=%v %s %s %v", i, ev.conn, ev.inTx, ev.kind, q, ev.args)
	}
	return b.String()
}

// publication is what a method published after its mutation, as recorded.
type publication struct {
	tables  []string
	message string
}

// mutationCommitIndex returns the index of the COMMIT that ended the SQL
// transaction in which a statement matching mutation ran, or -1. It identifies
// the mutation transaction by its write rather than assuming the first or last
// COMMIT is it, so verification and recheck transactions do not stand in.
func mutationCommitIndex(evs []pubEvent, mutation func(pubEvent) bool) int {
	for i, ev := range evs {
		if !ev.inTx || !mutation(ev) {
			continue
		}
		for j := i + 1; j < len(evs); j++ {
			if evs[j].conn != ev.conn {
				continue
			}
			switch evs[j].kind {
			case "COMMIT":
				return j
			case "ROLLBACK":
				j = len(evs) // this attempt rolled back; look for a later one
			}
		}
	}
	return -1
}

// requirePublishedAfterCommit asserts the post-commit publication contract on
// recorded events: every DOLT_ADD and DOLT_COMMIT ran with no SQL transaction
// open on its connection, after the mutation's SQL COMMIT, on one connection;
// and it returns what was published.
func requirePublishedAfterCommit(t *testing.T, name string, evs []pubEvent, mutation func(pubEvent) bool) publication {
	t.Helper()
	commitIdx := mutationCommitIndex(evs, mutation)
	if commitIdx < 0 {
		t.Fatalf("%s: no committed SQL transaction carried the mutation; events:%s", name, formatPubEvents(evs))
	}
	var pub publication
	pubConn := 0
	sawCommit := false
	for i, ev := range evs {
		if !isDoltAdd(ev) && !isDoltCommit(ev) {
			continue
		}
		if ev.inTx {
			t.Errorf("%s: %s ran INSIDE an open SQL transaction (event %d) — lost-update ordering hazard (GH#5752); events:%s",
				name, strings.Fields(ev.query)[1], i, formatPubEvents(evs))
			return pub
		}
		if i < commitIdx {
			t.Errorf("%s: %s ran at event %d, before the mutation's SQL COMMIT at %d; events:%s",
				name, strings.Fields(ev.query)[1], i, commitIdx, formatPubEvents(evs))
			return pub
		}
		if pubConn == 0 {
			pubConn = ev.conn
		} else if ev.conn != pubConn {
			t.Errorf("%s: publication statement at event %d ran on connection %d, want the pinned connection %d; events:%s",
				name, i, ev.conn, pubConn, formatPubEvents(evs))
		}
		if isDoltAdd(ev) {
			if len(ev.args) == 1 {
				pub.tables = append(pub.tables, fmt.Sprint(ev.args[0]))
			}
			continue
		}
		sawCommit = true
		if len(ev.args) > 0 {
			pub.message = fmt.Sprint(ev.args[0])
		}
	}
	if len(pub.tables) == 0 || !sawCommit {
		t.Fatalf("%s: no DOLT_ADD/DOLT_COMMIT publication recorded; events:%s", name, formatPubEvents(evs))
	}
	sort.Strings(pub.tables)
	return pub
}

// requireNoPublication asserts that no DOLT_ADD or DOLT_COMMIT was issued.
func requireNoPublication(t *testing.T, name string, evs []pubEvent) {
	t.Helper()
	for i, ev := range evs {
		if isDoltAdd(ev) || isDoltCommit(ev) {
			t.Fatalf("%s: want no publication, got %q at event %d; events:%s", name, ev.query, i, formatPubEvents(evs))
		}
	}
}

func countEvents(evs []pubEvent, pred func(pubEvent) bool) int {
	n := 0
	for _, ev := range evs {
		if pred(ev) {
			n++
		}
	}
	return n
}

func sortedCopy(tables ...string) []string {
	out := slices.Clone(tables)
	sort.Strings(out)
	return out
}

func requirePublication(t *testing.T, name string, got publication, wantTables []string, wantMessage string) {
	t.Helper()
	if !slices.Equal(got.tables, sortedCopy(wantTables...)) {
		t.Errorf("%s: staged tables = %v, want %v", name, got.tables, sortedCopy(wantTables...))
	}
	if got.message != wantMessage {
		t.Errorf("%s: commit message = %q, want %q", name, got.message, wantMessage)
	}
}

// writesTo matches a statement that writes the given table.
func writesTo(table string) func(pubEvent) bool {
	return func(ev pubEvent) bool {
		q := strings.ToUpper(strings.Join(strings.Fields(ev.query), " "))
		t := strings.ToUpper(table)
		return strings.Contains(q, "UPDATE "+t+" ") ||
			strings.Contains(q, "INSERT INTO "+t+" ") || strings.Contains(q, "INSERT INTO "+t+"(") ||
			strings.Contains(q, "INSERT IGNORE INTO "+t+" ") ||
			strings.Contains(q, "DELETE FROM "+t+" ")
	}
}

var errPubInjected = errors.New("boom: injected publication failure")

var _ driver.Connector = (*publicationOrderDriver)(nil)
var _ driver.ExecerContext = (*pubConn)(nil)
var _ driver.QueryerContext = (*pubConn)(nil)
var _ driver.ConnBeginTx = (*pubConn)(nil)
var _ = io.EOF
