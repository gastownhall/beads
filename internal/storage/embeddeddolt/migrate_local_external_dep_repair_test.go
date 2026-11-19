//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Real-Dolt coverage for ignored migration 0032's pre-migration repair
// (repairLocalExternalDepTargets, internal/storage/schema/
// local_external_dep_repair.go, gastownhall/beads#6066): dependency edges a
// prefix-classifying writer stored in depends_on_external although the
// database holds their target move to the typed column presence picks, and
// blocked state is recomputed, once per clone.

// localExternalDepRepairVersion is the ignored marker the repair rides on.
const localExternalDepRepairVersion = 32

type localDepEdge struct {
	table, issueID, target string
}

// localDepRow is one edge's full stored identity: what the repair moves and
// everything it must leave as it was.
type localDepRow struct {
	id, issueTarget, wispTarget, external, depType, createdAt, createdBy, metadata string
}

func readLocalDepRow(t *testing.T, ctx context.Context, conn *sql.Conn, e localDepEdge) localDepRow {
	t.Helper()
	var r localDepRow
	err := conn.QueryRowContext(ctx, `
SELECT id, COALESCE(depends_on_issue_id, ''), COALESCE(depends_on_wisp_id, ''),
       COALESCE(depends_on_external, ''), type, CAST(created_at AS CHAR), created_by,
       CAST(metadata AS CHAR)
FROM `+e.table+`
WHERE issue_id = ? AND ? IN (COALESCE(depends_on_issue_id, ''), COALESCE(depends_on_wisp_id, ''), COALESCE(depends_on_external, ''))`,
		e.issueID, e.target).Scan(&r.id, &r.issueTarget, &r.wispTarget, &r.external, &r.depType, &r.createdAt, &r.createdBy, &r.metadata)
	if err != nil {
		t.Fatalf("read %s edge %s -> %s: %v", e.table, e.issueID, e.target, err)
	}
	return r
}

func readBlockedState(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) (blocked int, updatedAt string) {
	t.Helper()
	if err := conn.QueryRowContext(ctx,
		"SELECT is_blocked, CAST(updated_at AS CHAR) FROM "+table+" WHERE id = ?", id).Scan(&blocked, &updatedAt); err != nil {
		t.Fatalf("read is_blocked for %s %s: %v", table, id, err)
	}
	return blocked, updatedAt
}

// dirtyStatus lists the working-set changes to the committable tables the
// repair writes, empty when both are clean. (The fixture's open leaves other
// clone-local bookkeeping such as leases behind; that is not the repair's.)
func dirtyStatus(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	return scalarString(t, ctx, conn,
		`SELECT COALESCE(GROUP_CONCAT(CONCAT(table_name, ' ', status, IF(staged, ' staged', '')) ORDER BY table_name SEPARATOR ', '), '')
FROM dolt_status WHERE table_name IN ('dependencies', 'issues')`)
}

// seedLocalExternalDepFixture seeds edges stored as external onto held
// foreign-prefix targets in both tables, plus the controls the repair must not
// touch: an explicit "external:" ref and a target this database does not hold.
// Ids are deterministic so the dependency re-key earlier in the pass writes
// nothing.
func seedLocalExternalDepFixture(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	seedIssue(t, ctx, conn, "demo-a")
	seedIssue(t, ctx, conn, "ops-blk")
	seedWisp(t, ctx, conn, "ops-wsp")
	seedWisp(t, ctx, conn, "demo-w")
	for _, e := range []localDepEdge{
		{"dependencies", "demo-a", "ops-blk"},
		{"dependencies", "demo-a", "ops-wsp"},
		{"dependencies", "demo-a", "external:ops:cap"},
		{"dependencies", "demo-a", "ops-gone"},
		{"wisp_dependencies", "demo-w", "ops-blk"},
	} {
		seedDepTable(t, ctx, conn, e.table, depid.New(e.issueID, e.target), e.issueID, nil, nil, strptr(e.target))
	}
	mustExecConn(t, ctx, conn,
		`UPDATE dependencies SET metadata = JSON_OBJECT('note', 'kept'), thread_id = 'thr-1' WHERE issue_id = 'demo-a' AND depends_on_external = 'ops-blk'`)
}

