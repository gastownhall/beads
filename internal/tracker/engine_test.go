package tracker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

// mockTracker implements IssueTracker for testing.
type mockTracker struct {
	name        string
	issues      []TrackerIssue
	created     []*types.Issue
	updated     map[string]*types.Issue
	fetchErr    error
	createErr   error
	updateErr   error
	fieldMapper FieldMapper
}

func newMockTracker(name string) *mockTracker {
	return &mockTracker{
		name:        name,
		updated:     make(map[string]*types.Issue),
		fieldMapper: &mockMapper{},
	}
}

func (m *mockTracker) Name() string                                    { return m.name }
func (m *mockTracker) DisplayName() string                             { return m.name }
func (m *mockTracker) ConfigPrefix() string                            { return m.name }
func (m *mockTracker) Init(_ context.Context, _ storage.Storage) error { return nil }
func (m *mockTracker) Validate() error                                 { return nil }
func (m *mockTracker) Close() error                                    { return nil }
func (m *mockTracker) FieldMapper() FieldMapper                        { return m.fieldMapper }
func (m *mockTracker) IsExternalRef(ref string) bool                   { return len(ref) > 0 }
func (m *mockTracker) ExtractIdentifier(ref string) string {
	// Extract "EXT-1" from "https://test.test/EXT-1"
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}
func (m *mockTracker) BuildExternalRef(issue *TrackerIssue) string {
	return fmt.Sprintf("https://%s.test/%s", m.name, issue.Identifier)
}

func (m *mockTracker) FetchIssues(_ context.Context, _ FetchOptions) ([]TrackerIssue, error) {
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	return m.issues, nil
}

func (m *mockTracker) FetchIssue(_ context.Context, identifier string) (*TrackerIssue, error) {
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	for i := range m.issues {
		if m.issues[i].Identifier == identifier {
			return &m.issues[i], nil
		}
	}
	return nil, nil
}

func (m *mockTracker) CreateIssue(_ context.Context, issue *types.Issue) (*TrackerIssue, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.created = append(m.created, issue)
	return &TrackerIssue{
		ID:         "ext-" + issue.ID,
		Identifier: "EXT-" + issue.ID,
		URL:        fmt.Sprintf("https://%s.test/EXT-%s", m.name, issue.ID),
		Title:      issue.Title,
	}, nil
}

func (m *mockTracker) UpdateIssue(_ context.Context, externalID string, issue *types.Issue) (*TrackerIssue, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	m.updated[externalID] = issue
	return &TrackerIssue{
		ID:         externalID,
		Identifier: externalID,
		Title:      issue.Title,
	}, nil
}

// mockMapper implements FieldMapper for testing.
type mockMapper struct{}

func (m *mockMapper) PriorityToBeads(p interface{}) int {
	if v, ok := p.(int); ok {
		return v
	}
	return 2
}
func (m *mockMapper) PriorityToTracker(p int) interface{}         { return p }
func (m *mockMapper) StatusToBeads(_ interface{}) types.Status    { return types.StatusOpen }
func (m *mockMapper) StatusToTracker(s types.Status) interface{}  { return string(s) }
func (m *mockMapper) TypeToBeads(_ interface{}) types.IssueType   { return types.TypeTask }
func (m *mockMapper) TypeToTracker(t types.IssueType) interface{} { return string(t) }
func (m *mockMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	return map[string]interface{}{
		"title":       issue.Title,
		"description": issue.Description,
	}
}

func (m *mockMapper) IssueToBeads(ti *TrackerIssue) *IssueConversion {
	return &IssueConversion{
		Issue: &types.Issue{
			Title:       ti.Title,
			Description: ti.Description,
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
		},
	}
}

func TestEnginePullOnly(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "1", Identifier: "TEST-1", Title: "First issue", Description: "Desc 1", UpdatedAt: time.Now()},
		{ID: "2", Identifier: "TEST-2", Title: "Second issue", Description: "Desc 2", UpdatedAt: time.Now()},
	}

	engine := NewEngine(tracker, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if !result.Success {
		t.Errorf("Sync() not successful: %s", result.Error)
	}
	if result.Stats.Created != 2 {
		t.Errorf("Stats.Created = %d, want 2", result.Stats.Created)
	}

	// Verify issues were stored
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues() error: %v", err)
	}
	if len(issues) != 2 {
		t.Errorf("stored %d issues, want 2", len(issues))
	}
}

