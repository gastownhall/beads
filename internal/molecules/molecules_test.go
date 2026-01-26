package molecules

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

func TestLoadMoleculesFromFile(t *testing.T) {
	// Create a temporary directory
	tempDir := t.TempDir()

	// Create a test molecules.jsonl file
	moleculesPath := filepath.Join(tempDir, "molecules.jsonl")
	content := `{"id":"mol-test-1","title":"Test Molecule 1","issue_type":"molecule","status":"open"}
{"id":"mol-test-2","title":"Test Molecule 2","issue_type":"molecule","status":"open"}`
	if err := os.WriteFile(moleculesPath, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Load molecules
	molecules, err := loadMoleculesFromFile(moleculesPath)
	if err != nil {
		t.Fatalf("Failed to load molecules: %v", err)
	}

	if len(molecules) != 2 {
		t.Errorf("Expected 2 molecules, got %d", len(molecules))
	}

	// Check that IsTemplate is set
	for _, mol := range molecules {
		if !mol.IsTemplate {
			t.Errorf("Molecule %s should have IsTemplate=true", mol.ID)
		}
	}

	// Check specific fields
	if molecules[0].ID != "mol-test-1" {
		t.Errorf("Expected ID 'mol-test-1', got '%s'", molecules[0].ID)
	}
	if molecules[0].Title != "Test Molecule 1" {
		t.Errorf("Expected Title 'Test Molecule 1', got '%s'", molecules[0].Title)
	}
}

func TestLoadMoleculesFromNonexistentFile(t *testing.T) {
	molecules, err := loadMoleculesFromFile("/nonexistent/path/molecules.jsonl")
	if err != nil {
		t.Errorf("Expected nil error for nonexistent file, got: %v", err)
	}
	if molecules != nil {
		t.Errorf("Expected nil molecules for nonexistent file, got: %v", molecules)
	}
}

func TestLoader_LoadAll(t *testing.T) {
	ctx := context.Background()

	// Create temporary directories
	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	// Create a test database
	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set issue prefix (required by storage)
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Configure custom types for Gas Town types (bd-find4)
	if err := store.SetConfig(ctx, "types.custom", "molecule"); err != nil {
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	// Create a project-level molecules.jsonl
	moleculesPath := filepath.Join(beadsDir, "molecules.jsonl")
	content := `{"id":"mol-feature","title":"Feature Template","issue_type":"molecule","status":"open","description":"Standard feature workflow"}
{"id":"mol-bugfix","title":"Bugfix Template","issue_type":"molecule","status":"open","description":"Bug fix workflow"}`
	if err := os.WriteFile(moleculesPath, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write molecules file: %v", err)
	}

	// Load molecules
	loader := NewLoader(store)
	result, err := loader.LoadAll(ctx, beadsDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}

	if result.Loaded != 2 {
		t.Errorf("Expected 2 loaded molecules, got %d", result.Loaded)
	}

	// Verify molecules are in the database
	mol1, err := store.GetIssue(ctx, "mol-feature")
	if err != nil {
		t.Fatalf("Failed to get mol-feature: %v", err)
	}
	if mol1 == nil {
		t.Fatal("mol-feature not found in database")
	}
	if !mol1.IsTemplate {
		t.Error("mol-feature should be marked as template")
	}
	if mol1.Title != "Feature Template" {
		t.Errorf("Expected title 'Feature Template', got '%s'", mol1.Title)
	}

	mol2, err := store.GetIssue(ctx, "mol-bugfix")
	if err != nil {
		t.Fatalf("Failed to get mol-bugfix: %v", err)
	}
	if mol2 == nil {
		t.Fatal("mol-bugfix not found in database")
	}
	if !mol2.IsTemplate {
		t.Error("mol-bugfix should be marked as template")
	}
}

func TestLoader_SkipExistingMolecules(t *testing.T) {
	ctx := context.Background()

	// Create temporary directories
	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	// Create a test database
	dbPath := filepath.Join(beadsDir, "test.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Set issue prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Configure custom types for Gas Town types (bd-find4)
	if err := store.SetConfig(ctx, "types.custom", "molecule"); err != nil {
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	// Pre-create a molecule in the database (skip prefix validation for mol-* IDs)
	existingMol := &types.Issue{
		ID:         "mol-existing",
		Title:      "Existing Molecule",
		IssueType:  "molecule",
		Status:     types.StatusOpen,
		IsTemplate: true,
	}
	opts := sqlite.BatchCreateOptions{SkipPrefixValidation: true}
	if err := store.CreateIssuesWithFullOptions(ctx, []*types.Issue{existingMol}, "test", opts); err != nil {
		t.Fatalf("Failed to create existing molecule: %v", err)
	}

	// Create a molecules.jsonl with the same ID
	moleculesPath := filepath.Join(beadsDir, "molecules.jsonl")
	content := `{"id":"mol-existing","title":"Updated Molecule","issue_type":"molecule","status":"open"}
{"id":"mol-new","title":"New Molecule","issue_type":"molecule","status":"open"}`
	if err := os.WriteFile(moleculesPath, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write molecules file: %v", err)
	}

	// Load molecules
	loader := NewLoader(store)
	result, err := loader.LoadAll(ctx, beadsDir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}

	// Should only load the new one (existing one is skipped)
	if result.Loaded != 1 {
		t.Errorf("Expected 1 loaded molecule, got %d", result.Loaded)
	}

	// Verify the existing molecule wasn't updated
	mol, err := store.GetIssue(ctx, "mol-existing")
	if err != nil {
		t.Fatalf("Failed to get mol-existing: %v", err)
	}
	if mol.Title != "Existing Molecule" {
		t.Errorf("Expected title 'Existing Molecule' (unchanged), got '%s'", mol.Title)
	}
}

func TestGetBuiltinMolecules(t *testing.T) {
	molecules := getBuiltinMolecules()
	// For now, we expect no built-in molecules (can be added later)
	if molecules == nil {
		// This is expected for now
		return
	}
	// When built-in molecules are added, verify they all have IsTemplate=true
	for _, mol := range molecules {
		if !mol.IsTemplate {
			t.Errorf("Built-in molecule %s should have IsTemplate=true", mol.ID)
		}
	}
}
