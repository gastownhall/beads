package schema

import (
	"strings"
	"testing"
)

// Aggregate row_lock backfill (gastownhall/beads#5738, gc-yfkxj): row_lock
// becomes the revision token of the whole user-authored aggregate (labels,
// comments and dependency edges included), so every durable row must carry a
// usable non-zero token. Legacy rows still holding the 0 column default are
// backfilled to the equality sentinel 1; the next supported mutation remints a
// random token. The clone-local twin for the dolt-ignored wisps table is
// ignored/0029_backfill_wisp_aggregate_row_lock.up.sql.

const migration0073Up = "0073_backfill_aggregate_row_lock.up.sql"
const ignoredMigration0029Up = "0029_backfill_wisp_aggregate_row_lock.up.sql"

// TestLatestVersionIncludesMigration0073 pins the real next free slot this
// backfill claims, superseding 0072's own version of this test
// (migration_0072_add_epoch_minted_addresses_test.go). Deliberately a hardcoded
// literal for the same reason 0067's through 0072's were: LatestVersion()
// drifting to 73 for the wrong reason (an unrelated migration landing first)
// should still be caught by this test failing to explain why 73 is
// aggregate-row_lock-shaped, which the bytes check below does.
//
// This backfill was 0070 at round 1 and 0072 at round 2 of the re-cut; each
// time a sibling migration landed on main first (0070_add_participation_generation,
// 0071_add_removed_restriction, then 0072_add_epoch_minted_addresses), so it
// moved to the next contiguous slot. Two files sharing a version number panic
// checkNoDuplicateVersions at store open, which bricks every bd invocation.
func TestLatestVersionIncludesMigration0073(t *testing.T) {
	const want = 73
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (the migration slot claimed for the aggregate row_lock backfill, #5738)", got, want)
	}
	const wantIgnored = 29
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (the ignored-plane twin of migration 0073)", got, wantIgnored)
	}
}

// TestMigration0073BackfillsAggregateRowLock is the pure-Go, DB-independent
// check of the frozen migration bytes, on both planes. No `dolt` binary is
// required; the behavioral half (legacy rows become guardable, and guard
// invalidation covers label and metadata mutations) lives in
// internal/storage/embeddeddolt's TestMigrateAggregateRevision* set.
//
// Both statements are plain UPDATEs with no PREPARE block, so unlike
// 0067-0071 they need no cliCompatibleMigrationSQL override (0072 is a plain
// CREATE TABLE and carries none either). The
// `updated_at = updated_at` self-assignment is load-bearing: it is the 0059 /
// ignored-0015 discipline that keeps a backfill from restamping closed rows or
// reading as a user edit to staleness consumers.
func TestMigration0073BackfillsAggregateRowLock(t *testing.T) {
	upSQL, err := MigrationSQL(migration0073Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0073Up, err)
	}
	if want := "UPDATE issues SET row_lock = 1, updated_at = updated_at WHERE row_lock = 0"; !strings.Contains(upSQL, want) {
		t.Errorf("0073 up migration missing %q\nfull SQL:\n%s", want, upSQL)
	}
	if strings.Contains(strings.ToUpper(upSQL), "PREPARE") {
		t.Error("0073 up migration is a plain UPDATE and must stay one; a PREPARE block would need the cliCompatibleMigrationSQL override 0067-0071 carry")
	}

	ignoredSQL, err := IgnoredMigrationSQL(ignoredMigration0029Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) error = %v, want the migration file to exist", ignoredMigration0029Up, err)
	}
	if want := "UPDATE wisps SET row_lock = 1, updated_at = updated_at WHERE row_lock = 0"; !strings.Contains(ignoredSQL, want) {
		t.Errorf("ignored/0029 up migration missing %q\nfull SQL:\n%s", want, ignoredSQL)
	}
	if !strings.Contains(ignoredSQL, "synced migration 0073") {
		t.Errorf("ignored/0029 header must name its synced twin, migration 0073\nfull SQL:\n%s", ignoredSQL)
	}
}
