//go:build cgo && integration
// +build cgo,integration

package compact

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func setupTestStorage(t *testing.T) *dolt.DoltStore {
	t.Helper()

	store, err := dolt.New(context.Background(), &dolt.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	ctx := context.Background()
	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}
	// Use 7 days minimum for Tier 1 compaction to ensure tests check eligibility properly
	if err := store.SetConfig(ctx, "compact_tier1_days", "7"); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}
	if err := store.SetConfig(ctx, "compact_tier1_dep_levels", "2"); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}

	return store
}

func createClosedIssue(t *testing.T, store *dolt.DoltStore, id string) *types.Issue {
	t.Helper()

	ctx := context.Background()

	// Get the configured prefix to determine actor
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		prefix = "bd" // fallback
	}

	now := time.Now()
	// Issue closed 8 days ago (beyond 7-day threshold for Tier 1)
	closedAt := now.Add(-8 * 24 * time.Hour)
	issue := &types.Issue{
		ID:    id,
		Title: "Test Issue",
		Description: `Implemented a comprehensive authentication system for the application.
		
The system includes JWT token generation, refresh token handling, password hashing with bcrypt,
rate limiting on login attempts, and session management. We chose JWT for stateless authentication
to enable horizontal scaling across multiple server instances.

The implementation follows OWASP security guidelines and includes protection against common attacks
like brute force, timing attacks, and token theft. All sensitive operations are logged for audit purposes.`,
		Design: `Authentication Flow:
1. User submits credentials
2. Server validates against database
3. On success, generate JWT with user claims
4. Return JWT + refresh token
5. Client stores tokens securely
6. JWT used for API requests (Authorization header)
7. Refresh token rotated on use

Security Measures:
- Passwords hashed with bcrypt (cost factor 12)
- Rate limiting: 5 attempts per 15 minutes
- JWT expires after 1 hour
- Refresh tokens expire after 30 days
- All tokens stored in httpOnly cookies`,
		Notes: `Performance considerations:
- JWT validation adds ~2ms latency per request
- Consider caching user data in Redis for frequently accessed profiles
- Monitor token refresh patterns for anomalies

Testing strategy:
- Unit tests for each authentication component
- Integration tests for full auth flow
- Security tests for attack scenarios
- Load tests for rate limiting behavior`,
		AcceptanceCriteria: `- Users can register with email/password
- Users can login and receive valid JWT
- Protected endpoints reject invalid/expired tokens
- Rate limiting blocks brute force attempts
- Tokens can be refreshed before expiry
- Logout invalidates current session
- All security requirements met per OWASP guidelines`,
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: now.Add(-48 * time.Hour),
		UpdatedAt: now.Add(-24 * time.Hour),
		ClosedAt:  &closedAt,
	}

	if err := store.CreateIssue(ctx, issue, prefix); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	return issue
}

func TestNew(t *testing.T) {
	store := setupTestStorage(t)
	defer store.Close()

	t.Run("creates compactor with config", func(t *testing.T) {
		config := &Config{
			Concurrency: 10,
			DryRun:      true,
		}
		c, err := New(store, "", config)
		if err != nil {
			t.Fatalf("failed to create compactor: %v", err)
		}
		if c.config.Concurrency != 10 {
			t.Errorf("expected concurrency 10, got %d", c.config.Concurrency)
		}
	})

	t.Run("uses default concurrency", func(t *testing.T) {
		c, err := New(store, "", nil)
		if err != nil {
			t.Fatalf("failed to create compactor: %v", err)
		}
		if c.config.Concurrency != defaultConcurrency {
			t.Errorf("expected default concurrency %d, got %d", defaultConcurrency, c.config.Concurrency)
		}
	})
}

func TestCompactTier1_DryRun(t *testing.T) {
	store := setupTestStorage(t)
	defer store.Close()

	issue := createClosedIssue(t, store, "bd-1")

	config := &Config{DryRun: true}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx := context.Background()
	err = c.CompactTier1(ctx, issue.ID)
	if err == nil {
		t.Fatal("expected dry-run error, got nil")
	}
	if err.Error()[:8] != "dry-run:" {
		t.Errorf("expected dry-run error prefix, got: %v", err)
	}

	afterIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if afterIssue.Description != issue.Description {
		t.Error("dry-run should not modify issue")
	}
}

func TestCompactTier1_IneligibleIssue(t *testing.T) {
	store := setupTestStorage(t)
	defer store.Close()

	ctx := context.Background()

	// Get the configured prefix to determine actor
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		prefix = "bd" // fallback
	}

	now := time.Now()
	issue := &types.Issue{
		ID:          "bd-open",
		Title:       "Open Issue",
		Description: "Should not be compacted",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateIssue(ctx, issue, prefix); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	config := &Config{DryRun: true}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	err = c.CompactTier1(ctx, issue.ID)
	if err == nil {
		t.Fatal("expected error for ineligible issue, got nil")
	}
	if err.Error() != "issue bd-open is not eligible for Tier 1 compaction: issue is not closed" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCompactTier1_WithAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow API test in short mode")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping API test")
	}

	store := setupTestStorage(t)
	defer store.Close()

	issue := createClosedIssue(t, store, "bd-api")

	c, err := New(store, "", &Config{Concurrency: 1})
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx := context.Background()
	if err := c.CompactTier1(ctx, issue.ID); err != nil {
		t.Fatalf("failed to compact: %v", err)
	}

	afterIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}

	if afterIssue.Description == issue.Description {
		t.Error("description should have changed")
	}
	if afterIssue.Design != "" {
		t.Error("design should be cleared")
	}
	if afterIssue.Notes != "" {
		t.Error("notes should be cleared")
	}
	if afterIssue.AcceptanceCriteria != "" {
		t.Error("acceptance criteria should be cleared")
	}
}

