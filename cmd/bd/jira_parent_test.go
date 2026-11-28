package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/jira"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// memJiraTrackerStore is an in-memory tracker.Store supporting issues,
// dependencies, config, and local metadata for end-to-end Jira sync tests
// without requiring a Dolt server.
type memJiraTrackerStore struct {
	mu       sync.Mutex
	config   map[string]string
	metadata map[string]string
	issues   map[string]*types.Issue
	order    []string // deterministic iteration order (can be reversed to test child-before-parent)
	deps     map[string][]*types.Dependency
}

func newMemJiraTrackerStore(cfg map[string]string) *memJiraTrackerStore {
	return &memJiraTrackerStore{
		config:   cfg,
		metadata: make(map[string]string),
		issues:   make(map[string]*types.Issue),
		deps:     make(map[string][]*types.Dependency),
	}
}

func (s *memJiraTrackerStore) addIssue(issue *types.Issue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *issue
	if issue.ExternalRef != nil {
		ref := *issue.ExternalRef
		cp.ExternalRef = &ref
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC().Add(-time.Minute)
	}
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = time.Now().UTC()
	}
	s.issues[cp.ID] = &cp
	s.order = append(s.order, cp.ID)
}

func (s *memJiraTrackerStore) addParentChild(childID, parentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps[childID] = append(s.deps[childID], &types.Dependency{
		IssueID:     childID,
		DependsOnID: parentID,
		Type:        types.DepParentChild,
	})
}

func (s *memJiraTrackerStore) GetConfig(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config[key], nil
}

func (s *memJiraTrackerStore) GetAllConfig(_ context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.config))
	for k, v := range s.config {
		out[k] = v
	}
	return out, nil
}

func (s *memJiraTrackerStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata[key], nil
}

func (s *memJiraTrackerStore) SetLocalMetadata(_ context.Context, key, val string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadata[key] = val
	return nil
}

