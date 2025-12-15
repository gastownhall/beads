package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestValidateBatchIssues(t *testing.T) {
	t.Run("validates all issues in batch", func(t *testing.T) {
		issues := []*types.Issue{
			{Title: "Valid issue 1", Priority: 1, IssueType: "task", Status: "open"},
			{Title: "Valid issue 2", Priority: 2, IssueType: "bug", Status: "open"},
		}

		err := validateBatchIssues(issues)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}

		// Verify timestamps were set
		for i, issue := range issues {
			if issue.CreatedAt.IsZero() {
				t.Errorf("issue %d CreatedAt should be set", i)
			}
			if issue.UpdatedAt.IsZero() {
				t.Errorf("issue %d UpdatedAt should be set", i)
			}
		}
	})

	t.Run("preserves provided timestamps", func(t *testing.T) {
		now := time.Now()
		pastTime := now.Add(-24 * time.Hour)

		issues := []*types.Issue{
			{
				Title:     "Issue with timestamp",
				Priority:  1,
				IssueType: "task",
				Status:    "open",
				CreatedAt: pastTime,
				UpdatedAt: pastTime,
			},
		}

		err := validateBatchIssues(issues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !issues[0].CreatedAt.Equal(pastTime) {
			t.Error("CreatedAt should be preserved")
		}
		if !issues[0].UpdatedAt.Equal(pastTime) {
			t.Error("UpdatedAt should be preserved")
		}
	})

	t.Run("rejects nil issue", func(t *testing.T) {
		issues := []*types.Issue{
			{Title: "Valid issue", Priority: 1, IssueType: "task", Status: "open"},
			nil,
		}

		err := validateBatchIssues(issues)
		if err == nil {
			t.Error("expected error for nil issue")
		}
		if !strings.Contains(err.Error(), "issue 1 is nil") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("rejects invalid issue", func(t *testing.T) {
		issues := []*types.Issue{
			{Title: "", Priority: 1, IssueType: "task", Status: "open"}, // invalid: empty title
		}

		err := validateBatchIssues(issues)
		if err == nil {
			t.Error("expected validation error")
		}
		if !strings.Contains(err.Error(), "validation failed") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestBatchCreateIssues(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("creates multiple issues atomically", func(t *testing.T) {
		issues := []*types.Issue{
			{Title: "Batch issue 1", Priority: 1, IssueType: "task", Status: "open", Description: "First issue"},
			{Title: "Batch issue 2", Priority: 2, IssueType: "bug", Status: "open", Description: "Second issue"},
			{Title: "Batch issue 3", Priority: 1, IssueType: "feature", Status: "open", Description: "Third issue"},
		}

		err := s.CreateIssues(ctx, issues, "test-actor")
		if err != nil {
			t.Fatalf("failed to create issues: %v", err)
		}

		// Verify all issues were created
		for i, issue := range issues {
			if issue.ID == "" {
				t.Errorf("issue %d ID should be generated", i)
			}

			got, err := s.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Errorf("failed to get issue %d: %v", i, err)
			}
			if got.Title != issue.Title {
				t.Errorf("issue %d title mismatch: want %q, got %q", i, issue.Title, got.Title)
			}
		}
	})

	t.Run("rolls back on validation error", func(t *testing.T) {
		issues := []*types.Issue{
			{Title: "Valid issue", Priority: 1, IssueType: "task", Status: "open"},
			{Title: "", Priority: 1, IssueType: "task", Status: "open"}, // invalid: empty title
		}

		err := s.CreateIssues(ctx, issues, "test-actor")
		if err == nil {
			t.Fatal("expected validation error")
		}

		// Verify no issues were created
		if issues[0].ID != "" {
			_, err := s.GetIssue(ctx, issues[0].ID)
			if err == nil {
				t.Error("first issue should not have been created (transaction rollback)")
			}
		}
	})

	t.Run("handles empty batch", func(t *testing.T) {
		var issues []*types.Issue
		err := s.CreateIssues(ctx, issues, "test-actor")
		if err != nil {
			t.Errorf("empty batch should succeed: %v", err)
		}
	})

	t.Run("handles explicit IDs", func(t *testing.T) {
		prefix := "bd"
		issues := []*types.Issue{
			{ID: prefix + "-explicit1", Title: "Explicit ID 1", Priority: 1, IssueType: "task", Status: "open"},
			{ID: prefix + "-explicit2", Title: "Explicit ID 2", Priority: 1, IssueType: "task", Status: "open"},
		}

		err := s.CreateIssues(ctx, issues, "test-actor")
		if err != nil {
			t.Fatalf("failed to create issues with explicit IDs: %v", err)
		}

		// Verify IDs were preserved
		for i, issue := range issues {
			got, err := s.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatalf("failed to get issue %d: %v", i, err)
			}
			if got.ID != issue.ID {
				t.Errorf("issue %d ID mismatch: want %q, got %q", i, issue.ID, got.ID)
			}
		}
	})

	t.Run("handles mix of explicit and generated IDs", func(t *testing.T) {
		prefix := "bd"
		issues := []*types.Issue{
			{ID: prefix + "-mixed1", Title: "Explicit ID", Priority: 1, IssueType: "task", Status: "open"},
			{Title: "Generated ID", Priority: 1, IssueType: "task", Status: "open"},
		}

		err := s.CreateIssues(ctx, issues, "test-actor")
		if err != nil {
			t.Fatalf("failed to create issues: %v", err)
		}

		// Verify both IDs are valid
		if issues[0].ID != prefix+"-mixed1" {
			t.Errorf("explicit ID should be preserved, got %q", issues[0].ID)
		}
		if issues[1].ID == "" || !strings.HasPrefix(issues[1].ID, prefix+"-") {
			t.Errorf("ID should be generated with correct prefix, got %q", issues[1].ID)
		}
	})

	t.Run("rejects wrong prefix", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "wrong-prefix-123", Title: "Wrong prefix", Priority: 1, IssueType: "task", Status: "open"},
		}

		err := s.CreateIssues(ctx, issues, "test-actor")
		if err == nil {
			t.Fatal("expected error for wrong prefix")
		}
		if !strings.Contains(err.Error(), "does not match configured prefix") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("marks issues dirty", func(t *testing.T) {
		issues := []*types.Issue{
			{Title: "Dirty test", Priority: 1, IssueType: "task", Status: "open"},
		}

		err := s.CreateIssues(ctx, issues, "test-actor")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Verify issue is marked dirty
		var count int
		err = s.db.QueryRow(`SELECT COUNT(*) FROM dirty_issues WHERE issue_id = ?`, issues[0].ID).Scan(&count)
		if err != nil {
			t.Fatalf("failed to check dirty status: %v", err)
		}
		if count != 1 {
			t.Error("issue should be marked dirty")
		}
	})

	t.Run("sets content hash", func(t *testing.T) {
		issues := []*types.Issue{
			{Title: "Hash test", Description: "Test content hash", Priority: 1, IssueType: "task", Status: "open"},
		}

		err := s.CreateIssues(ctx, issues, "test-actor")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Verify content hash was set
		got, err := s.GetIssue(ctx, issues[0].ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}
		if got.ContentHash == "" {
			t.Error("content hash should be set")
		}
	})
}

func TestGenerateBatchIDs(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("generates unique IDs for batch", func(t *testing.T) {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("failed to get connection: %v", err)
		}
		defer conn.Close()

		issues := []*types.Issue{
			{Title: "Issue 1", Description: "First", CreatedAt: time.Now()},
			{Title: "Issue 2", Description: "Second", CreatedAt: time.Now()},
			{Title: "Issue 3", Description: "Third", CreatedAt: time.Now()},
		}

		err = s.generateBatchIDs(ctx, conn, issues, "test-actor", OrphanAllow, false)
		if err != nil {
			t.Fatalf("failed to generate IDs: %v", err)
		}

		// Verify all IDs are unique
		seen := make(map[string]bool)
		for i, issue := range issues {
			if issue.ID == "" {
				t.Errorf("issue %d ID should be generated", i)
			}
			if seen[issue.ID] {
				t.Errorf("duplicate ID generated: %s", issue.ID)
			}
			seen[issue.ID] = true
		}
	})

	t.Run("validates explicit IDs match prefix", func(t *testing.T) {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("failed to get connection: %v", err)
		}
		defer conn.Close()

		issues := []*types.Issue{
			{ID: "wrong-prefix-123", Title: "Wrong", CreatedAt: time.Now()},
		}

		err = s.generateBatchIDs(ctx, conn, issues, "test-actor", OrphanAllow, false)
		if err == nil {
			t.Fatal("expected error for wrong prefix")
		}
	})

	t.Run("skips prefix validation when flag is set", func(t *testing.T) {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("failed to get connection: %v", err)
		}
		defer conn.Close()

		issues := []*types.Issue{
			{ID: "wrong-prefix-123", Title: "Wrong", CreatedAt: time.Now()},
		}

		// With skipPrefixValidation=true, should not error
		err = s.generateBatchIDs(ctx, conn, issues, "test-actor", OrphanAllow, true)
		if err != nil {
			t.Fatalf("should not error with skipPrefixValidation=true: %v", err)
		}
	})
}

