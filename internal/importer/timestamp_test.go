package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestImportTimestampPrecedence verifies that imports respect updated_at timestamps (bd-e55c)
// When importing an issue with the same ID but different content, the newer version should win.
func TestImportTimestampPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Initialize storage
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Set up database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Create an issue locally at time T1
	now := time.Now()
	closedAt := now
	localIssue := &types.Issue{
		ID:          "bd-test123",
		Title:       "Test Issue",
		Description: "Local version",
		Status:      types.StatusClosed,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   now.Add(-2 * time.Hour),
		UpdatedAt:   now, // Newer timestamp
		ClosedAt:    &closedAt,
	}
	localIssue.ContentHash = localIssue.ComputeContentHash()

	if err := store.CreateIssue(ctx, localIssue, "test"); err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}

	// Simulate importing an older version from remote (e.g., from git pull)
	// This represents the scenario in bd-e55c where remote has status=open from yesterday
	olderRemoteIssue := &types.Issue{
		ID:          "bd-test123", // Same ID
		Title:       "Test Issue",
		Description: "Remote version",
		Status:      types.StatusOpen, // Different status
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   now.Add(-2 * time.Hour),
		UpdatedAt:   now.Add(-1 * time.Hour), // Older timestamp
	}
	olderRemoteIssue.ContentHash = olderRemoteIssue.ComputeContentHash()

	// Import the older remote version
	result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{olderRemoteIssue}, Options{
		SkipPrefixValidation: true,
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify that the import did NOT update the local version
	// The local version is newer, so it should be preserved
	if result.Updated > 0 {
		t.Errorf("Expected 0 updates, got %d - older remote should not overwrite newer local", result.Updated)
	}
	if result.Unchanged == 0 {
		t.Errorf("Expected unchanged count > 0, got %d", result.Unchanged)
	}

	// Verify the database still has the local (newer) version
	dbIssue, err := store.GetIssue(ctx, "bd-test123")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}

	if dbIssue.Status != types.StatusClosed {
		t.Errorf("Expected status=closed (local version), got status=%s", dbIssue.Status)
	}
	if dbIssue.Description != "Local version" {
		t.Errorf("Expected description='Local version', got '%s'", dbIssue.Description)
	}

	// Now test the reverse: importing a NEWER version should update
	newerRemoteIssue := &types.Issue{
		ID:          "bd-test123",
		Title:       "Test Issue",
		Description: "Even newer remote version",
		Status:      types.StatusOpen,
		Priority:    2, // Changed priority too
		IssueType:   types.TypeBug,
		CreatedAt:   now.Add(-2 * time.Hour),
		UpdatedAt:   now.Add(1 * time.Hour), // Newer than current DB
	}
	newerRemoteIssue.ContentHash = newerRemoteIssue.ComputeContentHash()

	result2, err := ImportIssues(ctx, dbPath, store, []*types.Issue{newerRemoteIssue}, Options{
		SkipPrefixValidation: true,
	})
	if err != nil {
		t.Fatalf("Import of newer version failed: %v", err)
	}

	if result2.Updated == 0 {
		t.Errorf("Expected 1 update, got 0 - newer remote should overwrite older local")
	}

	// Verify the database now has the newer remote version
	dbIssue2, err := store.GetIssue(ctx, "bd-test123")
	if err != nil {
		t.Fatalf("Failed to get issue after second import: %v", err)
	}

	if dbIssue2.Priority != 2 {
		t.Errorf("Expected priority=2 (newer remote), got %d", dbIssue2.Priority)
	}
	if dbIssue2.Description != "Even newer remote version" {
		t.Errorf("Expected description='Even newer remote version', got '%s'", dbIssue2.Description)
	}
}

// TestImportSameTimestamp tests behavior when timestamps are equal
func TestImportSameTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	now := time.Now()

	// Create local issue
	localIssue := &types.Issue{
		ID:          "bd-test456",
		Title:       "Test Issue",
		Description: "Local version",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	localIssue.ContentHash = localIssue.ComputeContentHash()

	if err := store.CreateIssue(ctx, localIssue, "test"); err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}

	// Import with SAME timestamp but different content
	remoteIssue := &types.Issue{
		ID:          "bd-test456",
		Title:       "Test Issue",
		Description: "Remote version",
		Status:      types.StatusInProgress,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now, // Same timestamp
	}
	remoteIssue.ContentHash = remoteIssue.ComputeContentHash()

	result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{remoteIssue}, Options{
		SkipPrefixValidation: true,
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// With equal timestamps, we should NOT update (local wins)
	if result.Updated > 0 {
		t.Errorf("Expected 0 updates with equal timestamps, got %d", result.Updated)
	}

	// Verify local version is preserved
	dbIssue, err := store.GetIssue(ctx, "bd-test456")
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}

	if dbIssue.Description != "Local version" {
		t.Errorf("Expected local version to be preserved, got '%s'", dbIssue.Description)
	}
}

