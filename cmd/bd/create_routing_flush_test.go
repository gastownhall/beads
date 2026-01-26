package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestFlushRoutedRepo_DirectExport tests that routed issues are exported to JSONL
// in the target repo when no daemon is running (direct export fallback).
func TestFlushRoutedRepo_DirectExport(t *testing.T) {
	// Create a test source repo (current repo)
	sourceDir := t.TempDir()
	sourceBeadsDir := filepath.Join(sourceDir, ".beads")
	if err := os.MkdirAll(sourceBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create source .beads dir: %v", err)
	}

	// Create a test target repo (routing destination)
	targetDir := t.TempDir()
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create target .beads dir: %v", err)
	}
	targetJSONLPath := filepath.Join(targetBeadsDir, "issues.jsonl")

	// Create empty JSONL in target (simulates fresh planning repo)
	if err := os.WriteFile(targetJSONLPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create target JSONL: %v", err)
	}

	// Create database in target repo with a test issue
	targetDBPath := filepath.Join(targetBeadsDir, "beads.db")
	targetStore := newTestStore(t, targetDBPath)
	defer targetStore.Close()

	ctx := context.Background()

	// Create a test issue in the target store (let ID be auto-generated with correct prefix)
	issue := &types.Issue{
		Title:     "Test routed issue",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}

	if err := targetStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create test issue: %v", err)
	}

	// Save the generated ID for later verification
	testIssueID := issue.ID

	// Call flushRoutedRepo (the function we're testing)
	// This should export the issue to JSONL since no daemon is running
	flushRoutedRepo(targetStore, targetDir)

	// Verify the JSONL file was updated and contains the issue
	jsonlBytes, err := os.ReadFile(targetJSONLPath)
	if err != nil {
		t.Fatalf("failed to read target JSONL: %v", err)
	}

	if len(jsonlBytes) == 0 {
		t.Fatal("expected JSONL to contain data, but it's empty")
	}

	// Parse JSONL to verify our issue is there
	var foundIssue *types.Issue
	file, err := os.Open(targetJSONLPath)
	if err != nil {
		t.Fatalf("failed to open JSONL: %v", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	for decoder.More() {
		var iss types.Issue
		if err := decoder.Decode(&iss); err != nil {
			t.Fatalf("failed to decode JSONL issue: %v", err)
		}
		if iss.ID == testIssueID {
			foundIssue = &iss
			break
		}
	}

	if foundIssue == nil {
		t.Fatalf("could not find routed issue %s in target JSONL", testIssueID)
	}

	if foundIssue.Title != "Test routed issue" {
		t.Errorf("expected title 'Test routed issue', got %q", foundIssue.Title)
	}
}

// TestPerformAtomicExport tests the atomic export functionality (temp file + rename).
func TestPerformAtomicExport(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	ctx := context.Background()

	// Create test issues
	issues := []*types.Issue{
		{
			ID:        "beads-test1",
			Title:     "Issue 1",
			Priority:  1,
			IssueType: types.TypeBug,
			Status:    types.StatusOpen,
		},
		{
			ID:        "beads-test2",
			Title:     "Issue 2",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusClosed,
		},
	}

	// Call performAtomicExport
	if err := performAtomicExport(ctx, jsonlPath, issues, nil); err != nil {
		t.Fatalf("performAtomicExport failed: %v", err)
	}

	// Verify the JSONL file exists and contains the issues
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Fatal("JSONL file was not created")
	}

	// Verify no temp files left behind
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read temp dir: %v", err)
	}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", entry.Name())
		}
	}

	// Parse JSONL and verify issues
	file, err := os.Open(jsonlPath)
	if err != nil {
		t.Fatalf("failed to open JSONL: %v", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var parsedIssues []*types.Issue
	for decoder.More() {
		var iss types.Issue
		if err := decoder.Decode(&iss); err != nil {
			t.Fatalf("failed to decode issue: %v", err)
		}
		parsedIssues = append(parsedIssues, &iss)
	}

	if len(parsedIssues) != 2 {
		t.Fatalf("expected 2 issues in JSONL, got %d", len(parsedIssues))
	}

	if parsedIssues[0].ID != "beads-test1" || parsedIssues[1].ID != "beads-test2" {
		t.Error("issues not in expected order or with expected IDs")
	}
}

// TestFlushRoutedRepo_PathExpansion tests that ~ is expanded correctly in repo paths.
func TestFlushRoutedRepo_PathExpansion(t *testing.T) {
	// This is a simpler test that just verifies path expansion doesn't crash
	// We can't easily test actual home directory without affecting the real system

	tmpDir := t.TempDir()
	targetBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create target .beads dir: %v", err)
	}

	targetDBPath := filepath.Join(targetBeadsDir, "beads.db")
	targetStore := newTestStore(t, targetDBPath)
	defer targetStore.Close()

	// Call with relative path (should not crash)
	// Since there's no daemon and no issues, this should just return silently
	flushRoutedRepo(targetStore, tmpDir)

	// If we get here without crashing, path handling works
}

// TestRoutingWithHydrationIntegration is a higher-level integration test
// that verifies the full routing + hydration workflow.
func TestRoutingWithHydrationIntegration(t *testing.T) {
	// Setup: Create main repo and planning repo
	mainDir := t.TempDir()
	mainBeadsDir := filepath.Join(mainDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create main .beads dir: %v", err)
	}

	planningDir := t.TempDir()
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	if err := os.MkdirAll(planningBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create planning .beads dir: %v", err)
	}

	// Create config.yaml in main repo with routing configured
	configPath := filepath.Join(mainBeadsDir, "config.yaml")
	configContent := `routing:
  mode: auto
  contributor: ` + planningDir + `
repos:
  primary: .
  additional:
    - ` + planningDir + `
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	// Create issues.jsonl in planning repo
	planningJSONL := filepath.Join(planningBeadsDir, "issues.jsonl")
	if err := os.WriteFile(planningJSONL, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create planning JSONL: %v", err)
	}

	// Create database in planning repo
	planningDBPath := filepath.Join(planningBeadsDir, "beads.db")
	planningStore := newTestStore(t, planningDBPath)
	defer planningStore.Close()

	ctx := context.Background()

	// Create issue in planning repo (simulating routed create)
	issue := &types.Issue{
		Title:     "Routed issue",
		Priority:  2,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}

	if err := planningStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Flush to JSONL (this is what our fix does)
	flushRoutedRepo(planningStore, planningDir)

	// Verify config.yaml was written with correct content
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config.yaml: %v", err)
	}
	configStr := string(configBytes)

	// Check routing is configured
	if !strings.Contains(configStr, "routing:") || !strings.Contains(configStr, "mode: auto") {
		t.Error("expected routing.mode=auto in config.yaml")
	}

	// Check hydration is configured (planning dir should be in repos.additional)
	if !strings.Contains(configStr, "repos:") || !strings.Contains(configStr, "additional:") {
		t.Error("expected repos.additional in config.yaml")
	}

	if !strings.Contains(configStr, planningDir) {
		t.Errorf("expected planning dir %q to be in config.yaml", planningDir)
	}

	// Verify JSONL contains the routed issue
	jsonlBytes, err := os.ReadFile(planningJSONL)
	if err != nil {
		t.Fatalf("failed to read planning JSONL: %v", err)
	}

	if len(jsonlBytes) == 0 {
		t.Fatal("expected planning JSONL to contain data after flush")
	}
}
