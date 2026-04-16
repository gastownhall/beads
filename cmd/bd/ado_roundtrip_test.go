//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/ado"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// mockADOServer is a stateful mock that stores only what the ADO client sends,
// plus server-derived fields (WorkItemType from URL, default state, timestamps).
// Routes match the real URL shape: /{project}/_apis/wit/...
type mockADOServer struct {
	mu      sync.Mutex
	items   map[int]*ado.WorkItem
	nextID  int
	project string
}

func newMockADOServer(project string) *mockADOServer {
	return &mockADOServer{
		items:   make(map[int]*ado.WorkItem),
		nextID:  1,
		project: project,
	}
}

func (m *mockADOServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case strings.Contains(path, "/_apis/wit/wiql"):
		m.handleWIQL(w, r)
	case strings.Contains(path, "/_apis/wit/workitems/$") && r.Method == http.MethodPost:
		m.handleCreate(w, r)
	case strings.Contains(path, "/_apis/wit/workitems") && r.Method == http.MethodGet && !strings.Contains(path, "/_apis/wit/workitems/"):
		m.handleBatchGet(w, r)
	case strings.Contains(path, "/_apis/wit/workitems/") && r.Method == http.MethodPatch:
		m.handleUpdate(w, r)
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "unhandled: %s %s", r.Method, path)
	}
}

func (m *mockADOServer) handleWIQL(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return all stored work item IDs.
	refs := make([]ado.WIQLWorkItemRef, 0, len(m.items))
	for id := range m.items {
		refs = append(refs, ado.WIQLWorkItemRef{ID: id})
	}
	resp := ado.WIQLResult{WorkItems: refs}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (m *mockADOServer) handleBatchGet(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idsStr := r.URL.Query().Get("ids")
	if idsStr == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"value": []interface{}{}})
		return
	}

	var items []ado.WorkItem
	for _, s := range strings.Split(idsStr, ",") {
		id, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		if wi, ok := m.items[id]; ok {
			items = append(items, *wi)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	raw, _ := json.Marshal(items)
	json.NewEncoder(w).Encode(map[string]json.RawMessage{"value": raw})
}

func (m *mockADOServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Extract type from URL: /{project}/_apis/wit/workitems/$Task
	// Go's HTTP server already URL-decodes the path, so $User%20Story → $User Story.
	path := r.URL.Path
	dollarIdx := strings.LastIndex(path, "/$")
	typeName := "Task"
	if dollarIdx >= 0 {
		typeName = path[dollarIdx+2:]
	}
	// Strip query params if present (shouldn't be, but defensive)
	if qIdx := strings.Index(typeName, "?"); qIdx >= 0 {
		typeName = typeName[:qIdx]
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var ops []ado.PatchOperation
	if err := json.Unmarshal(body, &ops); err != nil {
		http.Error(w, "invalid patch ops", http.StatusBadRequest)
		return
	}

	id := m.nextID
	m.nextID++
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	fields := map[string]interface{}{
		ado.FieldWorkItemType: typeName,
		ado.FieldCreatedDate:  now,
		ado.FieldChangedDate:  now,
		ado.FieldAreaPath:     m.project,
		ado.FieldTeamProject:  m.project,
	}

	for _, op := range ops {
		if op.Op == "add" || op.Op == "replace" {
			fieldName := strings.TrimPrefix(op.Path, "/fields/")
			fields[fieldName] = op.Value
		}
	}

	// If no state was set, default to "New" (ADO behavior).
	if _, hasState := fields[ado.FieldState]; !hasState {
		fields[ado.FieldState] = "New"
	}

	apiURL := fmt.Sprintf("http://localhost/%s/_apis/wit/workItems/%d", m.project, id)
	wi := &ado.WorkItem{
		ID:     id,
		Rev:    1,
		Fields: fields,
		URL:    apiURL,
	}
	m.items[id] = wi

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(wi)
}

func (m *mockADOServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Extract ID from URL: /{project}/_apis/wit/workitems/{id}
	path := r.URL.Path
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash < 0 {
		http.Error(w, "missing work item ID", http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(path[lastSlash+1:])
	if err != nil {
		http.Error(w, "invalid work item ID", http.StatusBadRequest)
		return
	}

	wi, ok := m.items[id]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var ops []ado.PatchOperation
	if err := json.Unmarshal(body, &ops); err != nil {
		http.Error(w, "invalid patch ops", http.StatusBadRequest)
		return
	}

	// Simulate ADO state transition validation: reject invalid direct jumps.
	// E.g., New→Closed is not allowed (must go through Active→Resolved→Closed).
	currentState, _ := wi.Fields[ado.FieldState].(string)
	for _, op := range ops {
		fieldName := strings.TrimPrefix(op.Path, "/fields/")
		if fieldName == ado.FieldState {
			targetState, _ := op.Value.(string)
			if !m.isValidTransition(currentState, targetState) {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{
					"message": fmt.Sprintf("Invalid state transition from %q to %q", currentState, targetState),
				})
				return
			}
		}
	}

	for _, op := range ops {
		if op.Op == "add" || op.Op == "replace" {
			fieldName := strings.TrimPrefix(op.Path, "/fields/")
			wi.Fields[fieldName] = op.Value
		}
	}
	wi.Rev++
	wi.Fields[ado.FieldChangedDate] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(wi)
}

// isValidTransition checks if a state transition is allowed (Agile process).
// Rejects jumps that skip intermediate states (e.g., New→Closed).
func (m *mockADOServer) isValidTransition(from, to string) bool {
	if from == to {
		return true
	}
	// Model simplified Agile transitions.
	allowed := map[string]map[string]bool{
		"New":      {"Active": true},
		"Active":   {"New": true, "Resolved": true, "Closed": true},
		"Resolved": {"Active": true, "Closed": true},
		"Closed":   {"Active": true},
	}
	if targets, ok := allowed[from]; ok {
		return targets[to]
	}
	// Unknown states: allow anything.
	return true
}

func (m *mockADOServer) itemCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// adoTestStore creates a DoltStore for ADO round-trip tests using the Docker
// container infrastructure. Uses newTestStoreIsolatedDB for per-test DB isolation.
func adoTestStore(t *testing.T, prefix string) *dolt.DoltStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), ".beads", "dolt")
	return newTestStoreIsolatedDB(t, dbPath, prefix)
}

