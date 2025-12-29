package sqlite

import (
	"database/sql"
	"testing"
)

func TestCaptureSnapshot(t *testing.T) {
	db := setupInvariantTestDB(t)
	defer db.Close()

	// Create some test data
	_, err := db.Exec(`INSERT INTO issues (id, title) VALUES ('test-1', 'Test Issue')`)
	if err != nil {
		t.Fatalf("failed to insert test issue: %v", err)
	}

	_, err = db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, created_by) VALUES ('test-1', 'test-1', 'test')`)
	if err != nil {
		t.Fatalf("failed to insert test dependency: %v", err)
	}

	_, err = db.Exec(`INSERT INTO labels (issue_id, label) VALUES ('test-1', 'test-label')`)
	if err != nil {
		t.Fatalf("failed to insert test label: %v", err)
	}

	snapshot, err := captureSnapshot(db)
	if err != nil {
		t.Fatalf("captureSnapshot failed: %v", err)
	}

	if snapshot.IssueCount != 1 {
		t.Errorf("expected IssueCount=1, got %d", snapshot.IssueCount)
	}

	if snapshot.DependencyCount != 1 {
		t.Errorf("expected DependencyCount=1, got %d", snapshot.DependencyCount)
	}

	if snapshot.LabelCount != 1 {
		t.Errorf("expected LabelCount=1, got %d", snapshot.LabelCount)
	}
}

func TestCheckRequiredConfig(t *testing.T) {
	db := setupInvariantTestDB(t)
	defer db.Close()

	// Test with no issues - should pass even without issue_prefix
	snapshot := &Snapshot{IssueCount: 0}
	err := checkRequiredConfig(db, snapshot)
	if err != nil {
		t.Errorf("expected no error with 0 issues, got: %v", err)
	}

	// Add an issue to the database
	_, err = db.Exec(`INSERT INTO issues (id, title) VALUES ('test-1', 'Test Issue')`)
	if err != nil {
		t.Fatalf("failed to insert issue: %v", err)
	}

	// Delete issue_prefix config
	_, err = db.Exec(`DELETE FROM config WHERE key = 'issue_prefix'`)
	if err != nil {
		t.Fatalf("failed to delete config: %v", err)
	}

	// Should fail now that we have an issue but no prefix
	err = checkRequiredConfig(db, snapshot)
	if err == nil {
		t.Error("expected error for missing issue_prefix with issues, got nil")
	}

	// Add required config back
	_, err = db.Exec(`INSERT INTO config (key, value) VALUES ('issue_prefix', 'test')`)
	if err != nil {
		t.Fatalf("failed to insert config: %v", err)
	}

	// Test with required config present
	err = checkRequiredConfig(db, snapshot)
	if err != nil {
		t.Errorf("expected no error with issue_prefix set, got: %v", err)
	}
}

func TestCheckForeignKeys(t *testing.T) {
	db := setupInvariantTestDB(t)
	defer db.Close()

	snapshot := &Snapshot{}

	// Test with no data - should pass
	err := checkForeignKeys(db, snapshot)
	if err != nil {
		t.Errorf("expected no error with empty db, got: %v", err)
	}

	// Create an issue
	_, err = db.Exec(`INSERT INTO issues (id, title) VALUES ('test-1', 'Test Issue')`)
	if err != nil {
		t.Fatalf("failed to insert test issue: %v", err)
	}

	// Add valid dependency
	_, err = db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, created_by) VALUES ('test-1', 'test-1', 'test')`)
	if err != nil {
		t.Fatalf("failed to insert dependency: %v", err)
	}

	// Should pass with valid foreign keys
	err = checkForeignKeys(db, snapshot)
	if err != nil {
		t.Errorf("expected no error with valid dependencies, got: %v", err)
	}

	// Manually create orphaned dependency (bypassing FK constraints for testing)
	_, err = db.Exec(`PRAGMA foreign_keys = OFF`)
	if err != nil {
		t.Fatalf("failed to disable foreign keys: %v", err)
	}

	_, err = db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, created_by) VALUES ('orphan-1', 'test-1', 'test')`)
	if err != nil {
		t.Fatalf("failed to insert orphaned dependency: %v", err)
	}

	_, err = db.Exec(`PRAGMA foreign_keys = ON`)
	if err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	// Should fail with orphaned dependency
	err = checkForeignKeys(db, snapshot)
	if err == nil {
		t.Error("expected error for orphaned dependency, got nil")
	}
}

func TestCheckIssueCount(t *testing.T) {
	db := setupInvariantTestDB(t)
	defer db.Close()

	// Create initial issue
	_, err := db.Exec(`INSERT INTO issues (id, title) VALUES ('test-1', 'Test Issue')`)
	if err != nil {
		t.Fatalf("failed to insert test issue: %v", err)
	}

	snapshot, err := captureSnapshot(db)
	if err != nil {
		t.Fatalf("captureSnapshot failed: %v", err)
	}

	// Same count - should pass
	err = checkIssueCount(db, snapshot)
	if err != nil {
		t.Errorf("expected no error with same count, got: %v", err)
	}

	// Add an issue - should pass (count increased)
	_, err = db.Exec(`INSERT INTO issues (id, title) VALUES ('test-2', 'Test Issue 2')`)
	if err != nil {
		t.Fatalf("failed to insert second issue: %v", err)
	}

	err = checkIssueCount(db, snapshot)
	if err != nil {
		t.Errorf("expected no error with increased count, got: %v", err)
	}

	// Delete both issues to simulate data loss
	_, err = db.Exec(`DELETE FROM issues`)
	if err != nil {
		t.Fatalf("failed to delete issues: %v", err)
	}

	// Should fail when count decreased
	err = checkIssueCount(db, snapshot)
	if err == nil {
		t.Error("expected error for decreased issue count, got nil")
	}
}

func TestVerifyInvariants(t *testing.T) {
	db := setupInvariantTestDB(t)
	defer db.Close()

	snapshot, err := captureSnapshot(db)
	if err != nil {
		t.Fatalf("captureSnapshot failed: %v", err)
	}

	// All invariants should pass with empty database
	err = verifyInvariants(db, snapshot)
	if err != nil {
		t.Errorf("expected no errors with empty db, got: %v", err)
	}

	// Add an issue (which requires issue_prefix)
	_, err = db.Exec(`INSERT INTO issues (id, title) VALUES ('test-1', 'Test Issue')`)
	if err != nil {
		t.Fatalf("failed to insert issue: %v", err)
	}

	// Capture new snapshot with issue
	snapshot, err = captureSnapshot(db)
	if err != nil {
		t.Fatalf("captureSnapshot failed: %v", err)
	}

	// Should still pass (issue_prefix is set by newTestStore)
	err = verifyInvariants(db, snapshot)
	if err != nil {
		t.Errorf("expected no errors with issue and prefix, got: %v", err)
	}

	// Remove required config to trigger failure
	_, err = db.Exec(`DELETE FROM config WHERE key = 'issue_prefix'`)
	if err != nil {
		t.Fatalf("failed to delete config: %v", err)
	}

	err = verifyInvariants(db, snapshot)
	if err == nil {
		t.Error("expected error when issue_prefix missing with issues, got nil")
	}
}

func TestGetInvariantNames(t *testing.T) {
	names := GetInvariantNames()

	expectedNames := []string{
		"foreign_keys_valid",
		"issue_count_stable",
		"required_config_present",
	}

	if len(names) != len(expectedNames) {
		t.Errorf("expected %d invariants, got %d", len(expectedNames), len(names))
	}

	for i, name := range names {
		if name != expectedNames[i] {
			t.Errorf("expected invariant[%d]=%s, got %s", i, expectedNames[i], name)
		}
	}
}

func TestCleanOrphanedRefs(t *testing.T) {
	db := setupInvariantTestDB(t)
	defer db.Close()

	// Create a valid issue
	_, err := db.Exec(`INSERT INTO issues (id, title) VALUES ('test-1', 'Test Issue')`)
	if err != nil {
		t.Fatalf("failed to insert test issue: %v", err)
	}

	// Create valid dependency and label
	_, err = db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, created_by) VALUES ('test-1', 'test-1', 'test')`)
	if err != nil {
		t.Fatalf("failed to insert valid dependency: %v", err)
	}

	_, err = db.Exec(`INSERT INTO labels (issue_id, label) VALUES ('test-1', 'valid-label')`)
	if err != nil {
		t.Fatalf("failed to insert valid label: %v", err)
	}

	// Disable FK constraints to create orphaned refs
	_, err = db.Exec(`PRAGMA foreign_keys = OFF`)
	if err != nil {
		t.Fatalf("failed to disable foreign keys: %v", err)
	}

	// Create orphaned dependency (issue_id not in issues)
	_, err = db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, created_by) VALUES ('orphan-1', 'test-1', 'test')`)
	if err != nil {
		t.Fatalf("failed to insert orphaned dependency (issue_id): %v", err)
	}

	// Create orphaned dependency (depends_on_id not in issues)
	_, err = db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, created_by) VALUES ('test-1', 'orphan-2', 'test')`)
	if err != nil {
		t.Fatalf("failed to insert orphaned dependency (depends_on_id): %v", err)
	}

	// Create external dependency (should NOT be cleaned)
	_, err = db.Exec(`INSERT INTO dependencies (issue_id, depends_on_id, created_by) VALUES ('test-1', 'external:proj:cap', 'test')`)
	if err != nil {
		t.Fatalf("failed to insert external dependency: %v", err)
	}

	// Create orphaned label
	_, err = db.Exec(`INSERT INTO labels (issue_id, label) VALUES ('orphan-3', 'orphan-label')`)
	if err != nil {
		t.Fatalf("failed to insert orphaned label: %v", err)
	}

	_, err = db.Exec(`PRAGMA foreign_keys = ON`)
	if err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	// Verify we have orphaned refs (checkForeignKeys should fail)
	err = checkForeignKeys(db, &Snapshot{})
	if err == nil {
		t.Error("expected checkForeignKeys to fail with orphaned refs")
	}

	// Run cleanup
	deps, labels, err := CleanOrphanedRefs(db)
	if err != nil {
		t.Fatalf("CleanOrphanedRefs failed: %v", err)
	}

	// Should have cleaned 2 orphaned deps (orphan-1 and orphan-2, not external)
	if deps != 2 {
		t.Errorf("expected 2 orphaned deps cleaned, got %d", deps)
	}

	// Should have cleaned 1 orphaned label
	if labels != 1 {
		t.Errorf("expected 1 orphaned label cleaned, got %d", labels)
	}

	// Verify checkForeignKeys now passes
	err = checkForeignKeys(db, &Snapshot{})
	if err != nil {
		t.Errorf("expected checkForeignKeys to pass after cleanup, got: %v", err)
	}

	// Verify valid data is still present
	var depCount, labelCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&depCount)
	if err != nil {
		t.Fatalf("failed to count dependencies: %v", err)
	}
	// Should have 2: valid self-ref + external ref
	if depCount != 2 {
		t.Errorf("expected 2 dependencies remaining, got %d", depCount)
	}

	err = db.QueryRow(`SELECT COUNT(*) FROM labels`).Scan(&labelCount)
	if err != nil {
		t.Fatalf("failed to count labels: %v", err)
	}
	// Should have 1: valid label
	if labelCount != 1 {
		t.Errorf("expected 1 label remaining, got %d", labelCount)
	}
}

// setupInvariantTestDB creates an in-memory test database with schema
func setupInvariantTestDB(t *testing.T) *sql.DB {
	t.Helper()

	store := newTestStore(t, ":memory:")
	t.Cleanup(func() { _ = store.Close() })

	// Return the underlying database connection
	return store.db
}
