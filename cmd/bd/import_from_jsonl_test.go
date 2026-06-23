//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestImportFromLocalJSONL(t *testing.T) {
	skipIfNoDolt(t)

	t.Run("imports issues from JSONL file", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		// Create a JSONL file with test issues
		jsonlContent := `{"id":"test-abc123","title":"First issue","type":"bug","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-def456","title":"Second issue","type":"task","status":"open","priority":3,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}

		if count != 2 {
			t.Errorf("Expected 2 issues imported, got %d", count)
		}

		// Verify issues exist in the store
		issue1, err := store.GetIssue(ctx, "test-abc123")
		if err != nil {
			t.Fatalf("Failed to get first issue: %v", err)
		}
		if issue1.Title != "First issue" {
			t.Errorf("Expected title 'First issue', got %q", issue1.Title)
		}

		issue2, err := store.GetIssue(ctx, "test-def456")
		if err != nil {
			t.Fatalf("Failed to get second issue: %v", err)
		}
		if issue2.Title != "Second issue" {
			t.Errorf("Expected title 'Second issue', got %q", issue2.Title)
		}
	})

	t.Run("empty JSONL file imports zero issues", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}

		if count != 0 {
			t.Errorf("Expected 0 issues imported from empty file, got %d", count)
		}
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		ctx := context.Background()
		_, err := importFromLocalJSONL(ctx, store, "/nonexistent/issues.jsonl")
		if err == nil {
			t.Error("Expected error for nonexistent file, got nil")
		}
	})

	t.Run("skips beads-jsonl metadata header line", func(t *testing.T) {
		// Canonical beads-jsonl exports prepend a schema/provenance
		// header record (no _type, no issue fields). Without the
		// header-skip guard it falls through to the issue path and
		// aborts the whole import with
		// "validation failed for issue : title is required",
		// stranding every command on an empty auto-imported DB.
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlContent := `{"_dolt_branch":"main","_dolt_commit":"abc123","_project_id":"p1","_schema":"beads-jsonl/1","_sort":"stable-v1"}
{"id":"test-hdr1","title":"After header","type":"bug","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed on header line: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 issue imported (header skipped), got %d", count)
		}
		if _, err := store.GetIssue(ctx, "test-hdr1"); err != nil {
			t.Fatalf("issue after header was not imported: %v", err)
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte("not valid json\n"), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		_, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err == nil {
			t.Error("Expected error for invalid JSON, got nil")
		}
	})

	t.Run("re-import with duplicate IDs succeeds via upsert", func(t *testing.T) {
		// GH#2061: importing the same JSONL twice should not fail with
		// "duplicate primary key" — the second import should upsert.
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlContent := `{"id":"test-dup1","title":"Original title","type":"bug","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-dup2","title":"Second issue","type":"task","status":"open","priority":3,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()

		// First import
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("first importFromLocalJSONL failed: %v", err)
		}
		if count != 2 {
			t.Errorf("Expected 2 issues imported on first import, got %d", count)
		}

		// Second import with same IDs — should succeed (upsert), not fail
		updatedContent := `{"id":"test-dup1","title":"Updated title","type":"bug","status":"closed","priority":1,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-06-01T00:00:00Z"}
{"id":"test-dup2","title":"Second issue","type":"task","status":"open","priority":3,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		if err := os.WriteFile(jsonlPath, []byte(updatedContent), 0644); err != nil {
			t.Fatalf("Failed to write updated JSONL file: %v", err)
		}

		count2, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("second importFromLocalJSONL failed (duplicate key?): %v", err)
		}
		if count2 != 2 {
			t.Errorf("Expected 2 issues on re-import, got %d", count2)
		}

		// Verify the first issue was updated (upsert, not just inserted)
		issue, err := store.GetIssue(ctx, "test-dup1")
		if err != nil {
			t.Fatalf("Failed to get upserted issue: %v", err)
		}
		if issue.Title != "Updated title" {
			t.Errorf("Expected title 'Updated title' after upsert, got %q", issue.Title)
		}
		if issue.Status != "closed" {
			t.Errorf("Expected status 'closed' after upsert, got %q", issue.Status)
		}
	})

	t.Run("stale JSONL does not clobber newer local issue", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		ctx := context.Background()
		createdAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		localUpdatedAt := createdAt.Add(2 * time.Hour)
		local := &types.Issue{
			ID:        "test-stale-import",
			Title:     "newer local title",
			Status:    types.StatusInProgress,
			IssueType: types.TypeTask,
			Priority:  1,
			CreatedAt: createdAt,
			UpdatedAt: localUpdatedAt,
		}
		if err := store.CreateIssue(ctx, local, "test"); err != nil {
			t.Fatalf("CreateIssue local: %v", err)
		}

		jsonlContent := `{"id":"test-stale-import","title":"stale exported title","status":"open","priority":3,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T01:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}
		if count != 0 {
			t.Fatalf("Expected stale import to import 0 issues, got %d", count)
		}

		got, err := store.GetIssue(ctx, "test-stale-import")
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Title != "newer local title" {
			t.Fatalf("stale JSONL clobbered title: got %q", got.Title)
		}
		if got.Status != types.StatusInProgress {
			t.Fatalf("stale JSONL clobbered status: got %q", got.Status)
		}
		if got.Priority != 1 {
			t.Fatalf("stale JSONL clobbered priority: got %d", got.Priority)
		}
	})

	t.Run("child counter reconciled after JSONL import prevents overwrites", func(t *testing.T) {
		// Regression test for GH#2166: bd create --parent after bd init --from-jsonl
		// must not overwrite existing child issues. The child_counters table
		// must be reconciled from imported hierarchical IDs.
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		// Import an epic with two existing children via JSONL
		jsonlContent := `{"id":"test-epic1","title":"Epic","type":"epic","status":"open","priority":1,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-epic1.1","title":"Child 1","type":"task","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-epic1.2","title":"Child 2","type":"task","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}
		if count != 3 {
			t.Errorf("Expected 3 issues imported, got %d", count)
		}

		// Now request the next child ID for the epic — this MUST be .3, not .1
		nextID, err := store.GetNextChildID(ctx, "test-epic1")
		if err != nil {
			t.Fatalf("GetNextChildID failed: %v", err)
		}
		if nextID != "test-epic1.3" {
			t.Errorf("Expected next child ID 'test-epic1.3', got %q (would overwrite existing child!)", nextID)
		}

		// Verify original children are still intact
		child1, err := store.GetIssue(ctx, "test-epic1.1")
		if err != nil {
			t.Fatalf("Failed to get child 1: %v", err)
		}
		if child1.Title != "Child 1" {
			t.Errorf("Child 1 title changed unexpectedly: got %q", child1.Title)
		}
		child2, err := store.GetIssue(ctx, "test-epic1.2")
		if err != nil {
			t.Fatalf("Failed to get child 2: %v", err)
		}
		if child2.Title != "Child 2" {
			t.Errorf("Child 2 title changed unexpectedly: got %q", child2.Title)
		}
	})

	t.Run("skips cyclic and self dependencies instead of aborting import", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		ctx := context.Background()
		now := time.Now().UTC()
		issues := []*types.Issue{
			{
				ID:        "test-cycle-a",
				Title:     "Cycle A",
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
				Priority:  2,
				CreatedAt: now,
				UpdatedAt: now,
				Dependencies: []*types.Dependency{{
					DependsOnID: "test-cycle-b",
					Type:        types.DepBlocks,
				}},
			},
			{
				ID:        "test-cycle-b",
				Title:     "Cycle B",
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
				Priority:  2,
				CreatedAt: now,
				UpdatedAt: now,
				Dependencies: []*types.Dependency{{
					DependsOnID: "test-cycle-a",
					Type:        types.DepBlocks,
				}},
			},
			{
				ID:        "test-self",
				Title:     "Self dependency",
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
				Priority:  2,
				CreatedAt: now,
				UpdatedAt: now,
				Dependencies: []*types.Dependency{{
					DependsOnID: "test-self",
					Type:        types.DepBlocks,
				}},
			},
		}

		result, err := importIssuesCore(ctx, "", store, issues, ImportOptions{SkipPrefixValidation: true})
		if err != nil {
			t.Fatalf("importIssuesCore failed: %v", err)
		}
		if result.Created != 3 {
			t.Fatalf("Created = %d, want 3", result.Created)
		}
		if got := strings.Join(result.SkippedDependencies, "\n"); !strings.Contains(got, "test-cycle-b -> test-cycle-a") ||
			!strings.Contains(got, "test-self -> test-self") {
			t.Fatalf("SkippedDependencies = %#v, want cycle and self-dependency details", result.SkippedDependencies)
		}

		for _, id := range []string{"test-cycle-a", "test-cycle-b", "test-self"} {
			if _, err := store.GetIssue(ctx, id); err != nil {
				t.Fatalf("imported issue %s missing: %v", id, err)
			}
		}
		deps, err := store.GetDependencyRecords(ctx, "test-cycle-a")
		if err != nil {
			t.Fatalf("GetDependencyRecords(test-cycle-a): %v", err)
		}
		if len(deps) != 1 || deps[0].DependsOnID != "test-cycle-b" {
			t.Fatalf("test-cycle-a deps = %#v, want only test-cycle-a -> test-cycle-b", deps)
		}
		for _, id := range []string{"test-cycle-b", "test-self"} {
			deps, err := store.GetDependencyRecords(ctx, id)
			if err != nil {
				t.Fatalf("GetDependencyRecords(%s): %v", id, err)
			}
			if len(deps) != 0 {
				t.Fatalf("%s deps = %#v, want none", id, deps)
			}
		}
	})

	t.Run("skips mixed regular and wisp in-batch dependencies instead of aborting import", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		ctx := context.Background()
		now := time.Now().UTC()
		issues := []*types.Issue{
			{
				ID:        "test-mixed-regular",
				Title:     "Regular source",
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
				Priority:  2,
				CreatedAt: now,
				UpdatedAt: now,
				Dependencies: []*types.Dependency{{
					DependsOnID: "test-mixed-wisp",
					Type:        types.DepBlocks,
				}},
			},
			{
				ID:        "test-mixed-wisp",
				Title:     "Wisp target",
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
				Priority:  2,
				CreatedAt: now,
				UpdatedAt: now,
				Ephemeral: true,
			},
		}

		result, err := importIssuesCore(ctx, "", store, issues, ImportOptions{SkipPrefixValidation: true})
		if err != nil {
			t.Fatalf("importIssuesCore failed: %v", err)
		}
		if result.Created != 2 {
			t.Fatalf("Created = %d, want 2", result.Created)
		}
		if got := strings.Join(result.SkippedDependencies, "\n"); !strings.Contains(got, "test-mixed-regular -> test-mixed-wisp") ||
			!strings.Contains(got, "cross-bucket dependency") {
			t.Fatalf("SkippedDependencies = %#v, want mixed regular/wisp dependency detail", result.SkippedDependencies)
		}

		for _, id := range []string{"test-mixed-regular", "test-mixed-wisp"} {
			if _, err := store.GetIssue(ctx, id); err != nil {
				t.Fatalf("imported issue %s missing: %v", id, err)
			}
		}
		deps, err := store.GetDependencyRecords(ctx, "test-mixed-regular")
		if err != nil {
			t.Fatalf("GetDependencyRecords(test-mixed-regular): %v", err)
		}
		if len(deps) != 0 {
			t.Fatalf("test-mixed-regular deps = %#v, want none", deps)
		}
	})

	t.Run("preserves dependency created_by from JSONL import", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlContent := `{"id":"test-dep-target","title":"Dependency target","status":"open","priority":2,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-dep-source","title":"Dependency source","status":"open","priority":2,"issue_type":"task","dependencies":[{"depends_on_id":"test-dep-target","type":"blocks","created_by":"someone.else","created_at":"2025-01-01T00:00:00Z"}],"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}
		if count != 2 {
			t.Fatalf("imported count = %d, want 2", count)
		}

		deps, err := store.GetDependencyRecords(ctx, "test-dep-source")
		if err != nil {
			t.Fatalf("GetDependencyRecords(test-dep-source): %v", err)
		}
		if len(deps) != 1 {
			t.Fatalf("deps = %#v, want one dependency", deps)
		}
		if deps[0].CreatedBy != "someone.else" {
			t.Fatalf("dependency created_by = %q, want someone.else", deps[0].CreatedBy)
		}
	})

	t.Run("skips tombstone entries during import", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		// JSONL with a mix of valid issues and tombstone entries (deleted agents from older versions)
		jsonlContent := `{"id":"test-valid1","title":"Valid issue","type":"task","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-tombstone1","title":"Deleted agent","type":"agent","status":"tombstone","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-valid2","title":"Another valid issue","type":"bug","status":"open","priority":1,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-tombstone2","title":"Another deleted agent","type":"agent","status":"tombstone","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed (tombstones should be skipped, not cause errors): %v", err)
		}

		if count != 2 {
			t.Errorf("Expected 2 issues imported (tombstones skipped), got %d", count)
		}

		// Verify valid issues were imported
		issue1, err := store.GetIssue(ctx, "test-valid1")
		if err != nil {
			t.Fatalf("Failed to get valid issue 1: %v", err)
		}
		if issue1.Title != "Valid issue" {
			t.Errorf("Expected title 'Valid issue', got %q", issue1.Title)
		}

		// Verify tombstone entries were NOT imported
		_, err = store.GetIssue(ctx, "test-tombstone1")
		if err == nil {
			t.Error("Expected tombstone issue to be skipped, but it was imported")
		}
	})

	t.Run("sets prefix from first issue when not configured", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStoreWithPrefix(t, dbPath, "") // Empty prefix

		jsonlContent := `{"id":"myprefix-abc123","title":"Test issue","type":"bug","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		// Clear any existing prefix
		_ = store.SetConfig(ctx, "issue_prefix", "")

		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 issue imported, got %d", count)
		}

		// Verify prefix was auto-detected
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get issue_prefix: %v", err)
		}
		if prefix != "myprefix" {
			t.Errorf("Expected auto-detected prefix 'myprefix', got %q", prefix)
		}
	})

	t.Run("re-import does not duplicate comments", func(t *testing.T) {
		// Comments use a UUID PK (DEFAULT UUID()), so a naive INSERT would create
		// duplicates on every re-import. PersistComments must deduplicate
		// by checking (issue_id, author, created_at) before inserting.
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlContent := `{"id":"test-cmt1","title":"Issue with comments","type":"task","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z","comments":[{"author":"alice","text":"First comment","created_at":"2025-01-01T12:00:00Z"},{"author":"bob","text":"Second comment","created_at":"2025-01-01T13:00:00Z"}]}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()

		// First import
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("first import failed: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 issue imported, got %d", count)
		}

		// Second import — same data
		count2, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("second import failed: %v", err)
		}
		if count2 != 1 {
			t.Errorf("Expected 1 issue on re-import, got %d", count2)
		}

		// Verify comments were NOT duplicated
		issue, err := store.GetIssue(ctx, "test-cmt1")
		if err != nil {
			t.Fatalf("Failed to get issue: %v", err)
		}
		if len(issue.Comments) != 2 {
			t.Errorf("Expected 2 comments after re-import, got %d (duplicates!)", len(issue.Comments))
		}
	})

	t.Run("no_history flag survives JSONL import roundtrip", func(t *testing.T) {
		// Regression test for GH#2619: ImportFromLocalJSONL must preserve no_history=true.
		// NoHistory beads are stored in the wisps table with no_history=1. The issueops
		// InsertIssueIntoTable function must include no_history in the INSERT or the flag
		// is silently dropped and the bead becomes GC-eligible after restore.
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		// JSONL line with no_history=true (and ephemeral=false).
		// This represents a NoHistory bead exported from a live database.
		jsonlContent := `{"id":"test-nh1","title":"NoHistory bead","type":"task","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z","no_history":true}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("importFromLocalJSONL failed: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 issue imported, got %d", count)
		}

		// Verify the bead was imported with no_history=true preserved.
		issue, err := store.GetIssue(ctx, "test-nh1")
		if err != nil {
			t.Fatalf("Failed to get NoHistory bead after import: %v", err)
		}
		if issue.Title != "NoHistory bead" {
			t.Errorf("Expected title 'NoHistory bead', got %q", issue.Title)
		}
		if !issue.NoHistory {
			t.Error("no_history=true was lost during JSONL import: bead is now GC-eligible (would be incorrectly collected by wisp GC)")
		}
		// NoHistory beads must NOT have ephemeral=true (they're not GC-eligible)
		if issue.Ephemeral {
			t.Error("NoHistory bead must not be ephemeral=true after import")
		}
	})
}

func TestImportFromLocalJSONL_LegacyFormats(t *testing.T) {
	skipIfNoDolt(t)

	t.Run("numeric comment IDs from pre-v1.0", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlContent := `{"id":"test-numcmt","title":"Old comments","status":"open","priority":1,"issue_type":"task","comments":[{"id":7,"issue_id":"test-numcmt","author":"alice","text":"numeric id comment","created_at":"2025-01-01T01:00:00Z"}],"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("import with numeric comment IDs should not fail: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 issue imported, got %d", count)
		}

		issue, err := store.GetIssue(ctx, "test-numcmt")
		if err != nil {
			t.Fatalf("Failed to get issue: %v", err)
		}
		if len(issue.Comments) != 1 {
			t.Fatalf("Expected 1 comment, got %d", len(issue.Comments))
		}
		if issue.Comments[0].Text != "numeric id comment" {
			t.Errorf("Comment text = %q, want %q", issue.Comments[0].Text, "numeric id comment")
		}
	})

	t.Run("wisp field mapped to ephemeral", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "dolt")
		store := newTestStore(t, dbPath)

		jsonlContent := `{"id":"test-wisp1","title":"Wisp true","status":"open","priority":0,"issue_type":"task","wisp":true,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-wisp2","title":"Wisp false","status":"open","priority":0,"issue_type":"task","wisp":false,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-wisp3","title":"No wisp field","status":"open","priority":0,"issue_type":"task","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
`
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
			t.Fatalf("Failed to write JSONL file: %v", err)
		}

		ctx := context.Background()
		count, err := importFromLocalJSONL(ctx, store, jsonlPath)
		if err != nil {
			t.Fatalf("import with wisp field should not fail: %v", err)
		}
		if count != 3 {
			t.Errorf("Expected 3 issues imported, got %d", count)
		}

		issue1, err := store.GetIssue(ctx, "test-wisp1")
		if err != nil {
			t.Fatalf("Failed to get wisp=true issue: %v", err)
		}
		if !issue1.Ephemeral {
			t.Error("wisp=true should map to ephemeral=true")
		}

		issue2, err := store.GetIssue(ctx, "test-wisp2")
		if err != nil {
			t.Fatalf("Failed to get wisp=false issue: %v", err)
		}
		if issue2.Ephemeral {
			t.Error("wisp=false should not set ephemeral=true")
		}

		issue3, err := store.GetIssue(ctx, "test-wisp3")
		if err != nil {
			t.Fatalf("Failed to get no-wisp issue: %v", err)
		}
		if issue3.Ephemeral {
			t.Error("missing wisp field should not set ephemeral=true")
		}
	})
}
