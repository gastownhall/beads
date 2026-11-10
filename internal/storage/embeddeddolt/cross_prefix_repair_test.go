//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin the GH#8299 upgrade repair. Before the fix every write path
// filed a dependency target whose prefix differed from its source's in
// depends_on_external even when the database held it, so readiness (which
// joins the typed columns) never saw the edge. The repair runs from MigrateUp
// under ignored marker 0032. Each fixture builds a database at latest, writes
// the legacy rows a pre-fix binary left behind, commits them, and un-records
// the marker: exactly the state an existing clone opens in after upgrading.

// legacyStamp is a months-old, second-precision timestamp per fixture row, so
// a migration-time stamp can never collide with it.
func legacyStamp(n int) time.Time {
	return time.Date(2025, time.April, 1+n, 8, 15, n, 0, time.UTC)
}

// newCrossPrefixRepairDB returns a pinned connection to a fresh embedded
// database migrated to latest.
func newCrossPrefixRepairDB(t *testing.T, ctx context.Context) (*sql.Conn, string, func()) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		closeConn()
		t.Fatalf("baseline MigrateUp: %v", err)
	}
	return conn, dataDir, closeConn
}

// seedLegacyIssue writes one issues (or wisps) row with a historical
// updated_at and is_blocked = 0: no legacy external edge ever blocked it.
func seedLegacyIssue(t *testing.T, ctx context.Context, conn *sql.Conn, table, id, status string, n int) {
	t.Helper()
	var closedAt any
	if status == "closed" {
		closedAt = legacyStamp(n)
	}
	//nolint:gosec // G202: table is "issues" or "wisps" from this file's callers.
	if _, err := conn.ExecContext(ctx, "INSERT INTO "+table+
		" (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, is_blocked, created_at, updated_at, closed_at)"+
		" VALUES (?, ?, '', '', '', '', ?, 2, 'task', ?, 0, ?, ?, ?)",
		id, "fixture "+id, status, table == "wisps", legacyStamp(n), legacyStamp(n), closedAt); err != nil {
		t.Fatalf("seed %s row %s: %v", table, id, err)
	}
}

// seedDep writes one edge row with its target in column and a payload the
// repair must carry over untouched.
func seedDep(t *testing.T, ctx context.Context, conn *sql.Conn, table, source, target, column, depType string) {
	t.Helper()
	//nolint:gosec // G202: table and column are constants from this file's callers.
	if _, err := conn.ExecContext(ctx, "INSERT INTO "+table+
		" (id, issue_id, "+column+", type, created_by, created_at, metadata, thread_id)"+
		" VALUES (?, ?, ?, ?, 'legacy-writer', ?, ?, ?)",
		depid.New(source, target), source, target, depType, legacyStamp(40),
		`{"origin":"`+source+`"}`, "thread-"+source); err != nil {
		t.Fatalf("seed %s edge %s -> %s: %v", table, source, target, err)
	}
}

