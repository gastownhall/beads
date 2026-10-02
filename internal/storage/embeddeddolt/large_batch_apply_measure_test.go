//go:build cgo

package embeddeddolt_test

// B0: measure the ACTUAL number of SQL statements issueops.ApplyBatchInTx
// issues on the embedded Dolt backend, for three measured plan shapes. See
// internal/storage/dolt/large_batch_apply_measure_test.go for the Dolt
// server (TCP) backend equivalent, and internal/storage/batchfixtures for
// the shared, backend-agnostic plan construction.
//
// This file opens a SECOND, counting-wrapped connection against an on-disk
// embedded directory a normal *EmbeddedDoltStore already migrated — exactly
// the pattern testEnv.exec/queryScalar in create_issue_test.go use for raw
// verification queries — because issueops.ApplyBatchInTx takes a concrete
// *sql.Tx and the store's own connections are not wrapped. It duplicates
// open.go's buildDSN rather than modifying OpenSQL to accept a connector
// hook: that would add a test-only seam to a production construction path
// for the sake of one measurement harness.

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	doltembed "github.com/dolthub/driver/v2"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"

	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// measureCommitName/measureCommitEmail mirror open.go's commitName/commitEmail,
// duplicated because those are unexported.
const (
	measureCommitName  = "beads"
	measureCommitEmail = "beads@local"
)

