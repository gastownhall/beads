package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestUpdateAuditChanges(t *testing.T) {
	t.Parallel()
	open := &types.Issue{ID: "bd-1", Status: types.StatusOpen, Priority: 2}
	claimedBy := func(status types.Status, assignee string) *types.Issue {
		return &types.Issue{ID: "bd-1", Status: status, Assignee: assignee, Priority: 2}
	}
	var statusPatch, assigneePatch, priorityPatch issueops.IssuePatch
	statusPatch.Status = setField(types.StatusBlocked)
	assigneePatch.Assignee = setField("bob")
	priorityPatch.Priority = setField(0)

	tests := []struct {
		name    string
		patch   issueops.IssuePatch
		claim   bool
		changed bool
		before  *types.Issue
		after   *types.Issue
		want    []updateAuditChange
	}{
		{
			name:    "explicit fields only, as before",
			patch:   issueops.IssuePatch{Status: statusPatch.Status, Assignee: assigneePatch.Assignee, Priority: priorityPatch.Priority},
			changed: true,
			before:  open,
			after:   claimedBy(types.StatusBlocked, "bob"),
			want: []updateAuditChange{
				{"status", "open", "blocked"},
				{"assignee", "", "bob"},
				{"priority", "2", "0"},
			},
		},
		{
			name:    "explicit fields journal even when nothing changed",
			patch:   statusPatch,
			changed: false,
			before:  open,
			after:   open,
			want:    []updateAuditChange{{"status", "open", "blocked"}},
		},
		{
			name:    "plain claim records the pre-read against the post-state",
			claim:   true,
			changed: true,
			before:  open,
			after:   claimedBy(types.StatusInProgress, "alice"),
			want: []updateAuditChange{
				{"status", "open", "in_progress"},
				{"assignee", "", "alice"},
			},
		},
		{
			name:    "claim plus explicit status: the explicit value wins, assignee from the claim",
			patch:   statusPatch,
			claim:   true,
			changed: true,
			before:  open,
			after:   claimedBy(types.StatusBlocked, "alice"),
			want: []updateAuditChange{
				{"status", "open", "blocked"},
				{"assignee", "", "alice"},
			},
		},
		{
			name:    "claim plus explicit assignee: one assignee row, the explicit one",
			patch:   assigneePatch,
			claim:   true,
			changed: true,
			before:  open,
			after:   claimedBy(types.StatusInProgress, "bob"),
			want: []updateAuditChange{
				{"status", "open", "in_progress"},
				{"assignee", "", "bob"},
			},
		},
		{
			name:    "claim plus priority: priority stays explicit-only",
			patch:   priorityPatch,
			claim:   true,
			changed: true,
			before:  open,
			after:   &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "alice", Priority: 0},
			want: []updateAuditChange{
				{"status", "open", "in_progress"},
				{"assignee", "", "alice"},
				{"priority", "2", "0"},
			},
		},
		{
			name:    "pool-assigned custom-active pre-read is recorded as read, not assumed open",
			claim:   true,
			changed: true,
			before:  &types.Issue{ID: "bd-1", Status: "triaging", Assignee: "pool:triage", Priority: 2},
			after:   claimedBy(types.StatusInProgress, "alice"),
			want: []updateAuditChange{
				{"status", "triaging", "in_progress"},
				{"assignee", "pool:triage", "alice"},
			},
		},
		{
			name:    "a claim that changed nothing journals nothing, even over a stale pre-read",
			claim:   true,
			changed: false,
			before:  open,
			after:   claimedBy(types.StatusInProgress, "alice"),
			want:    nil,
		},
		{
			name:    "missing snapshots invent no claim rows",
			claim:   true,
			changed: true,
			before:  nil,
			after:   claimedBy(types.StatusInProgress, "alice"),
			want:    nil,
		},
		{
			name:    "missing post-state invents no claim rows",
			claim:   true,
			changed: true,
			before:  open,
			after:   nil,
			want:    nil,
		},
		{
			// The assignee comes from the returned row, never from the actor
			// spelling: an equivalent reclaim that kept "Alice" stays "Alice"
			// and is dropped by LogFieldChange as an equal value.
			name:    "equivalent-actor reclaim keeps the returned assignee",
			claim:   true,
			changed: true,
			before:  claimedBy(types.StatusInProgress, "Alice"),
			after:   claimedBy(types.StatusInProgress, "Alice"),
			want: []updateAuditChange{
				{"status", "in_progress", "in_progress"},
				{"assignee", "Alice", "Alice"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := updateAuditChanges(tc.patch, tc.claim, tc.changed, tc.before, tc.after)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("updateAuditChanges = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestLogUpdateAuditChangesSuppressesEqualValues writes an explicit status
// patch that matches the pre-read through the real sidecar: the row
// updateAuditChanges selects is dropped by audit.LogFieldChange, and only a
// later real change reaches the enabled sidecar.
func TestLogUpdateAuditChangesSuppressesEqualValues(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// beads.FindBeadsDir() validates that the directory holds project
	// files; metadata.json satisfies it, as internal/audit's own tests do.
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_AUDIT_ENABLED", "1")

	before := &types.Issue{ID: "bd-1", Status: types.StatusOpen, Priority: 2}
	var patch issueops.IssuePatch
	patch.Status = setField(types.StatusOpen)
	logUpdateAuditChanges("bd-1", "alice", updateAuditChanges(patch, false, false, before, nil))
	// A real change afterwards proves the sidecar is live, so the single
	// row below is not an artifact of a disabled journal.
	patch.Status = setField(types.StatusBlocked)
	logUpdateAuditChanges("bd-1", "alice", updateAuditChanges(patch, false, false, before, nil))

	data, err := os.ReadFile(filepath.Join(beadsDir, "interactions.jsonl"))
	if err != nil {
		t.Fatalf("read interactions.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"old_value":"open"`) || !strings.Contains(lines[0], `"new_value":"blocked"`) {
		t.Fatalf("sidecar rows = %q, want only the open->blocked row", lines)
	}
}
