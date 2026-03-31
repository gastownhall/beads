package ado

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestIsInitialState(t *testing.T) {
	tests := []struct {
		state string
		want  bool
	}{
		{"New", true},
		{"To Do", true},
		{"Proposed", true},
		{"Active", false},
		{"Closed", false},
		{"Resolved", false},
		{"Done", false},
		{"In Progress", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			if got := isInitialState(tt.state); got != tt.want {
				t.Errorf("isInitialState(%q) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestResolveTransitionPath(t *testing.T) {
	tests := []struct {
		name         string
		workItemType string
		fromState    string
		targetState  string
		wantPath     []string
	}{
		{
			name:         "same state — no transition",
			workItemType: "Bug",
			fromState:    "New",
			targetState:  "New",
			wantPath:     nil,
		},
		{
			name:         "Bug New to Closed",
			workItemType: "Bug",
			fromState:    "New",
			targetState:  "Closed",
			wantPath:     []string{"Active", "Resolved", "Closed"},
		},
		{
			name:         "Bug New to Active",
			workItemType: "Bug",
			fromState:    "New",
			targetState:  "Active",
			wantPath:     []string{"Active"},
		},
		{
			name:         "Bug New to Resolved",
			workItemType: "Bug",
			fromState:    "New",
			targetState:  "Resolved",
			wantPath:     []string{"Active", "Resolved"},
		},
		{
			name:         "Bug Active to Closed",
			workItemType: "Bug",
			fromState:    "Active",
			targetState:  "Closed",
			wantPath:     []string{"Resolved", "Closed"},
		},
		{
			name:         "Task New to Closed",
			workItemType: "Task",
			fromState:    "New",
			targetState:  "Closed",
			wantPath:     []string{"Active", "Closed"},
		},
		{
			name:         "Task New to Active",
			workItemType: "Task",
			fromState:    "New",
			targetState:  "Active",
			wantPath:     []string{"Active"},
		},
		{
			name:         "User Story New to Closed",
			workItemType: "User Story",
			fromState:    "New",
			targetState:  "Closed",
			wantPath:     []string{"Active", "Resolved", "Closed"},
		},
		{
			name:         "Epic New to Closed",
			workItemType: "Epic",
			fromState:    "New",
			targetState:  "Closed",
			wantPath:     []string{"Active", "Resolved", "Closed"},
		},
		{
			name:         "Scrum PBI New to Done",
			workItemType: "Product Backlog Item",
			fromState:    "New",
			targetState:  "Done",
			wantPath:     []string{"Approved", "Committed", "Done"},
		},
		{
			name:         "Scrum Task To Do to Done",
			workItemType: "Task",
			fromState:    "To Do",
			targetState:  "Done",
			wantPath:     []string{"In Progress", "Done"},
		},
		{
			name:         "CMMI Bug Proposed to Closed",
			workItemType: "Bug",
			fromState:    "Proposed",
			targetState:  "Closed",
			wantPath:     []string{"Active", "Resolved", "Closed"},
		},
		{
			name:         "unknown type — no path",
			workItemType: "CustomType",
			fromState:    "New",
			targetState:  "Closed",
			wantPath:     nil,
		},
		{
			name:         "unknown target — no path",
			workItemType: "Bug",
			fromState:    "New",
			targetState:  "CustomState",
			wantPath:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveTransitionPath(tt.workItemType, tt.fromState, tt.targetState)
			if tt.wantPath == nil {
				if got != nil {
					t.Errorf("resolveTransitionPath() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.wantPath) {
				t.Fatalf("resolveTransitionPath() = %v (len %d), want %v (len %d)",
					got, len(got), tt.wantPath, len(tt.wantPath))
			}
			for i := range got {
				if got[i] != tt.wantPath[i] {
					t.Errorf("path[%d] = %q, want %q", i, got[i], tt.wantPath[i])
				}
			}
		})
	}
}

func TestTransitionWorkItem_DirectSuccess(t *testing.T) {
	var updateCount int
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "expected PATCH", http.StatusMethodNotAllowed)
			return
		}
		updateCount++
		resp := `{"id":42,"rev":2,"fields":{"System.Title":"Test","System.State":"Closed","System.WorkItemType":"Task"}}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	wi, err := client.transitionWorkItem(context.Background(), 42, "Task", "New", "Closed")
	if err != nil {
		t.Fatalf("transitionWorkItem() error: %v", err)
	}
	if wi == nil {
		t.Fatal("transitionWorkItem() returned nil")
	}
	if updateCount != 1 {
		t.Errorf("expected 1 update call (direct), got %d", updateCount)
	}
	if got := wi.GetStringField(FieldState); got != "Closed" {
		t.Errorf("state = %q, want %q", got, "Closed")
	}
}

func TestTransitionWorkItem_WalkPath(t *testing.T) {
	var stateUpdates []string
	currentState := "New"

	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "expected PATCH", http.StatusMethodNotAllowed)
			return
		}

		body, _ := io.ReadAll(r.Body)
		var ops []PatchOperation
		_ = json.Unmarshal(body, &ops)

		var requestedState string
		for _, op := range ops {
			if op.Path == "/fields/"+FieldState {
				requestedState, _ = op.Value.(string)
			}
		}

		// Simulate ADO rejecting direct transition from New to Closed for Bug
		if currentState == "New" && requestedState == "Closed" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Invalid State 'Closed'"}`))
			return
		}

		stateUpdates = append(stateUpdates, requestedState)
		currentState = requestedState
		resp := fmt.Sprintf(`{"id":42,"rev":%d,"fields":{"System.Title":"Test","System.State":"%s","System.WorkItemType":"Bug"}}`,
			len(stateUpdates)+1, currentState)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	wi, err := client.transitionWorkItem(context.Background(), 42, "Bug", "New", "Closed")
	if err != nil {
		t.Fatalf("transitionWorkItem() error: %v", err)
	}
	if wi == nil {
		t.Fatal("transitionWorkItem() returned nil")
	}

	// Bug should walk: New → Active → Resolved → Closed
	expectedStates := []string{"Active", "Resolved", "Closed"}
	if len(stateUpdates) != len(expectedStates) {
		t.Fatalf("state updates = %v, want %v", stateUpdates, expectedStates)
	}
	for i, want := range expectedStates {
		if stateUpdates[i] != want {
			t.Errorf("state update[%d] = %q, want %q", i, stateUpdates[i], want)
		}
	}
	if got := wi.GetStringField(FieldState); got != "Closed" {
		t.Errorf("final state = %q, want %q", got, "Closed")
	}
}

