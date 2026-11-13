package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// The tests in this file force a create's retry replay deterministically
// (GH#7289). The store-under-test pool is swapped for one whose driver
// connections wrap every transaction: the first commit of a transaction that
// wrote an issues/wisps row instead rolls that transaction back for real,
// runs the test's competitor (on the store's ORIGINAL pool, committed), and
// returns a 1213 serialization failure, which withRetryTx replays. Later
// commits pass through. A replay is then observed as a second committed
// create transaction, and every outcome is read back from committed rows.

// issueRowInsert matches the issue-row write of a create (helpers.go
// executeIssueInsert), on either plane.
var issueRowInsert = regexp.MustCompile("(?is)\\bINSERT\\s+INTO\\s+`?(issues|wisps)`?\\s*\\(")

// replayFault is the armed fault and what it observed.
type replayFault struct {
	mu sync.Mutex
	// remaining is how many create commits still fail.
	remaining int
	// onFault runs after the real rollback and before the 1213 is returned;
	// its error is returned from the commit instead, so the create fails
	// permanently and the test sees it rather than a masked retry.
	onFault func(attempt int) error
	// fired counts faults injected; createCommits counts create
	// transactions that committed for real.
	fired         int
	createCommits int
}

func (f *replayFault) commitCreate(tx driver.Tx) error {
	f.mu.Lock()
	inject := f.remaining > 0
	if inject {
		f.remaining--
		f.fired++
	}
	attempt := f.fired
	onFault := f.onFault
	f.mu.Unlock()

	if !inject {
		err := tx.Commit()
		if err == nil {
			f.mu.Lock()
			f.createCommits++
			f.mu.Unlock()
		}
		return err
	}
	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("replay fault: real rollback failed: %w", err)
	}
	if onFault != nil {
		if err := onFault(attempt); err != nil {
			return fmt.Errorf("replay fault: competitor failed: %w", err)
		}
	}
	return &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction (injected replay fault)"}
}

func (f *replayFault) counts() (fired, createCommits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fired, f.createCommits
}

type replayFaultConnector struct {
	base  driver.Connector
	fault *replayFault
}

func (c *replayFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &replayFaultConn{conn: conn, fault: c.fault}, nil
}

func (c *replayFaultConnector) Driver() driver.Driver { return c.base.Driver() }

// replayFaultConn forwards every optional interface the mysql connection
// implements, so database/sql drives it exactly like the unwrapped pool.
type replayFaultConn struct {
	conn  driver.Conn
	fault *replayFault
	// wroteIssue is set when the open transaction wrote an issue row.
	wroteIssue bool
}

func (c *replayFaultConn) noteStatement(query string) {
	if issueRowInsert.MatchString(query) {
		c.wroteIssue = true
	}
}

func (c *replayFaultConn) Prepare(query string) (driver.Stmt, error) {
	c.noteStatement(query)
	return c.conn.Prepare(query)
}

func (c *replayFaultConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.noteStatement(query)
	if p, ok := c.conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return c.conn.Prepare(query)
}

func (c *replayFaultConn) Close() error { return c.conn.Close() }

//nolint:staticcheck // driver.Conn requires Begin; BeginTx is what database/sql calls.
func (c *replayFaultConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *replayFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := c.conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("replay fault: wrapped connection lacks BeginTx")
	}
	tx, err := b.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.wroteIssue = false
	return &replayFaultTx{tx: tx, conn: c}, nil
}

func (c *replayFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	res, err := e.ExecContext(ctx, query, args)
	if err != driver.ErrSkip {
		c.noteStatement(query)
	}
	return res, err
}

func (c *replayFaultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, query, args)
}

