package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeCommenter answers with a fixed comment so the firing rules can be
// checked without a database.
type fakeCommenter struct {
	comment *types.Comment
	err     error
}

func (f *fakeCommenter) AddComment(context.Context, issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	return issueops.AddCommentResult{Comment: f.comment}, f.err
}

// commenterStore is a DoltStorage whose only real method is Commenter.
type commenterStore struct {
	DoltStorage
	commenter issueops.Commenter
	err       error
}

func (s commenterStore) Commenter() (issueops.Commenter, error) { return s.commenter, s.err }

// TestHookFiringStoreCommenterLayersHooksOverInner pins the recursion.
// Delegating to the inner store instead would still compile and still satisfy
// Storage, and every guarded comment would silently stop firing the update
// hook AddIssueComment fires.
func TestHookFiringStoreCommenterLayersHooksOverInner(t *testing.T) {
	inner := &fakeCommenter{}
	store := &HookFiringStore{inner: commenterStore{commenter: inner}}

	commenter, err := store.Commenter()
	if err != nil {
		t.Fatalf("Commenter() error = %v", err)
	}
	hooked, ok := commenter.(*hookCommenter)
	if !ok {
		t.Fatalf("Commenter() = %T, want *hookCommenter", commenter)
	}
	if hooked.inner != issueops.Commenter(inner) {
		t.Fatalf("hook layer wraps %#v, want the inner store's commenter", hooked.inner)
	}
	if hooked.hooks != issueOperationHooks(store) {
		t.Fatalf("hook layer fires into %#v, want the decorator itself", hooked.hooks)
	}
}

func TestHookFiringStoreCommenterPropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: commenterStore{err: want}}

	commenter, err := store.Commenter()
	if !errors.Is(err, want) {
		t.Fatalf("Commenter() error = %v, want %v", err, want)
	}
	if commenter != nil {
		t.Fatalf("Commenter() = %T, want nil", commenter)
	}
}

// TestHookCommenterFiresForTheCommentedIssue pins that the hook names the
// issue the STORED comment landed on rather than the id the caller asked for:
// the request's id may be a wisp alias the role resolved, and a script handed
// the unresolved one would look up the wrong row.
func TestHookCommenterFiresForTheCommentedIssue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		commenter *fakeCommenter
		want      []string
	}{
		{
			name:      "stored comment fires its own issue id",
			commenter: &fakeCommenter{comment: &types.Comment{ID: "c1", IssueID: "bd-resolved"}},
			want:      []string{"comment:bd-resolved"},
		},
		{
			name:      "error fires nothing",
			commenter: &fakeCommenter{comment: &types.Comment{IssueID: "bd-1"}, err: errors.New("boom")},
			want:      nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &recordingIssueOperationHooks{}
			commenter := &hookCommenter{inner: tc.commenter, hooks: recorder}

			_, _ = commenter.AddComment(context.Background(), issueops.AddCommentRequest{
				Author: "worker", IssueID: "bd-asked", Text: "hi",
			})

			if !reflect.DeepEqual(recorder.completions, tc.want) {
				t.Fatalf("hooks fired = %v, want %v", recorder.completions, tc.want)
			}
		})
	}
}
