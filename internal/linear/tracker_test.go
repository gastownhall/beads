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

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// teamStatesResp builds the JSON body for a TeamStates GraphQL response.
func teamStatesResp(teamID, stateID, stateName, stateType string) map[string]interface{} {
	return map[string]interface{}{
		"data": map[string]interface{}{
			"team": map[string]interface{}{
				"id": teamID,
				"states": map[string]interface{}{
					"nodes": []interface{}{
						map[string]interface{}{
							"id":   stateID,
							"name": stateName,
							"type": stateType,
						},
					},
				},
			},
		},
	}
}

// issueByIdentifierResp builds the JSON body for an IssueByIdentifier GraphQL response.
func issueByIdentifierResp(id, identifier, title, description string, priority int, stateID, stateName, stateType string) map[string]interface{} {
	return map[string]interface{}{
		"data": map[string]interface{}{
			"issues": map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{
						"id":          id,
						"identifier":  identifier,
						"title":       title,
						"description": description,
						"url":         "https://linear.app/team/issue/" + identifier,
						"priority":    priority,
						"state": map[string]interface{}{
							"id":   stateID,
							"name": stateName,
							"type": stateType,
						},
						"createdAt": "2026-01-01T00:00:00Z",
						"updatedAt": "2026-01-01T00:00:00Z",
					},
				},
			},
		},
	}
}

// TestBatchPush_SkipsUnchangedIssue verifies that BatchPush does not call
// UpdateIssue when the remote issue content matches the local issue. The
// single-issue push path in engine.go:doPush performs this ContentEqual /
// UpdatedAt skip check; BatchPush must replicate it so every sync does not
// re-push all Linear-linked issues unchanged.
func TestBatchPush_SkipsUnchangedIssue(t *testing.T) {
	var updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "TeamStates"):
			json.NewEncoder(w).Encode(teamStatesResp("team-1", "state-open", "Backlog", "backlog"))
		case strings.Contains(req.Query, "IssueByIdentifier"):
			// Remote issue has the same title, empty description, priority 0 (no
			// priority), and "backlog" state — matching the local issue exactly.
			json.NewEncoder(w).Encode(issueByIdentifierResp(
				"remote-uuid", "TEAM-1", "My Issue", "", 0,
				"state-open", "Backlog", "backlog",
			))
		case strings.Contains(req.Query, "issueUpdate"):
			updateCalled = true
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueUpdate": map[string]interface{}{
						"success": true,
						"issue":   map[string]interface{}{"id": "remote-uuid", "url": "https://linear.app/team/issue/TEAM-1", "updatedAt": "2026-01-01T00:00:00Z"},
					},
				},
			})
		}
	}))
	defer server.Close()

	cfg := DefaultMappingConfig()
	// ExplicitStateMap is required by ResolveStateIDForBeadsStatus.
	cfg.ExplicitStateMap = map[string]string{"backlog": "open"}

	extRef := "https://linear.app/team/issue/TEAM-1"
	// Priority 4 (beads backlog) → PriorityToLinear = 0 (no priority) via default map.
	// Status open + state backlog → PushFieldsEqual returns true.
	local := &types.Issue{
		ID:          "local-1",
		Title:       "My Issue",
		Status:      types.StatusOpen,
		Priority:    4,
		ExternalRef: &extRef,
	}

	tr := &Tracker{
		teamIDs: []string{"team-1"},
		clients: map[string]*Client{
			"team-1": NewClient("key", "team-1").WithEndpoint(server.URL),
		},
		config: cfg,
	}

	result, err := tr.BatchPush(context.Background(), []*types.Issue{local}, nil)
	if err != nil {
		t.Fatalf("BatchPush: %v", err)
	}
	if updateCalled {
		t.Error("UpdateIssue was called for an unchanged issue; expected it to be skipped")
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "local-1" {
		t.Errorf("Skipped = %v, want [local-1]", result.Skipped)
	}
	if len(result.Updated) != 0 {
		t.Errorf("Updated = %v, want []", result.Updated)
	}
}

