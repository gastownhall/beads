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
		name: large712ShapeName,
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

// TestLargeBatchApplyStatementCounts{356,712,Classic40}_Embedded (below)
// jointly pin the ACTUAL number of SQL statements issueops.ApplyBatchInTx
// issues on the embedded backend, for each of three measured shapes (slice
// B0; originally one test, TestLargeBatchApplyStatementCounts_Embedded, see
// its split's doc comment below for why it is now 3). Together they are a
// REGRESSION BASELINE for B2 (a later, lighter fast path): B2 must lower
// these numbers, and these tests are what prove it did.
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

// large712ShapeName is largeBatchApplyShapes' "712 (mol 2x)" entry name,
// shared between the race-skip check and the zero-match guard in
// runLargeBatchApplyStatementCountsShape below so the two can never drift
// apart (see N2 in the F1 CI-speed review): if this shape is ever renamed in
// largeBatchApplyShapes, both call sites must be updated together, and the
// zero-match guard below fails loudly instead of letting a stale name here
// silently skip the entire pinned assertion.
const large712ShapeName = "712 (mol 2x)"

// TestLargeBatchApplyStatementCounts356_Embedded,
// TestLargeBatchApplyStatementCounts712_Embedded and
// TestLargeBatchApplyStatementCountsClassic40_Embedded were split from
// TestLargeBatchApplyStatementCounts_Embedded (measured ~350.25s under
// --config=embedded: 81.03s + 260.70s + 8.52s for the 356/712/classic-40
// shapes respectively) into 3 top-level tests, one per shape, for CI shard
// balance (see scripts/ci/embedded_storage_test_durations.json and
// engdocs/TESTING.md, slice F1). Each runs exactly one disjoint element of
// largeBatchApplyShapes, selected by name (not index) so the split stays
// correct if the shared shapes slice is reordered; the union of shapes run
// is identical to the original loop's, each exactly once.
//
// The 712 (mol 2x) shape alone still measures ~260-300s: it is a single
// pinned statement-count assertion over one indivisible ApplyBatchInTx
// transaction (no internal sub-cases to split further), a genuine, CPU-bound
// cost: 12790 real SQL statement round-trips through the race-instrumented
// in-process Dolt engine. It is skipped under -race below, following the
// exact precedent TestLargeBatchApplyWallClock_Embedded set for its
// 1000-item shape: the cost here is race-instrumentation overhead on the
// engine's own internal goroutine/lock machinery, not on anything this
// test's own logic does, and unlike the wall-clock test this one DOES have a
// real pass/fail assertion (the pinned Total() above), so skipping it under
// race is a deliberate reduction from per-PR to nightly-only coverage for
// this specific regression check — not a weakening of the assertion itself.
// nightly.yml's "Embedded Dolt batch-apply suite (non-race)" step (the
// nightly embedded non-race lane from #7128) has its -run regex extended
// alongside this change to include this test, so the full 12790-statement
// pinned baseline still runs, non-race, every night.
func runLargeBatchApplyStatementCountsShape(t *testing.T, shapeName string) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	matched := false
	for _, tc := range largeBatchApplyShapes {
		if tc.name != shapeName {
			continue
		}
		matched = true
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == large712ShapeName && raceEnabled {
				t.Skip("712-item shape's statement-count assertion skipped under -race (race-instrumentation overhead on the Dolt engine itself, not test logic); nightly.yml's Embedded Dolt batch-apply suite (non-race) step runs the full pinned assertion nightly instead; see doc comment above")
			}
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
	if !matched {
		// N2 (F1 review): a renamed/typo'd shapeName argument used to just
		// silently iterate zero times and PASS with zero subtests run,
		// looking identical to a legitimately skipped run. Fail loudly
		// instead so a drift between this function's callers and
		// largeBatchApplyShapes' names (or large712ShapeName above) is
		// caught immediately.
		t.Fatalf("no shape named %q in largeBatchApplyShapes; this test ran zero subtests", shapeName)
	}
}

