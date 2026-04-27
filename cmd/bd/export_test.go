//go:build cgo

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
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

func TestExportNoHistoryBeadRoundTrip(t *testing.T) {
	// GH#2619: NoHistory beads are stored in the wisps table. The JSONL export
	// must include them with no_history=true, and import must preserve the flag.
	// If no_history is dropped during import, the bead becomes GC-eligible.
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

	// Create a NoHistory bead using the store API (routes to wisps table with no_history=1).
	noHistoryBead := &types.Issue{
		Title:     "NoHistory export test bead",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := s.CreateIssue(ctx, noHistoryBead, "test"); err != nil {
		t.Fatalf("CreateIssue (NoHistory): %v", err)
	}

	// Also create a regular issue to ensure the export contains both.
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"nohistory-regular-1", "Regular issue", "", "", "", "", "open", 1, "task"); err != nil {
		t.Fatalf("insert regular issue: %v", err)
	}

	// Export to file.
	exportFile := filepath.Join(tmpDir, "nohistory_export.jsonl")
	exportOutput = exportFile
	exportAll = true // include everything
	exportIncludeInfra = false
	exportScrub = false
	t.Cleanup(func() {
		exportOutput = ""
		exportAll = false
	})

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}

	// Verify the NoHistory bead appears in the export with no_history=true.
	lines := splitJSONL(data)
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines in export (regular + NoHistory), got %d", len(lines))
	}

	var noHistoryLine map[string]interface{}
	for _, line := range lines {
		var rec map[string]interface{}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("parse exported JSONL: %v", err)
		}
		if rec["title"] == "NoHistory export test bead" {
			noHistoryLine = rec
			break
		}
	}
	if noHistoryLine == nil {
		t.Fatal("NoHistory bead not found in exported JSONL — export missed wisps with no_history=true")
	}
	if noHistoryLine["no_history"] != true {
		t.Errorf("exported NoHistory bead has no_history=%v, want true", noHistoryLine["no_history"])
	}

	// Import the exported JSONL into a fresh store and verify no_history survives.
	tmpDir2 := t.TempDir()
	dbPath2 := filepath.Join(tmpDir2, "dolt")
	store2 := newTestStore(t, dbPath2)

	count, err := importFromLocalJSONL(ctx, store2, exportFile)
	if err != nil {
		t.Fatalf("importFromLocalJSONL: %v", err)
	}
	if count < 2 {
		t.Errorf("expected at least 2 issues imported, got %d", count)
	}

	// Retrieve the NoHistory bead from the new store and check the flag.
	imported, err := store2.GetIssue(ctx, noHistoryBead.ID)
	if err != nil {
		t.Fatalf("GetIssue(%s) after import: %v", noHistoryBead.ID, err)
	}
	if !imported.NoHistory {
		t.Error("no_history=true was lost during export→import roundtrip: bead is now GC-eligible")
	}
	if imported.Ephemeral {
		t.Error("NoHistory bead must not become ephemeral=true after roundtrip")
	}
}

func TestExportMemoryDeterminism(t *testing.T) {
	// GH#3474: memory lines must appear in deterministic order across exports.
	// Seeds multiple memories, exports twice to separate files, and asserts
	// byte-for-byte identical output.
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

	// Seed 5 memories with keys that would sort differently than insertion order.
	memKeys := []string{"zeta-config", "alpha-note", "mu-decision", "beta-lesson", "omega-context"}
	for _, mk := range memKeys {
		storageKey := "kv.memory." + mk
		if err := s.SetConfig(ctx, storageKey, "value-for-"+mk); err != nil {
			t.Fatalf("SetConfig(%s): %v", storageKey, err)
		}
	}

	doExport := func(path string) []byte {
		t.Helper()
		exportOutput = path
		exportAll = false
		exportIncludeInfra = false
		exportScrub = false
		exportNoMemories = false
		if err := runExport(nil, nil); err != nil {
			t.Fatalf("runExport(%s): %v", path, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return data
	}

	export1 := doExport(filepath.Join(tmpDir, "export1.jsonl"))
	export2 := doExport(filepath.Join(tmpDir, "export2.jsonl"))

	if string(export1) != string(export2) {
		t.Error("exports are not byte-identical — memory ordering is non-deterministic")
		t.Logf("export1:\n%s", export1)
		t.Logf("export2:\n%s", export2)
	}

	// Verify memories are present and sorted alphabetically by key.
	lines := splitJSONL(export1)
	var memoryKeys []string
	for _, line := range lines {
		var rec map[string]interface{}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("parse line: %v", err)
		}
		if rec["_type"] == "memory" {
			memoryKeys = append(memoryKeys, rec["key"].(string))
		}
	}
	if len(memoryKeys) != len(memKeys) {
		t.Fatalf("expected %d memory lines, got %d", len(memKeys), len(memoryKeys))
	}
	for i := 1; i < len(memoryKeys); i++ {
		if memoryKeys[i] < memoryKeys[i-1] {
			t.Errorf("memory keys not sorted: %q appears after %q", memoryKeys[i], memoryKeys[i-1])
		}
	}
}

func TestExportNoDuplicateWisps(t *testing.T) {
	// GH#3352: A previous bug caused every wisp to appear twice in the export
	// because export.go ran a separate Ephemeral=true query and appended the
	// results, even though SearchIssues(Ephemeral=nil) already includes wisps.
	// This regression test ensures no duplicate IDs appear in the export and
	// the wisp count matches what was created.
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

	// Create regular (persistent) issues.
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("duptest-regular-%d", i)
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, fmt.Sprintf("Regular issue %d", i), "", "", "", "", "open", 2, "task"); err != nil {
			t.Fatalf("insert regular issue %d: %v", i, err)
		}
	}

	// Create ephemeral wisps via the store API (routes to wisps table).
	wispIDs := make(map[string]bool)
	for i := 1; i <= 3; i++ {
		wisp := &types.Issue{
			Title:     fmt.Sprintf("Wisp %d for export dedup", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := s.CreateIssue(ctx, wisp, "test"); err != nil {
			t.Fatalf("CreateIssue (wisp %d): %v", i, err)
		}
		wispIDs[wisp.ID] = true
	}

	// Export with --all to include everything.
	exportFile := filepath.Join(tmpDir, "dedup_export.jsonl")
	exportOutput = exportFile
	exportAll = true
	exportIncludeInfra = false
	exportScrub = false
	exportNoMemories = true
	t.Cleanup(func() {
		exportOutput = ""
		exportAll = false
		exportNoMemories = false
	})

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport: %v", err)
	}

	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}

	lines := splitJSONL(data)

	// Parse every line and collect IDs.
	seenIDs := make(map[string]int)
	exportedWispCount := 0
	for _, line := range lines {
		var rec map[string]interface{}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("parse exported JSONL: %v", err)
		}
		id, ok := rec["id"].(string)
		if !ok {
			continue // skip non-issue records (e.g. memories)
		}
		seenIDs[id]++
		if wispIDs[id] {
			exportedWispCount++
		}
	}

	// Assert no duplicate IDs.
	for id, count := range seenIDs {
		if count > 1 {
			t.Errorf("duplicate export entry for ID %q: appeared %d times", id, count)
		}
	}

	// Assert all wisps are present exactly once.
	if exportedWispCount != len(wispIDs) {
		t.Errorf("expected %d wisps in export, got %d", len(wispIDs), exportedWispCount)
	}

	// Assert total count = 3 regular + 3 wisps = 6.
	expectedTotal := 6
	if len(seenIDs) != expectedTotal {
		t.Errorf("expected %d unique issues in export, got %d", expectedTotal, len(seenIDs))
	}
}
