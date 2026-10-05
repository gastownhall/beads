package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/depid"
)

// The re-key scan reads the typed target columns rather than COALESCEing them,
// because the duplicate-edge merge has to know which column held the target and
// which type it would discard (gastownhall/beads#5268).
const depScanPattern = `SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type FROM `

func depScanRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external", "type"})
}

// expectDepScan primes the id-column probe (expectColumnExists, shared with
// lock_test.go's under-lock statement sequence) and the table scan. mock is in
// sqlmock's default ordered mode, so the Exec expectations each test adds after
// this also pin the statement ORDER — which is load-bearing here: a DELETE that
// runs after its UPDATE re-creates the duplicate-primary-key abort, and an
// UPDATE that runs before the update vacating its target id does the same.
func expectDepScan(mock sqlmock.Sqlmock, table string, rows *sqlmock.Rows) {
	expectColumnExists(mock, true)
	mock.ExpectQuery(regexp.QuoteMeta(depScanPattern + table)).WillReturnRows(rows)
}

func expectDepDelete(mock sqlmock.Sqlmock, table string, ids ...string) {
	args := make([]driver.Value, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	mock.ExpectExec(regexp.QuoteMeta(fmt.Sprintf("DELETE FROM %s WHERE id IN (%s)", table, placeholders))).
		WithArgs(args...).
		WillReturnResult(sqlmock.NewResult(0, int64(len(ids))))
}

func expectDepUpdate(mock sqlmock.Sqlmock, table, newID, oldID string) {
	mock.ExpectExec(regexp.QuoteMeta(fmt.Sprintf("UPDATE %s SET id = ? WHERE id = ?", table))).
		WithArgs(newID, oldID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// rekeyOneTable plans and executes a single table, the shape most of these tests
// exercise. Production code always plans BOTH tables before executing either
// (TestRekeyDependencyIDsPlansBothTablesBeforeWriting covers that).
func rekeyOneTable(db DBConn, table string) (bool, error) {
	plan, err := planDependencyTableRekey(context.Background(), db, table)
	if err != nil {
		return false, err
	}
	return plan.execute(context.Background(), db)
}

func newDepMock(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	})
	return db, mock
}

// --- statement-shape tests -------------------------------------------------

// TestRekeyDependencyTableRewritesOnlyDivergentRows verifies the backfill that
// converges existing rows after the #4259 fix: it re-keys a row whose id is not
// the deterministic value, and leaves an already-deterministic row untouched.
func TestRekeyDependencyTableRewritesOnlyDivergentRows(t *testing.T) {
	db, mock := newDepMock(t)

	randomRow := "random-uuid-aaaa"
	deterministicRow := depid.New("c", "d")
	expectDepScan(mock, "dependencies", depScanRows().
		AddRow(randomRow, "a", "b", nil, nil, "blocks").
		AddRow(deterministicRow, "c", "d", nil, nil, "blocks"))
	expectDepUpdate(mock, "dependencies", depid.New("a", "b"), randomRow)

	wrote, err := rekeyOneTable(db, "dependencies")
	if err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true when a row was re-keyed")
	}
}

// TestRekeyDependencyTableSkipsMissingTable verifies the backfill no-ops cleanly
// when the table/id column is absent (older or partial schema).
func TestRekeyDependencyTableSkipsMissingTable(t *testing.T) {
	db, mock := newDepMock(t)
	expectColumnExists(mock, false)

	wrote, err := rekeyOneTable(db, "dependencies")
	if err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false when the id column is absent")
	}
}

// TestRekeyDependencyTableIdempotent verifies that when every row already carries
// its deterministic id, no statement is issued at all.
func TestRekeyDependencyTableIdempotent(t *testing.T) {
	db, mock := newDepMock(t)
	expectDepScan(mock, "dependencies", depScanRows().
		AddRow(depid.New("a", "b"), "a", "b", nil, nil, "blocks").
		AddRow(depid.New("a", "external:e1"), "a", nil, nil, "external:e1", "related").
		AddRow(depid.New("b", "w1"), "b", nil, "w1", nil, "blocks"))
	// No ExpectExec: zero writes expected.

	wrote, err := rekeyOneTable(db, "dependencies")
	if err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false on a converged table")
	}
}

