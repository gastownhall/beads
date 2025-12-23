package compact

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// setupTestStore creates a test SQLite store for unit tests
func setupTestStore(t *testing.T) *sqlite.SQLiteStorage {
	t.Helper()

	tmpDB := t.TempDir() + "/test.db"
	store, err := sqlite.New(context.Background(), tmpDB)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	ctx := context.Background()
	// Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}
	// Use 7 days minimum for Tier 1 compaction
	if err := store.SetConfig(ctx, "compact_tier1_days", "7"); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}
	if err := store.SetConfig(ctx, "compact_tier1_dep_levels", "2"); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}

	return store
}

// createTestIssue creates a closed issue eligible for compaction
func createTestIssue(t *testing.T, store *sqlite.SQLiteStorage, id string) *types.Issue {
	t.Helper()

	ctx := context.Background()
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	if prefix == "" {
		prefix = "bd"
	}

	now := time.Now()
	// Issue closed 8 days ago (beyond 7-day threshold for Tier 1)
	closedAt := now.Add(-8 * 24 * time.Hour)
	issue := &types.Issue{
		ID:    id,
		Title: "Test Issue",
		Description: `Implemented a comprehensive authentication system for the application.

The system includes JWT token generation, refresh token handling, password hashing with bcrypt,
rate limiting on login attempts, and session management.`,
		Design: `Authentication Flow:
1. User submits credentials
2. Server validates against database
3. On success, generate JWT with user claims`,
		Notes:              "Performance considerations and testing strategy notes.",
		AcceptanceCriteria: "- Users can register\n- Users can login\n- Protected endpoints work",
		Status:             types.StatusClosed,
		Priority:           2,
		IssueType:          types.TypeTask,
		CreatedAt:          now.Add(-48 * time.Hour),
		UpdatedAt:          now.Add(-24 * time.Hour),
		ClosedAt:           &closedAt,
	}

	if err := store.CreateIssue(ctx, issue, prefix); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	return issue
}

func TestNew_WithConfig(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

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
	if !c.config.DryRun {
		t.Error("expected DryRun to be true")
	}
}

func TestNew_DefaultConcurrency(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	c, err := New(store, "", nil)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}
	if c.config.Concurrency != defaultConcurrency {
		t.Errorf("expected default concurrency %d, got %d", defaultConcurrency, c.config.Concurrency)
	}
}

func TestNew_ZeroConcurrency(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	config := &Config{
		Concurrency: 0,
		DryRun:      true,
	}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}
	// Zero concurrency should be replaced with default
	if c.config.Concurrency != defaultConcurrency {
		t.Errorf("expected default concurrency %d, got %d", defaultConcurrency, c.config.Concurrency)
	}
}

func TestNew_NegativeConcurrency(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	config := &Config{
		Concurrency: -5,
		DryRun:      true,
	}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}
	// Negative concurrency should be replaced with default
	if c.config.Concurrency != defaultConcurrency {
		t.Errorf("expected default concurrency %d, got %d", defaultConcurrency, c.config.Concurrency)
	}
}

func TestNew_WithAPIKey(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	// Clear env var to test explicit key
	t.Setenv("ANTHROPIC_API_KEY", "")

	config := &Config{
		DryRun: true, // DryRun so we don't actually need a valid key
	}
	c, err := New(store, "test-api-key", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}
	if c.config.APIKey != "test-api-key" {
		t.Errorf("expected api key 'test-api-key', got '%s'", c.config.APIKey)
	}
}

func TestNew_NoAPIKeyFallsToDryRun(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	// Clear env var
	t.Setenv("ANTHROPIC_API_KEY", "")

	config := &Config{
		DryRun: false, // Try to create real client
	}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}
	// Should fall back to DryRun when no API key
	if !c.config.DryRun {
		t.Error("expected DryRun to be true when no API key provided")
	}
}

