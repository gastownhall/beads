//go:build integration
// +build integration

// Package main provides the bd CLI commands.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestGitLabSyncRoundtrip verifies full bidirectional sync:
// 1. Pull issues from GitLab into beads
// 2. Modify issues locally
// 3. Push changes back to GitLab
// 4. Verify GitLab received updates
func TestGitLabSyncRoundtrip(t *testing.T) {
	// Track API calls
	var issuesCreated int32
	var issuesUpdated int32

	// Create mock GitLab server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodGet:
			// Return list of issues
			createdAt := time.Now().Add(-24 * time.Hour)
			updatedAt := time.Now().Add(-1 * time.Hour)
			issues := []gitlab.Issue{
				{
					ID:          100,
					IID:         1,
					ProjectID:   123,
					Title:       "GitLab Issue 1",
					Description: "From GitLab",
					State:       "opened",
					Labels:      []string{"type::task", "priority::high"},
					CreatedAt:   &createdAt,
					UpdatedAt:   &updatedAt,
					WebURL:      "https://gitlab.example.com/project/-/issues/1",
				},
				{
					ID:          101,
					IID:         2,
					ProjectID:   123,
					Title:       "GitLab Issue 2",
					Description: "Also from GitLab",
					State:       "opened",
					Labels:      []string{"type::bug"},
					CreatedAt:   &createdAt,
					UpdatedAt:   &updatedAt,
					WebURL:      "https://gitlab.example.com/project/-/issues/2",
				},
			}
			json.NewEncoder(w).Encode(issues)

		case http.MethodPost:
			// Create new issue
			atomic.AddInt32(&issuesCreated, 1)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(gitlab.Issue{
				ID:        200,
				IID:       3,
				ProjectID: 123,
				Title:     "New issue from beads",
				WebURL:    "https://gitlab.example.com/project/-/issues/3",
			})

		case http.MethodPut:
			// Update existing issue
			atomic.AddInt32(&issuesUpdated, 1)
			json.NewEncoder(w).Encode(gitlab.Issue{
				ID:        100,
				IID:       1,
				ProjectID: 123,
				Title:     "Updated title",
			})
		}
	}))
	defer server.Close()

	// Create in-memory store
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	// Set global store for sync functions
	oldStore := store
	store = testStore
	oldDbPath := dbPath
	dbPath = ":memory:"
	defer func() {
		store = oldStore
		dbPath = oldDbPath
	}()

	// Create GitLab client pointing to mock server
	client := gitlab.NewClient("test-token", server.URL, "123")
	mappingConfig := gitlab.DefaultMappingConfig()

	// Step 1: Pull issues from GitLab
	t.Run("PullFromGitLab", func(t *testing.T) {
		stats, err := doPullFromGitLab(ctx, client, mappingConfig, false, "all", nil)
		if err != nil {
			t.Fatalf("doPullFromGitLab() error = %v", err)
		}

		if stats.Created != 2 {
			t.Errorf("stats.Created = %d, want 2", stats.Created)
		}

		// Verify issues were imported
		issues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("SearchIssues() error = %v", err)
		}

		if len(issues) != 2 {
			t.Errorf("len(issues) = %d, want 2", len(issues))
		}
	})

	// Step 2: Create a new local issue
	t.Run("CreateLocalIssue", func(t *testing.T) {
		newIssue := &types.Issue{
			ID:          "bd-local-1",
			Title:       "New local issue",
			Description: "Created in beads",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}

		if err := testStore.CreateIssue(ctx, newIssue, "test"); err != nil {
			t.Fatalf("CreateIssue() error = %v", err)
		}
	})

	// Step 3: Push to GitLab - only push the new local issue (createOnly mode)
	// Note: The SQLite store doesn't allow updating source_system via UpdateIssue,
	// so imported GitLab issues can't be updated. We test create-only flow instead.
	t.Run("PushToGitLab", func(t *testing.T) {
		// Get only the local issue (filter by title)
		issues, err := testStore.SearchIssues(ctx, "New local issue", types.IssueFilter{})
		if err != nil {
			t.Fatalf("SearchIssues() error = %v", err)
		}

		if len(issues) != 1 {
			t.Fatalf("Expected 1 local issue, got %d", len(issues))
		}

		stats, err := doPushToGitLab(ctx, client, mappingConfig, issues, false, true, nil, nil)
		if err != nil {
			t.Fatalf("doPushToGitLab() error = %v", err)
		}

		// Should have created 1 new issue (the local one without external ref)
		if stats.Created != 1 {
			t.Errorf("stats.Created = %d, want 1", stats.Created)
		}
	})

	// Verify create API was called
	if atomic.LoadInt32(&issuesCreated) != 1 {
		t.Errorf("issuesCreated = %d, want 1", issuesCreated)
	}
}

