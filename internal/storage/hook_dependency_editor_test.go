package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// fakeDependencyEditor answers with a fixed result so the firing rules can be
// checked without a database.
type fakeDependencyEditor struct {
	added   []issueops.DependencyEdge
	removed bool
	err     error
}

func (f *fakeDependencyEditor) AddDependencies(context.Context, issueops.AddDependenciesRequest) (issueops.AddDependenciesResult, error) {
	return issueops.AddDependenciesResult{Added: f.added}, f.err
}

func (f *fakeDependencyEditor) RemoveDependency(context.Context, issueops.RemoveDependencyRequest) (issueops.RemoveDependencyResult, error) {
	return issueops.RemoveDependencyResult{Removed: f.removed}, f.err
}

// dependencyEditorStore is a DoltStorage whose only real method is
// DependencyEditor.
type dependencyEditorStore struct {
	DoltStorage
	editor issueops.DependencyEditor
	err    error
}

func (s dependencyEditorStore) DependencyEditor() (issueops.DependencyEditor, error) {
	return s.editor, s.err
}

// TestHookFiringStoreDependencyEditorLayersHooksOverInner pins the recursion.
// Delegating to the inner store instead would still compile and still satisfy
// Storage, and every guarded edge edit would silently stop firing the update
// hook that AddDependency and RemoveDependency fire.
func TestHookFiringStoreDependencyEditorLayersHooksOverInner(t *testing.T) {
	inner := &fakeDependencyEditor{}
	store := &HookFiringStore{inner: dependencyEditorStore{editor: inner}}

	editor, err := store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor() error = %v", err)
	}
	hooked, ok := editor.(*hookDependencyEditor)
	if !ok {
		t.Fatalf("DependencyEditor() = %T, want *hookDependencyEditor", editor)
	}
	if hooked.inner != issueops.DependencyEditor(inner) {
		t.Fatalf("hook layer wraps %#v, want the inner store's editor", hooked.inner)
	}
	if hooked.hooks != issueOperationHooks(store) {
		t.Fatalf("hook layer fires into %#v, want the decorator itself", hooked.hooks)
	}
}

func TestHookFiringStoreDependencyEditorPropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: dependencyEditorStore{err: want}}

	editor, err := store.DependencyEditor()
	if !errors.Is(err, want) {
		t.Fatalf("DependencyEditor() error = %v, want %v", err, want)
	}
	if editor != nil {
		t.Fatalf("DependencyEditor() = %T, want nil", editor)
	}
}

// TestHookDependencyEditorFiresOncePerSource pins the de-duplication. A hook
// script is written against one issue, so two edges leaving the same issue are
// one change to it; firing twice would have a script react to a graph it
// already saw.
func TestHookDependencyEditorFiresOncePerSource(t *testing.T) {
	edge := func(from, to string) issueops.DependencyEdge {
		return issueops.DependencyEdge{IssueID: from, DependsOnID: to, Type: issueops.DepBlocks}
	}
	for _, tc := range []struct {
		name   string
		editor *fakeDependencyEditor
		want   []string
	}{
		{
			name:   "one edge fires its source",
			editor: &fakeDependencyEditor{added: []issueops.DependencyEdge{edge("bd-1", "bd-2")}},
			want:   []string{"dependency:bd-1"},
		},
		{
			name: "two edges from one source fire once, in request order",
			editor: &fakeDependencyEditor{added: []issueops.DependencyEdge{
				edge("bd-1", "bd-2"), edge("bd-3", "bd-4"), edge("bd-1", "bd-5"),
			}},
			want: []string{"dependency:bd-1", "dependency:bd-3"},
		},
		{
			name: "a refused request fires nothing",
			editor: &fakeDependencyEditor{
				added: []issueops.DependencyEdge{edge("bd-1", "bd-2")},
				err:   errors.New("boom"),
			},
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			editor := &hookDependencyEditor{inner: tc.editor, hooks: recorder}

			_, _ = editor.AddDependencies(context.Background(), issueops.AddDependenciesRequest{Actor: "worker"})

			if !reflect.DeepEqual(recorder.completions, tc.want) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, tc.want)
			}
		})
	}
}

// TestHookDependencyEditorRemoveFiresOnlyForARemovedEdge pins the idempotent
// case. A removal that found nothing changed no graph, and a hook fired for it
// is a hook a replayed teardown runs on every pass.
func TestHookDependencyEditorRemoveFiresOnlyForARemovedEdge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		editor *fakeDependencyEditor
		want   []string
	}{
		{
			name:   "removed edge fires",
			editor: &fakeDependencyEditor{removed: true},
			want:   []string{"dependency:bd-1"},
		},
		{
			name:   "no such edge fires nothing",
			editor: &fakeDependencyEditor{},
			want:   nil,
		},
		{
			name:   "error fires nothing",
			editor: &fakeDependencyEditor{removed: true, err: errors.New("boom")},
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			editor := &hookDependencyEditor{inner: tc.editor, hooks: recorder}

			_, _ = editor.RemoveDependency(context.Background(), issueops.RemoveDependencyRequest{
				Actor: "worker", IssueID: "bd-1", DependsOnID: "bd-2",
			})

			if !reflect.DeepEqual(recorder.completions, tc.want) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, tc.want)
			}
		})
	}
}