func TestCompactTier1Batch_DryRun(t *testing.T) {
	store := setupTestStorage(t)
	defer store.Close()

	issue1 := createClosedIssue(t, store, "bd-batch-1")
	issue2 := createClosedIssue(t, store, "bd-batch-2")

	config := &Config{DryRun: true, Concurrency: 2}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx := context.Background()
	results, err := c.CompactTier1Batch(ctx, []string{issue1.ID, issue2.ID})
	if err != nil {
		t.Fatalf("failed to batch compact: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, result := range results {
		if result.Err != nil {
			t.Errorf("unexpected error for %s: %v", result.IssueID, result.Err)
		}
		if result.OriginalSize == 0 {
			t.Errorf("expected non-zero original size for %s", result.IssueID)
		}
	}
}

func TestCompactTier1Batch_WithIneligible(t *testing.T) {
	store := setupTestStorage(t)
	defer store.Close()

	closedIssue := createClosedIssue(t, store, "bd-closed")

	ctx := context.Background()

	// Get the configured prefix to determine actor
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		prefix = "bd" // fallback
	}

	now := time.Now()
	openIssue := &types.Issue{
		ID:          "bd-open",
		Title:       "Open Issue",
		Description: "Should not be compacted",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateIssue(ctx, openIssue, prefix); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	config := &Config{DryRun: true, Concurrency: 2}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	results, err := c.CompactTier1Batch(ctx, []string{closedIssue.ID, openIssue.ID})
	if err != nil {
		t.Fatalf("failed to batch compact: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, result := range results {
		switch result.IssueID {
		case openIssue.ID:
			if result.Err == nil {
				t.Error("expected error for ineligible issue")
			}
		case closedIssue.ID:
			if result.Err != nil {
				t.Errorf("unexpected error for eligible issue: %v", result.Err)
			}
		}
	}
}

func TestCompactTier1Batch_WithAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow API test in short mode")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping API test")
	}

	store := setupTestStorage(t)
	defer store.Close()

	issue1 := createClosedIssue(t, store, "bd-api-batch-1")
	issue2 := createClosedIssue(t, store, "bd-api-batch-2")
	issue3 := createClosedIssue(t, store, "bd-api-batch-3")

	c, err := New(store, "", &Config{Concurrency: 2})
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx := context.Background()
	results, err := c.CompactTier1Batch(ctx, []string{issue1.ID, issue2.ID, issue3.ID})
	if err != nil {
		t.Fatalf("failed to batch compact: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for _, result := range results {
		if result.Err != nil {
			t.Errorf("unexpected error for %s: %v", result.IssueID, result.Err)
		}
		if result.CompactedSize == 0 {
			t.Errorf("expected non-zero compacted size for %s", result.IssueID)
		}
		if result.CompactedSize >= result.OriginalSize {
			t.Errorf("expected size reduction for %s: %d → %d", result.IssueID, result.OriginalSize, result.CompactedSize)
		}
	}

	for _, id := range []string{issue1.ID, issue2.ID, issue3.ID} {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("failed to get issue %s: %v", id, err)
		}
		if issue.Design != "" || issue.Notes != "" || issue.AcceptanceCriteria != "" {
			t.Errorf("fields should be cleared for %s", id)
		}
	}
}

func TestMockAPI_CompactTier1(t *testing.T) {
	store := setupTestStorage(t)
	defer store.Close()

	issue := createClosedIssue(t, store, "bd-mock")

	c, err := New(store, "", &Config{DryRun: true, Concurrency: 1})
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx := context.Background()
	err = c.CompactTier1(ctx, issue.ID)
	if err == nil || err.Error()[:8] != "dry-run:" {
		t.Errorf("expected dry-run error, got: %v", err)
	}
}

func TestBatchOperations_ErrorHandling(t *testing.T) {
	store := setupTestStorage(t)
	defer store.Close()

	ctx := context.Background()

	// Get the configured prefix to determine actor
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		prefix = "bd" // fallback
	}

	closedIssue := createClosedIssue(t, store, "bd-closed")
	openIssue := &types.Issue{
		ID:          "bd-open",
		Title:       "Open",
		Description: "Open issue",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := store.CreateIssue(ctx, openIssue, prefix); err != nil {
		t.Fatalf("failed to create open issue: %v", err)
	}

	c, err := New(store, "", &Config{DryRun: true, Concurrency: 2})
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	results, err := c.CompactTier1Batch(ctx, []string{closedIssue.ID, openIssue.ID, "nonexistent"})
	if err != nil {
		t.Fatalf("batch operation failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	var successCount, errorCount int
	for _, r := range results {
		if r.Err == nil {
			successCount++
		} else {
			errorCount++
		}
	}

	if successCount != 1 {
		t.Errorf("expected 1 success, got %d", successCount)
	}
	if errorCount != 2 {
		t.Errorf("expected 2 errors, got %d", errorCount)
	}
}
