package storage

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type fakeIssueRelations struct{ issueops.Relations }

// relationsStore is a DoltStorage whose only real method is IssueRelations.
type relationsStore struct {
	DoltStorage
	relations issueops.Relations
	err       error
}

func (s relationsStore) IssueRelations() (issueops.Relations, error) { return s.relations, s.err }

// TestHookFiringStoreIssueRelationsReturnsTheInnerSurfaceUnwrapped pins the
// same decision hook_issue_reader_test.go pins, for the other read role: reads
// fire no completion hooks, so the surface comes back UNWRAPPED. A future edit
// that "made the accessors consistent" by wrapping would put a hook layer on a
// path with nothing to fire.
func TestHookFiringStoreIssueRelationsReturnsTheInnerSurfaceUnwrapped(t *testing.T) {
	inner := &fakeIssueRelations{}
	store := &HookFiringStore{inner: relationsStore{relations: inner}}

	relations, err := store.IssueRelations()
	if err != nil {
		t.Fatalf("IssueRelations() error = %v", err)
	}
	if relations != issueops.Relations(inner) {
		t.Fatalf("IssueRelations() = %#v, want the inner store's surface unwrapped", relations)
	}
}

func TestHookFiringStoreIssueRelationsPropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: relationsStore{err: want}}

	relations, err := store.IssueRelations()
	if !errors.Is(err, want) {
		t.Fatalf("IssueRelations() error = %v, want %v", err, want)
	}
	if relations != nil {
		t.Fatalf("IssueRelations() = %T, want nil", relations)
	}
}
