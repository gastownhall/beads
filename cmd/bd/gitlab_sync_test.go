// Package main provides the bd CLI commands.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoPullFromGitLab_Success verifies pulling issues from GitLab creates beads issues.
func TestDoPullFromGitLab_Success(t *testing.T) {
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

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
		json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()

	stats, err := doPullFromGitLab(ctx, client, config, false, "all", nil)
	if err != nil {
		t.Fatalf("doPullFromGitLab() error = %v", err)
	}

	if stats.Created != 1 {
		t.Errorf("stats.Created = %d, want 1", stats.Created)
	}
}

// TestDoPullFromGitLab_DryRun verifies dry run mode doesn't create issues.
func TestDoPullFromGitLab_DryRun(t *testing.T) {
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

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
		json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()

	stats, err := doPullFromGitLab(ctx, client, config, true, "all", nil)
	if err != nil {
		t.Fatalf("doPullFromGitLab() error = %v", err)
	}

	// Dry run should report what would be created but not actually create
	if stats.Created != 0 {
		t.Errorf("dry run stats.Created = %d, want 0", stats.Created)
	}
}

// TestDoPullFromGitLab_SkipIssues verifies skipGitLabIIDs filters issues.
func TestDoPullFromGitLab_SkipIssues(t *testing.T) {
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		issues := []gitlab.Issue{
			{ID: 1, IID: 1, ProjectID: 123, Title: "Issue 1", State: "opened", WebURL: "https://gitlab.example.com/-/issues/1"},
			{ID: 2, IID: 2, ProjectID: 123, Title: "Issue 2", State: "opened", WebURL: "https://gitlab.example.com/-/issues/2"},
			{ID: 3, IID: 3, ProjectID: 123, Title: "Issue 3", State: "opened", WebURL: "https://gitlab.example.com/-/issues/3"},
		}
		json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()

	// Skip issue IID 2
	skipIIDs := map[int]bool{2: true}

	stats, err := doPullFromGitLab(ctx, client, config, false, "all", skipIIDs)
	if err != nil {
		t.Fatalf("doPullFromGitLab() error = %v", err)
	}

	if stats.Skipped != 1 {
		t.Errorf("stats.Skipped = %d, want 1", stats.Skipped)
	}
}

// TestDoPushToGitLab_CreateNew verifies pushing new issues to GitLab.
func TestDoPushToGitLab_CreateNew(t *testing.T) {
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	var createCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			createCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(gitlab.Issue{
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
		json.NewEncoder(w).Encode([]gitlab.Issue{})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()

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

	stats, err := doPushToGitLab(ctx, client, config, localIssues, false, false, nil, nil)
	if err != nil {
		t.Fatalf("doPushToGitLab() error = %v", err)
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
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	var updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			updateCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gitlab.Issue{
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

	stats, err := doPushToGitLab(ctx, client, config, localIssues, false, false, nil, nil)
	if err != nil {
		t.Fatalf("doPushToGitLab() error = %v", err)
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
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	now := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlab.Issue{
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

	conflicts, err := detectGitLabConflicts(ctx, client, localIssues)
	if err != nil {
		t.Fatalf("detectGitLabConflicts() error = %v", err)
	}

	if len(conflicts) != 0 {
		t.Errorf("detectGitLabConflicts() returned %d conflicts, want 0", len(conflicts))
	}
}

// TestDetectGitLabConflicts_WithConflicts verifies conflicts detected when both sides updated.
func TestDetectGitLabConflicts_WithConflicts(t *testing.T) {
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	baseTime := time.Now().Add(-1 * time.Hour)
	gitlabTime := time.Now().Add(-30 * time.Minute)
	localTime := time.Now().Add(-15 * time.Minute)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]gitlab.Issue{
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

	conflicts, err := detectGitLabConflicts(ctx, client, localIssues)
	if err != nil {
		t.Fatalf("detectGitLabConflicts() error = %v", err)
	}

	if len(conflicts) != 1 {
		t.Errorf("detectGitLabConflicts() returned %d conflicts, want 1", len(conflicts))
	}
}

// TestDoPushToGitLab_PathBasedProjectID verifies push works with path-based project IDs.
func TestDoPushToGitLab_PathBasedProjectID(t *testing.T) {
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	var updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			updateCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gitlab.Issue{
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

	stats, err := doPushToGitLab(ctx, client, config, localIssues, false, false, nil, nil)
	if err != nil {
		t.Fatalf("doPushToGitLab() error = %v", err)
	}

	// Should update, not skip - the path "group/project" and numeric 789 are the same project
	if !updateCalled {
		t.Error("GitLab update API was not called - path-based project ID comparison failed")
	}
	if stats.Updated != 1 {
		t.Errorf("stats.Updated = %d, want 1 (got skipped due to project ID mismatch)", stats.Updated)
	}
}

// TestResolveGitLabConflictsByTimestamp_GitLabWins verifies conflict resolution prefers newer.
func TestResolveGitLabConflictsByTimestamp_GitLabWins(t *testing.T) {
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	gitlabTime := time.Now()
	localTime := time.Now().Add(-1 * time.Hour) // Local is older

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return single issue for FetchIssueByIID
		json.NewEncoder(w).Encode(gitlab.Issue{
			ID:          100,
			IID:         42,
			ProjectID:   123,
			Title:       "GitLab updated title",
			Description: "GitLab updated description",
			State:       "opened",
			UpdatedAt:   &gitlabTime,
			WebURL:      "https://gitlab.example.com/-/issues/42",
		})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()

	conflicts := []gitlab.Conflict{
		{
			IssueID:       "bd-1",
			LocalUpdated:  localTime,
			GitLabUpdated: gitlabTime, // GitLab is newer
			GitLabIID:     42,
			GitLabID:      100,
		},
	}

	err := resolveGitLabConflictsByTimestamp(ctx, client, config, conflicts)
	if err != nil {
		t.Fatalf("resolveGitLabConflictsByTimestamp() error = %v", err)
	}

	// Test passes if no error - actual store update requires store to be set
	// This test verifies the GitLab fetch path works
}

// TestResolveGitLabConflictsByTimestamp_LocalWins verifies local version kept when newer.
func TestResolveGitLabConflictsByTimestamp_LocalWins(t *testing.T) {
	// Save and restore global store
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	localTime := time.Now()
	gitlabTime := time.Now().Add(-1 * time.Hour) // GitLab is older

	var fetchCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gitlab.Issue{})
	}))
	defer server.Close()

	client := gitlab.NewClient("token", server.URL, "123")
	config := gitlab.DefaultMappingConfig()
	ctx := context.Background()

	conflicts := []gitlab.Conflict{
		{
			IssueID:       "bd-1",
			LocalUpdated:  localTime,   // Local is newer
			GitLabUpdated: gitlabTime,
			GitLabIID:     42,
			GitLabID:      100,
		},
	}

	err := resolveGitLabConflictsByTimestamp(ctx, client, config, conflicts)
	if err != nil {
		t.Fatalf("resolveGitLabConflictsByTimestamp() error = %v", err)
	}

	// When local wins, should NOT fetch from GitLab
	if fetchCalled {
		t.Error("GitLab API was called when local version should win")
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
