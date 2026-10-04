package schema

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// R20's epoch-transition enforcement (gastownhall/beads#5898 revision 9,
// #6136) adds one brand-new table,
// epoch_minted_addresses -- see
// migrations/0071_add_epoch_minted_addresses.up.sql for the full rationale.
// This migration is a plain, unguarded
// CREATE TABLE IF NOT EXISTS with no ALTER and no PREPARE, so it needs none
// of 0067/0068's CLI-hazard-override tests (already covered, for every
// migration including this one, by TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified
// and TestAllMigrationsSQLUsesDirectDDLForKnownCLIIncompatibilities).

// TestLatestVersionIncludesMigration0071 pins the real next free slot this
// phase claims, superseding 0070's own version of this test (only one such
// pin lives at a time, the same way 0070's superseded 0069's).
//
// This slice originally claimed 0069 too. It was renumbered because
// R7.1's removed_restriction migration -- a PARALLEL SIBLING of this branch,
// not an ancestor -- had also claimed 0069, and the two are now linearized:
// R7.1 goes first (it is published beneath gastownhall/beads#6661), this one
// takes the next contiguous slot. Both then moved up one more slot when
// upstream's 0069_widen_issue_versions_datetime_precision
// (gastownhall/beads#6675) landed first, so R7.1 is 0070 and this is 0071.
// Two files sharing a
// version number panic checkNoDuplicateVersions at store open, before any
// command's RunE, which bricks every bd invocation rather than merely
// reddening a package -- see the same hazard on #6358 vs #6008.
//
// Deliberately a hardcoded literal for the same reason 0069's and 0070's
// were: LatestVersion() drifting to 71 for the wrong reason (an unrelated
// migration landing first) should still be caught by this test failing to
// explain why 71 is epoch-minted-addresses-shaped.
func TestLatestVersionIncludesMigration0071(t *testing.T) {
	const want = 71
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (epoch_minted_addresses migration slot claimed by gastownhall/beads#6664)", got, want)
	}
}

const migration0071Up = "0071_add_epoch_minted_addresses.up.sql"
const migration0071Down = "0071_add_epoch_minted_addresses.down.sql"

// TestMigration0071RecordsSurvivalInTheAddressTable is a pure-Go,
// DB-independent check of the frozen migration bytes: it runs even where no
// `dolt` binary is available.
//
// An address's survival is RECORDED on its own row, not inferred from other
// rows (gastownhall/beads#6664, bee-ghosttrack review 5360880888, Major 1):
// carried_from names the root address of the lineage a token-scheme change
// carried the row from, and gone_at_epoch is the epoch the store lost it in.
// Both are NULL for a fresh mint. The index serves the two lookups that
// follow a lineage (the newest carry of a root, and every carry of a root to
// lose), and the CHECK is the database-level tripwire against a stale or
// rewound epoch being written into a loss marker. It is >=, not >, because a
// carry row is born at the new epoch and can be lost in that same epoch.
//
// The migration is edited IN PLACE and gains no ALTER and no second slot:
// 0071 has never been applied to a store, and an ALTER over an already-applied
// copy of the earlier shape would be a migration slot nothing may claim.
func TestMigration0071RecordsSurvivalInTheAddressTable(t *testing.T) {
	upSQL, err := MigrationSQL(migration0071Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0071Up, err)
	}
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS epoch_minted_addresses",
		"address VARCHAR(255) NOT NULL,",
		"store_id VARCHAR(255) NOT NULL,",
		"minted_id VARCHAR(255) NOT NULL,",
		"minted_epoch INT NOT NULL,",
		"minted_at DATETIME NOT NULL,",
		"carried_from VARCHAR(255) NULL,",
		"gone_at_epoch INT NULL,",
		"PRIMARY KEY (address),",
		"INDEX idx_epoch_minted_addresses_carried_from (carried_from),",
		"CONSTRAINT ck_epoch_minted_addresses_gone_not_before_mint CHECK (gone_at_epoch IS NULL OR gone_at_epoch >= minted_epoch)",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0071 up migration missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}

	// Only the statements count here: the header comments describe the
	// migration in words that would otherwise trip these checks.
	statements := strings.ToUpper(stripSQLLineComments(upSQL))
	if strings.Contains(statements, "ALTER TABLE") {
		t.Error("0071 up migration must not ALTER anything: the survival columns belong to its own CREATE TABLE, because the migration is edited in place while no store has applied it")
	}
	if strings.Contains(statements, "PREPARE") {
		t.Error("0071 up migration must stay plain DDL: CREATE TABLE IF NOT EXISTS needs no guarded PREPARE, and adding one would put it on the pre-2.3 CLI hazard path")
	}
	if got := strings.Count(statements, "CREATE TABLE"); got != 1 {
		t.Errorf("0071 up migration has %d CREATE TABLE statements, want exactly 1", got)
	}

	// Only migrations/*.up.sql is embedded, so the down side reads from disk by
	// package-relative path, as migration 0067's test does.
	downBytes, err := os.ReadFile("migrations/" + migration0071Down)
	if err != nil {
		t.Fatalf("read %s: %v, want the migration file to exist", migration0071Down, err)
	}
	if !strings.Contains(string(downBytes), "DROP TABLE IF EXISTS epoch_minted_addresses") {
		t.Errorf("0071 down migration must drop epoch_minted_addresses, got:\n%s", downBytes)
	}
}

