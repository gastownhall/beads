//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// repairCommitMessage is the exact commit message the ignored-0032 hook's
// scoped repair commit carries (migration_0075_is_blocked_repair.go). The
// commit is found BY MESSAGE because its hash is fixture-dependent.
const repairCommitMessage = "schema: repair is_blocked flags over-set by migration 0059 (gastownhall/beads#7037)"

// TestEmbeddedMigrate0075RepairCommitPublishesDedupGraphWithFlags pins AC-4 of
// gastownhall/beads#7037: on a pass that also runs the dependency-id re-key,
// the is_blocked repair must publish the re-key's deduplicated graph and the
// flags recomputed FROM that graph in one commit, so a store inspected at the
// repair commit sees flags that match the graph it holds — never flags from
// the pre-dedup graph next to the deduped table, or a deduped table whose
// derived flags were left in the working set.
//
// The dedup fixture is the #5268 shape: issue dg-dup holds the same logical
// edge twice, once typed issue (relates-to — never blocks) and once typed wisp
// (blocks, against an open wisp — blocks today). Both rows derive the same
// deterministic id, the re-key keeps the issue-typed survivor and deletes the
// wisp-typed loser, and that collapse is exactly what flips dg-dup's correct
// flag from blocked to free. If the repair commit published flags without the
// dependencies output (or vice versa), the commit would hold a graph and flags
// that disagree.
func TestEmbeddedMigrate0075RepairCommitPublishesDedupGraphWithFlags(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 74)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 31); err != nil {
		t.Fatalf("MigrateIgnoredUpTo: %v", err)
	}

	// dg-twin exists on BOTH planes under one id: the issue the survivor row
	// names and the open wisp the loser row blocks on. depid keys on the
	// resolved target string, so both rows derive the same deterministic id
	// and the re-key merges them.
	rows := []stampedIssue{
		{id: "dg-blocker", status: "open", stamp: historicalStamp(1)},
		{id: "dg-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
		{id: "dg-related", status: "open", stamp: historicalStamp(3)},
		{id: "dg-twin", status: "open", stamp: historicalStamp(4)},
		{id: "dg-dup", status: "open", stamp: historicalStamp(5), wantBlocked: true},
	}
	insertStampedIssues(t, ctx, conn, "issues", rows, true)
	mustExecOn(t, ctx, conn, `
INSERT INTO wisps (id, title, status, is_blocked, updated_at)
VALUES ('dg-twin', 'open wisp twin of the issue id', 'open', 0, ?)
`, historicalStamp(6))
	insertIssueEdges(t, ctx, conn, [][3]string{
		{"dg-blocked", "dg-blocker", "blocks"},
		{"dg-related", "dg-blocked", "relates-to"},
	})
	// The duplicated edge: issue-typed survivor first, wisp-typed loser
	// second, both carrying pre-#4259 random ids so the re-key owns them.
	mustExecOn(t, ctx, conn, `
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_by) VALUES
  (UUID(), 'dg-dup', 'dg-twin', NULL, 'relates-to', 'fixture'),
  (UUID(), 'dg-dup', NULL, 'dg-twin', 'blocks', 'fixture')
`)
	// dg-related carries the 0059-era over-set; dg-dup's 1 is CORRECT for the
	// pre-dedup graph (the wisp-typed blocks row) and must flip once the
	// re-key collapses the duplicate — the whole point of this pin.
	mustExecOn(t, ctx, conn, "UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id = 'dg-related'")
	commitFixture(t, ctx, conn, "test: v74 store with a #5268 duplicate edge and 0059-era over-set flags")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	// The repair commit exists exactly once, and only dependencies, issue
	// flags and the obligation key changed in it.
	hash, parent := repairCommitAndParent(t, ctx, conn)
	changed := changedTablesAtCommit(t, ctx, conn, parent, hash)
	want := map[string]bool{"dependencies": true, "issues": true, "metadata": true}
	for table := range changed {
		if !want[table] {
			t.Errorf("repair commit changed %q too; only dependencies, issue flags and the key belong in it (changed: %v)", table, keysOf(changed))
		}
	}
	for table := range want {
		if !changed[table] {
			t.Errorf("repair commit did not change %q; the rekey output, the recomputed flags and the key deletion must publish together (changed: %v)", table, keysOf(changed))
		}
	}

	// Flags at the repair commit match the graph at the repair commit. The
	// graph there is the deduped one: exactly one dg-dup edge, the
	// issue-typed relates-to survivor.
	var dupRows int
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM dependencies AS OF '`+hash+`'
WHERE issue_id = 'dg-dup'`).Scan(&dupRows); err != nil {
		t.Fatalf("count dg-dup edges at the repair commit: %v", err)
	}
	if dupRows != 1 {
		t.Fatalf("dependencies at the repair commit holds %d dg-dup edge(s), want 1 (the merged survivor)", dupRows)
	}
	var survivorType string
	var survivorIsIssueTyped, survivorIsWispTyped bool
	if err := conn.QueryRowContext(ctx, `
SELECT type, depends_on_issue_id IS NOT NULL, depends_on_wisp_id IS NOT NULL
FROM dependencies AS OF '`+hash+`' WHERE issue_id = 'dg-dup'`).Scan(&survivorType, &survivorIsIssueTyped, &survivorIsWispTyped); err != nil {
		t.Fatalf("read the surviving dg-dup edge at the repair commit: %v", err)
	}
	if !survivorIsIssueTyped || survivorIsWispTyped || survivorType != "relates-to" {
		t.Fatalf("surviving dg-dup edge = (type %q, issue-typed %v, wisp-typed %v); the merge must keep the issue-typed relates-to row", survivorType, survivorIsIssueTyped, survivorIsWispTyped)
	}
	wantFlags := map[string]bool{
		"dg-blocker": false,
		"dg-blocked": true,
		"dg-related": false,
		"dg-twin":    false,
		"dg-dup":     false,
	}
	for id, wantBlocked := range wantFlags {
		var got bool
		if err := conn.QueryRowContext(ctx,
			"SELECT is_blocked FROM issues AS OF '"+hash+"' WHERE id = '"+id+"'").Scan(&got); err != nil {
			t.Fatalf("read issues %s is_blocked at the repair commit: %v", id, err)
		}
		if got != wantBlocked {
			t.Errorf("issues %s: is_blocked = %v at the repair commit, want %v (flags must match that commit's deduped graph)", id, got, wantBlocked)
		}
	}

	// And the working set agrees: nothing left to correct, obligation gone,
	// stamps preserved.
	if corrected, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
		t.Fatalf("counting is_blocked inconsistencies: %v", err)
	} else if corrected != 0 {
		t.Errorf("full recompute still corrects %d row(s) after the pass", corrected)
	}
	if keyRows := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_repair_owed_0075'"); keyRows != 0 {
		t.Errorf("obligation key still holds %d row(s) after the repair", keyRows)
	}
	requireStamp(t, ctx, conn, "issues", "dg-dup", historicalStamp(5))
	requireStamp(t, ctx, conn, "issues", "dg-related", historicalStamp(3))
}

// repairCommitAndParent finds the scoped repair commit by its message and
// returns its hash and its parent's hash, walking dolt_log's first-parent
// order (the row after a commit is its parent).
func repairCommitAndParent(t *testing.T, ctx context.Context, conn *sql.Conn) (hash, parent string) {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	type commit struct{ hash, message string }
	var order []commit
	for rows.Next() {
		var c commit
		if err := rows.Scan(&c.hash, &c.message); err != nil {
			t.Fatalf("scan dolt_log row: %v", err)
		}
		order = append(order, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_log: %v", err)
	}
	for i, c := range order {
		if c.message != repairCommitMessage || i+1 >= len(order) {
			continue
		}
		return c.hash, order[i+1].hash
	}
	t.Fatalf("no commit with message %q in dolt_log (%d commits); the repair commit must exist", repairCommitMessage, len(order))
	return "", ""
}

// changedTablesAtCommit reports which tables tracked at commit `to` carry a
// diff between `from` and `to`. Ignored (never-committed) tables cannot appear
// because the table list itself is read AS OF the commit.
func changedTablesAtCommit(t *testing.T, ctx context.Context, conn *sql.Conn, from, to string) map[string]bool {
	t.Helper()
	tables, err := showTablesAsOf(t, ctx, conn, to)
	if err != nil {
		t.Fatalf("list tables at %s: %v", to, err)
	}
	changed := make(map[string]bool)
	for _, table := range tables {
		// from/to are hashes read from dolt_log in this test, never input.
		//nolint:gosec // G201: table comes from SHOW TABLES, diff range from dolt_log.
		n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_diff('"+from+"', '"+to+"', '"+table+"')")
		if n > 0 {
			changed[table] = true
		}
	}
	return changed
}

func showTablesAsOf(t *testing.T, ctx context.Context, conn *sql.Conn, ref string) ([]string, error) {
	t.Helper()
	// FULL, base tables only: the schema's ready/blocked views show up in a
	// bare SHOW TABLES and dolt_diff refuses them with 1146.
	rows, err := conn.QueryContext(ctx, "SHOW FULL TABLES AS OF '"+ref+"'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		if kind == "BASE TABLE" {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
