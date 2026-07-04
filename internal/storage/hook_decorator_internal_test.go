package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/types"
)

type recordingHookRunner struct {
	events []string
	issues []*types.Issue
}

func (r *recordingHookRunner) Run(event string, issue *types.Issue) {
	r.events = append(r.events, event)
	r.issues = append(r.issues, issue)
}

type fakeHookStore struct {
	DoltStorage
	issues           map[string]*types.Issue
	dropDependencies bool
}

func (s fakeHookStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	if s.issues != nil {
		s.issues[issue.ID] = cloneForFakeHookStore(issue, s.dropDependencies)
	}
	return nil
}

func (s fakeHookStore) CreateIssues(_ context.Context, issues []*types.Issue, _ string) error {
	for _, issue := range issues {
		if s.issues != nil {
			s.issues[issue.ID] = cloneForFakeHookStore(issue, s.dropDependencies)
		}
	}
	return nil
}

func (s fakeHookStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	issue, ok := s.issues[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return cloneIssueForHook(issue), nil
}

func (s fakeHookStore) GetDependencyRecords(_ context.Context, id string) ([]*types.Dependency, error) {
	issue, ok := s.issues[id]
	if !ok {
		return nil, errors.New("not found")
	}
	if s.dropDependencies {
		return nil, nil
	}
	return cloneDependenciesForHook(issue.Dependencies), nil
}

func (s fakeHookStore) RunInTransaction(ctx context.Context, _ string, fn func(tx Transaction) error) error {
	return fn(fakeHookTransaction{issues: s.issues, dropDependencies: s.dropDependencies})
}

type fakeHookTransaction struct {
	Transaction
	issues           map[string]*types.Issue
	dropDependencies bool
}

func (tx fakeHookTransaction) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	if tx.issues != nil {
		tx.issues[issue.ID] = cloneForFakeHookStore(issue, tx.dropDependencies)
	}
	return nil
}

func (tx fakeHookTransaction) CreateIssues(_ context.Context, issues []*types.Issue, _ string) error {
	for _, issue := range issues {
		if tx.issues != nil {
			tx.issues[issue.ID] = cloneForFakeHookStore(issue, tx.dropDependencies)
		}
	}
	return nil
}

func (tx fakeHookTransaction) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	issue, ok := tx.issues[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return cloneIssueForHook(issue), nil
}

func (tx fakeHookTransaction) GetDependencyRecords(_ context.Context, id string) ([]*types.Dependency, error) {
	issue, ok := tx.issues[id]
	if !ok {
		return nil, errors.New("not found")
	}
	if tx.dropDependencies {
		return nil, nil
	}
	return cloneDependenciesForHook(issue.Dependencies), nil
}

func cloneForFakeHookStore(issue *types.Issue, dropDependencies bool) *types.Issue {
	clone := cloneIssueForHook(issue)
	if dropDependencies {
		clone.Dependencies = nil
		return clone
	}
	for _, dep := range clone.Dependencies {
		if dep != nil && dep.IssueID == "" {
			dep.IssueID = clone.ID
		}
	}
	return clone
}

func TestCreateHookEventsIncludeSyntheticLabelUpdates(t *testing.T) {
	issue := &types.Issue{ID: "hooked-issue", Labels: []string{"a", "b"}}

	got := createHookEvents(issue)
	gotEvents := make([]string, len(got))
	for i, event := range got {
		gotEvents[i] = event.event
	}

	wantEvents := []string{hooks.EventCreate, hooks.EventUpdate, hooks.EventUpdate}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Fatalf("events = %v, want %v", gotEvents, wantEvents)
	}
	wantLabels := [][]string{nil, []string{"a"}, []string{"a", "b"}}
	for i, want := range wantLabels {
		if !reflect.DeepEqual(got[i].issue.Labels, want) {
			t.Fatalf("event %d labels = %v, want %v", i, got[i].issue.Labels, want)
		}
	}
	if !reflect.DeepEqual(issue.Labels, []string{"a", "b"}) {
		t.Fatalf("source issue labels mutated to %v", issue.Labels)
	}
}