// TestEmbeddedLocalExternalDepRepairMovesHeldTargets: a clone whose edges onto
// held foreign-prefix targets were stored as external upgrades, and the edges
// become local ones with blocked state to match, nothing else about the rows
// changes, and the next open repairs nothing.
func TestEmbeddedLocalExternalDepRepairMovesHeldTargets(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)
	seedLocalExternalDepFixture(t, ctx, conn)
	commitSeed(t, ctx, conn, "test: edges onto held foreign-prefix targets stored as external")
	unrecordIgnoredVersionsFrom(t, ctx, conn, localExternalDepRepairVersion)

	edges := []localDepEdge{
		{"dependencies", "demo-a", "ops-blk"},
		{"dependencies", "demo-a", "ops-wsp"},
		{"dependencies", "demo-a", "external:ops:cap"},
		{"dependencies", "demo-a", "ops-gone"},
		{"wisp_dependencies", "demo-w", "ops-blk"},
	}
	before := map[localDepEdge]localDepRow{}
	for _, e := range edges {
		before[e] = readLocalDepRow(t, ctx, conn, e)
	}
	if blocked, _ := readBlockedState(t, ctx, conn, "issues", "demo-a"); blocked != 0 {
		t.Fatalf("fixture: demo-a is_blocked = %d before the repair, want 0", blocked)
	}
	_, issueUpdatedBefore := readBlockedState(t, ctx, conn, "issues", "demo-a")
	closeConn()

	conn2, _ := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp with edges onto held targets stored as external: %v", err)
	}

	// Each moved edge keeps everything but its target column.
	wantMoved := map[localDepEdge]func(r *localDepRow){
		{"dependencies", "demo-a", "ops-blk"}:      func(r *localDepRow) { r.issueTarget = "ops-blk" },
		{"dependencies", "demo-a", "ops-wsp"}:      func(r *localDepRow) { r.wispTarget = "ops-wsp" },
		{"wisp_dependencies", "demo-w", "ops-blk"}: func(r *localDepRow) { r.issueTarget = "ops-blk" },
	}
	for _, e := range edges {
		want := before[e]
		if move, ok := wantMoved[e]; ok {
			want.external = ""
			move(&want)
		}
		if got := readLocalDepRow(t, ctx, conn2, e); got != want {
			t.Errorf("%s edge %s -> %s after the repair = %+v, want %+v", e.table, e.issueID, e.target, got, want)
		}
	}
	if got := scalarString(t, ctx, conn2,
		"SELECT COALESCE(thread_id, '') FROM dependencies WHERE issue_id = 'demo-a' AND depends_on_issue_id = 'ops-blk'"); got != "thr-1" {
		t.Errorf("moved edge thread_id = %q, want thr-1", got)
	}

	blocked, issueUpdatedAfter := readBlockedState(t, ctx, conn2, "issues", "demo-a")
	if blocked != 1 {
		t.Errorf("demo-a is_blocked = %d after the repair, want 1 (its local blocker ops-blk is open)", blocked)
	}
	if issueUpdatedAfter != issueUpdatedBefore {
		t.Errorf("demo-a updated_at = %s after the repair, want %s unchanged", issueUpdatedAfter, issueUpdatedBefore)
	}
	if blocked, _ := readBlockedState(t, ctx, conn2, "wisps", "demo-w"); blocked != 1 {
		t.Errorf("wisp demo-w is_blocked = %d after the repair, want 1 (its local blocker ops-blk is open)", blocked)
	}
	if !ignoredVersionRecorded(t, ctx, conn2, localExternalDepRepairVersion) {
		t.Fatalf("ignored migration %d not recorded after a successful repair", localExternalDepRepairVersion)
	}
	if dirty := dirtyStatus(t, ctx, conn2); dirty != "" {
		t.Fatalf("working set after the migration commit = %s, want clean", dirty)
	}

	// Reopening writes no repair changes: no new commit, nothing dirty.
	commits := scalarInt(t, ctx, conn2, "SELECT COUNT(*) FROM dolt_log")
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp on the repaired store: %v", err)
	}
	if got := scalarInt(t, ctx, conn2, "SELECT COUNT(*) FROM dolt_log"); got != commits {
		t.Errorf("the second open made %d commits, want 0", got-commits)
	}
	if dirty := dirtyStatus(t, ctx, conn2); dirty != "" {
		t.Errorf("working set after the second open = %s, want clean", dirty)
	}
}

