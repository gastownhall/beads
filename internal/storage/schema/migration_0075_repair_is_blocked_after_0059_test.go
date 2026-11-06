package schema

import "testing"

// TestLatestVersionIncludesMigration0075 pins the main slot the is_blocked
// repair marker claims (gastownhall/beads#7037), directly above the id
// tombstone tables' (0074), and that the marker carries its repair.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want = 75
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (is_blocked repair marker)", got, want)
	}
	if _, ok := preMigrationRepairs[repairKey{mainSource.cursorTable, want}]; !ok {
		t.Fatalf("main 0075 has no pre-migration repair registered; the marker alone records nothing")
	}
}

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot that
// performs the repair, directly above the events restore's (ignored 0031).
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const want = 32
	if got := LatestIgnoredVersion(); got != want {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (is_blocked repair)", got, want)
	}
	if want != isBlockedRepairIgnoredVersion {
		t.Fatalf("isBlockedRepairIgnoredVersion = %d, want %d", isBlockedRepairIgnoredVersion, want)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, want}]; !ok {
		t.Fatalf("ignored 0032 has no pre-migration repair registered; the marker alone repairs nothing")
	}
}