// commitLegacyStateAndUnmarkRepair commits the seeded versioned rows and
// un-records ignored marker 0032, so the next MigrateUp is the first pass an
// upgraded clone runs.
func commitLegacyStateAndUnmarkRepair(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	execFrozenGuard(t, ctx, conn, "CALL DOLT_ADD('-A')")
	execFrozenGuard(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: legacy cross-prefix rows')")
	execFrozenGuard(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 32")
}

// depRowState is everything about an edge row the repair must keep, plus the
// typed target columns it may move.
type depRowState struct {
	ID, Type, CreatedBy, CreatedAt, Metadata, Thread string
	Issue, Wisp, External                            sql.NullString
}

func readDepRow(t *testing.T, ctx context.Context, conn *sql.Conn, table, source, target string) depRowState {
	t.Helper()
	var s depRowState
	var thread sql.NullString
	//nolint:gosec // G202: table is a constant from this file's callers.
	err := conn.QueryRowContext(ctx, "SELECT id, type, created_by, CAST(created_at AS CHAR), CAST(metadata AS CHAR), thread_id,"+
		" depends_on_issue_id, depends_on_wisp_id, depends_on_external FROM "+table+
		" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		source, target).Scan(&s.ID, &s.Type, &s.CreatedBy, &s.CreatedAt, &s.Metadata, &thread, &s.Issue, &s.Wisp, &s.External)
	if err != nil {
		t.Fatalf("read %s edge %s -> %s: %v", table, source, target, err)
	}
	s.Thread = thread.String
	return s
}

// targetColumn names the one typed column holding the row's target.
func (s depRowState) targetColumn() string {
	switch {
	case s.Issue.Valid && !s.Wisp.Valid && !s.External.Valid:
		return "depends_on_issue_id"
	case s.Wisp.Valid && !s.Issue.Valid && !s.External.Valid:
		return "depends_on_wisp_id"
	case s.External.Valid && !s.Issue.Valid && !s.Wisp.Valid:
		return "depends_on_external"
	default:
		return fmt.Sprintf("malformed(issue=%v wisp=%v external=%v)", s.Issue, s.Wisp, s.External)
	}
}

func requireDepColumn(t *testing.T, ctx context.Context, conn *sql.Conn, table, source, target, column string) {
	t.Helper()
	if got := readDepRow(t, ctx, conn, table, source, target).targetColumn(); got != column {
		t.Errorf("%s edge %s -> %s holds its target in %s, want %s", table, source, target, got, column)
	}
}

func readBlocked(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) (bool, time.Time) {
	t.Helper()
	var blocked bool
	var updatedAt time.Time
	//nolint:gosec // G202: table is "issues" or "wisps" from this file's callers.
	if err := conn.QueryRowContext(ctx, "SELECT is_blocked, updated_at FROM "+table+" WHERE id = ?", id).
		Scan(&blocked, &updatedAt); err != nil {
		t.Fatalf("read %s row %s: %v", table, id, err)
	}
	return blocked, updatedAt
}

func ignoredMarkerRecorded(t *testing.T, ctx context.Context, conn *sql.Conn, version int) bool {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = ?", version) == 1
}

func requireBlockedStateConsistent(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn)
	if err != nil {
		t.Fatalf("CountIsBlockedInconsistenciesInTx: %v", err)
	}
	if n != 0 {
		t.Errorf("%d rows carry a stale is_blocked after the repair", n)
	}
}

// TestEmbeddedCrossPrefixRepairConvertsLegacyRows is AC-5: legacy rows naming
// targets the versioned issues table holds move to depends_on_issue_id with
// their id and payload intact; their sources and inherited descendants get
// the blocked state the edges imply, with no updated_at churn; explicit
// external references, targets held nowhere (of either prefix, on either
// plane), and versioned edges onto a clone-local wisp stay external; and the
// result is committed.
func TestEmbeddedCrossPrefixRepairConvertsLegacyRows(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, _, closeConn := newCrossPrefixRepairDB(t, ctx)
	defer closeConn()

	issues := []struct {
		id, status  string
		wantBlocked bool
	}{
		{"rb-open", "open", false},
		{"rb-closed", "closed", false},
		{"ra-blocked", "open", true},       // blocks -> open rb-open
		{"ra-unblocked", "open", false},    // blocks -> closed rb-closed
		{"ra-conditional", "open", true},   // conditional-blocks -> open rb-open
		{"ra-child", "open", true},         // parent-child -> ra-blocked: inherits
		{"ra-grandchild", "open", true},    // parent-child -> ra-child: inherits
		{"ra-external", "open", false},     // blocks -> external:proj:cap
		{"ra-absent", "open", false},       // blocks -> zz-absent, held nowhere
		{"ra-same-absent", "open", false},  // blocks -> ra-gone, same prefix, held nowhere
		{"ra-onto-wisp", "open", false},    // blocks -> rw-local, a clone-local wisp
		{"rc-closed-dep", "closed", false}, // closed itself: never blocked
	}
	for i, is := range issues {
		seedLegacyIssue(t, ctx, conn, "issues", is.id, is.status, i)
	}
	seedLegacyIssue(t, ctx, conn, "wisps", "rw-local", "open", 30)

	converted := [][3]string{
		{"ra-blocked", "rb-open", "blocks"},
		{"ra-unblocked", "rb-closed", "blocks"},
		{"ra-conditional", "rb-open", "conditional-blocks"},
		{"rc-closed-dep", "rb-open", "blocks"},
	}
	unchanged := [][3]string{
		{"ra-external", "external:proj:cap", "blocks"},
		{"ra-absent", "zz-absent", "blocks"},
		{"ra-same-absent", "ra-gone", "blocks"},
		{"ra-onto-wisp", "rw-local", "blocks"},
	}
	for _, e := range append(append([][3]string{}, converted...), unchanged...) {
		seedDep(t, ctx, conn, "dependencies", e[0], e[1], "depends_on_external", e[2])
	}
	// The clone-local plane's twin of the same-prefix row held nowhere.
	wispUnchanged := [3]string{"rw-local", "rw-gone", "blocks"}
	seedDep(t, ctx, conn, "wisp_dependencies", wispUnchanged[0], wispUnchanged[1], "depends_on_external", wispUnchanged[2])
	seedDep(t, ctx, conn, "dependencies", "ra-child", "ra-blocked", "depends_on_issue_id", "parent-child")
	seedDep(t, ctx, conn, "dependencies", "ra-grandchild", "ra-child", "depends_on_issue_id", "parent-child")
	commitLegacyStateAndUnmarkRepair(t, ctx, conn)

	before := map[[3]string]depRowState{}
	for _, e := range append(append([][3]string{}, converted...), unchanged...) {
		before[e] = readDepRow(t, ctx, conn, "dependencies", e[0], e[1])
	}
	wispBefore := readDepRow(t, ctx, conn, "wisp_dependencies", wispUnchanged[0], wispUnchanged[1])

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("repair MigrateUp: %v", err)
	}

	for _, e := range converted {
		got := readDepRow(t, ctx, conn, "dependencies", e[0], e[1])
		if col := got.targetColumn(); col != "depends_on_issue_id" {
			t.Errorf("edge %s -> %s holds its target in %s, want depends_on_issue_id", e[0], e[1], col)
		}
		want := before[e]
		want.Issue, want.External = sql.NullString{String: e[1], Valid: true}, sql.NullString{}
		if got != want {
			t.Errorf("edge %s -> %s = %+v, want only the target column moved from %+v", e[0], e[1], got, before[e])
		}
	}
	for _, e := range unchanged {
		if got := readDepRow(t, ctx, conn, "dependencies", e[0], e[1]); got != before[e] {
			t.Errorf("edge %s -> %s = %+v, want it unchanged %+v", e[0], e[1], got, before[e])
		}
	}
	if got := readDepRow(t, ctx, conn, "wisp_dependencies", wispUnchanged[0], wispUnchanged[1]); got != wispBefore {
		t.Errorf("wisp edge %s -> %s = %+v, want it unchanged %+v", wispUnchanged[0], wispUnchanged[1], got, wispBefore)
	}
	for i, is := range issues {
		blocked, updatedAt := readBlocked(t, ctx, conn, "issues", is.id)
		if blocked != is.wantBlocked {
			t.Errorf("%s is_blocked = %v, want %v", is.id, blocked, is.wantBlocked)
		}
		if !updatedAt.Equal(legacyStamp(i)) {
			t.Errorf("%s updated_at = %v, want it preserved at %v", is.id, updatedAt, legacyStamp(i))
		}
	}
	requireBlockedStateConsistent(t, ctx, conn)

	if !ignoredMarkerRecorded(t, ctx, conn, 32) {
		t.Error("ignored marker 0032 not recorded after a completed repair")
	}
	dirty, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("dolt_status: %v", err)
	}
	for _, table := range dirty {
		if table == "dependencies" || table == "issues" {
			t.Errorf("repair left %s uncommitted (dolt_status %v)", table, dirty)
		}
	}
}

