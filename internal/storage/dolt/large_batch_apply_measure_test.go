package dolt

// Measure the ACTUAL number of SQL statements issueops.ApplyBatchInTx issues
// on the Dolt server (TCP) backend, for three measured plan shapes. See
// internal/storage/embeddeddolt/large_batch_apply_measure_test.go for the
// embedded backend equivalent, and internal/storage/batchfixtures for the
// shared, backend-agnostic plan construction.
//
// This file opens a SECOND, counting-wrapped connection using the same
// mysql.Config a normal *DoltStore already parsed from its own connStr
// (store.connStr, set by newServerMode/openServerConnection) — because
// issueops.ApplyBatchInTx takes a concrete *sql.Tx and the store's own pool
// is not wrapped. It is package dolt (not dolt_test) specifically to reach
// that unexported field, the same way dolt_test.go's own helpers already do.

import (
	"context"
	"database/sql"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"

	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// openCountedDoltConn opens a second, counting-wrapped connection to the
// same Dolt server database an already-open *DoltStore is using, by
// re-parsing that store's own connStr. MaxOpenConns is pinned to 1 so every
// statement observed goes over one physical connection — matching how
// ApplyBatchInTx itself runs entirely inside one *sql.Tx on one connection.
func openCountedDoltConn(connStr string, counts *sqlcount.Counts) (*sql.DB, func(), error) {
	cfg, err := mysql.ParseDSN(connStr)
	if err != nil {
		return nil, nil, err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, nil, err
	}
	db := sql.OpenDB(sqlcount.WrapConnector(connector, counts))
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, func() { db.Close() }, nil
}

// measureDoltApply builds plan via batchfixtures.UncappedPlan (bypassing
// issueops.MaxApplyBatchItems, see uncapped_plan.go), runs it through
// issueops.ApplyBatchInTx inside one transaction on a fresh counted
// connection, and returns the statement-count snapshot. counts.Reset() is
// called immediately before BeginTx so the snapshot reflects only the
// transaction itself, not connection setup.
func measureDoltApply(ctx context.Context, connStr, rootID string, counts *sqlcount.Counts, build func(rootID string) issueops.ApplyBatchRequest) (sqlcount.Snapshot, error) {
	db, closeDB, err := openCountedDoltConn(connStr, counts)
	if err != nil {
		return sqlcount.Snapshot{}, err
	}
	defer closeDB()

	plan := batchfixtures.UncappedPlan(build(rootID))

	counts.Reset()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return sqlcount.Snapshot{}, err
	}
	if _, _, err := storageissueops.ApplyBatchInTx(ctx, tx, plan); err != nil {
		_ = tx.Rollback()
		return sqlcount.Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return sqlcount.Snapshot{}, err
	}
	return counts.Snapshot(), nil
}

// largeBatchApplyDoltShapes mirrors embeddeddolt's largeBatchApplyShapes:
// the three shapes the design measures, plus a per-shape database name (this
// backend uses one fresh throwaway database per shape via
// setupConcurrentTestStore, not a directory clone).
var largeBatchApplyDoltShapes = []struct {
	name  string
	build func(rootID string) issueops.ApplyBatchRequest
}{
	{
		name: "356 (mol 1x)",
		build: func(rootID string) issueops.ApplyBatchRequest {
			return batchfixtures.Shape356("tester", rootID)
		},
	},
	{
		name: "712 (mol 2x)",
		build: func(rootID string) issueops.ApplyBatchRequest {
			return batchfixtures.Shape712("tester", rootID)
		},
	},
	{
		name: "40 (classic)",
		build: func(string) issueops.ApplyBatchRequest {
			return batchfixtures.ShapeClassic40("tester")
		},
	},
}

