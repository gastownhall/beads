package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

// Regression test for the server-mode lost-update bug
// (LatentLabsSpace/NEXUS#92): the issue-mutation path used to run
// CALL DOLT_ADD / CALL DOLT_COMMIT inside the still-open SQL transaction.
// DOLT_ADD stages the whole table from the session's BEGIN-time root, so a
// Dolt commit built before the transaction's commit-time merge writes every
// concurrently-committed row back to its BEGIN-time value.
//
// The fix is an ordering contract: the Dolt add/commit MUST execute strictly
// after driver-level tx.Commit(). This test pins that contract with a
// sequence-capturing driver, so it needs no running Dolt server and holds
// for embedded and server mode alike.

// --- sequence-capturing driver --------------------------------------------

type seqRecorder struct {
	mu     sync.Mutex
	events []string
	conns  []int // conns[i] is the driver connection that produced events[i]
}

func (r *seqRecorder) add(conn int, ev string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	r.conns = append(r.conns, conn)
}

func (r *seqRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

// snapshotConns returns the events with the connection id each ran on.
func (r *seqRecorder) snapshotConns() ([]string, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	evs := make([]string, len(r.events))
	copy(evs, r.events)
	cs := make([]int, len(r.conns))
	copy(cs, r.conns)
	return evs, cs
}

type seqConnector struct {
	rec    *seqRecorder
	nextID atomic.Int64
	// queryErr, when set, can fail a statement after it is recorded (the
	// driver routes CALL statements through QueryContext via DrainCall).
	queryErr func(query string) error
	// rows, when set, serves a canned result set for matching queries.
	rows func(query string) (driver.Rows, bool)
	// commitErr, when set, can fail the driver-level tx COMMIT after it is
	// recorded (for exercising the ambiguous commit-phase path).
	commitErr func() error
}

func (c *seqConnector) Connect(context.Context) (driver.Conn, error) {
	return &seqConn{parent: c, id: int(c.nextID.Add(1))}, nil
}
func (c *seqConnector) Driver() driver.Driver { return seqDriver{} }

type seqDriver struct{}

func (seqDriver) Open(string) (driver.Conn, error) { return nil, driver.ErrSkip }

type seqConn struct {
	parent *seqConnector
	id     int
}

func (c *seqConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *seqConn) Close() error                        { return nil }
func (c *seqConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *seqConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.parent.rec.add(c.id, "BEGIN")
	return &seqTx{rec: c.parent.rec, conn: c.id, commitErr: c.parent.commitErr}, nil
}

func (c *seqConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.parent.rec.add(c.id, query)
	if c.parent.queryErr != nil {
		if err := c.parent.queryErr(query); err != nil {
			return nil, err
		}
	}
	return driver.RowsAffected(1), nil
}

func (c *seqConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.parent.rec.add(c.id, query)
	if c.parent.queryErr != nil {
		if err := c.parent.queryErr(query); err != nil {
			return nil, err
		}
	}
	if c.parent.rows != nil {
		if rows, ok := c.parent.rows(query); ok {
			return rows, nil
		}
	}
	if strings.Contains(query, "FROM dolt_status") {
		// doltAddAndCommit's empty-commit guard: report one staged row so the
		// post-tx sequence under test still reaches DOLT_COMMIT.
		return &valRows{cols: []string{"count"}, vals: [][]driver.Value{{int64(1)}}}, nil
	}
	return &seqRows{}, nil
}

type seqTx struct {
	rec       *seqRecorder
	conn      int
	commitErr func() error
}

func (t *seqTx) Commit() error {
	t.rec.add(t.conn, "COMMIT")
	if t.commitErr != nil {
		return t.commitErr()
	}
	return nil
}
func (t *seqTx) Rollback() error { t.rec.add(t.conn, "ROLLBACK"); return nil }

type seqRows struct{}

func (*seqRows) Columns() []string              { return []string{} }
func (*seqRows) Close() error                   { return nil }
func (*seqRows) Next(dest []driver.Value) error { return io.EOF }

// valRows serves one canned row, for tests that need a re-read to see state.
type valRows struct {
	cols []string
	vals [][]driver.Value
	i    int
}

func (r *valRows) Columns() []string { return r.cols }
func (r *valRows) Close() error      { return nil }
func (r *valRows) Next(dest []driver.Value) error {
	if r.i >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.i])
	r.i++
	return nil
}

// --- the ordering contract -------------------------------------------------

