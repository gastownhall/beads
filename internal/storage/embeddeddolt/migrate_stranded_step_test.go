//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests cover the step a migration pass strands when it is killed
// between a numbered migration's cursor row and that step's per-step commit
// (commitMigrationStep). Before the fix the next MigrateUp refused the step's
// own debris with *schema.DirtyTablesError (when a later pending migration
// touches the same table) or committed the cursor row while leaving the step's
// tables dirty (when none does), and nothing in bd recovered either.

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
	dataDir := seedMainSchemaAt(t, ctx, seed)
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

func headMainVersion(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations AS OF 'HEAD'")
}

// stepCommitTables returns, sorted, the tables changed by the commit
// commitMigrationStep made for migration `version` on HEAD's history.
func stepCommitTables(t *testing.T, ctx context.Context, conn *sql.Conn, version int) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT table_name FROM dolt_diff WHERE message LIKE ? ORDER BY table_name",
		fmt.Sprintf("schema: apply migration %04d_%%", version))
	if err != nil {
		t.Fatalf("read dolt_diff for migration %d: %v", version, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan dolt_diff: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read dolt_diff for migration %d: %v", version, err)
	}
	return out
}

// uninterruptedStepCommitTables seeds a second store at version-1, migrates it
// with one MigrateUp nothing interrupts, and returns the tables that pass's
// commit for migration `version` changed: the commit a finished stranded step
// must reproduce.
func uninterruptedStepCommitTables(t *testing.T, ctx context.Context, version int) []string {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, version-1)
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

// TestEmbeddedMigrateFinishesStrandedOwnStep strands one step and runs the
// next MigrateUp on the same store: it must finish the stranded step with the
// commit it missed and converge to the latest version with a clean working
// set. 46 and 54 are the steps of the three specimens measured on a loaded box
// (2026-09-21); 49 alters issues, which later pending migrations also alter,
// so unfixed code refuses it; 73 alters issues and no later migration does, so
// unfixed code commits its cursor row with migration 74 and leaves its ALTER
// dirty under a HEAD that claims it applied.
//
// The commit it missed is the one an uninterrupted pass from the same seed
// makes for the step, table for table. 49 also alters comments, which the
// aux-rekey exemption drops from the pass's dirty set on a lineage below main
// 51, as this seed is: the finish must commit it with the step all the same.
func TestEmbeddedMigrateFinishesStrandedOwnStep(t *testing.T) {
	requireEmbedded(t)
	for _, stranded := range []int{46, 49, 54, 73} {
		t.Run(fmt.Sprintf("v%d", stranded), func(t *testing.T) {
			ctx := t.Context()
			wantStepTables := uninterruptedStepCommitTables(t, ctx, stranded)
			dataDir := strandStepAt(t, ctx, stranded)

			conn, closeConn := openPinnedConn(t, ctx, dataDir)
			defer closeConn()
			t.Logf("working set before retry: %v", dirtyTableNames(t, ctx, conn))

			applied, err := schema.MigrateUp(ctx, conn)
			if err != nil {
				var dirty *schema.DirtyTablesError
				if errors.As(err, &dirty) {
					t.Fatalf("retry MigrateUp refused the stranded step with *DirtyTablesError %v: %v", dirty.Tables, err)
				}
				t.Fatalf("retry MigrateUp: %v", err)
			}
			t.Logf("retry applied %d migrations", applied)
			if v := currentMainVersion(t, ctx, conn); v != schema.LatestVersion() {
				t.Fatalf("schema version after retry = %d, want latest %d", v, schema.LatestVersion())
			}
			if v := headMainVersion(t, ctx, conn); v != schema.LatestVersion() {
				t.Fatalf("HEAD schema version after retry = %d, want latest %d", v, schema.LatestVersion())
			}
			dirty := dirtyTableNames(t, ctx, conn)
			if stranded >= 55 {
				// seedMainSchemaAt runs the main series alone and commits with
				// DOLT_ADD -A, so a seed at or above 55 tracks leases before the
				// ignored series that ignores it has run; the retry then leaves
				// leases dirty on an unstranded seed too. That is the seed's
				// shape, not the stranded step's.
				dirty = slices.DeleteFunc(dirty, func(name string) bool { return name == "leases" })
			}
			if len(dirty) != 0 {
				t.Fatalf("working set after retry is dirty: %v, want clean (the stranded step's tables must be committed)", dirty)
			}
			// The stranded step is committed on its own, under the message
			// commitMigrationStep gives it, not folded into a later commit.
			prefix := fmt.Sprintf("schema: apply migration %04d_", stranded)
			if n := scalarInt(t, ctx, conn,
				"SELECT COUNT(*) FROM dolt_log WHERE message LIKE ?", prefix+"%"); n != 1 {
				t.Fatalf("commits whose message starts %q = %d, want 1", prefix, n)
			}
			if got := stepCommitTables(t, ctx, conn, stranded); !slices.Equal(got, wantStepTables) {
				t.Fatalf("tables in the finished %04d commit = %v, want %v (the tables an uninterrupted pass commits with the step)",
					stranded, got, wantStepTables)
			}
		})
	}
}