func TestLargeBatchApplyStatementCounts356_Embedded(t *testing.T) {
	runLargeBatchApplyStatementCountsShape(t, "356 (mol 1x)")
}

func TestLargeBatchApplyStatementCounts712_Embedded(t *testing.T) {
	runLargeBatchApplyStatementCountsShape(t, large712ShapeName)
}

func TestLargeBatchApplyStatementCountsClassic40_Embedded(t *testing.T) {
	runLargeBatchApplyStatementCountsShape(t, "40 (classic)")
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
//
// The 1000-item shape is skipped under -race: that shape alone was observed
// taking 597-651s under the embedded tier's --config=embedded (race-enabled)
// CI lane, a multi-x inflation from race-instrumenting the in-process Dolt
// engine's own internal goroutine/lock machinery on every one of its
// hundreds of internal statements, not from anything this test asserts (it
// has no duration or count assertion to weaken). It twice pushed its shard
// over the job's 19-minute test timeout (see embedded-storage-test-shards.txt
// and this package's TestBatchApplyContract for the sibling case). The
// 356-item shape (the design's primary measured shape) always runs, race or
// not — only the 1000-item shape is conditionally skipped above.
//
// Coverage accounting for the skipped 1000-item shape (no assertion is
// weakened here — this test logs timing only — but the real question is
// what still exercises a true 1000-item apply at all):
//   - The shared inner write body (internal/storage/issueops.ApplyBatchInTx),
//     used by both this package's BatchApplier and internal/storage/dolt's,
//     still gets a real, full 1000-item apply with result assertions,
//     non-race, via internal/storage/dolt's own
//     TestBatchApplyContract/BoundsTheItemCount. That job runs unconditionally
//     on merge_group and push, but is conditional on PRs (gated by
//     detect-ci-tier's full_embedded output; see
//     .github/scripts/ci-embedded-tier.sh) — so "every PR" overstates it.
//   - This package's OWN wrapper around that body (the
//     version-commit-published-after-the-tx mechanism unique to the embedded
//     backend) does NOT get a full 1000-item apply under -race anymore: this
//     package's own TestBatchApplyContract/BoundsTheItemCount applies 150
//     items under -race and the full 1000 only when built without -race (see
//     its doc comment and conformance.RunBatchApplyBoundsTheItemCountAtScale).
//     So as of that change, the largest real, full-assertion apply embedded's
//     own wrapper gets under -race, anywhere in CI, is 150 items; without
//     -race it still gets the full 1000 in that same subtest. This is a
//     known, deliberate gap for the race-enabled lane specifically, not an
//     oversight — closing it needs a dedicated non-race embedded run (see the
//     nightly workflow step added alongside this comment).
func TestLargeBatchApplyWallClock_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	for _, tc := range wallClockShapes {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "1000" && raceEnabled {
				t.Skip("1000-item shape skipped under -race for wall-clock timing; see doc comment above for what still covers a real 1000-item apply")
			}
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
//
// Re-pinned for the batch-create round-trip work (was 7009 / 14014 / 846):
//   - ExecuteCreate hands its own BatchContext to the batch body instead of
//     letting it re-read the same config: -6 statements per create item
//     (356: 102 creates, -612; 712: 204, -1224; classic: 10, -60).
//   - A create's blocked-state recompute first probes which of its ids have
//     a dependency row of their own (+1). An id without one — every freshly
//     created item — then gets one plain UPDATE instead of the two union
//     UPDATEs: net 0, and the dropped statements were the expensive ones.
//     Recomputes outside the create paths (dep adds, updates, closes) run no
//     probe and are unchanged.
//
// Net: 356 -613 (incl. the documented 1-statement jitter), 712 -1224,
// classic -60.
var pinnedEmbeddedStatementCounts = map[string]int64{
	"356 (mol 1x)":    6396,
	large712ShapeName: 12790,
	"40 (classic)":    786,
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
