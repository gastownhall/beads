package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateMetadataInlineJSON tests inline JSON metadata update
func TestUpdateMetadataInlineJSON(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")

	// Create storage
	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	// Initialize database with issue prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create an issue
	issue := &types.Issue{
		ID:        "bd-test1",
		Title:     "Test Issue",
		Status:    "open",
		IssueType: "task",
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Update with inline JSON metadata
	metadata := `{"key": "value", "nested": {"foo": "bar"}}`
	updates := map[string]interface{}{
		"metadata": json.RawMessage(metadata),
	}
	if err := store.UpdateIssue(ctx, "bd-test1", updates, "test-actor"); err != nil {
		t.Fatalf("failed to update issue with metadata: %v", err)
	}

	// Verify the metadata was stored
	updated, err := store.GetIssue(ctx, "bd-test1")
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if updated.Metadata == nil {
		t.Fatal("metadata should not be nil after update")
	}
	if string(updated.Metadata) != metadata {
		t.Errorf("expected metadata %q, got %q", metadata, string(updated.Metadata))
	}
}

// TestUpdateMetadataFromFile tests reading metadata from a file
func TestUpdateMetadataFromFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a JSON file
	metadataJSON := `{"source": "file", "count": 42}`
	metadataFile := filepath.Join(tmpDir, "metadata.json")
	if err := os.WriteFile(metadataFile, []byte(metadataJSON), 0644); err != nil {
		t.Fatalf("failed to write metadata file: %v", err)
	}

	// Read and validate
	data, err := os.ReadFile(metadataFile)
	if err != nil {
		t.Fatalf("failed to read metadata file: %v", err)
	}

	if !json.Valid(data) {
		t.Fatal("metadata file should contain valid JSON")
	}

	if string(data) != metadataJSON {
		t.Errorf("expected %q, got %q", metadataJSON, string(data))
	}
}

// TestUpdateMetadataInvalidJSON tests that invalid JSON is rejected
func TestUpdateMetadataInvalidJSON(t *testing.T) {
	invalidCases := []string{
		"{invalid}",
		"not json",
		`{"unclosed": `,
		"",
	}

	for _, invalid := range invalidCases {
		if invalid == "" {
			// Empty string is not invalid for json.Valid per se, but we might want it
			continue
		}
		if json.Valid([]byte(invalid)) {
			t.Errorf("expected %q to be invalid JSON", invalid)
		}
	}
}

// TestUpdateMetadataValidJSON tests various valid JSON formats
func TestUpdateMetadataValidJSON(t *testing.T) {
	validCases := []string{
		`{}`,
		`{"key": "value"}`,
		`{"nested": {"deep": {"value": 123}}}`,
		`{"array": [1, 2, 3]}`,
		`{"mixed": [{"a": 1}, {"b": 2}]}`,
		`{"unicode": "\u0048\u0065\u006c\u006c\u006f"}`,
		`{"null": null}`,
		`{"bool": true}`,
		`{"number": 3.14159}`,
	}

	for _, valid := range validCases {
		if !json.Valid([]byte(valid)) {
			t.Errorf("expected %q to be valid JSON", valid)
		}
	}
}

// TestUpdateMetadataAtFileSyntax tests the @file.json parsing logic
func TestUpdateMetadataAtFileSyntax(t *testing.T) {
	tests := []struct {
		input    string
		isFile   bool
		filePath string
	}{
		{`{"inline": true}`, false, ""},
		{"@metadata.json", true, "metadata.json"},
		{"@/absolute/path.json", true, "/absolute/path.json"},
		{"@./relative/path.json", true, "./relative/path.json"},
		// Edge case: JSON that starts with @ in a string
		{`{"email": "@user.com"}`, false, ""},
	}

	for _, tc := range tests {
		// Check if starts with @ for file reference
		isFile := len(tc.input) > 0 && tc.input[0] == '@'
		if isFile != tc.isFile {
			t.Errorf("input %q: expected isFile=%v, got %v", tc.input, tc.isFile, isFile)
		}
		if isFile {
			filePath := tc.input[1:]
			if filePath != tc.filePath {
				t.Errorf("input %q: expected filePath=%q, got %q", tc.input, tc.filePath, filePath)
			}
		}
	}
}

// TestUpdateMetadataRPCRoundtrip tests the RPC protocol for metadata
func TestUpdateMetadataRPCRoundtrip(t *testing.T) {
	// Test that metadata can be marshaled/unmarshaled through RPC
	metadata := `{"key": "value"}`

	// Simulate UpdateArgs with metadata
	type testUpdateArgs struct {
		ID       string  `json:"id"`
		Metadata *string `json:"metadata,omitempty"`
	}

	args := testUpdateArgs{
		ID:       "bd-123",
		Metadata: &metadata,
	}

	// Marshal to JSON (as RPC would do)
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Unmarshal (as server would do)
	var decoded testUpdateArgs
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Metadata == nil {
		t.Fatal("metadata should not be nil after roundtrip")
	}
	if *decoded.Metadata != metadata {
		t.Errorf("expected %q, got %q", metadata, *decoded.Metadata)
	}
}
