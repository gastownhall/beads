// Shared mock Linear GraphQL server for cmd/bd front-door tests.
//
// The `cgo` build tag is deliberately looser than any single consumer: this
// file is the union superset of `cgo && integration` (linear_roundtrip_test.go)
// and `cgo && unix` (the linear_proxy_* parity tests), which is what lets one
// fixture serve both tag sets. Keep it that way — only add helpers that compile
// under plain `cgo`. A unix-only or integration-only helper pulled in here
// breaks windows-cgo builds with no local signal; put it in the consumer file
// instead.

//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/linear"
)

// mockLinearServer is the stateful GraphQL double used by Linear command
// front-door tests. It stores only fields the Linear client sends.
type mockLinearServer struct {
	mu       sync.Mutex
	issues   map[string]*linear.Issue
	nextSeq  int
	teamID   string
	teamKey  string
	states   []linear.State
	stateMap map[string]linear.State
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
	case strings.Contains(req.Query, "issueBatchCreate"):
		data, err = m.handleBatchCreate(req)
	case strings.Contains(req.Query, "issueCreate"):
		data, err = m.handleCreate(req)
	case strings.Contains(req.Query, "issueUpdate"):
		data, err = m.handleUpdate(req)
	case strings.Contains(req.Query, "TeamLabels"):
		data = m.handleTeamLabels()
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]json.RawMessage{"data": respBytes})
}

func (m *mockLinearServer) handleCreate(req linear.GraphQLRequest) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	input, ok := req.Variables["input"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing input")
	}
	issue := m.createLocked(input)
	return map[string]interface{}{"issueCreate": map[string]interface{}{"success": true, "issue": issue}}, nil
}

func (m *mockLinearServer) handleBatchCreate(req linear.GraphQLRequest) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	input, ok := req.Variables["input"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing input")
	}
	rawIssues, ok := input["issues"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("missing batch issues")
	}
	issues := make([]*linear.Issue, 0, len(rawIssues))
	for _, raw := range rawIssues {
		issueInput, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid batch issue")
		}
		issues = append(issues, m.createLocked(issueInput))
	}
	return map[string]interface{}{"issueBatchCreate": map[string]interface{}{"success": true, "issues": issues}}, nil
}

func (m *mockLinearServer) createLocked(input map[string]interface{}) *linear.Issue {
	m.nextSeq++
	id := fmt.Sprintf("uuid-%d", m.nextSeq)
	identifier := fmt.Sprintf("%s-%d", m.teamKey, m.nextSeq)
	now := time.Now().UTC().Format(time.RFC3339)
	issue := &linear.Issue{
		ID: id, Identifier: identifier, Title: strVal(input, "title"), Description: strVal(input, "description"),
		URL: fmt.Sprintf("https://linear.app/mock/issue/%s", identifier), CreatedAt: now, UpdatedAt: now,
	}
	if p, ok := input["priority"].(float64); ok {
		issue.Priority = int(p)
	}
	if state, ok := m.stateForID(strVal(input, "stateId")); ok {
		issue.State = &state
	}
	m.issues[id] = issue
	return issue
}

func (m *mockLinearServer) handleUpdate(req linear.GraphQLRequest) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, _ := req.Variables["id"].(string)
	issue, ok := m.issues[id]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	input, ok := req.Variables["input"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing input")
	}
	if v := strVal(input, "title"); v != "" {
		issue.Title = v
	}
	if v := strVal(input, "description"); v != "" {
		issue.Description = v
	}
	if p, ok := input["priority"].(float64); ok {
		issue.Priority = int(p)
	}
	if state, ok := m.stateForID(strVal(input, "stateId")); ok {
		issue.State = &state
	}
	issue.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return map[string]interface{}{"issueUpdate": map[string]interface{}{"success": true, "issue": issue}}, nil
}

func (m *mockLinearServer) stateForID(id string) (linear.State, bool) {
	for _, state := range m.states {
		if state.ID == id {
			return state, true
		}
	}
	return linear.State{}, false
}

func (m *mockLinearServer) handleTeamStates() interface{} {
	return map[string]interface{}{"team": map[string]interface{}{"id": m.teamID, "states": map[string]interface{}{"nodes": m.states}}}
}

func (m *mockLinearServer) handleTeamLabels() interface{} {
	return map[string]interface{}{"team": map[string]interface{}{"labels": map[string]interface{}{
		"nodes":    []linear.Label{{ID: "label-remote", Name: "remote-label"}},
		"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
	}}}
}

func (m *mockLinearServer) handleFetchIssues(req linear.GraphQLRequest) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var nodes []*linear.Issue
	for _, issue := range m.issues {
		if mockMatchesIssueFilter(issue, req.Variables["filter"]) {
			nodes = append(nodes, issue)
		}
	}
	return map[string]interface{}{"issues": map[string]interface{}{"nodes": nodes, "pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""}}}, nil
}

func mockMatchesIssueFilter(issue *linear.Issue, raw any) bool {
	filter, ok := raw.(map[string]interface{})
	if !ok {
		return true
	}
	if number, ok := mockNestedFloat(filter, "number", "eq"); ok {
		return strings.HasSuffix(issue.Identifier, fmt.Sprintf("-%d", int(number)))
	}
	if identifier, ok := mockNestedString(filter, "identifier", "eq"); ok {
		return issue.Identifier == identifier
	}
	if description, ok := mockNestedString(filter, "description", "contains"); ok {
		return strings.Contains(issue.Description, description)
	}
	return true
}

func mockNestedFloat(m map[string]interface{}, path ...string) (float64, bool) {
	var current any = m
	for _, key := range path {
		child, ok := current.(map[string]interface{})[key]
		if !ok {
			return 0, false
		}
		current = child
	}
	value, ok := current.(float64)
	return value, ok
}

func mockNestedString(m map[string]interface{}, path ...string) (string, bool) {
	var current any = m
	for _, key := range path {
		child, ok := current.(map[string]interface{})[key]
		if !ok {
			return "", false
		}
		current = child
	}
	value, ok := current.(string)
	return value, ok
}

func (m *mockLinearServer) issueCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.issues)
}

// snapshotIssues returns a value copy of the remote fixture state so command
// tests can prove dry-run never reaches a GraphQL mutation.
func (m *mockLinearServer) snapshotIssues(t testing.TB) map[string]linear.Issue {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	encoded, err := json.Marshal(m.issues)
	if err != nil {
		t.Fatalf("marshal mock Linear issues: %v", err)
	}
	var snapshot map[string]linear.Issue
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatalf("unmarshal mock Linear issues: %v", err)
	}
	return snapshot
}

func strVal(m map[string]interface{}, key string) string {
	value, _ := m[key].(string)
	return value
}