// TestBatchPush_ForceBypassesSkip verifies that an issue in forceIDs is
// updated even when PushFieldsEqual would normally skip it.
func TestBatchPush_ForceBypassesSkip(t *testing.T) {
	var updateCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "TeamStates"):
			json.NewEncoder(w).Encode(teamStatesResp("team-1", "state-open", "Backlog", "backlog"))
		case strings.Contains(req.Query, "IssueByIdentifier"):
			// Return the same content as local (would be skipped without force).
			json.NewEncoder(w).Encode(issueByIdentifierResp(
				"remote-uuid", "TEAM-1", "My Issue", "", 0,
				"state-open", "Backlog", "backlog",
			))
		case strings.Contains(req.Query, "issueUpdate"):
			updateCalled = true
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueUpdate": map[string]interface{}{
						"success": true,
						"issue":   map[string]interface{}{"id": "remote-uuid", "url": "https://linear.app/team/issue/TEAM-1", "updatedAt": "2026-01-02T00:00:00Z"},
					},
				},
			})
		}
	}))
	defer server.Close()

	cfg := DefaultMappingConfig()
	cfg.ExplicitStateMap = map[string]string{"backlog": "open"}

	extRef := "https://linear.app/team/issue/TEAM-1"
	local := &types.Issue{
		ID:          "local-1",
		Title:       "My Issue",
		Status:      types.StatusOpen,
		Priority:    4,
		ExternalRef: &extRef,
	}

	tr := &Tracker{
		teamIDs: []string{"team-1"},
		clients: map[string]*Client{
			"team-1": NewClient("key", "team-1").WithEndpoint(server.URL),
		},
		config: cfg,
	}

	forceIDs := map[string]bool{"local-1": true}
	result, err := tr.BatchPush(context.Background(), []*types.Issue{local}, forceIDs)
	if err != nil {
		t.Fatalf("BatchPush: %v", err)
	}
	if !updateCalled {
		t.Error("UpdateIssue was not called; forceIDs should bypass skip semantics")
	}
	if len(result.Updated) != 1 {
		t.Errorf("Updated = %v, want 1 item", result.Updated)
	}
}

// TestBatchPush_BatchCreateMappingByTitle verifies that batch-create results are
// matched by title rather than array index. Linear's API does not guarantee that
// issueBatchCreate returns results in the same order as the inputs, so index-based
// mapping is unsafe and can silently associate the wrong ExternalRef with each issue.
func TestBatchPush_BatchCreateMappingByTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "TeamStates"):
			json.NewEncoder(w).Encode(teamStatesResp("team-1", "state-open", "Backlog", "backlog"))
		case strings.Contains(req.Query, "issueBatchCreate"):
			// Return the two issues in REVERSE order to expose index-based mapping bugs.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueBatchCreate": map[string]interface{}{
						"success": true,
						"issues": []interface{}{
							map[string]interface{}{
								"id":         "uuid-beta",
								"identifier": "TEAM-2",
								"title":      "Beta Issue",
								"url":        "https://linear.app/team/issue/TEAM-2",
								"priority":   0,
								"state":      map[string]interface{}{"id": "state-open", "name": "Backlog", "type": "backlog"},
								"createdAt":  "2026-01-01T00:00:00Z",
								"updatedAt":  "2026-01-01T00:00:00Z",
							},
							map[string]interface{}{
								"id":         "uuid-alpha",
								"identifier": "TEAM-1",
								"title":      "Alpha Issue",
								"url":        "https://linear.app/team/issue/TEAM-1",
								"priority":   0,
								"state":      map[string]interface{}{"id": "state-open", "name": "Backlog", "type": "backlog"},
								"createdAt":  "2026-01-01T00:00:00Z",
								"updatedAt":  "2026-01-01T00:00:00Z",
							},
						},
					},
				},
			})
		}
	}))
	defer server.Close()

	cfg := DefaultMappingConfig()
	cfg.ExplicitStateMap = map[string]string{"backlog": "open"}

	// Two new issues — no ExternalRef, so they go through the batch-create path.
	alpha := &types.Issue{ID: "local-alpha", Title: "Alpha Issue", Status: types.StatusOpen, Priority: 4}
	beta := &types.Issue{ID: "local-beta", Title: "Beta Issue", Status: types.StatusOpen, Priority: 4}

	tr := &Tracker{
		teamIDs: []string{"team-1"},
		clients: map[string]*Client{
			"team-1": NewClient("key", "team-1").WithEndpoint(server.URL),
		},
		config: cfg,
	}

	result, err := tr.BatchPush(context.Background(), []*types.Issue{alpha, beta}, nil)
	if err != nil {
		t.Fatalf("BatchPush: %v", err)
	}
	if len(result.Created) != 2 {
		t.Fatalf("Created = %d items, want 2; errors: %v", len(result.Created), result.Errors)
	}

	// Build a LocalID → ExternalRef map from the results.
	got := make(map[string]string, len(result.Created))
	for _, item := range result.Created {
		got[item.LocalID] = item.ExternalRef
	}

	if got["local-alpha"] != "https://linear.app/team/issue/TEAM-1" {
		t.Errorf("local-alpha mapped to %q, want TEAM-1 URL", got["local-alpha"])
	}
	if got["local-beta"] != "https://linear.app/team/issue/TEAM-2" {
		t.Errorf("local-beta mapped to %q, want TEAM-2 URL", got["local-beta"])
	}
}

