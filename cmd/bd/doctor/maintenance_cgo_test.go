//go:build cgo

package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// setupStaleClosedTestDB creates a dolt store with n closed issues and returns tmpDir.
// closedAt sets the closed_at timestamp. pinnedIndices marks specific issues as pinned.
// For small counts, uses the store API. For large counts (>100), uses raw SQL bulk insert.
func setupStaleClosedTestDB(t *testing.T, numClosed int, closedAt time.Time, pinnedIndices map[int]bool, thresholdDays int) string {
	t.Helper()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.StaleClosedIssuesDays = thresholdDays
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "dolt")
	ctx := context.Background()

	store, err := dolt.New(ctx, &dolt.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	db := store.UnderlyingDB()
	if db == nil {
		t.Fatal("UnderlyingDB returned nil")
	}

	if numClosed <= 100 {
		// Small count: use store API for realistic data
		for i := 0; i < numClosed; i++ {
			issue := &types.Issue{
				Title:     "Closed issue",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := store.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("Failed to create issue %d: %v", i, err)
			}
			if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
				t.Fatalf("Failed to close issue %s: %v", issue.ID, err)
			}
		}
	} else {
		// Large count: raw SQL bulk insert for speed
		now := time.Now().UTC()
		for i := 0; i < numClosed; i++ {
			id := fmt.Sprintf("test-%06d", i)
			_, err := db.Exec(
				`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, closed_at, pinned)
				 VALUES (?, 'Closed issue', '', '', '', '', 'closed', 2, 'task', ?, ?, ?, 0)`,
				id, now, now, closedAt,
			)
			if err != nil {
				t.Fatalf("Failed to insert issue %d: %v", i, err)
			}
		}
	}

	// Set closed_at for store-API-created issues
	if numClosed <= 100 {
		_, err = db.Exec("UPDATE issues SET closed_at = ? WHERE status = 'closed'", closedAt)
		if err != nil {
			t.Fatalf("Failed to update closed_at: %v", err)
		}
	}

	// Set pinned flag for specified indices
	if len(pinnedIndices) > 0 {
		rows, err := db.Query("SELECT id FROM issues WHERE status = 'closed' ORDER BY id")
		if err != nil {
			t.Fatalf("Failed to query IDs: %v", err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("Failed to scan ID: %v", err)
			}
			ids = append(ids, id)
		}
		rows.Close()

		for idx := range pinnedIndices {
			if idx < len(ids) {
				if _, err := db.Exec("UPDATE issues SET pinned = 1 WHERE id = ?", ids[idx]); err != nil {
					t.Fatalf("Failed to set pinned for %s: %v", ids[idx], err)
				}
			}
		}
	}

	return tmpDir
}

// Test #2: Disabled (threshold=0), small closed count → OK
func TestCheckStaleClosedIssues_DisabledSmallCount(t *testing.T) {
	tmpDir := setupStaleClosedTestDB(t, 50, time.Now().AddDate(0, 0, -60), nil, 0)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (disabled with small count should be OK)", check.Status, StatusOK)
	}
	if check.Message != "Disabled (set stale_closed_issues_days to enable)" {
		t.Errorf("Message = %q, want disabled message", check.Message)
	}
}

// Test #3: Disabled (threshold=0), large closed count (≥10k) → warning
func TestCheckStaleClosedIssues_DisabledLargeCount(t *testing.T) {
	tmpDir := setupStaleClosedTestDB(t, largeClosedIssuesThreshold, time.Now().AddDate(0, 0, -60), nil, 0)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (disabled with ≥10k closed should warn)", check.Status, StatusWarning)
	}
	if check.Fix == "" {
		t.Error("Expected fix suggestion for large closed count")
	}
}

// Test #4: Enabled (threshold=30d), old closed issues → correct count
func TestCheckStaleClosedIssues_EnabledWithCleanable(t *testing.T) {
	tmpDir := setupStaleClosedTestDB(t, 5, time.Now().AddDate(0, 0, -60), nil, 30)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	expected := "5 closed issue(s) older than 30 days"
	if check.Message != expected {
		t.Errorf("Message = %q, want %q", check.Message, expected)
	}
}

// Test #5: Enabled (threshold=30d), all closed recently → OK
func TestCheckStaleClosedIssues_EnabledNoneCleanable(t *testing.T) {
	tmpDir := setupStaleClosedTestDB(t, 5, time.Now().AddDate(0, 0, -10), nil, 30)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (all within threshold)", check.Status, StatusOK)
	}
	if check.Message != "No stale closed issues" {
		t.Errorf("Message = %q, want 'No stale closed issues'", check.Message)
	}
}

// Test #6: Pinned closed issues excluded from cleanable count
func TestCheckStaleClosedIssues_PinnedExcluded(t *testing.T) {
	pinned := map[int]bool{0: true, 1: true, 2: true}
	tmpDir := setupStaleClosedTestDB(t, 3, time.Now().AddDate(0, 0, -60), pinned, 30)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (all pinned should be excluded)", check.Status, StatusOK)
	}
}

// Test #7: Mixed pinned and unpinned → only unpinned counted
func TestCheckStaleClosedIssues_MixedPinnedAndStale(t *testing.T) {
	pinned := map[int]bool{0: true, 1: true, 2: true}
	tmpDir := setupStaleClosedTestDB(t, 8, time.Now().AddDate(0, 0, -60), pinned, 30)

	check := CheckStaleClosedIssues(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	expected := "5 closed issue(s) older than 30 days"
	if check.Message != expected {
		t.Errorf("Message = %q, want %q", check.Message, expected)
	}
}
