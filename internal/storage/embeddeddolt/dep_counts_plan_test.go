//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
)

// explainPlanOrFatal is explainPlan's strict twin, for statements this file
// GATES on. A plan with no IndexedTableAccess at all is a full scan — exactly
// the failure mode the gate exists to catch — so an unrecognized plan must
// fail the test, never skip: a skip here would silently pass the old
// COALESCE-filtered counts, whose plans are scans of the whole table
// (gascity#7666). The lenient twin skips because its assertions are
// opportunistic; these are not.
func explainPlanOrFatal(t *testing.T, tx *sql.Tx, st recordedStatement) string {
	t.Helper()
	rows, err := tx.QueryContext(context.Background(), "EXPLAIN PLAN "+st.query, st.args...)
	if err != nil {
		t.Fatalf("EXPLAIN PLAN: %v\n%s", err, st.query)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line sql.NullString
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

// assertBoundedTargetPlan gates one durable-plane count plan: every guarded
// branch of the per-column union must reach dependencies through an index
// whose last key column is the branch's bare target column —
// idx_dep_<plane>_target, or the idx_dep_type_<plane> composite with the
// target still range-bounded — with a key range holding the requested id,
// never a table-wide scan. (The durable plane has no generated depends_on_id
// column since migration 0043; its wisp twin is why the wisp plane is
// report-only here.)
func assertBoundedTargetPlan(t *testing.T, name, plan, target string) {
	t.Helper()
	if strings.Contains(plan, "TableScan(dependencies)") {
		t.Errorf("%s scans the whole dependencies table:\n%s", name, plan)
	}
	if got := strings.Count(plan, "IndexedTableAccess(dependencies)"); got != 3 {
		t.Errorf("%s has %d IndexedTableAccess(dependencies) nodes, want 3 (one guarded branch per target column):\n%s", name, got, plan)
	}
	for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
		bare := "index: [dependencies." + col + "]"
		typed := "index: [dependencies.type,dependencies." + col + "]"
		if !strings.Contains(plan, bare) && !strings.Contains(plan, typed) {
			t.Errorf("%s: the %s branch is not served by its target index:\n%s", name, col, plan)
		}
	}
	// A bounded, not prefix-only, probe: each branch's access carries a point
	// range on the requested id ([id, id] inside its filters), so the target's
	// range text shows up once per branch.
	if got := strings.Count(plan, "["+target+", "+target+"]"); got < 3 {
		t.Errorf("%s: a key range bounded to %q appears %d times, want at least 3 (one per branch):\n%s", name, target, got, plan)
	}
}

// TestDependentCountPlansUseTargetIndexes is the EXPLAIN evidence for
// gascity#7666: over a populated, indexed durable dependencies table, the
// changed inbound count statements — CountDependencyEdgesInTx's In direction
// with and without a type filter, and CountDependentsByStatusInTx — must plan
// each guarded union branch as an index lookup bounded to the requested id,
// not the table-wide scan the old COALESCE target filter forced. Parity of
// what the statements RETURN is pinned by the Coalesce-oracle tests; this
// test pins how the engine may execute them.
//
// The wisp plane is reported, not gated: pristine wisp_dependencies carries
// only type-leading composite indexes (migration 0021), no leading-target
// index, so its plans legitimately scan; adding one is a schema decision this
// change deliberately does not make. The wisp statements' plans are logged so
// the gap stays visible.
func TestDependentCountPlansUseTargetIndexes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	fixture := newPristineEmbeddedDoltFixture(t, "dcplan")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()

	const n = 2000
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Bulk durable backdrop: issues plus issue-targeted edges (issue i blocks
	// on (i-1)/2, plus a back edge), all sources open.
	for _, st := range batchfixtures.LargeGraphInserts("dcp", n) {
		if _, err := tx.ExecContext(ctx, st.SQL, st.Args...); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	id := func(i int) string { return batchfixtures.LargeGraphID("dcp", i) }
	target := id(0)

	// Dual-home target into wisps so rows in depends_on_wisp_id satisfy the
	// foreign key (the oracle fixture's trick), then populate every target
	// column of the durable plane: 120 wisp-target rows and 120 external
	// rows whose external value is the local id itself — the collision case.
	// Disjoint source ranges keep the (issue_id, depends_on_id) primary key
	// happy: a source's wisp-column and external-column rows would both
	// generate the same depends_on_id.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wisps (id, title, status) VALUES (?, ?, ?)`,
		target, "dual-homed wisp row", string(types.StatusOpen)); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	const handRows = 120
	depTypes := []string{string(types.DepBlocks), string(types.DepWaitsFor), string(types.DepRelated)}
	for kind, targetCol := range []string{"depends_on_wisp_id", "depends_on_external"} {
		values := make([]string, handRows)
		args := make([]any, 0, handRows*5)
		for i := 0; i < handRows; i++ {
			values[i] = "(?, ?, ?, ?, 'tester')"
			args = append(args, fmt.Sprintf("dcp-k%d-%04d", kind, i), id(1000+kind*handRows+i), target, depTypes[i%3])
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dependencies (id, issue_id, `+targetCol+`, type, created_by) VALUES `+strings.Join(values, ", "),
			args...); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	// Wisp-plane backdrop for the report leg: one wisp source per row, all
	// targeting the issue id through depends_on_issue_id (the plane's unique
	// key is (issue_id, depends_on_issue_id), so sources cannot repeat).
	values := make([]string, handRows)
	args := make([]any, 0, handRows*3)
	for i := 0; i < handRows; i++ {
		values[i] = "(?, ?, ?)"
		args = append(args, fmt.Sprintf("dcpw-%03d", i), fmt.Sprintf("wisp source %d", i), string(types.StatusOpen))
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wisps (id, title, status) VALUES `+strings.Join(values, ", "), args...); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	values = make([]string, handRows)
	args = make([]any, 0, handRows*5)
	for i := 0; i < handRows; i++ {
		values[i] = "(?, ?, ?, ?, 'tester')"
		args = append(args, fmt.Sprintf("dcp-ww-%04d", i), fmt.Sprintf("dcpw-%03d", i), id(0), depTypes[i%3])
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES `+strings.Join(values, ", "),
		args...); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	// Each count under the recorder, timed (timings are report-only
	// evidence; the gates are the plans).
	record := func(name string, run func(rec *statementRecorder) error) (durable, wisp recordedStatement, elapsed time.Duration) {
		t.Helper()
		rec := &statementRecorder{DBTX: tx}
		start := time.Now()
		if err := run(rec); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		elapsed = time.Since(start)
		if len(rec.stmts) != 2 {
			t.Fatalf("%s recorded %d statements, want 2 (durable count, then wisp count): %+v", name, len(rec.stmts), rec.stmts)
		}
		return rec.stmts[0], rec.stmts[1], elapsed
	}

	// Fixture sanity, so the EXPLAINs below ran against a populated table:
	// durable 2 (backdrop edges into id 0: issues 1 and 2) + 120 + 120, wisp
	// 120. The blocks filter keeps each kind's every-third row plus the two
	// backdrop edges (both blocks).
	t.Run("edges unfiltered", func(t *testing.T) {
		var got int64
		durable, wisp, elapsed := record("inbound edges", func(rec *statementRecorder) error {
			var err error
			got, err = issueops.CountDependencyEdgesInTx(ctx, rec, target, domain.DepDirectionIn, nil)
			return err
		})
		if got != 362 {
			t.Fatalf("CountDependencyEdgesInTx In(%s) = %d, want 362 — fixture drifted", target, got)
		}
		t.Logf("inbound edge count took %v", elapsed)
		assertBoundedTargetPlan(t, "inbound edge count", explainPlanOrFatal(t, tx, durable), target)
		t.Logf("wisp-plane inbound edge count plan (report only; pristine wisp_dependencies has no leading-target index):\n%s", explainPlan(t, tx, wisp))
	})

	t.Run("edges type-filtered", func(t *testing.T) {
		var got int64
		durable, wisp, elapsed := record("filtered inbound edges", func(rec *statementRecorder) error {
			var err error
			got, err = issueops.CountDependencyEdgesInTx(ctx, rec, target, domain.DepDirectionIn, []types.DependencyType{types.DepBlocks})
			return err
		})
		if got != 122 {
			t.Fatalf("CountDependencyEdgesInTx In(%s, blocks) = %d, want 122 — fixture drifted", target, got)
		}
		t.Logf("type-filtered inbound edge count took %v", elapsed)
		assertBoundedTargetPlan(t, "type-filtered inbound edge count", explainPlanOrFatal(t, tx, durable), target)
		t.Logf("wisp-plane type-filtered inbound edge count plan (report only):\n%s", explainPlan(t, tx, wisp))
	})

	t.Run("dependents by status", func(t *testing.T) {
		var got int64
		durable, wisp, elapsed := record("dependents by status", func(rec *statementRecorder) error {
			var err error
			got, err = issueops.CountDependentsByStatusInTx(ctx, rec, target, types.StatusOpen)
			return err
		})
		if got != 362 {
			t.Fatalf("CountDependentsByStatusInTx(%s, open) = %d, want 362 — fixture drifted", target, got)
		}
		t.Logf("dependents-by-status count took %v", elapsed)
		assertBoundedTargetPlan(t, "dependents-by-status count", explainPlanOrFatal(t, tx, durable), target)
		t.Logf("wisp-plane dependents-by-status plan (report only):\n%s", explainPlan(t, tx, wisp))
	})
}