// TestMigration0071RecordsSurvivalInTheAddressTableThroughDoltCLI applies the
// full migration bundle through a real `dolt` binary (skipped without one, see
// testutil.RequireDoltBinary) and checks what a pure-Go SQL-text check cannot:
// the column types and nullability Dolt reports, the index it actually built,
// and that the CHECK is enforced on both INSERT and UPDATE, with the boundary
// it is meant to have.
func TestMigration0071RecordsSurvivalInTheAddressTableThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := t.TempDir()
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	requireDoltColumnShape(t, dir, "epoch_minted_addresses", "carried_from", "varchar(255)", "YES")
	requireDoltDataType(t, dir, "epoch_minted_addresses", "gone_at_epoch", "int", "YES")
	// The five columns the mint writes keep their shape, so the mint's
	// five-column INSERT is unchanged.
	requireDoltColumnShape(t, dir, "epoch_minted_addresses", "address", "varchar(255)", "NO")
	requireDoltColumnShape(t, dir, "epoch_minted_addresses", "minted_epoch", "int", "NO")
	requireDoltNoRows(t, dir, "SELECT address FROM epoch_minted_addresses", "epoch_minted_addresses")
	requireDoltCount(t, dir,
		`SELECT COUNT(*) AS c FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'epoch_minted_addresses' AND INDEX_NAME = 'idx_epoch_minted_addresses_carried_from' AND COLUMN_NAME = 'carried_from'`,
		"1")

	// A fresh mint writes the five original columns and is a root row: both
	// survival columns come out NULL with no change to the INSERT.
	runDoltSQL(t, dir, `INSERT INTO epoch_minted_addresses (address, store_id, minted_id, minted_epoch, minted_at) VALUES ('root-a', 'store', 'record-a', 3, '2026-09-01 00:00:00')`)
	requireDoltCount(t, dir, `SELECT COUNT(*) AS c FROM epoch_minted_addresses WHERE address = 'root-a' AND carried_from IS NULL AND gone_at_epoch IS NULL`, "1")

	// A carry row names the lineage root.
	runDoltSQL(t, dir, `INSERT INTO epoch_minted_addresses (address, store_id, minted_id, minted_epoch, minted_at, carried_from) VALUES ('carry-a', 'store', 'record-a', 5, '2026-09-01 00:00:00', 'root-a')`)
	requireDoltCount(t, dir, `SELECT COUNT(*) AS c FROM epoch_minted_addresses WHERE carried_from = 'root-a'`, "1")

	// The CHECK: an epoch lost in may not precede the epoch minted in.
	if err := runDoltSQLExpectingError(t, dir, `UPDATE epoch_minted_addresses SET gone_at_epoch = 2 WHERE address = 'root-a'`); err == nil {
		t.Error("UPDATE gone_at_epoch below minted_epoch succeeded, want the CHECK constraint to reject it")
	}
	if err := runDoltSQLExpectingError(t, dir, `INSERT INTO epoch_minted_addresses (address, store_id, minted_id, minted_epoch, minted_at, gone_at_epoch) VALUES ('bad-b', 'store', 'record-b', 4, '2026-09-01 00:00:00', 3)`); err == nil {
		t.Error("INSERT with gone_at_epoch below minted_epoch succeeded, want the CHECK constraint to reject it")
	}
	// Equal is allowed: a carry row is born at the new epoch and can be lost in
	// that same epoch.
	runDoltSQL(t, dir, `UPDATE epoch_minted_addresses SET gone_at_epoch = 3 WHERE address = 'root-a'`)
	requireDoltCount(t, dir, `SELECT COUNT(*) AS c FROM epoch_minted_addresses WHERE address = 'root-a' AND gone_at_epoch = 3`, "1")
	// A later epoch is allowed too.
	runDoltSQL(t, dir, `UPDATE epoch_minted_addresses SET gone_at_epoch = 9 WHERE address = 'carry-a'`)
	requireDoltCount(t, dir, `SELECT COUNT(*) AS c FROM epoch_minted_addresses WHERE address = 'carry-a' AND gone_at_epoch = 9`, "1")

	// The address stays the primary key.
	if err := runDoltSQLExpectingError(t, dir, `INSERT INTO epoch_minted_addresses (address, store_id, minted_id, minted_epoch, minted_at) VALUES ('root-a', 'store', 'record-a', 3, '2026-09-01 00:00:00')`); err == nil {
		t.Error("duplicate address insert succeeded, want primary key violation")
	}
}

// stripSQLLineComments drops "--" comment lines so a check over a migration's
// statements is not tripped by the prose in its header.
func stripSQLLineComments(sqlText string) string {
	var kept []string
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
