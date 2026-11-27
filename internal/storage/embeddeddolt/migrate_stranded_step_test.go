//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests cover the step a migration pass strands when it is killed
// between a numbered migration's cursor row and that step's per-step commit
// (commitMigrationStep). Before the fix the next MigrateUp refused the step's
// own work with *schema.DirtyTablesError (when a later pending migration
// touches the same table) or committed the cursor row with a later step while
// leaving the step's change uncommitted (when none does).
//
// The layout and the first tests come from @bee-ghosttrack's PR #8603.

// seedStrandFixtureAt seeds a store at version `at` the way a production
// store at `at` looks to the next open: the canonical dolt_ignore patterns
// MigrateUp seeds at every open are committed (MigrateUpTo seeds none, and an
// uncommitted seed would make the retry's own seed commit sweep a stranded
// step's dolt_ignore row out of sight), and issues, comments and
// dependencies hold a few rows for the steps to rewrite.
func seedStrandFixtureAt(t *testing.T, ctx context.Context, at int) string {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, at)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	for _, pattern := range schema.DoltIgnoreSeedPatternsForTest() {
		mustExecStranded(t, ctx, conn, "INSERT IGNORE INTO dolt_ignore VALUES (?, true)", pattern)
	}
	for _, id := range []string{"bd-s1", "bd-s2"} {
		mustExecStranded(t, ctx, conn,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
				"VALUES (?, 'fixture row', 'body', '', '', '', 'open', 2, 'task')", id)
	}
	mustExecStranded(t, ctx, conn,
		"INSERT INTO comments (id, issue_id, author, text, created_at) "+
			"VALUES ('00000000-0000-0000-0000-00000000c001', 'bd-s1', 'fixture', 'a comment', '2025-01-01 00:00:00')")
	mustExecStranded(t, ctx, conn,
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) "+
			"VALUES ('00000000-0000-0000-0000-00000000d001', 'bd-s2', 'bd-s1', 'blocks', 'fixture')")
	mustExecStranded(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustExecStranded(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed stranded-step fixture')")
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("fixture at %d left a dirty working set: %v", at, dirty)
	}
	return dataDir
}

// strandStepAt seeds a store committed clean at version at-1, then applies
// migration `at` through the real step runner without its per-step commit.
// MigrateUpTo's test-support path runs the same snapshot, pre-repair, body,
// updated_at restore and cursor INSERT a production step runs, and skips only
// commitMigrationStep (its per-step commit is gated to upTo == 0), so the
// working set it leaves is what a process killed between a step's cursor row
// and its commit leaves behind.
func strandStepAt(t *testing.T, ctx context.Context, at int) string {
	t.Helper()
	return strandStepsThrough(t, ctx, at-1, at)
}

// strandStepsThrough seeds at `seed` and leaves migrations seed+1..through
// applied and uncommitted.
func strandStepsThrough(t *testing.T, ctx context.Context, seed, through int) string {
	t.Helper()
	dataDir := seedStrandFixtureAt(t, ctx, seed)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := schema.MigrateUpTo(ctx, conn, through); err != nil {
		t.Fatalf("stranding migrations %d..%d: %v", seed+1, through, err)
	}
	if v := currentMainVersion(t, ctx, conn); v != through {
		t.Fatalf("working cursor after stranding = %d, want %d", v, through)
	}
	if v := headMainVersion(t, ctx, conn); v != seed {
		t.Fatalf("HEAD cursor after stranding = %d, want %d (the stranded steps must be uncommitted)", v, seed)
	}
	if dirty := dirtyTableNames(t, ctx, conn); !contains(dirty, "schema_migrations") {
		t.Fatalf("working set after stranding = %v, want schema_migrations dirty", dirty)
	}
	return dataDir
}

func mustExecStranded(t *testing.T, ctx context.Context, conn *sql.Conn, query string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func headMainVersion(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations AS OF 'HEAD'")
}

func stepMessagePattern(version int) string {
	return fmt.Sprintf("schema: apply migration %04d\\_%%", version)
}

// stepCommitHash returns the one commit on HEAD's history that
// commitMigrationStep made for migration `version`.
func stepCommitHash(t *testing.T, ctx context.Context, conn *sql.Conn, version int) string {
	t.Helper()
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message LIKE ?", stepMessagePattern(version)); n != 1 {
		t.Fatalf("commits for migration %04d on HEAD's history = %d, want 1", version, n)
	}
	var hash string
	if err := conn.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message LIKE ?",
		stepMessagePattern(version)).Scan(&hash); err != nil {
		t.Fatalf("find the %04d commit: %v", version, err)
	}
	return hash
}