func (s *memJiraTrackerStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	if filter.MaxRows != 0 || filter.MaxRowsSource != "" {
		return nil, fmt.Errorf("unexpected MaxRows=%d source=%q in jira sync path", filter.MaxRows, filter.MaxRowsSource)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*types.Issue, 0, len(s.order))
	for _, id := range s.order {
		if iss, ok := s.issues[id]; ok {
			cp := *iss
			if iss.ExternalRef != nil {
				ref := *iss.ExternalRef
				cp.ExternalRef = &ref
			}
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *memJiraTrackerStore) GetIssueByExternalRef(_ context.Context, ref string) (*types.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, iss := range s.issues {
		if iss.ExternalRef != nil && *iss.ExternalRef == ref {
			cp := *iss
			r := *iss.ExternalRef
			cp.ExternalRef = &r
			return &cp, nil
		}
	}
	return nil, storage.ErrNotFound
}

func (s *memJiraTrackerStore) GetDependentsWithMetadata(_ context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*types.IssueWithDependencyMetadata
	for childID, deps := range s.deps {
		for _, d := range deps {
			if d.DependsOnID == id {
				if child, ok := s.issues[childID]; ok {
					out = append(out, &types.IssueWithDependencyMetadata{
						Issue:          *child,
						DependencyType: d.Type,
					})
				}
			}
		}
	}
	return out, nil
}

func (s *memJiraTrackerStore) GetDependenciesWithMetadata(_ context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*types.IssueWithDependencyMetadata
	for _, d := range s.deps[id] {
		if parent, ok := s.issues[d.DependsOnID]; ok {
			out = append(out, &types.IssueWithDependencyMetadata{
				Issue:          *parent,
				DependencyType: d.Type,
			})
		}
	}
	return out, nil
}

func (s *memJiraTrackerStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	s.addIssue(issue)
	return nil
}

func (s *memJiraTrackerStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss, ok := s.issues[id]
	if !ok {
		return storage.ErrNotFound
	}
	if ref, ok := updates["external_ref"].(string); ok {
		iss.ExternalRef = &ref
	}
	iss.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *memJiraTrackerStore) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deps[dep.IssueID] = append(s.deps[dep.IssueID], dep)
	return nil
}

// mockJiraRESTServer implements a stateful Jira v3 REST API for cmd/bd tests.
type mockJiraRESTServer struct {
	mu          sync.Mutex
	projectKey  string
	issues      map[string]*jira.Issue
	nextNum     int
	postCalls   []map[string]interface{}
	putCalls    map[string][]map[string]interface{}
	searchCalls int
}

func newMockJiraRESTServer(projectKey string, startNum int) *mockJiraRESTServer {
	return &mockJiraRESTServer{
		projectKey: projectKey,
		issues:     make(map[string]*jira.Issue),
		nextNum:    startNum,
		putCalls:   make(map[string][]map[string]interface{}),
	}
}

func (m *mockJiraRESTServer) seed(key, issueType, summary, parentKey string, updated time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := fmt.Sprintf("id-%s", key)
	iss := &jira.Issue{
		ID:   id,
		Key:  key,
		Self: "https://example.atlassian.net/rest/api/3/issue/" + id,
		Fields: jira.IssueFields{
			Summary:   summary,
			IssueType: &jira.IssueTypeField{Name: issueType},
			Status:    &jira.StatusField{Name: "To Do"},
			Priority:  &jira.PriorityField{Name: "Medium"},
			Project:   &jira.ProjectField{Key: m.projectKey},
			Created:   updated.Add(-time.Hour).Format("2006-01-02T15:04:05.000+0000"),
			Updated:   updated.Format("2006-01-02T15:04:05.000+0000"),
		},
	}
	if parentKey != "" {
		iss.Fields.Parent = &jira.ParentField{Key: parentKey}
	}
	m.issues[key] = iss
}

func (m *mockJiraRESTServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue":
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)
		fields, _ := payload["fields"].(map[string]interface{})

		m.mu.Lock()
		m.postCalls = append(m.postCalls, fields)
		m.nextNum++
		key := fmt.Sprintf("%s-%d", m.projectKey, m.nextNum)
		id := fmt.Sprintf("id-%s", key)
		summary, _ := fields["summary"].(string)
		typeName := "Task"
		if it, ok := fields["issuetype"].(map[string]interface{}); ok {
			if n, _ := it["name"].(string); n != "" {
				typeName = n
			}
		}
		var parent *jira.ParentField
		if p, ok := fields["parent"].(map[string]interface{}); ok {
			if pk, _ := p["key"].(string); pk != "" {
				parent = &jira.ParentField{Key: pk}
			}
		}
		now := time.Now().UTC().Add(time.Minute)
		iss := &jira.Issue{
			ID:   id,
			Key:  key,
			Self: "https://example.atlassian.net/rest/api/3/issue/" + id,
			Fields: jira.IssueFields{
				Summary:   summary,
				IssueType: &jira.IssueTypeField{Name: typeName},
				Status:    &jira.StatusField{Name: "To Do"},
				Priority:  &jira.PriorityField{Name: "Medium"},
				Project:   &jira.ProjectField{Key: m.projectKey},
				Parent:    parent,
				Created:   now.Format("2006-01-02T15:04:05.000+0000"),
				Updated:   now.Format("2006-01-02T15:04:05.000+0000"),
			},
		}
		m.issues[key] = iss
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "key": key, "self": iss.Self})

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
		key := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
		m.mu.Lock()
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
		_ = json.Unmarshal(body, &payload)
		fields, _ := payload["fields"].(map[string]interface{})

		m.mu.Lock()
		iss, ok := m.issues[key]
		if !ok {
			m.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.putCalls[key] = append(m.putCalls[key], fields)
		if p, ok := fields["parent"].(map[string]interface{}); ok {
			if pk, _ := p["key"].(string); pk != "" {
				iss.Fields.Parent = &jira.ParentField{Key: pk}
			}
		}
		if s, ok := fields["summary"].(string); ok && s != "" {
			iss.Fields.Summary = s
		}
		iss.Fields.Updated = time.Now().UTC().Add(time.Minute).Format("2006-01-02T15:04:05.000+0000")
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/search/jql":
		m.mu.Lock()
		m.searchCalls++
		var list []jira.Issue
		for _, iss := range m.issues {
			list = append(list, *iss)
		}
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(jira.SearchResult{Total: len(list), IsLast: true, Issues: list})

	default:
		http.NotFound(w, r)
	}
}

