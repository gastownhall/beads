package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// mockJiraHierarchyServer is a stateful in-memory Jira REST v3 mock that stores
// issues (including parent, issuetype, summary, description, updated) and records
// POST/PUT payloads so tests can assert exact wire fields and idempotency.
type mockJiraHierarchyServer struct {
	mu          sync.Mutex
	issues      map[string]*Issue
	nextSeq     int
	projectKey  string
	getCount    map[string]int
	createPayls []map[string]interface{}
	updatePayls map[string][]map[string]interface{}
	forcePutErr map[string]int // key -> HTTP status code to return on PUT
}

func newMockJiraHierarchyServer(projectKey string) *mockJiraHierarchyServer {
	return &mockJiraHierarchyServer{
		issues:      make(map[string]*Issue),
		projectKey:  projectKey,
		getCount:    make(map[string]int),
		updatePayls: make(map[string][]map[string]interface{}),
		forcePutErr: make(map[string]int),
	}
}

func (m *mockJiraHierarchyServer) seedIssue(key, issueType, summary, parentKey string) *Issue {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSeq++
	id := fmt.Sprintf("%d", 10000+m.nextSeq)
	iss := &Issue{
		ID:   id,
		Key:  key,
		Self: "https://example.atlassian.net/rest/api/3/issue/" + id,
		Fields: IssueFields{
			Summary:   summary,
			IssueType: &IssueTypeField{Name: issueType},
			Status:    &StatusField{Name: "To Do"},
			Priority:  &PriorityField{Name: "Medium"},
			Project:   &ProjectField{Key: m.projectKey},
			Created:   time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000+0000"),
			Updated:   time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05.000+0000"),
		},
	}
	if parentKey != "" {
		iss.Fields.Parent = &ParentField{Key: parentKey}
	}
	m.issues[key] = iss
	return iss
}

func (m *mockJiraHierarchyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue":
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		fields, _ := payload["fields"].(map[string]interface{})
		m.mu.Lock()
		m.createPayls = append(m.createPayls, fields)
		m.nextSeq++
		key := fmt.Sprintf("%s-%d", m.projectKey, 100+m.nextSeq)
		id := fmt.Sprintf("%d", 20000+m.nextSeq)
		summary, _ := fields["summary"].(string)
		typeName := "Task"
		if it, ok := fields["issuetype"].(map[string]interface{}); ok {
			if n, ok := it["name"].(string); ok && n != "" {
				typeName = n
			}
		}
		var parent *ParentField
		if p, ok := fields["parent"].(map[string]interface{}); ok {
			if pk, ok := p["key"].(string); ok && pk != "" {
				parent = &ParentField{Key: pk}
			}
		}
		iss := &Issue{
			ID:   id,
			Key:  key,
			Self: "https://example.atlassian.net/rest/api/3/issue/" + id,
			Fields: IssueFields{
				Summary:   summary,
				IssueType: &IssueTypeField{Name: typeName},
				Status:    &StatusField{Name: "To Do"},
				Priority:  &PriorityField{Name: "Medium"},
				Project:   &ProjectField{Key: m.projectKey},
				Parent:    parent,
				Created:   time.Now().UTC().Format("2006-01-02T15:04:05.000+0000"),
				Updated:   time.Now().UTC().Add(time.Minute).Format("2006-01-02T15:04:05.000+0000"),
			},
		}
		m.issues[key] = iss
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":   id,
			"key":  key,
			"self": iss.Self,
		})

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
		key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
		m.mu.Lock()
		m.getCount[key]++
		iss, ok := m.issues[key]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errorMessages":["Issue Does Not Exist"]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(iss)

	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
		key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		fields, _ := payload["fields"].(map[string]interface{})
		m.mu.Lock()
		if statusCode, forced := m.forcePutErr[key]; forced {
			m.mu.Unlock()
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(`{"errorMessages":["Issue type hierarchy violation"]}`))
			return
		}
		iss, ok := m.issues[key]
		if !ok {
			m.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.updatePayls[key] = append(m.updatePayls[key], fields)
		if summary, ok := fields["summary"].(string); ok && summary != "" {
			iss.Fields.Summary = summary
		}
		if p, ok := fields["parent"].(map[string]interface{}); ok {
			if pk, ok := p["key"].(string); ok && pk != "" {
				iss.Fields.Parent = &ParentField{Key: pk}
			}
		}
		iss.Fields.Updated = time.Now().UTC().Add(time.Minute).Format("2006-01-02T15:04:05.000+0000")
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	default:
		http.NotFound(w, r)
	}
}

type depStubStore struct {
	*configStore
	deps map[string][]*types.IssueWithDependencyMetadata
}

func (s *depStubStore) GetDependenciesWithMetadata(_ context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps[id], nil
}

