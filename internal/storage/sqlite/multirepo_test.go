package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

func TestExpandTilde(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"no tilde", "/absolute/path", false},
		{"tilde alone", "~", false},
		{"tilde with path", "~/Documents", false},
		{"relative path", "relative/path", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := expandTilde(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandTilde() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result == "" {
				t.Error("expandTilde() returned empty string")
			}
		})
	}
}

func TestHydrateFromMultiRepo(t *testing.T) {
	t.Run("single-repo mode returns nil", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// No multi-repo config - should return nil
		ctx := context.Background()
		results, err := store.HydrateFromMultiRepo(ctx)
		if err != nil {
			t.Fatalf("HydrateFromMultiRepo() error = %v", err)
		}
		if results != nil {
			t.Errorf("expected nil results in single-repo mode, got %v", results)
		}
	})

	t.Run("hydrates from primary repo", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Initialize config
		if err := config.Initialize(); err != nil {
			t.Fatalf("failed to initialize config: %v", err)
		}

		// Create temporary repo with JSONL file
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads dir: %v", err)
		}

		// Create test issue
		issue := types.Issue{
			ID:         "test-1",
			Title:      "Test Issue",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: ".",
		}
		issue.ContentHash = issue.ComputeContentHash()

		// Write JSONL file
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		f, err := os.Create(jsonlPath)
		if err != nil {
			t.Fatalf("failed to create JSONL file: %v", err)
		}
		enc := json.NewEncoder(f)
		if err := enc.Encode(issue); err != nil {
			f.Close()
			t.Fatalf("failed to write issue: %v", err)
		}
		f.Close()

		// Set multi-repo config
		config.Set("repos.primary", tmpDir)

		ctx := context.Background()
		results, err := store.HydrateFromMultiRepo(ctx)
		if err != nil {
			t.Fatalf("HydrateFromMultiRepo() error = %v", err)
		}

		if results == nil || results["."] != 1 {
			t.Errorf("expected 1 issue from primary repo, got %v", results)
		}

		// Verify issue was imported
		imported, err := store.GetIssue(ctx, "test-1")
		if err != nil {
			t.Fatalf("failed to get imported issue: %v", err)
		}
		if imported.Title != "Test Issue" {
			t.Errorf("expected title 'Test Issue', got %q", imported.Title)
		}
		if imported.SourceRepo != "." {
			t.Errorf("expected source_repo '.', got %q", imported.SourceRepo)
		}

		// Clean up config
		config.Set("repos.primary", "")
	})

	t.Run("uses mtime caching to skip unchanged files", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Initialize config
		if err := config.Initialize(); err != nil {
			t.Fatalf("failed to initialize config: %v", err)
		}

		// Create temporary repo with JSONL file
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads dir: %v", err)
		}

		// Create test issue
		issue := types.Issue{
			ID:         "test-2",
			Title:      "Test Issue 2",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: ".",
		}
		issue.ContentHash = issue.ComputeContentHash()

		// Write JSONL file
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		f, err := os.Create(jsonlPath)
		if err != nil {
			t.Fatalf("failed to create JSONL file: %v", err)
		}
		enc := json.NewEncoder(f)
		if err := enc.Encode(issue); err != nil {
			f.Close()
			t.Fatalf("failed to write issue: %v", err)
		}
		f.Close()

		// Set multi-repo config
		config.Set("repos.primary", tmpDir)

		ctx := context.Background()

		// First hydration - should import
		results1, err := store.HydrateFromMultiRepo(ctx)
		if err != nil {
			t.Fatalf("first HydrateFromMultiRepo() error = %v", err)
		}
		if results1["."] != 1 {
			t.Errorf("first hydration: expected 1 issue, got %d", results1["."])
		}

		// Second hydration - should skip (mtime unchanged)
		results2, err := store.HydrateFromMultiRepo(ctx)
		if err != nil {
			t.Fatalf("second HydrateFromMultiRepo() error = %v", err)
		}
		if results2["."] != 0 {
			t.Errorf("second hydration: expected 0 issues (cached), got %d", results2["."])
		}
	})

	t.Run("imports additional repos", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Initialize config
		if err := config.Initialize(); err != nil {
			t.Fatalf("failed to initialize config: %v", err)
		}

		// Create primary repo
		primaryDir := t.TempDir()
		primaryBeadsDir := filepath.Join(primaryDir, ".beads")
		if err := os.MkdirAll(primaryBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create primary .beads dir: %v", err)
		}

		issue1 := types.Issue{
			ID:         "primary-1",
			Title:      "Primary Issue",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: ".",
		}
		issue1.ContentHash = issue1.ComputeContentHash()

		f1, err := os.Create(filepath.Join(primaryBeadsDir, "issues.jsonl"))
		if err != nil {
			t.Fatalf("failed to create primary JSONL: %v", err)
		}
		json.NewEncoder(f1).Encode(issue1)
		f1.Close()

		// Create additional repo
		additionalDir := t.TempDir()
		additionalBeadsDir := filepath.Join(additionalDir, ".beads")
		if err := os.MkdirAll(additionalBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create additional .beads dir: %v", err)
		}

		issue2 := types.Issue{
			ID:         "additional-1",
			Title:      "Additional Issue",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: additionalDir,
		}
		issue2.ContentHash = issue2.ComputeContentHash()

		f2, err := os.Create(filepath.Join(additionalBeadsDir, "issues.jsonl"))
		if err != nil {
			t.Fatalf("failed to create additional JSONL: %v", err)
		}
		json.NewEncoder(f2).Encode(issue2)
		f2.Close()

		// Set multi-repo config
		config.Set("repos.primary", primaryDir)
		config.Set("repos.additional", []string{additionalDir})

		ctx := context.Background()
		results, err := store.HydrateFromMultiRepo(ctx)
		if err != nil {
			t.Fatalf("HydrateFromMultiRepo() error = %v", err)
		}

		if results["."] != 1 {
			t.Errorf("expected 1 issue from primary, got %d", results["."])
		}
		if results[additionalDir] != 1 {
			t.Errorf("expected 1 issue from additional, got %d", results[additionalDir])
		}

		// Verify both issues were imported
		primary, err := store.GetIssue(ctx, "primary-1")
		if err != nil {
			t.Fatalf("failed to get primary issue: %v", err)
		}
		if primary.SourceRepo != "." {
			t.Errorf("primary issue: expected source_repo '.', got %q", primary.SourceRepo)
		}

		additional, err := store.GetIssue(ctx, "additional-1")
		if err != nil {
			t.Fatalf("failed to get additional issue: %v", err)
		}
		if additional.SourceRepo != additionalDir {
			t.Errorf("additional issue: expected source_repo %q, got %q", additionalDir, additional.SourceRepo)
		}
	})
}

