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

// TestEmbeddedMigrateFinishesStrandedOwnStep strands one step and runs the
// next MigrateUp on the same store: it must finish the stranded step with the
// commit it missed and converge to the latest version with a clean working
// set. 46 and 54 are the steps of the three specimens measured on a loaded box
// (2026-09-21); 49 alters issues, which later pending migrations also alter,
// so unfixed code refuses it; 73 alters issues and no later migration does, so
// unfixed code commits its cursor row with migration 74 and leaves its ALTER
// dirty under a HEAD that claims it applied.
func TestEmbeddedMigrateFinishesStrandedOwnStep(t *testing.T) {
	requireEmbedded(t)
	for _, stranded := range []int{46, 49, 54, 73} {
		t.Run(fmt.Sprintf("v%d", stranded), func(t *testing.T) {
			ctx := t.Context()
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
		})
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
