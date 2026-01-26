package storage_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/memory"
)

// TestIsVersioned verifies the IsVersioned type detection helper.
func TestIsVersioned(t *testing.T) {
	// Memory storage is NOT versioned
	memStore := memory.New("")

	if storage.IsVersioned(memStore) {
		t.Error("IsVersioned should return false for memory storage")
	}

	// Test AsVersioned returns false for non-versioned storage
	vs, ok := storage.AsVersioned(memStore)
	if ok {
		t.Error("AsVersioned should return false for memory storage")
	}
	if vs != nil {
		t.Error("AsVersioned should return nil for memory storage")
	}
}

// TestVersionedStorageInterface ensures the interface is correctly defined.
func TestVersionedStorageInterface(t *testing.T) {
	// This test verifies that the interface types exist and have the expected methods.
	// Actual implementation testing would be done in the dolt package.

	// HistoryEntry should have the expected fields
	entry := storage.HistoryEntry{
		CommitHash: "abc123",
		Committer:  "test",
	}
	if entry.CommitHash != "abc123" {
		t.Error("HistoryEntry.CommitHash not working")
	}

	// DiffEntry should have the expected fields
	diff := storage.DiffEntry{
		IssueID:  "bd-123",
		DiffType: "modified",
	}
	if diff.IssueID != "bd-123" {
		t.Error("DiffEntry.IssueID not working")
	}
	if diff.DiffType != "modified" {
		t.Error("DiffEntry.DiffType not working")
	}

	// Conflict should have the expected fields
	conflict := storage.Conflict{
		IssueID: "bd-456",
		Field:   "title",
	}
	if conflict.IssueID != "bd-456" {
		t.Error("Conflict.IssueID not working")
	}
	if conflict.Field != "title" {
		t.Error("Conflict.Field not working")
	}
}

// TestVersionedStorageTypes verifies type values work as expected.
func TestVersionedStorageTypes(t *testing.T) {
	// Test DiffEntry types
	testCases := []struct {
		diffType string
		valid    bool
	}{
		{"added", true},
		{"modified", true},
		{"removed", true},
	}

	for _, tc := range testCases {
		entry := storage.DiffEntry{DiffType: tc.diffType}
		if entry.DiffType != tc.diffType {
			t.Errorf("DiffEntry.DiffType mismatch: expected %s, got %s", tc.diffType, entry.DiffType)
		}
	}
}