func TestTransitionWorkItem_SameState(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no API call expected for same-state transition")
		w.WriteHeader(http.StatusOK)
	})

	wi, err := client.transitionWorkItem(context.Background(), 42, "Task", "New", "New")
	if err != nil {
		t.Fatalf("transitionWorkItem() error: %v", err)
	}
	if wi != nil {
		t.Errorf("transitionWorkItem() = %v, want nil for same state", wi)
	}
}

func TestTransitionWorkItem_NoPathFails(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// Direct transition fails.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Invalid State"}`))
	})

	_, err := client.transitionWorkItem(context.Background(), 42, "CustomType", "New", "CustomState")
	if err == nil {
		t.Fatal("expected error for unknown transition path")
	}
	if !strings.Contains(err.Error(), "no known transition path") {
		t.Errorf("error should mention no known path: %v", err)
	}
}

func TestCreateIssue_ClosedState_TransitionsAfterCreate(t *testing.T) {
	var createOps []PatchOperation
	var requestLog []string

	mux := http.NewServeMux()
	mux.HandleFunc("/testproject/_apis/wit/workitems/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			// Create request — should NOT include Closed state.
			requestLog = append(requestLog, "CREATE")
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &createOps)

			created := workItemJSON(300, 1, "Closed Bug", "New", "Bug")
			json.NewEncoder(w).Encode(created)
			return
		}

		if r.Method == http.MethodPatch {
			// Update requests for state transitions.
			body, _ := io.ReadAll(r.Body)
			var ops []PatchOperation
			_ = json.Unmarshal(body, &ops)

			var requestedState string
			for _, op := range ops {
				if op.Path == "/fields/"+FieldState {
					requestedState, _ = op.Value.(string)
				}
			}
			requestLog = append(requestLog, "UPDATE:"+requestedState)

			// Simulate successful transitions.
			updated := workItemJSON(300, len(requestLog), "Closed Bug", requestedState, "Bug")
			json.NewEncoder(w).Encode(updated)
			return
		}

		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	})

	tr, _ := newTestTracker(t, mux)
	issue := &types.Issue{
		Title:     "Closed Bug",
		Priority:  1,
		Status:    types.StatusClosed,
		IssueType: types.TypeBug,
	}

	result, err := tr.CreateIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}
	if result == nil {
		t.Fatal("CreateIssue() returned nil")
	}

	// Verify the create request did NOT include the Closed state.
	for _, op := range createOps {
		if op.Path == "/fields/"+FieldState {
			t.Errorf("create request should not include State field, got %v", op.Value)
		}
	}

	// Verify that a direct transition to "Closed" was attempted.
	if len(requestLog) < 2 {
		t.Fatalf("expected CREATE + at least 1 UPDATE, got %v", requestLog)
	}
	if requestLog[0] != "CREATE" {
		t.Errorf("first request should be CREATE, got %q", requestLog[0])
	}
	// Direct transition attempt: UPDATE:Closed
	if requestLog[1] != "UPDATE:Closed" {
		t.Errorf("second request should be UPDATE:Closed, got %q", requestLog[1])
	}

	// Final state should be Closed.
	if result.State != "Closed" {
		t.Errorf("result state = %q, want %q", result.State, "Closed")
	}
}