// seedRefusalFixture writes and commits a legacy edge whose repair moves a
// versioned row and flips a blocked flag, with the repair marker un-recorded.
func seedRefusalFixture(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	seedLegacyIssue(t, ctx, conn, "issues", "rb-open", "open", 1)
	seedLegacyIssue(t, ctx, conn, "issues", "ra-blocked", "open", 2)
	seedLegacyIssue(t, ctx, conn, "issues", "ra-other", "open", 3)
	seedDep(t, ctx, conn, "dependencies", "ra-blocked", "rb-open", "depends_on_external", "blocks")
	seedDep(t, ctx, conn, "dependencies", "ra-other", "ra-blocked", "depends_on_issue_id", "related")
	commitLegacyStateAndUnmarkRepair(t, ctx, conn)
}

// requireRepairConverged asserts the refusal fixture's edge is typed, its
// source blocked, the blocked state consistent and the marker recorded.
func requireRepairConverged(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	requireDepColumn(t, ctx, conn, "dependencies", "ra-blocked", "rb-open", "depends_on_issue_id")
	if blocked, _ := readBlocked(t, ctx, conn, "issues", "ra-blocked"); !blocked {
		t.Error("ra-blocked is_blocked = false after the repair, want true")
	}
	requireBlockedStateConsistent(t, ctx, conn)
	if !ignoredMarkerRecorded(t, ctx, conn, 32) {
		t.Error("ignored marker 0032 not recorded after the repair converged")
	}
}

