package fix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/deletions"
	"github.com/steveyegge/beads/internal/types"
)

func TestMigrateTombstones(t *testing.T) {
	// Setup: create temp .beads directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create an issue in issues.jsonl
	issue := &types.Issue{
		ID:        "test-abc",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	issueData, _ := json.Marshal(issue)
	if err := os.WriteFile(jsonlPath, append(issueData, '\n'), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Create deletions.jsonl with one entry
	record := deletions.DeletionRecord{
		ID:        "test-deleted",
		Timestamp: time.Now().Add(-time.Hour),
		Actor:     "testuser",
		Reason:    "test deletion",
	}
	if err := deletions.AppendDeletion(deletionsPath, record); err != nil {
		t.Fatalf("failed to create deletions.jsonl: %v", err)
	}

	// Run migration
	err := MigrateTombstones(tmpDir)
	if err != nil {
		t.Fatalf("MigrateTombstones failed: %v", err)
	}

	// Verify deletions.jsonl was archived
	if _, err := os.Stat(deletionsPath); !os.IsNotExist(err) {
		t.Error("deletions.jsonl should have been archived")
	}
	if _, err := os.Stat(deletionsPath + ".migrated"); os.IsNotExist(err) {
		t.Error("deletions.jsonl.migrated should exist")
	}

	// Verify tombstone was added to issues.jsonl
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read issues.jsonl: %v", err)
	}

	// Should have 2 lines now (original issue + tombstone)
	lines := 0
	var foundTombstone bool
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		lines++
		var iss struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(line, &iss); err == nil {
			if iss.ID == "test-deleted" && iss.Status == string(types.StatusTombstone) {
				foundTombstone = true
			}
		}
	}

	if lines != 2 {
		t.Errorf("expected 2 lines in issues.jsonl, got %d", lines)
	}
	if !foundTombstone {
		t.Error("tombstone for test-deleted not found in issues.jsonl")
	}
}

func TestMigrateTombstones_SkipsExisting(t *testing.T) {
	// Setup: create temp .beads directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create issues.jsonl with an existing tombstone
	tombstone := &types.Issue{
		ID:        "test-already-tombstone",
		Title:     "[Deleted]",
		Status:    types.StatusTombstone,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	tombstoneData, _ := json.Marshal(tombstone)
	if err := os.WriteFile(jsonlPath, append(tombstoneData, '\n'), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Create deletions.jsonl with the same ID
	record := deletions.DeletionRecord{
		ID:        "test-already-tombstone",
		Timestamp: time.Now().Add(-time.Hour),
		Actor:     "testuser",
		Reason:    "test deletion",
	}
	if err := deletions.AppendDeletion(deletionsPath, record); err != nil {
		t.Fatalf("failed to create deletions.jsonl: %v", err)
	}

	// Run migration
	err := MigrateTombstones(tmpDir)
	if err != nil {
		t.Fatalf("MigrateTombstones failed: %v", err)
	}

	// Verify issues.jsonl still has only 1 line (no duplicate tombstone)
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read issues.jsonl: %v", err)
	}

	lines := 0
	for _, line := range splitLines(data) {
		if len(line) > 0 {
			lines++
		}
	}

	if lines != 1 {
		t.Errorf("expected 1 line in issues.jsonl (existing tombstone), got %d", lines)
	}
}

func TestMigrateTombstones_NoDeletionsFile(t *testing.T) {
	// Setup: create temp .beads directory without deletions.jsonl
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Run migration - should succeed without error
	err := MigrateTombstones(tmpDir)
	if err != nil {
		t.Fatalf("MigrateTombstones failed: %v", err)
	}
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