// stepCommitTables returns, sorted, the tables changed by the commit
// commitMigrationStep made for migration `version` on HEAD's history.
func stepCommitTables(t *testing.T, ctx context.Context, conn *sql.Conn, version int) []string {
	t.Helper()
	return queryStrings(t, ctx, conn,
		"SELECT table_name FROM dolt_diff WHERE message LIKE ? ORDER BY table_name", stepMessagePattern(version))
}

func queryStrings(t *testing.T, ctx context.Context, conn *sql.Conn, query string, args ...any) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan %q: %v", query, err)
		}
		out = append(out, s.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return out
}

// uninterruptedStepCommitTables seeds a twin store at version-1, migrates it
// with one MigrateUp nothing interrupts, and returns the tables that pass's
// commit for migration `version` changed: the commit a finished stranded step
// must reproduce.
func uninterruptedStepCommitTables(t *testing.T, ctx context.Context, version int) []string {
	t.Helper()
	dataDir := seedStrandFixtureAt(t, ctx, version-1)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("uninterrupted MigrateUp from %d: %v", version-1, err)
	}
	tables := stepCommitTables(t, ctx, conn, version)
	if len(tables) == 0 {
		t.Fatalf("an uninterrupted pass from %d made no commit for migration %d", version-1, version)
	}
	return tables
}

// retryStranded runs the MigrateUp that follows a stranded step and requires
// it to succeed with HEAD and the working set at the latest version, clean.
func retryStranded(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	t.Logf("working set before retry: %v", dirtyTableNames(t, ctx, conn))
	applied, err := schema.MigrateUp(ctx, conn)
	if err != nil {
		var dirty *schema.DirtyTablesError
		if errors.As(err, &dirty) {
			t.Fatalf("retry MigrateUp refused the stranded step with *DirtyTablesError %v: %v", dirty.Tables, err)
		}
		t.Fatalf("retry MigrateUp: %v", err)
	}
	if v := currentMainVersion(t, ctx, conn); v != schema.LatestVersion() {
		t.Fatalf("schema version after retry = %d, want latest %d", v, schema.LatestVersion())
	}
	if v := headMainVersion(t, ctx, conn); v != schema.LatestVersion() {
		t.Fatalf("HEAD schema version after retry = %d, want latest %d", v, schema.LatestVersion())
	}
	// seedMainSchemaAt runs the main series alone and commits with DOLT_ADD
	// -A, so a seed at or above 55 tracks leases before the ignored series
	// that ignores it has run, and any pass from such a seed leaves leases
	// dirty, stranded step or not (#8603 filtered it the same way). Below 55
	// the seed has no leases table to track.
	dirty := slices.DeleteFunc(dirtyTableNames(t, ctx, conn), func(name string) bool { return name == "leases" })
	if len(dirty) != 0 {
		t.Fatalf("working set after retry is dirty: %v, want clean (the stranded step's tables must be committed)", dirty)
	}
	return applied
}

// TestEmbeddedMigrateFinishesStrandedOwnStep strands one step whose tables a
// later pending migration also touches and runs the next MigrateUp on the
// same store: it must finish the stranded step with the commit it missed and
// converge to the latest version with a clean working set. 46 and 54 are the
// steps of the specimens measured on a loaded box (2026-09-21); 49 also
// alters comments, which the aux-rekey exemption drops from the pass's dirty
// set on a lineage below main 51, as this seed is.
func TestEmbeddedMigrateFinishesStrandedOwnStep(t *testing.T) {
	requireEmbedded(t)
	for _, stranded := range []int{46, 49, 54} {
		t.Run(fmt.Sprintf("v%d", stranded), func(t *testing.T) {
			ctx := t.Context()
			wantStepTables := uninterruptedStepCommitTables(t, ctx, stranded)
			dataDir := strandStepAt(t, ctx, stranded)

			conn, closeConn := openPinnedConn(t, ctx, dataDir)
			defer closeConn()
			retryStranded(t, ctx, conn)

			stepCommitHash(t, ctx, conn, stranded) // exactly one, under its own message
			if got := stepCommitTables(t, ctx, conn, stranded); !slices.Equal(got, wantStepTables) {
				t.Fatalf("tables in the finished %04d commit = %v, want %v (the tables an uninterrupted pass commits with the step)",
					stranded, got, wantStepTables)
			}
		})
	}
}