// TestLargeBatchApplyStatementCounts_Dolt pins the ACTUAL number of SQL
// statements issueops.ApplyBatchInTx issues on the Dolt server (TCP)
// backend, for each of three measured shapes. Sibling regression baseline to
// TestLargeBatchApplyStatementCounts{356,712,Classic40}_Embedded in the
// embeddeddolt package (originally one test, split for CI shard balance;
// see its doc comment) — see that test's doc comment for why counts are
// pinned with a small tolerance rather than exactly, and why a drift beyond
// it should be re-measured and re-pinned deliberately rather than loosened
// further.
func TestLargeBatchApplyStatementCounts_Dolt(t *testing.T) {
	ctx := context.Background()

	for _, tc := range largeBatchApplyDoltShapes {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupConcurrentTestStore(t)
			defer cleanup()

			root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := store.CreateIssue(ctx, root, "tester"); err != nil {
				t.Fatalf("create root issue: %v", err)
			}

			var counts sqlcount.Counts
			got, err := measureDoltApply(ctx, store.connStr, root.ID, &counts, tc.build)
			if err != nil {
				t.Fatalf("measureDoltApply: %v", err)
			}
			t.Logf("dolt %s: Prepare=%d Exec=%d Query=%d StmtExec=%d StmtQuery=%d Total=%d",
				tc.name, got.Prepare, got.Exec, got.Query, got.StmtExec, got.StmtQuery, got.Total())

			want, ok := pinnedDoltStatementCounts[tc.name]
			if !ok {
				t.Fatalf("no pinned count for shape %q; run with -v, read the logged Total, and add it to pinnedDoltStatementCounts", tc.name)
			}
			diff := got.Total() - want
			if diff < 0 {
				diff = -diff
			}
			if diff > statementCountToleranceDolt {
				t.Errorf("dolt %s: Total() = %d, want pinned %d +/- %d (see large_batch_apply_measure_test.go)",
					tc.name, got.Total(), want, statementCountToleranceDolt)
			}
		})
	}
}

// statementCountToleranceDolt mirrors embeddeddolt's statementCountTolerance:
// a narrow allowance for occasional host-contention-driven retry polling,
// not a license to loosen a real drift. See that constant's doc comment.
const statementCountToleranceDolt = 2

// pinnedDoltStatementCounts is the B0 regression baseline for the Dolt
// server (TCP) backend: the Total() statement count issueops.ApplyBatchInTx
// issued for each measured shape, as last observed on this branch. B2 must
// lower these; a change for any other reason should be re-measured and
// re-pinned deliberately, not adjusted to make a failure go away.
//
// Re-pinned for the batch-create round-trip work (was 7008 / 14014 / 846);
// see pinnedEmbeddedStatementCounts in internal/storage/embeddeddolt for the
// per-change breakdown. The two backends now agree to within the tolerance.
var pinnedDoltStatementCounts = map[string]int64{
	"356 (mol 1x)": 6396,
	"712 (mol 2x)": 12791,
	"40 (classic)": 786,
}

// BenchmarkLargeBatchApply_Dolt is gated by setupBenchStore's own
// BEADS_BENCH_DOLT_PORT opt-in (benchDoltServerPort) — never resolved from
// ambient BEADS_DOLT_SERVER_PORT/BEADS_DOLT_PORT, which a gc agent shell may
// point at a shared production server (be-cfm3z).
func BenchmarkLargeBatchApply_Dolt(b *testing.B) {
	for _, tc := range largeBatchApplyDoltShapes {
		b.Run(tc.name, func(b *testing.B) {
			store, cleanup := setupBenchStore(b)
			defer cleanup()

			ctx := context.Background()
			root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := store.CreateIssue(ctx, root, "bench"); err != nil {
				b.Fatalf("create root issue: %v", err)
			}

			var counts sqlcount.Counts
			var lastTotal int64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, err := measureDoltApply(ctx, store.connStr, root.ID, &counts, tc.build)
				if err != nil {
					b.Fatalf("measureDoltApply: %v", err)
				}
				lastTotal = got.Total()
			}
			b.StopTimer()
			b.ReportMetric(float64(lastTotal), "statements/op")
		})
	}
}
