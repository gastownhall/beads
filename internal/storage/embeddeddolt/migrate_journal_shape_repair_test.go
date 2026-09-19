//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin ignored migration 0023, the events-journal shape repair
// (bd-t9ovd). A workspace provisioned from the fork lineage that created the
// journal tables under ignored slot 0017 arrives with its ignored cursor past
// 0022, so upstream's 0017..0022 are recorded as applied without ever having
// run: the payload columns stay TEXT, idx_bd_events_journal_ts is missing, and
// idx_wisps_defer_until (0017's actual content upstream) never got created.
// 0023 repairs all three, each behind its own probe, and does nothing at all on
// a healthy workspace.
//
// Every fixture seeds the main schema at journalRepairSeedVersion rather than
// at latest. That is not cosmetic: seedMainSchemaAt's DOLT_ADD('-A') baseline
// commit runs before MigrateUp seeds the static dolt_ignore patterns, so a
// baseline taken at or after 0064 lands bd_events_journal in committed history
// (the harness artifact migrate_ignored_plane_shape_test.go documents), and the
// drifted fixture's drop/recreate would then show as a tracked change that
// MigrateUp's ignored-source dirty-table guard refuses. Seeding one version
// short of 0064 keeps the journal clone-local from the moment it is created,
// which is also what a real workspace looks like.
const (
	journalRepairSeedVersion = 63
	migration0023Version     = 23
)

// forkLineageJournalDDL is the journal table as the fork's
// ignored/0017_create_events_journal built it: TEXT payload columns and the
// issue_id index alone. Everything else matches 0022, because everything else
// is what the two lineages agree on.
const forkLineageJournalDDL = `
CREATE TABLE bd_events_journal (
    seq BIGINT NOT NULL PRIMARY KEY,
    ts DATETIME NOT NULL,
    op VARCHAR(32) NOT NULL,
    issue_id VARCHAR(255) NOT NULL,
    issue_json LONGTEXT,
    dep_json TEXT,
    comment_json TEXT,
    INDEX idx_bd_events_journal_issue (issue_id)
);`

