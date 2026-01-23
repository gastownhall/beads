// Package gitlab provides client and data types for the GitLab REST API.
package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewClient verifies the constructor creates a properly configured client.
func TestNewClient(t *testing.T) {
	client := NewClient("test-token", "https://gitlab.example.com", "123")

	if client.Token != "test-token" {
		t.Errorf("Token = %q, want %q", client.Token, "test-token")
	}
	if client.BaseURL != "https://gitlab.example.com" {
		t.Errorf("BaseURL = %q, want %q", client.BaseURL, "https://gitlab.example.com")
	}
	if client.ProjectID != "123" {
		t.Errorf("ProjectID = %q, want %q", client.ProjectID, "123")
	}
	if client.HTTPClient == nil {
		t.Error("HTTPClient is nil, want non-nil default client")
	}
}

// TestClientWithHTTPClient verifies the builder pattern for custom HTTP client.
func TestClientWithHTTPClient(t *testing.T) {
	customClient := &http.Client{Timeout: 60 * time.Second}
	client := NewClient("token", "https://gitlab.example.com", "123").
		WithHTTPClient(customClient)

	if client.HTTPClient != customClient {
		t.Error("HTTPClient not set to custom client")
	}
	// Original values preserved
	if client.Token != "token" {
		t.Errorf("Token = %q, want %q", client.Token, "token")
	}
}

// TestBuildURL verifies URL construction for API endpoints.
func TestBuildURL(t *testing.T) {
	client := NewClient("token", "https://gitlab.example.com", "123")

	tests := []struct {
		name     string
		path     string
		params   map[string]string
		wantURL  string
	}{
		{
			name:    "issues endpoint",
			path:    "/projects/123/issues",
			params:  nil,
			wantURL: "https://gitlab.example.com/api/v4/projects/123/issues",
		},
		{
			name:    "with query params",
			path:    "/projects/123/issues",
			params:  map[string]string{"state": "opened", "per_page": "100"},
			wantURL: "https://gitlab.example.com/api/v4/projects/123/issues",
		},
		{
			name:    "issue links",
			path:    "/projects/123/issues/42/links",
			params:  nil,
			wantURL: "https://gitlab.example.com/api/v4/projects/123/issues/42/links",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := client.buildURL(tt.path, tt.params)
			if !strings.HasPrefix(got, tt.wantURL) {
				t.Errorf("buildURL(%q) = %q, want prefix %q", tt.path, got, tt.wantURL)
			}
			// Verify query params are included
			for k, v := range tt.params {
				if !strings.Contains(got, k+"="+v) {
					t.Errorf("buildURL missing param %s=%s in %q", k, v, got)
				}
			}
		})
	}
}

// TestFetchIssues_Success verifies fetching issues from GitLab API.
func TestFetchIssues_Success(t *testing.T) {
	// Mock GitLab API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != http.MethodGet {
			t.Errorf("Method = %s, want GET", r.Method)
		}
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Errorf("PRIVATE-TOKEN header = %q, want %q", r.Header.Get("PRIVATE-TOKEN"), "test-token")
		}
		if !strings.Contains(r.URL.Path, "/projects/123/issues") {
			t.Errorf("URL path = %s, want to contain /projects/123/issues", r.URL.Path)
		}

		// Return mock response
		issues := []Issue{
			{ID: 1, IID: 1, Title: "First issue", State: "opened"},
			{ID: 2, IID: 2, Title: "Second issue", State: "opened"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClient("test-token", server.URL, "123")
	ctx := context.Background()

	issues, err := client.FetchIssues(ctx, "opened")
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}

	if len(issues) != 2 {
		t.Errorf("FetchIssues() returned %d issues, want 2", len(issues))
	}
	if issues[0].Title != "First issue" {
		t.Errorf("issues[0].Title = %q, want %q", issues[0].Title, "First issue")
	}
}

// TestFetchIssues_Pagination verifies client handles paginated responses.
func TestFetchIssues_Pagination(t *testing.T) {
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")

		if page == 1 {
			// First page - indicate more pages via X-Next-Page header
			w.Header().Set("X-Next-Page", "2")
			w.Header().Set("X-Total-Pages", "2")
			issues := []Issue{{ID: 1, IID: 1, Title: "Issue 1"}}
			json.NewEncoder(w).Encode(issues)
		} else {
			// Second page - no more pages
			w.Header().Set("X-Total-Pages", "2")
			issues := []Issue{{ID: 2, IID: 2, Title: "Issue 2"}}
			json.NewEncoder(w).Encode(issues)
		}
	}))
	defer server.Close()

	client := NewClient("token", server.URL, "123")
	ctx := context.Background()

	issues, err := client.FetchIssues(ctx, "all")
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}

	if len(issues) != 2 {
		t.Errorf("FetchIssues() returned %d issues, want 2 (from 2 pages)", len(issues))
	}
}