// TestEmbeddedMigrateStrandedStepSurvivesASecondKill strands migration 49 and
// kills the retry right after the series' last step commits, before the
// pass's tail (rekeys, ignored series, final commit) runs. A pass killed
// between steps must leave a clean working set (#4566), and the finished step
// is no exception: it must have committed everything 0049 changed, comments
// included, so the second kill leaves nothing of a step HEAD already claims
// in the working set, and a third open converges. A finish that left
// comments to the pass's final commit left it dirty here, and the third
// open, starting from the latest main version, past the aux-rekey
// exemption's windows, treated it as user dirt and never committed it.
func TestEmbeddedMigrateStrandedStepSurvivesASecondKill(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	const stranded = 49
	dataDir := strandStepAt(t, ctx, stranded)

	latest := schema.LatestVersion()
	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version == latest {
			return fmt.Errorf("injected fault: process killed after migration %d", latest)
		}
		return nil
	})
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	_, err := schema.MigrateUp(ctx, conn)
	closeConn()
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("retry MigrateUp err = %v, want the injected fault", err)
	}

	conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
	defer closeConn2()
	if v := headMainVersion(t, ctx, conn2); v != latest {
		t.Fatalf("HEAD schema version after the second kill = %d, want %d", v, latest)
	}
	if dirty := dirtyTableNames(t, ctx, conn2); len(dirty) != 0 {
		t.Fatalf("working set after the second kill = %v, want clean (the finished %04d and every later step commit their own tables)",
			dirty, stranded)
	}
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("third MigrateUp: %v", err)
	}
	if v := headMainVersion(t, ctx, conn2); v != latest {
		t.Fatalf("HEAD schema version after the third open = %d, want %d", v, latest)
	}
	if dirty := dirtyTableNames(t, ctx, conn2); len(dirty) != 0 {
		t.Fatalf("working set after the third open = %v, want clean", dirty)
	}
}

// TestEmbeddedMigrateStrandedStepWithExtraDirtRefuses strands migration 49
// and also leaves a user's uncommitted row in config, a table 49 does not
// touch. The working set is then not provably the step's own debris, so the
// pass must refuse exactly as it did before the fix and commit no migration.
func TestEmbeddedMigrateStrandedStepWithExtraDirtRefuses(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	const stranded = 49
	dataDir := strandStepAt(t, ctx, stranded)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	const dirtKey = "user-dirt-beside-stranded-step"
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES (?, ?)", dirtKey, "1"); err != nil {
		t.Fatalf("insert user dirt into config: %v", err)
	}
	before := snapshotStrandedRefusal(t, ctx, conn)

	_, err := schema.MigrateUp(ctx, conn)
	var dirty *schema.DirtyTablesError
	if !errors.As(err, &dirty) {
		t.Fatalf("MigrateUp err = %v, want *DirtyTablesError", err)
	}
	if !slices.Equal(dirty.Tables, []string{"issues"}) {
		t.Fatalf("DirtyTablesError tables = %v, want [issues]", dirty.Tables)
	}
	before.assertUnchanged(t, ctx, conn)
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM config WHERE `key` = ?", dirtKey); n != 1 {
		t.Fatalf("working config rows = %d, want 1", n)
	}
}

// TestEmbeddedMigrateStrandedStepCommitsUserDirtInItsOwnTable strands
// migration 49 and then leaves a user's uncommitted row in issues, a table 49
// itself alters. Recognition is by table, not by row, so the row cannot be
// told apart from the step's own change, and the finish commits it with the
// step, under the step's message (the bd dolt commit the old refusal
// prescribed committed it too, under the user's). The CHANGELOG states this;
// the test pins it, so that refusing such a working set instead is a
// deliberate change.
func TestEmbeddedMigrateStrandedStepCommitsUserDirtInItsOwnTable(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	const stranded = 49
	dataDir := strandStepAt(t, ctx, stranded)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('bd-user1', 'user row', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("insert user dirt into issues: %v", err)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("retry MigrateUp: %v", err)
	}
	var stepCommit string
	if err := conn.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log WHERE message LIKE ?",
		fmt.Sprintf("schema: apply migration %04d_%%", stranded)).Scan(&stepCommit); err != nil {
		t.Fatalf("find the %04d commit: %v", stranded, err)
	}
	if n := scalarInt(t, ctx, conn,
		"SELECT COUNT(*) FROM issues AS OF '"+stepCommit+"' WHERE id = 'bd-user1'"); n != 1 {
		t.Fatalf("user row in the %04d commit: %d rows, want 1 (committed with the step)", stranded, n)
	}
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("working set after retry is dirty: %v, want clean", dirty)
	}
}

