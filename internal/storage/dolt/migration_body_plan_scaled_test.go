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

// The AC bounds for the scaled-graph steps, and the failing-control grace
// beyond them: the control connection kills the pinned migration connection's
// query planKillGrace after the step starts, so a performance regression
// fails the bound with a descriptive error instead of hanging the lane for
// the hours the un-planned body needs (gastownhall/beads#6520 measured the
// frozen 0059 body unfinished after two hours at 8x this fixture).
const (
	planStepBound = 30 * time.Second
	planKillGrace = 45 * time.Second
)

// TestMigrationBodyPlanScaledGraph is the scaled-graph regression for
// gastownhall/beads#6520 (bd-f1s6sq): on a 285-issue / 113-dependency /
// 1,457-wisp / 1,770-wisp-dependency graph — the shape of the report's
// restored clone at roughly 1/8 of its wisp tier — the frozen recompute
// bodies of main 0059 and ignored 0015 do not finish, while the registered
// execution plans must complete each step within the AC bound, compute the
// frozen predicate's blocked state, and leave the cursor recording the
// frozen file's hash.
func TestMigrationBodyPlanScaledGraph(t *testing.T) {
	skipIfNoDolt(t)
	fixture := buildPlanFixture()
	fixture.checkCounts(t)
	model := fixture.model()
	fixture.assertStructuralExpectations(t, model)
	blockedIssues, blockedWisps := 0, 0
	for _, b := range model.issueBlocked {
		if b {
			blockedIssues++
		}
	}
	for _, b := range model.wispBlocked {
		if b {
			blockedWisps++
		}
	}
	t.Logf("fixture model: %d/%d issues blocked, %d/%d wisps blocked",
		blockedIssues, planFixtureIssues, blockedWisps, planFixtureWisps)

	t.Run("issue_plane_main_59", func(t *testing.T) {
		skipIfNoDolt(t)
		acquireTestSlot()
		t.Cleanup(releaseTestSlot)

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		t.Cleanup(cancel)

		admin, conn := planScaledDB(t, ctx, fixture)

		// The bounded failing control: take the pinned connection's id
		// BEFORE the step starts (it cannot be read from the busy
		// connection afterwards), and have a separate connection kill that
		// exact query once the grace deadline passes.
		var connID int64
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connID); err != nil {
			t.Fatalf("read pinned connection id: %v", err)
		}
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-time.After(planKillGrace):
				_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", connID))
			case <-done:
			}
		}()

		start := time.Now()
		_, err := schema.MigrateUpTo(ctx, conn, 59)
		elapsed := time.Since(start)
		t.Logf("main 0059 over %d issues / %d dependencies / %d wisps / %d wisp dependencies: %s",
			planFixtureIssues, planFixtureDeps, planFixtureWisps, planFixtureWispDeps, elapsed.Round(time.Millisecond))
		if err != nil {
			t.Fatalf("main 0059 failed after %s (the failing control kills the step at %s; the AC bound is %s): %v",
				elapsed.Round(time.Millisecond), planKillGrace, planStepBound, err)
		}
		if elapsed > planStepBound {
			t.Fatalf("main 0059 took %s, want <= %s over the scaled graph (#6520)", elapsed.Round(time.Millisecond), planStepBound)
		}

		// The step's issue blocked state must match the frozen-predicate
		// reference model.
		rows, err := conn.QueryContext(ctx, "SELECT id, is_blocked FROM issues")
		if err != nil {
			t.Fatalf("read issues: %v", err)
		}
		defer rows.Close()
		mismatches := 0
		for rows.Next() {
			var id string
			var blocked bool
			if err := rows.Scan(&id, &blocked); err != nil {
				t.Fatalf("scan issue: %v", err)
			}
			if blocked != model.issueBlocked[id] {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("issue %s is_blocked=%v, want %v (frozen-predicate model)", id, blocked, model.issueBlocked[id])
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate issues: %v", err)
		}
		if mismatches > 0 {
			t.Fatalf("%d of %d issues disagree with the frozen-predicate model", mismatches, planFixtureIssues)
		}

		// The cursor must still record the frozen file's hash: a plan
		// changes what runs, never the migration's recorded identity.
		var hash string
		if err := conn.QueryRowContext(ctx, "SELECT content_hash FROM schema_migrations WHERE version = 59").Scan(&hash); err != nil {
			t.Fatalf("read main 59 cursor row: %v", err)
		}
		if hash != planFrozenMain59Hash {
			t.Errorf("main 59 cursor hash %s, want the frozen file's %s", hash, planFrozenMain59Hash)
		}
	})

	t.Run("wisp_plane_ignored_15", func(t *testing.T) {
		skipIfNoDolt(t)
		acquireTestSlot()
		t.Cleanup(releaseTestSlot)

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		t.Cleanup(cancel)

		admin, conn := planScaledDB(t, ctx, fixture)

		var connID int64
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connID); err != nil {
			t.Fatalf("read pinned connection id: %v", err)
		}
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-time.After(planKillGrace):
				_, _ = admin.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", connID))
			case <-done:
			}
		}()

		start := time.Now()
		_, err := schema.MigrateIgnoredUpTo(ctx, conn, 15)
		elapsed := time.Since(start)
		t.Logf("ignored 0015 over %d issues / %d dependencies / %d wisps / %d wisp dependencies: %s",
			planFixtureIssues, planFixtureDeps, planFixtureWisps, planFixtureWispDeps, elapsed.Round(time.Millisecond))
		if err != nil {
			t.Fatalf("ignored 0015 failed after %s (the failing control kills the step at %s; the AC bound is %s): %v",
				elapsed.Round(time.Millisecond), planKillGrace, planStepBound, err)
		}
		if elapsed > planStepBound {
			t.Fatalf("ignored 0015 took %s, want <= %s over the scaled graph (#6520)", elapsed.Round(time.Millisecond), planStepBound)
		}

		// The step's wisp blocked state must match the frozen-predicate
		// reference model (the ignored twin recomputes wisps against the
		// real wisp tables; no stand-ins, no index DDL in its plan).
		rows, err := conn.QueryContext(ctx, "SELECT id, is_blocked FROM wisps")
		if err != nil {
			t.Fatalf("read wisps: %v", err)
		}
		defer rows.Close()
		mismatches := 0
		for rows.Next() {
			var id string
			var blocked bool
			if err := rows.Scan(&id, &blocked); err != nil {
				t.Fatalf("scan wisp: %v", err)
			}
			if blocked != model.wispBlocked[id] {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("wisp %s is_blocked=%v, want %v (frozen-predicate model)", id, blocked, model.wispBlocked[id])
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate wisps: %v", err)
		}
		if mismatches > 0 {
			t.Fatalf("%d of %d wisps disagree with the frozen-predicate model", mismatches, planFixtureWisps)
		}

		var hash string
		if err := conn.QueryRowContext(ctx, "SELECT content_hash FROM ignored_schema_migrations WHERE version = 15").Scan(&hash); err != nil {
			t.Fatalf("read ignored 15 cursor row: %v", err)
		}
		if hash != planFrozenIgnored15 {
			t.Errorf("ignored 15 cursor hash %s, want the frozen file's %s", hash, planFrozenIgnored15)
		}
	})
}

