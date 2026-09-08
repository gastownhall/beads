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
	"time"

	"github.com/steveyegge/beads/internal/storage/kvkeys"
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
		storageKey := kvkeys.MemoryConfigKeyPrefix + mk
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
		exportIncludeMemories = true
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

func TestExportByteStabilityAllRecordTypes(t *testing.T) {
	// Follow-up to gh beads 3787 / GH#3474: TestExportMemoryDeterminism only
	// proves memory lines are stable. This generalizes the invariant — running
	// `bd export` repeatedly over unchanged data must produce byte-identical
	// output for EVERY record type (issues + their labels, dependencies, and
	// comments, plus memories). Go's randomized map iteration is the usual
	// source of phantom diffs, so any collection serialized without a total
	// order (a sort key that ends in a unique tiebreaker) would surface here.
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

	// Seed several issues at the SAME priority and creation time so the export's
	// ORDER BY priority, created_at DESC, id ASC must fall through to the id
	// tiebreaker — exactly the total-order property the principle depends on.
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	titles := []string{
		"zeta epic", "alpha task", "mu feature", "beta bug", "omega chore",
	}
	var issues []*types.Issue
	for _, title := range titles {
		issue := &types.Issue{
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: created,
		}
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%q): %v", title, err)
		}
		issues = append(issues, issue)
	}

	// Labels: multiple per issue, added in non-alphabetical order so an
	// unsorted label serialization would reorder between runs.
	for _, issue := range issues {
		for _, label := range []string{"gamma", "alpha", "delta", "beta"} {
			if err := s.AddLabel(ctx, issue.ID, label, "test"); err != nil {
				t.Fatalf("AddLabel(%s, %s): %v", issue.ID, label, err)
			}
		}
	}

	// Dependencies: fan the other issues onto the first as children, so a
	// single issue is referenced by multiple edges. Parent-child is used (as in
	// children_test) to stay on the issues-table dependency path.
	for _, issue := range issues[1:] {
		if err := s.AddDependency(ctx, &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: issues[0].ID,
			Type:        types.DepParentChild,
		}, "test"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", issue.ID, issues[0].ID, err)
		}
	}

	// Comments: several per issue. AddIssueComment writes to the comments table
	// that export reads (AddComment records an event instead and would not show).
	for _, issue := range issues {
		for _, c := range []string{"first note", "second note", "third note"} {
			if _, err := s.AddIssueComment(ctx, issue.ID, "test", c); err != nil {
				t.Fatalf("AddIssueComment(%s): %v", issue.ID, err)
			}
		}
	}

	// Memories, keyed so insertion order differs from sorted order.
	for _, mk := range []string{"zeta-config", "alpha-note", "mu-decision", "beta-lesson"} {
		if err := s.SetConfig(ctx, kvkeys.MemoryConfigKeyPrefix+mk, "value-for-"+mk); err != nil {
			t.Fatalf("SetConfig(%s): %v", mk, err)
		}
	}

	doExport := func(path string) []byte {
		t.Helper()
		exportOutput = path
		exportAll = false
		exportIncludeInfra = false
		exportScrub = false
		exportNoMemories = false
		exportIncludeMemories = true
		if err := runExport(nil, nil); err != nil {
			t.Fatalf("runExport(%s): %v", path, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return data
	}

	// Export several times; randomized map iteration means a non-determinism
	// bug may only manifest on some runs, so compare multiple exports.
	const runs = 5
	first := doExport(filepath.Join(tmpDir, "export-0.jsonl"))
	for i := 1; i < runs; i++ {
		next := doExport(filepath.Join(tmpDir, fmt.Sprintf("export-%d.jsonl", i)))
		if string(next) != string(first) {
			t.Errorf("export run %d is not byte-identical to run 0 — serialization is non-deterministic", i)
			t.Logf("run 0:\n%s", first)
			t.Logf("run %d:\n%s", i, next)
			break
		}
	}

	// Sanity: confirm the export actually contained the record types we seeded,
	// so a future change that silently drops a section can't make the stability
	// check pass vacuously.
	var issueCount, memoryCount, labeled, withDeps, withComments int
	for _, line := range splitJSONL(first) {
		var rec map[string]interface{}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("parse line: %v", err)
		}
		switch rec["_type"] {
		case "memory":
			memoryCount++
		case "issue":
			issueCount++
			if labels, ok := rec["labels"].([]interface{}); ok && len(labels) > 0 {
				labeled++
			}
			if deps, ok := rec["dependencies"].([]interface{}); ok && len(deps) > 0 {
				withDeps++
			}
			if comments, ok := rec["comments"].([]interface{}); ok && len(comments) > 0 {
				withComments++
			}
		}
	}
	if issueCount != len(issues) {
		t.Errorf("expected %d issue lines, got %d", len(issues), issueCount)
	}
	if memoryCount != 4 {
		t.Errorf("expected 4 memory lines, got %d", memoryCount)
	}
	if labeled == 0 {
		t.Error("no exported issue carried labels — label serialization was not exercised")
	}
	if withDeps == 0 {
		t.Error("no exported issue carried dependencies — dependency serialization was not exercised")
	}
	if withComments == 0 {
		t.Error("no exported issue carried comments — comment serialization was not exercised")
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

func TestExportExcludesMemoriesByDefault(t *testing.T) {
	// GH#3650: bd export must exclude memories by default because they may
	// contain sensitive agent context. Only --include-memories or --all
	// should include them.
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

	// Create a persistent issue.
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"memexcl-1", "Regular issue", "", "", "", "", "open", 2, "task"); err != nil {
		t.Fatalf("insert issue: %v", err)
	}

	// Seed memories.
	for _, mk := range []string{"secret-api-pattern", "debug-session-notes"} {
		storageKey := kvkeys.MemoryConfigKeyPrefix + mk
		if err := s.SetConfig(ctx, storageKey, "sensitive-value-for-"+mk); err != nil {
			t.Fatalf("SetConfig(%s): %v", storageKey, err)
		}
	}

	countMemoryLines := func(data []byte) int {
		count := 0
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			var rec map[string]interface{}
			if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
				continue
			}
			if rec["_type"] == "memory" {
				count++
			}
		}
		return count
	}

	// Default export: memories must be excluded.
	defaultFile := filepath.Join(tmpDir, "default_export.jsonl")
	exportOutput = defaultFile
	exportAll = false
	exportIncludeInfra = false
	exportScrub = false
	exportNoMemories = false
	exportIncludeMemories = false

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport (default): %v", err)
	}
	defaultData, err := os.ReadFile(defaultFile)
	if err != nil {
		t.Fatalf("read default export: %v", err)
	}
	if n := countMemoryLines(defaultData); n != 0 {
		t.Errorf("default export: expected 0 memory lines, got %d", n)
	}

	// --include-memories: memories must appear.
	includeFile := filepath.Join(tmpDir, "include_export.jsonl")
	exportOutput = includeFile
	exportIncludeMemories = true
	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport (--include-memories): %v", err)
	}
	includeData, err := os.ReadFile(includeFile)
	if err != nil {
		t.Fatalf("read --include-memories export: %v", err)
	}
	if n := countMemoryLines(includeData); n != 2 {
		t.Errorf("--include-memories export: expected 2 memory lines, got %d", n)
	}

	// --all: memories must also appear.
	allFile := filepath.Join(tmpDir, "all_export.jsonl")
	exportOutput = allFile
	exportAll = true
	exportIncludeMemories = false
	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport (--all): %v", err)
	}
	allData, err := os.ReadFile(allFile)
	if err != nil {
		t.Fatalf("read --all export: %v", err)
	}
	if n := countMemoryLines(allData); n != 2 {
		t.Errorf("--all export: expected 2 memory lines, got %d", n)
	}
}

