//go:build cgo

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// setupValidateTestDB creates a temp .beads workspace with a configured database.
// Uses newTestStoreWithPrefix to ensure metadata.json has the correct database name
// so that collectValidateChecks (which reads metadata.json) connects to the right DB.
func setupValidateTestDB(t *testing.T, prefix string) (tmpDir string, store *dolt.DoltStore) {
	t.Helper()
	tmpDir = t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "dolt")
	store = newTestStoreIsolatedDB(t, dbPath, prefix)

	return tmpDir, store
}

func TestValidateCheck_AllClean(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "val")
	ctx := context.Background()

	issues := []*types.Issue{
		{Title: "Fix login bug", Description: "Login fails", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
		{Title: "Add search", Description: "Full-text search", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "val"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Write clean JSONL so git conflicts check has a file to scan
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create JSONL: %v", err)
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Status != statusOK {
			t.Errorf("%s: status = %q, want %q (message: %s)", cr.check.Name, cr.check.Status, statusOK, cr.check.Message)
		}
	}
	if len(checks) != 4 {
		t.Errorf("Expected 4 checks, got %d", len(checks))
	}
}

func TestValidateCheck_DetectsDuplicates(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		issue := &types.Issue{
			Title:       "Duplicate task",
			Description: "Same description",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Duplicate Issues" {
			if cr.check.Status != statusWarning {
				t.Errorf("Duplicate Issues status = %q, want %q", cr.check.Status, statusWarning)
			}
			return
		}
	}
	t.Error("Duplicate Issues check not found")
}

func TestValidateCheck_DetectsOrphanedDeps(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Real issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	db := store.UnderlyingDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	// FK on depends_on_issue_id would normally block this; simulate the
	// schema-drift scenario the validator is designed to catch.
	if _, err = tx.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("Failed to disable FK checks: %v", err)
	}
	_, err = tx.Exec("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (UUID(), ?, ?, ?, ?)",
		issue.ID, "test-nonexistent", "blocks", "test")
	if err != nil {
		t.Fatalf("Failed to insert orphaned dep: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit orphaned dep: %v", err)
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Orphaned Dependencies" {
			if cr.check.Status != statusWarning {
				t.Errorf("Orphaned Dependencies status = %q, want %q", cr.check.Status, statusWarning)
			}
			if !cr.fixable {
				t.Error("Orphaned Dependencies should be marked fixable")
			}
			return
		}
	}
	t.Error("Orphaned Dependencies check not found")
}

func TestValidateCheck_GitConflicts_DoltClean(t *testing.T) {
	// When metadata is absent, GetBackend() defaults to "dolt",
	// the Git Conflicts check now queries dolt_conflicts (GH-2249).
	// Verify it reports OK for a clean Dolt database.
	tmpDir, store := setupValidateTestDB(t, "val")
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Clean issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "val"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Git Conflicts" {
			if cr.check.Status != statusOK {
				t.Errorf("Git Conflicts status = %q, want %q (clean DB)", cr.check.Status, statusOK)
			}
			return
		}
	}
	t.Error("Git Conflicts check not found")
}

func TestValidateCheck_DetectsTestPollution(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	testIssues := []*types.Issue{
		{Title: "test-pollution-check", Description: "A test issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "Test Issue 1", Description: "Another test", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range testIssues {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Test Pollution" {
			if cr.check.Status != statusWarning {
				t.Errorf("Test Pollution status = %q, want %q", cr.check.Status, statusWarning)
			}
			return
		}
	}
	t.Error("Test Pollution check not found")
}

func TestValidateCheck_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Status != statusOK {
			t.Errorf("%s: status = %q, want %q when no .beads/ exists", cr.check.Name, cr.check.Status, statusOK)
		}
	}
}

func TestValidateCheck_FixOrphanedDeps(t *testing.T) {
	// The orphaned deps fix uses raw SQLite queries and skips Dolt backends.
	// Since the default backend is now Dolt, the fix is a no-op.
	// This test verifies that detection works and the fix gracefully skips.
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Real issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	db := store.UnderlyingDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	// FK on depends_on_issue_id would normally block this; simulate the
	// schema-drift scenario the validator/fixer is designed to catch.
	if _, err = tx.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("Failed to disable FK checks: %v", err)
	}
	_, err = tx.Exec("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (UUID(), ?, ?, ?, ?)",
		issue.ID, "test-nonexistent", "blocks", "test")
	if err != nil {
		t.Fatalf("Failed to insert orphaned dep: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit orphaned dep: %v", err)
	}
	store.Close()

	// Verify orphan is detected
	checks := collectValidateChecks(tmpDir)
	found := false
	for _, cr := range checks {
		if cr.check.Name == "Orphaned Dependencies" {
			found = true
			if cr.check.Status != statusWarning {
				t.Errorf("Expected orphaned deps warning, got %q", cr.check.Status)
			}
			if !cr.fixable {
				t.Error("Orphaned Dependencies should be marked fixable")
			}
		}
	}
	if !found {
		t.Error("Orphaned Dependencies check not found")
	}
}

