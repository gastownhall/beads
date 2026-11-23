package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// Migration 0075 (+ ignored/0032) adds closed_by, the durable close-actor
// column (types.Issue.ClosedBy, GH#8617): the close-side twin of created_by,
// stamped from the write's actor by the close-lifecycle writers and cleared
// on reopen.

const (
	migration0075Up        = "0075_add_closed_by.up.sql"
	migration0075Down      = "0075_add_closed_by.down.sql"
	ignoredMigration0032Up = "0032_add_wisps_closed_by.up.sql"
)

// TestLatestVersionIncludesMigration0075 pins the main slot this migration
// claims, 75, superseding TestLatestVersionIncludesMigration0074
// (migration_0074_create_id_tombstones_test.go) now that 0075 claims the next
// free slot -- only one such pin lives at a time, matching how that test
// itself already superseded 0073's own version. Its ignored twin, 0032, is
// likewise the new top of the ignored series: that pin moved here from
// TestLatestIgnoredVersionIncludesMigration0031.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want = 75
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (the closed_by column)", got, want)
	}
}

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot the
// wisps half of closed_by claims, superseding
// TestLatestIgnoredVersionIncludesMigration0031's version pin (its repair
// assertions live on in TestMigration0031RepairRegistered).
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const wantIgnored = 32
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (wisps.closed_by)", got, wantIgnored)
	}
}

// TestMigration0075AddsClosedByToBothPlanes checks the frozen bytes and the
// CLI-bundle substitute directly, no dolt binary required. The shape rules
// it pins are the ones the embedded replay test exercises against a real
// engine.
func TestMigration0075AddsClosedByToBothPlanes(t *testing.T) {
	upSQL, err := MigrationSQL(migration0075Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0075Up, err)
	}
	for _, want := range []string{
		"'ALTER TABLE issues ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''",
		"'ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''",
		"COLUMN_NAME = 'closed_by'",
		// The wisps arm is guarded on the TABLE existing, not only on the
		// column: older workspaces created issues-only (0054/0073 precedent).
		"INFORMATION_SCHEMA.TABLES",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0075 up migration missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0075 must keep its guarded PREPARE blocks: they make a raw .up.sql replay onto an already-migrated store a no-op")
	}
	// The column matches created_by's shape exactly: VARCHAR(255) with the
	// empty default, so no existing row's cell is rewritten and a row closed
	// before this column reads '' rather than NULL.
	if !strings.Contains(upSQL, "closed_by VARCHAR(255) DEFAULT ''''") {
		t.Error("0075 must declare closed_by VARCHAR(255) DEFAULT '' (the created_by shape)")
	}
	// No backfill, no bead row touched: the actor of a historical close lives
	// only in the clone-local journal, and a migration that wrote it would
	// fork every clone (see the migration's header).
	for _, forbidden := range []string{"UPDATE issues", "UPDATE wisps", "INSERT INTO issues", "INSERT INTO wisps", "DELETE FROM"} {
		if strings.Contains(strings.ToUpper(upSQL), forbidden) {
			t.Errorf("0075 must not write bead rows (%q found): migrations never touch a bead", forbidden)
		}
	}

	bundle := cliCompatibleMigrationSQL(migration0075Up, upSQL)
	for _, want := range []string{
		"ALTER TABLE issues ADD COLUMN closed_by VARCHAR(255) DEFAULT '';",
		"ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT '';",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("0075's CLI bundle substitute missing direct DDL %q: a pre-2.3 CLI no-ops the prepared ALTER", want)
		}
	}
	if !cliSubstituteAssumesWispTables(migration0075Up) {
		t.Error("0075's CLI substitute ALTERs wisps unguarded, so it must be listed in cliSubstituteAssumesWispTables")
	}

	downBytes, err := os.ReadFile("migrations/" + migration0075Down)
	if err != nil {
		t.Fatalf("read %s: %v", migration0075Down, err)
	}
	downSQL := string(downBytes)
	for _, want := range []string{
		"'ALTER TABLE issues DROP COLUMN closed_by'",
		"'ALTER TABLE wisps DROP COLUMN closed_by'",
	} {
		if !strings.Contains(downSQL, want) {
			t.Errorf("0075 down migration missing %q\nfull SQL:\n%s", want, downSQL)
		}
	}

	// The ignored twin mirrors the guarded definition exactly, or a fresh
	// clone's wisps table diverges from an upgraded one's.
	ignoredSQL, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) error = %v", ignoredMigration0032Up, err)
	}
	for _, want := range []string{
		"'ALTER TABLE wisps ADD COLUMN closed_by VARCHAR(255) DEFAULT ''''",
		"COLUMN_NAME = 'closed_by'",
	} {
		if !strings.Contains(ignoredSQL, want) {
			t.Errorf("ignored/0032 missing %q\nfull SQL:\n%s", want, ignoredSQL)
		}
	}
	// The twin must stay a no-op on an in-place-upgraded workspace, where
	// synced 0075 already added the column: the guard, not the cursor, is
	// what makes it idempotent.
	if !strings.Contains(ignoredSQL, "INFORMATION_SCHEMA.TABLES") {
		t.Error("ignored/0032 must guard on the wisps table existing (0013/0029 precedent)")
	}
}

