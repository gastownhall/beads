package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/deletions"
	"github.com/steveyegge/beads/internal/types"
)

func TestMigrateTombstones_NoDeletions(t *testing.T) {
	// Setup: create temp .beads directory with no deletions.jsonl
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create empty issues.jsonl
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte{}, 0600); err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}

	// Run in temp dir
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

	// The command should report no deletions to migrate
	deletionsPath := deletions.DefaultPath(beadsDir)
	loadResult, err := deletions.LoadDeletions(deletionsPath)
	if err != nil {
		t.Fatalf("LoadDeletions failed: %v", err)
	}

	if len(loadResult.Records) != 0 {
		t.Errorf("Expected 0 deletions, got %d", len(loadResult.Records))
	}
}

func TestMigrateTombstones_WithDeletions(t *testing.T) {
	// Setup: create temp .beads directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create deletions.jsonl with some entries
	deletionsPath := deletions.DefaultPath(beadsDir)
	deleteTime := time.Now().Add(-24 * time.Hour)

	records := []deletions.DeletionRecord{
		{ID: "test-abc", Timestamp: deleteTime, Actor: "alice", Reason: "duplicate"},
		{ID: "test-def", Timestamp: deleteTime.Add(-1 * time.Hour), Actor: "bob", Reason: "obsolete"},
	}

	for _, record := range records {
		if err := deletions.AppendDeletion(deletionsPath, record); err != nil {
			t.Fatalf("Failed to write deletion: %v", err)
		}
	}

	// Create empty issues.jsonl
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte{}, 0600); err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}

	// Load deletions
	loadResult, err := deletions.LoadDeletions(deletionsPath)
	if err != nil {
		t.Fatalf("LoadDeletions failed: %v", err)
	}

	if len(loadResult.Records) != 2 {
		t.Fatalf("Expected 2 deletions, got %d", len(loadResult.Records))
	}

	// Simulate migration by converting to tombstones
	var tombstones []*types.Issue
	for _, record := range loadResult.Records {
		tombstones = append(tombstones, convertDeletionRecordToTombstone(record))
	}

	// Verify tombstone fields
	for _, ts := range tombstones {
		if ts.Status != types.StatusTombstone {
			t.Errorf("Expected status tombstone, got %s", ts.Status)
		}
		if ts.DeletedAt == nil {
			t.Error("Expected DeletedAt to be set")
		}
		if ts.DeletedBy == "" {
			t.Error("Expected DeletedBy to be set")
		}
	}
}

func TestMigrateTombstones_SkipsExistingTombstones(t *testing.T) {
	// Setup: create temp .beads directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create deletions.jsonl with some entries
	deletionsPath := deletions.DefaultPath(beadsDir)
	deleteTime := time.Now().Add(-24 * time.Hour)

	records := []deletions.DeletionRecord{
		{ID: "test-abc", Timestamp: deleteTime, Actor: "alice", Reason: "duplicate"},
		{ID: "test-def", Timestamp: deleteTime.Add(-1 * time.Hour), Actor: "bob", Reason: "obsolete"},
	}

	for _, record := range records {
		if err := deletions.AppendDeletion(deletionsPath, record); err != nil {
			t.Fatalf("Failed to write deletion: %v", err)
		}
	}

	// Create issues.jsonl with an existing tombstone for test-abc
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	existingTombstone := types.Issue{
		ID:        "test-abc",
		Title:     "(deleted)",
		Status:    types.StatusTombstone,
		DeletedBy: "alice",
	}

	file, err := os.Create(issuesPath)
	if err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(existingTombstone); err != nil {
		file.Close()
		t.Fatalf("Failed to write existing tombstone: %v", err)
	}
	file.Close()

	// Load existing tombstones
	existingTombstones := make(map[string]bool)
	file, _ = os.Open(issuesPath)
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			break
		}
		if issue.IsTombstone() {
			existingTombstones[issue.ID] = true
		}
	}
	file.Close()

	// Load deletions
	loadResult, err := deletions.LoadDeletions(deletionsPath)
	if err != nil {
		t.Fatalf("LoadDeletions failed: %v", err)
	}

	// Count what should be migrated vs skipped
	var toMigrate, skipped int
	for id := range loadResult.Records {
		if existingTombstones[id] {
			skipped++
		} else {
			toMigrate++
		}
	}

	if toMigrate != 1 {
		t.Errorf("Expected 1 to migrate, got %d", toMigrate)
	}
	if skipped != 1 {
		t.Errorf("Expected 1 skipped, got %d", skipped)
	}
}

func TestConvertDeletionRecordToTombstone(t *testing.T) {
	deleteTime := time.Now().Add(-24 * time.Hour)
	record := deletions.DeletionRecord{
		ID:        "test-xyz",
		Timestamp: deleteTime,
		Actor:     "alice",
		Reason:    "test reason",
	}

	tombstone := convertDeletionRecordToTombstone(record)

	if tombstone.ID != "test-xyz" {
		t.Errorf("Expected ID test-xyz, got %s", tombstone.ID)
	}
	if tombstone.Status != types.StatusTombstone {
		t.Errorf("Expected status tombstone, got %s", tombstone.Status)
	}
	if tombstone.Title != "(deleted)" {
		t.Errorf("Expected title '(deleted)', got %s", tombstone.Title)
	}
	if tombstone.DeletedBy != "alice" {
		t.Errorf("Expected DeletedBy 'alice', got %s", tombstone.DeletedBy)
	}
	if tombstone.DeleteReason != "test reason" {
		t.Errorf("Expected DeleteReason 'test reason', got %s", tombstone.DeleteReason)
	}
	if tombstone.DeletedAt == nil {
		t.Error("Expected DeletedAt to be set")
	} else if !tombstone.DeletedAt.Equal(deleteTime) {
		t.Errorf("Expected DeletedAt %v, got %v", deleteTime, *tombstone.DeletedAt)
	}
	if tombstone.Priority != 0 {
		t.Errorf("Expected priority 0 (unknown), got %d", tombstone.Priority)
	}
	if tombstone.IssueType != types.TypeTask {
		t.Errorf("Expected type task, got %s", tombstone.IssueType)
	}
	if tombstone.OriginalType != "" {
		t.Errorf("Expected empty OriginalType, got %s", tombstone.OriginalType)
	}
}
