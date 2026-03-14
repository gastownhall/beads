//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gastownhall/beads/internal/types"
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