// TestGitLabConflictStrategiesIntegration tests conflict strategy selection.
func TestGitLabConflictStrategiesIntegration(t *testing.T) {
	// Test prefer-newer strategy (default)
	t.Run("PreferNewer", func(t *testing.T) {
		strategy, err := getConflictStrategy(false, false, true)
		if err != nil {
			t.Fatalf("getConflictStrategy() error = %v", err)
		}
		if strategy != ConflictStrategyPreferNewer {
			t.Errorf("strategy = %q, want %q", strategy, ConflictStrategyPreferNewer)
		}
	})

	// Test prefer-local strategy
	t.Run("PreferLocal", func(t *testing.T) {
		strategy, err := getConflictStrategy(true, false, false)
		if err != nil {
			t.Fatalf("getConflictStrategy() error = %v", err)
		}
		if strategy != ConflictStrategyPreferLocal {
			t.Errorf("strategy = %q, want %q", strategy, ConflictStrategyPreferLocal)
		}
	})

	// Test prefer-gitlab strategy
	t.Run("PreferGitLab", func(t *testing.T) {
		strategy, err := getConflictStrategy(false, true, false)
		if err != nil {
			t.Fatalf("getConflictStrategy() error = %v", err)
		}
		if strategy != ConflictStrategyPreferGitLab {
			t.Errorf("strategy = %q, want %q", strategy, ConflictStrategyPreferGitLab)
		}
	})

	// Test default (no flags) returns prefer-newer
	t.Run("DefaultIsPreferNewer", func(t *testing.T) {
		strategy, err := getConflictStrategy(false, false, false)
		if err != nil {
			t.Fatalf("getConflictStrategy() error = %v", err)
		}
		if strategy != ConflictStrategyPreferNewer {
			t.Errorf("strategy = %q, want %q (default)", strategy, ConflictStrategyPreferNewer)
		}
	})

	// Test multiple flags returns error
	t.Run("MultipleFlags", func(t *testing.T) {
		_, err := getConflictStrategy(true, true, false)
		if err == nil {
			t.Error("Expected error for multiple conflicting flags")
		}
	})
}