func TestIssueOperationDoltCommitRunsAfterTxCommit(t *testing.T) {
	rec := &seqRecorder{}
	db := sql.OpenDB(&seqConnector{rec: rec})
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}

	err := s.runIssueOperationTx(context.Background(), "bd: update test-1", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		if _, err := tx.ExecContext(context.Background(), "UPDATE issues SET status = 'in_progress' WHERE id = 'test-1'"); err != nil {
			return nil, err
		}
		return storageissueops.ChangedTables{"issues": true}, nil
	})
	if err != nil {
		t.Fatalf("runIssueOperationTx: %v", err)
	}

	events := rec.snapshot()
	idx := func(pred func(string) bool) int {
		for i, ev := range events {
			if pred(ev) {
				return i
			}
		}
		return -1
	}

	commitIdx := idx(func(ev string) bool { return ev == "COMMIT" })
	doltAddIdx := idx(func(ev string) bool { return strings.Contains(ev, "DOLT_ADD") })
	doltCommitIdx := idx(func(ev string) bool { return strings.Contains(ev, "DOLT_COMMIT") })

	if commitIdx == -1 {
		t.Fatalf("no driver-level tx COMMIT recorded; events: %v", events)
	}
	if doltAddIdx == -1 || doltCommitIdx == -1 {
		t.Fatalf("DOLT_ADD/DOLT_COMMIT not recorded; events: %v", events)
	}
	if doltAddIdx < commitIdx {
		t.Errorf("DOLT_ADD ran INSIDE the open transaction (index %d < COMMIT index %d) — lost-update ordering regression; events: %v",
			doltAddIdx, commitIdx, events)
	}
	if doltCommitIdx < commitIdx {
		t.Errorf("DOLT_COMMIT ran INSIDE the open transaction (index %d < COMMIT index %d) — lost-update ordering regression; events: %v",
			doltCommitIdx, commitIdx, events)
	}
	if doltCommitIdx < doltAddIdx {
		t.Errorf("DOLT_COMMIT before DOLT_ADD; events: %v", events)
	}

	// No transaction may still be open when the Dolt commit runs: assert no
	// BEGIN after the data COMMIT and before the DOLT_COMMIT.
	for i := commitIdx + 1; i < doltCommitIdx; i++ {
		if events[i] == "BEGIN" {
			t.Errorf("unexpected BEGIN between tx COMMIT and DOLT_COMMIT at index %d; events: %v", i, events)
		}
	}
}

// TestIssueOperationNoTablesSkipsDoltCommit pins the no-op contract: an
// operation reporting no changed tables must not create a Dolt commit.
func TestIssueOperationNoTablesSkipsDoltCommit(t *testing.T) {
	rec := &seqRecorder{}
	db := sql.OpenDB(&seqConnector{rec: rec})
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}

	err := s.runIssueOperationTx(context.Background(), "bd: noop", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("runIssueOperationTx: %v", err)
	}
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev, "DOLT_COMMIT") {
			t.Errorf("no-op operation produced a DOLT_COMMIT; events: %v", rec.snapshot())
		}
	}
}

// TestPostTxDoltAddCommitSharesOneConnection pins the connection contract:
// the post-tx DOLT_ADD…DOLT_COMMIT sequence must run on ONE pinned pool
// connection (GH#2455). With MaxIdleConns(0) every bare s.db statement gets a
// fresh driver connection, so an unpinned sequence deterministically shows
// distinct connection ids — including a DOLT_COMMIT on a different session
// than the DOLT_ADD it depends on, which under a multi-branch pool commits
// the wrong branch's staged root or degrades to nothing-to-commit.
func TestPostTxDoltAddCommitSharesOneConnection(t *testing.T) {
	rec := &seqRecorder{}
	db := sql.OpenDB(&seqConnector{rec: rec})
	defer func() { _ = db.Close() }()
	db.SetMaxIdleConns(0)

	s := &DoltStore{db: db}

	err := s.runIssueOperationTx(context.Background(), "bd: update test-1", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		if _, err := tx.ExecContext(context.Background(), "UPDATE issues SET status = 'in_progress' WHERE id = 'test-1'"); err != nil {
			return nil, err
		}
		return storageissueops.ChangedTables{"issues": true, "events": true}, nil
	})
	if err != nil {
		t.Fatalf("runIssueOperationTx: %v", err)
	}

	events, conns := rec.snapshotConns()
	doltConn := 0
	for i, ev := range events {
		if !strings.Contains(ev, "DOLT_ADD") && !strings.Contains(ev, "DOLT_COMMIT") {
			continue
		}
		if doltConn == 0 {
			doltConn = conns[i]
			continue
		}
		if conns[i] != doltConn {
			t.Fatalf("post-tx dolt statement %q ran on connection %d, want pinned connection %d; events: %v conns: %v",
				ev, conns[i], doltConn, events, conns)
		}
	}
	if doltConn == 0 {
		t.Fatalf("no DOLT_ADD/DOLT_COMMIT recorded; events: %v", events)
	}
}