func TestImportJSONLFile(t *testing.T) {
	t.Run("imports issues with dependencies and labels", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Create test JSONL file
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "test.jsonl")
		f, err := os.Create(jsonlPath)
		if err != nil {
			t.Fatalf("failed to create JSONL file: %v", err)
		}

		// Create issues with dependencies and labels
		issue1 := types.Issue{
			ID:         "test-1",
			Title:      "Issue 1",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Labels:     []string{"bug", "critical"},
			SourceRepo: "test",
		}
		issue1.ContentHash = issue1.ComputeContentHash()

		issue2 := types.Issue{
			ID:        "test-2",
			Title:     "Issue 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Dependencies: []*types.Dependency{
				{
					IssueID:     "test-2",
					DependsOnID: "test-1",
					Type:        types.DepBlocks,
					CreatedAt:   time.Now(),
					CreatedBy:   "test",
				},
			},
			SourceRepo: "test",
		}
		issue2.ContentHash = issue2.ComputeContentHash()

		enc := json.NewEncoder(f)
		enc.Encode(issue1)
		enc.Encode(issue2)
		f.Close()

		// Import
		ctx := context.Background()
		count, err := store.importJSONLFile(ctx, jsonlPath, "test")
		if err != nil {
			t.Fatalf("importJSONLFile() error = %v", err)
		}
		if count != 2 {
			t.Errorf("expected 2 issues imported, got %d", count)
		}

		// Verify issues
		imported1, err := store.GetIssue(ctx, "test-1")
		if err != nil {
			t.Fatalf("failed to get issue 1: %v", err)
		}
		if len(imported1.Labels) != 2 {
			t.Errorf("expected 2 labels, got %d", len(imported1.Labels))
		}

		// Verify dependency
		deps, err := store.GetDependencies(ctx, "test-2")
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}
		if len(deps) != 1 {
			t.Errorf("expected 1 dependency, got %d", len(deps))
		}
		if len(deps) > 0 && deps[0].ID != "test-1" {
			t.Errorf("expected dependency on test-1, got %s", deps[0].ID)
		}
	})
}

