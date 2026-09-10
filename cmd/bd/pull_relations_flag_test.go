package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/tracker"
)

// TestRelationsFlagRegistered verifies that relation-derived dependency import
// is opt-in on every tracker command that can create it. A relation edge is
// typed 'blocks', which marks the dependent is_blocked and drops it out of
// `bd ready`, so pulling one must never be the default.
func TestRelationsFlagRegistered(t *testing.T) {
	// Not parallel: accesses shared cobra command tree.

	cmds := []struct {
		name string
		cmd  *cobra.Command
	}{
		{"jira sync", jiraSyncCmd},
		{"jira pull", jiraPullCmd},
		{"linear sync", linearSyncCmd},
		{"linear pull", linearPullCmd},
	}

	for _, tc := range cmds {
		t.Run(tc.name, func(t *testing.T) {
			flag := tc.cmd.Flags().Lookup("relations")
			if flag == nil {
				t.Fatalf("%s command missing --relations flag", tc.name)
			}
			if flag.DefValue != "false" {
				t.Errorf("%s --relations default = %q, want false", tc.name, flag.DefValue)
			}
		})
	}
}

// TestPullDependencySourcesGatesRelations pins the gate itself: with the flag
// off, pull is restricted to parent-sourced edges; with it on, the filter is
// empty so the engine imports every source the mapper emitted.
func TestPullDependencySourcesGatesRelations(t *testing.T) {
	t.Parallel()

	off := pullDependencySources(false)
	if len(off) != 1 || off[0] != tracker.DependencySourceParent {
		t.Fatalf("pullDependencySources(false) = %v, want [%s]", off, tracker.DependencySourceParent)
	}
	if on := pullDependencySources(true); on != nil {
		t.Fatalf("pullDependencySources(true) = %v, want nil (no source filter)", on)
	}
}
