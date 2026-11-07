package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestMigrationBodyPlanDifferentialMatchesFrozenBodies is the AC-3
// differential for gastownhall/beads#6520 (bd-f1s6sq): on identical small
// mixed graphs whose blocked flags are scrambled and whose updated_at
// values are fixed historical timestamps, executing the frozen body and the
// plan-resolved body of each registered migration through real Dolt
// SQL-server connections must leave the entities identical — every column
// of issues and wisps, is_blocked and updated_at included — and leave no
// __bd_0059 stand-in behind. Equivalence against the frozen bytes is what
// licenses the plan; per-plane correctness against the frozen-predicate
// model is AC-1/AC-2's job.
func TestMigrationBodyPlanDifferentialMatchesFrozenBodies(t *testing.T) {
	skipIfNoDolt(t)

	t.Run("main_0059", func(t *testing.T) {
		frozen, err := schema.MigrationSQL("0059_recompute_null_gate_is_blocked.up.sql")
		if err != nil {
			t.Fatalf("read frozen 0059: %v", err)
		}
		planned, err := schema.PlannedMigrationSQL(59)
		if err != nil {
			t.Fatalf("resolve planned 0059: %v", err)
		}
		if planned == frozen {
			t.Fatalf("planned 0059 body is byte-identical to the frozen body: the plan substituted nothing (vacuous differential)")
		}
		planRunBodyDifferential(t, frozen, planned)
	})

	t.Run("ignored_0015", func(t *testing.T) {
		frozen, err := schema.IgnoredMigrationSQL("0015_recompute_null_gate_wisp_is_blocked.up.sql")
		if err != nil {
			t.Fatalf("read frozen ignored/0015: %v", err)
		}
		planned, err := schema.PlannedIgnoredMigrationSQL(15)
		if err != nil {
			t.Fatalf("resolve planned ignored/0015: %v", err)
		}
		if planned == frozen {
			t.Fatalf("planned ignored/0015 body is byte-identical to the frozen body: the plan substituted nothing (vacuous differential)")
		}
		planRunBodyDifferential(t, frozen, planned)
	})
}

// planRunBodyDifferential executes each body on its own identical fresh
// database and compares the entity dumps.
func planRunBodyDifferential(t *testing.T, frozenBody, plannedBody string) {
	t.Helper()
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	frozenDump := planExecBodyAndDump(t, ctx, "frozen", frozenBody)
	plannedDump := planExecBodyAndDump(t, ctx, "planned", plannedBody)
	if frozenDump != plannedDump {
		t.Fatalf("frozen and planned bodies disagree:\n--- frozen ---\n%s\n--- planned ---\n%s",
			frozenDump, plannedDump)
	}
}

