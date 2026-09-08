package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateIdempotencyMarker(t *testing.T) {
	marker := GenerateIdempotencyMarker("btl-abc", "alice@example.com", 1714600000000000000)

	if !strings.HasPrefix(marker, "<!-- bd-idempotency: ") {
		t.Errorf("marker missing prefix, got %q", marker)
	}
	if !strings.HasSuffix(marker, " -->") {
		t.Errorf("marker missing suffix, got %q", marker)
	}

	// Hash portion is 12 hex chars.
	hash := strings.TrimPrefix(marker, "<!-- bd-idempotency: ")
	hash = strings.TrimSuffix(hash, " -->")
	if len(hash) != 12 {
		t.Errorf("hash length = %d, want 12", len(hash))
	}
}

func TestGenerateIdempotencyMarkerDeterministic(t *testing.T) {
	a := GenerateIdempotencyMarker("btl-abc", "alice@example.com", 1714600000000000000)
	b := GenerateIdempotencyMarker("btl-abc", "alice@example.com", 1714600000000000000)
	if a != b {
		t.Errorf("markers not deterministic: %q vs %q", a, b)
	}
}

func TestGenerateIdempotencyMarkerExcludesTitle(t *testing.T) {
	// Same inputs except we're not passing title at all — the function
	// signature doesn't accept it. This test documents the design decision
	// that title changes don't break idempotency.
	a := GenerateIdempotencyMarker("id-1", "bob@test.com", 100)
	b := GenerateIdempotencyMarker("id-1", "bob@test.com", 100)
	if a != b {
		t.Errorf("markers differ despite identical non-title inputs")
	}

	// Different beadID → different marker.
	c := GenerateIdempotencyMarker("id-2", "bob@test.com", 100)
	if a == c {
		t.Error("different beadID should produce different marker")
	}
}

func TestGenerateIdempotencyMarkerVariesOnInputs(t *testing.T) {
	base := GenerateIdempotencyMarker("id-1", "a@b.com", 100)

	if m := GenerateIdempotencyMarker("id-2", "a@b.com", 100); m == base {
		t.Error("different beadID should change marker")
	}
	if m := GenerateIdempotencyMarker("id-1", "x@y.com", 100); m == base {
		t.Error("different creatorEmail should change marker")
	}
	if m := GenerateIdempotencyMarker("id-1", "a@b.com", 999); m == base {
		t.Error("different createdAtNano should change marker")
	}
}

func TestAppendIdempotencyMarker(t *testing.T) {
	marker := "<!-- bd-idempotency: abc123def456 -->"

	got := AppendIdempotencyMarker("existing description", marker)
	if got != "existing description\n"+marker {
		t.Errorf("append to non-empty = %q", got)
	}

	got = AppendIdempotencyMarker("", marker)
	if got != marker {
		t.Errorf("append to empty = %q, want just marker", got)
	}
}

// graphqlHandler dispatches test responses based on the GraphQL operation.
type graphqlHandler struct {
	t             *testing.T
	searchCalls   int
	createCalls   int
	searchResults []Issue
}