func (c *replayFaultConn) Ping(ctx context.Context) error {
	if p, ok := c.conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *replayFaultConn) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := c.conn.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (c *replayFaultConn) ResetSession(ctx context.Context) error {
	if r, ok := c.conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *replayFaultConn) IsValid() bool {
	if v, ok := c.conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

type replayFaultTx struct {
	tx   driver.Tx
	conn *replayFaultConn
}

func (t *replayFaultTx) Commit() error {
	wrote := t.conn.wroteIssue
	t.conn.wroteIssue = false
	if !wrote {
		return t.tx.Commit()
	}
	return t.conn.fault.commitCreate(t.tx)
}

func (t *replayFaultTx) Rollback() error {
	t.conn.wroteIssue = false
	return t.tx.Rollback()
}

// installReplayFault swaps store.db for a faulting pool on the same server
// database and returns the fault, the store's original pool (for competitors
// and read-back), and an idempotent restore that must run before the store's
// own cleanup.
func installReplayFault(t *testing.T, store *DoltStore, faults int) (*replayFault, *sql.DB, func()) {
	t.Helper()
	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("parse store DSN: %v", err)
	}
	base, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("mysql connector: %v", err)
	}
	fault := &replayFault{remaining: faults}
	faultDB := sql.OpenDB(&replayFaultConnector{base: base, fault: fault})
	faultDB.SetMaxOpenConns(2)
	faultDB.SetMaxIdleConns(0)

	orig := store.db
	store.db = faultDB
	var once sync.Once
	restore := func() {
		once.Do(func() {
			store.db = orig
			_ = faultDB.Close()
		})
	}
	return fault, orig, restore
}

// competitorCreate commits issue through issueops on db, the way another
// writer's create would land while the caller's transaction is rolled back.
func competitorCreate(ctx context.Context, db *sql.DB, issue *types.Issue) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := issueops.CreateIssueInTxWithResult(ctx, tx, bc, issue, "competitor"); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func assertReplayed(t *testing.T, fault *replayFault, wantFired int) {
	t.Helper()
	fired, commits := fault.counts()
	if fired != wantFired {
		t.Fatalf("replay fault fired %d time(s), want %d: the replay was not forced", fired, wantFired)
	}
	if commits != 1 {
		t.Fatalf("create transactions committed after the fault = %d, want exactly 1 (the replay)", commits)
	}
}

func enableSeededCounterMode(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("set issue_id_mode: %v", err)
	}
	// One create seeds the issue_counter row, so later mints take the
	// counter's next value on trust instead of reseeding from live rows.
	seed := &types.Issue{Title: "counter seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, seed, "seed"); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
}

func newRetryTestIssue(title string) *types.Issue {
	return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

func requireTitle(t *testing.T, ctx context.Context, store *DoltStore, id, want string) {
	t.Helper()
	got, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", id, err)
	}
	if got.Title != want {
		t.Errorf("issue %s title = %q, want %q", id, got.Title, want)
	}
}

// AC-1: a singular auto-ID CreateIssue rolled back before a competitor
// auto-mints its aborted counter ID must re-mint on replay, leaving the
// competitor's row intact.
func TestCreateIssueReplayRemintsAutoMintedID(t *testing.T) {
	for _, faults := range []int{1, 2} {
		t.Run(fmt.Sprintf("rollbacks=%d", faults), func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			enableSeededCounterMode(t, ctx, store)

			fault, orig, restore := installReplayFault(t, store, faults)
			defer restore()

			caller := newRetryTestIssue("caller")
			var stale []string
			var competitors []*types.Issue
			fault.onFault = func(int) error {
				stale = append(stale, caller.ID)
				c := newRetryTestIssue(fmt.Sprintf("competitor %d", len(competitors)+1))
				competitors = append(competitors, c)
				return competitorCreate(ctx, orig, c)
			}

			if err := store.CreateIssue(ctx, caller, "caller"); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			restore()
			assertReplayed(t, fault, faults)

			for i, c := range competitors {
				if stale[i] == "" || c.ID != stale[i] {
					t.Fatalf("competitor %d minted %q, want the aborted id %q: the race was not reproduced", i+1, c.ID, stale[i])
				}
				requireTitle(t, ctx, store, c.ID, c.Title)
				if caller.ID == c.ID {
					t.Errorf("replayed create kept the aborted id %s that competitor %d now owns", c.ID, i+1)
				}
			}
			requireTitle(t, ctx, store, caller.ID, "caller")
		})
	}
}