// TestBatchPush_PerTeamStateCache verifies that updates to issues belonging to a
// non-primary team use that team's workflow state cache rather than the primary
// team's. Using the wrong team's state IDs can cause API errors or apply an
// incorrect workflow state if the two teams have different state UUID sets.
func TestBatchPush_PerTeamStateCache(t *testing.T) {
	var capturedStateID string

	// team-2 server: owns the issue, has its own distinct state IDs.
	team2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "TeamStates"):
			json.NewEncoder(w).Encode(teamStatesResp("team-2", "t2-state-open", "Ready", "backlog"))
		case strings.Contains(req.Query, "IssueByIdentifier"):
			// Return the issue with DIFFERENT title so PushFieldsEqual = false and we proceed.
			json.NewEncoder(w).Encode(issueByIdentifierResp(
				"t2-uuid", "T2-1", "Old Title", "", 0,
				"t2-state-open", "Ready", "backlog",
			))
		case strings.Contains(req.Query, "issueUpdate"):
			// Capture the stateId sent in the update so we can verify it came from team-2's cache.
			input, _ := req.Variables["input"].(map[string]interface{})
			if sid, ok := input["stateId"].(string); ok {
				capturedStateID = sid
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueUpdate": map[string]interface{}{
						"success": true,
						"issue":   map[string]interface{}{"id": "t2-uuid", "url": "https://linear.app/team/issue/T2-1", "updatedAt": "2026-01-02T00:00:00Z"},
					},
				},
			})
		}
	}))
	defer team2Server.Close()

	// team-1 server: primary team, different state IDs, does not own this issue.
	team1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "TeamStates"):
			json.NewEncoder(w).Encode(teamStatesResp("team-1", "t1-state-open", "Backlog", "backlog"))
		case strings.Contains(req.Query, "IssueByIdentifier"):
			// Team-1 does not own this issue; return an empty result.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issues": map[string]interface{}{"nodes": []interface{}{}},
				},
			})
		}
	}))
	defer team1Server.Close()

	cfg := DefaultMappingConfig()
	cfg.ExplicitStateMap = map[string]string{"backlog": "open"}

	extRef := "https://linear.app/team/issue/T2-1"
	local := &types.Issue{
		ID:          "local-t2-1",
		Title:       "New Title", // differs from "Old Title" → not skipped
		Status:      types.StatusOpen,
		Priority:    4,
		ExternalRef: &extRef,
	}

	tr := &Tracker{
		teamIDs: []string{"team-1", "team-2"},
		clients: map[string]*Client{
			"team-1": NewClient("key", "team-1").WithEndpoint(team1Server.URL),
			"team-2": NewClient("key", "team-2").WithEndpoint(team2Server.URL),
		},
		config: cfg,
	}

	result, err := tr.BatchPush(context.Background(), []*types.Issue{local}, nil)
	if err != nil {
		t.Fatalf("BatchPush: %v", err)
	}
	if len(result.Updated) != 1 {
		t.Errorf("Updated = %v, want 1 item; errors: %v", result.Updated, result.Errors)
	}
	// The stateId in the update must come from team-2's cache, not team-1's.
	if capturedStateID != "t2-state-open" {
		t.Errorf("stateId sent in update = %q, want %q (team-2's state ID, not team-1's %q)",
			capturedStateID, "t2-state-open", "t1-state-open")
	}
}