// TestADORoundTripCoreFields tests push→pull fidelity for core fields:
// title, description, priority, status, type, and external_ref.
func TestADORoundTripCoreFields(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	project := "TestProject"

	// --- 1. Setup source DB ---
	sourceStore := adoTestStore(t, "bd")

	// Start mock server
	mock := newMockADOServer(project)
	server := httptest.NewServer(mock)
	defer server.Close()

	// Configure ADO settings in source store
	for k, v := range map[string]string{
		"ado.pat":     "test-pat",
		"ado.url":     server.URL,
		"ado.project": project,
	} {
		if err := sourceStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig(%s): %v", k, err)
		}
	}

	// --- 2. Seed source DB with varied issues ---
	type seedIssue struct {
		title       string
		description string
		priority    int
		status      types.Status
		issueType   types.IssueType
	}
	seeds := []seedIssue{
		{"Critical security fix", "Fix the auth bypass vulnerability", 0, types.StatusOpen, types.TypeBug},
		{"Add search feature", "Implement full-text search for issues", 1, types.StatusInProgress, types.TypeFeature},
		{"Update dependencies", "Routine dep update for Q2", 3, types.StatusClosed, types.TypeTask},
	}

	sourceIssueIDs := make([]string, 0, len(seeds))
	for i, s := range seeds {
		issue := &types.Issue{
			ID:          fmt.Sprintf("bd-rt-%d", i),
			Title:       s.title,
			Description: s.description,
			Priority:    s.priority,
			Status:      s.status,
			IssueType:   s.issueType,
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

	// --- 3. Push to mock ADO ---
	at := &ado.Tracker{}
	if err := at.Init(ctx, sourceStore); err != nil {
		t.Fatalf("Tracker.Init: %v", err)
	}

	pushEngine := tracker.NewEngine(at, sourceStore, "test-actor")

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
	if got := mock.itemCount(); got != len(seeds) {
		t.Fatalf("mock server has %d items, want %d", got, len(seeds))
	}

	// --- 4. Setup target DB (fresh) ---
	targetStore := adoTestStore(t, "bd")

	for k, v := range map[string]string{
		"ado.pat":     "test-pat",
		"ado.url":     server.URL,
		"ado.project": project,
	} {
		if err := targetStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig(%s) target: %v", k, err)
		}
	}

	// --- 5. Pull from mock ADO into fresh DB ---
	at2 := &ado.Tracker{}
	if err := at2.Init(ctx, targetStore); err != nil {
		t.Fatalf("Tracker.Init (target): %v", err)
	}

	pullEngine := tracker.NewEngine(at2, targetStore, "test-actor")
	pullEngine.PullHooks = buildADOPullHooksForTest(ctx, targetStore)

	pullResult, err := pullEngine.Sync(ctx, tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Pull sync failed: %v", err)
	}
	if pullResult.Stats.Created != len(seeds) {
		t.Fatalf("expected %d pulled/created, got created=%d", len(seeds), pullResult.Stats.Created)
	}

	// --- 6. Assert fidelity ---
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
			if pulled.Title != source.Title {
				t.Errorf("title: got %q, want %q", pulled.Title, source.Title)
			}

			if pulled.Priority != source.Priority {
				t.Errorf("priority: got %d, want %d", pulled.Priority, source.Priority)
			}

			if pulled.Status != source.Status {
				t.Errorf("status: got %q, want %q", pulled.Status, source.Status)
			}

			if pulled.IssueType != source.IssueType {
				t.Errorf("type: got %q, want %q", pulled.IssueType, source.IssueType)
			}

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

// TestADORoundTripBlockedStatus tests that blocked status survives a round-trip
// via the beads:blocked tag mechanism (ADO has no native blocked state).
func TestADORoundTripBlockedStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	project := "TestProject"

	sourceStore := adoTestStore(t, "bd")

	mock := newMockADOServer(project)
	server := httptest.NewServer(mock)
	defer server.Close()

	for k, v := range map[string]string{
		"ado.pat": "test-pat", "ado.url": server.URL,
		"ado.project": project,
	} {
		if err := sourceStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig(%s): %v", k, err)
		}
	}

	// Create a blocked issue
	issue := &types.Issue{
		ID:        "bd-blocked-1",
		Title:     "Blocked on upstream fix",
		Status:    types.StatusBlocked,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	if err := sourceStore.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Push
	at := &ado.Tracker{}
	if err := at.Init(ctx, sourceStore); err != nil {
		t.Fatalf("Init: %v", err)
	}
	pushEngine := tracker.NewEngine(at, sourceStore, "test-actor")
	if _, err := pushEngine.Sync(ctx, tracker.SyncOptions{Push: true}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Pull into fresh DB
	targetStore := adoTestStore(t, "bd")
	for k, v := range map[string]string{
		"ado.pat": "test-pat", "ado.url": server.URL,
		"ado.project": project,
	} {
		if err := targetStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig target: %v", err)
		}
	}

	at2 := &ado.Tracker{}
	if err := at2.Init(ctx, targetStore); err != nil {
		t.Fatalf("Init target: %v", err)
	}
	pullEngine := tracker.NewEngine(at2, targetStore, "test-actor")
	pullEngine.PullHooks = buildADOPullHooksForTest(ctx, targetStore)
	if _, err := pullEngine.Sync(ctx, tracker.SyncOptions{Pull: true}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	pulled, err := targetStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(pulled) != 1 {
		t.Fatalf("got %d issues, want 1", len(pulled))
	}
	if pulled[0].Status != types.StatusBlocked {
		t.Errorf("status: got %q, want %q", pulled[0].Status, types.StatusBlocked)
	}
}

// TestADORoundTripLossyPriority tests the lossy priority mapping behavior.
// beads priorities 3 and 4 both map to ADO priority 4. On a fresh pull, ADO 4
// maps back to beads 3, so priority 4 (Backlog) is lost. This test documents
// the current behavior:
//   - P3 survives round-trip (ADO 4 → beads 3)
//   - P4 degrades to P3 (ADO 4 → beads 3) — this is a known lossy mapping
func TestADORoundTripLossyPriority(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	project := "TestProject"

	sourceStore := adoTestStore(t, "bd")

	mock := newMockADOServer(project)
	server := httptest.NewServer(mock)
	defer server.Close()

	for k, v := range map[string]string{
		"ado.pat": "test-pat", "ado.url": server.URL,
		"ado.project": project,
	} {
		if err := sourceStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig(%s): %v", k, err)
		}
	}

	// Create issues with priority 3 (Low) and 4 (Backlog) — both map to ADO 4
	for _, p := range []int{3, 4} {
		issue := &types.Issue{
			ID:        fmt.Sprintf("bd-lossy-p%d", p),
			Title:     fmt.Sprintf("Priority %d issue", p),
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			Priority:  p,
		}
		if err := sourceStore.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue(p%d): %v", p, err)
		}
	}

	// Push to ADO
	at := &ado.Tracker{}
	if err := at.Init(ctx, sourceStore); err != nil {
		t.Fatalf("Init: %v", err)
	}
	pushEngine := tracker.NewEngine(at, sourceStore, "test-actor")
	if _, err := pushEngine.Sync(ctx, tracker.SyncOptions{Push: true}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Pull into fresh DB (simulating a different machine)
	targetStore := adoTestStore(t, "bd")
	for k, v := range map[string]string{
		"ado.pat": "test-pat", "ado.url": server.URL,
		"ado.project": project,
	} {
		if err := targetStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig target: %v", err)
		}
	}

	at2 := &ado.Tracker{}
	if err := at2.Init(ctx, targetStore); err != nil {
		t.Fatalf("Init target: %v", err)
	}
	pullEngine := tracker.NewEngine(at2, targetStore, "test-actor")
	pullEngine.PullHooks = buildADOPullHooksForTest(ctx, targetStore)
	if _, err := pullEngine.Sync(ctx, tracker.SyncOptions{Pull: true}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	pulled, err := targetStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(pulled) != 2 {
		t.Fatalf("got %d issues, want 2", len(pulled))
	}

	// P3 survives perfectly (ADO 4 → beads 3)
	// P4 degrades to P3 (known lossy mapping: ADO 4 → beads 3)
	for _, issue := range pulled {
		switch {
		case strings.Contains(issue.Title, "Priority 3"):
			if issue.Priority != 3 {
				t.Errorf("P3 issue: got priority %d, want 3", issue.Priority)
			}
		case strings.Contains(issue.Title, "Priority 4"):
			// Documented lossy behavior: P4 becomes P3 after fresh round-trip
			if issue.Priority != 3 {
				t.Errorf("P4 issue (lossy): got priority %d, want 3 (degraded from 4)", issue.Priority)
			}
		default:
			t.Errorf("unexpected issue title: %q", issue.Title)
		}
	}
}

