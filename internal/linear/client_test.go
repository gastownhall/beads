package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCanonicalizeLinearExternalRef(t *testing.T) {
	tests := []struct {
		name        string
		externalRef string
		want        string
		ok          bool
	}{
		{
			name:        "slugged url",
			externalRef: "https://linear.app/crown-dev/issue/BEA-93/updated-title-for-beads",
			want:        "https://linear.app/crown-dev/issue/BEA-93",
			ok:          true,
		},
		{
			name:        "canonical url",
			externalRef: "https://linear.app/crown-dev/issue/BEA-93",
			want:        "https://linear.app/crown-dev/issue/BEA-93",
			ok:          true,
		},
		{
			name:        "not linear",
			externalRef: "https://example.com/issues/BEA-93",
			want:        "",
			ok:          false,
		},
	}

	for _, tt := range tests {
		got, ok := CanonicalizeLinearExternalRef(tt.externalRef)
		if ok != tt.ok {
			t.Fatalf("%s: ok=%v, want %v", tt.name, ok, tt.ok)
		}
		if got != tt.want {
			t.Fatalf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestNewClient(t *testing.T) {
	client := NewClient("test-api-key", "test-team-id")

	if client.APIKey != "test-api-key" {
		t.Errorf("APIKey = %q, want %q", client.APIKey, "test-api-key")
	}
	if client.TeamID != "test-team-id" {
		t.Errorf("TeamID = %q, want %q", client.TeamID, "test-team-id")
	}
	if client.Endpoint != DefaultAPIEndpoint {
		t.Errorf("Endpoint = %q, want %q", client.Endpoint, DefaultAPIEndpoint)
	}
	if client.HTTPClient == nil {
		t.Error("HTTPClient should not be nil")
	}
}

func TestWithEndpoint(t *testing.T) {
	client := NewClient("key", "team")
	customEndpoint := "https://custom.linear.app/graphql"

	newClient := client.WithEndpoint(customEndpoint)

	if newClient.Endpoint != customEndpoint {
		t.Errorf("Endpoint = %q, want %q", newClient.Endpoint, customEndpoint)
	}
	// Original should be unchanged
	if client.Endpoint != DefaultAPIEndpoint {
		t.Errorf("Original endpoint changed: %q", client.Endpoint)
	}
	// Other fields preserved
	if newClient.APIKey != "key" {
		t.Errorf("APIKey not preserved: %q", newClient.APIKey)
	}
}

func TestWithHTTPClient(t *testing.T) {
	client := NewClient("key", "team")
	customHTTPClient := &http.Client{Timeout: 60 * time.Second}

	newClient := client.WithHTTPClient(customHTTPClient)

	if newClient.HTTPClient != customHTTPClient {
		t.Error("HTTPClient not set correctly")
	}
	// Other fields preserved
	if newClient.APIKey != "key" {
		t.Errorf("APIKey not preserved: %q", newClient.APIKey)
	}
	if newClient.Endpoint != DefaultAPIEndpoint {
		t.Errorf("Endpoint not preserved: %q", newClient.Endpoint)
	}
}

func TestExtractLinearIdentifier(t *testing.T) {
	tests := []struct {
		name        string
		externalRef string
		want        string
	}{
		{
			name:        "standard URL",
			externalRef: "https://linear.app/team/issue/PROJ-123",
			want:        "PROJ-123",
		},
		{
			name:        "URL with slug",
			externalRef: "https://linear.app/team/issue/PROJ-456/some-title-here",
			want:        "PROJ-456",
		},
		{
			name:        "URL with trailing slash",
			externalRef: "https://linear.app/team/issue/ABC-789/",
			want:        "ABC-789",
		},
		{
			name:        "non-linear URL",
			externalRef: "https://jira.example.com/browse/PROJ-123",
			want:        "",
		},
		{
			name:        "empty string",
			externalRef: "",
			want:        "",
		},
		{
			name:        "malformed URL",
			externalRef: "not-a-url",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractLinearIdentifier(tt.externalRef)
			if got != tt.want {
				t.Errorf("ExtractLinearIdentifier(%q) = %q, want %q", tt.externalRef, got, tt.want)
			}
		})
	}
}

func TestIsLinearExternalRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://linear.app/team/issue/PROJ-123", true},
		{"https://linear.app/team/issue/PROJ-123/slug", true},
		{"https://jira.example.com/browse/PROJ-123", false},
		{"https://github.com/org/repo/issues/123", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := IsLinearExternalRef(tt.ref)
			if got != tt.want {
				t.Errorf("IsLinearExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestBatchCreateIssues_SingleBatch(t *testing.T) {
	mutationCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutationCount++
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		if !strings.Contains(req.Query, "issueBatchCreate") {
			t.Fatalf("expected issueBatchCreate mutation, got: %s", req.Query)
		}

		issues := make([]Issue, 0)
		inputs := req.Variables["input"].([]interface{})
		for i := range inputs {
			issues = append(issues, Issue{
				ID:         fmt.Sprintf("id-%d", i),
				Identifier: fmt.Sprintf("TEAM-%d", i+1),
				Title:      fmt.Sprintf("Issue %d", i+1),
				URL:        fmt.Sprintf("https://linear.app/team/issue/TEAM-%d", i+1),
			})
		}
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issueBatchCreate": map[string]interface{}{
					"success": true,
					"issues":  issues,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("test-key", "test-team").WithEndpoint(server.URL)
	inputs := make([]IssueCreateInput, 50)
	for i := range inputs {
		inputs[i] = IssueCreateInput{
			TeamID: "test-team",
			Title:  fmt.Sprintf("Issue %d", i+1),
		}
	}

	issues, err := client.BatchCreateIssues(context.Background(), inputs)
	if err != nil {
		t.Fatalf("BatchCreateIssues failed: %v", err)
	}
	if mutationCount != 1 {
		t.Errorf("expected 1 mutation call, got %d", mutationCount)
	}
	if len(issues) != 50 {
		t.Errorf("expected 50 issues, got %d", len(issues))
	}
}

func TestBatchCreateIssues_Chunking(t *testing.T) {
	mutationCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutationCount++
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		json.Unmarshal(body, &req)

		inputs := req.Variables["input"].([]interface{})
		issues := make([]Issue, len(inputs))
		for i := range inputs {
			issues[i] = Issue{
				ID:         fmt.Sprintf("id-%d-%d", mutationCount, i),
				Identifier: fmt.Sprintf("TEAM-%d", i+1),
				Title:      fmt.Sprintf("Issue %d", i+1),
				URL:        fmt.Sprintf("https://linear.app/team/issue/TEAM-%d", i+1),
			}
		}
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issueBatchCreate": map[string]interface{}{
					"success": true,
					"issues":  issues,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("test-key", "test-team").WithEndpoint(server.URL)
	inputs := make([]IssueCreateInput, 120)
	for i := range inputs {
		inputs[i] = IssueCreateInput{
			TeamID: "test-team",
			Title:  fmt.Sprintf("Issue %d", i+1),
		}
	}

	issues, err := client.BatchCreateIssues(context.Background(), inputs)
	if err != nil {
		t.Fatalf("BatchCreateIssues failed: %v", err)
	}
	if mutationCount != 3 {
		t.Errorf("expected 3 batch calls (50+50+20), got %d", mutationCount)
	}
	if len(issues) != 120 {
		t.Errorf("expected 120 issues, got %d", len(issues))
	}
}

func TestBatchCreateIssues_FallbackOnFailure(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		json.Unmarshal(body, &req)

		if strings.Contains(req.Query, "issueBatchCreate") {
			// Batch call fails with success=false.
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"issueBatchCreate": map[string]interface{}{
						"success": false,
						"issues":  []Issue{},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.Contains(req.Query, "issueCreate") {
			// Single-issue fallback succeeds.
			input := req.Variables["input"].(map[string]interface{})
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"issueCreate": map[string]interface{}{
						"success": true,
						"issue": Issue{
							ID:         fmt.Sprintf("id-fallback-%d", callCount),
							Identifier: fmt.Sprintf("TEAM-%d", callCount),
							Title:      input["title"].(string),
							URL:        fmt.Sprintf("https://linear.app/team/issue/TEAM-%d", callCount),
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
	}))
	defer server.Close()

	client := NewClient("test-key", "test-team").WithEndpoint(server.URL)
	inputs := []IssueCreateInput{
		{TeamID: "test-team", Title: "Issue A"},
		{TeamID: "test-team", Title: "Issue B"},
		{TeamID: "test-team", Title: "Issue C"},
		{TeamID: "test-team", Title: "Issue D"},
		{TeamID: "test-team", Title: "Issue E"},
	}

	issues, err := client.BatchCreateIssues(context.Background(), inputs)
	if err != nil {
		t.Fatalf("BatchCreateIssues with fallback failed: %v", err)
	}
	if len(issues) != 5 {
		t.Errorf("expected 5 issues from fallback, got %d", len(issues))
	}
	// 1 batch call + 5 individual creates = 6 total calls
	if callCount != 6 {
		t.Errorf("expected 6 total calls (1 batch + 5 single), got %d", callCount)
	}
}

func TestBatchUpdateIssues_Chunking(t *testing.T) {
	mutationCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutationCount++
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		json.Unmarshal(body, &req)

		ids := req.Variables["ids"].([]interface{})
		issues := make([]Issue, len(ids))
		for i, id := range ids {
			issues[i] = Issue{
				ID:         id.(string),
				Identifier: fmt.Sprintf("TEAM-%d", i+1),
				URL:        fmt.Sprintf("https://linear.app/team/issue/TEAM-%d", i+1),
			}
		}
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issueBatchUpdate": map[string]interface{}{
					"success": true,
					"issues":  issues,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("test-key", "test-team").WithEndpoint(server.URL)
	ids := make([]string, 120)
	for i := range ids {
		ids[i] = fmt.Sprintf("uuid-%d", i)
	}

	updates := map[string]interface{}{"stateId": "done-state-id"}
	issues, err := client.BatchUpdateIssues(context.Background(), ids, updates)
	if err != nil {
		t.Fatalf("BatchUpdateIssues failed: %v", err)
	}
	if mutationCount != 3 {
		t.Errorf("expected 3 batch calls (50+50+20), got %d", mutationCount)
	}
	if len(issues) != 120 {
		t.Errorf("expected 120 issues, got %d", len(issues))
	}
}

func TestBatchCreateIssues_Empty(t *testing.T) {
	client := NewClient("test-key", "test-team")
	issues, err := client.BatchCreateIssues(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error for empty input, got: %v", err)
	}
	if issues != nil {
		t.Errorf("expected nil result for empty input, got %d issues", len(issues))
	}
}

func TestBatchUpdateIssues_Empty(t *testing.T) {
	client := NewClient("test-key", "test-team")
	issues, err := client.BatchUpdateIssues(context.Background(), nil, map[string]interface{}{"title": "x"})
	if err != nil {
		t.Fatalf("expected no error for empty input, got: %v", err)
	}
	if issues != nil {
		t.Errorf("expected nil result for empty input, got %d issues", len(issues))
	}
}
