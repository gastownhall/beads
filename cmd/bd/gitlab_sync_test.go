// Package main provides the bd CLI commands.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoPullFromGitLab_Success verifies pulling issues from GitLab creates beads issues.
func TestDoPullFromGitLab_Success(t *testing.T) {
	// Mock GitLab API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		issues := []gitlab.Issue{
			{
				ID:          1,
				IID:         1,
				ProjectID:   123,
				Title:       "Test issue",
				Description: "Test description",
				State:       "opened",
				Labels:      []string{"type::bug", "priority::high"},
				WebURL:      "https://gitlab.example.com/group/project/-/issues/1",
			},
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext() // Use SyncContext instead of globals

	stats, err := doPullFromGitLabWithContext(ctx, syncCtx, client, config, false, "all", nil)
	if err != nil {
		t.Fatalf("doPullFromGitLabWithContext() error = %v", err)
	}

	if stats.Created != 1 {
		t.Errorf("stats.Created = %d, want 1", stats.Created)
	}
}

// TestDoPullFromGitLab_DryRun verifies dry run mode doesn't create issues.
func TestDoPullFromGitLab_DryRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		issues := []gitlab.Issue{
			{
				ID:          1,
				IID:         1,
				ProjectID:   123,
				Title:       "Test issue",
				State:       "opened",
				WebURL:      "https://gitlab.example.com/group/project/-/issues/1",
			},
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext()

	stats, err := doPullFromGitLabWithContext(ctx, syncCtx, client, config, true, "all", nil)
	if err != nil {
		t.Fatalf("doPullFromGitLabWithContext() error = %v", err)
	}

	// Dry run should report what would be created but not actually create
	if stats.Created != 0 {
		t.Errorf("dry run stats.Created = %d, want 0", stats.Created)
	}
}

// TestDoPullFromGitLab_SkipIssues verifies skipGitLabIIDs filters issues.
func TestDoPullFromGitLab_SkipIssues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		issues := []gitlab.Issue{
			{ID: 1, IID: 1, ProjectID: 123, Title: "Issue 1", State: "opened", WebURL: "https://gitlab.example.com/-/issues/1"},
			{ID: 2, IID: 2, ProjectID: 123, Title: "Issue 2", State: "opened", WebURL: "https://gitlab.example.com/-/issues/2"},
			{ID: 3, IID: 3, ProjectID: 123, Title: "Issue 3", State: "opened", WebURL: "https://gitlab.example.com/-/issues/3"},
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext()

	// Skip issue IID 2
	skipIIDs := map[int]bool{2: true}

	stats, err := doPullFromGitLabWithContext(ctx, syncCtx, client, config, false, "all", skipIIDs)
	if err != nil {
		t.Fatalf("doPullFromGitLabWithContext() error = %v", err)
	}

	if stats.Skipped != 1 {
		t.Errorf("stats.Skipped = %d, want 1", stats.Skipped)
	}
}

// TestDoPushToGitLab_CreateNew verifies pushing new issues to GitLab.
func TestDoPushToGitLab_CreateNew(t *testing.T) {
	var createCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(gitlab.Issue{
				ID:     100,
				IID:    42,
				Title:  "New issue",
				State:  "opened",
				WebURL: "https://gitlab.example.com/-/issues/42",
			})
			return
		}
		// GET requests for fetching issues
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]gitlab.Issue{})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext()

	// Create a local issue without external ref (new issue)
	localIssues := []*types.Issue{
		{
			ID:          "bd-1",
			Title:       "New issue",
			Description: "New issue description",
			IssueType:   types.TypeBug,
			Priority:    1,
			Status:      types.StatusOpen,
		},
	}

	stats, err := doPushToGitLabWithContext(ctx, syncCtx, client, config, localIssues, false, false, nil, nil)
	if err != nil {
		t.Fatalf("doPushToGitLabWithContext() error = %v", err)
	}

	if !createCalled {
		t.Error("GitLab create API was not called")
	}
	if stats.Created != 1 {
		t.Errorf("stats.Created = %d, want 1", stats.Created)
	}
}