// TestRekeyDependencyTableMergesFreshDuplicatePair is the reported abort in its
// simplest form: the same logical edge recorded once as an issue ref and once as
// an external ref, both still carrying random ids. Both derive the same
// deterministic id, which is what made the old row-at-a-time rewrite die on the
// second UPDATE.
func TestRekeyDependencyTableMergesFreshDuplicatePair(t *testing.T) {
	db, mock := newDepMock(t)
	expectDepScan(mock, "dependencies", depScanRows().
		AddRow("random-issue-row", "hq-cv-vvnmq", "ops-w05", nil, nil, "blocks").
		AddRow("random-external-row", "hq-cv-vvnmq", nil, nil, "ops-w05", "blocks"))
	expectDepDelete(mock, "dependencies", "random-external-row")
	expectDepUpdate(mock, "dependencies", depid.New("hq-cv-vvnmq", "ops-w05"), "random-issue-row")

	wrote, err := rekeyOneTable(db, "dependencies")
	if err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true when a duplicate pair was merged")
	}
}

// TestRekeyDependencyTableMergesHalfRekeyedPairLoserHoldsID covers the database
// a pre-1.3.0 binary left half-re-keyed with the LOSER moved first: the external
// twin already sits on the deterministic id and the surviving issue row does not.
// The twin still loses — the survivor rule is the typed column, not who got
// there first — so the DELETE has to run before the UPDATE that reclaims the id.
func TestRekeyDependencyTableMergesHalfRekeyedPairLoserHoldsID(t *testing.T) {
	db, mock := newDepMock(t)
	want := depid.New("a", "t")
	expectDepScan(mock, "dependencies", depScanRows().
		AddRow("random-issue-row", "a", "t", nil, nil, "blocks").
		AddRow(want, "a", nil, nil, "t", "blocks"))
	expectDepDelete(mock, "dependencies", want)
	expectDepUpdate(mock, "dependencies", want, "random-issue-row")

	if _, err := rekeyOneTable(db, "dependencies"); err != nil {
		t.Fatalf("rekey: %v", err)
	}
}

// TestRekeyDependencyTableMergesTripleDuplicate exercises the full survivor
// priority and the delete chunking: the same target in all three typed columns
// collapses to the issue ref with both losers dropped in ONE statement.
func TestRekeyDependencyTableMergesTripleDuplicate(t *testing.T) {
	db, mock := newDepMock(t)
	expectDepScan(mock, "dependencies", depScanRows().
		AddRow("row-issue", "a", "t", nil, nil, "blocks").
		AddRow("row-wisp", "a", nil, "t", nil, "blocks").
		AddRow("row-external", "a", nil, nil, "t", "blocks"))
	expectDepDelete(mock, "dependencies", "row-external", "row-wisp")
	expectDepUpdate(mock, "dependencies", depid.New("a", "t"), "row-issue")

	if _, err := rekeyOneTable(db, "dependencies"); err != nil {
		t.Fatalf("rekey: %v", err)
	}
}

// TestRekeyDependencyTableChunksDeletes pins that a large merge does not issue
// one working-set flush per row: the victims of #5268 are exactly the databases
// with thousands of divergent rows, and this pass runs at open.
func TestRekeyDependencyTableChunksDeletes(t *testing.T) {
	db, mock := newDepMock(t)

	rows := depScanRows()
	var losers []string
	for i := 0; i < depDeleteChunk+5; i++ {
		issue := fmt.Sprintf("i%03d", i)
		// Each pair is one logical edge duplicated across two typed columns, so
		// each contributes exactly one loser and one already-correct survivor.
		rows.AddRow(depid.New(issue, "t"), issue, "t", nil, nil, "blocks")
		loser := fmt.Sprintf("loser-%03d", i)
		rows.AddRow(loser, issue, nil, nil, "t", "blocks")
		losers = append(losers, loser)
	}
	expectDepScan(mock, "dependencies", rows)
	slices.Sort(losers)
	expectDepDelete(mock, "dependencies", losers[:depDeleteChunk]...)
	expectDepDelete(mock, "dependencies", losers[depDeleteChunk:]...)

	if _, err := rekeyOneTable(db, "dependencies"); err != nil {
		t.Fatalf("rekey: %v", err)
	}
}

