package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestBlockedStatusDescriptionNamesTheDistinction pins the bd statuses
// wording for 'blocked' (GH#6565): the status is an explicit marker bd
// close does not clear, distinct from the dependency graph's derived
// is_blocked flag — not "blocked by a dependency", which reads as if the
// graph owned the status.
func TestBlockedStatusDescriptionNamesTheDistinction(t *testing.T) {
	var desc string
	for _, s := range builtInStatuses {
		if s.Status == types.StatusBlocked {
			desc = s.Description
		}
	}
	if desc == "" {
		t.Fatal("no blocked entry in builtInStatuses")
	}
	for _, want := range []string{"explicit", "close"} {
		if !strings.Contains(desc, want) {
			t.Errorf("blocked description %q must name %q", desc, want)
		}
	}
}