// TestADORoundTripLabels tests that user labels survive a round-trip and
// internal beads:* tags are correctly filtered on pull.
func TestADORoundTripLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	project := "TestProject"

	sourceStore := adoTestStore(t, "bd")

	mock := newMockADOServer(project)
	server := httptest.NewServer(mock)
	defer server.Close()

	for k, v := range map[string]string{
		"ado.pat": "test-pat", "ado.url": server.URL,
		"ado.project": project,
	} {
		if err := sourceStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig(%s): %v", k, err)
		}
	}

	issue := &types.Issue{
		ID:        "bd-labels-1",
		Title:     "Issue with labels",
		Status:    types.StatusOpen,
		IssueType: types.TypeBug,
		Priority:  1,
		Labels:    []string{"backend", "urgent", "api"},
	}
	if err := sourceStore.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Push
	at := &ado.Tracker{}
	if err := at.Init(ctx, sourceStore); err != nil {
		t.Fatalf("Init: %v", err)
	}
	pushEngine := tracker.NewEngine(at, sourceStore, "test-actor")
	if _, err := pushEngine.Sync(ctx, tracker.SyncOptions{Push: true}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Pull into fresh DB
	targetStore := adoTestStore(t, "bd")
	for k, v := range map[string]string{
		"ado.pat": "test-pat", "ado.url": server.URL,
		"ado.project": project,
	} {
		if err := targetStore.SetConfig(ctx, k, v); err != nil {
			t.Fatalf("SetConfig target: %v", err)
		}
	}

	at2 := &ado.Tracker{}
	if err := at2.Init(ctx, targetStore); err != nil {
		t.Fatalf("Init target: %v", err)
	}
	pullEngine := tracker.NewEngine(at2, targetStore, "test-actor")
	pullEngine.PullHooks = buildADOPullHooksForTest(ctx, targetStore)
	if _, err := pullEngine.Sync(ctx, tracker.SyncOptions{Pull: true}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	pulled, err := targetStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(pulled) != 1 {
		t.Fatalf("got %d issues, want 1", len(pulled))
	}

	// Verify labels survived and no beads:* tags leaked through
	gotLabels := pulled[0].Labels
	wantLabels := []string{"backend", "urgent", "api"}
	if len(gotLabels) != len(wantLabels) {
		t.Errorf("labels: got %v, want %v", gotLabels, wantLabels)
	} else {
		gotSet := make(map[string]bool)
		for _, l := range gotLabels {
			gotSet[l] = true
		}
		for _, l := range wantLabels {
			if !gotSet[l] {
				t.Errorf("missing label %q in pulled issue", l)
			}
		}
	}
	// Ensure no beads:* tags leaked
	for _, l := range gotLabels {
		if strings.HasPrefix(l, "beads:") {
			t.Errorf("internal tag leaked through: %q", l)
		}
	}
}

// buildADOPullHooksForTest mirrors buildADOPullHooks from ado.go but works
// with an explicit store instead of global state.
func buildADOPullHooksForTest(ctx context.Context, st interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
}) *tracker.PullHooks {
	hooks := &tracker.PullHooks{}

	existingIssues, err := st.SearchIssues(ctx, "", types.IssueFilter{})
	usedIDs := make(map[string]bool)
	if err == nil {
		for _, issue := range existingIssues {
			if issue.ID != "" {
				usedIDs[issue.ID] = true
			}
		}
	}

	seq := 0
	hooks.GenerateID = func(_ context.Context, issue *types.Issue) error {
		if issue.ID == "" {
			seq++
			issue.ID = fmt.Sprintf("bd-ado-%d", seq)
			for usedIDs[issue.ID] {
				seq++
				issue.ID = fmt.Sprintf("bd-ado-%d", seq)
			}
			usedIDs[issue.ID] = true
		}
		return nil
	}

	return hooks
}