func TestEnginePushOnly(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	// Create a local issue
	issue := &types.Issue{
		ID:        "bd-test1",
		Title:     "Local issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if !result.Success {
		t.Errorf("Sync() not successful: %s", result.Error)
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1", result.Stats.Created)
	}
	if len(tracker.created) != 1 {
		t.Errorf("tracker.created = %d, want 1", len(tracker.created))
	}
}

func TestEngineDryRun(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "1", Identifier: "TEST-1", Title: "Issue", UpdatedAt: time.Now()},
	}

	engine := NewEngine(tracker, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if !result.Success {
		t.Errorf("Sync() not successful: %s", result.Error)
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1 (dry-run counted)", result.Stats.Created)
	}

	// Verify nothing was actually stored
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues() error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("stored %d issues in dry-run, want 0", len(issues))
	}
}

func TestEngineExcludeTypes(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	// Create issues of different types
	for _, tc := range []struct {
		id  string
		typ types.IssueType
	}{
		{"bd-task1", types.TypeTask},
		{"bd-bug1", types.TypeBug},
		{"bd-feat1", types.TypeFeature},
	} {
		issue := &types.Issue{
			ID:        tc.id,
			Title:     "Issue " + tc.id,
			Status:    types.StatusOpen,
			IssueType: tc.typ,
			Priority:  2,
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue(%s) error: %v", tc.id, err)
		}
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")

	// Push excluding bugs
	result, err := engine.Sync(ctx, SyncOptions{Push: true, ExcludeTypes: []types.IssueType{types.TypeBug}})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if !result.Success {
		t.Errorf("Sync() not successful: %s", result.Error)
	}
	if len(tracker.created) != 2 {
		t.Errorf("created %d issues (excluding bugs), want 2", len(tracker.created))
	}
}

func TestEngineConflictResolution(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	// Set up last_sync
	lastSync := time.Now().Add(-1 * time.Hour)
	if err := store.SetConfig(ctx, "test.last_sync", lastSync.Format(time.RFC3339)); err != nil {
		t.Fatalf("SetConfig() error: %v", err)
	}

	// Create a local issue that was modified after last_sync
	issue := &types.Issue{
		ID:          "bd-conflict1",
		Title:       "Local version",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: strPtr("https://test.test/EXT-1"),
		UpdatedAt:   time.Now().Add(-30 * time.Minute), // Modified 30 min ago
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	// Set up tracker with an external issue also modified after last_sync
	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{
			ID:         "EXT-1",
			Identifier: "EXT-1",
			Title:      "External version",
			UpdatedAt:  time.Now().Add(-15 * time.Minute), // Modified 15 min ago (newer)
		},
	}

	engine := NewEngine(tracker, store, "test-actor")

	// Detect conflicts
	conflicts, err := engine.DetectConflicts(ctx)
	if err != nil {
		t.Fatalf("DetectConflicts() error: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("detected %d conflicts, want 1", len(conflicts))
	}
	if conflicts[0].IssueID != "bd-conflict1" {
		t.Errorf("conflict issue ID = %q, want %q", conflicts[0].IssueID, "bd-conflict1")
	}
}

func TestEnginePullWithShouldImport(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "1", Identifier: "TEST-1", Title: "Keep this", UpdatedAt: time.Now()},
		{ID: "2", Identifier: "TEST-2", Title: "Skip this", UpdatedAt: time.Now()},
		{ID: "3", Identifier: "TEST-3", Title: "Keep this too", UpdatedAt: time.Now()},
	}

	engine := NewEngine(tracker, store, "test-actor")
	engine.PullHooks = &PullHooks{
		ShouldImport: func(issue *TrackerIssue) bool {
			return issue.Title != "Skip this"
		},
	}

	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 2 {
		t.Errorf("Stats.Created = %d, want 2", result.Stats.Created)
	}
	if result.Stats.Skipped != 1 {
		t.Errorf("Stats.Skipped = %d, want 1", result.Stats.Skipped)
	}

	issues, _ := store.SearchIssues(ctx, "", types.IssueFilter{})
	if len(issues) != 2 {
		t.Errorf("stored %d issues, want 2", len(issues))
	}
}