// TestDoPushToGitLab_UpdateExisting verifies updating existing GitLab issues.
func TestDoPushToGitLab_UpdateExisting(t *testing.T) {
	var updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			updateCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gitlab.Issue{
			ID:     100,
			IID:    42,
			Title:  "Updated issue",
			State:  "opened",
			WebURL: "https://gitlab.example.com/-/issues/42",
		})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext()

	// Create a local issue with external ref (existing issue)
	webURL := "https://gitlab.example.com/-/issues/42"
	localIssues := []*types.Issue{
		{
			ID:           "bd-1",
			Title:        "Updated issue",
			Description:  "Updated description",
			IssueType:    types.TypeBug,
			Priority:     1,
			Status:       types.StatusOpen,
			ExternalRef:  &webURL,
			SourceSystem: "gitlab:123:42",
		},
	}

	stats, err := doPushToGitLabWithContext(ctx, syncCtx, client, config, localIssues, false, false, nil, nil)
	if err != nil {
		t.Fatalf("doPushToGitLabWithContext() error = %v", err)
	}

	if !updateCalled {
		t.Error("GitLab update API was not called")
	}
	if stats.Updated != 1 {
		t.Errorf("stats.Updated = %d, want 1", stats.Updated)
	}
}

// TestDetectGitLabConflicts_NoConflicts verifies no conflicts detected when timestamps match.
func TestDetectGitLabConflicts_NoConflicts(t *testing.T) {
	now := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]gitlab.Issue{
			{
				ID:        100,
				IID:       42,
				ProjectID: 123,
				Title:     "Same title",
				State:     "opened",
				UpdatedAt: &now,
				WebURL:    "https://gitlab.example.com/-/issues/42",
			},
		})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	ctx := context.Background()
	syncCtx := NewSyncContext()

	// Local issue with same updated_at timestamp
	webURL := "https://gitlab.example.com/-/issues/42"
	localIssues := []*types.Issue{
		{
			ID:           "bd-1",
			Title:        "Same title",
			UpdatedAt:    now,
			ExternalRef:  &webURL,
			SourceSystem: "gitlab:123:42",
		},
	}

	conflicts, err := detectGitLabConflictsWithContext(ctx, syncCtx, client, localIssues)
	if err != nil {
		t.Fatalf("detectGitLabConflictsWithContext() error = %v", err)
	}

	if len(conflicts) != 0 {
		t.Errorf("detectGitLabConflictsWithContext() returned %d conflicts, want 0", len(conflicts))
	}
}

// TestDetectGitLabConflicts_WithConflicts verifies conflicts detected when both sides updated.
func TestDetectGitLabConflicts_WithConflicts(t *testing.T) {
	baseTime := time.Now().Add(-1 * time.Hour)
	gitlabTime := time.Now().Add(-30 * time.Minute)
	localTime := time.Now().Add(-15 * time.Minute)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]gitlab.Issue{
			{
				ID:        100,
				IID:       42,
				ProjectID: 123,
				Title:     "GitLab title",
				State:     "opened",
				UpdatedAt: &gitlabTime,
				WebURL:    "https://gitlab.example.com/-/issues/42",
			},
		})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	ctx := context.Background()
	syncCtx := NewSyncContext()

	// Local issue updated more recently than base but GitLab also updated
	webURL := "https://gitlab.example.com/-/issues/42"
	localIssues := []*types.Issue{
		{
			ID:           "bd-1",
			Title:        "Local title",
			UpdatedAt:    localTime,
			ExternalRef:  &webURL,
			SourceSystem: "gitlab:123:42",
		},
	}

	// Set base time (simulating last sync)
	_ = baseTime // Used for understanding, actual comparison is local vs gitlab

	conflicts, err := detectGitLabConflictsWithContext(ctx, syncCtx, client, localIssues)
	if err != nil {
		t.Fatalf("detectGitLabConflictsWithContext() error = %v", err)
	}

	if len(conflicts) != 1 {
		t.Errorf("detectGitLabConflictsWithContext() returned %d conflicts, want 1", len(conflicts))
	}
}