// planExecBodyAndDump builds a fresh main-v58/ignored-v14 database carrying
// the small mixed graph with scrambled flags and fixed historical stamps,
// executes body through the pinned connection, and renders every issues and
// wisps row as one deterministic line.
func planExecBodyAndDump(t *testing.T, ctx context.Context, side, body string) string {
	t.Helper()
	dbName := uniqueTestDBName(t)
	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	db, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName, Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("%s side exec %.80q: %v", side, query, err)
		}
	}

	if _, err := schema.MigrateUpTo(ctx, conn, 58); err != nil {
		t.Fatalf("%s side: migrate to main v58: %v", side, err)
	}
	if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 14); err != nil {
		t.Fatalf("%s side: migrate to ignored v14: %v", side, err)
	}

	for _, stmt := range planDiffGraphIssuesSQL() {
		exec(stmt)
	}
	for _, stmt := range planDiffGraphWispsSQL() {
		exec(stmt)
	}
	for _, stmt := range planDiffGraphIssueDepsSQL() {
		exec(stmt)
	}
	for _, stmt := range planDiffGraphWispDepsSQL() {
		exec(stmt)
	}
	// Scrambled blocked flags (parity of the numeric suffix) and fixed
	// historical timestamps (distinct per row, months old): the bodies must
	// actually recompute and must not stamp updated_at.
	exec("UPDATE issues SET is_blocked = CAST(SUBSTRING(id, 3) AS UNSIGNED) % 2, updated_at = DATE_ADD('2024-06-01 12:00:00', INTERVAL CAST(SUBSTRING(id, 3) AS UNSIGNED) MINUTE)")
	exec("UPDATE wisps SET is_blocked = CAST(SUBSTRING(id, 3) AS UNSIGNED) % 2, updated_at = DATE_ADD('2024-06-01 12:00:00', INTERVAL CAST(SUBSTRING(id, 3) AS UNSIGNED) MINUTE)")
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'test: differential base graph')")

	exec(body)

	// No 0059 stand-in survives a successful body (the frozen suffix's DROPs
	// run in both bodies).
	var standIns int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME LIKE '__bd_0059%'",
		dbName).Scan(&standIns); err != nil {
		t.Fatalf("%s side: read stand-in count: %v", side, err)
	}
	if standIns != 0 {
		t.Errorf("%s side: %d __bd_0059 stand-in tables remain after the body", side, standIns)
	}

	var dump strings.Builder
	for _, table := range []string{"issues", "wisps"} {
		fmt.Fprintf(&dump, "== %s ==\n", table)
		rows, err := conn.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY id")
		if err != nil {
			t.Fatalf("%s side: read %s: %v", side, table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("%s side: columns of %s: %v", side, table, err)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		// created_at is the fixture insert's wall clock — each side's
		// database is built seconds apart — and neither body writes it, so
		// it is the one column excluded from the dump.
		keep := make([]bool, len(cols))
		for i, c := range cols {
			keep[i] = c != "created_at"
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatalf("%s side: scan %s row: %v", side, table, err)
			}
			parts := make([]string, 0, len(cols))
			for i, v := range vals {
				if !keep[i] {
					continue
				}
				if v == nil {
					parts = append(parts, "NULL")
					continue
				}
				if b, ok := v.([]byte); ok {
					parts = append(parts, string(b))
					continue
				}
				parts = append(parts, fmt.Sprintf("%v", v))
			}
			fmt.Fprintf(&dump, "%s\n", strings.Join(parts, "|"))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("%s side: iterate %s: %v", side, table, err)
		}
		rows.Close()
	}
	return dump.String()
}

// planDiffGraph is the small mixed graph both differential sides start
// from: enough shape to exercise every clause of the frozen predicate —
// open/closed/pinned children in both planes, waits-for under all three
// gate modes (metadata absent, all-children, any-children) against issue,
// wisp and childless targets, blocks against open and closed targets, a
// conditional-block, and a cross-plane parent-child chain — while staying
// small enough for the frozen correlated body to run quickly.
type planDiffEdge struct {
	id, from, typ, targetIssue, targetWisp, gate string
}

func planDiffIssueStatus() map[string]string {
	return map[string]string{
		"i-000": "open", "i-001": "open", "i-002": "closed", "i-003": "open",
		"i-004": "open", "i-005": "open", "i-006": "open", "i-007": "open",
		"i-008": "open", "i-009": "open", "i-010": "open", "i-011": "open",
		"i-012": "closed", "i-013": "pinned", "i-014": "open",
	}
}

func planDiffWispStatus() map[string]string {
	return map[string]string{
		"w-000": "open", "w-001": "open", "w-002": "closed", "w-003": "open",
		"w-004": "open", "w-005": "pinned",
	}
}

func planDiffIssueDeps() []planDiffEdge {
	return []planDiffEdge{
		// Parent-child: i-000 has open child i-001 and closed child i-002.
		{"d-000", "i-001", "parent-child", "i-000", "", ""},
		{"d-001", "i-002", "parent-child", "i-000", "", ""},
		// Waits-for on the mixed-parent hub under each gate mode, and on a
		// closed target, and on a childless issue.
		{"d-002", "i-003", "waits-for", "i-000", "", ""},
		{"d-003", "i-004", "waits-for", "i-000", "", "any-children"},
		{"d-004", "i-005", "waits-for", "i-002", "", "any-children"},
		{"d-005", "i-006", "waits-for", "i-012", "", ""},
		// Blocks: open target blocks, closed/pinned targets do not.
		{"d-006", "i-007", "blocks", "i-000", "", ""},
		{"d-007", "i-008", "blocks", "i-012", "", ""},
		{"d-008", "i-009", "blocks", "i-013", "", ""},
		// A conditional-block on the open hub.
		{"d-009", "i-010", "conditional-blocks", "i-000", "", ""},
		// A waits-for whose own waiter is itself blocked transitively.
		{"d-010", "i-011", "waits-for", "i-003", "", ""},
	}
}

