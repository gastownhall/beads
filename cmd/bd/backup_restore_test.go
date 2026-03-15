//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
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

	// Create test store with data
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

	// Populate test data: issues, labels, events, comments, dependencies, config
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rt-1", "Round Trip Issue 1", "description one", "", "", "", "open", 2, "task"); err != nil {
		t.Fatalf("insert issue 1: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rt-2", "Round Trip Issue 2", "description two", "", "", "", "done", 1, "bug"); err != nil {
		t.Fatalf("insert issue 2: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO labels (issue_id, label) VALUES (?, ?)`, "rt-1", "backend"); err != nil {
		t.Fatalf("insert label: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO labels (issue_id, label) VALUES (?, ?)`, "rt-1", "urgent"); err != nil {
		t.Fatalf("insert label 2: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO events (issue_id, event_type, actor) VALUES (?, ?, ?)`, "rt-1", "created", "tester"); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO comments (issue_id, author, text) VALUES (?, ?, ?)`, "rt-1", "tester", "first comment"); err != nil {
		t.Fatalf("insert comment: %v", err)
	}
	depMetadata := `{"gate":"any-children","spawner_id":"rt-1"}`
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, metadata) VALUES (?, ?, ?, ?, ?)`,
		"rt-2", "rt-1", "blocks", "tester", depMetadata); err != nil {
		t.Fatalf("insert dependency: %v", err)
	}
	if err := s.SetConfig(ctx, "issue_prefix", "rt"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	// Commit data
	if _, err := s.DB().ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'test data')"); err != nil {
		t.Fatalf("dolt commit: %v", err)
	}

	// Export backup
	state, err := runBackupExport(ctx, true)
	if err != nil {
		t.Fatalf("runBackupExport: %v", err)
	}
	if state.Counts.Issues != 2 {
		t.Errorf("exported issues = %d, want 2", state.Counts.Issues)
	}

	backupPath := filepath.Join(beadsDir, "backup")

	// Create a NEW store (simulating database loss and re-init)
	dbName2 := uniqueTestDBName(t)
	testDBPath2 := filepath.Join(t.TempDir(), "dolt")
	writeTestMetadata(t, testDBPath2, dbName2)
	s2 := newTestStore(t, testDBPath2)
	store = s2
	t.Cleanup(func() {
		store = nil
	})

	// Restore from backup
	result, err := runBackupRestore(ctx, s2, backupPath, false)
	if err != nil {
		t.Fatalf("runBackupRestore: %v", err)
	}

	// Verify counts
	if result.Issues != 2 {
		t.Errorf("restored issues = %d, want 2", result.Issues)
	}
	if result.Labels != 2 {
		t.Errorf("restored labels = %d, want 2", result.Labels)
	}
	if result.Events != 1 {
		t.Errorf("restored events = %d, want 1", result.Events)
	}
	if result.Comments != 1 {
		t.Errorf("restored comments = %d, want 1", result.Comments)
	}
	if result.Dependencies != 1 {
		t.Errorf("restored dependencies = %d, want 1", result.Dependencies)
	}
	if result.Config < 1 {
		t.Errorf("restored config = %d, want >= 1", result.Config)
	}
	if result.Warnings != 0 {
		t.Errorf("restore warnings = %d, want 0", result.Warnings)
	}

	// Verify data was actually restored by querying
	issue, err := s2.GetIssue(ctx, "rt-1")
	if err != nil {
		t.Fatalf("get issue rt-1: %v", err)
	}
	if issue.Title != "Round Trip Issue 1" {
		t.Errorf("issue title = %q, want %q", issue.Title, "Round Trip Issue 1")
	}
	if issue.Description != "description one" {
		t.Errorf("issue description = %q, want %q", issue.Description, "description one")
	}

	// Verify labels were restored
	labels, err := s2.GetLabels(ctx, "rt-1")
	if err != nil {
		t.Fatalf("get labels: %v", err)
	}
	if len(labels) != 2 {
		t.Errorf("labels count = %d, want 2", len(labels))
	}

	var restoredMetadata string
	if err := s2.DB().QueryRowContext(ctx,
		`SELECT metadata FROM dependencies WHERE issue_id = ? AND depends_on_id = ?`,
		"rt-2", "rt-1").Scan(&restoredMetadata); err != nil {
		t.Fatalf("query restored dependency metadata: %v", err)
	}
	if restoredMetadata != depMetadata {
		t.Errorf("restored dependency metadata = %q, want %q", restoredMetadata, depMetadata)
	}

	// Verify config was restored
	prefix, err := s2.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if prefix != "rt" {
		t.Errorf("config issue_prefix = %q, want %q", prefix, "rt")
	}
}

func TestBackupRestoreDryRun(t *testing.T) {
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
	backupPath := filepath.Join(tmpDir, "backup")
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Write minimal JSONL backup files
	issuesData := `{"id":"dry-1","title":"Dry Run Issue","status":"open","priority":2,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(filepath.Join(backupPath, "issues.jsonl"), []byte(issuesData), 0600); err != nil {
		t.Fatal(err)
	}
	labelsData := `{"issue_id":"dry-1","label":"test"}` + "\n"
	if err := os.WriteFile(filepath.Join(backupPath, "labels.jsonl"), []byte(labelsData), 0600); err != nil {
		t.Fatal(err)
	}

	// Create a store but dry-run shouldn't touch it
	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(t.TempDir(), "dolt")
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

	result, err := runBackupRestore(ctx, s, backupPath, true)
	if err != nil {
		t.Fatalf("dry-run restore: %v", err)
	}

	if result.Issues != 1 {
		t.Errorf("dry-run issues = %d, want 1", result.Issues)
	}
	if result.Labels != 1 {
		t.Errorf("dry-run labels = %d, want 1", result.Labels)
	}

	// Verify nothing was actually written
	_, err = s.GetIssue(ctx, "dry-1")
	if err == nil {
		t.Error("dry-run should not have written issue to database")
	}
}

func TestBackupRestoreAcceptsUUIDCommentAndEventIDs(t *testing.T) {
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
	backupPath := filepath.Join(tmpDir, "backup")
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		t.Fatal(err)
	}

	issuesData := `{"id":"uuid-1","title":"UUID Restore","status":"open","priority":2,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(filepath.Join(backupPath, "issues.jsonl"), []byte(issuesData), 0600); err != nil {
		t.Fatal(err)
	}

	commentsData := `{"id":"c6b39fb0-a0fa-4f4a-8cd2-2d5f3f3d8b73","issue_id":"uuid-1","author":"tester","text":"comment restored","created_at":"2026-03-14T18:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(backupPath, "comments.jsonl"), []byte(commentsData), 0600); err != nil {
		t.Fatal(err)
	}

	eventsData := `{"id":"ec2dd5f8-8e1a-4aa9-9c27-3fda5d7406fc","issue_id":"uuid-1","event_type":"created","actor":"tester","created_at":"2026-03-14T18:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(backupPath, "events.jsonl"), []byte(eventsData), 0600); err != nil {
		t.Fatal(err)
	}

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(t.TempDir(), "dolt")
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
	stderr := captureStderr(t, func() {
		result, err := runBackupRestore(ctx, s, backupPath, false)
		if err != nil {
			t.Fatalf("restore uuid comment/event ids: %v", err)
		}
		if result.Issues != 1 {
			t.Errorf("restored issues = %d, want 1", result.Issues)
		}
		if result.Comments != 1 {
			t.Errorf("restored comments = %d, want 1", result.Comments)
		}
		if result.Events != 1 {
			t.Errorf("restored events = %d, want 1", result.Events)
		}
		if result.Warnings != 0 {
			t.Errorf("restore warnings = %d, want 0", result.Warnings)
		}
	})

	if stderr != "" {
		t.Fatalf("expected no restore warnings, got stderr: %s", stderr)
	}

	var commentCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE issue_id = ?`, "uuid-1").Scan(&commentCount); err != nil {
		t.Fatalf("count restored comments: %v", err)
	}
	if commentCount != 1 {
		t.Errorf("comment count = %d, want 1", commentCount)
	}

	var eventCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE issue_id = ?`, "uuid-1").Scan(&eventCount); err != nil {
		t.Fatalf("count restored events: %v", err)
	}
	if eventCount != 1 {
		t.Errorf("event count = %d, want 1", eventCount)
	}
}

func TestBackupRestoreDependenciesWithoutMetadataDefaultsToEmptyObject(t *testing.T) {
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
	backupPath := filepath.Join(tmpDir, "backup")
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		t.Fatal(err)
	}

	issuesData := "" +
		`{"id":"compat-1","title":"Compat Parent","status":"open","priority":2,"issue_type":"task"}` + "\n" +
		`{"id":"compat-2","title":"Compat Child","status":"open","priority":2,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(filepath.Join(backupPath, "issues.jsonl"), []byte(issuesData), 0600); err != nil {
		t.Fatal(err)
	}
	depsData := `{"issue_id":"compat-2","depends_on_id":"compat-1","type":"blocks","created_by":"tester"}` + "\n"
	if err := os.WriteFile(filepath.Join(backupPath, "dependencies.jsonl"), []byte(depsData), 0600); err != nil {
		t.Fatal(err)
	}

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(t.TempDir(), "dolt")
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

	result, err := runBackupRestore(ctx, s, backupPath, false)
	if err != nil {
		t.Fatalf("restore from old-format dependency backup: %v", err)
	}
	if result.Dependencies != 1 {
		t.Fatalf("restored dependencies = %d, want 1", result.Dependencies)
	}
	if result.Warnings != 0 {
		t.Fatalf("restore warnings = %d, want 0", result.Warnings)
	}

	var restoredMetadata string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT metadata FROM dependencies WHERE issue_id = ? AND depends_on_id = ?`,
		"compat-2", "compat-1").Scan(&restoredMetadata); err != nil {
		t.Fatalf("query restored dependency metadata: %v", err)
	}
	if restoredMetadata != "{}" {
		t.Errorf("restored dependency metadata = %q, want %q", restoredMetadata, "{}")
	}
}

func TestBackupRestoreMissingDir(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(t.TempDir(), "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	s := newTestStore(t, testDBPath)
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	_, err := runBackupRestore(ctx, s, "/nonexistent/path", false)
	if err == nil {
		t.Error("expected error for nonexistent backup dir")
	}
}

// TestBackupRestoreDenormalized verifies that `bd backup restore` handles
// denormalized JSONL from `bd export`, which embeds labels, dependencies,
// and count fields directly in issue rows. These must be extracted and
// inserted into their proper relational tables.
func TestBackupRestoreDenormalized(t *testing.T) {
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
	backupPath := filepath.Join(tmpDir, "backup")
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Write JSONL with denormalized data (as produced by `bd export`)
	issuesData := `{"id":"dn-1","title":"Issue with labels","description":"has embedded labels","status":"open","priority":2,"issue_type":"task","labels":["backend","urgent"],"dependencies":[],"dependency_count":0,"dependent_count":0,"comment_count":0}
{"id":"dn-2","title":"Issue with deps","description":"has embedded deps","status":"open","priority":1,"issue_type":"bug","labels":["frontend"],"dependencies":[{"issue_id":"dn-2","depends_on_id":"dn-1","type":"blocks","created_at":"2026-01-15T10:30:00Z","created_by":"tester","metadata":"{}"}],"dependency_count":1,"dependent_count":0,"comment_count":0}
`
	if err := os.WriteFile(filepath.Join(backupPath, "issues.jsonl"), []byte(issuesData), 0600); err != nil {
		t.Fatal(err)
	}

	// Create a fresh store
	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(t.TempDir(), "dolt")
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

	result, err := runBackupRestore(ctx, s, backupPath, false)
	if err != nil {
		t.Fatalf("restore denormalized: %v", err)
	}

	// Both issues should be restored
	if result.Issues != 2 {
		t.Errorf("restored issues = %d, want 2", result.Issues)
	}
	if result.Warnings != 0 {
		t.Errorf("restore warnings = %d, want 0", result.Warnings)
	}

	// Verify issues exist
	issue1, err := s.GetIssue(ctx, "dn-1")
	if err != nil {
		t.Fatalf("get issue dn-1: %v", err)
	}
	if issue1.Title != "Issue with labels" {
		t.Errorf("issue1 title = %q, want %q", issue1.Title, "Issue with labels")
	}

	issue2, err := s.GetIssue(ctx, "dn-2")
	if err != nil {
		t.Fatalf("get issue dn-2: %v", err)
	}
	if issue2.Title != "Issue with deps" {
		t.Errorf("issue2 title = %q, want %q", issue2.Title, "Issue with deps")
	}

	// Verify labels were extracted and inserted into the labels table
	labels1, err := s.GetLabels(ctx, "dn-1")
	if err != nil {
		t.Fatalf("get labels dn-1: %v", err)
	}
	if len(labels1) != 2 {
		t.Errorf("dn-1 labels count = %d, want 2 (got %v)", len(labels1), labels1)
	}

	labels2, err := s.GetLabels(ctx, "dn-2")
	if err != nil {
		t.Fatalf("get labels dn-2: %v", err)
	}
	if len(labels2) != 1 {
		t.Errorf("dn-2 labels count = %d, want 1 (got %v)", len(labels2), labels2)
	}

	// Verify dependencies were extracted and inserted into the dependencies table
	deps, err := s.GetDependencies(ctx, "dn-2")
	if err != nil {
		t.Fatalf("get dependencies dn-2: %v", err)
	}
	if len(deps) != 1 {
		t.Errorf("dn-2 dependencies count = %d, want 1", len(deps))
	}
	if len(deps) > 0 && deps[0].ID != "dn-1" {
		t.Errorf("dn-2 depends_on = %q, want %q", deps[0].ID, "dn-1")
	}
}

func TestReadJSONLFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	content := `{"key":"a","value":"1"}
{"key":"b","value":"2"}

{"key":"c","value":"3"}
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	lines, err := readJSONLFile(path)
	if err != nil {
		t.Fatalf("readJSONLFile: %v", err)
	}

	if len(lines) != 3 {
		t.Errorf("got %d lines, want 3", len(lines))
	}

	// Verify each line is valid JSON
	for i, line := range lines {
		var m map[string]string
		if err := json.Unmarshal(line, &m); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestParseTimeOrNow(t *testing.T) {
	t.Parallel()

	// Valid time
	ts := parseTimeOrNow("2026-01-15T10:30:00Z")
	if ts.Year() != 2026 || ts.Month() != 1 || ts.Day() != 15 {
		t.Errorf("unexpected parsed time: %v", ts)
	}

	// Empty string returns now-ish
	ts2 := parseTimeOrNow("")
	if ts2.Year() < 2026 {
		t.Errorf("empty string should return now, got: %v", ts2)
	}

	// Invalid string returns now-ish
	ts3 := parseTimeOrNow("not-a-time")
	if ts3.Year() < 2026 {
		t.Errorf("invalid string should return now, got: %v", ts3)
	}
}