func setupJiraTestTrustWorkspace(t *testing.T, srvURL string) {
	t.Helper()
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("JIRA_URL", srvURL)
	t.Setenv("JIRA_API_TOKEN", "test-token")
}

func TestJiraSyncPushParentSubtreePreservesHierarchy(t *testing.T) {
	mock := newMockJiraRESTServer("CORP", 12345)
	mock.seed("CORP-12345", "Epic", "Example epic", "", time.Now().UTC().Add(time.Hour))
	mock.seed("CORP-90001", "Task", "Out of scope task", "", time.Now().UTC().Add(time.Hour))
	srv := httptest.NewServer(mock)
	defer srv.Close()

	setupJiraTestTrustWorkspace(t, srv.URL)
	ctx := context.Background()

	epicRef := srv.URL + "/browse/CORP-12345"
	unrelatedRef := srv.URL + "/browse/CORP-90001"
	st := newMemJiraTrackerStore(map[string]string{
		"jira.url":     srv.URL,
		"jira.project": "CORP",
	})
	st.addIssue(&types.Issue{ID: "bd-abc", Title: "Example epic", IssueType: types.TypeEpic, Status: types.StatusOpen, ExternalRef: &epicRef})
	st.addIssue(&types.Issue{ID: "bd-abc.1", Title: "Task A", IssueType: types.TypeTask, Status: types.StatusOpen})
	st.addIssue(&types.Issue{ID: "bd-abc.2", Title: "Task B", IssueType: types.TypeTask, Status: types.StatusOpen})
	st.addIssue(&types.Issue{ID: "bd-other-epic", Title: "Other epic", IssueType: types.TypeEpic, Status: types.StatusOpen, ExternalRef: &epicRef})
	st.addIssue(&types.Issue{ID: "bd-other-task", Title: "Other task", IssueType: types.TypeTask, Status: types.StatusOpen, ExternalRef: &unrelatedRef})
	st.addParentChild("bd-abc.1", "bd-abc")
	st.addParentChild("bd-abc.2", "bd-abc")
	st.addParentChild("bd-other-task", "bd-other-epic") // outside --parent bd-abc scope

	jt := &jira.Tracker{}
	if err := jt.Init(ctx, st); err != nil {
		t.Fatalf("jt.Init: %v", err)
	}
	pushHooks := buildJiraPushHooksForStore(ctx, st)

	// 1. Dry-run with --parent bd-abc: previews parent assignment for new tasks, touches nothing, skips out-of-scope bd-other-task.
	dryEngine := tracker.NewEngine(jt, st, "tester")
	dryEngine.PushHooks = pushHooks
	dryOpts := tracker.SyncOptions{Push: true, DryRun: true, ParentID: "bd-abc"}
	dryRes, err := dryEngine.Sync(ctx, dryOpts)
	if err != nil {
		t.Fatalf("dry-run Sync: %v", err)
	}
	reconcileJiraParentsForStore(ctx, st, jt, pushHooks, dryOpts, nil, true, true, dryRes)
	if dryRes.Stats.Created != 2 || len(mock.postCalls) != 0 || len(mock.putCalls) != 0 {
		t.Fatalf("dry-run mutated remote or wrong created count: stats=%+v posts=%d puts=%d", dryRes.Stats, len(mock.postCalls), len(mock.putCalls))
	}

	// 2. Wet run with --parent bd-abc: creates bd-abc.1 and bd-abc.2 with parent=CORP-12345, reuses CORP-12345, leaves CORP-90001 untouched.
	engine := tracker.NewEngine(jt, st, "tester")
	engine.PushHooks = pushHooks
	opts := tracker.SyncOptions{Push: true, ParentID: "bd-abc"}
	res, err := engine.Sync(ctx, opts)
	if err != nil {
		t.Fatalf("wet Sync: %v", err)
	}
	reconcileJiraParentsForStore(ctx, st, jt, pushHooks, opts, nil, false, true, res)
	if res.Stats.Created != 2 || res.Stats.Errors != 0 {
		t.Fatalf("wet sync stats = %+v warnings=%v", res.Stats, res.Warnings)
	}
	if len(mock.postCalls) != 2 {
		t.Fatalf("POST count = %d, want 2 (epic must be reused, not duplicated)", len(mock.postCalls))
	}
	for _, key := range []string{"CORP-12346", "CORP-12347"} {
		iss := mock.issues[key]
		if iss == nil || iss.Fields.Parent == nil || iss.Fields.Parent.Key != "CORP-12345" {
			t.Errorf("%s parent = %+v, want CORP-12345", key, iss)
		}
		if iss != nil && (iss.Fields.IssueType == nil || iss.Fields.IssueType.Name != "Task") {
			t.Errorf("%s issuetype = %+v, want Task", key, iss.Fields.IssueType)
		}
	}
	if mock.issues["CORP-90001"].Fields.Parent != nil {
		t.Errorf("out-of-scope CORP-90001 should not have been reparented, got %+v", mock.issues["CORP-90001"].Fields.Parent)
	}

	// 3. Re-run must be idempotent (0 creates, 0 updates, 0 PUTs).
	putCountBefore := len(mock.putCalls)
	res2, err := engine.Sync(ctx, opts)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	reconcileJiraParentsForStore(ctx, st, jt, pushHooks, opts, nil, false, true, res2)
	if res2.Stats.Created != 0 || res2.Stats.Updated != 0 || len(mock.putCalls) != putCountBefore {
		t.Fatalf("second run not idempotent: stats=%+v puts=%+v", res2.Stats, mock.putCalls)
	}
}

