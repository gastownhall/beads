//go:build cgo

package tracker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// newTestStore creates a dolt store on the shared database with branch isolation.
func newTestStore(t *testing.T) *dolt.DoltStore {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping test")
	}
	if testServerPort == 0 || testSharedDB == "" {
		t.Skip("shared test Dolt database not initialized, skipping test")
	}
	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   testServerPort,
		Database:     testSharedDB,
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("Failed to create dolt store: %v", err)
	}

	// Create an isolated branch for this test
	_, branchCleanup := testutil.StartTestBranch(t, store.DB(), testSharedDB)

	// Re-create dolt_ignore'd tables on the branch
	if err := dolt.CreateIgnoredTables(store.DB()); err != nil {
		branchCleanup()
		store.Close()
		t.Fatalf("CreateIgnoredTables failed: %v", err)
	}

	t.Cleanup(func() {
		branchCleanup()
		store.Close()
	})
	return store
}

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

type mockExternalRefTracker struct {
	*mockTracker
	buildRef func(*TrackerIssue) string
	extract  func(string) string
	isRef    func(string) bool
}

type mockBatchTracker struct {
	*mockTracker
	batchResult   *BatchPushResult
	batchDryRun   *BatchPushResult
	batchErr      error
	batchCalls    int
	batchDryCalls int
	batchIssues   []*types.Issue
	batchForceIDs map[string]bool
}

func newMockTracker(name string) *mockTracker {
	return &mockTracker{
		name:        name,
		updated:     make(map[string]*types.Issue),
		fieldMapper: &mockMapper{},
	}
}

func (m *mockExternalRefTracker) IsExternalRef(ref string) bool {
	if m.isRef != nil {
		return m.isRef(ref)
	}
	return m.mockTracker.IsExternalRef(ref)
}

func (m *mockExternalRefTracker) ExtractIdentifier(ref string) string {
	if m.extract != nil {
		return m.extract(ref)
	}
	return m.mockTracker.ExtractIdentifier(ref)
}