func TestNew_AuditSettings(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	config := &Config{
		AuditEnabled: true,
		Actor:        "test-actor",
	}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}
	if c.haiku == nil {
		t.Fatal("expected haiku client to be created")
	}
	if !c.haiku.auditEnabled {
		t.Error("expected auditEnabled to be true")
	}
	if c.haiku.auditActor != "test-actor" {
		t.Errorf("expected auditActor 'test-actor', got '%s'", c.haiku.auditActor)
	}
}

func TestCompactTier1_DryRun(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	issue := createTestIssue(t, store, "bd-1")

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
	if !strings.HasPrefix(err.Error(), "dry-run:") {
		t.Errorf("expected dry-run error prefix, got: %v", err)
	}

	// Verify issue was not modified
	afterIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if afterIssue.Description != issue.Description {
		t.Error("dry-run should not modify issue")
	}
}

func TestCompactTier1_IneligibleOpenIssue(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	if prefix == "" {
		prefix = "bd"
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
	if !strings.Contains(err.Error(), "not eligible") {
		t.Errorf("expected 'not eligible' error, got: %v", err)
	}
}

func TestCompactTier1_NonexistentIssue(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	config := &Config{DryRun: true}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx := context.Background()
	err = c.CompactTier1(ctx, "bd-nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent issue")
	}
}

func TestCompactTier1_ContextCanceled(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	issue := createTestIssue(t, store, "bd-cancel")

	config := &Config{DryRun: true}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err = c.CompactTier1(ctx, issue.ID)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestCompactTier1Batch_EmptyList(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	config := &Config{DryRun: true}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx := context.Background()
	results, err := c.CompactTier1Batch(ctx, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for empty list, got: %v", results)
	}
}

func TestCompactTier1Batch_DryRun(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	issue1 := createTestIssue(t, store, "bd-batch-1")
	issue2 := createTestIssue(t, store, "bd-batch-2")

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

func TestCompactTier1Batch_MixedEligibility(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	closedIssue := createTestIssue(t, store, "bd-closed")

	ctx := context.Background()
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	if prefix == "" {
		prefix = "bd"
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

	var foundClosed, foundOpen bool
	for _, result := range results {
		switch result.IssueID {
		case openIssue.ID:
			foundOpen = true
			if result.Err == nil {
				t.Error("expected error for ineligible issue")
			}
		case closedIssue.ID:
			foundClosed = true
			if result.Err != nil {
				t.Errorf("unexpected error for eligible issue: %v", result.Err)
			}
		}
	}
	if !foundClosed || !foundOpen {
		t.Error("missing expected results")
	}
}

func TestCompactTier1Batch_NonexistentIssue(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	closedIssue := createTestIssue(t, store, "bd-closed")

	config := &Config{DryRun: true, Concurrency: 2}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	ctx := context.Background()
	results, err := c.CompactTier1Batch(ctx, []string{closedIssue.ID, "bd-nonexistent"})
	if err != nil {
		t.Fatalf("batch operation failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
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
	if errorCount != 1 {
		t.Errorf("expected 1 error, got %d", errorCount)
	}
}

func TestCompactTier1_WithMockAPI(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	issue := createTestIssue(t, store, "bd-mock-api")

	// Create mock server that returns a short summary
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "msg_test123",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-3-5-haiku-20241022",
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "**Summary:** Short summary.\n\n**Key Decisions:** None.\n\n**Resolution:** Done.",
				},
			},
		})
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	// Create compactor with mock API
	config := &Config{Concurrency: 1}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	// Replace the haiku client with one pointing to mock server
	c.haiku, err = NewHaikuClient("test-key", option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("failed to create mock haiku client: %v", err)
	}

	ctx := context.Background()
	err = c.CompactTier1(ctx, issue.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify issue was updated
	afterIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}

	if afterIssue.Description == issue.Description {
		t.Error("description should have been updated")
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

func TestCompactTier1_SummaryNotShorter(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	// Create issue with very short content
	ctx := context.Background()
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	if prefix == "" {
		prefix = "bd"
	}

	now := time.Now()
	closedAt := now.Add(-8 * 24 * time.Hour)
	issue := &types.Issue{
		ID:          "bd-short",
		Title:       "Short",
		Description: "X", // Very short description
		Status:      types.StatusClosed,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   now.Add(-48 * time.Hour),
		UpdatedAt:   now.Add(-24 * time.Hour),
		ClosedAt:    &closedAt,
	}
	if err := store.CreateIssue(ctx, issue, prefix); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Create mock server that returns a longer summary
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "msg_test123",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-3-5-haiku-20241022",
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "**Summary:** This is a much longer summary that exceeds the original content length.\n\n**Key Decisions:** Multiple decisions.\n\n**Resolution:** Complete.",
				},
			},
		})
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	config := &Config{Concurrency: 1}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	c.haiku, err = NewHaikuClient("test-key", option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("failed to create mock haiku client: %v", err)
	}

	err = c.CompactTier1(ctx, issue.ID)
	if err == nil {
		t.Fatal("expected error when summary is longer")
	}
	if !strings.Contains(err.Error(), "would increase size") {
		t.Errorf("expected 'would increase size' error, got: %v", err)
	}

	// Verify issue was NOT modified (kept original)
	afterIssue, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if afterIssue.Description != issue.Description {
		t.Error("description should not have been modified when summary is longer")
	}
}