// TestEmbeddedCrossPrefixRepairRefusesDirtyTable is AC-6: a pass that would
// write versioned rows while a graph table holds uncommitted user edits
// refuses before writing anything, names the table, leaves the marker
// pending, and converges on the next open once the user commits.
func TestEmbeddedCrossPrefixRepairRefusesDirtyTable(t *testing.T) {
	requireEmbedded(t)
	for _, tc := range []struct {
		table, edit string
	}{
		{"issues", "UPDATE issues SET title = 'user edit' WHERE id = 'ra-other'"},
		{"dependencies", `UPDATE dependencies SET metadata = '{"user":"edit"}' WHERE issue_id = 'ra-other'`},
	} {
		t.Run(tc.table, func(t *testing.T) {
			ctx := t.Context()
			conn, _, closeConn := newCrossPrefixRepairDB(t, ctx)
			defer closeConn()
			seedRefusalFixture(t, ctx, conn)
			execFrozenGuard(t, ctx, conn, tc.edit)

			_, err := schema.MigrateUp(ctx, conn)
			var dirtyErr *schema.DirtyTablesError
			if !errors.As(err, &dirtyErr) {
				t.Fatalf("MigrateUp over dirty %s = %v, want a DirtyTablesError", tc.table, err)
			}
			if !slices.Contains(dirtyErr.Tables, tc.table) {
				t.Errorf("DirtyTablesError names %v, want %s", dirtyErr.Tables, tc.table)
			}
			requireDepColumn(t, ctx, conn, "dependencies", "ra-blocked", "rb-open", "depends_on_external")
			if blocked, _ := readBlocked(t, ctx, conn, "issues", "ra-blocked"); blocked {
				t.Error("refused pass wrote ra-blocked is_blocked")
			}
			if ignoredMarkerRecorded(t, ctx, conn, 32) {
				t.Error("refused pass recorded ignored marker 0032")
			}

			execFrozenGuard(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'user edit')")
			if _, err := schema.MigrateUp(ctx, conn); err != nil {
				t.Fatalf("MigrateUp after committing the user edit: %v", err)
			}
			requireRepairConverged(t, ctx, conn)
		})
	}
}

// graphSnapshot is everything a no-op pass must leave alone: HEAD, the
// working set, every edge row, and every issue's blocked flag and stamp.
func graphSnapshot(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	var head string
	if err := conn.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatalf("HASHOF(HEAD): %v", err)
	}
	dirty, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("dolt_status: %v", err)
	}
	return fmt.Sprintf("head=%s\ndirty=%v\n%s%s", head, dirty,
		dumpRows(t, ctx, conn, "SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type,"+
			" CAST(metadata AS CHAR), thread_id FROM dependencies ORDER BY id"),
		dumpRows(t, ctx, conn, "SELECT id, is_blocked, CAST(updated_at AS CHAR) FROM issues ORDER BY id"))
}

// dumpRows renders every row query returns, one line per row.
func dumpRows(t *testing.T, ctx context.Context, conn *sql.Conn, query string) string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	var b strings.Builder
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fmt.Fprintln(&b, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return b.String()
}

// TestEmbeddedCrossPrefixRepairReopenIsNoop is AC-7: once the repair has
// converged, reopening the store, and even a forced re-run of the repair
// pass, plans nothing and leaves HEAD, the working set and every row alone.
func TestEmbeddedCrossPrefixRepairReopenIsNoop(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, dataDir, closeConn := newCrossPrefixRepairDB(t, ctx)
	seedRefusalFixture(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		closeConn()
		t.Fatalf("repair MigrateUp: %v", err)
	}
	requireRepairConverged(t, ctx, conn)
	converged := graphSnapshot(t, ctx, conn)
	closeConn()

	conn, closeConn = openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("reopen MigrateUp: %v", err)
	}
	if got := graphSnapshot(t, ctx, conn); got != converged {
		t.Errorf("reopen changed the converged store:\n got: %s\nwant: %s", got, converged)
	}

	execFrozenGuard(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = 32")
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("forced re-run MigrateUp: %v", err)
	}
	if got := graphSnapshot(t, ctx, conn); got != converged {
		t.Errorf("forced re-run changed the converged store:\n got: %s\nwant: %s", got, converged)
	}
	requireIgnoredCursorAtLatest(t, ctx, conn)
}