func (m *mockExternalRefTracker) BuildExternalRef(issue *TrackerIssue) string {
	if m.buildRef != nil {
		return m.buildRef(issue)
	}
	return m.mockTracker.BuildExternalRef(issue)
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

func (m *mockBatchTracker) BatchPush(_ context.Context, issues []*types.Issue, forceIDs map[string]bool) (*BatchPushResult, error) {
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	m.batchCalls++
	m.batchIssues = append(m.batchIssues, issues...)
	m.batchForceIDs = forceIDs
	if m.batchResult != nil {
		return m.batchResult, nil
	}
	return &BatchPushResult{}, nil
}

func (m *mockBatchTracker) BatchPushDryRun(_ context.Context, issues []*types.Issue, forceIDs map[string]bool) (*BatchPushResult, error) {
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	m.batchDryCalls++
	m.batchIssues = append(m.batchIssues, issues...)
	m.batchForceIDs = forceIDs
	if m.batchDryRun != nil {
		return m.batchDryRun, nil
	}
	return &BatchPushResult{}, nil
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
	store := newTestStore(t)
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
	if result.PullStats.Created != 2 || result.PullStats.Updated != 0 {
		t.Errorf("PullStats = %+v, want Created=2 Updated=0", result.PullStats)
	}
	if result.PushStats.Created != 0 || result.PushStats.Updated != 0 {
		t.Errorf("PushStats = %+v, want zero value", result.PushStats)
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
	store := newTestStore(t)
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
	if result.PushStats.Created != 1 || result.PushStats.Updated != 0 {
		t.Errorf("PushStats = %+v, want Created=1 Updated=0", result.PushStats)
	}
	if result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
		t.Errorf("PullStats = %+v, want zero value", result.PullStats)
	}
	if len(tracker.created) != 1 {
		t.Errorf("tracker.created = %d, want 1", len(tracker.created))
	}
}

func TestEnginePushUsesBatchTrackerWhenAvailable(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	ref := strPtr("https://notion.so/existing")
	issues := []*types.Issue{
		{ID: "bd-batch-1", Title: "Create me", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2},
		{ID: "bd-batch-2", Title: "Update me", Status: types.StatusInProgress, IssueType: types.TypeFeature, Priority: 1, ExternalRef: ref},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
			t.Fatalf("CreateIssue(%s) error: %v", issue.ID, err)
		}
	}

	tracker := &mockBatchTracker{
		mockTracker: newMockTracker("notion"),
		batchResult: &BatchPushResult{
			Created:  []BatchPushItem{{LocalID: "bd-batch-1", ExternalRef: "https://notion.so/new-page"}},
			Updated:  []BatchPushItem{{LocalID: "bd-batch-2", ExternalRef: "https://notion.so/existing"}},
			Warnings: []string{"Skipped unsupported Notion issue types: pm=1"},
		},
	}
	engine := NewEngine(tracker, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if tracker.batchCalls != 1 {
		t.Fatalf("batchCalls = %d, want 1", tracker.batchCalls)
	}
	if len(tracker.created) != 0 || len(tracker.updated) != 0 {
		t.Fatalf("fallback create/update path should not run: created=%d updated=%d", len(tracker.created), len(tracker.updated))
	}
	if result.PushStats.Created != 1 || result.PushStats.Updated != 1 {
		t.Fatalf("PushStats = %+v", result.PushStats)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "pm=1") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	stored, err := store.GetIssue(ctx, "bd-batch-1")
	if err != nil {
		t.Fatalf("GetIssue() error: %v", err)
	}
	if stored.ExternalRef == nil || *stored.ExternalRef != "https://notion.so/new-page" {
		t.Fatalf("external_ref = %#v, want batch-created ref", stored.ExternalRef)
	}
	storedUpdated, err := store.GetIssue(ctx, "bd-batch-2")
	if err != nil {
		t.Fatalf("GetIssue() error: %v", err)
	}
	if storedUpdated.ExternalRef == nil || *storedUpdated.ExternalRef != "https://notion.so/existing" {
		t.Fatalf("updated external_ref = %#v, want batch-updated ref", storedUpdated.ExternalRef)
	}
}

func TestEngineDryRunUsesBatchPreviewWhenAvailable(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	issue := &types.Issue{
		ID:          "bd-batch-preview",
		Title:       "Preview me",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: strPtr("https://notion.so/existing"),
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := &mockBatchTracker{
		mockTracker: newMockTracker("notion"),
		batchDryRun: &BatchPushResult{
			Skipped:  []string{"bd-batch-preview"},
			Warnings: []string{"Skipped bd-batch-preview: Notion external_ref points outside the current target"},
		},
	}
	engine := NewEngine(tracker, store, "test-actor")
	var msgs []string
	var warns []string
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }
	engine.OnWarning = func(msg string) { warns = append(warns, msg) }

	result, err := engine.Sync(ctx, SyncOptions{Push: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if tracker.batchDryCalls != 1 {
		t.Fatalf("batchDryCalls = %d, want 1", tracker.batchDryCalls)
	}
	if tracker.batchCalls != 0 {
		t.Fatalf("batchCalls = %d, want 0", tracker.batchCalls)
	}
	if result.PushStats.Updated != 0 || result.PushStats.Created != 0 {
		t.Fatalf("PushStats = %+v, want zero create/update", result.PushStats)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "outside the current target") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	if len(msgs) != 0 {
		t.Fatalf("msgs = %#v, want no create/update preview lines", msgs)
	}
	if len(warns) != 0 {
		t.Fatalf("warns = %#v, want no immediate warning spam", warns)
	}
	stored, err := store.GetIssue(ctx, "bd-batch-preview")
	if err != nil {
		t.Fatalf("GetIssue() error: %v", err)
	}
	if stored.ExternalRef == nil || *stored.ExternalRef != "https://notion.so/existing" {
		t.Fatalf("external_ref mutated in dry-run: %#v", stored.ExternalRef)
	}
}

func TestEnginePushCountsCreateErrors(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	issue := &types.Issue{
		ID:        "bd-createerr1",
		Title:     "Local issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("test")
	tracker.createErr = fmt.Errorf("push response reported 1 error")
	engine := NewEngine(tracker, store, "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 0 {
		t.Errorf("Stats.Created = %d, want 0", result.Stats.Created)
	}
	if result.Stats.Errors != 1 {
		t.Errorf("Stats.Errors = %d, want 1", result.Stats.Errors)
	}

	stored, err := store.GetIssue(ctx, "bd-createerr1")
	if err != nil {
		t.Fatalf("GetIssue() error: %v", err)
	}
	if stored.ExternalRef != nil {
		t.Fatalf("external_ref = %q, want nil", *stored.ExternalRef)
	}
	if len(tracker.created) != 0 {
		t.Errorf("tracker.created = %d, want 0", len(tracker.created))
	}
}

func TestEngineDryRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
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

func TestEnginePullMatchesExistingIssueByExternalIdentifier(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	externalRef := strPtr("https://www.notion.so/0123456789abcdef0123456789abcdef")
	issue := &types.Issue{
		ID:          "bd-notion1",
		Title:       "Existing issue",
		Description: "old description",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: externalRef,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	base := newMockTracker("notion")
	base.issues = []TrackerIssue{
		{
			ID:          "01234567-89ab-cdef-0123-456789abcdef",
			Identifier:  "01234567-89ab-cdef-0123-456789abcdef",
			URL:         "https://www.notion.so/0123456789abcdef0123456789abcdef",
			Title:       "Existing issue updated",
			Description: "new description",
			UpdatedAt:   time.Now(),
		},
	}
	tracker := &mockExternalRefTracker{
		mockTracker: base,
		buildRef:    func(issue *TrackerIssue) string { return issue.URL },
		extract: func(ref string) string {
			ref = strings.TrimSpace(ref)
			if strings.HasPrefix(ref, "https://www.notion.so/") {
				ref = strings.TrimPrefix(ref, "https://www.notion.so/")
			}
			return strings.ToLower(strings.ReplaceAll(ref, "-", ""))
		},
		isRef: func(ref string) bool { return strings.HasPrefix(ref, "https://www.notion.so/") },
	}

	engine := NewEngine(tracker, store, "test-actor")
	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 0 {
		t.Errorf("Stats.Created = %d, want 0", result.Stats.Created)
	}
	if result.Stats.Updated != 1 {
		t.Errorf("Stats.Updated = %d, want 1", result.Stats.Updated)
	}

	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues() error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("stored issues = %d, want 1", len(issues))
	}
	if issues[0].ExternalRef == nil || *issues[0].ExternalRef != "https://www.notion.so/0123456789abcdef0123456789abcdef" {
		t.Fatalf("external_ref = %v, want canonical url", issues[0].ExternalRef)
	}
	if issues[0].Title != "Existing issue updated" {
		t.Fatalf("title = %q, want updated title", issues[0].Title)
	}
}

func TestEngineDryRunCountsExistingIssuesAsUpdates(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	extRef := strPtr("https://notion.test/EXT-1")
	issue := &types.Issue{
		ID:          "bd-existing1",
		Title:       "Existing issue",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: extRef,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("notion")
	tracker.issues = []TrackerIssue{
		{
			ID:         "EXT-1",
			Identifier: "EXT-1",
			URL:        "https://notion.test/EXT-1",
			Title:      "Existing issue updated",
			UpdatedAt:  time.Now(),
		},
	}

	var messages []string
	engine := NewEngine(tracker, store, "test-actor")
	engine.OnMessage = func(msg string) { messages = append(messages, msg) }

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.PullStats.Created != 0 {
		t.Errorf("PullStats.Created = %d, want 0", result.PullStats.Created)
	}
	if result.PullStats.Updated != 1 {
		t.Errorf("PullStats.Updated = %d, want 1", result.PullStats.Updated)
	}
	joined := strings.Join(messages, "\n")
	if strings.Contains(joined, "Would import") {
		t.Fatalf("messages = %q, want update preview without import", joined)
	}
	if !strings.Contains(joined, "Would update local issue") {
		t.Fatalf("messages = %q, want update preview", joined)
	}
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues() error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("stored issues = %d, want 1", len(issues))
	}
}

func TestEngineExcludeTypes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
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
	store := newTestStore(t)
	defer store.Close()

	// Set up last_sync (use UTC to avoid DATETIME timezone round-trip issues)
	lastSync := time.Now().UTC().Add(-1 * time.Hour)
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
		UpdatedAt:   time.Now().UTC().Add(-30 * time.Minute), // Modified 30 min ago
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
			UpdatedAt:  time.Now().UTC().Add(-15 * time.Minute), // Modified 15 min ago (newer)
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

func TestEngineSyncDoesNotCreateFalseConflictsAfterPull(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	lastSync := time.Now().UTC().Add(-1 * time.Hour)
	if err := store.SetConfig(ctx, "test.last_sync", lastSync.Format(time.RFC3339)); err != nil {
		t.Fatalf("SetConfig() error: %v", err)
	}

	issue := &types.Issue{
		ID:          "bd-sync1",
		Title:       "Local title",
		Description: "Local description",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: strPtr("https://test.test/EXT-1"),
		UpdatedAt:   lastSync.Add(-10 * time.Minute),
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{
			ID:          "EXT-1",
			Identifier:  "EXT-1",
			URL:         "https://test.test/EXT-1",
			Title:       "Remote title",
			Description: "Remote description",
			UpdatedAt:   lastSync.Add(30 * time.Minute),
		},
	}

	engine := NewEngine(tracker, store, "test-actor")
	result, err := engine.Sync(ctx, SyncOptions{Pull: true, Push: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", result.Stats.Conflicts)
	}
	if len(tracker.updated) != 0 {
		t.Fatalf("tracker.updated = %d, want 0", len(tracker.updated))
	}
	stored, err := store.GetIssue(ctx, "bd-sync1")
	if err != nil {
		t.Fatalf("GetIssue() error: %v", err)
	}
	if stored.Title != "Remote title" {
		t.Fatalf("title = %q, want remote title", stored.Title)
	}
}

func TestEnginePullWithShouldImport(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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

func TestEnginePushWithShouldPushCanInspectLabels(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	for _, tc := range []struct {
		id    string
		title string
		label string
	}{
		{id: "bd-labelpush1", title: "Push labeled", label: "notion-sync"},
		{id: "bd-labelskip1", title: "Skip unlabeled"},
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
		if tc.label != "" {
			if err := store.AddLabel(ctx, tc.id, tc.label, "test-actor"); err != nil {
				t.Fatalf("AddLabel(%s) error: %v", tc.id, err)
			}
		}
	}

	tracker := newMockTracker("test")
	engine := NewEngine(tracker, store, "test-actor")
	engine.PushHooks = &PushHooks{
		ShouldPush: func(issue *types.Issue) bool {
			for _, label := range issue.Labels {
				if label == "notion-sync" {
					return true
				}
			}
			return false
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
	if tracker.created[0].ID != "bd-labelpush1" {
		t.Fatalf("pushed issue ID = %q, want bd-labelpush1", tracker.created[0].ID)
	}
}

func TestEngineDryRunTreatsForeignExternalRefAsCreate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	foreignRef := "https://github.com/example/repo/issues/1"
	issue := &types.Issue{
		ID:          "bd-foreign1",
		Title:       "Mirror me to Notion",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: &foreignRef,
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := &mockExternalRefTracker{
		mockTracker: newMockTracker("notion"),
		isRef:       func(ref string) bool { return strings.Contains(ref, "notion.so") },
	}
	engine := NewEngine(tracker, store, "test-actor")

	var msgs []string
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }

	result, err := engine.Sync(ctx, SyncOptions{Push: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Fatalf("Stats.Created = %d, want 1", result.Stats.Created)
	}
	if result.Stats.Updated != 0 {
		t.Fatalf("Stats.Updated = %d, want 0", result.Stats.Updated)
	}
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "Would create in notion: Mirror me to Notion") {
		t.Fatalf("messages = %q, want create message", joined)
	}
	if strings.Contains(joined, "Would update in notion") {
		t.Fatalf("messages = %q, did not expect update message", joined)
	}

	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues() error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("stored issues = %d, want 1", len(issues))
	}
	if issues[0].ExternalRef == nil || *issues[0].ExternalRef != foreignRef {
		t.Fatalf("external_ref = %v, want %q", issues[0].ExternalRef, foreignRef)
	}
}

func TestEnginePushWithContentEqual(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