// TestEmbeddedIgnoredMigration0023RepairsForkLineageJournalShape is the
// drifted-workspace repro: on a workspace sitting at ignored cursor 22,
// hand-build the fork's journal shape with rows and a seq counter already in
// it and strip idx_wisps_defer_until, then prove the one MigrateUp that applies
// 0023 converges the shape without disturbing a byte of the data.
func TestEmbeddedIgnoredMigration0023RepairsForkLineageJournalShape(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, closeConn := newWorkspaceBeforeMigration0023(t, ctx)
	defer closeConn()

	// The drift. bd_events_journal and wisps are both clone-local and absent
	// from HEAD here, so replacing the table and dropping the index leaves the
	// working set exactly as clean as it was.
	execFrozenGuard(t, ctx, conn, "DROP TABLE IF EXISTS bd_events_journal;"+forkLineageJournalDDL)
	execFrozenGuard(t, ctx, conn, "DROP INDEX idx_wisps_defer_until ON wisps")

	// Data the repair must not touch: three journal rows whose payloads carry
	// quotes, backslashes, NULLs, and a multi-KB blob (bound as parameters, so
	// the column holds the literal Go string), plus a seq counter deliberately
	// set BELOW the journal's own MAX(seq). Only 0022's seeding raises it to
	// the high-water mark, so next_seq staying at 1 is proof that 0022 did not
	// re-run and that 0023 left the counter alone.
	bigPayload := strings.Repeat(`{"k":"v'\"\\","n":0},`, 400) + `{"end":true}`
	seed := []struct {
		seq                             int64
		ts, op                          string
		issueJSON, depJSON, commentJSON any
	}{
		{1, "2026-08-01 00:00:00", "create", `{"id":"bd-t9ovd"}`, `[{"type":"blocks"}]`, nil},
		{2, "2026-08-01 00:00:01", "update", `{"id":"bd-t9ovd","p":0}`, nil, `{"text":"it's \"fine\""}`},
		{3, "2026-08-01 00:00:02", "comment", nil, nil, bigPayload},
	}
	for _, s := range seed {
		if _, err := conn.ExecContext(ctx, `
INSERT INTO bd_events_journal (seq, ts, op, issue_id, issue_json, dep_json, comment_json)
VALUES (?, ?, ?, 'bd-t9ovd', ?, ?, ?)`, s.seq, s.ts, s.op, s.issueJSON, s.depJSON, s.commentJSON); err != nil {
			t.Fatalf("seed journal row %d: %v", s.seq, err)
		}
	}
	execFrozenGuard(t, ctx, conn, "UPDATE bd_events_seq SET next_seq = 1 WHERE id = 0")
	rowsBefore := readJournalRows(t, ctx, conn)
	if len(rowsBefore) != len(seed) {
		t.Fatalf("seeded journal rows = %d, want %d", len(rowsBefore), len(seed))
	}

	// Prove the fixture is really drifted, in the terms that make it matter: a
	// payload past TEXT's 65535-byte ceiling is refused, and in production that
	// refusal rolls back the user mutation whose transaction the journal row
	// shares.
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO bd_events_journal (seq, ts, op, issue_id, comment_json) VALUES (99, '2026-08-01 00:00:03', 'comment', 'bd-t9ovd', ?)",
		strings.Repeat("x", 70000)); err == nil {
		t.Fatal("oversized comment_json insert succeeded on the drifted TEXT column; the fixture is not reproducing the drift this migration repairs")
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("repair MigrateUp: %v", err)
	}
	requireIgnoredCursorAtLatest(t, ctx, conn)

	// Shape converged.
	for _, column := range []string{"dep_json", "comment_json"} {
		if got := journalColumnDataType(t, ctx, conn, column); got != "longtext" {
			t.Errorf("bd_events_journal.%s DATA_TYPE = %q after repair, want %q", column, got, "longtext")
		}
	}
	requireIndex(t, ctx, conn, "bd_events_journal", "idx_bd_events_journal_ts", 1)
	requireIndex(t, ctx, conn, "bd_events_journal", "idx_bd_events_journal_issue", 1)
	requireIndex(t, ctx, conn, "wisps", "idx_wisps_defer_until", 1)

	// Data survived byte for byte, and the counter was not touched.
	assertJournalRowsEqual(t, rowsBefore, readJournalRows(t, ctx, conn))
	if got := scalarInt(t, ctx, conn, "SELECT next_seq FROM bd_events_seq WHERE id = 0"); got != 1 {
		t.Errorf("bd_events_seq.next_seq = %d after repair, want 1 (0023 must touch neither the counter nor any data)", got)
	}

	// The repair is what makes the write that used to roll back a user's
	// mutation land instead.
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO bd_events_journal (seq, ts, op, issue_id, comment_json) VALUES (99, '2026-08-01 00:00:03', 'comment', 'bd-t9ovd', ?)",
		strings.Repeat("x", 70000)); err != nil {
		t.Fatalf("oversized comment_json insert still refused after repair: %v", err)
	}
	if got := scalarInt(t, ctx, conn, "SELECT LENGTH(comment_json) FROM bd_events_journal WHERE seq = 99"); got != 70000 {
		t.Errorf("stored oversized comment_json length = %d, want 70000", got)
	}
	execFrozenGuard(t, ctx, conn, "DELETE FROM bd_events_journal WHERE seq = 99")

	// Crash-replay: the pass can be killed and re-run, so the frozen bytes must
	// be a clean no-op against the state they just produced.
	createBefore := showCreateTable(t, ctx, conn, "bd_events_journal")
	execFrozenGuard(t, ctx, conn, ignoredMigration0023SQL(t))
	if createAfter := showCreateTable(t, ctx, conn, "bd_events_journal"); createAfter != createBefore {
		t.Errorf("replaying 0023 changed bd_events_journal:\nbefore:\n%s\nafter:\n%s", createBefore, createAfter)
	}
	assertJournalRowsEqual(t, rowsBefore, readJournalRows(t, ctx, conn))
}

