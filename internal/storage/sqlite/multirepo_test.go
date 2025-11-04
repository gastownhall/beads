package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
			ID:          "test-1",
			Title:       "Test Issue",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
			SourceRepo:  ".",
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
			ID:          "test-2",
			Title:       "Test Issue 2",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
			SourceRepo:  ".",
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
