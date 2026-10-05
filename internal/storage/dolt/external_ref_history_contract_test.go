package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

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

func TestPreviousExternalRefsRefusedDialLeavesFallbackAdmitted(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	refused := ln.Addr().String()
	_ = ln.Close()
	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	cfg.Addr = refused
	store.connStr = cfg.FormatDSN()
	t.Setenv("BEADS_TEST_MODE", "")
	store.breaker = newTestCircuitBreaker(t)

	_, _ = store.PreviousExternalRefs(ctx, []string{"test-1"}, time.Now())
	if _, _, err := store.PreviousExternalRef(ctx, "test-1", time.Now()); err != nil {
		t.Fatalf("PreviousExternalRef after a batch with dials refused: %v (breaker %s)", err, store.breaker.State())
	}
}

func TestPreviousExternalRefsChunksServerHistoryReads(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
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
	if err := fx.CommitOn(ctx, fx.Branch, time.Now().UTC(), "UPDATE issues SET external_ref = CONCAT('ref-', id)"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := store.DeleteIssues(ctx, ids, false, true, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	asOf := time.Now()

	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	rec := &historyReadRecorder{}
	pooled := store.db
	store.db = sql.OpenDB(recordingConnector{connector, rec})
	t.Cleanup(func() { _ = store.db.Close(); store.db = pooled })

	got, err := store.PreviousExternalRefs(ctx, ids, asOf)
	if err != nil {
		t.Fatalf("PreviousExternalRefs: %v", err)
	}
	total := 0
	for _, n := range rec.sizes {
		total += n
	}
	if total != len(ids) || slices.Max(rec.sizes) > 16 {
		t.Fatalf("history read IN-list sizes %v: want every one of %d ids sent, at most 16 per read", rec.sizes, len(ids))
	}
	for _, id := range ids {
		ref, found, err := store.PreviousExternalRef(ctx, id, asOf)
		if err != nil || !found {
			t.Fatalf("PreviousExternalRef(%s) = (%q, %v, %v), want found", id, ref, found, err)
		}
		if g, ok := got[id]; !ok || g != ref {
			t.Errorf("PreviousExternalRefs[%s] = (%q, %v), PreviousExternalRef = %q", id, g, ok, ref)
		}
	}
}

type historyReadRecorder struct{ sizes []int }

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
	if _, in, ok := strings.Cut(query, " IN ("); ok && strings.Contains(query, "dolt_history_issues") {
		in, _, _ = strings.Cut(in, ")")
		c.rec.sizes = append(c.rec.sizes, strings.Count(in, "?"))
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
