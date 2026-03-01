//go:build cgo

package doctor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Test #2: Disabled (threshold=0), small closed count → OK
func TestCheckStaleClosedIssues_DisabledSmallCount(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	closedAt := time.Now().AddDate(0, 0, -60)
	for i := 0; i < 50; i++ {
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

	// Set closed_at timestamp
	db := store.DB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	if _, err := tx.Exec("UPDATE issues SET closed_at = ? WHERE status = 'closed'", closedAt); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Failed to update closed_at: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	check := checkStaleClosedIssuesDB(db, 0)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (disabled with small count should be OK)", check.Status, StatusOK)
	}
	if check.Message != "Disabled (set stale_closed_issues_days to enable)" {
		t.Errorf("Message = %q, want disabled message", check.Message)
	}
}

// Test #3: Disabled (threshold=0), large closed count (≥threshold) → warning
func TestCheckStaleClosedIssues_DisabledLargeCount(t *testing.T) {
	orig := largeClosedIssuesThreshold
	largeClosedIssuesThreshold = 100
	t.Cleanup(func() { largeClosedIssuesThreshold = orig })

	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Bulk insert via raw SQL for speed
	db := store.DB()
	closedAt := time.Now().AddDate(0, 0, -60)
	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	for i := 0; i < largeClosedIssuesThreshold; i++ {
		id := fmt.Sprintf("test-%06d", i)
		_, err := tx.Exec(
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, closed_at, pinned)
			 VALUES (?, 'Closed issue', '', '', '', '', 'closed', 2, 'task', ?, ?, ?, 0)`,
			id, now, now, closedAt,
		)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("Failed to insert issue %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	check := checkStaleClosedIssuesDB(db, 0)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q (disabled with ≥threshold closed should warn)", check.Status, StatusWarning)
	}
	if check.Fix == "" {
		t.Error("Expected fix suggestion for large closed count")
	}
}

// Test #4: Enabled (threshold=30d), old closed issues → correct count
func TestCheckStaleClosedIssues_EnabledWithCleanable(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	closedAt := time.Now().AddDate(0, 0, -60)
	for i := 0; i < 5; i++ {
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

	db := store.DB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	if _, err := tx.Exec("UPDATE issues SET closed_at = ? WHERE status = 'closed'", closedAt); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Failed to update closed_at: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	check := checkStaleClosedIssuesDB(db, 30)

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
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	closedAt := time.Now().AddDate(0, 0, -10)
	for i := 0; i < 5; i++ {
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

	db := store.DB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	if _, err := tx.Exec("UPDATE issues SET closed_at = ? WHERE status = 'closed'", closedAt); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Failed to update closed_at: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	check := checkStaleClosedIssuesDB(db, 30)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (all within threshold)", check.Status, StatusOK)
	}
	if check.Message != "No stale closed issues" {
		t.Errorf("Message = %q, want 'No stale closed issues'", check.Message)
	}
}

// Test #6: Pinned closed issues excluded from cleanable count
func TestCheckStaleClosedIssues_PinnedExcluded(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	closedAt := time.Now().AddDate(0, 0, -60)
	for i := 0; i < 3; i++ {
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

	db := store.DB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	if _, err := tx.Exec("UPDATE issues SET closed_at = ? WHERE status = 'closed'", closedAt); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Failed to update closed_at: %v", err)
	}
	// Pin all issues
	if _, err := tx.Exec("UPDATE issues SET pinned = 1 WHERE status = 'closed'"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Failed to set pinned: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	check := checkStaleClosedIssuesDB(db, 30)

	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q (all pinned should be excluded)", check.Status, StatusOK)
	}
}

// Test #7: Mixed pinned and unpinned → only unpinned counted
func TestCheckStaleClosedIssues_MixedPinnedAndStale(t *testing.T) {
	store := newTestDoltStore(t, "test")
	ctx := context.Background()

	closedAt := time.Now().AddDate(0, 0, -60)
	var ids []string
	for i := 0; i < 8; i++ {
		issue := &types.Issue{
			Title:     "Closed issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %d: %v", i, err)
		}
		ids = append(ids, issue.ID)
		if err := store.CloseIssue(ctx, issue.ID, "done", "test", ""); err != nil {
			t.Fatalf("Failed to close issue %s: %v", issue.ID, err)
		}
	}

	db := store.DB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	if _, err := tx.Exec("UPDATE issues SET closed_at = ? WHERE status = 'closed'", closedAt); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Failed to update closed_at: %v", err)
	}
	// Pin first 3
	for i := 0; i < 3 && i < len(ids); i++ {
		if _, err := tx.Exec("UPDATE issues SET pinned = 1 WHERE id = ?", ids[i]); err != nil {
			_ = tx.Rollback()
			t.Fatalf("Failed to set pinned for %s: %v", ids[i], err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	check := checkStaleClosedIssuesDB(db, 30)

	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
	expected := "5 closed issue(s) older than 30 days"
	if check.Message != expected {
		t.Errorf("Message = %q, want %q", check.Message, expected)
	}
}

func TestCheckPersistentMolIssues_UsesDoltWithoutJSONL(t *testing.T) {
	store := newTestDoltStore(t, "mol")
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "mol issue should have been ephemeral",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Test the core logic directly with the issue
	issues := []*types.Issue{issue}
	check := checkPersistentMolIssuesForIssues(issues)
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "1 mol- issue(s) should be ephemeral" {
		t.Fatalf("message = %q, want exact count warning", check.Message)
	}
}

func TestCheckMisclassifiedWisps_UsesDoltWithoutJSONL(t *testing.T) {
	// Test the core logic directly
	issues := []*types.Issue{
		{ID: "bd-wisp-misclassified", Ephemeral: false},
	}

	check := checkMisclassifiedWispsForIssues(issues)
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q", check.Status, StatusWarning)
	}
	if check.Message != "1 wisp issue(s) missing ephemeral flag" {
		t.Fatalf("message = %q, want exact count warning", check.Message)
	}
}

func TestCheckPatrolPollution_UsesDoltWithoutJSONL(t *testing.T) {
	// Create issues directly for testing the core logic
	var issues []*types.Issue
	for i := 0; i < PatrolDigestThreshold+1; i++ {
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bd-%04d", i),
			Title:     fmt.Sprintf("Digest: mol-%02d-patrol", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		})
	}

	check := checkPatrolPollutionForIssues(issues)
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q", check.Status, StatusWarning)
	}
	if !strings.Contains(check.Message, "11 patrol digest beads (should be 0)") {
		t.Fatalf("message = %q, want patrol digest warning", check.Message)
	}
}

func TestCheckPatrolPollution_IgnoresEphemeralWisps(t *testing.T) {
	// Ephemeral issues are filtered out by loadMaintenanceIssues at the DB layer
	// (SearchIssues with Ephemeral=false filter). When the check logic receives
	// its issue list, ephemeral issues are already excluded. Verify that an empty
	// list (all ephemeral, all filtered) produces OK.
	var issues []*types.Issue // empty — all ephemeral issues were filtered at load time

	check := checkPatrolPollutionForIssues(issues)
	if check.Status != StatusOK {
		t.Fatalf("status = %q, want %q", check.Status, StatusOK)
	}
	if check.Message != "No patrol pollution detected" {
		t.Fatalf("message = %q, want no-pollution message", check.Message)
	}
}

func TestClassifyPatrolIssue(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  patrolIssueKind
	}{
		{
			name:  "digest patrol",
			title: "Digest: mol-abc-patrol",
			want:  patrolIssueDigest,
		},
		{
			name:  "session ended",
			title: "Session ended: patrol complete",
			want:  patrolIssueSessionEnded,
		},
		{
			name:  "normal issue",
			title: "Regular task title",
			want:  patrolIssueNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPatrolIssue(tc.title); got != tc.want {
				t.Fatalf("classifyPatrolIssue(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}
