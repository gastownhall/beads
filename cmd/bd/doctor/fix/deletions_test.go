package fix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetCurrentJSONLIDs_IncludesTombstones verifies that tombstones ARE included
// in the current ID set. This is critical for bd-552 fix: tombstones represent
// migrated deletions that are accounted for. By including them, they won't appear
// "missing" when compared to historicalIDs, preventing erroneous re-addition to
// deletions.jsonl.
func TestGetCurrentJSONLIDs_IncludesTombstones(t *testing.T) {
	// Setup: Create temp file with mix of normal issues and tombstones
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create a JSONL file with both normal issues and tombstones
	issues := []*types.Issue{
		{
			ID:     "bd-abc",
			Title:  "Normal issue",
			Status: types.StatusOpen,
		},
		{
			ID:        "bd-def",
			Title:     "(deleted)",
			Status:    types.StatusTombstone,
			DeletedBy: "test-user",
		},
		{
			ID:     "bd-ghi",
			Title:  "Another normal issue",
			Status: types.StatusOpen,
		},
		{
			ID:        "bd-jkl",
			Title:     "(deleted)",
			Status:    types.StatusTombstone,
			DeletedBy: "test-user",
		},
	}

	file, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create test JSONL file: %v", err)
	}
	encoder := json.NewEncoder(file)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			_ = file.Close()
			t.Fatalf("Failed to write issue to JSONL: %v", err)
		}
	}
	_ = file.Close()

	// Call getCurrentJSONLIDs
	ids, err := getCurrentJSONLIDs(jsonlPath)
	if err != nil {
		t.Fatalf("getCurrentJSONLIDs failed: %v", err)
	}

	// Verify: Should contain ALL IDs including tombstones (bd-552 fix)
	expectedIDs := map[string]bool{
		"bd-abc": true,
		"bd-def": true, // tombstone - must be included
		"bd-ghi": true,
		"bd-jkl": true, // tombstone - must be included
	}

	if len(ids) != len(expectedIDs) {
		t.Errorf("Expected %d IDs, got %d. IDs: %v", len(expectedIDs), len(ids), ids)
	}

	for expectedID := range expectedIDs {
		if !ids[expectedID] {
			t.Errorf("Expected ID %s to be present", expectedID)
		}
	}

	// Verify tombstones ARE included (this is the bd-552 fix)
	if !ids["bd-def"] {
		t.Error("Tombstone bd-def MUST be included in current IDs (bd-552 fix)")
	}
	if !ids["bd-jkl"] {
		t.Error("Tombstone bd-jkl MUST be included in current IDs (bd-552 fix)")
	}
}

func TestGetCurrentJSONLIDs_HandlesEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Create empty file
	if _, err := os.Create(jsonlPath); err != nil {
		t.Fatalf("Failed to create empty file: %v", err)
	}

	ids, err := getCurrentJSONLIDs(jsonlPath)
	if err != nil {
		t.Fatalf("getCurrentJSONLIDs failed: %v", err)
	}

	if len(ids) != 0 {
		t.Errorf("Expected 0 IDs from empty file, got %d", len(ids))
	}
}

func TestGetCurrentJSONLIDs_HandlesMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	nonexistentPath := filepath.Join(tmpDir, "nonexistent.jsonl")

	ids, err := getCurrentJSONLIDs(nonexistentPath)
	if err != nil {
		t.Fatalf("getCurrentJSONLIDs should handle missing file gracefully: %v", err)
	}

	if len(ids) != 0 {
		t.Errorf("Expected 0 IDs from missing file, got %d", len(ids))
	}
}

func TestGetCurrentJSONLIDs_SkipsInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Write mixed valid and invalid JSON lines
	content := `{"id":"bd-valid","status":"open"}
invalid json line
{"id":"bd-another","status":"open"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	ids, err := getCurrentJSONLIDs(jsonlPath)
	if err != nil {
		t.Fatalf("getCurrentJSONLIDs failed: %v", err)
	}

	if len(ids) != 2 {
		t.Errorf("Expected 2 valid IDs, got %d. IDs: %v", len(ids), ids)
	}
	if !ids["bd-valid"] || !ids["bd-another"] {
		t.Error("Expected to parse both valid issues despite invalid line in between")
	}
}

// Note: Full integration test for HydrateDeletionsManifest would require git repo setup.
// The unit tests above verify the core fix (bd-552: including tombstones in getCurrentJSONLIDs
// so they aren't erroneously re-added to deletions.jsonl).
// Integration tests are handled in migrate_tombstones_test.go with full sync cycle.