func TestBulkOperations(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("bulkInsertIssues", func(t *testing.T) {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("failed to get connection: %v", err)
		}
		defer conn.Close()

		prefix := "bd"
		now := time.Now()
		issues := []*types.Issue{
			{
				ID:          prefix + "-bulk1",
				ContentHash: "hash1",
				Title:       "Bulk 1",
				Description: "First",
				Priority:    1,
				IssueType:   "task",
				Status:      "open",
				CreatedAt:   now,
				UpdatedAt:   now,
			},
			{
				ID:          prefix + "-bulk2",
				ContentHash: "hash2",
				Title:       "Bulk 2",
				Description: "Second",
				Priority:    1,
				IssueType:   "task",
				Status:      "open",
				CreatedAt:   now,
				UpdatedAt:   now,
			},
		}

		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer conn.ExecContext(context.Background(), "ROLLBACK")

		err = bulkInsertIssues(ctx, conn, issues)
		if err != nil {
			t.Fatalf("failed to bulk insert: %v", err)
		}

		conn.ExecContext(ctx, "COMMIT")

		// Verify issues were inserted
		for _, issue := range issues {
			got, err := s.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Errorf("failed to get issue %s: %v", issue.ID, err)
			}
			if got.Title != issue.Title {
				t.Errorf("title mismatch for %s", issue.ID)
			}
		}
	})

	t.Run("bulkRecordEvents", func(t *testing.T) {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("failed to get connection: %v", err)
		}
		defer conn.Close()

		// Create test issues first
		issue1 := &types.Issue{Title: "event-test-1", Priority: 1, IssueType: "task", Status: "open"}
		err = s.CreateIssue(ctx, issue1, "test")
		if err != nil {
			t.Fatalf("failed to create issue1: %v", err)
		}
		issue2 := &types.Issue{Title: "event-test-2", Priority: 1, IssueType: "task", Status: "open"}
		err = s.CreateIssue(ctx, issue2, "test")
		if err != nil {
			t.Fatalf("failed to create issue2: %v", err)
		}

		issues := []*types.Issue{issue1, issue2}

		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer conn.ExecContext(context.Background(), "ROLLBACK")

		err = bulkRecordEvents(ctx, conn, issues, "test-actor")
		if err != nil {
			t.Fatalf("failed to bulk record events: %v", err)
		}

		conn.ExecContext(ctx, "COMMIT")

		// Verify events were recorded
		for _, issue := range issues {
			var count int
			err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?`,
				issue.ID, types.EventCreated).Scan(&count)
			if err != nil {
				t.Fatalf("failed to check events: %v", err)
			}
			if count < 1 {
				t.Errorf("no creation event found for %s", issue.ID)
			}
		}
	})

	t.Run("bulkMarkDirty", func(t *testing.T) {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("failed to get connection: %v", err)
		}
		defer conn.Close()

		// Create test issues
		issue1 := &types.Issue{Title: "dirty-test-1", Priority: 1, IssueType: "task", Status: "open"}
		err = s.CreateIssue(ctx, issue1, "test")
		if err != nil {
			t.Fatalf("failed to create issue1: %v", err)
		}
		issue2 := &types.Issue{Title: "dirty-test-2", Priority: 1, IssueType: "task", Status: "open"}
		err = s.CreateIssue(ctx, issue2, "test")
		if err != nil {
			t.Fatalf("failed to create issue2: %v", err)
		}

		issues := []*types.Issue{issue1, issue2}

		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatalf("failed to begin transaction: %v", err)
		}
		defer conn.ExecContext(context.Background(), "ROLLBACK")

		err = bulkMarkDirty(ctx, conn, issues)
		if err != nil {
			t.Fatalf("failed to bulk mark dirty: %v", err)
		}

		conn.ExecContext(ctx, "COMMIT")

		// Verify issues are marked dirty
		for _, issue := range issues {
			var count int
			err := s.db.QueryRow(`SELECT COUNT(*) FROM dirty_issues WHERE issue_id = ?`, issue.ID).Scan(&count)
			if err != nil {
				t.Fatalf("failed to check dirty status: %v", err)
			}
			if count != 1 {
				t.Errorf("issue %s should be marked dirty", issue.ID)
			}
		}
	})
}

// TestDefensiveClosedAtFix tests GH#523 - closed issues without closed_at timestamp
// from older versions of bd should be automatically fixed during import.
func TestDefensiveClosedAtFix(t *testing.T) {
	t.Run("sets closed_at for closed issues missing it", func(t *testing.T) {
		now := time.Now()
		pastTime := now.Add(-24 * time.Hour)

		issues := []*types.Issue{
			{
				Title:     "Closed issue without closed_at",
				Priority:  1,
				IssueType: "task",
				Status:    "closed",
				CreatedAt: pastTime,
				UpdatedAt: pastTime.Add(time.Hour),
				// ClosedAt intentionally NOT set - simulating old bd data
			},
		}

		err := validateBatchIssues(issues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// closed_at should be set to max(created_at, updated_at) + 1 second
		if issues[0].ClosedAt == nil {
			t.Fatal("closed_at should have been set")
		}

		expectedClosedAt := pastTime.Add(time.Hour).Add(time.Second)
		if !issues[0].ClosedAt.Equal(expectedClosedAt) {
			t.Errorf("closed_at mismatch: want %v, got %v", expectedClosedAt, *issues[0].ClosedAt)
		}
	})

	t.Run("preserves existing closed_at", func(t *testing.T) {
		now := time.Now()
		pastTime := now.Add(-24 * time.Hour)
		closedTime := pastTime.Add(2 * time.Hour)

		issues := []*types.Issue{
			{
				Title:     "Closed issue with closed_at",
				Priority:  1,
				IssueType: "task",
				Status:    "closed",
				CreatedAt: pastTime,
				UpdatedAt: pastTime.Add(time.Hour),
				ClosedAt:  &closedTime,
			},
		}

		err := validateBatchIssues(issues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// closed_at should be preserved
		if !issues[0].ClosedAt.Equal(closedTime) {
			t.Errorf("closed_at should be preserved: want %v, got %v", closedTime, *issues[0].ClosedAt)
		}
	})

	t.Run("does not set closed_at for open issues", func(t *testing.T) {
		issues := []*types.Issue{
			{
				Title:     "Open issue",
				Priority:  1,
				IssueType: "task",
				Status:    "open",
			},
		}

		err := validateBatchIssues(issues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if issues[0].ClosedAt != nil {
			t.Error("closed_at should remain nil for open issues")
		}
	})

	t.Run("sets deleted_at for tombstones missing it", func(t *testing.T) {
		now := time.Now()
		pastTime := now.Add(-24 * time.Hour)

		issues := []*types.Issue{
			{
				Title:     "Tombstone without deleted_at",
				Priority:  1,
				IssueType: "task",
				Status:    "tombstone",
				CreatedAt: pastTime,
				UpdatedAt: pastTime.Add(time.Hour),
				// DeletedAt intentionally NOT set
			},
		}

		err := validateBatchIssues(issues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// deleted_at should be set to max(created_at, updated_at) + 1 second
		if issues[0].DeletedAt == nil {
			t.Fatal("deleted_at should have been set")
		}

		expectedDeletedAt := pastTime.Add(time.Hour).Add(time.Second)
		if !issues[0].DeletedAt.Equal(expectedDeletedAt) {
			t.Errorf("deleted_at mismatch: want %v, got %v", expectedDeletedAt, *issues[0].DeletedAt)
		}
	})
}