// TestDoPushToGitLab_PathBasedProjectID verifies push works with path-based project IDs.
func TestDoPushToGitLab_PathBasedProjectID(t *testing.T) {
	var updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			updateCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gitlab.Issue{
			ID:        100,
			IID:       42,
			ProjectID: 789, // Numeric project ID from API
			Title:     "Updated issue",
			State:     "opened",
			WebURL:    "https://gitlab.example.com/group/project/-/issues/42",
		})
	}))
	defer server.Close()

	// Client configured with path-based project ID
	client := gitlab.NewClient("token", server.URL, "group/project")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext()

	// Local issue linked to numeric project ID 789 (same project, different representation)
	webURL := "https://gitlab.example.com/group/project/-/issues/42"
	localIssues := []*types.Issue{
		{
			ID:           "bd-1",
			Title:        "Updated issue",
			Description:  "Updated description",
			IssueType:    types.TypeBug,
			Priority:     1,
			Status:       types.StatusOpen,
			ExternalRef:  &webURL,
			SourceSystem: "gitlab:789:42", // Numeric project ID from previous sync
		},
	}

	stats, err := doPushToGitLabWithContext(ctx, syncCtx, client, config, localIssues, false, false, nil, nil)
	if err != nil {
		t.Fatalf("doPushToGitLabWithContext() error = %v", err)
	}

	// Should update, not skip - the path "group/project" and numeric 789 are the same project
	if !updateCalled {
		t.Error("GitLab update API was not called - path-based project ID comparison failed")
	}
	if stats.Updated != 1 {
		t.Errorf("stats.Updated = %d, want 1 (got skipped due to project ID mismatch)", stats.Updated)
	}
}

// TestGenerateUniqueIssueIDs verifies IDs are unique even when generated rapidly.
func TestGenerateUniqueIssueIDs(t *testing.T) {
	seen := make(map[string]bool)
	prefix := "bd"

	// Generate 100 IDs rapidly
	for i := 0; i < 100; i++ {
		id := generateIssueID(prefix)
		if seen[id] {
			t.Errorf("Duplicate ID generated: %s", id)
		}
		seen[id] = true
	}
}

// TestGenerateIssueIDHasRandomComponent verifies IDs include random bytes for restart safety.
// Without random bytes, counter reset on restart could cause ID collisions.
func TestGenerateIssueIDHasRandomComponent(t *testing.T) {
	// Save current counter
	oldCounter := issueIDCounter
	defer func() { issueIDCounter = oldCounter }()

	prefix := "test"

	// Generate ID, reset counter (simulating restart), generate another
	// Both generated at same counter value - should still be unique due to random component
	issueIDCounter = 100
	id1 := generateIssueID(prefix)

	// Simulate restart by resetting counter to same value
	issueIDCounter = 100
	id2 := generateIssueID(prefix)

	// Even with same counter, IDs should differ due to random component
	// Note: timestamp might also differ, but within same millisecond they'd collide without randomness
	if id1 == id2 {
		t.Errorf("IDs should be unique even with counter reset: id1=%s, id2=%s", id1, id2)
	}

	// Verify format includes hex suffix (random bytes)
	// Expected format: prefix-timestamp-counter-hex
	parts := strings.Split(id1, "-")
	if len(parts) != 4 {
		t.Errorf("Expected 4 parts (prefix-timestamp-counter-random), got %d: %s", len(parts), id1)
	}
}

