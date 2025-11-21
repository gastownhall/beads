package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestExportToJSONLWithStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create test issue
	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Export to JSONL
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("exportToJSONLWithStore failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Fatal("JSONL file was not created")
	}

	// Read and verify content
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL: %v", err)
	}

	var exported types.Issue
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatalf("failed to unmarshal JSONL: %v", err)
	}

	if exported.ID != "test-1" {
		t.Errorf("expected ID 'test-1', got %s", exported.ID)
	}
	if exported.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %s", exported.Title)
	}
}

func TestExportToJSONLWithStore_EmptyDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage (empty)
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create existing JSONL with content
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	existingIssue := &types.Issue{
		ID:        "existing-1",
		Title:     "Existing",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	data, _ := json.Marshal(existingIssue)
	if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write existing JSONL: %v", err)
	}

	// Should refuse to export empty DB over non-empty JSONL
	err = exportToJSONLWithStore(ctx, store, jsonlPath)
	if err == nil {
		t.Fatal("expected error when exporting empty DB over non-empty JSONL")
	}
}

func TestImportToJSONLWithStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage first to initialize database
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create JSONL with test data
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	issue := &types.Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "Test description",
		IssueType:   types.TypeBug,
		Priority:    1,
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	data, _ := json.Marshal(issue)
	if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	// Import from JSONL
	if err := importToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("importToJSONLWithStore failed: %v", err)
	}

	// Verify issue was imported
	imported, err := store.GetIssue(ctx, "test-1")
	if err != nil {
		t.Fatalf("failed to get imported issue: %v", err)
	}

	if imported.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %s", imported.Title)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	// Create storage and add issues
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create multiple issues with dependencies
	issue1 := &types.Issue{
		ID:        "test-1",
		Title:     "Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	issue2 := &types.Issue{
		ID:        "test-2",
		Title:     "Issue 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeFeature,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatalf("failed to create issue1: %v", err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatalf("failed to create issue2: %v", err)
	}

	// Add dependency
	dep := &types.Dependency{
		IssueID:     "test-2",
		DependsOnID: "test-1",
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Add labels
	if err := store.AddLabel(ctx, "test-1", "bug", "test"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}

	// Export
	if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Create new database
	dbPath2 := filepath.Join(tmpDir, ".beads", "beads2.db")
	store2, err := sqlite.New(context.Background(), dbPath2)
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}
	defer store2.Close()

	// Set issue_prefix for second database
	if err := store2.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix for store2: %v", err)
	}

	// Import
	if err := importToJSONLWithStore(ctx, store2, jsonlPath); err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify issues
	imported1, err := store2.GetIssue(ctx, "test-1")
	if err != nil {
		t.Fatalf("failed to get imported issue1: %v", err)
	}
	if imported1.Title != "Issue 1" {
		t.Errorf("expected title 'Issue 1', got %s", imported1.Title)
	}

	imported2, err := store2.GetIssue(ctx, "test-2")
	if err != nil {
		t.Fatalf("failed to get imported issue2: %v", err)
	}
	if imported2.Title != "Issue 2" {
		t.Errorf("expected title 'Issue 2', got %s", imported2.Title)
	}

	// Verify dependency
	deps, err := store2.GetDependencies(ctx, "test-2")
	if err != nil {
		t.Fatalf("failed to get dependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "test-1" {
		t.Errorf("expected dependency test-2 -> test-1, got %v", deps)
	}

	// Verify labels
	labels, err := store2.GetLabels(ctx, "test-1")
	if err != nil {
		t.Fatalf("failed to get labels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "bug" {
		t.Errorf("expected label 'bug', got %v", labels)
	}
}