// TestEmbeddedLocalExternalDepRepairRefusesDirtyTables: a needed repair over
// uncommitted user changes in issues or dependencies refuses with the typed
// error before writing, leaves the marker pending, and runs once the user's
// changes are committed.
func TestEmbeddedLocalExternalDepRepairRefusesDirtyTables(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	for _, tc := range []struct {
		name, table string
		dirty       func(t *testing.T, ctx context.Context, conn *sql.Conn)
	}{
		{"dirty issues", "issues", func(t *testing.T, ctx context.Context, conn *sql.Conn) {
			seedIssue(t, ctx, conn, "demo-uncommitted")
		}},
		{"dirty dependencies", "dependencies", func(t *testing.T, ctx context.Context, conn *sql.Conn) {
			mustExecConn(t, ctx, conn,
				`UPDATE dependencies SET metadata = JSON_OBJECT('user', 'edit') WHERE issue_id = 'demo-a' AND depends_on_external = 'ops-gone'`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := seedMigratedStore(t, ctx)
			conn, closeConn := openDepConn(t, ctx, dataDir)
			seedLocalExternalDepFixture(t, ctx, conn)
			commitSeed(t, ctx, conn, "test: edges onto held foreign-prefix targets stored as external")
			unrecordIgnoredVersionsFrom(t, ctx, conn, localExternalDepRepairVersion)
			tc.dirty(t, ctx, conn)
			closeConn()

			conn2, closeConn2 := openDepConn(t, ctx, dataDir)
			deps, wispDeps := readDeps(t, ctx, conn2), readDepTable(t, ctx, conn2, "wisp_dependencies")

			_, err := schema.MigrateUp(ctx, conn2)
			var dirtyErr *schema.DirtyTablesError
			if !errors.As(err, &dirtyErr) {
				t.Fatalf("MigrateUp over a needed repair and dirty %s = %v, want a *schema.DirtyTablesError", tc.table, err)
			}
			if !slices.Contains(dirtyErr.Tables, tc.table) {
				t.Errorf("dirtyErr.Tables = %v, want it to name %s", dirtyErr.Tables, tc.table)
			}
			if got := readDeps(t, ctx, conn2); !slices.Equal(got, deps) {
				t.Errorf("dependencies = %v after the refusal, want %v untouched", got, deps)
			}
			if got := readDepTable(t, ctx, conn2, "wisp_dependencies"); !slices.Equal(got, wispDeps) {
				t.Errorf("wisp_dependencies = %v after the refusal, want %v untouched", got, wispDeps)
			}
			if blocked, _ := readBlockedState(t, ctx, conn2, "issues", "demo-a"); blocked != 0 {
				t.Errorf("demo-a is_blocked = %d after the refusal, want 0 (nothing written)", blocked)
			}
			if ignoredVersionRecorded(t, ctx, conn2, localExternalDepRepairVersion) {
				t.Fatalf("ignored migration %d recorded despite the refusal", localExternalDepRepairVersion)
			}

			// The recovery the refusal names: commit the user's changes, reopen.
			commitSeed(t, ctx, conn2, "test: commit the user's working set")
			closeConn2()

			conn3, _ := openDepConn(t, ctx, dataDir)
			if _, err := schema.MigrateUp(ctx, conn3); err != nil {
				t.Fatalf("MigrateUp after committing the working set: %v", err)
			}
			if got := readLocalDepRow(t, ctx, conn3, localDepEdge{"dependencies", "demo-a", "ops-blk"}); got.issueTarget != "ops-blk" || got.external != "" {
				t.Errorf("demo-a -> ops-blk after the retried pass = %+v, want it in depends_on_issue_id", got)
			}
			if blocked, _ := readBlockedState(t, ctx, conn3, "issues", "demo-a"); blocked != 1 {
				t.Errorf("demo-a is_blocked = %d after the retried pass, want 1", blocked)
			}
			if !ignoredVersionRecorded(t, ctx, conn3, localExternalDepRepairVersion) {
				t.Fatalf("ignored migration %d not recorded after the retried pass", localExternalDepRepairVersion)
			}
		})
	}
}