func TestImportJSONLFileOutOfOrderDeps(t *testing.T) {
	t.Run("handles out-of-order dependencies", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Create test JSONL file with dependency BEFORE its target
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "test.jsonl")
		f, err := os.Create(jsonlPath)
		if err != nil {
			t.Fatalf("failed to create JSONL file: %v", err)
		}

		// Issue 1 depends on Issue 2, but Issue 1 comes FIRST in the file
		// This would fail with FK constraint if not handled properly
		issue1 := types.Issue{
			ID:        "test-1",
			Title:     "Issue 1 (depends on Issue 2)",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Dependencies: []*types.Dependency{
				{
					IssueID:     "test-1",
					DependsOnID: "test-2", // test-2 doesn't exist yet!
					Type:        types.DepBlocks,
					CreatedAt:   time.Now(),
					CreatedBy:   "test",
				},
			},
			SourceRepo: "test",
		}
		issue1.ContentHash = issue1.ComputeContentHash()

		issue2 := types.Issue{
			ID:         "test-2",
			Title:      "Issue 2 (dependency target)",
			Status:     types.StatusOpen,
			Priority:   2,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: "test",
		}
		issue2.ContentHash = issue2.ComputeContentHash()

		enc := json.NewEncoder(f)
		enc.Encode(issue1) // Dependent first
		enc.Encode(issue2) // Dependency target second
		f.Close()

		// Import should succeed despite out-of-order dependencies
		ctx := context.Background()
		count, err := store.importJSONLFile(ctx, jsonlPath, "test")
		if err != nil {
			t.Fatalf("importJSONLFile() error = %v", err)
		}
		if count != 2 {
			t.Errorf("expected 2 issues imported, got %d", count)
		}

		// Verify dependency was created
		deps, err := store.GetDependencies(ctx, "test-1")
		if err != nil {
			t.Fatalf("failed to get dependencies: %v", err)
		}
		if len(deps) != 1 {
			t.Errorf("expected 1 dependency, got %d", len(deps))
		}
		if len(deps) > 0 && deps[0].ID != "test-2" {
			t.Errorf("expected dependency on test-2, got %s", deps[0].ID)
		}
	})

	t.Run("detects orphaned dependencies in corrupted data", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Create test JSONL with orphaned dependency (target doesn't exist)
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "test.jsonl")
		f, err := os.Create(jsonlPath)
		if err != nil {
			t.Fatalf("failed to create JSONL file: %v", err)
		}

		issue := types.Issue{
			ID:        "test-orphan",
			Title:     "Issue with orphaned dependency",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Dependencies: []*types.Dependency{
				{
					IssueID:     "test-orphan",
					DependsOnID: "nonexistent-issue", // This issue doesn't exist
					Type:        types.DepBlocks,
					CreatedAt:   time.Now(),
					CreatedBy:   "test",
				},
			},
			SourceRepo: "test",
		}
		issue.ContentHash = issue.ComputeContentHash()

		enc := json.NewEncoder(f)
		enc.Encode(issue)
		f.Close()

		// Import should fail due to FK violation
		ctx := context.Background()
		_, err = store.importJSONLFile(ctx, jsonlPath, "test")
		if err == nil {
			t.Error("expected error for orphaned dependency, got nil")
		}
		if err != nil && !strings.Contains(err.Error(), "foreign key violation") {
			t.Errorf("expected foreign key violation error, got: %v", err)
		}
	})
}