func TestExportExcludesWispsByDefault(t *testing.T) {
	// GH#3649: bd export must exclude ephemeral wisps by default.
	// Wisps are private/transient and must not reach git history.
	// Only --all should include them.
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

	// Create persistent issues.
	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("wispexcl-regular-%d", i)
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, fmt.Sprintf("Persistent issue %d", i), "", "", "", "", "open", 2, "task"); err != nil {
			t.Fatalf("insert persistent issue %d: %v", i, err)
		}
	}

	// Create ephemeral wisps via the store API (routes to wisps table).
	for i := 1; i <= 3; i++ {
		wisp := &types.Issue{
			Title:     fmt.Sprintf("Private wisp %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := s.CreateIssue(ctx, wisp, "test"); err != nil {
			t.Fatalf("CreateIssue (wisp %d): %v", i, err)
		}
	}

	// Default export (no --all): wisps must be excluded.
	exportFile := filepath.Join(tmpDir, "default_export.jsonl")
	exportOutput = exportFile
	exportAll = false
	exportIncludeInfra = false
	exportScrub = false
	exportNoMemories = true
	t.Cleanup(func() {
		exportOutput = ""
		exportAll = false
		exportNoMemories = false
	})

	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport (default): %v", err)
	}

	data, err := os.ReadFile(exportFile)
	if err != nil {
		t.Fatalf("read default export: %v", err)
	}
	defaultLines := splitJSONL(data)
	for _, line := range defaultLines {
		var rec map[string]interface{}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ephemeral, ok := rec["ephemeral"].(bool); ok && ephemeral {
			t.Errorf("default export contains ephemeral wisp: %s", rec["id"])
		}
	}
	if len(defaultLines) != 2 {
		t.Errorf("default export: expected 2 persistent issues, got %d lines", len(defaultLines))
	}

	// --all export: wisps must be included.
	allFile := filepath.Join(tmpDir, "all_export.jsonl")
	exportOutput = allFile
	exportAll = true
	if err := runExport(nil, nil); err != nil {
		t.Fatalf("runExport (--all): %v", err)
	}
	allData, err := os.ReadFile(allFile)
	if err != nil {
		t.Fatalf("read --all export: %v", err)
	}
	allLines := splitJSONL(allData)
	if len(allLines) != 5 {
		t.Errorf("--all export: expected 5 issues (2 persistent + 3 wisps), got %d", len(allLines))
	}
}