// TestEmbeddedIgnoredMigration0023NoopsOnHealthyWorkspace is the insurance
// half of the contract, and the one that decides whether shipping this
// migration is free: on a workspace that never met the fork lineage, 0023's
// first run must change nothing. SHOW CREATE TABLE before and after is the
// whole assertion — it carries the column types, the index set, and their
// order, so any DDL that fired would show up in it.
func TestEmbeddedIgnoredMigration0023NoopsOnHealthyWorkspace(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, closeConn := newWorkspaceBeforeMigration0023(t, ctx)
	defer closeConn()

	tables := []string{"bd_events_journal", "bd_events_seq", "wisps"}
	before := make(map[string]string, len(tables))
	for _, table := range tables {
		before[table] = showCreateTable(t, ctx, conn, table)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp on healthy workspace: %v", err)
	}
	requireIgnoredCursorAtLatest(t, ctx, conn)

	for _, table := range tables {
		if after := showCreateTable(t, ctx, conn, table); after != before[table] {
			t.Errorf("0023 changed %s on a healthy workspace:\nbefore:\n%s\nafter:\n%s", table, before[table], after)
		}
	}
}

// TestEmbeddedIgnoredMigration0023NoopsWithoutItsTables covers the third
// intermediate state 0023 has to survive: neither table present. The column
// probes reach it through an empty INFORMATION_SCHEMA result (NULL = 1 is not
// true, so IF takes its no-op branch) and the index probes through their
// explicit TABLES conjunct — two different mechanisms, one contract.
//
// It runs the frozen bytes directly rather than through MigrateUp: dropping
// wisps contradicts the ignored cursor's sentinel list, which would correctly
// re-run the whole series and rebuild both tables before 0023 ever saw them.
func TestEmbeddedIgnoredMigration0023NoopsWithoutItsTables(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, closeConn := newWorkspaceBeforeMigration0023(t, ctx)
	defer closeConn()

	// Children before the FK parent, as migrate_ignored_plane_shape_test.go's
	// clone door does.
	execFrozenGuard(t, ctx, conn, `
DROP TABLE IF EXISTS bd_events_journal;
DROP TABLE IF EXISTS wisp_child_counters;
DROP TABLE IF EXISTS wisp_comments;
DROP TABLE IF EXISTS wisp_events;
DROP TABLE IF EXISTS wisp_labels;
DROP TABLE IF EXISTS wisp_dependencies;
DROP TABLE IF EXISTS wisps;
`)
	execFrozenGuard(t, ctx, conn, ignoredMigration0023SQL(t))

	for _, table := range []string{"bd_events_journal", "wisps"} {
		var got int
		if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table).Scan(&got); err != nil {
			t.Fatalf("read %s presence: %v", table, err)
		}
		if got != 0 {
			t.Errorf("0023 created %s out of nothing (count = %d); it must only ever repair an existing table", table, got)
		}
	}
}

// --- helpers ---

// errStopBefore0023 aborts the baseline pass at the exact boundary these tests
// need to start from.
var errStopBefore0023 = errors.New("test: stop the ignored pass just before migration 0023")

