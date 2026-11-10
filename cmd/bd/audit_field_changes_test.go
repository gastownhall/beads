package main

import (
	"reflect"
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
