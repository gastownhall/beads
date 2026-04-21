//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// mockLinearServer is a stateful mock that only stores what the Linear client
// actually sends — no fabricated fields. This keeps the round-trip test honest.
type mockLinearServer struct {
	mu       sync.Mutex
	issues   map[string]*linear.Issue // keyed by Linear UUID
	nextSeq  int
	teamID   string
	teamKey  string // e.g. "MOCK"
	states   []linear.State
	stateMap map[string]linear.State // state type → State
}

func newMockLinearServer(teamID, teamKey string) *mockLinearServer {
	states := []linear.State{
		{ID: "state-backlog", Name: "Backlog", Type: "backlog"},
		{ID: "state-unstarted", Name: "Todo", Type: "unstarted"},
		{ID: "state-started", Name: "In Progress", Type: "started"},
		{ID: "state-completed", Name: "Done", Type: "completed"},
		{ID: "state-canceled", Name: "Canceled", Type: "canceled"},
	}
	stateMap := make(map[string]linear.State, len(states))
	for _, s := range states {
		stateMap[s.Type] = s
	}
	return &mockLinearServer{
		issues:   make(map[string]*linear.Issue),
		teamID:   teamID,
		teamKey:  teamKey,
		states:   states,
		stateMap: stateMap,
	}
}

func (m *mockLinearServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req linear.GraphQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var data interface{}
	var err error

	switch {
	case strings.Contains(req.Query, "issueCreate"):
		data, err = m.handleCreate(req)
	case strings.Contains(req.Query, "issueUpdate"):
		data, err = m.handleUpdate(req)
	case strings.Contains(req.Query, "TeamStates") || strings.Contains(req.Query, "team(id:") || (strings.Contains(req.Query, "team(") && strings.Contains(req.Query, "states")):
		data = m.handleTeamStates()
	case strings.Contains(req.Query, "issues"):
		data, err = m.handleFetchIssues(req)
	default:
		http.Error(w, fmt.Sprintf("unhandled query: %s", req.Query[:min(80, len(req.Query))]), http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respBytes, _ := json.Marshal(data)
	resp := map[string]json.RawMessage{"data": respBytes}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (m *mockLinearServer) handleCreate(req linear.GraphQLRequest) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	vars := req.Variables
	inputRaw, ok := vars["input"]
	if !ok {
		return nil, fmt.Errorf("missing input")
	}
	input, ok := inputRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("input is not a map")
	}

	m.nextSeq++
	id := fmt.Sprintf("uuid-%d", m.nextSeq)
	identifier := fmt.Sprintf("%s-%d", m.teamKey, m.nextSeq)
	now := time.Now().UTC().Format(time.RFC3339)

	issue := &linear.Issue{
		ID:          id,
		Identifier:  identifier,
		Title:       strVal(input, "title"),
		Description: strVal(input, "description"),
		URL:         fmt.Sprintf("https://linear.app/mock/issue/%s", identifier),
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if p, ok := input["priority"]; ok {
		if pf, ok := p.(float64); ok {
			issue.Priority = int(pf)
		}
	}

	if stateID := strVal(input, "stateId"); stateID != "" {
		for _, s := range m.states {
			if s.ID == stateID {
				issue.State = &linear.State{ID: s.ID, Name: s.Name, Type: s.Type}
				break
			}
		}
	}

	m.issues[id] = issue

	return map[string]interface{}{
		"issueCreate": map[string]interface{}{
			"success": true,
			"issue":   issue,
		},
	}, nil
}

func (m *mockLinearServer) handleUpdate(req linear.GraphQLRequest) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	vars := req.Variables
	id := ""
	if v, ok := vars["id"]; ok {
		id, _ = v.(string)
	}

	issue, exists := m.issues[id]
	if !exists {
		return nil, fmt.Errorf("issue %s not found", id)
	}

	inputRaw, ok := vars["input"]
	if !ok {
		return nil, fmt.Errorf("missing input")
	}
	input, ok := inputRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("input is not a map")
	}

	if v := strVal(input, "title"); v != "" {
		issue.Title = v
	}
	if v := strVal(input, "description"); v != "" {
		issue.Description = v
	}
	if p, ok := input["priority"]; ok {
		if pf, ok := p.(float64); ok {
			issue.Priority = int(pf)
		}
	}
	if stateID := strVal(input, "stateId"); stateID != "" {
		for _, s := range m.states {
			if s.ID == stateID {
				issue.State = &linear.State{ID: s.ID, Name: s.Name, Type: s.Type}
				break
			}
		}
	}
	issue.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	return map[string]interface{}{
		"issueUpdate": map[string]interface{}{
			"success": true,
			"issue":   issue,
		},
	}, nil
}

