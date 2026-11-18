package storage

import "testing"

// TestIsBookkeepingTable pins the bookkeeping set that bd doctor and bd vc
// status both leave out of "uncommitted changes": the exact names wisps,
// leases, local_metadata, repo_mtimes and events, plus any wisp_ table.
func TestIsBookkeepingTable(t *testing.T) {
	for _, tt := range []struct {
		table string
		want  bool
	}{
		{"wisps", true},
		{"wisp_events", true},
		{"wisp_labels", true},
		{"wisp_dependencies", true},
		{"wisp_comments", true},
		{"wisp_", true},
		{"leases", true},
		{"local_metadata", true},
		{"repo_mtimes", true},
		{"events", true},
		{"issues", false},
		{"labels", false},
		{"comments", false},
		{"dependencies", false},
		{"config", false},
		{"metadata", false},
		{"dolt_ignore", false},
		{"wisp", false},
		{"Wisps", false},
		{"events_archive", false},
		{"my_wisps", false},
		{"", false},
	} {
		if got := IsBookkeepingTable(tt.table); got != tt.want {
			t.Errorf("IsBookkeepingTable(%q) = %v, want %v", tt.table, got, tt.want)
		}
	}
}
