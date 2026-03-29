//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Load from empty dir returns zero state
	state, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState: %v", err)
	}
	if state.LastDoltCommit != "" {
		t.Errorf("expected empty commit, got %q", state.LastDoltCommit)
	}

	// Save and reload
	state.LastDoltCommit = "abc123"
	state.Timestamp = time.Date(2026, 2, 26, 12, 0, 0, 0, time.UTC)

	if err := saveBackupState(dir, state); err != nil {
		t.Fatalf("saveBackupState: %v", err)
	}

	loaded, err := loadBackupState(dir)
	if err != nil {
		t.Fatalf("loadBackupState after save: %v", err)
	}
	if loaded.LastDoltCommit != "abc123" {
		t.Errorf("commit = %q, want abc123", loaded.LastDoltCommit)
	}
}

func TestBackupAtomicWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	data := []byte(`{"id":"test-1","title":"hello"}` + "\n")
	if err := atomicWriteFile(path, data); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

// splitJSONL splits JSONL data into individual JSON lines, skipping empty lines.
func splitJSONL(data []byte) []json.RawMessage {
	var result []json.RawMessage
	for _, line := range splitLines(data) {
		if len(line) > 0 {
			result = append(result, json.RawMessage(line))
		}
	}
	return result
}

// splitLines splits data into lines without importing strings.
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
