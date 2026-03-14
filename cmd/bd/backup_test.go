//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/beads/internal/testutil"
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
	state.LastEventID = 42
	state.Timestamp = time.Date(2026, 2, 26, 12, 0, 0, 0, time.UTC)
	state.Counts.Issues = 10
	state.Counts.Events = 100

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
	if loaded.LastEventID != 42 {
		t.Errorf("event ID = %d, want 42", loaded.LastEventID)
	}
	if loaded.Counts.Issues != 10 {
		t.Errorf("issues = %d, want 10", loaded.Counts.Issues)
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

func TestBackupNormalizeValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   interface{}
		want interface{}
	}{
		{"nil", nil, nil},
		{"string bytes", []byte("hello"), "hello"},
		{"int", int64(42), int64(42)},
		{"zero time", time.Time{}, nil},
		{"time", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "2026-01-01T00:00:00Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeValue(tt.in)
			if got != tt.want {
				t.Errorf("normalizeValue(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestBackupExport(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)
	saved := saveAndRestoreGlobals(t)
	_ = saved

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Set up working directory so beads.FindBeadsDir() works
	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	// Create test store with some data
	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(beadsDir, "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	s := newTestStore(t, testDBPath)
	store = s
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		store = nil
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	ctx := context.Background()

	// Create some test issues
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"test-1", "Test Issue 1", "desc1", "", "", "", "open", 2, "task"); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"test-2", "Test Issue 2", "desc2", "", "", "", "done", 1, "bug"); err != nil {
		t.Fatalf("insert issue: %v", err)
	}

	// Create a label
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO labels (issue_id, label) VALUES (?, ?)`,
		"test-1", "backend"); err != nil {
		t.Fatalf("insert label: %v", err)
	}

	// Create an event
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO events (issue_id, event_type, actor) VALUES (?, ?, ?)`,
		"test-1", "created", "tester"); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	depMetadata := `{"gate":"any-children","spawner_id":"test-1"}`
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, metadata) VALUES (?, ?, ?, ?, ?)`,
		"test-2", "test-1", "blocks", "tester", depMetadata); err != nil {
		t.Fatalf("insert dependency: %v", err)
	}

	// Commit so GetCurrentCommit returns something
	if _, err := s.DB().ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'test data')"); err != nil {
		t.Fatalf("dolt commit: %v", err)
	}

	// Run backup
	state, err := runBackupExport(ctx, false)
	if err != nil {
		t.Fatalf("runBackupExport: %v", err)
	}

	if state.Counts.Issues != 2 {
		t.Errorf("issues = %d, want 2", state.Counts.Issues)
	}
	if state.Counts.Labels != 1 {
		t.Errorf("labels = %d, want 1", state.Counts.Labels)
	}
	if state.Counts.Events != 1 {
		t.Errorf("events = %d, want 1", state.Counts.Events)
	}
	if state.Counts.Dependencies != 1 {
		t.Errorf("dependencies = %d, want 1", state.Counts.Dependencies)
	}
	if state.LastDoltCommit == "" {
		t.Error("expected non-empty dolt commit")
	}

	// Verify files exist
	backupPath := filepath.Join(beadsDir, "backup")
	for _, file := range []string{"issues.jsonl", "events.jsonl", "dependencies.jsonl", "labels.jsonl", "config.jsonl", "backup_state.json"} {
		path := filepath.Join(backupPath, file)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", file)
		}
	}

	// Verify issues.jsonl content
	issuesData, err := os.ReadFile(filepath.Join(backupPath, "issues.jsonl"))
	if err != nil {
		t.Fatalf("read issues.jsonl: %v", err)
	}
	lines := splitJSONL(issuesData)
	if len(lines) != 2 {
		t.Errorf("issues.jsonl has %d lines, want 2", len(lines))
	}

	depsData, err := os.ReadFile(filepath.Join(backupPath, "dependencies.jsonl"))
	if err != nil {
		t.Fatalf("read dependencies.jsonl: %v", err)
	}
	depLines := splitJSONL(depsData)
	if len(depLines) != 1 {
		t.Fatalf("dependencies.jsonl has %d lines, want 1", len(depLines))
	}
	var depRow struct {
		IssueID     string `json:"issue_id"`
		DependsOnID string `json:"depends_on_id"`
		Metadata    string `json:"metadata"`
	}
	if err := json.Unmarshal(depLines[0], &depRow); err != nil {
		t.Fatalf("unmarshal dependency row: %v", err)
	}
	if depRow.IssueID != "test-2" || depRow.DependsOnID != "test-1" {
		t.Fatalf("dependency row = %s -> %s, want test-2 -> test-1", depRow.IssueID, depRow.DependsOnID)
	}
	if depRow.Metadata != depMetadata {
		t.Errorf("dependency metadata = %q, want %q", depRow.Metadata, depMetadata)
	}

	// Second export with no changes should be a no-op
	state2, err := runBackupExport(ctx, false)
	if err != nil {
		t.Fatalf("second runBackupExport: %v", err)
	}
	if state2.LastDoltCommit != state.LastDoltCommit {
		t.Error("expected same commit on second export with no changes")
	}
}

func TestBackupIncremental(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)
	saved := saveAndRestoreGlobals(t)
	_ = saved

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(beadsDir, "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	s := newTestStore(t, testDBPath)
	store = s
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		store = nil
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	ctx := context.Background()

	// Create an issue and event
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"inc-1", "Inc Issue", "desc", "", "", "", "open", 2, "task"); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO events (issue_id, event_type, actor) VALUES (?, ?, ?)`,
		"inc-1", "created", "tester"); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'initial')"); err != nil {
		t.Fatalf("dolt commit: %v", err)
	}

	// First export
	state, err := runBackupExport(ctx, false)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	if state.Counts.Events != 1 {
		t.Errorf("first export events = %d, want 1", state.Counts.Events)
	}

	// Add another event
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO events (issue_id, event_type, actor) VALUES (?, ?, ?)`,
		"inc-1", "status_changed", "tester"); err != nil {
		t.Fatalf("insert event 2: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'second event')"); err != nil {
		t.Fatalf("dolt commit 2: %v", err)
	}

	// Second export should include all events (full re-export)
	state2, err := runBackupExport(ctx, false)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if state2.Counts.Events != 2 {
		t.Errorf("second export events = %d, want 2", state2.Counts.Events)
	}

	// Events file should have 2 lines total (full re-export)
	eventsData, err := os.ReadFile(filepath.Join(beadsDir, "backup", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	lines := splitJSONL(eventsData)
	if len(lines) != 2 {
		t.Errorf("events.jsonl has %d lines, want 2", len(lines))
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