func TestSearchFieldsIncludesParentAndConversionSetsParent(t *testing.T) {
	if !strings.Contains(searchFields, "parent") {
		t.Fatalf("searchFields = %q, must include parent", searchFields)
	}

	ji := &Issue{
		ID:   "10002",
		Key:  "CORP-12346",
		Self: "https://example.atlassian.net/rest/api/3/issue/10002",
		Fields: IssueFields{
			Summary:   "Task A",
			IssueType: &IssueTypeField{Name: "Task"},
			Parent: &ParentField{
				ID:   "10001",
				Key:  "CORP-12345",
				Self: "https://example.atlassian.net/rest/api/3/issue/10001",
			},
		},
	}

	ti := jiraToTrackerIssue(ji, nil)
	if ti.ParentID != "CORP-12345" {
		t.Errorf("ParentID = %q, want %q", ti.ParentID, "CORP-12345")
	}
	if ti.ParentInternalID != "10001" {
		t.Errorf("ParentInternalID = %q, want %q", ti.ParentInternalID, "10001")
	}

	mapper := &jiraFieldMapper{}
	conv := mapper.IssueToBeads(&ti)
	if conv == nil {
		t.Fatal("IssueToBeads returned nil")
	}
	if len(conv.Dependencies) != 1 {
		t.Fatalf("Dependencies len = %d, want 1", len(conv.Dependencies))
	}
	dep := conv.Dependencies[0]
	if dep.Type != string(types.DepParentChild) || dep.ToExternalID != "CORP-12345" || dep.Source != tracker.DependencySourceParent {
		t.Errorf("unexpected dep = %+v", dep)
	}
}

