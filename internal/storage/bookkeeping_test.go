package storage

import "testing"

// TestIsBookkeepingTable pins the exact bookkeeping set shared by the
// uncommitted-changes presentations (`bd doctor`'s checks and `bd vc
// status`): the five named tables plus the wisp_ prefix, nothing else.
// A table drifting in or out of this set changes what "uncommitted changes"
// means in two commands at once, so near-misses are pinned too.
func TestIsBookkeepingTable(t *testing.T) {
	bookkeeping := []string{
		"wisps", "leases", "local_metadata", "repo_mtimes", "events",
		"wisp_", "wisp_tmp", "wisp_abc123",
	}
	for _, table := range bookkeeping {
		if !IsBookkeepingTable(table) {
			t.Errorf("IsBookkeepingTable(%q) = false, want true", table)
		}
	}

	notBookkeeping := []string{
		"", "issues", "labels", "comments", "dependencies", "config",
		"wisp", "wispless", "WISPS", "Wisps", "events2", "event",
		"local_metadata_x", "x_local_metadata", "myrepo_mtimes", "lease",
	}
	for _, table := range notBookkeeping {
		if IsBookkeepingTable(table) {
			t.Errorf("IsBookkeepingTable(%q) = true, want false", table)
		}
	}
}