func TestDeleteIssuesBySourceRepo(t *testing.T) {
	t.Run("deletes all issues from specified repo", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		ctx := context.Background()

		// Create issues with different source_repos
		issue1 := &types.Issue{
			ID:         "bd-repo1-1",
			Title:      "Repo1 Issue 1",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: "~/test-repo",
		}
		issue1.ContentHash = issue1.ComputeContentHash()

		issue2 := &types.Issue{
			ID:         "bd-repo1-2",
			Title:      "Repo1 Issue 2",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: "~/test-repo",
		}
		issue2.ContentHash = issue2.ComputeContentHash()

		issue3 := &types.Issue{
			ID:         "bd-primary-1",
			Title:      "Primary Issue",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: ".",
		}
		issue3.ContentHash = issue3.ComputeContentHash()

		// Insert all issues
		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("failed to create issue1: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("failed to create issue2: %v", err)
		}
		if err := store.CreateIssue(ctx, issue3, "test"); err != nil {
			t.Fatalf("failed to create issue3: %v", err)
		}

		// Delete issues from ~/test-repo
		deletedCount, err := store.DeleteIssuesBySourceRepo(ctx, "~/test-repo")
		if err != nil {
			t.Fatalf("DeleteIssuesBySourceRepo() error = %v", err)
		}
		if deletedCount != 2 {
			t.Errorf("expected 2 issues deleted, got %d", deletedCount)
		}

		// Verify ~/test-repo issues are gone
		// GetIssue returns (nil, nil) when issue doesn't exist
		issue1After, err := store.GetIssue(ctx, "bd-repo1-1")
		if issue1After != nil || err != nil {
			t.Errorf("expected bd-repo1-1 to be deleted, got issue=%v, err=%v", issue1After, err)
		}
		issue2After, err := store.GetIssue(ctx, "bd-repo1-2")
		if issue2After != nil || err != nil {
			t.Errorf("expected bd-repo1-2 to be deleted, got issue=%v, err=%v", issue2After, err)
		}

		// Verify primary issue still exists
		primary, err := store.GetIssue(ctx, "bd-primary-1")
		if err != nil {
			t.Fatalf("primary issue should still exist: %v", err)
		}
		if primary.Title != "Primary Issue" {
			t.Errorf("expected 'Primary Issue', got %q", primary.Title)
		}
	})

	t.Run("returns 0 when no issues match", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		ctx := context.Background()

		// Create an issue with a different source_repo
		issue := &types.Issue{
			ID:         "bd-other-1",
			Title:      "Other Issue",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: ".",
		}
		issue.ContentHash = issue.ComputeContentHash()

		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Delete from non-existent repo
		deletedCount, err := store.DeleteIssuesBySourceRepo(ctx, "~/nonexistent")
		if err != nil {
			t.Fatalf("DeleteIssuesBySourceRepo() error = %v", err)
		}
		if deletedCount != 0 {
			t.Errorf("expected 0 issues deleted, got %d", deletedCount)
		}

		// Verify original issue still exists
		_, err = store.GetIssue(ctx, "bd-other-1")
		if err != nil {
			t.Errorf("issue should still exist: %v", err)
		}
	})

	t.Run("cleans up related data", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		ctx := context.Background()

		// Create an issue with labels and comments
		issue := &types.Issue{
			ID:         "bd-cleanup-1",
			Title:      "Cleanup Test Issue",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: "~/cleanup-repo",
			Labels:     []string{"test", "cleanup"},
		}
		issue.ContentHash = issue.ComputeContentHash()

		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Add a comment
		_, err := store.AddIssueComment(ctx, "bd-cleanup-1", "test", "Test comment")
		if err != nil {
			t.Fatalf("failed to add comment: %v", err)
		}

		// Delete the repo
		deletedCount, err := store.DeleteIssuesBySourceRepo(ctx, "~/cleanup-repo")
		if err != nil {
			t.Fatalf("DeleteIssuesBySourceRepo() error = %v", err)
		}
		if deletedCount != 1 {
			t.Errorf("expected 1 issue deleted, got %d", deletedCount)
		}

		// Verify issue is gone
		// GetIssue returns (nil, nil) when issue doesn't exist
		issueAfter, err := store.GetIssue(ctx, "bd-cleanup-1")
		if issueAfter != nil || err != nil {
			t.Errorf("expected issue to be deleted, got issue=%v, err=%v", issueAfter, err)
		}

		// Verify labels are gone (query directly to check)
		var labelCount int
		err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labels WHERE issue_id = ?`, "bd-cleanup-1").Scan(&labelCount)
		if err != nil {
			t.Fatalf("failed to query labels: %v", err)
		}
		if labelCount != 0 {
			t.Errorf("expected 0 labels, got %d", labelCount)
		}

		// Verify comments are gone
		var commentCount int
		err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE issue_id = ?`, "bd-cleanup-1").Scan(&commentCount)
		if err != nil {
			t.Fatalf("failed to query comments: %v", err)
		}
		if commentCount != 0 {
			t.Errorf("expected 0 comments, got %d", commentCount)
		}
	})
}