func TestJiraSyncPushChildBeforeNewParentReconcilesAndReparents(t *testing.T) {
	mock := newMockJiraRESTServer("CORP", 200)
	mock.seed("CORP-199", "Epic", "New Target Epic", "", time.Now().UTC().Add(time.Hour))
	srv := httptest.NewServer(mock)
	defer srv.Close()

	setupJiraTestTrustWorkspace(t, srv.URL)
	ctx := context.Background()

	st := newMemJiraTrackerStore(map[string]string{
		"jira.url":     srv.URL,
		"jira.project": "CORP",
	})
	// Add child BEFORE parent so engine creates child first (no parent external_ref yet).
	st.addIssue(&types.Issue{ID: "bd-child", Title: "Child Task", IssueType: types.TypeTask, Status: types.StatusOpen})
	st.addIssue(&types.Issue{ID: "bd-epic", Title: "New Epic", IssueType: types.TypeEpic, Status: types.StatusOpen})
	st.addParentChild("bd-child", "bd-epic")

	jt := &jira.Tracker{}
	if err := jt.Init(ctx, st); err != nil {
		t.Fatalf("jt.Init: %v", err)
	}
	pushHooks := buildJiraPushHooksForStore(ctx, st)
	engine := tracker.NewEngine(jt, st, "tester")
	engine.PushHooks = pushHooks
	opts := tracker.SyncOptions{Push: true, ParentID: "bd-epic"}

	res, err := engine.Sync(ctx, opts)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	reconcileJiraParentsForStore(ctx, st, jt, pushHooks, opts, nil, false, true, res)

	childStored := st.issues["bd-child"]
	epicStored := st.issues["bd-epic"]
	if childStored.ExternalRef == nil || epicStored.ExternalRef == nil {
		t.Fatalf("expected external_refs on both child and epic")
	}
	childKey := jt.ExtractIdentifier(*childStored.ExternalRef)
	epicKey := jt.ExtractIdentifier(*epicStored.ExternalRef)
	if childKey != "CORP-201" || epicKey != "CORP-202" {
		t.Fatalf("expected child=CORP-201 created before epic=CORP-202, got child=%s epic=%s", childKey, epicKey)
	}
	if mock.issues[childKey].Fields.Parent == nil || mock.issues[childKey].Fields.Parent.Key != epicKey {
		t.Fatalf("reconcile failed to wire %s -> %s, got %+v", childKey, epicKey, mock.issues[childKey].Fields.Parent)
	}

	// Now reparent bd-child in Beads to an existing epic CORP-199 and push via --issues bd-child.
	newEpicRef := srv.URL + "/browse/CORP-199"
	st.addIssue(&types.Issue{ID: "bd-epic-2", Title: "New Target Epic", IssueType: types.TypeEpic, Status: types.StatusOpen, ExternalRef: &newEpicRef})
	st.mu.Lock()
	st.deps["bd-child"] = []*types.Dependency{{IssueID: "bd-child", DependsOnID: "bd-epic-2", Type: types.DepParentChild}}
	st.mu.Unlock()

	issuesOpts := tracker.SyncOptions{Push: true, IssueIDs: []string{"bd-child"}}
	res2, err := engine.Sync(ctx, issuesOpts)
	if err != nil {
		t.Fatalf("reparent Sync: %v", err)
	}
	reconcileJiraParentsForStore(ctx, st, jt, pushHooks, issuesOpts, nil, false, true, res2)
	if mock.issues[childKey].Fields.Parent == nil || mock.issues[childKey].Fields.Parent.Key != "CORP-199" {
		t.Fatalf("reparent failed: %s parent = %+v, want CORP-199", childKey, mock.issues[childKey].Fields.Parent)
	}
}