func TestValidateCheck_DetectsOrphanedChildCounters(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "test")
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Real issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	db := store.UnderlyingDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	// child_counters has an FK on parent_id; simulate the drift scenario
	// (#4539, follow-up to #4534) the validator is designed to catch.
	if _, err = tx.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("Failed to disable FK checks: %v", err)
	}
	_, err = tx.Exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)",
		"test-nonexistent-parent", 3)
	if err != nil {
		t.Fatalf("Failed to insert orphaned child counter: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit orphaned child counter: %v", err)
	}
	store.Close()

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Orphaned Child Counters" {
			if cr.check.Status != statusError {
				t.Errorf("Orphaned Child Counters status = %q, want %q", cr.check.Status, statusError)
			}
			if !cr.fixable {
				t.Error("Orphaned Child Counters should be marked fixable")
			}
			return
		}
	}
	t.Error("Orphaned Child Counters check not found")
}

// TestValidateCheck_DetectsOrphanedChildCounters_LocalDolt is the Docker-free
// companion to TestValidateCheck_DetectsOrphanedChildCounters. Unlike
// setupValidateTestDB (which needs the package-level testDoltServerPort set
// by a Docker-starting TestMain), this test starts its own real local `dolt
// sql-server` subprocess via dolt.New(AutoStart:true) — the local `dolt`
// binary already on PATH, no Docker required. collectValidateChecks reads
// metadata.json (via configfile) to find that server/database, so the
// config must be saved to .beads BEFORE dolt.New starts the server — same
// sequencing as TestOrphanedChildCounters_FixDeletesOnlyOrphans in
// cmd/bd/doctor/fix/validation_test.go, the ground-truth example of this
// pattern.
func TestValidateCheck_DetectsOrphanedChildCounters_LocalDolt(t *testing.T) {
	testutil.RequireDoltBinary(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}

	// Unique database name: the local server/process may outlive a single run.
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	dbName := "validatecc_" + hex.EncodeToString(h[:6])

	// Seed metadata.json up front (with the target database name already
	// set) so collectValidateChecks' own doctor.CheckOrphanedChildCounters
	// (a separate connection from the one below) resolves the same
	// server/database this test seeds.
	seedCfg := &configfile.Config{Database: "dolt", DoltDatabase: dbName}
	if err := seedCfg.Save(beadsDir); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		Database:        dbName,
		CreateIfMissing: true,
		MaxOpenConns:    1,
		AutoStart:       true,
	})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	// Unlike the Docker-backed variant (setupValidateTestDB), which closes
	// the store before calling collectValidateChecks because the shared
	// Docker test server outlives any single store's connection, AutoStart
	// ties the local `dolt sql-server` subprocess's lifetime to the last
	// store referencing it (see DoltStore.Close -> autoStartRelease). This
	// store must stay open for the rest of the test so the server survives
	// long enough for collectValidateChecks's own connection to reach it.
	defer func() { _ = store.Close() }()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	issue := &types.Issue{
		Title:     "Real issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	db := store.UnderlyingDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	// child_counters has an FK on parent_id; simulate the drift scenario
	// (#4539, follow-up to #4534) the validator is designed to catch.
	if _, err = tx.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("Failed to disable FK checks: %v", err)
	}
	_, err = tx.Exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)",
		"test-nonexistent-parent", 3)
	if err != nil {
		t.Fatalf("Failed to insert orphaned child counter: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit orphaned child counter: %v", err)
	}

	checks := collectValidateChecks(tmpDir)

	for _, cr := range checks {
		if cr.check.Name == "Orphaned Child Counters" {
			if cr.check.Status != statusError {
				t.Errorf("Orphaned Child Counters status = %q, want %q", cr.check.Status, statusError)
			}
			if !cr.fixable {
				t.Error("Orphaned Child Counters should be marked fixable")
			}
			return
		}
	}
	t.Error("Orphaned Child Counters check not found")
}

func TestValidateOverallOK(t *testing.T) {
	allPass := []validateCheckResult{
		{check: doctorCheck{Status: statusOK}},
		{check: doctorCheck{Status: statusOK}},
	}
	if !validateOverallOK(allPass) {
		t.Error("Expected true when all checks pass")
	}

	hasWarning := []validateCheckResult{
		{check: doctorCheck{Status: statusOK}},
		{check: doctorCheck{Status: statusWarning}},
	}
	if validateOverallOK(hasWarning) {
		t.Error("Expected false when a check has warning")
	}

	hasError := []validateCheckResult{
		{check: doctorCheck{Status: statusOK}},
		{check: doctorCheck{Status: statusError}},
	}
	if validateOverallOK(hasError) {
		t.Error("Expected false when a check has error")
	}
}