// TestRekeyDependencyTableRefusalWithPendingDeletesWritesNothing is the
// all-or-nothing contract at its only interesting point: a table that holds BOTH
// a mergeable duplicate (so the plan has deletes) and an unresolvable squat. A
// pass that validated after deleting would drop rows and then abort.
func TestRekeyDependencyTableRefusalWithPendingDeletesWritesNothing(t *testing.T) {
	db, mock := newDepMock(t)
	expectDepScan(mock, "dependencies", depScanRows().
		// A mergeable duplicate pair: contributes a DELETE and an UPDATE.
		AddRow("dup-issue", "a", "t", nil, nil, "blocks").
		AddRow("dup-external", "a", nil, nil, "t", "blocks").
		// A malformed row squatting on the id that b -> u must move to.
		AddRow("rand-b", "b", "u", nil, nil, "blocks").
		AddRow(depid.New("b", "u"), "squatter", nil, nil, nil, "blocks"))
	// No ExpectExec at all: validation runs before the first statement.

	wrote, err := rekeyOneTable(db, "dependencies")
	if err == nil {
		t.Fatal("expected a refusal when a malformed row squats a planned id")
	}
	if wrote {
		t.Error("expected wrote=false when the plan was rejected before any write")
	}
}

// TestRekeyDependencyIDsPlansBothTablesBeforeWriting pins the cross-table
// contract: a refusal on wisp_dependencies must not leave the synced
// dependencies table already rewritten, because those writes would sit
// uncommitted (the pass aborts before MigrateUp's DOLT_COMMIT) and the recovery
// pass would classify them as pre-existing dirty and skip them again.
func TestRekeyDependencyIDsPlansBothTablesBeforeWriting(t *testing.T) {
	db, mock := newDepMock(t)
	expectDepScan(mock, "dependencies", depScanRows().
		AddRow("random-row", "a", "t", nil, nil, "blocks"))
	expectDepScan(mock, "wisp_dependencies", depScanRows().
		AddRow("rand-b", "b", "u", nil, nil, "blocks").
		AddRow(depid.New("b", "u"), "squatter", nil, nil, nil, "blocks"))
	// No ExpectExec: the dependencies plan is valid but must not execute.

	wrote, err := rekeyDependencyIDs(context.Background(), db, nil)
	if err == nil {
		t.Fatal("expected the wisp_dependencies refusal to abort the pass")
	}
	if wrote {
		t.Error("expected wrote=false: no table may be written when either plan is refused")
	}
	var conflict *DependencyRekeyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error %v is not a *DependencyRekeyConflictError", err)
	}
	if conflict.Table != "wisp_dependencies" {
		t.Errorf("conflict.Table = %q, want wisp_dependencies", conflict.Table)
	}
}

// TestRekeyDependencyIDsRefusesToWriteIntoDirtyTable covers the marker-vs-guard
// hazard: MigrateUp's changed-signature guard runs AFTER ignoredSource.migrate
// has durably recorded the 0026 marker, so a re-key that wrote into a
// pre-existing dirty table would fail the open once and never retry, leaving the
// heal to ride an unrelated user commit. Refusing at plan time keeps the pass
// write-free and retryable, and DirtyTablesError is the type whose documented
// recovery (bd dolt commit) already covers it.
func TestRekeyDependencyIDsRefusesToWriteIntoDirtyTable(t *testing.T) {
	db, mock := newDepMock(t)
	expectDepScan(mock, "dependencies", depScanRows().
		AddRow("random-row", "a", "t", nil, nil, "blocks"))
	expectDepScan(mock, "wisp_dependencies", depScanRows())
	// No ExpectExec: the plan is valid but must not execute.

	dirty := map[string]dirtyTableState{"dependencies": {}}
	wrote, err := rekeyDependencyIDs(context.Background(), db, dirty)
	if err == nil {
		t.Fatal("expected a refusal when the re-key would write into a dirty table")
	}
	if wrote {
		t.Error("expected wrote=false")
	}
	var dirtyErr *DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("error %v is not a *DirtyTablesError", err)
	}
	if !slices.Equal(dirtyErr.Tables, []string{"dependencies"}) {
		t.Errorf("dirtyErr.Tables = %v, want [dependencies]", dirtyErr.Tables)
	}
}