func TestJiraSyncCreateOnlyAndUnresolvedParentWarnings(t *testing.T) {
	mock := newMockJiraRESTServer("CORP", 300)
	mock.seed("CORP-300", "Epic", "Epic 300", "", time.Now().UTC().Add(time.Hour))
	mock.seed("CORP-299", "Task", "Pre-existing linked child", "", time.Now().UTC().Add(time.Hour))
	srv := httptest.NewServer(mock)
	defer srv.Close()

	setupJiraTestTrustWorkspace(t, srv.URL)
	ctx := context.Background()

	epicRef := srv.URL + "/browse/CORP-300"
	existingChildRef := srv.URL + "/browse/CORP-299"
	st := newMemJiraTrackerStore(map[string]string{
		"jira.url":     srv.URL,
		"jira.project": "CORP",
	})
	st.addIssue(&types.Issue{ID: "bd-existing-child", Title: "Existing child", IssueType: types.TypeTask, Status: types.StatusOpen, ExternalRef: &existingChildRef})
	st.addIssue(&types.Issue{ID: "bd-unlinked-parent", Title: "Unlinked parent", IssueType: types.TypeEpic, Status: types.StatusOpen})
	st.addIssue(&types.Issue{ID: "bd-orphan-child", Title: "Child with unlinked parent", IssueType: types.TypeTask, Status: types.StatusOpen})
	st.addIssue(&types.Issue{ID: "bd-epic", Title: "Linked epic", IssueType: types.TypeEpic, Status: types.StatusOpen, ExternalRef: &epicRef})
	st.addParentChild("bd-existing-child", "bd-epic")
	st.addParentChild("bd-orphan-child", "bd-unlinked-parent")

	jt := &jira.Tracker{}
	if err := jt.Init(ctx, st); err != nil {
		t.Fatalf("jt.Init: %v", err)
	}
	pushHooks := buildJiraPushHooksForStore(ctx, st)
	engine := tracker.NewEngine(jt, st, "tester")
	engine.PushHooks = pushHooks

	// With --create-only --issues bd-existing-child,bd-orphan-child:
	// - bd-existing-child already existed before sync, so --create-only leaves CORP-299 untouched.
	// - bd-orphan-child is created, and because bd-unlinked-parent is not linked and out of scope, an actionable warning is emitted.
	opts := tracker.SyncOptions{
		Push:       true,
		CreateOnly: true,
		IssueIDs:   []string{"bd-existing-child", "bd-orphan-child"},
	}
	preLinked := snapshotJiraLinkedIssueIDs(ctx, st, jt)
	res, err := engine.Sync(ctx, opts)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	reconcileJiraParentsForStore(ctx, st, jt, pushHooks, opts, preLinked, false, true, res)

	if len(mock.putCalls["CORP-299"]) != 0 {
		t.Errorf("--create-only should not update pre-existing CORP-299, got PUTs: %+v", mock.putCalls["CORP-299"])
	}
	foundUnresolved := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "bd-orphan-child") && strings.Contains(w, "bd-unlinked-parent") && strings.Contains(w, "no Jira external_ref") {
			foundUnresolved = true
		}
	}
	if !foundUnresolved {
		t.Errorf("expected actionable unresolved-parent warning in %v", res.Warnings)
	}
}