// planScaledDB creates an isolated database at main v58 / ignored v14 with
// the fixture graph durably committed — the pre-0059 state of the #6520
// rehearsal. It returns the admin connection (for the failing control's
// KILL QUERY) and the pinned migration connection.
func planScaledDB(t *testing.T, ctx context.Context, fixture *planFixture) (*sql.DB, *sql.Conn) {
	t.Helper()
	dbName := uniqueTestDBName(t)
	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	// No DROP DATABASE on cleanup, as in migrate_updated_at_preservation_test.go:
	// rapid CREATE/DROP cycles crash the shared Dolt server.
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
			t.Fatalf("exec %.80q: %v", query, err)
		}
	}

	if _, err := schema.MigrateUpTo(ctx, conn, 58); err != nil {
		t.Fatalf("migrate to main v58: %v", err)
	}
	if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 14); err != nil {
		t.Fatalf("migrate to ignored v14: %v", err)
	}

	for _, stmt := range fixture.insertIssuesSQL() {
		exec(stmt)
	}
	for _, stmt := range fixture.insertWispsSQL() {
		exec(stmt)
	}
	for _, stmt := range fixture.insertIssueDepsSQL() {
		exec(stmt)
	}
	for _, stmt := range fixture.insertWispDepsSQL() {
		exec(stmt)
	}
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'test: v58/ignored-14 scaled fixture graph')")
	return admin, conn
}

// insertIssuesSQL renders the fixture's issues as batched INSERT statements.
func (f *planFixture) insertIssuesSQL() []string {
	rows := make([]string, 0, len(f.issueIDs))
	for _, id := range f.issueIDs {
		rows = append(rows, fmt.Sprintf("('%s', 'fixture %s', '', '', '', '', '%s')", id, id, f.issueStatus[id]))
	}
	return planBatch(rows, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status) VALUES")
}

func (f *planFixture) insertWispsSQL() []string {
	rows := make([]string, 0, len(f.wispIDs))
	for _, id := range f.wispIDs {
		rows = append(rows, fmt.Sprintf("('%s', 'fixture %s', '%s')", id, id, f.wispStatus[id]))
	}
	return planBatch(rows, "INSERT INTO wisps (id, title, status) VALUES")
}

func (f *planFixture) insertIssueDepsSQL() []string {
	rows := make([]string, 0, len(f.issueEdges))
	for _, e := range f.issueEdges {
		rows = append(rows, planEdgeValues(e, true))
	}
	return planBatch(rows, "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_by, metadata) VALUES")
}

func (f *planFixture) insertWispDepsSQL() []string {
	rows := make([]string, 0, len(f.wispEdges))
	for _, e := range f.wispEdges {
		rows = append(rows, planEdgeValues(e, false))
	}
	return planBatch(rows, "INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata) VALUES")
}

// planEdgeValues renders one dependency row. Ids, types and gates are
// generator-controlled constants, so no escaping is needed.
func planEdgeValues(e planFixtureEdge, durable bool) string {
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

// planBatch groups rows into INSERT statements of at most 250 rows.
func planBatch(rows []string, prefix string) []string {
	var stmts []string
	for len(rows) > 0 {
		n := len(rows)
		if n > 250 {
			n = 250
		}
		stmts = append(stmts, prefix+" "+strings.Join(rows[:n], ",")+";")
		rows = rows[n:]
	}
	return stmts
}