// TestBatchPush_DuplicateTitlesFallbackToSingleCreate verifies that issues with
// duplicate titles within a batch are routed through single-create with idempotency
// markers instead of being sent through the batch mutation, where title-based
// result correlation would silently lose one of the duplicates.
func TestBatchPush_DuplicateTitlesFallbackToSingleCreate(t *testing.T) {
	var batchCreateCount int
	var singleCreateCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "TeamStates"):
			json.NewEncoder(w).Encode(teamStatesResp("team-1", "state-open", "Backlog", "backlog"))
		case strings.Contains(req.Query, "FindByDescription"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issues": map[string]interface{}{
						"nodes":    []interface{}{},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
		case strings.Contains(req.Query, "issueBatchCreate"):
			batchCreateCount++
			inputs := req.Variables["input"].([]interface{})
			var issues []interface{}
			for i, inp := range inputs {
				m := inp.(map[string]interface{})
				issues = append(issues, map[string]interface{}{
					"id": fmt.Sprintf("batch-uuid-%d", i), "identifier": fmt.Sprintf("TEAM-%d", i+10),
					"title": m["title"], "url": fmt.Sprintf("https://linear.app/team/issue/TEAM-%d", i+10),
					"priority": 0, "state": map[string]interface{}{"id": "state-open", "name": "Backlog", "type": "backlog"},
					"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
				})
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueBatchCreate": map[string]interface{}{"success": true, "issues": issues},
				},
			})
		case strings.Contains(req.Query, "issueCreate"):
			singleCreateCount++
			input := req.Variables["input"].(map[string]interface{})
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueCreate": map[string]interface{}{
						"success": true,
						"issue": map[string]interface{}{
							"id": fmt.Sprintf("single-uuid-%d", singleCreateCount), "identifier": fmt.Sprintf("TEAM-%d", singleCreateCount),
							"title": input["title"], "description": input["description"],
							"url":      fmt.Sprintf("https://linear.app/team/issue/TEAM-%d", singleCreateCount),
							"priority": 0, "state": map[string]interface{}{"id": "state-open", "name": "Backlog", "type": "backlog"},
							"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
						},
					},
				},
			})
		}
	}))
	defer server.Close()

	cfg := DefaultMappingConfig()
	cfg.ExplicitStateMap = map[string]string{"backlog": "open"}

	tr := &Tracker{
		teamIDs: []string{"team-1"},
		clients: map[string]*Client{
			"team-1": NewClient("key", "team-1").WithEndpoint(server.URL),
		},
		config: cfg,
	}

	// Two issues with the same title + one unique title.
	dupA := &types.Issue{ID: "dup-a", Title: "Duplicate Title", Status: types.StatusOpen, Priority: 4}
	dupB := &types.Issue{ID: "dup-b", Title: "Duplicate Title", Status: types.StatusOpen, Priority: 4}
	unique := &types.Issue{ID: "unique-1", Title: "Unique Title", Status: types.StatusOpen, Priority: 4}

	result, err := tr.BatchPush(context.Background(), []*types.Issue{dupA, dupB, unique}, nil)
	if err != nil {
		t.Fatalf("BatchPush: %v", err)
	}

	if singleCreateCount != 2 {
		t.Errorf("single creates = %d, want 2 (one per duplicate-title issue)", singleCreateCount)
	}
	if batchCreateCount != 1 {
		t.Errorf("batch creates = %d, want 1 (for the unique-title issue)", batchCreateCount)
	}
	if len(result.Created) != 3 {
		t.Errorf("Created = %d, want 3; errors: %v", len(result.Created), result.Errors)
	}

	createdIDs := make(map[string]bool)
	for _, item := range result.Created {
		createdIDs[item.LocalID] = true
	}
	for _, wantID := range []string{"dup-a", "dup-b", "unique-1"} {
		if !createdIDs[wantID] {
			t.Errorf("missing Created entry for %s", wantID)
		}
	}
}