// TestRekeyDependencyIDsIgnoresDirtyTableWithNothingToDo keeps the refusal
// scoped to passes that actually write: a converged clone whose dependencies
// table happens to be dirty must still open.
func TestRekeyDependencyIDsIgnoresDirtyTableWithNothingToDo(t *testing.T) {
	db, mock := newDepMock(t)
	expectDepScan(mock, "dependencies", depScanRows().
		AddRow(depid.New("a", "t"), "a", "t", nil, nil, "blocks"))
	expectDepScan(mock, "wisp_dependencies", depScanRows())

	wrote, err := rekeyDependencyIDs(context.Background(), db, map[string]dirtyTableState{"dependencies": {}})
	if err != nil {
		t.Fatalf("rekeyDependencyIDs: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false")
	}
}

// --- planner tests ---------------------------------------------------------

func depIssueRow(id, issueID, target string) depRow {
	return depRow{id: id, issueID: issueID, target: target, depType: "blocks", targetCount: 1, priority: 0}
}

func depWispRow(id, issueID, target string) depRow {
	return depRow{id: id, issueID: issueID, target: target, depType: "blocks", targetCount: 1, priority: 1}
}

func depExternalRow(id, issueID, target string) depRow {
	return depRow{id: id, issueID: issueID, target: target, depType: "blocks", targetCount: 1, priority: 2}
}

func mustPlan(t *testing.T, rows []depRow) *depPlan {
	t.Helper()
	plan, err := planDependencyRekey("dependencies", rows)
	if err != nil {
		t.Fatalf("planDependencyRekey: %v", err)
	}
	return plan
}

func planUpdatePairs(plan *depPlan) [][2]string {
	out := make([][2]string, 0, len(plan.updates))
	for _, u := range plan.updates {
		out = append(out, [2]string{u.oldID, u.newID})
	}
	return out
}

// TestPlanDependencyRekeyOrdersRenameChain is the workflow the refusal used to
// brick, reproduced at the planner: `bd rename A B` leaves the edge's id derived
// from A while its target column already reads B (the FK cascade beat the
// rewrite), and a later `bd rename C A` puts a second row on the id the first one
// needs. Neither row is corrupt and the state is mechanically convergible —
// vacate the first, then move the second.
func TestPlanDependencyRekeyOrdersRenameChain(t *testing.T) {
	staleAfterFirstRename := depid.New("src", "A")
	rows := []depRow{
		// Renamed A -> B: id still derived from A, target column already B.
		depIssueRow(staleAfterFirstRename, "src", "B"),
		// Renamed C -> A: id still derived from C, target column already A.
		depIssueRow(depid.New("src", "C"), "src", "A"),
	}
	plan := mustPlan(t, rows)

	if len(plan.deletes) != 0 {
		t.Errorf("deletes = %v, want none: neither row is a duplicate", plan.deletes)
	}
	want := [][2]string{
		{staleAfterFirstRename, depid.New("src", "B")},
		{depid.New("src", "C"), depid.New("src", "A")},
	}
	if got := planUpdatePairs(plan); !slices.Equal(got, want) {
		t.Fatalf("updates = %v, want %v (the occupant must vacate first)", got, want)
	}
}