// TestEmbeddedCrossPrefixRepairIsMarkerGatedAfterConvergence is AC-7's
// steady state: once the marker records, a converged reopen short-circuits
// before the repair, so a legacy-shaped row committed after convergence onto
// a held target stays external, and HEAD and the working set stay put. The
// marker, not a re-scan, says the repair is done.
func TestEmbeddedCrossPrefixRepairIsMarkerGatedAfterConvergence(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, dataDir, closeConn := newCrossPrefixRepairDB(t, ctx)
	seedRefusalFixture(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		closeConn()
		t.Fatalf("repair MigrateUp: %v", err)
	}
	requireRepairConverged(t, ctx, conn)

	// Artificial by construction now that the write path classifies by
	// evidence, which is exactly why the converged open must not touch it.
	seedLegacyIssue(t, ctx, conn, "issues", "rb-post", "open", 4)
	seedDep(t, ctx, conn, "dependencies", "ra-other", "rb-post", "depends_on_external", "blocks")
	execFrozenGuard(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: legacy-shaped row after convergence')")
	before := graphSnapshot(t, ctx, conn)
	closeConn()

	conn, closeConn = openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("converged reopen MigrateUp: %v", err)
	}
	requireDepColumn(t, ctx, conn, "dependencies", "ra-other", "rb-post", "depends_on_external")
	if got := graphSnapshot(t, ctx, conn); got != before {
		t.Errorf("converged reopen changed the store:\n got: %s\nwant: %s", got, before)
	}
	if !ignoredMarkerRecorded(t, ctx, conn, 32) {
		t.Error("ignored marker 0032 lost by the converged reopen")
	}
}

// TestEmbeddedCrossPrefixRepairClonesConverge is AC-8: two clones with the
// same versioned history but different clone-local wisps repair the synced
// dependencies table identically, because a versioned row binds only to the
// versioned issues table, while each clone's own wisp edges resolve against
// its own wisps.
func TestEmbeddedCrossPrefixRepairClonesConverge(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	seedVersioned := func(conn *sql.Conn) {
		seedLegacyIssue(t, ctx, conn, "issues", "rb-open", "open", 1)
		seedLegacyIssue(t, ctx, conn, "issues", "ra-a", "open", 2)
		seedLegacyIssue(t, ctx, conn, "issues", "ra-b", "open", 3)
		seedDep(t, ctx, conn, "dependencies", "ra-a", "rw-1", "depends_on_external", "blocks")
		seedDep(t, ctx, conn, "dependencies", "ra-b", "rb-open", "depends_on_external", "blocks")
	}

	connA, _, closeA := newCrossPrefixRepairDB(t, ctx)
	defer closeA()
	seedVersioned(connA)
	seedLegacyIssue(t, ctx, connA, "wisps", "rw-1", "open", 10)
	seedLegacyIssue(t, ctx, connA, "wisps", "ra-w1", "open", 11)
	seedDep(t, ctx, connA, "wisp_dependencies", "ra-w1", "rw-1", "depends_on_external", "blocks")
	seedDep(t, ctx, connA, "wisp_dependencies", "ra-w1", "rb-open", "depends_on_external", "related")
	commitLegacyStateAndUnmarkRepair(t, ctx, connA)

	connB, _, closeB := newCrossPrefixRepairDB(t, ctx)
	defer closeB()
	seedVersioned(connB)
	seedLegacyIssue(t, ctx, connB, "wisps", "ra-w2", "open", 12)
	seedDep(t, ctx, connB, "wisp_dependencies", "ra-w2", "rw-1", "depends_on_external", "blocks")
	commitLegacyStateAndUnmarkRepair(t, ctx, connB)

	for name, conn := range map[string]*sql.Conn{"A": connA, "B": connB} {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("clone %s repair MigrateUp: %v", name, err)
		}
		requireDepColumn(t, ctx, conn, "dependencies", "ra-a", "rw-1", "depends_on_external")
		requireDepColumn(t, ctx, conn, "dependencies", "ra-b", "rb-open", "depends_on_issue_id")
		requireBlockedStateConsistent(t, ctx, conn)
		if !ignoredMarkerRecorded(t, ctx, conn, 32) {
			t.Errorf("clone %s: ignored marker 0032 not recorded", name)
		}
	}
	requireDepColumn(t, ctx, connA, "wisp_dependencies", "ra-w1", "rw-1", "depends_on_wisp_id")
	requireDepColumn(t, ctx, connA, "wisp_dependencies", "ra-w1", "rb-open", "depends_on_issue_id")
	if blocked, _ := readBlocked(t, ctx, connA, "wisps", "ra-w1"); !blocked {
		t.Error("clone A: ra-w1 is_blocked = false, want true (blocks -> open local wisp rw-1)")
	}
	requireDepColumn(t, ctx, connB, "wisp_dependencies", "ra-w2", "rw-1", "depends_on_external")

	versioned := "SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by," +
		" CAST(created_at AS CHAR), CAST(metadata AS CHAR), thread_id FROM dependencies ORDER BY id"
	if a, b := dumpRows(t, ctx, connA, versioned), dumpRows(t, ctx, connB, versioned); a != b {
		t.Errorf("clones repaired the synced dependencies table differently:\nA: %s\nB: %s", a, b)
	}
}