// TestBatchPush_AmbiguousBatchFailureSearchesMarkers verifies that when a batch
// mutation returns an ambiguous error, the system searches for idempotency markers
// to find partially-created issues instead of blindly retrying the entire chunk.
func TestBatchPush_AmbiguousBatchFailureSearchesMarkers(t *testing.T) {
	var searchCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req GraphQLRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "TeamStates"):
			json.NewEncoder(w).Encode(teamStatesResp("team-1", "state-open", "Backlog", "backlog"))
		case strings.Contains(req.Query, "issueBatchCreate"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueBatchCreate": map[string]interface{}{
						"success": false,
						"issues":  []interface{}{},
					},
				},
			})
		case strings.Contains(req.Query, "FindByDescription"):
			searchCount++
			filter := req.Variables["filter"].(map[string]interface{})
			desc := filter["description"].(map[string]interface{})
			searchText := desc["contains"].(string)

			// Simulate: issue A was created by Linear before the failure, B was not.
			if strings.Contains(searchText, "bd-idempotency") {
				// We'll check which marker this is by looking at the search count.
				// First search (issue A) → found; second search (issue B) → not found.
				if searchCount == 1 {
					json.NewEncoder(w).Encode(map[string]interface{}{
						"data": map[string]interface{}{
							"issues": map[string]interface{}{
								"nodes": []interface{}{
									map[string]interface{}{
										"id": "recovered-uuid", "identifier": "TEAM-1",
										"title": "Issue A", "url": "https://linear.app/team/issue/TEAM-1",
										"priority": 0, "state": map[string]interface{}{"id": "state-open", "name": "Backlog", "type": "backlog"},
										"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
									},
								},
								"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
							},
						},
					})
				} else {
					json.NewEncoder(w).Encode(map[string]interface{}{
						"data": map[string]interface{}{
							"issues": map[string]interface{}{
								"nodes":    []interface{}{},
								"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
							},
						},
					})
				}
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issues": map[string]interface{}{
						"nodes":    []interface{}{},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
		}
	}))
	defer server.Close()

	cfg := DefaultMappingConfig()
	cfg.ExplicitStateMap = map[string]string{"backlog": "open"}

	tr := &Tracker{
		teamIDs: []string{"team-1"},
		clients: map[string]*Client{
			"team-1": NewClient("key", "team-1").WithEndpoint(server.URL),
		},
		config: cfg,
	}

	issueA := &types.Issue{ID: "local-a", Title: "Issue A", Status: types.StatusOpen, Priority: 4}
	issueB := &types.Issue{ID: "local-b", Title: "Issue B", Status: types.StatusOpen, Priority: 4}

	result, err := tr.BatchPush(context.Background(), []*types.Issue{issueA, issueB}, nil)
	// We expect a warning/error about unconfirmed issues, but no panic or full-chunk retry.
	if err != nil {
		t.Fatalf("BatchPush: %v", err)
	}

	if searchCount != 2 {
		t.Errorf("marker searches = %d, want 2 (one per issue in the failed batch)", searchCount)
	}

	// Issue A was found via marker search → should appear in Created.
	// Issue B was NOT found → should appear in Errors (not duplicated by a blind retry).
	if len(result.Created) != 1 {
		t.Errorf("Created = %d, want 1 (only the recovered issue)", len(result.Created))
	}
	if len(result.Created) == 1 && result.Created[0].LocalID != "local-a" {
		t.Errorf("Created[0].LocalID = %q, want local-a", result.Created[0].LocalID)
	}

	// Issue B should have an error (unconfirmed), not a silent retry.
	hasErrorForB := false
	for _, e := range result.Errors {
		if e.LocalID == "local-b" {
			hasErrorForB = true
		}
	}
	if !hasErrorForB {
		t.Error("expected error for local-b (unconfirmed after ambiguous batch failure)")
	}
}