// migrationsThrough builds the bundle AllMigrationsSQL() produces, cut at
// maxVersion: bootstrap plus every migration file up to and including it, so a
// test can stand up a store at an older schema and drive a later migration by
// hand.
func migrationsThrough(t *testing.T, maxVersion int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(mainSource.bootstrapSQL())
	b.WriteString(";\n")
	for _, f := range mainSource.list() {
		if f.version > maxVersion {
			continue
		}
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
		if err != nil {
			t.Fatalf("read migration %s: %v", f.name, err)
		}
		b.WriteString(cliCompatibleMigrationSQL(f.name, string(data)))
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "\nINSERT IGNORE INTO %s (version, content_hash) VALUES (%d, '%s');\n",
			mainSource.cursorTable, f.version, hex.EncodeToString(sum[:]))
	}
	return b.String()
}

// TestMigration0075PreservesExistingRowsAndReplaysNoOp drives the real dolt
// engine through the 0074 -> 0075 upgrade (GH#8617): a store at 0074 with rows
// on both planes gains the closed_by column on both planes, every pre-existing
// row keeps its stored cells byte for byte (content_hash and updated_at
// included -- ADD COLUMN rewrites no row), reads closed_by as the column's
// empty-string default rather than NULL, and applying the same guarded SQL a
// second time is a no-op: the probes find the column, the PREPARE blocks skip,
// the cursor insert is INSERT IGNORE, and nothing about the rows changes.
func TestMigration0075PreservesExistingRowsAndReplaysNoOp(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "m0075")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create store dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, migrationsThrough(t, 74))

	issueHash := strings.Repeat("5a", 32)
	wispHash := strings.Repeat("6b", 32)
	runDoltSQL(t, dir, fmt.Sprintf(`
		INSERT INTO issues (id, content_hash, title, description, design, acceptance_criteria, notes, updated_at)
			VALUES ('m75-i1', '%s', 'pre-upgrade issue', 'd', 'de', 'ac', 'n', '2001-02-03 04:05:06');
		INSERT INTO wisps (id, content_hash, title, updated_at)
			VALUES ('m75-w1', '%s', 'pre-upgrade wisp', '2001-02-03 04:05:07');
	`, issueHash, wispHash))

	closedByColumns := func(table string) string {
		rows := queryDoltCSV(t, dir, fmt.Sprintf(
			"SELECT COUNT(*) AS n FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' AND COLUMN_NAME = 'closed_by'", table))
		if len(rows) != 1 {
			t.Fatalf("count closed_by columns on %s: got %d rows", table, len(rows))
		}
		return rows[0]["n"]
	}

	// Pre-upgrade: neither plane has the column yet.
	for _, table := range []string{"issues", "wisps"} {
		if got := closedByColumns(table); got != "0" {
			t.Fatalf("store at 0074 already has %s.closed_by (%s columns)", table, got)
		}
	}

	// The identity the upgrade must not touch.
	identity := func(table, id string) string {
		rows := queryDoltCSV(t, dir, fmt.Sprintf(
			"SELECT CONCAT(content_hash, '|', updated_at, '|', title) AS ident FROM %s WHERE id = '%s'", table, id))
		if len(rows) != 1 {
			t.Fatalf("read %s %s: got %d rows", table, id, len(rows))
		}
		return rows[0]["ident"]
	}
	issueIdentity := identity("issues", "m75-i1")
	wispIdentity := identity("wisps", "m75-w1")

	upSQL, err := MigrationSQL(migration0075Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s): %v", migration0075Up, err)
	}
	upBytes, err := os.ReadFile("migrations/" + migration0075Up)
	if err != nil {
		t.Fatalf("read %s: %v", migration0075Up, err)
	}
	sum := sha256.Sum256(upBytes)
	upgrade := upSQL + fmt.Sprintf("\nINSERT IGNORE INTO %s (version, content_hash) VALUES (75, '%s');\n",
		mainSource.cursorTable, hex.EncodeToString(sum[:]))
	runDoltSQL(t, dir, upgrade)

	// Both planes carry the column, in the created_by shape.
	for _, table := range []string{"issues", "wisps"} {
		if got := closedByColumns(table); got != "1" {
			t.Errorf("%s.closed_by column count = %s, want 1 after the 0075 upgrade", table, got)
		}
		rows := queryDoltCSV(t, dir, fmt.Sprintf(
			"SELECT DATA_TYPE AS dt, CHARACTER_MAXIMUM_LENGTH AS len, IF(COLUMN_DEFAULT IS NULL, '<null>', COLUMN_DEFAULT) AS dflt FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' AND COLUMN_NAME = 'closed_by'", table))
		if len(rows) != 1 || rows[0]["dt"] != "varchar" || rows[0]["len"] != "255" || rows[0]["dflt"] != "" {
			t.Errorf("%s.closed_by shape = %#v, want varchar(255) with the '' default", table, rows)
		}
	}

	// The pre-existing rows are untouched, and closed_by reads as the empty
	// default (not NULL).
	if got := identity("issues", "m75-i1"); got != issueIdentity {
		t.Errorf("issues m75-i1 identity changed by the upgrade: %q, want %q", got, issueIdentity)
	}
	if got := identity("wisps", "m75-w1"); got != wispIdentity {
		t.Errorf("wisps m75-w1 identity changed by the upgrade: %q, want %q", got, wispIdentity)
	}
	for _, probe := range []struct{ table, id string }{
		{"issues", "m75-i1"}, {"wisps", "m75-w1"},
	} {
		rows := queryDoltCSV(t, dir, fmt.Sprintf(
			"SELECT IF(closed_by IS NULL, '<null>', CONCAT('<', closed_by, '>')) AS tag FROM %s WHERE id = '%s'", probe.table, probe.id))
		if len(rows) != 1 || rows[0]["tag"] != "<>" {
			t.Errorf("%s %s closed_by after upgrade = %#v, want the empty default <> (not NULL)", probe.table, probe.id, rows)
		}
	}

	// Replay the same SQL: a no-op on every observable.
	runDoltSQL(t, dir, upgrade)
	for _, table := range []string{"issues", "wisps"} {
		if got := closedByColumns(table); got != "1" {
			t.Errorf("%s.closed_by column count = %s after replay, want 1", table, got)
		}
	}
	if got := identity("issues", "m75-i1"); got != issueIdentity {
		t.Errorf("issues m75-i1 identity changed by the replay: %q, want %q", got, issueIdentity)
	}
	if got := identity("wisps", "m75-w1"); got != wispIdentity {
		t.Errorf("wisps m75-w1 identity changed by the replay: %q, want %q", got, wispIdentity)
	}
}
