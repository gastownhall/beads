package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

func TestExternalRefHistoryBatchContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	defer cancel()
	storagecontract.RunExternalRefHistoryBatchContract(t, ctx, externalRefHistoryFixture(t, store))
}

// TestPreviousExternalRefsChunksServerHistoryReads runs the batch on a real
// server through a connector that records both IN-lists of every history
// query: 40 ids whose latest rows are unambiguous cost ceil(40/16) = 3
// queries of 16, 16 and 8 ids per side, in one transaction, and every
// answer matches PreviousExternalRef. The store's own PreviousExternalRefs
// dials its connection from connStr (see
// TestPreviousExternalRefsUsesDedicatedLongTimeoutConnection), so the test
// opens the same one-connection handle through the recorder, pins the
// store's branch as withReadTxLongTimeout does, and hands its transaction
// to issueops.PreviousExternalRefsInTx.
func TestPreviousExternalRefsChunksServerHistoryReads(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	defer cancel()

	ids := make([]string, 40)
	for i := range ids {
		ids[i] = fmt.Sprintf("test-ck%02d", i)
	}
	fx := externalRefHistoryFixture(t, store)
	if err := fx.CreateIssues(ctx, ids); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Dated after the creates' own commits, so each id has one latest row.
	refsAt := time.Now().UTC().Add(time.Minute)
	if err := fx.CommitOn(ctx, fx.Branch, refsAt, "UPDATE issues SET external_ref = CONCAT('ref-', id)"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := store.DeleteIssues(ctx, ids, false, true, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	asOf := refsAt.Add(time.Hour)

	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	rec := &historyReadRecorder{}
	db := sql.OpenDB(recordingConnector{connector, rec})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := store.pinStoreBranch(ctx, db); err != nil {
		t.Fatalf("pin branch: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	got, err := issueops.PreviousExternalRefsInTx(ctx, tx, ids, asOf)
	if err != nil {
		t.Fatalf("PreviousExternalRefsInTx: %v", err)
	}
	if want := [][]int{{16, 16}, {16, 16}, {8, 8}}; fmt.Sprint(rec.inLists) != fmt.Sprint(want) {
		t.Fatalf("history queries' IN-list sizes %v, want %v: ceil(40/16) queries, each binding its chunk into both sides", rec.inLists, want)
	}
	for _, id := range ids {
		ref, found, err := store.PreviousExternalRef(ctx, id, asOf)
		if err != nil || !found {
			t.Fatalf("PreviousExternalRef(%s) = (%q, %v, %v), want found", id, ref, found, err)
		}
		if g, ok := got[id]; !ok || g != ref {
			t.Errorf("PreviousExternalRefsInTx[%s] = (%q, %v), PreviousExternalRef = %q", id, g, ok, ref)
		}
	}
}

// historyReadRecorder holds, per dolt_history_issues query, the number of
// placeholders in each of its IN-lists.
type historyReadRecorder struct{ inLists [][]int }

type recordingConnector struct {
	driver.Connector
	rec *historyReadRecorder
}

func (c recordingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	full, ok := conn.(mysqlConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("%T lacks a driver interface the recorder forwards", conn)
	}
	return recordingConn{full, c.rec}, nil
}

type mysqlConn interface {
	driver.Conn
	driver.ConnPrepareContext
	driver.ConnBeginTx
	driver.ExecerContext
	driver.QueryerContext
	driver.NamedValueChecker
	driver.SessionResetter
	driver.Validator
	driver.Pinger
}

type recordingConn struct {
	mysqlConn
	rec *historyReadRecorder
}

func (c recordingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "dolt_history_issues") {
		var sizes []int
		for rest := query; ; {
			_, in, ok := strings.Cut(rest, " IN (")
			if !ok {
				break
			}
			in, rest, _ = strings.Cut(in, ")")
			sizes = append(sizes, strings.Count(in, "?"))
		}
		c.rec.inLists = append(c.rec.inLists, sizes)
	}
	return c.mysqlConn.QueryContext(ctx, query, args)
}

func BenchmarkExternalRefHistoryBatch(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	b.Cleanup(cleanup)
	storagecontract.RunExternalRefHistoryBatchBenchmark(b, b.Context(), externalRefHistoryFixture(b, store))
}

// DOLT_CHECKOUT is per connection: branch work needs a one-connection pool.
func externalRefHistoryFixture(tb testing.TB, store *DoltStore) storagecontract.ExternalRefHistoryFixture {
	var branch string
	if err := store.db.QueryRowContext(context.Background(), "SELECT active_branch()").Scan(&branch); err != nil {
		tb.Fatalf("active_branch: %v", err)
	}
	exec := func(ctx context.Context, stmt string, args ...any) error {
		_, err := store.db.ExecContext(ctx, stmt, args...)
		return err
	}
	return storagecontract.ExternalRefHistoryFixture{
		Store:  store,
		Branch: branch,
		CreateIssues: func(ctx context.Context, ids []string) error {
			for _, id := range ids {
				issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
					return err
				}
			}
			return nil
		},
		DeleteIssue: store.DeleteIssue,
		CreateBranch: func(ctx context.Context, name string) error {
			tb.Cleanup(func() { _ = exec(context.Background(), "CALL DOLT_BRANCH('-D', ?)", name) })
			return exec(ctx, "CALL DOLT_BRANCH(?)", name)
		},
		CommitOn: func(ctx context.Context, onto string, when time.Time, stmts ...string) (err error) {
			if err := exec(ctx, "CALL DOLT_CHECKOUT(?)", onto); err != nil {
				return err
			}
			defer func() {
				if cerr := exec(ctx, "CALL DOLT_CHECKOUT(?)", branch); err == nil {
					err = cerr
				}
			}()
			for _, stmt := range stmts {
				if err := exec(ctx, stmt); err != nil {
					return err
				}
			}
			return exec(ctx, "CALL DOLT_COMMIT('-Am', 'step', '--date', ?)", when.Format("2006-01-02T15:04:05.000Z"))
		},
		Merge: func(ctx context.Context, from string) error {
			_, err := store.Merge(ctx, from)
			return err
		},
	}
}