func TestEnginePullWithTransformHook(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "1", Identifier: "TEST-1", Title: "Issue one", Description: "raw desc", UpdatedAt: time.Now()},
	}

	engine := NewEngine(tracker, store, "test-actor")
	engine.PullHooks = &PullHooks{
		TransformIssue: func(issue *types.Issue) {
			issue.Description = "transformed: " + issue.Description
		},
	}

	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1", result.Stats.Created)
	}

	issues, _ := store.SearchIssues(ctx, "", types.IssueFilter{})
	if len(issues) != 1 {
		t.Fatalf("stored %d issues, want 1", len(issues))
	}
	if issues[0].Description != "transformed: raw desc" {
		t.Errorf("description = %q, want %q", issues[0].Description, "transformed: raw desc")
	}
}

func TestEnginePullWithGenerateID(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "1", Identifier: "TEST-1", Title: "Issue one", UpdatedAt: time.Now()},
	}

	engine := NewEngine(tracker, store, "test-actor")
	engine.PullHooks = &PullHooks{
		GenerateID: func(_ context.Context, issue *types.Issue) error {
			issue.ID = "bd-custom-id-123"
			return nil
		},
	}

	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1", result.Stats.Created)
	}

	// Verify the custom ID was used
	issues, _ := store.SearchIssues(ctx, "", types.IssueFilter{})
	if len(issues) != 1 {
		t.Fatalf("stored %d issues, want 1", len(issues))
	}
	if issues[0].ID != "bd-custom-id-123" {
		t.Errorf("issue ID = %q, want %q", issues[0].ID, "bd-custom-id-123")
	}
}

func TestEnginePullWithGenerateIDError(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "1", Identifier: "TEST-1", Title: "Good issue", UpdatedAt: time.Now()},
		{ID: "2", Identifier: "TEST-2", Title: "Bad issue", UpdatedAt: time.Now()},
	}

	engine := NewEngine(tracker, store, "test-actor")
	engine.PullHooks = &PullHooks{
		GenerateID: func(_ context.Context, issue *types.Issue) error {
			if issue.Title == "Bad issue" {
				return fmt.Errorf("collision detected")
			}
			issue.ID = "bd-good-1"
			return nil
		},
	}

	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1", result.Stats.Created)
	}
	if result.Stats.Skipped != 1 {
		t.Errorf("Stats.Skipped = %d, want 1 (GenerateID error should skip)", result.Stats.Skipped)
	}
}

func TestEnginePushWithFormatDescription(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	issue := &types.Issue{
		ID:          "bd-fmt1",
		Title:       "Issue with design",
		Description: "Base description",
		Design:      "Some design notes",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")
	engine.PushHooks = &PushHooks{
		FormatDescription: func(issue *types.Issue) string {
			return issue.Description + "\n\n## Design\n" + issue.Design
		},
	}

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1", result.Stats.Created)
	}
	if len(tracker.created) != 1 {
		t.Fatalf("tracker.created = %d, want 1", len(tracker.created))
	}
	// The issue sent to the tracker should have the formatted description
	if tracker.created[0].Description != "Base description\n\n## Design\nSome design notes" {
		t.Errorf("pushed description = %q, want formatted version", tracker.created[0].Description)
	}

	// Verify the local issue was NOT mutated
	localIssue, _ := store.GetIssue(ctx, "bd-fmt1")
	if localIssue.Description != "Base description" {
		t.Errorf("local description was mutated to %q, should still be %q", localIssue.Description, "Base description")
	}
}

