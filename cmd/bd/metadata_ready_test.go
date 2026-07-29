//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestGetReadyWork_MetadataSuite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()

	// Create all test data up front with unique metadata keys per subtest.
	allIssues := []*types.Issue{
		// --- FieldMatch data ---
		{ID: "mr-fm-1", Title: "Platform task (fm)", Priority: 2, IssueType: types.TypeTask, Status: types.StatusOpen,
			Metadata: json.RawMessage(`{"mr_fm_team":"platform"}`)},
		{ID: "mr-fm-2", Title: "Frontend task (fm)", Priority: 2, IssueType: types.TypeTask, Status: types.StatusOpen,
			Metadata: json.RawMessage(`{"mr_fm_team":"frontend"}`)},
		// --- HasMetadataKey data ---
		{ID: "mr-hmk-1", Title: "Has team (hmk)", Priority: 2, IssueType: types.TypeTask, Status: types.StatusOpen,
			Metadata: json.RawMessage(`{"mr_hmk_team":"platform"}`)},
		{ID: "mr-hmk-2", Title: "No metadata (hmk)", Priority: 2, IssueType: types.TypeTask, Status: types.StatusOpen},
		// --- NoMatch data ---
		{ID: "mr-nm-1", Title: "Platform task (nm)", Priority: 2, IssueType: types.TypeTask, Status: types.StatusOpen,
			Metadata: json.RawMessage(`{"mr_nm_team":"platform"}`)},
	}
	for _, issue := range allIssues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}

	t.Run("FieldMatch", func(t *testing.T) {
		results, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:         "open",
			MetadataFields: map[string]string{"mr_fm_team": "platform"},
		})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != "mr-fm-1" {
			t.Errorf("expected issue mr-fm-1, got %s", results[0].ID)
		}
	})

	t.Run("HasMetadataKey", func(t *testing.T) {
		results, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:         "open",
			HasMetadataKey: "mr_hmk_team",
		})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ID != "mr-hmk-1" {
			t.Errorf("expected issue mr-hmk-1, got %s", results[0].ID)
		}
	})

	t.Run("FieldNoMatch", func(t *testing.T) {
		results, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:         "open",
			MetadataFields: map[string]string{"mr_nm_team": "backend"},
		})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("FieldInvalidKey", func(t *testing.T) {
		_, err := store.GetReadyWork(ctx, types.WorkFilter{
			Status:         "open",
			MetadataFields: map[string]string{"'; DROP TABLE issues; --": "val"},
		})
		if err == nil {
			t.Fatal("expected error for invalid metadata key, got nil")
		}
	})
}

func TestGetReadyWork_IncludeEphemeralAssigneeIsSuperset(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	ctx := context.Background()
	worker := "control-dispatcher"

	issues := []*types.Issue{
		{
			ID:        "mr-assignee-persistent",
			Title:     "Persistent assigned task",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Assignee:  worker,
		},
		{
			ID:        "mr-assignee-no-history",
			Title:     "No-history assigned task",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Assignee:  worker,
			NoHistory: true,
		},
		{
			ID:        "mr-assignee-ephemeral",
			Title:     "Ephemeral assigned task",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Assignee:  worker,
			Ephemeral: true,
		},
		{
			ID:        "mr-assignee-other",
			Title:     "Other assigned task",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Assignee:  "someone-else",
			Ephemeral: true,
		},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}

	defaultResults, err := store.GetReadyWork(ctx, types.WorkFilter{
		Status:   types.StatusOpen,
		Assignee: &worker,
	})
	if err != nil {
		t.Fatalf("GetReadyWork default: %v", err)
	}
	if got := issueIDs(defaultResults); !sameStringSet(got, []string{"mr-assignee-persistent", "mr-assignee-no-history"}) {
		t.Fatalf("default ready IDs = %v, want persistent and no-history only", got)
	}

	allResults, err := store.GetReadyWork(ctx, types.WorkFilter{
		Status:           types.StatusOpen,
		Assignee:         &worker,
		IncludeEphemeral: true,
	})
	if err != nil {
		t.Fatalf("GetReadyWork include ephemeral: %v", err)
	}
	if got := issueIDs(allResults); !sameStringSet(got, []string{"mr-assignee-persistent", "mr-assignee-no-history", "mr-assignee-ephemeral"}) {
		t.Fatalf("include-ephemeral ready IDs = %v, want persistent, no-history, and ephemeral for assignee", got)
	}
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, v := range want {
		counts[v]++
	}
	for _, v := range got {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	return true
}