// TestGetConflictStrategy verifies conflict strategy selection from flags.
func TestGetConflictStrategy(t *testing.T) {
	tests := []struct {
		name           string
		preferLocal    bool
		preferGitLab   bool
		preferNewer    bool
		wantStrategy   ConflictStrategy
		wantError      bool
	}{
		{
			name:         "no flags - default to prefer-newer",
			wantStrategy: ConflictStrategyPreferNewer,
		},
		{
			name:         "prefer-local",
			preferLocal:  true,
			wantStrategy: ConflictStrategyPreferLocal,
		},
		{
			name:         "prefer-gitlab",
			preferGitLab: true,
			wantStrategy: ConflictStrategyPreferGitLab,
		},
		{
			name:         "prefer-newer explicit",
			preferNewer:  true,
			wantStrategy: ConflictStrategyPreferNewer,
		},
		{
			name:         "multiple flags - error",
			preferLocal:  true,
			preferGitLab: true,
			wantError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy, err := getConflictStrategy(tt.preferLocal, tt.preferGitLab, tt.preferNewer)
			if tt.wantError {
				if err == nil {
					t.Error("expected error for multiple flags, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strategy != tt.wantStrategy {
				t.Errorf("strategy = %q, want %q", strategy, tt.wantStrategy)
			}
		})
	}
}

// TestResolveConflicts_PreferLocal verifies --prefer-local always uses local version.
func TestResolveConflicts_PreferLocal(t *testing.T) {
	localTime := time.Now().Add(-1 * time.Hour) // Local is OLDER
	gitlabTime := time.Now()                     // GitLab is newer

	var fetchCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalled = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gitlab.Issue{})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	ctx := context.Background()
	syncCtx := NewSyncContext()

	conflicts := []gitlab.Conflict{
		{
			IssueID:       "bd-1",
			LocalUpdated:  localTime,
			GitLabUpdated: gitlabTime, // GitLab is newer, but we prefer local
			GitLabIID:     42,
			GitLabID:      100,
		},
	}

	// Should NOT fetch from GitLab when preferring local
	err := resolveGitLabConflictsWithContext(ctx, syncCtx, client, nil, conflicts, ConflictStrategyPreferLocal)
	if err != nil {
		t.Fatalf("resolveGitLabConflictsWithContext() error = %v", err)
	}

	if fetchCalled {
		t.Error("GitLab API was called when --prefer-local should skip remote fetch")
	}
}

// TestResolveConflicts_PreferGitLab verifies --prefer-gitlab always fetches from GitLab.
func TestResolveConflicts_PreferGitLab(t *testing.T) {
	localTime := time.Now()                       // Local is newer
	gitlabTime := time.Now().Add(-1 * time.Hour) // GitLab is OLDER

	var fetchCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalled = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gitlab.Issue{
			ID:          100,
			IID:         42,
			ProjectID:   123,
			Title:       "GitLab title",
			Description: "GitLab description",
			State:       "opened",
			UpdatedAt:   &gitlabTime,
			WebURL:      "https://gitlab.example.com/-/issues/42",
		})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext()

	conflicts := []gitlab.Conflict{
		{
			IssueID:       "bd-1",
			LocalUpdated:  localTime, // Local is newer, but we prefer gitlab
			GitLabUpdated: gitlabTime,
			GitLabIID:     42,
			GitLabID:      100,
		},
	}

	// Should fetch from GitLab even though local is newer
	err := resolveGitLabConflictsWithContext(ctx, syncCtx, client, config, conflicts, ConflictStrategyPreferGitLab)
	if err != nil {
		t.Fatalf("resolveGitLabConflictsWithContext() error = %v", err)
	}

	if !fetchCalled {
		t.Error("GitLab API was NOT called when --prefer-gitlab should always fetch")
	}
}

// TestResolveConflicts_PreferNewer verifies default behavior uses timestamps.
func TestResolveConflicts_PreferNewer(t *testing.T) {
	localTime := time.Now().Add(-1 * time.Hour) // Local is older
	gitlabTime := time.Now()                     // GitLab is newer

	var fetchCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalled = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gitlab.Issue{
			ID:        100,
			IID:       42,
			ProjectID: 123,
			Title:     "GitLab title",
			State:     "opened",
			UpdatedAt: &gitlabTime,
			WebURL:    "https://gitlab.example.com/-/issues/42",
		})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext()

	conflicts := []gitlab.Conflict{
		{
			IssueID:       "bd-1",
			LocalUpdated:  localTime,
			GitLabUpdated: gitlabTime, // GitLab is newer
			GitLabIID:     42,
			GitLabID:      100,
		},
	}

	// GitLab is newer, so should fetch from GitLab
	err := resolveGitLabConflictsWithContext(ctx, syncCtx, client, config, conflicts, ConflictStrategyPreferNewer)
	if err != nil {
		t.Fatalf("resolveGitLabConflictsWithContext() error = %v", err)
	}

	if !fetchCalled {
		t.Error("GitLab API was NOT called when GitLab version is newer")
	}
}

// =============================================================================
// P0 BUG FIX TESTS
// =============================================================================