// TestPlanDependencyRekeyOrdersLongerChain keeps the ordering honest beyond the
// two-row case: a three-link chain must come out head-first, not in the sorted
// order the candidates were built in.
func TestPlanDependencyRekeyOrdersLongerChain(t *testing.T) {
	// c holds b's wanted id, b holds a's wanted id, a's wanted id is free.
	idA, idB, idC := depid.New("s", "a"), depid.New("s", "b"), depid.New("s", "c")
	rows := []depRow{
		depIssueRow(idC, "s", "b"), // wants idB, currently holds idC
		depIssueRow(idB, "s", "a"), // wants idA, currently holds idB
		depIssueRow("free-random", "s", "c"),
	}
	plan := mustPlan(t, rows)

	want := [][2]string{
		{idB, idA},           // idA is free
		{idC, idB},           // now idB is free
		{"free-random", idC}, // now idC is free
	}
	if got := planUpdatePairs(plan); !slices.Equal(got, want) {
		t.Fatalf("updates = %v, want %v", got, want)
	}
}

// TestPlanDependencyRekeyRefusesCycle is the one ordering that has no solution
// without a temporary id: two rows each holding exactly the id the other needs.
func TestPlanDependencyRekeyRefusesCycle(t *testing.T) {
	idA, idB := depid.New("s", "a"), depid.New("s", "b")
	rows := []depRow{
		depIssueRow(idA, "s", "b"),
		depIssueRow(idB, "s", "a"),
	}
	_, err := planDependencyRekey("dependencies", rows)
	if err == nil {
		t.Fatal("expected a refusal for a rotation with no free id")
	}
	var conflict *DependencyRekeyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error %v is not a *DependencyRekeyConflictError", err)
	}
	for _, want := range []string{"cycle", idA, idB, "bd doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestPlanDependencyRekeyRefusesMalformedSquatter pins the one refusal that
// survives the chain ordering. A converged row can never squat another edge's id
// (it would derive the same id and join that group), so the only squatters left
// are malformed rows — and the diagnostic must say so rather than render a
// dangling "issue x -> ".
func TestPlanDependencyRekeyRefusesMalformedSquatter(t *testing.T) {
	squatted := depid.New("a", "t")
	rows := []depRow{
		depIssueRow("random-row", "a", "t"),
		{id: squatted, issueID: "orphan", depType: "blocks", targetCount: 0, priority: 3},
	}
	_, err := planDependencyRekey("dependencies", rows)
	if err == nil {
		t.Fatal("expected a refusal when a malformed row squats a planned id")
	}
	for _, want := range []string{"random-row", squatted, "no dependency target", "bd doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "dependencies: dependencies:") {
		t.Errorf("error %q double-stamps the table name", err)
	}
	if strings.Contains(err.Error(), "-> )") {
		t.Errorf("error %q renders a dangling target arrow", err)
	}
}

// TestPlanDependencyRekeyLeavesMalformedRows keeps the "surface, don't guess"
// contract symmetric. A row with no target was always skipped; a row with
// SEVERAL non-null targets (reachable by merging drifted schemas or hand-repair
// SQL) must be skipped too, because truncating it to its first non-null column
// could now group it as a duplicate and DELETE it, silently destroying the edge
// its other column recorded.
func TestPlanDependencyRekeyLeavesMalformedRows(t *testing.T) {
	rows := []depRow{
		{id: "no-target", issueID: "a", depType: "blocks", targetCount: 0, priority: 3},
		{id: "multi-target", issueID: "a", target: "t", depType: "blocks", targetCount: 2, priority: 0},
		// A well-formed row on the same (issue, first-non-null target) as the
		// multi-target row. Without the carve-out the multi-target row joins this
		// group and loses on priority, and its second target is destroyed.
		depIssueRow(depid.New("a", "t"), "a", "t"),
	}
	plan := mustPlan(t, rows)

	if len(plan.deletes) != 0 {
		t.Errorf("deletes = %v, want none", plan.deletes)
	}
	if len(plan.updates) != 0 {
		t.Errorf("updates = %v, want none", planUpdatePairs(plan))
	}
}

// TestPlanDependencyRekeyRecordsMergedLosers pins the audit trail for the one
// thing the merge destroys. The twins can legitimately carry different types —
// each insert wrote its own caller's — and type drives readiness, so a dropped
// row has to be nameable after the fact.
func TestPlanDependencyRekeyRecordsMergedLosers(t *testing.T) {
	survivor := depIssueRow("issue-row", "a", "t")
	survivor.depType = "related"
	loser := depExternalRow("external-row", "a", "t")
	loser.depType = "blocks"
	plan := mustPlan(t, []depRow{survivor, loser})

	if len(plan.merges) != 1 {
		t.Fatalf("merges = %v, want exactly one", plan.merges)
	}
	m := plan.merges[0]
	if m.survivor.id != "issue-row" || m.survivor.depType != "related" {
		t.Errorf("survivor = %+v, want the issue-ref row keeping its own type", m.survivor)
	}
	if m.loser.id != "external-row" || m.loser.depType != "blocks" {
		t.Errorf("loser = %+v, want the external row with its dropped type", m.loser)
	}
	if m.issueID != "a" || m.target != "t" {
		t.Errorf("merge edge = %s -> %s, want a -> t", m.issueID, m.target)
	}
}

// TestPlanDependencyRekeySurvivorPriority states the merge rule directly, across
// every pairing, so a reordering of depTargetColumns cannot silently change
// which row survives.
func TestPlanDependencyRekeySurvivorPriority(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rows         []depRow
		wantSurvivor string
		wantDeletes  []string
	}{
		{
			name:         "issue beats wisp",
			rows:         []depRow{depWispRow("w", "a", "t"), depIssueRow("i", "a", "t")},
			wantSurvivor: "i",
			wantDeletes:  []string{"w"},
		},
		{
			name:         "issue beats external",
			rows:         []depRow{depExternalRow("e", "a", "t"), depIssueRow("i", "a", "t")},
			wantSurvivor: "i",
			wantDeletes:  []string{"e"},
		},
		{
			name:         "wisp beats external",
			rows:         []depRow{depExternalRow("e", "a", "t"), depWispRow("w", "a", "t")},
			wantSurvivor: "w",
			wantDeletes:  []string{"e"},
		},
		{
			name:         "issue beats both",
			rows:         []depRow{depExternalRow("e", "a", "t"), depWispRow("w", "a", "t"), depIssueRow("i", "a", "t")},
			wantSurvivor: "i",
			wantDeletes:  []string{"e", "w"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := mustPlan(t, tc.rows)
			if !slices.Equal(plan.deletes, tc.wantDeletes) {
				t.Errorf("deletes = %v, want %v", plan.deletes, tc.wantDeletes)
			}
			want := [][2]string{{tc.wantSurvivor, depid.New("a", "t")}}
			if got := planUpdatePairs(plan); !slices.Equal(got, want) {
				t.Errorf("updates = %v, want %v", got, want)
			}
		})
	}
}

// TestPlanDependencyRekeyIsOrderIndependent is the #4259 contract: the plan must
// be a pure function of table CONTENT, never of scan order, or two clones diverge.
func TestPlanDependencyRekeyIsOrderIndependent(t *testing.T) {
	rows := []depRow{
		depIssueRow("r1", "a", "t"),
		depExternalRow("r2", "a", "t"),
		depWispRow("r3", "b", "w1"),
		depIssueRow(depid.New("c", "d"), "c", "d"),
		depIssueRow(depid.New("s", "a"), "s", "b"),
		depIssueRow(depid.New("s", "c"), "s", "a"),
	}
	want := mustPlan(t, rows)
	reversed := slices.Clone(rows)
	slices.Reverse(reversed)
	got := mustPlan(t, reversed)

	if !slices.Equal(got.deletes, want.deletes) {
		t.Errorf("deletes = %v, want %v", got.deletes, want.deletes)
	}
	if !slices.Equal(planUpdatePairs(got), planUpdatePairs(want)) {
		t.Errorf("updates = %v, want %v", planUpdatePairs(got), planUpdatePairs(want))
	}
}