// TestGitLabConflictDetectionIntegration tests conflict detection with mock issues.
func TestGitLabConflictDetectionIntegration(t *testing.T) {
	serverTime := time.Now().Add(1 * time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlab.Issue{
			{
				ID:        100,
				IID:       1,
				ProjectID: 123,
				Title:     "GitLab version",
				State:     "opened",
				UpdatedAt: &serverTime,
				WebURL:    "https://gitlab.example.com/project/-/issues/1",
			},
		})
	}))
	defer server.Close()

	ctx := context.Background()
	client := gitlab.NewClient("test-token", server.URL, "123")

	// Create a mock local issue with SourceSystem set
	localTime := time.Now().Add(-1 * time.Hour)
	localIssues := []*types.Issue{
		{
			ID:           "bd-local-1",
			Title:        "Local version",
			SourceSystem: "gitlab:123:1",
			UpdatedAt:    localTime,
		},
	}

	// Detect conflicts
	conflicts, err := detectGitLabConflicts(ctx, client, localIssues)
	if err != nil {
		t.Fatalf("detectGitLabConflicts() error = %v", err)
	}

	if len(conflicts) != 1 {
		t.Fatalf("len(conflicts) = %d, want 1", len(conflicts))
	}

	// Verify conflict details
	if conflicts[0].IssueID != "bd-local-1" {
		t.Errorf("conflict.IssueID = %q, want %q", conflicts[0].IssueID, "bd-local-1")
	}
	if conflicts[0].GitLabIID != 1 {
		t.Errorf("conflict.GitLabIID = %d, want 1", conflicts[0].GitLabIID)
	}

	// Verify GitLab is newer
	if !conflicts[0].GitLabUpdated.After(conflicts[0].LocalUpdated) {
		t.Error("Expected GitLab timestamp to be after local timestamp")
	}
}

// TestIncrementalSync verifies that incremental sync respects last_sync timestamp.
func TestIncrementalSync(t *testing.T) {
	fetchCount := int32(0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fetchCount, 1)

		// Check if updated_after parameter is present
		updatedAfter := r.URL.Query().Get("updated_after")

		w.Header().Set("Content-Type", "application/json")

		if updatedAfter != "" {
			// Incremental sync - return only new issues
			createdAt := time.Now()
			json.NewEncoder(w).Encode([]gitlab.Issue{
				{
					ID:        200,
					IID:       10,
					ProjectID: 123,
					Title:     "New issue since last sync",
					State:     "opened",
					CreatedAt: &createdAt,
					UpdatedAt: &createdAt,
					WebURL:    "https://gitlab.example.com/project/-/issues/10",
				},
			})
		} else {
			// Full sync - return all issues
			createdAt := time.Now().Add(-48 * time.Hour)
			updatedAt := time.Now().Add(-24 * time.Hour)
			json.NewEncoder(w).Encode([]gitlab.Issue{
				{
					ID:        100,
					IID:       1,
					ProjectID: 123,
					Title:     "Old issue",
					State:     "opened",
					CreatedAt: &createdAt,
					UpdatedAt: &updatedAt,
					WebURL:    "https://gitlab.example.com/project/-/issues/1",
				},
			})
		}
	}))
	defer server.Close()

	ctx := context.Background()
	testStore, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer testStore.Close()

	if err := testStore.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("Failed to set prefix: %v", err)
	}

	oldStore := store
	store = testStore
	defer func() { store = oldStore }()

	client := gitlab.NewClient("test-token", server.URL, "123")
	mappingConfig := gitlab.DefaultMappingConfig()

	// First sync - should be full sync
	t.Run("FullSync", func(t *testing.T) {
		stats, err := doPullFromGitLab(ctx, client, mappingConfig, false, "all", nil)
		if err != nil {
			t.Fatalf("doPullFromGitLab() error = %v", err)
		}

		if stats.Incremental {
			t.Error("First sync should not be incremental")
		}
		if stats.Created != 1 {
			t.Errorf("stats.Created = %d, want 1", stats.Created)
		}
	})

	// Second sync - should be incremental (uses stored gitlab.last_sync)
	t.Run("IncrementalSync", func(t *testing.T) {
		stats, err := doPullFromGitLab(ctx, client, mappingConfig, false, "all", nil)
		if err != nil {
			t.Fatalf("doPullFromGitLab() error = %v", err)
		}

		if !stats.Incremental {
			t.Error("Second sync should be incremental")
		}
		if stats.SyncedSince == "" {
			t.Error("SyncedSince should be set for incremental sync")
		}
	})

	// Verify both requests were made
	if atomic.LoadInt32(&fetchCount) != 2 {
		t.Errorf("fetchCount = %d, want 2", fetchCount)
	}
}