// TestP0_ConflictDetectionBeforePush verifies that conflict detection happens
// BEFORE push, not after. This is critical because pushing before detecting
// conflicts can cause data loss - conflicting local changes overwrite GitLab
// changes before we even know there's a conflict.
//
// BUG: The current implementation does: Pull -> Push -> Detect conflicts (WRONG)
// FIX: Should be: Pull -> Detect conflicts -> Push (skip conflicting issues)
func TestP0_ConflictDetectionBeforePush(t *testing.T) {
	localTime := time.Now()
	gitlabTime := time.Now().Add(-30 * time.Minute)

	var pushCalls []int // Track which issue IIDs were pushed

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet {
			// Return issues for conflict detection
			_ = json.NewEncoder(w).Encode([]gitlab.Issue{
				{
					ID:        100,
					IID:       42,
					ProjectID: 123,
					Title:     "GitLab version",
					State:     "opened",
					UpdatedAt: &gitlabTime,
					WebURL:    "https://gitlab.example.com/-/issues/42",
				},
				{
					ID:        101,
					IID:       43,
					ProjectID: 123,
					Title:     "Another issue",
					State:     "opened",
					UpdatedAt: &gitlabTime,
					WebURL:    "https://gitlab.example.com/-/issues/43",
				},
			})
			return
		}

		if r.Method == http.MethodPut {
			// Track which issue was pushed (extract IID from URL)
			// URL format: /api/v4/projects/123/issues/42
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) >= 2 {
				if iid, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
					pushCalls = append(pushCalls, iid)
				}
			}
			_ = json.NewEncoder(w).Encode(gitlab.Issue{})
			return
		}
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()
	syncCtx := NewSyncContext()

	// Local issues - issue 42 is in conflict (local modified), issue 43 is safe
	webURL42 := "https://gitlab.example.com/-/issues/42"
	webURL43 := "https://gitlab.example.com/-/issues/43"
	localIssues := []*types.Issue{
		{
			ID:           "bd-1",
			Title:        "Local version", // Different from GitLab
			UpdatedAt:    localTime,       // More recent than GitLab
			ExternalRef:  &webURL42,
			SourceSystem: "gitlab:123:42",
		},
		{
			ID:           "bd-2",
			Title:        "Another issue", // Same as GitLab
			UpdatedAt:    gitlabTime,      // Same time as GitLab (no conflict)
			ExternalRef:  &webURL43,
			SourceSystem: "gitlab:123:43",
		},
	}

	// Detect conflicts BEFORE push
	conflicts, err := detectGitLabConflictsWithContext(ctx, syncCtx, client, localIssues)
	if err != nil {
		t.Fatalf("detectGitLabConflictsWithContext() error = %v", err)
	}

	// Should detect conflict for issue 42 (timestamps differ)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].GitLabIID != 42 {
		t.Errorf("expected conflict for IID 42, got %d", conflicts[0].GitLabIID)
	}

	// Build skipUpdateIDs from conflicts
	skipUpdateIDs := make(map[string]bool)
	for _, c := range conflicts {
		skipUpdateIDs[c.IssueID] = true
	}

	// Push should skip conflicting issues
	stats, err := doPushToGitLabWithContext(ctx, syncCtx, client, config, localIssues, false, false, nil, skipUpdateIDs)
	if err != nil {
		t.Fatalf("doPushToGitLabWithContext() error = %v", err)
	}

	// Issue 42 should be skipped, only issue 43 should be pushed
	if stats.Skipped != 1 {
		t.Errorf("stats.Skipped = %d, want 1 (conflict issue should be skipped)", stats.Skipped)
	}
	if stats.Updated != 1 {
		t.Errorf("stats.Updated = %d, want 1 (non-conflict issue should be pushed)", stats.Updated)
	}

	// Verify the push call was only for issue 43, not 42
	for _, iid := range pushCalls {
		if iid == 42 {
			t.Error("CRITICAL: Issue 42 was pushed despite being in conflict - this causes data loss!")
		}
	}
}