func TestClearRepoMtime(t *testing.T) {
	t.Run("clears mtime cache for repo", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		ctx := context.Background()

		// Insert a mtime cache entry directly
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		// Create a dummy JSONL file for the mtime
		f, err := os.Create(jsonlPath)
		if err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}
		f.Close()

		_, err = store.db.ExecContext(ctx, `
			INSERT INTO repo_mtimes (repo_path, jsonl_path, mtime_ns, last_checked)
			VALUES (?, ?, ?, ?)
		`, tmpDir, jsonlPath, 12345, time.Now())
		if err != nil {
			t.Fatalf("failed to insert mtime cache: %v", err)
		}

		// Verify it exists
		var count int
		err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_mtimes WHERE repo_path = ?`, tmpDir).Scan(&count)
		if err != nil {
			t.Fatalf("failed to query mtime cache: %v", err)
		}
		if count != 1 {
			t.Fatalf("expected 1 mtime cache entry, got %d", count)
		}

		// Clear it
		if err := store.ClearRepoMtime(ctx, tmpDir); err != nil {
			t.Fatalf("ClearRepoMtime() error = %v", err)
		}

		// Verify it's gone
		err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_mtimes WHERE repo_path = ?`, tmpDir).Scan(&count)
		if err != nil {
			t.Fatalf("failed to query mtime cache: %v", err)
		}
		if count != 0 {
			t.Errorf("expected 0 mtime cache entries, got %d", count)
		}
	})

	t.Run("handles non-existent repo gracefully", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		ctx := context.Background()

		// Clear a repo that doesn't exist in cache - should not error
		err := store.ClearRepoMtime(ctx, "/nonexistent/path")
		if err != nil {
			t.Errorf("ClearRepoMtime() should not error for non-existent path: %v", err)
		}
	})
}

func TestExportToMultiRepo(t *testing.T) {
	t.Run("returns nil in single-repo mode", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Initialize config fresh
		if err := config.Initialize(); err != nil {
			t.Fatalf("failed to initialize config: %v", err)
		}

		// Clear any multi-repo config from previous tests
		config.Set("repos.primary", "")
		config.Set("repos.additional", nil)

		ctx := context.Background()
		results, err := store.ExportToMultiRepo(ctx)
		if err != nil {
			t.Errorf("unexpected error in single-repo mode: %v", err)
		}
		if results != nil {
			t.Errorf("expected nil results in single-repo mode, got %v", results)
		}
	})

	t.Run("exports issues to correct repos", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Initialize config
		if err := config.Initialize(); err != nil {
			t.Fatalf("failed to initialize config: %v", err)
		}

		// Create temporary repos
		primaryDir := t.TempDir()
		additionalDir := t.TempDir()

		// Create .beads directories
		primaryBeadsDir := filepath.Join(primaryDir, ".beads")
		additionalBeadsDir := filepath.Join(additionalDir, ".beads")
		if err := os.MkdirAll(primaryBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create primary .beads dir: %v", err)
		}
		if err := os.MkdirAll(additionalBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create additional .beads dir: %v", err)
		}

		// Set multi-repo config
		config.Set("repos.primary", primaryDir)
		config.Set("repos.additional", []string{additionalDir})

		ctx := context.Background()

		// Create issues with different source_repos
		issue1 := &types.Issue{
			ID:         "bd-primary-1",
			Title:      "Primary Issue",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: ".",
		}
		issue1.ContentHash = issue1.ComputeContentHash()

		issue2 := &types.Issue{
			ID:         "bd-additional-1",
			Title:      "Additional Issue",
			Status:     types.StatusOpen,
			Priority:   1,
			IssueType:  types.TypeTask,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			SourceRepo: additionalDir,
		}
		issue2.ContentHash = issue2.ComputeContentHash()

		// Insert issues
		if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
			t.Fatalf("failed to create primary issue: %v", err)
		}
		if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
			t.Fatalf("failed to create additional issue: %v", err)
		}

		// Export to multi-repo
		results, err := store.ExportToMultiRepo(ctx)
		if err != nil {
			t.Fatalf("ExportToMultiRepo() error = %v", err)
		}

		// Verify export counts
		if results["."] != 1 {
			t.Errorf("expected 1 issue exported to primary, got %d", results["."])
		}
		if results[additionalDir] != 1 {
			t.Errorf("expected 1 issue exported to additional, got %d", results[additionalDir])
		}

		// Verify JSONL files exist and contain correct issues
		primaryJSONL := filepath.Join(primaryBeadsDir, "issues.jsonl")
		additionalJSONL := filepath.Join(additionalBeadsDir, "issues.jsonl")

		// Check primary JSONL
		f1, err := os.Open(primaryJSONL)
		if err != nil {
			t.Fatalf("failed to open primary JSONL: %v", err)
		}
		defer f1.Close()

		var primaryIssue types.Issue
		if err := json.NewDecoder(f1).Decode(&primaryIssue); err != nil {
			t.Fatalf("failed to decode primary issue: %v", err)
		}
		if primaryIssue.ID != "bd-primary-1" {
			t.Errorf("expected bd-primary-1 in primary JSONL, got %s", primaryIssue.ID)
		}

		// Check additional JSONL
		f2, err := os.Open(additionalJSONL)
		if err != nil {
			t.Fatalf("failed to open additional JSONL: %v", err)
		}
		defer f2.Close()

		var additionalIssue types.Issue
		if err := json.NewDecoder(f2).Decode(&additionalIssue); err != nil {
			t.Fatalf("failed to decode additional issue: %v", err)
		}
		if additionalIssue.ID != "bd-additional-1" {
			t.Errorf("expected bd-additional-1 in additional JSONL, got %s", additionalIssue.ID)
		}
	})
}