func planDiffWispDeps() []planDiffEdge {
	return []planDiffEdge{
		// Wisp-plane parent-child into an open issue and into w-000, which
		// collects open, closed and pinned wisp children (wisp_dependencies'
		// from-side FK targets wisps, so every edge's from is a wisp).
		{"wd-000", "w-001", "parent-child", "i-014", "", ""},
		{"wd-001", "w-002", "parent-child", "", "w-000", ""},
		{"wd-002", "w-003", "parent-child", "", "w-000", ""},
		{"wd-003", "w-005", "parent-child", "", "w-000", ""},
		// Waits-for onto a wisp with mixed children under each gate mode,
		// and onto a childless wisp.
		{"wd-004", "w-001", "waits-for", "", "w-000", "any-children"},
		{"wd-005", "w-004", "waits-for", "", "w-000", ""},
		{"wd-006", "w-003", "waits-for", "", "w-004", ""},
		// Blocks across planes: an open wisp blocks an open issue; a pinned
		// wisp target does not.
		{"wd-007", "w-004", "blocks", "i-014", "", ""},
		{"wd-008", "w-001", "blocks", "", "w-005", ""},
		// Cross-plane reachability: w-005 is a parent of open i-003, which
		// the issue plane holds in waits-for on the mixed hub, chaining
		// blocked state across planes through the recursive members.
		{"wd-009", "w-005", "parent-child", "i-003", "", ""},
	}
}

func planDiffGraphIssuesSQL() []string {
	status := planDiffIssueStatus()
	rows := make([]string, 0, len(status))
	for id, st := range status {
		rows = append(rows, fmt.Sprintf("('%s', 'diff %s', '', '', '', '', '%s')", id, id, st))
	}
	return planBatch(rows, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status) VALUES")
}

func planDiffGraphWispsSQL() []string {
	status := planDiffWispStatus()
	rows := make([]string, 0, len(status))
	for id, st := range status {
		rows = append(rows, fmt.Sprintf("('%s', 'diff %s', '%s')", id, id, st))
	}
	return planBatch(rows, "INSERT INTO wisps (id, title, status) VALUES")
}

func planDiffGraphIssueDepsSQL() []string {
	rows := make([]string, 0, len(planDiffIssueDeps()))
	for _, e := range planDiffIssueDeps() {
		rows = append(rows, planDiffEdgeValues(e, true))
	}
	return planBatch(rows, "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_by, metadata) VALUES")
}

func planDiffGraphWispDepsSQL() []string {
	rows := make([]string, 0, len(planDiffWispDeps()))
	for _, e := range planDiffWispDeps() {
		rows = append(rows, planDiffEdgeValues(e, false))
	}
	return planBatch(rows, "INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata) VALUES")
}

// planDiffEdgeValues renders one differential edge row (planEdgeValues's
// shape for the hand-written planDiffEdge literals).
func planDiffEdgeValues(e planDiffEdge, durable bool) string {
	targetIssue, targetWisp := "NULL", "NULL"
	if e.targetIssue != "" {
		targetIssue = "'" + e.targetIssue + "'"
	}
	if e.targetWisp != "" {
		targetWisp = "'" + e.targetWisp + "'"
	}
	metadata := "NULL"
	if e.gate != "" {
		metadata = fmt.Sprintf("JSON_OBJECT('gate', '%s')", e.gate)
	}
	if durable {
		return fmt.Sprintf("('%s', '%s', %s, %s, '%s', 'fixture', %s)",
			e.id, e.from, targetIssue, targetWisp, e.typ, metadata)
	}
	return fmt.Sprintf("('%s', '%s', %s, %s, '%s', %s)",
		e.id, e.from, targetIssue, targetWisp, e.typ, metadata)
}