func TestRegistered(t *testing.T) {
	factory := tracker.Get("linear")
	if factory == nil {
		t.Fatal("linear tracker not registered")
	}
	tr := factory()
	if tr.Name() != "linear" {
		t.Errorf("Name() = %q, want %q", tr.Name(), "linear")
	}
	if tr.DisplayName() != "Linear" {
		t.Errorf("DisplayName() = %q, want %q", tr.DisplayName(), "Linear")
	}
	if tr.ConfigPrefix() != "linear" {
		t.Errorf("ConfigPrefix() = %q, want %q", tr.ConfigPrefix(), "linear")
	}
}

func TestIsExternalRef(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://linear.app/team/issue/PROJ-123", true},
		{"https://linear.app/team/issue/PROJ-123/some-title", true},
		{"https://github.com/org/repo/issues/1", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tr.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

func TestExtractIdentifier(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://linear.app/team/issue/PROJ-123/some-title", "PROJ-123"},
		{"https://linear.app/team/issue/PROJ-123", "PROJ-123"},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestBuildExternalRef(t *testing.T) {
	tr := &Tracker{}
	ti := &tracker.TrackerIssue{
		URL:        "https://linear.app/team/issue/PROJ-123/some-title-slug",
		Identifier: "PROJ-123",
	}
	ref := tr.BuildExternalRef(ti)
	want := "https://linear.app/team/issue/PROJ-123"
	if ref != want {
		t.Errorf("BuildExternalRef() = %q, want %q", ref, want)
	}
}

func TestFieldMapperPriority(t *testing.T) {
	m := &linearFieldMapper{config: DefaultMappingConfig()}

	// Linear 1 (urgent) -> Beads 0 (critical)
	if got := m.PriorityToBeads(1); got != 0 {
		t.Errorf("PriorityToBeads(1) = %d, want 0", got)
	}
	// Beads 0 (critical) -> Linear 1 (urgent)
	if got := m.PriorityToTracker(0); got != 1 {
		t.Errorf("PriorityToTracker(0) = %v, want 1", got)
	}
}

func TestFieldMapperStatus(t *testing.T) {
	m := &linearFieldMapper{config: DefaultMappingConfig()}

	// Started -> in_progress
	state := &State{Type: "started", Name: "In Progress"}
	if got := m.StatusToBeads(state); got != types.StatusInProgress {
		t.Errorf("StatusToBeads(started) = %q, want %q", got, types.StatusInProgress)
	}

	// Completed -> closed
	state = &State{Type: "completed", Name: "Done"}
	if got := m.StatusToBeads(state); got != types.StatusClosed {
		t.Errorf("StatusToBeads(completed) = %q, want %q", got, types.StatusClosed)
	}
}

func TestTrackerMultiTeamValidate(t *testing.T) {
	// Empty tracker should fail validation.
	tr := &Tracker{}
	if err := tr.Validate(); err == nil {
		t.Error("expected Validate() to fail on uninitialized tracker")
	}

	// Tracker with clients should pass.
	tr.clients = map[string]*Client{
		"team-1": NewClient("key", "team-1"),
	}
	if err := tr.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestTrackerSetTeamIDs(t *testing.T) {
	tr := &Tracker{}
	ids := []string{"id-1", "id-2", "id-3"}
	tr.SetTeamIDs(ids)

	if len(tr.teamIDs) != 3 {
		t.Fatalf("expected 3 team IDs, got %d", len(tr.teamIDs))
	}
	for i, want := range ids {
		if tr.teamIDs[i] != want {
			t.Errorf("teamIDs[%d] = %q, want %q", i, tr.teamIDs[i], want)
		}
	}
}

func TestTrackerTeamIDsAccessor(t *testing.T) {
	tr := &Tracker{teamIDs: []string{"a", "b"}}
	got := tr.TeamIDs()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("TeamIDs() = %v, want [a b]", got)
	}
}

func TestTrackerPrimaryClient(t *testing.T) {
	tr := &Tracker{
		teamIDs: []string{"team-1", "team-2"},
		clients: map[string]*Client{
			"team-1": NewClient("key", "team-1"),
			"team-2": NewClient("key", "team-2"),
		},
	}

	client := tr.PrimaryClient()
	if client == nil {
		t.Fatal("PrimaryClient() returned nil")
	}
	if client.TeamID != "team-1" {
		t.Errorf("PrimaryClient().TeamID = %q, want %q", client.TeamID, "team-1")
	}

	// Empty tracker should return nil.
	empty := &Tracker{}
	if empty.PrimaryClient() != nil {
		t.Error("PrimaryClient() should return nil for empty tracker")
	}
}

func TestLinearToTrackerIssue(t *testing.T) {
	li := &Issue{
		ID:          "uuid-123",
		Identifier:  "TEAM-42",
		Title:       "Fix the bug",
		Description: "It's broken",
		URL:         "https://linear.app/team/issue/TEAM-42/fix-the-bug",
		Priority:    2,
		CreatedAt:   "2026-01-15T10:00:00Z",
		UpdatedAt:   "2026-01-16T14:30:00Z",
		Assignee:    &User{ID: "user-1", Name: "Alice", Email: "alice@example.com"},
		State:       &State{Type: "started", Name: "In Progress"},
		ProjectMilestone: &ProjectMilestone{
			ID:          "milestone-1",
			Name:        "M7: Team-Ready",
			Description: "Team-ready milestone",
			Progress:    60.61,
			TargetDate:  "2026-05-12",
		},
	}

	ti := linearToTrackerIssue(li)

	if ti.ID != "uuid-123" {
		t.Errorf("ID = %q, want %q", ti.ID, "uuid-123")
	}
	if ti.Identifier != "TEAM-42" {
		t.Errorf("Identifier = %q, want %q", ti.Identifier, "TEAM-42")
	}
	if ti.Assignee != "Alice" {
		t.Errorf("Assignee = %q, want %q", ti.Assignee, "Alice")
	}
	if ti.AssigneeEmail != "alice@example.com" {
		t.Errorf("AssigneeEmail = %q, want %q", ti.AssigneeEmail, "alice@example.com")
	}
	if ti.Raw != li {
		t.Error("Raw should reference original linear.Issue")
	}
	var meta struct {
		Linear struct {
			ProjectMilestone ProjectMilestone `json:"project_milestone"`
		} `json:"linear"`
	}
	raw, err := json.Marshal(ti.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta.Linear.ProjectMilestone.ID != "milestone-1" {
		t.Errorf("ProjectMilestone.ID = %q, want milestone-1", meta.Linear.ProjectMilestone.ID)
	}
}

// TestTrackerInitOAuthOnly verifies that Init() succeeds with only OAuth credentials
// and no API key. This is the CI worker use case.
func TestTrackerInitOAuthOnly(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	t.Setenv("LINEAR_OAUTH_CLIENT_ID", "test-client-id")
	t.Setenv("LINEAR_OAUTH_CLIENT_SECRET", "test-client-secret")
	// No LINEAR_API_KEY set — OAuth-only path.

	tr := &Tracker{}
	tr.SetTeamIDs([]string{"team-uuid-1"})
	// Inject the test token server so we don't hit production.
	oauthClient := NewOAuthClient(OAuthConfig{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		TokenURL:     tokenServer.URL,
	}, "team-uuid-1")
	tr.clients = map[string]*Client{"team-uuid-1": oauthClient}
	tr.config = DefaultMappingConfig()

	if err := tr.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (OAuth-only tracker should be valid)", err)
	}

	client := tr.PrimaryClient()
	if client == nil {
		t.Fatal("PrimaryClient() returned nil")
	}
	if client.AuthMode != AuthModeOAuth {
		t.Errorf("AuthMode = %v, want AuthModeOAuth", client.AuthMode)
	}
}

// TestTrackerInitNoAuthFails verifies that Init() returns a clear error when neither
// OAuth credentials nor an API key are present.
func TestTrackerInitNoAuthFails(t *testing.T) {
	// Ensure no credentials leak from environment or config.
	t.Setenv("LINEAR_OAUTH_CLIENT_ID", "")
	t.Setenv("LINEAR_OAUTH_CLIENT_SECRET", "")
	t.Setenv("LINEAR_API_KEY", "")

	tr := &Tracker{}
	tr.SetTeamIDs([]string{"team-uuid-1"})

	err := tr.Init(context.Background(), nil)
	if err == nil {
		t.Fatal("Init() should fail when no auth credentials are configured")
	}
	msg := err.Error()
	if !strings.Contains(msg, "LINEAR_OAUTH_CLIENT_ID") {
		t.Errorf("error should mention LINEAR_OAUTH_CLIENT_ID; got: %s", msg)
	}
	if !strings.Contains(msg, "LINEAR_API_KEY") {
		t.Errorf("error should mention LINEAR_API_KEY; got: %s", msg)
	}
}

// TestCreateIssueNoDoubleFormatDescription verifies that Tracker.CreateIssue passes
// issue.Description directly to Linear without calling BuildLinearDescription a
// second time. The sync engine's FormatDescription hook already builds the full
// description (merging AcceptanceCriteria/Design/Notes) before calling CreateIssue;
// calling BuildLinearDescription inside CreateIssue would duplicate those sections.
func TestCreateIssueNoDoubleFormatDescription(t *testing.T) {
	var capturedDescription string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(req.Query, "TeamStates") || strings.Contains(req.Query, "team(") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"team": map[string]interface{}{
						"id": "team-1",
						"states": map[string]interface{}{
							"nodes": []map[string]interface{}{
								{"id": "state-open", "name": "Todo", "type": "unstarted"},
							},
						},
					},
				},
			})
			return
		}

		if strings.Contains(req.Query, "issueCreate") {
			input, _ := req.Variables["input"].(map[string]interface{})
			capturedDescription, _ = input["description"].(string)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issueCreate": map[string]interface{}{
						"success": true,
						"issue": map[string]interface{}{
							"id":          "new-id",
							"identifier":  "TEAM-1",
							"title":       "Test",
							"description": capturedDescription,
							"url":         "https://linear.app/team/issue/TEAM-1",
							"state":       map[string]interface{}{"id": "state-open", "name": "Todo", "type": "unstarted"},
							"createdAt":   "2026-05-02T00:00:00Z",
							"updatedAt":   "2026-05-02T00:00:00Z",
						},
					},
				},
			})
			return
		}

		if strings.Contains(req.Query, "FindByDescription") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"issues": map[string]interface{}{
						"nodes":    []interface{}{},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
			return
		}

		t.Logf("unhandled query: %s", req.Query)
		http.Error(w, "unexpected query", http.StatusInternalServerError)
	}))
	defer server.Close()

	tr := &Tracker{
		teamIDs: []string{"team-1"},
		clients: map[string]*Client{
			"team-1": NewClient("key", "team-1").WithEndpoint(server.URL),
		},
		config: func() *MappingConfig {
			cfg := DefaultMappingConfig()
			cfg.ExplicitStateMap["todo"] = "open"
			return cfg
		}(),
	}

	createdAt := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)

	// Simulate what the sync engine does: Description is already the fully
	// formatted output of BuildLinearDescription (base + AC/Design/Notes merged in).
	formattedDesc := "base description\n\n## Acceptance Criteria\ncriteria here\n\n## Design\ndesign here"
	issue := &types.Issue{
		ID:                 "bead-1",
		Title:              "Test",
		Description:        formattedDesc, // pre-formatted by sync engine
		AcceptanceCriteria: "criteria here",
		Design:             "design here",
		Status:             types.StatusOpen,
		CreatedBy:          "dev@test.com",
		CreatedAt:          createdAt,
	}

	_, err := tr.CreateIssue(t.Context(), issue)
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// The description sent to Linear must be exactly the pre-formatted one.
	// If BuildLinearDescription were called inside CreateIssue, the AC and Design
	// sections would be appended a second time.
	if strings.Count(capturedDescription, "## Acceptance Criteria") != 1 {
		t.Errorf("description has %d '## Acceptance Criteria' sections, want 1 (double-format bug)\ndesc: %q",
			strings.Count(capturedDescription, "## Acceptance Criteria"), capturedDescription)
	}
	if strings.Count(capturedDescription, "## Design") != 1 {
		t.Errorf("description has %d '## Design' sections, want 1 (double-format bug)\ndesc: %q",
			strings.Count(capturedDescription, "## Design"), capturedDescription)
	}
	if !strings.Contains(capturedDescription, formattedDesc) {
		t.Errorf("description does not contain expected formatted content\ngot:  %q\nwant: %q",
			capturedDescription, formattedDesc)
	}
}