func TestCompactTier1Batch_WithMockAPI(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	issue1 := createTestIssue(t, store, "bd-batch-mock-1")
	issue2 := createTestIssue(t, store, "bd-batch-mock-2")

	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "msg_test123",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-3-5-haiku-20241022",
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "**Summary:** Compacted.\n\n**Key Decisions:** None.\n\n**Resolution:** Done.",
				},
			},
		})
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	config := &Config{Concurrency: 2}
	c, err := New(store, "", config)
	if err != nil {
		t.Fatalf("failed to create compactor: %v", err)
	}

	c.haiku, err = NewHaikuClient("test-key", option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("failed to create mock haiku client: %v", err)
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
		if result.CompactedSize == 0 {
			t.Errorf("expected non-zero compacted size for %s", result.IssueID)
		}
		if result.CompactedSize >= result.OriginalSize {
			t.Errorf("expected size reduction for %s: %d → %d", result.IssueID, result.OriginalSize, result.CompactedSize)
		}
	}
}

func TestResult_Fields(t *testing.T) {
	r := &Result{
		IssueID:       "bd-1",
		OriginalSize:  100,
		CompactedSize: 50,
		Err:           nil,
	}

	if r.IssueID != "bd-1" {
		t.Errorf("expected IssueID 'bd-1', got '%s'", r.IssueID)
	}
	if r.OriginalSize != 100 {
		t.Errorf("expected OriginalSize 100, got %d", r.OriginalSize)
	}
	if r.CompactedSize != 50 {
		t.Errorf("expected CompactedSize 50, got %d", r.CompactedSize)
	}
	if r.Err != nil {
		t.Errorf("expected nil Err, got %v", r.Err)
	}
}

func TestConfig_Fields(t *testing.T) {
	c := &Config{
		APIKey:       "test-key",
		Concurrency:  10,
		DryRun:       true,
		AuditEnabled: true,
		Actor:        "test-actor",
	}

	if c.APIKey != "test-key" {
		t.Errorf("expected APIKey 'test-key', got '%s'", c.APIKey)
	}
	if c.Concurrency != 10 {
		t.Errorf("expected Concurrency 10, got %d", c.Concurrency)
	}
	if !c.DryRun {
		t.Error("expected DryRun true")
	}
	if !c.AuditEnabled {
		t.Error("expected AuditEnabled true")
	}
	if c.Actor != "test-actor" {
		t.Errorf("expected Actor 'test-actor', got '%s'", c.Actor)
	}
}
