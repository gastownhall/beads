package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
}