// TestUpsertPreservesGateFields tests that gate await fields are preserved during upsert (bd-gr4q).
// Gates are wisps and aren't exported to JSONL. When an issue with the same ID is imported,
// the await fields should NOT be cleared.
func TestUpsertPreservesGateFields(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a gate with await fields directly in the database
	gate := &types.Issue{
		ID:        "bd-gate1",
		Title:     "Test Gate",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeGate,
		Ephemeral: true,
		AwaitType: "gh:run",
		AwaitID:   "123456789",
		Timeout:   30 * 60 * 1000000000, // 30 minutes in nanoseconds
		Waiters:   []string{"beads/dave"},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	gate.ContentHash = gate.ComputeContentHash()

	if err := store.CreateIssue(ctx, gate, "test"); err != nil {
		t.Fatalf("failed to create gate: %v", err)
	}

	// Verify gate was created with await fields
	retrieved, err := store.GetIssue(ctx, gate.ID)
	if err != nil || retrieved == nil {
		t.Fatalf("failed to get gate: %v", err)
	}
	if retrieved.AwaitType != "gh:run" {
		t.Errorf("expected AwaitType=gh:run, got %q", retrieved.AwaitType)
	}
	if retrieved.AwaitID != "123456789" {
		t.Errorf("expected AwaitID=123456789, got %q", retrieved.AwaitID)
	}

	// Create a JSONL file with an issue that has the same ID but no await fields
	// (simulating what happens when a non-gate issue is imported)
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("failed to create JSONL file: %v", err)
	}

	// Same ID, different content (to trigger update), no await fields
	incomingIssue := types.Issue{
		ID:          "bd-gate1",
		Title:       "Test Gate Updated", // Different title to trigger update
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeGate,
		AwaitType:   "", // Empty - simulating JSONL without await fields
		AwaitID:     "", // Empty
		Timeout:     0,
		Waiters:     nil,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now().Add(time.Second), // Newer timestamp
	}
	incomingIssue.ContentHash = incomingIssue.ComputeContentHash()

	enc := json.NewEncoder(f)
	if err := enc.Encode(incomingIssue); err != nil {
		t.Fatalf("failed to encode issue: %v", err)
	}
	f.Close()

	// Import the JSONL file (this should NOT clear the await fields)
	_, err = store.importJSONLFile(ctx, jsonlPath, "test")
	if err != nil {
		t.Fatalf("importJSONLFile failed: %v", err)
	}

	// Verify await fields are preserved
	updated, err := store.GetIssue(ctx, gate.ID)
	if err != nil || updated == nil {
		t.Fatalf("failed to get updated gate: %v", err)
	}

	// Title should be updated
	if updated.Title != "Test Gate Updated" {
		t.Errorf("expected title to be updated, got %q", updated.Title)
	}

	// Await fields should be PRESERVED (not cleared)
	if updated.AwaitType != "gh:run" {
		t.Errorf("AwaitType was cleared! expected 'gh:run', got %q", updated.AwaitType)
	}
	if updated.AwaitID != "123456789" {
		t.Errorf("AwaitID was cleared! expected '123456789', got %q", updated.AwaitID)
	}
	if updated.Timeout != 30*60*1000000000 {
		t.Errorf("Timeout was cleared! expected %d, got %d", 30*60*1000000000, updated.Timeout)
	}
	if len(updated.Waiters) != 1 || updated.Waiters[0] != "beads/dave" {
		t.Errorf("Waiters was cleared! expected [beads/dave], got %v", updated.Waiters)
	}
}