// TestEmbeddedMigrateStrandedStepWithoutItsContentHashRefuses strands
// migration 49 and then changes its cursor row's content_hash, to NULL and to
// a well-formed hash no migration file has. Only runMigrations writes the
// frozen file's hash, so such a row was marked by hand or by another build,
// and the working set is not provably this binary's stranded step: the pass
// must refuse exactly as it did before the fix and commit no migration.
func TestEmbeddedMigrateStrandedStepWithoutItsContentHashRefuses(t *testing.T) {
	requireEmbedded(t)
	const stranded = 49
	for _, tc := range []struct {
		name string
		hash any
	}{
		{name: "null", hash: nil},
		{name: "mismatched", hash: strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dataDir := strandStepAt(t, ctx, stranded)

			conn, closeConn := openPinnedConn(t, ctx, dataDir)
			defer closeConn()
			res, err := conn.ExecContext(ctx,
				"UPDATE schema_migrations SET content_hash = ? WHERE version = ?", tc.hash, stranded)
			if err != nil {
				t.Fatalf("set content_hash of cursor row %d: %v", stranded, err)
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("set content_hash of cursor row %d: rows affected = %d (err %v), want 1", stranded, n, err)
			}
			before := snapshotStrandedRefusal(t, ctx, conn)

			_, err = schema.MigrateUp(ctx, conn)
			var dirty *schema.DirtyTablesError
			if !errors.As(err, &dirty) {
				t.Fatalf("MigrateUp err = %v, want *DirtyTablesError", err)
			}
			if !slices.Equal(dirty.Tables, []string{"issues"}) {
				t.Fatalf("DirtyTablesError tables = %v, want [issues]", dirty.Tables)
			}
			before.assertUnchanged(t, ctx, conn)
		})
	}
}

// TestEmbeddedMigrateTwoStrandedStepsRefuse leaves migrations 49 and 50 both
// applied and uncommitted. A per-step-committing pass strands at most one
// step, so two uncommitted cursor rows are not the signature of one stranded
// step (they are what an older whole-pass binary, or a hand-run migration,
// leaves), and the pass must refuse as before.
func TestEmbeddedMigrateTwoStrandedStepsRefuse(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := strandStepsThrough(t, ctx, 48, 50)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	before := snapshotStrandedRefusal(t, ctx, conn)

	_, err := schema.MigrateUp(ctx, conn)
	var dirty *schema.DirtyTablesError
	if !errors.As(err, &dirty) {
		t.Fatalf("MigrateUp err = %v, want *DirtyTablesError", err)
	}
	before.assertUnchanged(t, ctx, conn)
}

// TestEmbeddedMigrateUserDirtWithoutStrandedStepRefuses leaves a user's
// uncommitted row in issues, a table pending migrations alter, with no
// stranded cursor row. That is the #4566 guard's own case, and the pass must
// refuse as before and leave the row uncommitted.
func TestEmbeddedMigrateUserDirtWithoutStrandedStepRefuses(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 48)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('bd-user1', 'user row', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("insert user dirt into issues: %v", err)
	}
	before := snapshotStrandedRefusal(t, ctx, conn)

	_, err := schema.MigrateUp(ctx, conn)
	var dirty *schema.DirtyTablesError
	if !errors.As(err, &dirty) {
		t.Fatalf("MigrateUp err = %v, want *DirtyTablesError", err)
	}
	if !slices.Equal(dirty.Tables, []string{"issues"}) {
		t.Fatalf("DirtyTablesError tables = %v, want [issues]", dirty.Tables)
	}
	before.assertUnchanged(t, ctx, conn)
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues AS OF 'HEAD' WHERE id = 'bd-user1'"); n != 0 {
		t.Fatalf("user row committed at HEAD (%d rows), want it left uncommitted", n)
	}
}

// strandedRefusalState is what a refused pass must leave as it found: the commit
// log apart from MigrateUp's own dolt_ignore seed commit (which runs before
// the guard by design), the HEAD cursor, and the dirty tables.
type strandedRefusalState struct {
	log         []string
	headVersion int
	dirty       []string
}

func snapshotStrandedRefusal(t *testing.T, ctx context.Context, conn *sql.Conn) strandedRefusalState {
	t.Helper()
	return strandedRefusalState{
		log:         strandedLogMessages(t, ctx, conn),
		headVersion: headMainVersion(t, ctx, conn),
		dirty:       dirtyTableNames(t, ctx, conn),
	}
}

func (b strandedRefusalState) assertUnchanged(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	after := strandedLogMessages(t, ctx, conn)
	if len(after) < len(b.log) || !slices.Equal(after[len(after)-len(b.log):], b.log) {
		t.Fatalf("commit log rewritten by a refused pass: before %q, after %q", b.log, after)
	}
	for _, msg := range after[:len(after)-len(b.log)] {
		if msg != "schema: seed dolt_ignore patterns" {
			t.Fatalf("refused pass committed %q, want no commit but the dolt_ignore seed", msg)
		}
	}
	if v := headMainVersion(t, ctx, conn); v != b.headVersion {
		t.Fatalf("HEAD schema version after refusal = %d, want %d", v, b.headVersion)
	}
	if dirty := dirtyTableNames(t, ctx, conn); !slices.Equal(dirty, b.dirty) {
		t.Fatalf("dirty tables after refusal = %v, want %v as before", dirty, b.dirty)
	}
}

// strandedLogMessages returns the commit messages on HEAD's history, newest first.
func strandedLogMessages(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	return out
}