func TestCreateHookEventsDedupesLabels(t *testing.T) {
	issue := &types.Issue{ID: "hooked-issue", Labels: []string{"a", "a", "b"}}

	got := createHookEvents(issue)
	gotEvents := make([]string, len(got))
	for i, event := range got {
		gotEvents[i] = event.event
	}

	wantEvents := []string{hooks.EventCreate, hooks.EventUpdate, hooks.EventUpdate}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Fatalf("events = %v, want %v", gotEvents, wantEvents)
	}
	wantLabels := [][]string{nil, []string{"a"}, []string{"a", "b"}}
	for i, want := range wantLabels {
		if !reflect.DeepEqual(got[i].issue.Labels, want) {
			t.Fatalf("event %d labels = %v, want %v", i, got[i].issue.Labels, want)
		}
	}
	if !reflect.DeepEqual(issue.Labels, []string{"a", "a", "b"}) {
		t.Fatalf("source issue labels mutated to %v", issue.Labels)
	}
}

func TestCreateHookEventsCloneNoLabelIssue(t *testing.T) {
	closedAt := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	issue := &types.Issue{
		ID:       "hooked-issue",
		Metadata: []byte(`{"key":"value"}`),
		ClosedAt: &closedAt,
	}

	got := createHookEvents(issue)
	if len(got) != 1 {
		t.Fatalf("event count = %d, want 1", len(got))
	}
	snapshot := got[0].issue
	if snapshot == issue {
		t.Fatalf("create hook reused source issue pointer")
	}
	if len(snapshot.Metadata) == 0 {
		t.Fatalf("snapshot metadata is empty")
	}
	snapshot.Metadata[0] = '['
	if string(issue.Metadata) != `{"key":"value"}` {
		t.Fatalf("source metadata mutated to %s", issue.Metadata)
	}
	if snapshot.ClosedAt == issue.ClosedAt {
		t.Fatalf("snapshot ClosedAt shares source pointer")
	}
	snapshot.ClosedAt = nil
	if issue.ClosedAt == nil {
		t.Fatalf("source ClosedAt mutated to nil")
	}
}

func TestCloneIssueForHookCopiesReferenceFields(t *testing.T) {
	estimatedMinutes := 15
	startedAt := time.Date(2026, 5, 22, 10, 1, 0, 0, time.UTC)
	closedAt := time.Date(2026, 5, 22, 10, 2, 0, 0, time.UTC)
	dueAt := time.Date(2026, 5, 22, 10, 3, 0, 0, time.UTC)
	deferUntil := time.Date(2026, 5, 22, 10, 4, 0, 0, time.UTC)
	externalRef := "gh:owner/repo#1"
	compactedAt := time.Date(2026, 5, 22, 10, 5, 0, 0, time.UTC)
	compactedAtCommit := "abc123"
	issue := &types.Issue{
		ID:                "hooked-issue",
		EstimatedMinutes:  &estimatedMinutes,
		StartedAt:         &startedAt,
		ClosedAt:          &closedAt,
		DueAt:             &dueAt,
		DeferUntil:        &deferUntil,
		ExternalRef:       &externalRef,
		Metadata:          []byte(`{"key":"value"}`),
		CompactedAt:       &compactedAt,
		CompactedAtCommit: &compactedAtCommit,
		Labels:            []string{"alpha"},
		Dependencies: []*types.Dependency{{
			IssueID:     "hooked-issue",
			DependsOnID: "target",
			Type:        types.DepBlocks,
		}},
		Comments: []*types.Comment{{
			ID:     "1",
			Author: "tester",
			Text:   "note",
		}},
		BondedFrom: []types.BondRef{{SourceID: "proto-1", BondType: "sequential"}},
		Waiters:    []string{"agent@example.com"},
	}

	snapshot := cloneIssueForHook(issue)
	if snapshot.EstimatedMinutes == issue.EstimatedMinutes ||
		snapshot.StartedAt == issue.StartedAt ||
		snapshot.ClosedAt == issue.ClosedAt ||
		snapshot.DueAt == issue.DueAt ||
		snapshot.DeferUntil == issue.DeferUntil ||
		snapshot.ExternalRef == issue.ExternalRef ||
		snapshot.CompactedAt == issue.CompactedAt ||
		snapshot.CompactedAtCommit == issue.CompactedAtCommit {
		t.Fatalf("clone shares pointer fields with source issue")
	}
	snapshot.Metadata[0] = '['
	snapshot.Labels[0] = "beta"
	snapshot.Dependencies[0].DependsOnID = "other-target"
	snapshot.Comments[0].Text = "changed"
	snapshot.BondedFrom[0].SourceID = "proto-2"
	snapshot.Waiters[0] = "other@example.com"

	if string(issue.Metadata) != `{"key":"value"}` ||
		issue.Labels[0] != "alpha" ||
		issue.Dependencies[0].DependsOnID != "target" ||
		issue.Comments[0].Text != "note" ||
		issue.BondedFrom[0].SourceID != "proto-1" ||
		issue.Waiters[0] != "agent@example.com" {
		t.Fatalf("mutating clone changed source issue")
	}
}

