package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func TestOutputJSON(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Test data
	testData := map[string]interface{}{
		"id":    "bd-1",
		"title": "Test Issue",
		"count": 42,
	}

	// Call outputJSON
	outputJSON(testData)

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout

	// Read output
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify it's valid JSON
	var result map[string]interface{}
	err := json.Unmarshal([]byte(output), &result)
	if err != nil {
		t.Fatalf("outputJSON did not produce valid JSON: %v", err)
	}

	// Verify content
	if result["id"] != "bd-1" {
		t.Errorf("Expected id 'bd-1', got '%v'", result["id"])
	}
	if result["title"] != "Test Issue" {
		t.Errorf("Expected title 'Test Issue', got '%v'", result["title"])
	}
	// Note: JSON numbers are float64
	if result["count"] != float64(42) {
		t.Errorf("Expected count 42, got %v", result["count"])
	}
}

func TestOutputJSONArray(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Test data - array of issues
	testData := []map[string]string{
		{"id": "bd-1", "title": "First"},
		{"id": "bd-2", "title": "Second"},
	}

	// Call outputJSON
	outputJSON(testData)

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout

	// Read output
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Verify it's valid JSON array
	var result []map[string]string
	err := json.Unmarshal([]byte(output), &result)
	if err != nil {
		t.Fatalf("outputJSON did not produce valid JSON array: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(result))
	}
}

// Tests for printCollisionReport and printRemappingReport were removed
// These functions no longer exist after refactoring to shared importIssuesCore (bd-157)

// Note: createIssuesFromMarkdown is tested via cmd/bd/markdown_test.go which has
// comprehensive tests for the markdown parsing functionality. We don't duplicate
// those tests here since they require full DB setup.