func TestCreateIssueSetsParentFromLinkedParentBead(t *testing.T) {
	mock := newMockJiraHierarchyServer("CORP")
	mock.seedIssue("CORP-12345", "Epic", "Example epic", "")
	srv := httptest.NewServer(mock)
	defer srv.Close()

	epicRef := srv.URL + "/browse/CORP-12345"
	st := &depStubStore{
		configStore: &configStore{data: map[string]string{}},
		deps: map[string][]*types.IssueWithDependencyMetadata{
			"bd-abc.1": {
				{
					Issue: types.Issue{
						ID:          "bd-abc",
						Title:       "Example epic",
						IssueType:   types.TypeEpic,
						ExternalRef: &epicRef,
					},
					DependencyType: types.DepParentChild,
				},
			},
		},
	}

	tr := &Tracker{
		client:      newTestClient(srv.URL, "3"),
		store:       st,
		jiraURL:     srv.URL,
		projectKeys: []string{"CORP"},
		apiVersion:  "3",
	}

	created, err := tr.CreateIssue(context.Background(), &types.Issue{
		ID:        "bd-abc.1",
		Title:     "Task A",
		IssueType: types.TypeTask,
		Priority:  2,
		Status:    types.StatusOpen,
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if created.ParentID != "CORP-12345" {
		t.Errorf("created.ParentID = %q, want %q", created.ParentID, "CORP-12345")
	}
	if len(mock.createPayls) != 1 {
		t.Fatalf("createPayls len = %d, want 1", len(mock.createPayls))
	}
	fields := mock.createPayls[0]
	it, _ := fields["issuetype"].(map[string]interface{})
	if it["name"] != "Task" {
		t.Errorf("issuetype = %v, want Task (must not convert to Sub-task)", it)
	}
	parentField, ok := fields["parent"].(map[string]interface{})
	if !ok || parentField["key"] != "CORP-12345" {
		t.Errorf("create payload parent = %v, want key CORP-12345", fields["parent"])
	}
}

func TestCreateIssueRejectsInvalidHierarchyAndMultipleParents(t *testing.T) {
	mock := newMockJiraHierarchyServer("CORP")
	mock.seedIssue("CORP-10", "Task", "Parent task", "")
	srv := httptest.NewServer(mock)
	defer srv.Close()

	parentRef := srv.URL + "/browse/CORP-10"
	parentRef2 := srv.URL + "/browse/CORP-11"

	t.Run("task under task rejected", func(t *testing.T) {
		st := &depStubStore{
			configStore: &configStore{data: map[string]string{}},
			deps: map[string][]*types.IssueWithDependencyMetadata{
				"bd-child": {{
					Issue:          types.Issue{ID: "bd-parent", IssueType: types.TypeTask, ExternalRef: &parentRef},
					DependencyType: types.DepParentChild,
				}},
			},
		}
		tr := &Tracker{client: newTestClient(srv.URL, "3"), store: st, jiraURL: srv.URL, projectKeys: []string{"CORP"}, apiVersion: "3"}
		_, err := tr.CreateIssue(context.Background(), &types.Issue{ID: "bd-child", Title: "Child", IssueType: types.TypeTask})
		if err == nil || !strings.Contains(err.Error(), "unsupported Jira hierarchy") {
			t.Fatalf("expected unsupported Jira hierarchy error, got %v", err)
		}
		if len(mock.createPayls) != 0 {
			t.Errorf("expected no POST /issue on invalid hierarchy, got %d", len(mock.createPayls))
		}
	})

	t.Run("multiple parents rejected", func(t *testing.T) {
		st := &depStubStore{
			configStore: &configStore{data: map[string]string{}},
			deps: map[string][]*types.IssueWithDependencyMetadata{
				"bd-child": {
					{Issue: types.Issue{ID: "bd-p1", IssueType: types.TypeEpic, ExternalRef: &parentRef}, DependencyType: types.DepParentChild},
					{Issue: types.Issue{ID: "bd-p2", IssueType: types.TypeEpic, ExternalRef: &parentRef2}, DependencyType: types.DepParentChild},
				},
			},
		}
		tr := &Tracker{client: newTestClient(srv.URL, "3"), store: st, jiraURL: srv.URL, projectKeys: []string{"CORP"}, apiVersion: "3"}
		_, err := tr.CreateIssue(context.Background(), &types.Issue{ID: "bd-child", Title: "Child", IssueType: types.TypeTask})
		if err == nil || !strings.Contains(err.Error(), "multiple parent-child dependencies") {
			t.Fatalf("expected multiple parent-child dependencies error, got %v", err)
		}
	})
}

func TestReconcileParents_AssignReparentIdempotentAndCache(t *testing.T) {
	mock := newMockJiraHierarchyServer("CORP")
	mock.seedIssue("CORP-100", "Epic", "Epic 1", "")
	mock.seedIssue("CORP-999", "Epic", "Old Epic", "")
	mock.seedIssue("CORP-101", "Task", "Missing parent", "")
	mock.seedIssue("CORP-102", "Task", "Wrong parent", "CORP-999")
	mock.seedIssue("CORP-103", "Task", "Already right", "CORP-100")

	srv := httptest.NewServer(mock)
	defer srv.Close()

	tr := &Tracker{
		client:      newTestClient(srv.URL, "3"),
		jiraURL:     srv.URL,
		projectKeys: []string{"CORP"},
		apiVersion:  "3",
	}

	links := []ParentLink{
		{ChildBeadID: "bd-1", ChildIdentifier: "CORP-101", ParentBeadID: "bd-epic", ParentIdentifier: "CORP-100"},
		{ChildBeadID: "bd-2", ChildIdentifier: "CORP-102", ParentBeadID: "bd-epic", ParentIdentifier: "CORP-100"},
		{ChildBeadID: "bd-3", ChildIdentifier: "CORP-103", ParentBeadID: "bd-epic", ParentIdentifier: "CORP-100"},
	}

	// Dry-run first: must preview 2 mutations, skip 1, and issue 0 PUTs.
	dryStats, err := tr.ReconcileParents(context.Background(), links, true)
	if err != nil {
		t.Fatalf("dry-run ReconcileParents err: %v", err)
	}
	if dryStats.WouldUpdate != 2 || dryStats.Updated != 0 || dryStats.Skipped != 1 || len(dryStats.Errors) != 0 {
		t.Fatalf("dryStats = %+v, want WouldUpdate=2 Updated=0 Skipped=1", dryStats)
	}
	if dryStats.Mutations[0].Action != "assign" || dryStats.Mutations[1].Action != "reparent" || dryStats.Mutations[1].CurrentParentKey != "CORP-999" {
		t.Errorf("dryStats.Mutations = %+v", dryStats.Mutations)
	}
	if len(mock.updatePayls) != 0 {
		t.Fatalf("dry-run issued PUT requests: %+v", mock.updatePayls)
	}
	// Parent CORP-100 should have been fetched only once across 3 links.
	if mock.getCount["CORP-100"] != 1 {
		t.Errorf("CORP-100 fetch count = %d, want 1", mock.getCount["CORP-100"])
	}

	// Wet run: updates CORP-101 and CORP-102, skips CORP-103.
	stats, err := tr.ReconcileParents(context.Background(), links, false)
	if err != nil {
		t.Fatalf("wet ReconcileParents err: %v", err)
	}
	if stats.Updated != 2 || stats.Skipped != 1 || len(stats.Errors) != 0 {
		t.Fatalf("stats = %+v, want Updated=2 Skipped=1", stats)
	}
	if mock.issues["CORP-101"].Fields.Parent == nil || mock.issues["CORP-101"].Fields.Parent.Key != "CORP-100" {
		t.Errorf("CORP-101 parent = %+v, want CORP-100", mock.issues["CORP-101"].Fields.Parent)
	}
	if mock.issues["CORP-102"].Fields.Parent == nil || mock.issues["CORP-102"].Fields.Parent.Key != "CORP-100" {
		t.Errorf("CORP-102 parent = %+v, want CORP-100", mock.issues["CORP-102"].Fields.Parent)
	}

	// Second wet run: idempotent — 0 updates, 3 skipped.
	stats2, err := tr.ReconcileParents(context.Background(), links, false)
	if err != nil {
		t.Fatalf("second ReconcileParents err: %v", err)
	}
	if stats2.Updated != 0 || stats2.Skipped != 3 {
		t.Fatalf("second run stats = %+v, want Updated=0 Skipped=3", stats2)
	}
	if len(mock.updatePayls["CORP-101"]) != 1 || len(mock.updatePayls["CORP-102"]) != 1 || len(mock.updatePayls["CORP-103"]) != 0 {
		t.Errorf("unexpected PUT counts: %+v", mock.updatePayls)
	}
}

func TestReconcileParents_HierarchyValidationAndErrors(t *testing.T) {
	mock := newMockJiraHierarchyServer("CORP")
	mock.seedIssue("CORP-1", "Epic", "Epic 1", "")
	mock.seedIssue("CORP-2", "Epic", "Epic 2", "")
	mock.seedIssue("CORP-3", "Task", "Task 3", "")
	mock.seedIssue("CORP-4", "Sub-task", "Subtask 4", "")
	mock.seedIssue("CORP-5", "Initiative", "Initiative 5", "") // Custom Jira Premium level above Epic
	mock.seedIssue("CORP-6", "Sub-task", "Subtask 6", "")
	mock.forcePutErr["CORP-6"] = http.StatusBadRequest

	srv := httptest.NewServer(mock)
	defer srv.Close()

	tr := &Tracker{
		client:      newTestClient(srv.URL, "3"),
		jiraURL:     srv.URL,
		projectKeys: []string{"CORP"},
		apiVersion:  "3",
	}

	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{ChildIdentifier: "CORP-2", ParentIdentifier: "CORP-1"},   // Epic under Epic -> invalid
		{ChildIdentifier: "CORP-1", ParentIdentifier: "CORP-3"},   // Epic under Task -> invalid
		{ChildIdentifier: "CORP-3", ParentIdentifier: "CORP-4"},   // Task under Sub-task -> invalid
		{ChildIdentifier: "CORP-3", ParentIdentifier: "CORP-3"},   // Self-parent -> invalid
		{ChildIdentifier: "CORP-3", ParentIdentifier: "CORP-404"}, // Missing parent -> NotFound
		{ChildIdentifier: "CORP-6", ParentIdentifier: "CORP-1"},   // Jira API 400 -> Error
		{ChildIdentifier: "CORP-1", ParentIdentifier: "CORP-5"},   // Epic under Initiative -> allowed
	}, false)
	if err != nil {
		t.Fatalf("ReconcileParents unexpected fatal err: %v", err)
	}
	if stats.Updated != 1 {
		t.Errorf("Updated = %d, want 1 (CORP-1 -> CORP-5)", stats.Updated)
	}
	if len(stats.NotFound) != 1 || stats.NotFound[0] != "CORP-404" {
		t.Errorf("NotFound = %v, want [CORP-404]", stats.NotFound)
	}
	if len(stats.Errors) != 5 {
		t.Errorf("Errors len = %d, want 5; got: %v", len(stats.Errors), stats.Errors)
	}
}

