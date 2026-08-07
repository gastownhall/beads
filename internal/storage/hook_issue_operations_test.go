package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeIssueOperations records which verbs reached it and can fail or report an
// unchanged result on demand.
type fakeIssueOperations struct {
	calls   []string
	changed bool
	err     error
}

func (f *fakeIssueOperations) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	f.calls = append(f.calls, "create")
	return issueops.CreateResult{}, f.err
}
func (f *fakeIssueOperations) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	f.calls = append(f.calls, "update")
	return issueops.UpdateResult{Changed: f.changed}, f.err
}
func (f *fakeIssueOperations) Close(context.Context, issueops.CloseRequest) (issueops.CloseResult, error) {
	f.calls = append(f.calls, "close")
	return issueops.CloseResult{Changed: f.changed}, f.err
}
func (f *fakeIssueOperations) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	f.calls = append(f.calls, "reopen")
	return issueops.ReopenResult{Changed: f.changed}, f.err
}

// recordingIssueOperationHooks records which completion hooks hookIssueOperations
// fired, so tests can assert the per-verb firing rules without a hook runner.
type recordingIssueOperationHooks struct {
	completions []string
}

func (r *recordingIssueOperationHooks) CompleteIssueOperationCreate(context.Context, *types.Issue, []*types.Dependency) {
	r.completions = append(r.completions, "create")
}
func (r *recordingIssueOperationHooks) CompleteIssueOperationUpdate(*types.Issue) {
	r.completions = append(r.completions, "update")
}
func (r *recordingIssueOperationHooks) CompleteIssueOperationClose(*types.Issue) {
	r.completions = append(r.completions, "close")
}
func (r *recordingIssueOperationHooks) CompleteIssueOperationDependency(_ context.Context, issueID string) {
	r.completions = append(r.completions, "dependency:"+issueID)
}
func (r *recordingIssueOperationHooks) CompleteIssueOperationComment(_ context.Context, issueID string) {
	r.completions = append(r.completions, "comment:"+issueID)
}

// lifecycleStore is a DoltStorage whose only real method is IssueLifecycle.
type lifecycleStore struct {
	DoltStorage
	lifecycle issueops.Lifecycle
	err       error
}

func (s lifecycleStore) IssueLifecycle() (issueops.Lifecycle, error) { return s.lifecycle, s.err }

// TestHookFiringStoreIssueLifecycleLayersHooksOverInner pins the recursion.
// Delegating to the inner store instead would still compile and still satisfy
// Storage, and every guarded write would silently stop firing hooks.
func TestHookFiringStoreIssueLifecycleLayersHooksOverInner(t *testing.T) {
	inner := &fakeIssueOperations{}
	store := &HookFiringStore{inner: lifecycleStore{lifecycle: inner}}

	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle() error = %v", err)
	}
	hooked, ok := lifecycle.(*hookIssueOperations)
	if !ok {
		t.Fatalf("IssueLifecycle() = %T, want *hookIssueOperations", lifecycle)
	}
	if hooked.inner != issueops.Lifecycle(inner) {
		t.Fatalf("hook layer wraps %#v, want the inner store's lifecycle", hooked.inner)
	}
	if hooked.hooks != issueOperationHooks(store) {
		t.Fatalf("hook layer fires into %#v, want the decorator itself", hooked.hooks)
	}
}

func TestHookFiringStoreIssueLifecyclePropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: lifecycleStore{err: want}}

	lifecycle, err := store.IssueLifecycle()
	if !errors.Is(err, want) {
		t.Fatalf("IssueLifecycle() error = %v, want %v", err, want)
	}
	if lifecycle != nil {
		t.Fatalf("IssueLifecycle() = %T, want nil", lifecycle)
	}
}

// TestHookFiringStoreCompleteIssueOperationsFireOncePerCall pins the completion
// entry points to one hook event per call, in the caller's order. Deciding
// whether a committed change warrants a hook belongs to the caller
// (hookIssueOperations, below), not to these methods.
func TestHookFiringStoreCompleteIssueOperationsFireOncePerCall(t *testing.T) {
	runner := &recordingHookRunner{}
	store := &HookFiringStore{runner: runner}
	issue := &types.Issue{ID: "hook-issue"}

	store.CompleteIssueOperationCreate(context.Background(), issue, nil)
	store.CompleteIssueOperationUpdate(issue)
	store.CompleteIssueOperationClose(issue)

	if !reflect.DeepEqual(runner.events, []string{hooks.EventCreate, hooks.EventUpdate, hooks.EventClose}) {
		t.Fatalf("hook events = %#v, want create/update/close", runner.events)
	}
}

// TestHookFiringStoreCompleteIssueOperationsSnapshotIssue pins the clone at the
// completion entry points: the real hook runner marshals the issue on its own
// goroutine, and the CLI mutates the result object it handed in right after
// completing (cmd/bd close/update/reopen nil out .Dependencies), so handing
// the runner the caller's pointer is a data race with a nondeterministic
// payload (bd-9wgv3).
func TestHookFiringStoreCompleteIssueOperationsSnapshotIssue(t *testing.T) {
	runner := &recordingHookRunner{}
	store := &HookFiringStore{runner: runner}
	issue := &types.Issue{ID: "hook-issue", Dependencies: []*types.Dependency{
		{IssueID: "hook-issue", DependsOnID: "dep", Type: types.DepBlocks},
	}}

	store.CompleteIssueOperationUpdate(issue)
	store.CompleteIssueOperationClose(issue)
	issue.Dependencies = nil

	if len(runner.issues) != 2 {
		t.Fatalf("recorded issues = %d, want 2", len(runner.issues))
	}
	for i, got := range runner.issues {
		if got == issue {
			t.Fatalf("event %d received the caller's issue pointer; completion must hand the runner a clone", i)
		}
		if len(got.Dependencies) != 1 || got.Dependencies[0].DependsOnID != "dep" {
			t.Fatalf("event %d dependencies = %#v, want the snapshot taken before the caller mutated the issue", i, got.Dependencies)
		}
	}
}