func (h *graphqlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req GraphQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.t.Fatalf("failed to decode request: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")

	if strings.Contains(req.Query, "FindByDescription") {
		h.searchCalls++
		nodes := h.searchResults
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issues": map[string]interface{}{
					"nodes": nodes,
					"pageInfo": map[string]interface{}{
						"hasNextPage": false,
						"endCursor":   "",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	if strings.Contains(req.Query, "issueCreate") {
		h.createCalls++

		// Extract description from input to verify marker embedding.
		input, _ := req.Variables["input"].(map[string]interface{})
		desc, _ := input["description"].(string)

		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issueCreate": map[string]interface{}{
					"success": true,
					"issue": map[string]interface{}{
						"id":          "new-uuid",
						"identifier":  "TEAM-99",
						"title":       input["title"],
						"description": desc,
						"url":         "https://linear.app/team/issue/TEAM-99",
						"priority":    input["priority"],
						"state": map[string]interface{}{
							"id":   "state-1",
							"name": "Todo",
							"type": "unstarted",
						},
						"createdAt": "2026-05-01T10:00:00Z",
						"updatedAt": "2026-05-01T10:00:00Z",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	h.t.Fatalf("unexpected query: %s", req.Query)
}

func TestCreateIssueEmbedsMarker(t *testing.T) {
	handler := &graphqlHandler{t: t}
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient("test-key", "team-1").WithEndpoint(server.URL)

	marker := GenerateIdempotencyMarker("bead-1", "dev@test.com", 100)
	issue, deduped, err := client.CreateIssueIdempotent(
		context.Background(),
		"Test Issue",
		"Original description",
		3, "", nil,
		marker,
	)
	if err != nil {
		t.Fatalf("CreateIssueIdempotent failed: %v", err)
	}
	if deduped {
		t.Error("expected deduped=false for fresh create")
	}
	if issue == nil {
		t.Fatal("expected non-nil issue")
	}

	if handler.searchCalls != 1 {
		t.Errorf("search calls = %d, want 1", handler.searchCalls)
	}
	if handler.createCalls != 1 {
		t.Errorf("create calls = %d, want 1", handler.createCalls)
	}

	if !strings.Contains(issue.Description, marker) {
		t.Errorf("issue description should contain marker, got %q", issue.Description)
	}
}

func TestCreateIssueDedups(t *testing.T) {
	existing := Issue{
		ID:         "existing-uuid",
		Identifier: "TEAM-42",
		Title:      "Already Created",
		URL:        "https://linear.app/team/issue/TEAM-42",
	}

	handler := &graphqlHandler{
		t:             t,
		searchResults: []Issue{existing},
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient("test-key", "team-1").WithEndpoint(server.URL)

	marker := GenerateIdempotencyMarker("bead-1", "dev@test.com", 100)
	issue, deduped, err := client.CreateIssueIdempotent(
		context.Background(),
		"Already Created",
		"desc",
		3, "", nil,
		marker,
	)
	if err != nil {
		t.Fatalf("CreateIssueIdempotent failed: %v", err)
	}
	if !deduped {
		t.Error("expected deduped=true when search finds existing issue")
	}
	if issue.Identifier != "TEAM-42" {
		t.Errorf("returned issue identifier = %q, want TEAM-42", issue.Identifier)
	}

	if handler.searchCalls != 1 {
		t.Errorf("search calls = %d, want 1", handler.searchCalls)
	}
	if handler.createCalls != 0 {
		t.Errorf("create calls = %d, want 0 (dedup should skip create)", handler.createCalls)
	}
}

func TestCreateIssueBackwardCompat(t *testing.T) {
	handler := &graphqlHandler{t: t}
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient("test-key", "team-1").WithEndpoint(server.URL)

	// Plain CreateIssue (no marker) still works — backward compat path.
	issue, err := client.CreateIssue(
		context.Background(),
		"Legacy Issue",
		"No marker here",
		2, "", nil,
	)
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if issue == nil {
		t.Fatal("expected non-nil issue")
	}
	if issue.Identifier != "TEAM-99" {
		t.Errorf("identifier = %q, want TEAM-99", issue.Identifier)
	}

	// No search should have been performed — only the create call.
	if handler.searchCalls != 0 {
		t.Errorf("search calls = %d, want 0 for backward-compat path", handler.searchCalls)
	}
	if handler.createCalls != 1 {
		t.Errorf("create calls = %d, want 1", handler.createCalls)
	}
}

func TestCreateIssueIdempotentEmptyDescription(t *testing.T) {
	handler := &graphqlHandler{t: t}
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient("test-key", "team-1").WithEndpoint(server.URL)

	marker := GenerateIdempotencyMarker("bead-x", "dev@test.com", 200)
	issue, _, err := client.CreateIssueIdempotent(
		context.Background(),
		"Empty Desc Issue",
		"",
		0, "", nil,
		marker,
	)
	if err != nil {
		t.Fatalf("CreateIssueIdempotent failed: %v", err)
	}

	// When description is empty, the marker becomes the full description.
	if issue.Description != marker {
		t.Errorf("description = %q, want just marker %q", issue.Description, marker)
	}
}

// recoveryHandler simulates an ambiguous create failure followed by a
// successful dedup search. It models the scenario where issueCreate reaches
// Linear (the issue is created) but the HTTP response is lost, so the next
// call to FindIssueByDescriptionContains finds the already-created issue.
type recoveryHandler struct {
	t             *testing.T
	createdIssue  Issue
	findCallCount int
	createCalls   int
}

func (h *recoveryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req GraphQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.t.Fatalf("failed to decode request: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")

	if strings.Contains(req.Query, "FindByDescription") {
		h.findCallCount++
		var nodes []Issue
		if h.findCallCount >= 2 {
			// Recovery search: return the issue that was created despite the error.
			nodes = []Issue{h.createdIssue}
		}
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issues": map[string]interface{}{
					"nodes":    nodes,
					"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	if strings.Contains(req.Query, "issueCreate") {
		h.createCalls++
		// Simulate: mutation reached Linear (issue created) but response was lost.
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
		return
	}

	h.t.Fatalf("unexpected query: %s", req.Query)
}

func TestCreateIssueIdempotentRecoverAfterAmbiguousFailure(t *testing.T) {
	recovered := Issue{
		ID:         "recovered-uuid",
		Identifier: "TEAM-77",
		Title:      "Network Failure Issue",
		URL:        "https://linear.app/team/issue/TEAM-77",
	}

	handler := &recoveryHandler{t: t, createdIssue: recovered}
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient("test-key", "team-1").WithEndpoint(server.URL)

	marker := GenerateIdempotencyMarker("bead-net", "dev@test.com", 42)
	issue, deduped, err := client.CreateIssueIdempotent(
		context.Background(),
		"Network Failure Issue",
		"some description",
		3, "", nil,
		marker,
	)
	if err != nil {
		t.Fatalf("CreateIssueIdempotent should recover after ambiguous failure, got: %v", err)
	}
	if !deduped {
		t.Error("expected deduped=true on recovery (issue was created despite the error)")
	}
	if issue == nil || issue.Identifier != "TEAM-77" {
		t.Errorf("expected recovered issue TEAM-77, got %v", issue)
	}

	if handler.createCalls != 1 {
		t.Errorf("create calls = %d, want 1 (no retry of the mutation)", handler.createCalls)
	}
	if handler.findCallCount != 2 {
		t.Errorf("find calls = %d, want 2 (initial check + recovery check)", handler.findCallCount)
	}
}