// TestPostTxDoltCommitFailureDoesNotFailMutation pins the swallow contract:
// a post-tx DOLT_ADD/DOLT_COMMIT failure happens after the data transaction
// committed, so the mutation IS applied and the operation must report
// success (the failure is logged; the change rides the next dolt commit).
// Surfacing it would make callers treat an applied mutation as failed —
// automated retries double-apply, hooks are skipped, and claim callers
// abandon claims they hold (the wy-x543k inversion).
func TestPostTxDoltCommitFailureDoesNotFailMutation(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	connector.queryErr = func(query string) error {
		if strings.Contains(query, "DOLT_COMMIT") {
			return errors.New("boom: injected dolt commit failure")
		}
		return nil
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}

	err := s.runIssueOperationTx(context.Background(), "bd: update test-1", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		return storageissueops.ChangedTables{"issues": true}, nil
	})
	if err != nil {
		t.Fatalf("post-tx dolt commit failure must not fail the mutation (data committed); got: %v", err)
	}

	// The data COMMIT and the DOLT_COMMIT attempt must both have run.
	events := rec.snapshot()
	sawCommit, sawDoltCommit := false, false
	for _, ev := range events {
		if ev == "COMMIT" {
			sawCommit = true
		}
		if strings.Contains(ev, "DOLT_COMMIT") {
			sawDoltCommit = true
		}
	}
	if !sawCommit {
		t.Fatalf("data transaction never committed; events: %v", events)
	}
	if !sawDoltCommit {
		t.Fatalf("post-tx DOLT_COMMIT never attempted; events: %v", events)
	}
}

// TestBatchMutatorPostTxDoltCommitFailureReturnsSuccess pins the batch-path
// facet of the swallow contract (PR #6040 review, cat-4 gap). CreateBatch,
// ApplyBatch, and CloseBatch all funnel through runIssueOperationTxWithMessage
// — the message-composing entry point — and, unlike the claim paths, have NO
// verify-by-re-read recovery. So a post-tx DOLT_COMMIT failure on a batch must
// still report success: the batch's data COMMITted (durable in the branch
// working set) and was staged by DOLT_ADD before the trailing history commit
// was attempted, and surfacing the audit-commit failure would make a batch
// caller retry an already-applied batch (double-apply/double-create). This
// drives that exact entry point with the commit message composed from what
// "landed", exactly as the batch mutators do.
func TestBatchMutatorPostTxDoltCommitFailureReturnsSuccess(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	connector.queryErr = func(query string) error {
		if strings.Contains(query, "DOLT_COMMIT") {
			return errors.New("boom: injected dolt commit failure")
		}
		return nil
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}

	// Mirror a batch mutator: run the item work in the tx, then compose the
	// commit message from what landed. CreateBatch/ApplyBatch/CloseBatch all
	// build their message inside the body for exactly this reason, which is why
	// they use runIssueOperationTxWithMessage rather than runIssueOperationTx.
	err := s.runIssueOperationTxWithMessage(context.Background(), func(tx *sql.Tx) (storageissueops.ChangedTables, string, error) {
		if _, err := tx.ExecContext(context.Background(), "INSERT INTO issues (id) VALUES ('batch-1')"); err != nil {
			return nil, "", err
		}
		return storageissueops.ChangedTables{"issues": true}, "bd: create 1 issue (batch)", nil
	})
	if err != nil {
		t.Fatalf("batch post-tx dolt commit failure must not fail the mutation (data committed); got: %v", err)
	}

	// Durability proof: the batch data must have COMMITted and been staged by
	// DOLT_ADD before the swallowed DOLT_COMMIT — i.e. it is in the working set,
	// not lost — and the operation still returned success above.
	events := rec.snapshot()
	idx := func(pred func(string) bool) int {
		for i, ev := range events {
			if pred(ev) {
				return i
			}
		}
		return -1
	}
	commitIdx := idx(func(ev string) bool { return ev == "COMMIT" })
	doltAddIdx := idx(func(ev string) bool { return strings.Contains(ev, "DOLT_ADD") })
	doltCommitIdx := idx(func(ev string) bool { return strings.Contains(ev, "DOLT_COMMIT") })
	if commitIdx == -1 {
		t.Fatalf("batch data transaction never committed; events: %v", events)
	}
	if doltAddIdx == -1 {
		t.Fatalf("post-tx DOLT_ADD never staged the batch working set; events: %v", events)
	}
	if doltCommitIdx == -1 {
		t.Fatalf("post-tx DOLT_COMMIT never attempted; events: %v", events)
	}
	if !(commitIdx < doltAddIdx && doltAddIdx < doltCommitIdx) {
		t.Fatalf("batch durability ordering violated: want COMMIT(%d) < DOLT_ADD(%d) < DOLT_COMMIT(%d); events: %v",
			commitIdx, doltAddIdx, doltCommitIdx, events)
	}
}