func TestHookFiringStoreCompleteIssueOperationCreateFiresReverseDependencyUpdate(t *testing.T) {
	runner := &recordingHookRunner{}
	reverse := &types.Dependency{IssueID: "existing-source", DependsOnID: "created", Type: types.DepRelatesTo, Metadata: `{"key":"value"}`, ThreadID: "thread"}
	inner := fakeHookStore{issues: map[string]*types.Issue{
		"created":         {ID: "created"},
		"existing-source": {ID: "existing-source", Dependencies: []*types.Dependency{reverse}},
	}}
	store := &HookFiringStore{DoltStorage: inner, inner: inner, runner: runner}
	created := &types.Issue{ID: "created"}

	store.CompleteIssueOperationCreate(context.Background(), created, []*types.Dependency{{IssueID: "existing-source", DependsOnID: "created", Type: types.DepRelatesTo}})

	if !reflect.DeepEqual(runner.events, []string{hooks.EventCreate, hooks.EventUpdate}) {
		t.Fatalf("hook events = %#v, want create then dependency update", runner.events)
	}
	if runner.issues[1].ID != "existing-source" || !reflect.DeepEqual(runner.issues[1].Dependencies, []*types.Dependency{reverse}) {
		t.Fatalf("dependency hook snapshot = %#v", runner.issues[1])
	}
	if created.Dependencies != nil {
		t.Fatalf("created result was mutated: %#v", created.Dependencies)
	}
}

func TestHookIssueOperationsForwardsEveryVerbExactlyOnceAndPreservesErrors(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name string
		call func(issueops.Lifecycle) error
	}{
		{"create", func(ops issueops.Lifecycle) error { _, err := ops.Create(ctx, issueops.CreateRequest{}); return err }},
		{"update", func(ops issueops.Lifecycle) error { _, err := ops.Update(ctx, issueops.UpdateRequest{}); return err }},
		{"close", func(ops issueops.Lifecycle) error { _, err := ops.Close(ctx, issueops.CloseRequest{}); return err }},
		{"reopen", func(ops issueops.Lifecycle) error { _, err := ops.Reopen(ctx, issueops.ReopenRequest{}); return err }},
	} {
		t.Run(test.name+" success", func(t *testing.T) {
			fake := &fakeIssueOperations{changed: true}
			if err := test.call(&hookIssueOperations{inner: fake, hooks: NewHookFiringStore(nil, nil)}); err != nil || len(fake.calls) != 1 {
				t.Fatalf("forward = %v, calls=%v", err, fake.calls)
			}
		})
		t.Run(test.name+" error", func(t *testing.T) {
			want := errors.New("underlying")
			fake := &fakeIssueOperations{changed: true, err: want}
			if err := test.call(&hookIssueOperations{inner: fake, hooks: NewHookFiringStore(nil, nil)}); !errors.Is(err, want) || len(fake.calls) != 1 {
				t.Fatalf("error=%v calls=%v", err, fake.calls)
			}
		})
	}
}

func TestHookIssueOperationsFiresCompletionHooksPerVerbRules(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		fake    *fakeIssueOperations
		call    func(issueops.Lifecycle) error
		wantErr bool
		want    []string
	}{
		{
			// Reopen mirrors hookTrackingLifecycleTransaction.ReopenIssueWithResult
			// (internal/storage/hook_decorator.go), which queues no hook when the
			// reopen changed nothing.
			name: "reopen no-op fires nothing",
			fake: &fakeIssueOperations{changed: false},
			call: func(ops issueops.Lifecycle) error { _, err := ops.Reopen(ctx, issueops.ReopenRequest{}); return err },
			want: nil,
		},
		{
			name: "reopen change fires update",
			fake: &fakeIssueOperations{changed: true},
			call: func(ops issueops.Lifecycle) error { _, err := ops.Reopen(ctx, issueops.ReopenRequest{}); return err },
			want: []string{"update"},
		},
		{
			name:    "reopen error fires nothing",
			fake:    &fakeIssueOperations{changed: true, err: errors.New("underlying")},
			call:    func(ops issueops.Lifecycle) error { _, err := ops.Reopen(ctx, issueops.ReopenRequest{}); return err },
			wantErr: true,
			want:    nil,
		},
		{
			// Update fires on every success, no-op included, mirroring
			// HookFiringStore.UpdateIssueChecked (internal/storage/hook_decorator.go).
			// Do not "unify" this with reopen's gating.
			name: "update no-op still fires update",
			fake: &fakeIssueOperations{changed: false},
			call: func(ops issueops.Lifecycle) error { _, err := ops.Update(ctx, issueops.UpdateRequest{}); return err },
			want: []string{"update"},
		},
		{
			// Close fires on every success, including the idempotent re-close,
			// mirroring HookFiringStore.CloseIssueChecked
			// (internal/storage/hook_decorator.go). Do not "unify" this either.
			name: "close no-op still fires close",
			fake: &fakeIssueOperations{changed: false},
			call: func(ops issueops.Lifecycle) error { _, err := ops.Close(ctx, issueops.CloseRequest{}); return err },
			want: []string{"close"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			err := test.call(&hookIssueOperations{inner: test.fake, hooks: recorder})
			if (err != nil) != test.wantErr {
				t.Fatalf("call error = %v, wantErr = %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(recorder.completions, test.want) {
				t.Fatalf("completion hooks = %#v, want %#v", recorder.completions, test.want)
			}
		})
	}
}
