package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestHoldBackRetainedTypesKeepsDecayOffRetainedBeads pins `bd gc`'s decay
// partition: a closed bead of a type types.retained names is held back and
// counted; an unset setting holds nothing back.
func TestHoldBackRetainedTypesKeepsDecayOffRetainedBeads(t *testing.T) {
	op := &types.Issue{ID: "bd-op", IssueType: types.IssueType("op")}
	task := &types.Issue{ID: "bd-task", IssueType: types.TypeTask}

	kept, held := holdBackRetainedTypes([]*types.Issue{op, task}, `["op"]`)
	if held != 1 || len(kept) != 1 || kept[0] != task {
		t.Errorf("holdBackRetainedTypes(op retained) = %v, %d; want [bd-task], 1", kept, held)
	}
	kept, held = holdBackRetainedTypes([]*types.Issue{op, task}, "")
	if held != 0 || len(kept) != 2 {
		t.Errorf("holdBackRetainedTypes(unset) = %v, %d; want both kept, 0", kept, held)
	}
	if line := retainedTypesKeptLine(3); !strings.Contains(line, "Kept 3") || !strings.Contains(line, "--retain-ids") {
		t.Errorf("retainedTypesKeptLine(3) = %q, want the count and the flag that deletes them", line)
	}
}

// TestRetentionStatsAppearOnlyWhenSomethingWasKept pins the --json shape of
// prune and purge: retention_required is attached when the sweep held a
// retained bead back and absent otherwise, so a workspace without the setting
// sees its old shape.
func TestRetentionStatsAppearOnlyWhenSomethingWasKept(t *testing.T) {
	stats := map[string]interface{}{}
	addRetentionStats(stats, issueops.SweepResult{})
	if _, ok := stats["retention_required"]; ok {
		t.Errorf("retention_required attached for a sweep that kept nothing: %v", stats)
	}
	result := issueops.SweepResult{Skipped: issueops.SweepSkips{RetentionRequired: 2}}
	addRetentionStats(stats, result)
	if stats["retention_required"] != 2 {
		t.Errorf("retention_required = %v, want 2", stats["retention_required"])
	}
	if note := retentionNote(result); !strings.Contains(note, "2 closed bead(s) of a retained type") || !strings.Contains(note, "--retain-ids") {
		t.Errorf("retentionNote = %q, want the count and the flag that deletes them", note)
	}
}