// TestFetchIssuesSince verifies incremental sync with updated_after param.
func TestFetchIssuesSince(t *testing.T) {
	since := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	var capturedURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Issue{})
	}))
	defer server.Close()

	client := NewClient("token", server.URL, "123")
	ctx := context.Background()

	_, err := client.FetchIssuesSince(ctx, "all", since)
	if err != nil {
		t.Fatalf("FetchIssuesSince() error = %v", err)
	}

	// Verify updated_after param in ISO8601 format
	if !strings.Contains(capturedURL, "updated_after=2024-01-15") {
		t.Errorf("URL = %s, want to contain updated_after=2024-01-15", capturedURL)
	}
}

// TestCreateIssue_Success verifies creating an issue via POST.
func TestCreateIssue_Success(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/projects/123/issues") {
			t.Errorf("URL path = %s, want to contain /projects/123/issues", r.URL.Path)
		}

		// Capture request body
		json.NewDecoder(r.Body).Decode(&capturedBody)

		// Return created issue
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Issue{
			ID:    100,
			IID:   42,
			Title: "New issue",
			State: "opened",
		})
	}))
	defer server.Close()

	client := NewClient("token", server.URL, "123")
	ctx := context.Background()

	issue, err := client.CreateIssue(ctx, "New issue", "Description here", []string{"bug", "priority::high"})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	if issue.IID != 42 {
		t.Errorf("issue.IID = %d, want 42", issue.IID)
	}
	if capturedBody["title"] != "New issue" {
		t.Errorf("request body title = %v, want %q", capturedBody["title"], "New issue")
	}
	if capturedBody["description"] != "Description here" {
		t.Errorf("request body description = %v, want %q", capturedBody["description"], "Description here")
	}
}

// TestUpdateIssue_Success verifies updating an issue via PUT.
func TestUpdateIssue_Success(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("Method = %s, want PUT", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/projects/123/issues/42") {
			t.Errorf("URL path = %s, want to contain /projects/123/issues/42", r.URL.Path)
		}

		json.NewDecoder(r.Body).Decode(&capturedBody)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Issue{
			ID:    100,
			IID:   42,
			Title: "Updated title",
			State: "opened",
		})
	}))
	defer server.Close()

	client := NewClient("token", server.URL, "123")
	ctx := context.Background()

	updates := map[string]interface{}{
		"title":       "Updated title",
		"state_event": "close",
	}
	issue, err := client.UpdateIssue(ctx, 42, updates)
	if err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}

	if issue.Title != "Updated title" {
		t.Errorf("issue.Title = %q, want %q", issue.Title, "Updated title")
	}
	if capturedBody["title"] != "Updated title" {
		t.Errorf("request body title = %v, want %q", capturedBody["title"], "Updated title")
	}
}

// TestGetIssueLinks_Success verifies fetching issue links.
func TestGetIssueLinks_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/projects/123/issues/42/links") {
			t.Errorf("URL path = %s, want to contain /projects/123/issues/42/links", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		links := []IssueLink{
			{
				SourceIssue: &Issue{IID: 42, Title: "Source"},
				TargetIssue: &Issue{IID: 43, Title: "Target"},
				LinkType:    "blocks",
			},
		}
		json.NewEncoder(w).Encode(links)
	}))
	defer server.Close()

	client := NewClient("token", server.URL, "123")
	ctx := context.Background()

	links, err := client.GetIssueLinks(ctx, 42)
	if err != nil {
		t.Fatalf("GetIssueLinks() error = %v", err)
	}

	if len(links) != 1 {
		t.Errorf("GetIssueLinks() returned %d links, want 1", len(links))
	}
	if links[0].LinkType != "blocks" {
		t.Errorf("links[0].LinkType = %q, want %q", links[0].LinkType, "blocks")
	}
}

// TestRateLimiting verifies retry behavior on 429 responses.
func TestRateLimiting(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Issue{{ID: 1, IID: 1, Title: "After retry"}})
	}))
	defer server.Close()

	client := NewClient("token", server.URL, "123")
	ctx := context.Background()

	issues, err := client.FetchIssues(ctx, "all")
	if err != nil {
		t.Fatalf("FetchIssues() error = %v, want success after retry", err)
	}

	if attempts < 2 {
		t.Errorf("attempts = %d, want >= 2 (retry after 429)", attempts)
	}
	if len(issues) != 1 {
		t.Errorf("FetchIssues() returned %d issues after retry, want 1", len(issues))
	}
}

// TestErrorHandling verifies error responses are properly reported.
func TestErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message": "401 Unauthorized"}`))
	}))
	defer server.Close()

	client := NewClient("bad-token", server.URL, "123")
	ctx := context.Background()

	_, err := client.FetchIssues(ctx, "all")
	if err == nil {
		t.Fatal("FetchIssues() error = nil, want error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want to contain '401'", err)
	}
}