// TestP0_SyncContextIsolation verifies that SyncContext can be used instead of
// global variables, allowing multiple sync operations to run concurrently
// without race conditions.
//
// BUG: Current implementation uses global variables (store, actor, dbPath, issueIDCounter)
// FIX: Should use SyncContext struct passed to functions
func TestP0_SyncContextIsolation(t *testing.T) {
	// Test that SyncContext exists and can hold required state
	// This test will fail until SyncContext is implemented
	ctx1 := NewSyncContext()
	ctx2 := NewSyncContext()

	// Verify contexts are independent
	if ctx1 == ctx2 {
		t.Error("NewSyncContext should return distinct instances")
	}

	// Verify context can hold store reference
	ctx1.SetActor("user1")
	ctx2.SetActor("user2")

	if ctx1.Actor() == ctx2.Actor() {
		t.Error("SyncContext instances should have independent actor values")
	}

	// Verify issue ID generation is isolated per context
	id1 := ctx1.GenerateIssueID("bd")
	id2 := ctx2.GenerateIssueID("bd")

	if id1 == id2 {
		t.Error("Issue IDs generated from different contexts should be unique")
	}
}

// TestP0_SyncFunctionsUseSyncContext verifies that sync functions accept
// SyncContext instead of relying on global variables.
func TestP0_SyncFunctionsUseSyncContext(t *testing.T) {
	// Save and restore global store to avoid interfering with other tests
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]gitlab.Issue{})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()

	// Create a SyncContext
	syncCtx := NewSyncContext()
	syncCtx.SetActor("test-actor")

	// These function calls should work with SyncContext
	// The test verifies the API exists and compiles
	_, err := doPullFromGitLabWithContext(ctx, syncCtx, client, config, false, "all", nil)
	if err != nil {
		// Expected to fail if not implemented, but should compile
		t.Logf("doPullFromGitLabWithContext returned error (expected if not implemented): %v", err)
	}

	_, err = doPushToGitLabWithContext(ctx, syncCtx, client, config, nil, false, false, nil, nil)
	if err != nil {
		t.Logf("doPushToGitLabWithContext returned error (expected if not implemented): %v", err)
	}

	_, err = detectGitLabConflictsWithContext(ctx, syncCtx, client, nil)
	if err != nil {
		t.Logf("detectGitLabConflictsWithContext returned error (expected if not implemented): %v", err)
	}

	err = resolveGitLabConflictsWithContext(ctx, syncCtx, client, config, nil, ConflictStrategyPreferNewer)
	if err != nil {
		t.Logf("resolveGitLabConflictsWithContext returned error (expected if not implemented): %v", err)
	}
}

// =============================================================================
// END P0 BUG FIX TESTS
// =============================================================================

// TestParseGitLabSourceSystem verifies parsing source system string.
func TestParseGitLabSourceSystem(t *testing.T) {
	tests := []struct {
		name        string
		sourceSystem string
		wantProjectID int
		wantIID       int
		wantOK        bool
	}{
		{
			name:        "valid gitlab source",
			sourceSystem: "gitlab:123:42",
			wantProjectID: 123,
			wantIID:       42,
			wantOK:        true,
		},
		{
			name:        "different project",
			sourceSystem: "gitlab:456:99",
			wantProjectID: 456,
			wantIID:       99,
			wantOK:        true,
		},
		{
			name:        "non-gitlab source",
			sourceSystem: "linear:ABC-123",
			wantProjectID: 0,
			wantIID:       0,
			wantOK:        false,
		},
		{
			name:        "empty source",
			sourceSystem: "",
			wantProjectID: 0,
			wantIID:       0,
			wantOK:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID, iid, ok := parseGitLabSourceSystem(tt.sourceSystem)
			if ok != tt.wantOK {
				t.Errorf("parseGitLabSourceSystem(%q) ok = %v, want %v", tt.sourceSystem, ok, tt.wantOK)
			}
			if projectID != tt.wantProjectID {
				t.Errorf("parseGitLabSourceSystem(%q) projectID = %d, want %d", tt.sourceSystem, projectID, tt.wantProjectID)
			}
			if iid != tt.wantIID {
				t.Errorf("parseGitLabSourceSystem(%q) iid = %d, want %d", tt.sourceSystem, iid, tt.wantIID)
			}
		})
	}
}
