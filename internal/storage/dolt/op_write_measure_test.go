package dolt

// Measure the SQL statements one gascity-shaped op write issues on the Dolt
// server (TCP) backend: a no-history wisp created at an explicit id with no
// edges (the op journal's open, through issueops.ExecuteCreate as the
// IssueLifecycle role runs it), then the row closed through a single-item
// apply batch with a version fence (the op journal's close, as
// NativeDoltStore.UpdateIfMatch issues it). Each write is one transaction,
// as in production. Statement text is logged so the breakdown, not only the
// total, is in the test log.
//
// Like large_batch_apply_measure_test.go, it opens a second, counting-wrapped
// connection from the store's own connStr because the bodies take a concrete
// *sql.Tx.

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// statementLog collects the statements a counted connection issues.
type statementLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *statementLog) observe(kind, query string) {
	text := strings.Join(strings.Fields(query), " ")
	if len(text) > 150 {
		text = text[:150] + "..."
	}
	l.mu.Lock()
	l.lines = append(l.lines, kind+": "+text)
	l.mu.Unlock()
}

func (l *statementLog) reset() {
	l.mu.Lock()
	l.lines = nil
	l.mu.Unlock()
}

func (l *statementLog) dump(t *testing.T, label string, snap sqlcount.Snapshot, elapsed time.Duration) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	t.Logf("%s: Prepare=%d Exec=%d Query=%d StmtExec=%d StmtQuery=%d Total=%d elapsed=%s",
		label, snap.Prepare, snap.Exec, snap.Query, snap.StmtExec, snap.StmtQuery, snap.Total(), elapsed)
	for i, line := range l.lines {
		t.Logf("  %s %3d %s", label, i+1, line)
	}
}

// openObservedDoltConn is openCountedDoltConn with a statement observer.
func openObservedDoltConn(t *testing.T, connStr string, counts *sqlcount.Counts, log *statementLog) *sql.DB {
	t.Helper()
	cfg, err := mysql.ParseDSN(connStr)
	if err != nil {
		t.Fatalf("parse connStr: %v", err)
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("mysql connector: %v", err)
	}
	db := sql.OpenDB(sqlcount.WrapConnector(connector, counts, sqlcount.WithObserver(log.observe)))
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// measureTx runs body in one transaction on db and returns the statement
// snapshot and wall time of BeginTx..Commit.
func measureTx(ctx context.Context, t *testing.T, db *sql.DB, counts *sqlcount.Counts, log *statementLog, journal bool, body func(tx *sql.Tx) error) (sqlcount.Snapshot, time.Duration) {
	t.Helper()
	counts.Reset()
	log.reset()
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	release := storageissueops.ScopeEventsJournalTransaction(tx, journal)
	defer release()
	if err := body(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("body: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return counts.Snapshot(), time.Since(start)
}

func opCreateRequest(id string) issueops.CreateRequest {
	return issueops.CreateRequest{
		Actor: "gc",
		Issue: &types.Issue{
			ID:        id,
			Title:     "op " + id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			NoHistory: true,
		},
		ForceIDPrefix: true,
	}
}

func opClosePlan(t *testing.T, id string, version int64) storage.ApplyBatchPlan {
	t.Helper()
	plan, err := storage.PlanApplyBatch(issueops.ApplyBatchRequest{
		Actor: "gc",
		Items: []issueops.ApplyItem{{
			Kind: issueops.ItemUpdate,
			Update: &issueops.UpdateItem{
				Target:           issueops.Ref{ID: id},
				Patch:            issueops.IssuePatch{Status: issueops.Field[issueops.Status]{Set: true, Value: types.StatusClosed}},
				ExpectedVersion:  &version,
				ForceClosePolicy: true,
			},
		}},
		Provenance: "gc: conditional update " + id,
	})
	if err != nil {
		t.Fatalf("plan close: %v", err)
	}
	return plan
}

// TestOpWriteStatements logs the statements of an op open and close, with
// the events journal off and on (gascity enables it on its native store).
func TestOpWriteStatements(t *testing.T) {
	ctx := context.Background()
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	var counts sqlcount.Counts
	var log statementLog
	db := openObservedDoltConn(t, store.connStr, &counts, &log)

	for _, journal := range []bool{false, true} {
		label := "journal=off"
		if journal {
			label = "journal=on"
		}
		t.Run(label, func(t *testing.T) {
			id := "gco-op" + strings.ReplaceAll(label, "=", "")
			var created issueops.CreateResult
			snap, elapsed := measureTx(ctx, t, db, &counts, &log, journal, func(tx *sql.Tx) error {
				var err error
				created, _, err = storageissueops.ExecuteCreate(ctx, tx, opCreateRequest(id))
				return err
			})
			log.dump(t, "create(explicit id)", snap, elapsed)

			snap, elapsed = measureTx(ctx, t, db, &counts, &log, journal, func(tx *sql.Tx) error {
				_, _, err := storageissueops.ApplyBatchInTx(ctx, tx, opClosePlan(t, id, created.Issue.RowVersion))
				return err
			})
			log.dump(t, "close(apply, fenced)", snap, elapsed)

			minted := opCreateRequest("")
			snap, elapsed = measureTx(ctx, t, db, &counts, &log, journal, func(tx *sql.Tx) error {
				_, _, err := storageissueops.ExecuteCreate(ctx, tx, minted)
				return err
			})
			log.dump(t, "create(minted id)", snap, elapsed)
		})
	}
}