func TestCloneIssueForHookCoversReferenceFields(t *testing.T) {
	copiedFields := map[string]struct{}{
		"EstimatedMinutes":  {},
		"StartedAt":         {},
		"ClosedAt":          {},
		"DueAt":             {},
		"DeferUntil":        {},
		"LeaseExpiresAt":    {},
		"HeartbeatAt":       {},
		"ExternalRef":       {},
		"Metadata":          {},
		"CompactedAt":       {},
		"CompactedAtCommit": {},
		"Labels":            {},
		"Dependencies":      {},
		"Comments":          {},
		"BondedFrom":        {},
		"Waiters":           {},
	}
	issueType := reflect.TypeOf(types.Issue{})
	for i := 0; i < issueType.NumField(); i++ {
		field := issueType.Field(i)
		switch field.Type.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Map:
			if _, ok := copiedFields[field.Name]; !ok {
				t.Fatalf("reference field %s must be cloned by cloneIssueForHook", field.Name)
			}
		}
	}
}

func TestHookFiringStoreCreateIssueFiresInitialLabelUpdates(t *testing.T) {
	runner := &recordingHookRunner{}
	inner := fakeHookStore{}
	store := &HookFiringStore{DoltStorage: inner, inner: inner, runner: runner}
	issue := &types.Issue{ID: "hooked-issue", Labels: []string{"a", "b"}}

	if err := store.CreateIssue(context.Background(), issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	wantEvents := []string{hooks.EventCreate, hooks.EventUpdate, hooks.EventUpdate}
	if !reflect.DeepEqual(runner.events, wantEvents) {
		t.Fatalf("events = %v, want %v", runner.events, wantEvents)
	}
	wantLabels := [][]string{nil, []string{"a"}, []string{"a", "b"}}
	for i, want := range wantLabels {
		if !reflect.DeepEqual(runner.issues[i].Labels, want) {
			t.Fatalf("event %d labels = %v, want %v", i, runner.issues[i].Labels, want)
		}
	}
}

func TestHookFiringStoreTransactionCreateIssueFiresInitialLabelUpdates(t *testing.T) {
	runner := &recordingHookRunner{}
	inner := fakeHookStore{}
	store := &HookFiringStore{DoltStorage: inner, inner: inner, runner: runner}
	issue := &types.Issue{ID: "tx-hooked-issue", Labels: []string{"a", "b"}}

	err := store.RunInTransaction(context.Background(), "test", func(tx Transaction) error {
		return tx.CreateIssue(context.Background(), issue, "tester")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}

	wantEvents := []string{hooks.EventCreate, hooks.EventUpdate, hooks.EventUpdate}
	if !reflect.DeepEqual(runner.events, wantEvents) {
		t.Fatalf("events = %v, want %v", runner.events, wantEvents)
	}
	wantLabels := [][]string{nil, []string{"a"}, []string{"a", "b"}}
	for i, want := range wantLabels {
		if !reflect.DeepEqual(runner.issues[i].Labels, want) {
			t.Fatalf("event %d labels = %v, want %v", i, runner.issues[i].Labels, want)
		}
	}
}

func TestHookFiringStoreCreateIssuesFiresDependencyUpdates(t *testing.T) {
	runner := &recordingHookRunner{}
	inner := fakeHookStore{issues: map[string]*types.Issue{}}
	store := &HookFiringStore{DoltStorage: inner, inner: inner, runner: runner}
	source := &types.Issue{
		ID:        "source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{
			{
				DependsOnID: "target-a",
				Type:        types.DepBlocks,
			},
			{
				DependsOnID: "target-b",
				Type:        types.DepBlocks,
			},
		},
	}
	targetA := &types.Issue{ID: "target-a", IssueType: types.TypeTask}
	targetB := &types.Issue{ID: "target-b", IssueType: types.TypeTask}

	if err := store.CreateIssues(context.Background(), []*types.Issue{source, targetA, targetB}, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}

	wantEvents := []string{hooks.EventCreate, hooks.EventCreate, hooks.EventCreate, hooks.EventUpdate, hooks.EventUpdate}
	if !reflect.DeepEqual(runner.events, wantEvents) {
		t.Fatalf("events = %v, want %v", runner.events, wantEvents)
	}
	if got := len(runner.issues[3].Dependencies); got != 1 {
		t.Fatalf("first dependency update dependency count = %d, want 1", got)
	}
	if got := len(runner.issues[4].Dependencies); got != 2 {
		t.Fatalf("second dependency update dependency count = %d, want 2", got)
	}
	if runner.issues[3].Dependencies[0].DependsOnID != "target-a" {
		t.Fatalf("first dependency update target = %q, want target-a", runner.issues[3].Dependencies[0].DependsOnID)
	}
	if runner.issues[4].Dependencies[1].DependsOnID != "target-b" {
		t.Fatalf("second dependency update target = %q, want target-b", runner.issues[4].Dependencies[1].DependsOnID)
	}
}

func TestHookFiringStoreTransactionCreateIssuesFiresDependencyUpdates(t *testing.T) {
	runner := &recordingHookRunner{}
	inner := fakeHookStore{issues: map[string]*types.Issue{}}
	store := &HookFiringStore{DoltStorage: inner, inner: inner, runner: runner}
	source := &types.Issue{
		ID:        "source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "target",
			Type:        types.DepBlocks,
		}},
	}
	target := &types.Issue{ID: "target", IssueType: types.TypeTask}

	err := store.RunInTransaction(context.Background(), "test", func(tx Transaction) error {
		return tx.CreateIssues(context.Background(), []*types.Issue{source, target}, "tester")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}

	wantEvents := []string{hooks.EventCreate, hooks.EventCreate, hooks.EventUpdate}
	if !reflect.DeepEqual(runner.events, wantEvents) {
		t.Fatalf("events = %v, want %v", runner.events, wantEvents)
	}
	if runner.issues[2].ID != source.ID {
		t.Fatalf("dependency update issue ID = %q, want %q", runner.issues[2].ID, source.ID)
	}
}

func TestHookFiringStoreCreateIssuesSkipsUnpersistedDependencies(t *testing.T) {
	runner := &recordingHookRunner{}
	inner := fakeHookStore{
		issues:           map[string]*types.Issue{},
		dropDependencies: true,
	}
	store := &HookFiringStore{DoltStorage: inner, inner: inner, runner: runner}
	source := &types.Issue{
		ID:        "source",
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "missing-target",
			Type:        types.DepBlocks,
		}},
	}
	target := &types.Issue{ID: "target", IssueType: types.TypeTask}

	if err := store.CreateIssues(context.Background(), []*types.Issue{source, target}, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}

	wantEvents := []string{hooks.EventCreate, hooks.EventCreate}
	if !reflect.DeepEqual(runner.events, wantEvents) {
		t.Fatalf("events = %v, want %v", runner.events, wantEvents)
	}
}

func TestNewHookFiringStoreNilRunnerSkipsCreateHooks(t *testing.T) {
	var runner *hooks.Runner
	inner := fakeHookStore{}
	store := NewHookFiringStore(inner, runner)
	issue := &types.Issue{ID: "nil-runner-issue", Labels: []string{"a"}}

	if err := store.CreateIssue(context.Background(), issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
}
