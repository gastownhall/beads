package main

import (
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func metaIssue(t *testing.T, id string, meta string) *types.Issue {
	t.Helper()
	issue := &types.Issue{ID: id, Title: "Build and sanity check", Status: types.StatusOpen}
	if meta != "" {
		issue.Metadata = json.RawMessage(meta)
	}
	return issue
}

func TestHasOrchestratorMetadata(t *testing.T) {
	tests := []struct {
		name string
		meta string
		want bool
	}{
		{"empty metadata", "", false},
		{"migration only", `{"migration":{"survivor_id":"x"}}`, false},
		{"gc kind", `{"gc.kind":"ralph"}`, true},
		{"gc root bead", `{"gc.root_bead_id":"gct-mk05t","gc.step_id":"x"}`, true},
		{"mixed", `{"migration":{},"gc.logical_bead_id":"y"}`, true},
		{"non-gc namespaced", `{"gct.other":1,"gcc":2,"agc":3}`, false},
		{"invalid json", `{not json`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := metaIssue(t, "bd-test", tt.meta)
			if got := hasOrchestratorMetadata(issue); got != tt.want {
				t.Errorf("hasOrchestratorMetadata(%s) = %v, want %v", tt.meta, got, tt.want)
			}
		})
	}
}

func TestPartitionWorkflowIssues(t *testing.T) {
	issues := []*types.Issue{
		metaIssue(t, "bd-1", ""),
		metaIssue(t, "bd-2", `{"gc.kind":"spec"}`),
		metaIssue(t, "bd-3", `{"migration":{"x":1}}`),
		metaIssue(t, "bd-4", `{"gc.step_id":"s"}`),
	}
	normal, workflowCount := partitionWorkflowIssues(issues)
	if workflowCount != 2 {
		t.Errorf("workflowCount = %d, want 2", workflowCount)
	}
	if len(normal) != 2 {
		t.Fatalf("len(normal) = %d, want 2", len(normal))
	}
	for _, issue := range normal {
		if hasOrchestratorMetadata(issue) {
			t.Errorf("normal partition contains workflow bead %s", issue.ID)
		}
	}
}

func TestFindDuplicateGroupsSkipsNothingByItself(t *testing.T) {
	// findDuplicateGroups itself is content-based; the command layer is
	// responsible for partitioning workflow beads out. This test pins the
	// contract used by the command wiring: partition first, then group.
	issues := []*types.Issue{
		metaIssue(t, "bd-1", ""),
		metaIssue(t, "bd-2", `{"gc.kind":"spec"}`),
		metaIssue(t, "bd-3", ""),
	}
	normal, _ := partitionWorkflowIssues(issues)
	groups := findDuplicateGroups(normal)
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1 (bd-1 + bd-3 only)", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("group size = %d, want 2", len(groups[0]))
	}
}