func TestReconcileParents_DryRunNewIssues(t *testing.T) {
	mock := newMockJiraHierarchyServer("CORP")
	mock.seedIssue("CORP-100", "Epic", "Existing Epic", "")
	srv := httptest.NewServer(mock)
	defer srv.Close()

	tr := &Tracker{
		client:      newTestClient(srv.URL, "3"),
		jiraURL:     srv.URL,
		projectKeys: []string{"CORP"},
		apiVersion:  "3",
	}

	stats, err := tr.ReconcileParents(context.Background(), []ParentLink{
		{
			ChildBeadID:      "bd-new-1",
			ChildJiraType:    "Task",
			ParentBeadID:     "bd-epic",
			ParentIdentifier: "CORP-100",
		},
		{
			ChildBeadID:    "bd-new-2",
			ChildJiraType:  "Task",
			ParentBeadID:   "bd-new-epic",
			ParentJiraType: "Epic",
		},
		{
			ChildBeadID:    "bd-bad",
			ChildJiraType:  "Epic",
			ParentBeadID:   "bd-new-task",
			ParentJiraType: "Task",
		},
	}, true)
	if err != nil {
		t.Fatalf("dry-run err: %v", err)
	}
	if stats.WouldUpdate != 2 || len(stats.Errors) != 1 {
		t.Fatalf("stats = %+v, want WouldUpdate=2, 1 hierarchy error", stats)
	}
}