func (m *mockLinearServer) handleTeamStates() interface{} {
	return map[string]interface{}{
		"team": map[string]interface{}{
			"id": m.teamID,
			"states": map[string]interface{}{
				"nodes": m.states,
			},
		},
	}
}

func (m *mockLinearServer) handleFetchIssues(req linear.GraphQLRequest) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for identifier filter (FetchIssueByIdentifier)
	vars := req.Variables
	if filterRaw, ok := vars["filter"]; ok {
		if filter, ok := filterRaw.(map[string]interface{}); ok {
			if idFilter, ok := filter["identifier"]; ok {
				if idMap, ok := idFilter.(map[string]interface{}); ok {
					if eqVal, ok := idMap["eq"]; ok {
						identifier, _ := eqVal.(string)
						for _, issue := range m.issues {
							if issue.Identifier == identifier {
								return map[string]interface{}{
									"issues": map[string]interface{}{
										"nodes":    []interface{}{issue},
										"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
									},
								}, nil
							}
						}
						return map[string]interface{}{
							"issues": map[string]interface{}{
								"nodes":    []interface{}{},
								"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
							},
						}, nil
					}
				}
			}
		}
	}

	// Return all issues
	nodes := make([]*linear.Issue, 0, len(m.issues))
	for _, issue := range m.issues {
		nodes = append(nodes, issue)
	}

	return map[string]interface{}{
		"issues": map[string]interface{}{
			"nodes":    nodes,
			"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
		},
	}, nil
}

func (m *mockLinearServer) issueCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.issues)
}

func strVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// TestLinearRoundTripCoreFields tests push→pull fidelity for fields that the
// Linear integration currently supports: title, description, priority, status,
// and external_ref. See upstream #3187.
func TestLinearRoundTripCoreFields(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	teamID := "test-team-uuid"

	// --- 1. Setup source DB ---
	sourceStore, cleanup := setupTestDB(t)
	defer cleanup()

	// Configure Linear settings in source store
	for k, v := range map[string]string{
		"linear.api_key": "test-api-key",
		"linear.team_id": teamID,
		"issue_prefix":   "bd",
	} {
		if err := sourceStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig(%s): %v", k, err)
		}
	}

	// Start mock server
	mock := newMockLinearServer(teamID, "MOCK")
	server := httptest.NewServer(mock)
	defer server.Close()

	if err := sourceStore.SetConfig(ctx, "linear.api_endpoint", server.URL); err != nil {
		t.Fatalf("SetConfig(endpoint): %v", err)
	}

	// --- 2. Seed source DB with varied issues ---
	type seedIssue struct {
		title       string
		description string
		priority    int
		status      types.Status
	}
	seeds := []seedIssue{
		{"Critical security fix", "Fix the auth bypass vulnerability", 0, types.StatusOpen},
		{"Add search feature", "Implement full-text search for issues", 1, types.StatusInProgress},
		{"Update dependencies", "Routine dep update for Q2", 3, types.StatusClosed},
	}

	sourceIssueIDs := make([]string, 0, len(seeds))
	for i, s := range seeds {
		issue := &types.Issue{
			ID:          fmt.Sprintf("bd-rt-%d", i),
			Title:       s.title,
			Description: s.description,
			Priority:    s.priority,
			Status:      s.status,
			IssueType:   types.TypeTask,
		}
		if s.status == types.StatusClosed {
			now := time.Now()
			issue.ClosedAt = &now
		}
		if err := sourceStore.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
		sourceIssueIDs = append(sourceIssueIDs, issue.ID)
	}

	// --- 3. Push to mock Linear ---
	lt := &linear.Tracker{}
	lt.SetTeamIDs([]string{teamID})
	if err := lt.Init(ctx, sourceStore); err != nil {
		t.Fatalf("Tracker.Init: %v", err)
	}

	pushEngine := tracker.NewEngine(lt, sourceStore, "test-actor")
	pushEngine.PushHooks = buildLinearPushHooksForTest(ctx, lt)

	pushResult, err := pushEngine.Sync(ctx, tracker.SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Push sync failed: %v", err)
	}
	if pushResult.Stats.Created != len(seeds) {
		t.Fatalf("expected %d pushed, got created=%d", len(seeds), pushResult.Stats.Created)
	}

	// Verify external refs were written
	for _, id := range sourceIssueIDs {
		issue, err := sourceStore.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s) after push: %v", id, err)
		}
		if issue.ExternalRef == nil || *issue.ExternalRef == "" {
			t.Errorf("issue %s: expected external_ref after push, got nil", id)
		}
	}

	// Verify mock server received all issues
	if got := mock.issueCount(); got != len(seeds) {
		t.Fatalf("mock server has %d issues, want %d", got, len(seeds))
	}

	// --- 4. Setup target DB (fresh) ---
	targetStore, cleanup2 := setupTestDB(t)
	defer cleanup2()

	for k, v := range map[string]string{
		"linear.api_key":      "test-api-key",
		"linear.team_id":      teamID,
		"linear.api_endpoint": server.URL,
		"issue_prefix":        "bd",
	} {
		if err := targetStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig(%s) target: %v", k, err)
		}
	}

	// --- 5. Pull from mock Linear into fresh DB ---
	lt2 := &linear.Tracker{}
	lt2.SetTeamIDs([]string{teamID})
	if err := lt2.Init(ctx, targetStore); err != nil {
		t.Fatalf("Tracker.Init (target): %v", err)
	}

	pullEngine := tracker.NewEngine(lt2, targetStore, "test-actor")
	pullEngine.PullHooks = buildLinearPullHooksForTest(ctx, targetStore)

	pullResult, err := pullEngine.Sync(ctx, tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Pull sync failed: %v", err)
	}
	if pullResult.Stats.Created != len(seeds) {
		t.Fatalf("expected %d pulled/created, got created=%d", len(seeds), pullResult.Stats.Created)
	}

	// --- 6. Assert fidelity ---
	// Build a map of pulled issues keyed by external_ref
	pulledIssues, err := targetStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues on target: %v", err)
	}
	if len(pulledIssues) != len(seeds) {
		t.Fatalf("target has %d issues, want %d", len(pulledIssues), len(seeds))
	}

	pulledByRef := make(map[string]*types.Issue)
	for _, issue := range pulledIssues {
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			pulledByRef[*issue.ExternalRef] = issue
		}
	}

	// For each source issue, find the corresponding pulled issue and compare
	for i, id := range sourceIssueIDs {
		source, err := sourceStore.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s) from source: %v", id, err)
		}
		if source.ExternalRef == nil {
			t.Fatalf("source issue %s has no external_ref", id)
		}

		pulled, ok := pulledByRef[*source.ExternalRef]
		if !ok {
			t.Fatalf("seed[%d] %s: no pulled issue with external_ref %s", i, id, *source.ExternalRef)
		}

		t.Run(fmt.Sprintf("seed_%d_%s", i, source.Title), func(t *testing.T) {
			// Title
			if pulled.Title != source.Title {
				t.Errorf("title: got %q, want %q", pulled.Title, source.Title)
			}

			// Priority round-trip (beads→linear→beads)
			if pulled.Priority != source.Priority {
				t.Errorf("priority: got %d, want %d", pulled.Priority, source.Priority)
			}

			// Status round-trip
			// Note: StatusOpen→unstarted→open, StatusInProgress→started→in_progress,
			// StatusClosed→completed→closed
			if pulled.Status != source.Status {
				t.Errorf("status: got %q, want %q", pulled.Status, source.Status)
			}

			// External ref preserved
			if pulled.ExternalRef == nil || *pulled.ExternalRef != *source.ExternalRef {
				pulledRef := "<nil>"
				if pulled.ExternalRef != nil {
					pulledRef = *pulled.ExternalRef
				}
				t.Errorf("external_ref: got %q, want %q", pulledRef, *source.ExternalRef)
			}
		})
	}
}