func TestCreateIssue_ClosedState_WalksPathOnReject(t *testing.T) {
	var requestLog []string
	currentState := "New"

	mux := http.NewServeMux()
	mux.HandleFunc("/testproject/_apis/wit/workitems/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			requestLog = append(requestLog, "CREATE")
			created := workItemJSON(301, 1, "Walk Path Bug", "New", "Bug")
			json.NewEncoder(w).Encode(created)
			return
		}

		if r.Method == http.MethodPatch {
			body, _ := io.ReadAll(r.Body)
			var ops []PatchOperation
			_ = json.Unmarshal(body, &ops)

			var requestedState string
			for _, op := range ops {
				if op.Path == "/fields/"+FieldState {
					requestedState, _ = op.Value.(string)
				}
			}
			requestLog = append(requestLog, "UPDATE:"+requestedState)

			// Reject direct New→Closed transition.
			if currentState == "New" && requestedState == "Closed" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"Invalid State 'Closed'"}`))
				return
			}

			currentState = requestedState
			updated := workItemJSON(301, len(requestLog), "Walk Path Bug", currentState, "Bug")
			json.NewEncoder(w).Encode(updated)
			return
		}

		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	})

	tr, _ := newTestTracker(t, mux)
	issue := &types.Issue{
		Title:     "Walk Path Bug",
		Priority:  1,
		Status:    types.StatusClosed,
		IssueType: types.TypeBug,
	}

	result, err := tr.CreateIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}
	if result == nil {
		t.Fatal("CreateIssue() returned nil")
	}

	// Expect: CREATE, UPDATE:Closed (rejected), UPDATE:Active, UPDATE:Resolved, UPDATE:Closed
	expected := []string{"CREATE", "UPDATE:Closed", "UPDATE:Active", "UPDATE:Resolved", "UPDATE:Closed"}
	if len(requestLog) != len(expected) {
		t.Fatalf("request log = %v, want %v", requestLog, expected)
	}
	for i, want := range expected {
		if requestLog[i] != want {
			t.Errorf("request[%d] = %q, want %q", i, requestLog[i], want)
		}
	}

	if result.State != "Closed" {
		t.Errorf("result state = %q, want %q", result.State, "Closed")
	}
}

func TestCreateIssue_OpenState_NoTransition(t *testing.T) {
	var requestLog []string

	mux := http.NewServeMux()
	mux.HandleFunc("/testproject/_apis/wit/workitems/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			requestLog = append(requestLog, "CREATE")
			created := workItemJSON(302, 1, "Open Task", "New", "Task")
			json.NewEncoder(w).Encode(created)
			return
		}

		// No PATCH calls should happen for initial-state creation.
		requestLog = append(requestLog, "UNEXPECTED:"+r.Method)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	})

	tr, _ := newTestTracker(t, mux)
	issue := &types.Issue{
		Title:     "Open Task",
		Priority:  2,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
	}

	result, err := tr.CreateIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	// "New" is an initial state, so it should be included in creation and
	// no transition should occur.
	if len(requestLog) != 1 {
		t.Errorf("request log = %v, want just [CREATE]", requestLog)
	}
	if result.State != "New" {
		t.Errorf("result state = %q, want %q", result.State, "New")
	}
}

func TestCreateIssue_ActiveState_TransitionsAfterCreate(t *testing.T) {
	var requestLog []string

	mux := http.NewServeMux()
	mux.HandleFunc("/testproject/_apis/wit/workitems/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			requestLog = append(requestLog, "CREATE")
			created := workItemJSON(303, 1, "Active Task", "New", "Task")
			json.NewEncoder(w).Encode(created)
			return
		}

		if r.Method == http.MethodPatch {
			body, _ := io.ReadAll(r.Body)
			var ops []PatchOperation
			_ = json.Unmarshal(body, &ops)
			var requestedState string
			for _, op := range ops {
				if op.Path == "/fields/"+FieldState {
					requestedState, _ = op.Value.(string)
				}
			}
			requestLog = append(requestLog, "UPDATE:"+requestedState)
			updated := workItemJSON(303, 2, "Active Task", requestedState, "Task")
			json.NewEncoder(w).Encode(updated)
			return
		}

		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	})

	tr, _ := newTestTracker(t, mux)
	issue := &types.Issue{
		Title:     "Active Task",
		Priority:  2,
		Status:    types.StatusInProgress,
		IssueType: types.TypeTask,
	}

	result, err := tr.CreateIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	// "Active" is not an initial state, so: CREATE + UPDATE:Active
	if len(requestLog) != 2 {
		t.Fatalf("request log = %v, want [CREATE UPDATE:Active]", requestLog)
	}
	if requestLog[1] != "UPDATE:Active" {
		t.Errorf("request[1] = %q, want %q", requestLog[1], "UPDATE:Active")
	}
	if result.State != "Active" {
		t.Errorf("result state = %q, want %q", result.State, "Active")
	}
}

func TestCreateIssue_DeferredState_Removed(t *testing.T) {
	var createOps []PatchOperation
	var requestLog []string

	mux := http.NewServeMux()
	mux.HandleFunc("/testproject/_apis/wit/workitems/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			requestLog = append(requestLog, "CREATE")
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &createOps)
			created := workItemJSON(304, 1, "Deferred Task", "New", "Task")
			json.NewEncoder(w).Encode(created)
			return
		}

		if r.Method == http.MethodPatch {
			body, _ := io.ReadAll(r.Body)
			var ops []PatchOperation
			_ = json.Unmarshal(body, &ops)
			var requestedState string
			for _, op := range ops {
				if op.Path == "/fields/"+FieldState {
					requestedState, _ = op.Value.(string)
				}
			}
			requestLog = append(requestLog, "UPDATE:"+requestedState)
			updated := workItemJSON(304, 2, "Deferred Task", requestedState, "Task")
			json.NewEncoder(w).Encode(updated)
			return
		}

		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	})

	tr, _ := newTestTracker(t, mux)
	issue := &types.Issue{
		Title:     "Deferred Task",
		Priority:  3,
		Status:    types.StatusDeferred,
		IssueType: types.TypeTask,
	}

	result, err := tr.CreateIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	// "Removed" is not an initial state, so should be stripped from create
	// and set via transition.
	for _, op := range createOps {
		if op.Path == "/fields/"+FieldState {
			t.Errorf("create should not include State, got %v", op.Value)
		}
	}

	// Direct transition to "Removed" should succeed.
	if len(requestLog) < 2 {
		t.Fatalf("request log = %v, want CREATE + UPDATE:Removed", requestLog)
	}
	if requestLog[1] != "UPDATE:Removed" {
		t.Errorf("request[1] = %q, want %q", requestLog[1], "UPDATE:Removed")
	}
	if result.State != "Removed" {
		t.Errorf("result state = %q, want %q", result.State, "Removed")
	}
}