// TestImportTimestampAwareProtection tests the GH#865 fix: timestamp-aware snapshot protection
// The ProtectLocalExportIDs map should only protect issues if the local snapshot version is newer.
func TestImportTimestampAwareProtection(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	now := time.Now()

	// Create a local issue in the database
	localIssue := &types.Issue{
		ID:          "bd-protect1",
		Title:       "Test Issue",
		Description: "Local version",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   now.Add(-2 * time.Hour),
		UpdatedAt:   now.Add(-1 * time.Hour), // DB has old timestamp
	}
	localIssue.ContentHash = localIssue.ComputeContentHash()

	if err := store.CreateIssue(ctx, localIssue, "test"); err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}

	t.Run("incoming newer than local snapshot - should update", func(t *testing.T) {
		// Scenario: Remote closed the issue after we exported locally
		// Local snapshot: issue open at T1 (10:00)
		// Incoming: issue closed at T2 (11:30) - NEWER
		// Expected: Update should proceed (remote is newer)

		snapshotTime := now.Add(-30 * time.Minute) // Local snapshot at 10:00
		incomingTime := now                        // Incoming at 11:30 (newer)
		closedAt := incomingTime

		incomingIssue := &types.Issue{
			ID:          "bd-protect1",
			Title:       "Test Issue",
			Description: "Remote closed version",
			Status:      types.StatusClosed,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   now.Add(-2 * time.Hour),
			UpdatedAt:   incomingTime,
			ClosedAt:    &closedAt,
		}
		incomingIssue.ContentHash = incomingIssue.ComputeContentHash()

		// Protection map has the issue with the local snapshot timestamp
		protectMap := map[string]time.Time{
			"bd-protect1": snapshotTime,
		}

		result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{incomingIssue}, Options{
			SkipPrefixValidation:  true,
			ProtectLocalExportIDs: protectMap,
		})
		if err != nil {
			t.Fatalf("Import failed: %v", err)
		}

		// Incoming is newer than snapshot, so update should proceed
		if result.Updated == 0 {
			t.Errorf("Expected 1 update (incoming newer than snapshot), got 0")
		}
		if result.Skipped > 0 {
			t.Errorf("Expected 0 skipped, got %d", result.Skipped)
		}

		// Verify the issue was updated to closed
		dbIssue, err := store.GetIssue(ctx, "bd-protect1")
		if err != nil {
			t.Fatalf("Failed to get issue: %v", err)
		}
		if dbIssue.Status != types.StatusClosed {
			t.Errorf("Expected status=closed (remote version), got %s", dbIssue.Status)
		}
	})

	t.Run("incoming older than local snapshot - should protect", func(t *testing.T) {
		// Reset the issue for next test
		resetIssue := &types.Issue{
			ID:          "bd-protect2",
			Title:       "Another Issue",
			Description: "Local modified version",
			Status:      types.StatusInProgress,
			Priority:    2,
			IssueType:   types.TypeBug,
			CreatedAt:   now.Add(-2 * time.Hour),
			UpdatedAt:   now, // Current timestamp
		}
		resetIssue.ContentHash = resetIssue.ComputeContentHash()
		if err := store.CreateIssue(ctx, resetIssue, "test"); err != nil {
			t.Fatalf("Failed to create reset issue: %v", err)
		}

		// Scenario: We modified locally, remote has older version
		// Local snapshot: issue in_progress at T2 (11:30)
		// Incoming: issue open at T1 (10:00) - OLDER
		// Expected: Skip update (protect local changes)

		snapshotTime := now                        // Local snapshot at 11:30
		incomingTime := now.Add(-30 * time.Minute) // Incoming at 10:00 (older)

		incomingIssue := &types.Issue{
			ID:          "bd-protect2",
			Title:       "Another Issue",
			Description: "Old remote version",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeBug,
			CreatedAt:   now.Add(-2 * time.Hour),
			UpdatedAt:   incomingTime,
		}
		incomingIssue.ContentHash = incomingIssue.ComputeContentHash()

		// Protection map has the issue with the local snapshot timestamp
		protectMap := map[string]time.Time{
			"bd-protect2": snapshotTime,
		}

		result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{incomingIssue}, Options{
			SkipPrefixValidation:  true,
			ProtectLocalExportIDs: protectMap,
		})
		if err != nil {
			t.Fatalf("Import failed: %v", err)
		}

		// Incoming is older than snapshot, so update should be skipped (protected)
		if result.Skipped == 0 {
			t.Errorf("Expected 1 skipped (local snapshot newer), got 0")
		}
		if result.Updated > 0 {
			t.Errorf("Expected 0 updates, got %d", result.Updated)
		}

		// Verify the issue was NOT updated
		dbIssue, err := store.GetIssue(ctx, "bd-protect2")
		if err != nil {
			t.Fatalf("Failed to get issue: %v", err)
		}
		if dbIssue.Status != types.StatusInProgress {
			t.Errorf("Expected status=in_progress (protected local), got %s", dbIssue.Status)
		}
	})

	t.Run("issue not in protection map - normal behavior", func(t *testing.T) {
		// Create an issue not in the protection map
		unprotectedIssue := &types.Issue{
			ID:          "bd-unprotected",
			Title:       "Unprotected Issue",
			Description: "Original",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   now.Add(-2 * time.Hour),
			UpdatedAt:   now.Add(-1 * time.Hour),
		}
		unprotectedIssue.ContentHash = unprotectedIssue.ComputeContentHash()
		if err := store.CreateIssue(ctx, unprotectedIssue, "test"); err != nil {
			t.Fatalf("Failed to create unprotected issue: %v", err)
		}

		// Incoming version is newer
		closedAt := now
		incomingIssue := &types.Issue{
			ID:          "bd-unprotected",
			Title:       "Unprotected Issue",
			Description: "Updated",
			Status:      types.StatusClosed,
			Priority:    1,
			IssueType:   types.TypeTask,
			CreatedAt:   now.Add(-2 * time.Hour),
			UpdatedAt:   now, // Newer than DB
			ClosedAt:    &closedAt,
		}
		incomingIssue.ContentHash = incomingIssue.ComputeContentHash()

		// Protection map does NOT contain this issue
		protectMap := map[string]time.Time{
			"bd-other": now, // Different issue
		}

		result, err := ImportIssues(ctx, dbPath, store, []*types.Issue{incomingIssue}, Options{
			SkipPrefixValidation:  true,
			ProtectLocalExportIDs: protectMap,
		})
		if err != nil {
			t.Fatalf("Import failed: %v", err)
		}

		// Issue not in protection map, incoming is newer - should update
		if result.Updated == 0 {
			t.Errorf("Expected 1 update (not in protection map), got 0")
		}

		dbIssue, err := store.GetIssue(ctx, "bd-unprotected")
		if err != nil {
			t.Fatalf("Failed to get issue: %v", err)
		}
		if dbIssue.Status != types.StatusClosed {
			t.Errorf("Expected status=closed (updated), got %s", dbIssue.Status)
		}
	})
}