// TestLinearRoundTripRelationships is a spec test documenting that parent-child
// hierarchy, blocking dependencies, and issue type do not survive a push→pull
// round-trip because the Linear push path does not yet send these fields.
// When those features are implemented, remove the Skip and this test becomes
// a regression gate. See upstream #3187.
func TestLinearRoundTripRelationships(t *testing.T) {
	t.Skip("push does not yet support parent/relations/type — see upstream #3187")

	// When enabled, this test should:
	// 1. Create an epic + child tasks + blocking dep
	// 2. Push to mock Linear
	// 3. Verify mock received parent and relation fields
	// 4. Pull into fresh DB
	// 5. Assert:
	//    - Epic exists with IssueType=epic
	//    - Child tasks have parent-child dep to epic
	//    - Blocking dep preserved
	//    - Issue types preserved via label round-trip
}

// buildLinearPushHooksForTest mirrors buildLinearPushHooks from linear.go
// but works with an explicit store instead of the global.
func buildLinearPushHooksForTest(ctx context.Context, lt *linear.Tracker) *tracker.PushHooks {
	return &tracker.PushHooks{
		FormatDescription: func(issue *types.Issue) string {
			return linear.BuildLinearDescription(issue)
		},
		ContentEqual: func(local *types.Issue, remote *tracker.TrackerIssue) bool {
			localComparable := linear.NormalizeIssueForLinearHash(local)
			remoteConv := lt.FieldMapper().IssueToBeads(remote)
			if remoteConv == nil || remoteConv.Issue == nil {
				return false
			}
			return localComparable.ComputeContentHash() == remoteConv.Issue.ComputeContentHash()
		},
		BuildStateCache: func(ctx context.Context) (interface{}, error) {
			return linear.BuildStateCacheFromTracker(ctx, lt)
		},
		ResolveState: func(cache interface{}, status types.Status) (string, bool) {
			sc, ok := cache.(*linear.StateCache)
			if !ok || sc == nil {
				return "", false
			}
			id := sc.FindStateForBeadsStatus(status)
			return id, id != ""
		},
	}
}

// buildLinearPullHooksForTest mirrors buildLinearPullHooks from linear.go
// but works with an explicit store.
func buildLinearPullHooksForTest(ctx context.Context, store interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
}) *tracker.PullHooks {
	hooks := &tracker.PullHooks{}

	existingIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	usedIDs := make(map[string]bool)
	if err == nil {
		for _, issue := range existingIssues {
			if issue.ID != "" {
				usedIDs[issue.ID] = true
			}
		}
	}

	hooks.GenerateID = func(_ context.Context, issue *types.Issue) error {
		ids := []*types.Issue{issue}
		idOpts := linear.IDGenerationOptions{
			BaseLength: 6,
			MaxLength:  8,
			UsedIDs:    usedIDs,
		}
		if err := linear.GenerateIssueIDs(ids, "bd", "linear-import", idOpts); err != nil {
			return err
		}
		usedIDs[issue.ID] = true
		return nil
	}

	return hooks
}