func TestEnginePushWithShouldPush(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	// Create two local issues
	for _, tc := range []struct {
		id    string
		title string
	}{
		{"bd-push1", "Push this"},
		{"bd-skip1", "Skip this"},
	} {
		issue := &types.Issue{
			ID:        tc.id,
			Title:     tc.title,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			Priority:  2,
		}
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue(%s) error: %v", tc.id, err)
		}
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")
	engine.PushHooks = &PushHooks{
		ShouldPush: func(issue *types.Issue) bool {
			return !strings.HasPrefix(issue.ID, "bd-skip")
		},
	}

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1", result.Stats.Created)
	}
	if len(tracker.created) != 1 {
		t.Errorf("tracker.created = %d, want 1", len(tracker.created))
	}
}

func TestEnginePushWithContentEqual(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	// Create a local issue that already exists externally
	issue := &types.Issue{
		ID:          "bd-eq1",
		Title:       "Identical content",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: strPtr("https://test.test/EXT-EQ1"),
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{
			ID:         "EXT-EQ1",
			Identifier: "EXT-EQ1",
			Title:      "Identical content",
			UpdatedAt:  time.Now().Add(-1 * time.Hour), // older, would normally trigger update
		},
	}

	engine := NewEngine(tracker, store, "test-actor")
	engine.PushHooks = &PushHooks{
		ContentEqual: func(local *types.Issue, remote *TrackerIssue) bool {
			// Content-hash dedup: titles match = identical
			return local.Title == remote.Title
		},
	}

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	// Should be skipped because ContentEqual returns true
	if result.Stats.Updated != 0 {
		t.Errorf("Stats.Updated = %d, want 0 (content equal should skip)", result.Stats.Updated)
	}
	if len(tracker.updated) != 0 {
		t.Errorf("tracker.updated = %d, want 0", len(tracker.updated))
	}
}

func TestEnginePushExcludeEphemeral(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	// Create a normal issue and an ephemeral one
	normal := &types.Issue{
		ID:        "bd-normal1",
		Title:     "Normal issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	ephemeral := &types.Issue{
		ID:        "bd-wisp-eph1",
		Title:     "Ephemeral wisp",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, normal, "test-actor"); err != nil {
		t.Fatalf("CreateIssue(normal) error: %v", err)
	}
	if err := store.CreateIssue(ctx, ephemeral, "test-actor"); err != nil {
		t.Fatalf("CreateIssue(ephemeral) error: %v", err)
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")

	// With ExcludeEphemeral: only the normal issue should be pushed
	result, err := engine.Sync(ctx, SyncOptions{Push: true, ExcludeEphemeral: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1", result.Stats.Created)
	}
	if len(tracker.created) != 1 {
		t.Errorf("tracker.created = %d, want 1", len(tracker.created))
	}
	if tracker.created[0].Title != "Normal issue" {
		t.Errorf("pushed issue title = %q, want %q", tracker.created[0].Title, "Normal issue")
	}
}

func TestEnginePushWithStateCache(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	defer store.Close()

	issue := &types.Issue{
		ID:        "bd-state1",
		Title:     "Issue with state",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")

	buildCacheCalled := false
	engine.PushHooks = &PushHooks{
		BuildStateCache: func(ctx context.Context) (interface{}, error) {
			buildCacheCalled = true
			return map[types.Status]string{
				types.StatusOpen:   "state-open-id",
				types.StatusClosed: "state-closed-id",
			}, nil
		},
		ResolveState: func(cache interface{}, status types.Status) (string, bool) {
			m := cache.(map[types.Status]string)
			id, ok := m[status]
			return id, ok
		},
	}

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if !buildCacheCalled {
		t.Error("BuildStateCache was not called")
	}
	if result.Stats.Created != 1 {
		t.Errorf("Stats.Created = %d, want 1", result.Stats.Created)
	}

	// Verify ResolveState works via the Engine method
	stateID, ok := engine.ResolveState(types.StatusOpen)
	if !ok || stateID != "state-open-id" {
		t.Errorf("ResolveState(Open) = (%q, %v), want (%q, true)", stateID, ok, "state-open-id")
	}
	stateID, ok = engine.ResolveState(types.StatusClosed)
	if !ok || stateID != "state-closed-id" {
		t.Errorf("ResolveState(Closed) = (%q, %v), want (%q, true)", stateID, ok, "state-closed-id")
	}
}
