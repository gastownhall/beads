//go:build cgo

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/beads/internal/testutil"
)

func TestExportToFile(t *testing.T) {
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
	rootCtx = ctx

	// Create test issues
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"exp-1", "Export Issue 1", "description one", "", "", "", "open", 1, "task"); err != nil {
		t.Fatalf("insert issue 1: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"exp-2", "Export Issue 2", "description two", "", "", "", "closed", 2, "bug"); err != nil {
		t.Fatalf("insert issue 2: %v", err)
	}

	// Add a label
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO labels (issue_id, label) VALUES (?, ?)`,
		"exp-1", "important"); err != nil {
		t.Fatalf("insert label: %v", err)
	}

	// Export to file
	exportFile := filepath.Join(tmpDir, "export.jsonl")
	exportOutput = exportFile
	exportAll = false
	exportIncludeInfra = false
	exportScrub = false
	t.Cleanup(func() { exportOutput = "" })

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(exportFile); os.IsNotExist(err) {
		t.Fatal("export file not created")
	}

	// Read and verify content
	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}

	lines := splitJSONL(data)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	// Parse first issue and check fields
	var issue1 map[string]interface{}
	if err := json.Unmarshal(lines[0], &issue1); err != nil {
		t.Fatalf("parse line 0: %v", err)
	}

	// One of the two issues should have the label
	foundLabel := false
	for _, line := range lines {
		var iss map[string]interface{}
		json.Unmarshal(line, &iss)
		if labels, ok := iss["labels"].([]interface{}); ok && len(labels) > 0 {
			if labels[0].(string) == "important" {
				foundLabel = true
			}
		}
	}
	if !foundLabel {
		t.Error("expected to find 'important' label in exported issues")
	}
}

func TestExportToStdout(t *testing.T) {
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
	rootCtx = ctx

	// Create a test issue
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"exp-3", "Stdout Export", "testing stdout", "", "", "", "open", 1, "task"); err != nil {
		t.Fatalf("insert issue: %v", err)
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	exportOutput = ""
	exportAll = false
	exportIncludeInfra = false
	exportScrub = false

	err := runExport(nil, nil)

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runExport: %v", err)
	}

	// Read captured output
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if len(lines) != 1 {
		t.Fatalf("expected 1 line on stdout, got %d", len(lines))
	}

	var issue map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &issue); err != nil {
		t.Fatalf("parse stdout line: %v", err)
	}
	if issue["title"] != "Stdout Export" {
		t.Errorf("expected title 'Stdout Export', got %v", issue["title"])
	}
}

func TestExportScrub(t *testing.T) {
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
	rootCtx = ctx

	// Create a real issue and a test pollution issue
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"exp-4", "Real Issue", "real work", "", "", "", "open", 1, "task"); err != nil {
		t.Fatalf("insert real issue: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"exp-5", "test-pollution item", "should be scrubbed", "", "", "", "open", 3, "task"); err != nil {
		t.Fatalf("insert test issue: %v", err)
	}

	// Export with scrub
	exportFile := filepath.Join(tmpDir, "scrubbed.jsonl")
	exportOutput = exportFile
	exportAll = false
	exportIncludeInfra = false
	exportScrub = true
	t.Cleanup(func() {
		exportOutput = ""
		exportScrub = false
	})

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read scrubbed file: %v", err)
	}

	lines := splitJSONL(data)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line after scrub, got %d", len(lines))
	}

	var issue map[string]interface{}
	json.Unmarshal(lines[0], &issue)
	if issue["title"] != "Real Issue" {
		t.Errorf("expected 'Real Issue', got %v", issue["title"])
	}
}

func TestExportImportRoundTrip(t *testing.T) {
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
	rootCtx = ctx

	// Create a test issue
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"exp-6", "Round Trip", "round trip test", "", "", "", "open", 1, "feature"); err != nil {
		t.Fatalf("insert issue: %v", err)
	}

	// Export
	exportFile := filepath.Join(tmpDir, "roundtrip.jsonl")
	exportOutput = exportFile
	exportAll = false
	exportIncludeInfra = false
	exportScrub = false
	t.Cleanup(func() { exportOutput = "" })

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("export: %v", err)
	}

	// Verify the exported JSONL can be parsed by the import system
	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	var count int
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var issue map[string]interface{}
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			t.Fatalf("parse exported JSONL line %d: %v", count, err)
		}
		if issue["id"] == nil || issue["title"] == nil {
			t.Errorf("line %d missing required fields: %v", count, issue)
		}
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 issue, got %d", count)
	}
}

func TestFilterOutPollution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		title string
		want  bool
	}{
		{"Real feature request", false},
		{"test-something", true},
		{"benchmark-perf test", true},
		{"Actual bug fix", false},
		{"tmp-throwaway", true},
	}

	for _, tt := range tests {
		if got := isTestIssue(tt.title); got != tt.want {
			t.Errorf("isTestIssue(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}