// measureDSN replicates open.go's buildDSN for a raw, counting-wrapped
// connection to an already-migrated embedded directory.
func measureDSN(dir, database string) string {
	v := url.Values{}
	v.Set(doltembed.CommitNameParam, measureCommitName)
	v.Set(doltembed.CommitEmailParam, measureCommitEmail)
	v.Set(doltembed.MultiStatementsParam, "true")
	if strings.TrimSpace(database) != "" {
		v.Set(doltembed.DatabaseParam, database)
	}
	path := dir
	if os.PathSeparator == '\\' {
		path = strings.ReplaceAll(path, `\`, `/`)
	}
	return "file://" + path + "?" + v.Encode()
}

// openCountedConn opens a counting-wrapped *sql.DB against dataDir and USEs
// database on it. The returned cleanup closes both the db and the
// connector; the caller drives transactions on the returned db itself.
func openCountedConn(ctx context.Context, dataDir, database string, counts *sqlcount.Counts) (*sql.DB, func(), error) {
	cfg, err := doltembed.ParseDSN(measureDSN(dataDir, database))
	if err != nil {
		return nil, nil, err
	}
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = 0
	bo.MaxInterval = 5 * time.Second
	cfg.BackOff = bo

	connector, err := doltembed.NewConnector(cfg)
	if err != nil {
		return nil, nil, err
	}
	wrapped := sqlcount.WrapConnector(connector, counts)
	db := sql.OpenDB(wrapped)
	// A single connection: PrepareContext/BeginTx on this db must land on
	// the same embedded session for USE below to still apply when the
	// measured transaction begins.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	cleanup := func() {
		_ = db.Close()
		_ = connector.Close()
	}

	if err := db.PingContext(ctx); err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, err := db.ExecContext(ctx, "USE `"+database+"`"); err != nil {
		cleanup()
		return nil, nil, err
	}
	return db, cleanup, nil
}

// measureApply runs build(rootID) through issueops.ApplyBatchInTx on a
// counting-wrapped connection to dataDir/database — which must already be a
// migrated embedded Dolt database containing an issue at rootID, or "" if
// the shape needs no root (ShapeClassic40) — and returns the resulting
// statement counts. Everything before the returned counts.Reset() point is
// setup and excluded; only the transaction ApplyBatchInTx runs inside is
// measured.
func measureApply(ctx context.Context, dataDir, database, rootID string, build func(rootID string) issueops.ApplyBatchRequest) (sqlcount.Snapshot, error) {
	counts := &sqlcount.Counts{}
	db, cleanup, err := openCountedConn(ctx, dataDir, database, counts)
	if err != nil {
		return sqlcount.Snapshot{}, err
	}
	defer cleanup()

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

// largeBatchApplyShapes is the design's three measured shapes, shared by the
// pinning test and the benchmark below.
var largeBatchApplyShapes = []struct {
	name  string
	db    string
	build func(rootID string) issueops.ApplyBatchRequest
}{
	{
		name: "356 (mol 1x)",
		db:   "m356",
		build: func(rootID string) issueops.ApplyBatchRequest {
			return batchfixtures.Shape356("tester", rootID)
		},
	},
	{
		name: "712 (mol 2x)",
		db:   "m712",
		build: func(rootID string) issueops.ApplyBatchRequest {
			return batchfixtures.Shape712("tester", rootID)
		},
	},
	{
		name: "40 (classic)",
		db:   "mclassic",
		build: func(string) issueops.ApplyBatchRequest {
			return batchfixtures.ShapeClassic40("tester")
		},
	},
}

// TestLargeBatchApplyStatementCounts_Embedded pins the ACTUAL number of SQL
// statements issueops.ApplyBatchInTx issues on the embedded backend, for
// each of three measured shapes (slice B0). It is a REGRESSION BASELINE for
// B2 (a later, lighter fast
// path): B2 must lower these numbers, and this test is what proves it did.
//
// The exact counts are backend- and Dolt-version-sensitive by nature — they
// come from real driver round trips, not a cost model — so a failure here
// after an unrelated Dolt/driver upgrade is expected and should be
// re-pinned with a note in the commit, not silently loosened to a range.
//
// statementCountTolerance absorbs one narrow, observed source of
// non-determinism: under host contention, the embedded engine occasionally
// issues one extra (or one fewer) internal Query round trip on the largest
// shape — most likely a lock-wait/retry poll, since the exact same input
// plan otherwise executes identically. Exec was observed PERFECTLY
// deterministic across >10 repeated runs of every shape; only Query ever
// moved, and only by 1, and only occasionally (roughly 1 run in 9 on the
// 356-item shape; 712 and classic-40 were never observed to vary). A wider
// drift than this tolerance is a real regression/improvement, not jitter,
// and must be re-measured and re-pinned deliberately.
const statementCountTolerance = 2

func TestLargeBatchApplyStatementCounts_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	for _, tc := range largeBatchApplyShapes {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newPristineEmbeddedDoltFixture(t, tc.db)
			t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })

			root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := fixture.store.CreateIssue(ctx, root, "tester"); err != nil {
				t.Fatalf("create root issue: %v", err)
			}

			got, err := measureApply(ctx, fixture.dataDir, fixture.database, root.ID, tc.build)
			if err != nil {
				t.Fatalf("measureApply: %v", err)
			}
			t.Logf("embedded %s: Prepare=%d Exec=%d Query=%d StmtExec=%d StmtQuery=%d Total=%d",
				tc.name, got.Prepare, got.Exec, got.Query, got.StmtExec, got.StmtQuery, got.Total())

			want, ok := pinnedEmbeddedStatementCounts[tc.name]
			if !ok {
				t.Fatalf("no pinned count for shape %q; run with -v, read the logged Total, and add it to pinnedEmbeddedStatementCounts", tc.name)
			}
			diff := got.Total() - want
			if diff < 0 {
				diff = -diff
			}
			if diff > statementCountTolerance {
				t.Errorf("embedded %s: Total() = %d, want pinned %d +/- %d (see large_batch_apply_measure_test.go)",
					tc.name, got.Total(), want, statementCountTolerance)
			}
		})
	}
}

// wallClockShapes is the coordinator's item-8 ask: real wall-clock numbers at
// 356 items (the design doc's primary measured shape) and 1000 items (the
// hard cap, issueops.MaxApplyBatchItems) — not just statement counts — so the
// server's --large-apply-ceiling default (5 minutes) is backed by an actual
// measurement at the top of the envelope, not only extrapolated from smaller
// shapes.
var wallClockShapes = []struct {
	name  string
	db    string
	build func(rootID string) issueops.ApplyBatchRequest
}{
	{name: "356", db: "wc356", build: func(rootID string) issueops.ApplyBatchRequest {
		return batchfixtures.Shape356("tester", rootID)
	}},
	{name: "1000", db: "wc1000", build: func(rootID string) issueops.ApplyBatchRequest {
		return batchfixtures.Shape1000("tester", rootID)
	}},
}

// TestLargeBatchApplyWallClock_Embedded measures (and simply logs, rather
// than asserting a bound on) how long issueops.ApplyBatchInTx actually takes
// on the embedded backend at 356 and 1000 items. It is deliberately NOT a
// pass/fail regression gate — wall-clock time is host- and CI-runner-
// sensitive in a way statement counts are not — but the numbers it logs are
// the real-data justification for httpapi.DefaultLargeApplyCeiling
// (internal/httpapi/server.go, 5 minutes): both measured shapes must
// complete in a small fraction of that budget for the ceiling to be
// generous headroom rather than a number picked out of thin air.
func TestLargeBatchApplyWallClock_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	for _, tc := range wallClockShapes {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newPristineEmbeddedDoltFixture(t, tc.db)
			t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })

			root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := fixture.store.CreateIssue(ctx, root, "tester"); err != nil {
				t.Fatalf("create root issue: %v", err)
			}

			plan := tc.build(root.ID)
			db, cleanup, err := openCountedConn(ctx, fixture.dataDir, fixture.database, &sqlcount.Counts{})
			if err != nil {
				t.Fatalf("openCountedConn: %v", err)
			}
			defer cleanup()

			uncapped := batchfixtures.UncappedPlan(plan)
			start := time.Now()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			if _, _, err := storageissueops.ApplyBatchInTx(ctx, tx, uncapped); err != nil {
				_ = tx.Rollback()
				t.Fatalf("ApplyBatchInTx: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			elapsed := time.Since(start)

			t.Logf("embedded wall-clock: %s items (%d actual) = %s (DefaultLargeApplyCeiling headroom: %.1fx)",
				tc.name, len(plan.Items), elapsed, (5*time.Minute).Seconds()/elapsed.Seconds())
		})
	}
}

// pinnedEmbeddedStatementCounts is the B0 regression baseline for the
// embedded backend: the Total() statement count issueops.ApplyBatchInTx
// issued for each measured shape, as last observed on this branch. B2 must
// lower these; a change for any other reason should be re-measured and
// re-pinned deliberately, not adjusted to make a failure go away.
var pinnedEmbeddedStatementCounts = map[string]int64{
	"356 (mol 1x)": 7009,
	"712 (mol 2x)": 14014,
	"40 (classic)": 846,
}

// BenchmarkLargeBatchApply_Embedded benchmarks issueops.ApplyBatchInTx on
// the embedded backend for each measured shape and reports the statement
// count alongside the usual timing, via b.ReportMetric. Each iteration
// builds and migrates a fresh, throwaway embedded database — not the
// pristine-template-clone fixture the *_test.go suite uses, because that
// helper is *testing.T-only — so per-iteration time includes that setup;
// -benchtime=Nx therefore matters more than a wall-clock target here.
func BenchmarkLargeBatchApply_Embedded(b *testing.B) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		b.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt benchmarks")
	}
	ctx := context.Background()

	for _, shape := range largeBatchApplyShapes {
		b.Run(shape.name, func(b *testing.B) {
			var totalStatements int64
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				beadsDir, err := os.MkdirTemp("", "beads-bench-embedded-*")
				if err != nil {
					b.Fatalf("MkdirTemp: %v", err)
				}
				store, err := embeddeddolt.Open(ctx, beadsDir, shape.db, "main")
				if err != nil {
					os.RemoveAll(beadsDir)
					b.Fatalf("Open: %v", err)
				}
				if err := store.SetConfig(ctx, "issue_prefix", shape.db); err != nil {
					store.Close()
					os.RemoveAll(beadsDir)
					b.Fatalf("SetConfig: %v", err)
				}
				root := &types.Issue{Title: "root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				if err := store.CreateIssue(ctx, root, "tester"); err != nil {
					store.Close()
					os.RemoveAll(beadsDir)
					b.Fatalf("create root issue: %v", err)
				}
				dataDir := filepath.Join(beadsDir, "embeddeddolt")
				b.StartTimer()

				got, err := measureApply(ctx, dataDir, shape.db, root.ID, shape.build)

				b.StopTimer()
				if err != nil {
					store.Close()
					os.RemoveAll(beadsDir)
					b.Fatalf("measureApply: %v", err)
				}
				totalStatements += got.Total()
				store.Close()
				os.RemoveAll(beadsDir)
				b.StartTimer()
			}
			if b.N > 0 {
				b.ReportMetric(float64(totalStatements)/float64(b.N), "statements/op")
			}
		})
	}
}