// TestPostTxDoltCommitRetriesSerializationConflict pins the retry contract:
// Dolt's rollback-guaranteed commit conflicts (1213/1205, 1105 autocommit
// rollback) were retried when the dolt commit ran inside withRetryTx, and
// they are routine under the concurrent-writer load the post-tx ordering
// targets — so the post-tx sequence must retry them too, not fail (and lose
// the audit commit) on first conflict.
func TestPostTxDoltCommitRetriesSerializationConflict(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	failures := 0
	connector.queryErr = func(query string) error {
		if strings.Contains(query, "DOLT_COMMIT") && failures == 0 {
			failures++
			return &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}
		}
		return nil
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}

	if err := s.doltAddAndCommitPostTx(context.Background(), []string{"issues"}, "bd: update test-1"); err != nil {
		t.Fatalf("serialization conflict must be retried to success, got: %v", err)
	}
	doltCommits := 0
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev, "DOLT_COMMIT") {
			doltCommits++
		}
	}
	if doltCommits < 2 {
		t.Fatalf("expected the DOLT_COMMIT to be retried after 1213, saw %d attempt(s)", doltCommits)
	}
}

// TestPostTxDoltCommitSurvivesCallerCancellation pins the detached-context
// contract: the data transaction has already committed when the post-tx
// dolt commit runs, so the caller's request-scoped cancellation (deadline,
// shutdown, Ctrl-C) must not skip the audit commit — the post-tx phase runs
// on a detached context with its own budget.
func TestPostTxDoltCommitSurvivesCallerCancellation(t *testing.T) {
	rec := &seqRecorder{}
	db := sql.OpenDB(&seqConnector{rec: rec})
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the post-tx commit starts

	if err := s.doltAddAndCommitPostTx(ctx, []string{"issues"}, "bd: update test-1"); err != nil {
		t.Fatalf("post-tx dolt commit must survive caller cancellation, got: %v", err)
	}
	sawDoltCommit := false
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev, "DOLT_COMMIT") {
			sawDoltCommit = true
		}
	}
	if !sawDoltCommit {
		t.Fatalf("DOLT_COMMIT never ran under a cancelled caller context; events: %v", rec.snapshot())
	}
}

// TestAmbiguousTxCommitDoesNotMintDoltCommit pins the ErrCommitIndeterminate
// contract: a connection loss during the SQL COMMIT is ambiguous — and NO
// post-tx dolt commit may be attempted off that error. In server mode the
// branch working set is shared, so staging it after a commit that actually
// rolled back would mint a dolt commit of a CONCURRENT writer's pending rows
// under this operation's message — phantom audit evidence for a mutation
// the caller is simultaneously told did not land. The error must surface
// unchanged so the claim-verify protocol resolves the true outcome.
func TestAmbiguousTxCommitDoesNotMintDoltCommit(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	connector.commitErr = func() error { return errors.New("invalid connection") }
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}

	err := s.runIssueOperationTx(context.Background(), "bd: update test-1", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		return storageissueops.ChangedTables{"issues": true}, nil
	})
	if err == nil {
		t.Fatal("ambiguous commit loss must surface its error to the caller")
	}
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("commit-phase connection loss must carry ErrCommitIndeterminate, got: %v", err)
	}
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev, "DOLT_ADD") || strings.Contains(ev, "DOLT_COMMIT") {
			t.Fatalf("no dolt staging/commit may be attempted after an ambiguous tx commit (phantom audit hazard); events: %v", rec.snapshot())
		}
	}
}

// TestVerifiedClaimResolvesPostTxCommitFailureAsApplied pins the end-to-end
// recovery: a claim whose data transaction committed but whose trailing dolt
// commit failed must resolve as SUCCESS via verify-by-re-read — not surface
// an error for a claim the caller actually holds (the wy-x543k inversion).
func TestVerifiedClaimResolvesPostTxCommitFailureAsApplied(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	connector.queryErr = func(query string) error {
		if strings.Contains(query, "DOLT_COMMIT") {
			return errors.New("boom: injected dolt commit failure")
		}
		return nil
	}
	connector.rows = func(query string) (driver.Rows, bool) {
		// The verify re-read: report the claim as applied.
		if strings.Contains(query, "SELECT assignee, status FROM issues") {
			return &valRows{
				cols: []string{"assignee", "status"},
				vals: [][]driver.Value{{"claim-actor", "in_progress"}},
			}, true
		}
		return nil, false
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db, serverMode: true}

	write := func() error {
		return s.runIssueOperationTx(context.Background(), "bd: claim test-1 by claim-actor", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
			return storageissueops.ChangedTables{"issues": true}, nil
		})
	}
	if err := s.verifiedClaimWrite(context.Background(), "test-1", claimedBy("claim-actor"), write); err != nil {
		t.Fatalf("post-tx dolt commit failure must resolve as applied via re-read, got: %v", err)
	}
}