// newWorkspaceBeforeMigration0023 returns a workspace with every table in its
// canonical shape, the main cursor at latest, and the ignored cursor at 22 —
// the state every workspace in the field is in the instant before 0023 runs for
// the first time.
//
// Stopping there matters for the no-op proof specifically. Letting the baseline
// MigrateUp run to completion would apply 0023 before the test could snapshot
// anything, so the "before" picture would already contain whatever 0023 did and
// the second run would trivially match it — a mutation that fires wrongly on
// healthy workspaces would slip through unseen (observed: it did).
//
// The per-step fault hook is the seam that stops the pass. It sees only a
// version number and cannot tell the two migration sources apart, so the main
// cursor disambiguates: the main pass runs first and finishes at latest, so a
// main cursor already at latest means this is the ignored pass. The abort
// leaves the working set exactly as a killed pass would (the #4566 contract),
// which is a state MigrateUp is required to resume from anyway.
func newWorkspaceBeforeMigration0023(t *testing.T, ctx context.Context) (*sql.Conn, func()) {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, journalRepairSeedVersion)

	restore := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, db schema.DBConn, version int) error {
		if version != migration0023Version-1 {
			return nil
		}
		if v, err := schema.CurrentVersion(ctx, db); err != nil || v != schema.LatestVersion() {
			return nil
		}
		return errStopBefore0023
	})
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	_, err := schema.MigrateUp(ctx, conn)
	restore()
	if !errors.Is(err, errStopBefore0023) {
		closeConn()
		t.Fatalf("baseline MigrateUp = %v, want the injected stop before migration 0023", err)
	}

	want := migration0023Version - 1
	if got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != want {
		closeConn()
		t.Fatalf("baseline ignored cursor = %d, want %d", got, want)
	}
	return conn, closeConn
}

func ignoredMigration0023SQL(t *testing.T) string {
	t.Helper()
	sqlText, err := schema.IgnoredMigrationSQL("0023_repair_events_journal_shape.up.sql")
	if err != nil {
		t.Fatalf("read ignored 0023 migration: %v", err)
	}
	return sqlText
}

func requireIgnoredCursorAtLatest(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations")
	if want := schema.LatestIgnoredVersion(); got != want {
		t.Fatalf("ignored cursor = %d, want latest %d", got, want)
	}
}

func journalColumnDataType(t *testing.T, ctx context.Context, conn *sql.Conn, column string) string {
	t.Helper()
	var got string
	if err := conn.QueryRowContext(ctx, `
SELECT DATA_TYPE FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal' AND COLUMN_NAME = ?`, column).Scan(&got); err != nil {
		t.Fatalf("read bd_events_journal.%s data type: %v", column, err)
	}
	return got
}

func requireIndex(t *testing.T, ctx context.Context, conn *sql.Conn, table, index string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`, table, index).Scan(&got); err != nil {
		t.Fatalf("read %s index %s: %v", table, index, err)
	}
	if got != want {
		t.Errorf("%s index %s count = %d, want %d", table, index, got, want)
	}
}

func showCreateTable(t *testing.T, ctx context.Context, conn *sql.Conn, table string) string {
	t.Helper()
	var name, ddl string
	//nolint:gosec // G201: table is a test-local literal.
	if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE `"+table+"`").Scan(&name, &ddl); err != nil {
		t.Fatalf("SHOW CREATE TABLE %s: %v", table, err)
	}
	return ddl
}

type journalRow struct {
	seq                             int64
	ts, op, issueID                 string
	issueJSON, depJSON, commentJSON sql.NullString
}

func readJournalRows(t *testing.T, ctx context.Context, conn *sql.Conn) []journalRow {
	t.Helper()
	rows, err := conn.QueryContext(ctx, `
SELECT seq, ts, op, issue_id, issue_json, dep_json, comment_json
FROM bd_events_journal ORDER BY seq`)
	if err != nil {
		t.Fatalf("read journal rows: %v", err)
	}
	defer rows.Close()
	var out []journalRow
	for rows.Next() {
		var r journalRow
		if err := rows.Scan(&r.seq, &r.ts, &r.op, &r.issueID, &r.issueJSON, &r.depJSON, &r.commentJSON); err != nil {
			t.Fatalf("scan journal row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate journal rows: %v", err)
	}
	return out
}

func assertJournalRowsEqual(t *testing.T, before, after []journalRow) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("journal row count = %d, want %d (0023 must not touch data)", len(after), len(before))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("journal row seq %d changed: before %+v, after %+v", before[i].seq, before[i], after[i])
		}
	}
}