// TestShouldProtectFromUpdate tests the helper function directly
func TestShouldProtectFromUpdate(t *testing.T) {
	now := time.Now()

	t.Run("nil map - no protection", func(t *testing.T) {
		if shouldProtectFromUpdate("bd-123", now, nil) {
			t.Error("Expected no protection with nil map")
		}
	})

	t.Run("issue not in map - no protection", func(t *testing.T) {
		protectMap := map[string]time.Time{
			"bd-other": now,
		}
		if shouldProtectFromUpdate("bd-123", now, protectMap) {
			t.Error("Expected no protection when issue not in map")
		}
	})

	t.Run("incoming newer than local - no protection", func(t *testing.T) {
		localTime := now.Add(-1 * time.Hour)
		protectMap := map[string]time.Time{
			"bd-123": localTime,
		}
		if shouldProtectFromUpdate("bd-123", now, protectMap) {
			t.Error("Expected no protection when incoming is newer")
		}
	})

	t.Run("incoming same as local - protect", func(t *testing.T) {
		protectMap := map[string]time.Time{
			"bd-123": now,
		}
		if !shouldProtectFromUpdate("bd-123", now, protectMap) {
			t.Error("Expected protection when timestamps are equal")
		}
	})

	t.Run("incoming older than local - protect", func(t *testing.T) {
		localTime := now.Add(1 * time.Hour) // Local is newer
		protectMap := map[string]time.Time{
			"bd-123": localTime,
		}
		if !shouldProtectFromUpdate("bd-123", now, protectMap) {
			t.Error("Expected protection when local is newer")
		}
	})
}

func TestMain(m *testing.M) {
	// Ensure test DB files are cleaned up
	code := m.Run()
	os.Exit(code)
}
