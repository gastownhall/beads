package storage

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type fakeIssueReader struct{ issueops.Reader }

// readerStore is a DoltStorage whose only real method is IssueReader.
type readerStore struct {
	DoltStorage
	reader issueops.Reader
	err    error
}

func (s readerStore) IssueReader() (issueops.Reader, error) { return s.reader, s.err }

// TestHookFiringStoreIssueReaderReturnsTheInnerReaderUnwrapped pins the
// decision this decorator's read accessor makes, which is the opposite of the
// one its write accessor makes: reads fire no completion hooks, because there
// is no completion to report, so the reader comes back UNWRAPPED.
//
// Its Lifecycle sibling is pinned by
// TestHookFiringStoreIssueLifecycleLayersHooksOverInner precisely because
// blind delegation there would silently stop firing hooks. Here blind
// delegation IS the contract — and a future edit that "made the two accessors
// consistent" by wrapping would put a hook layer on a path with nothing to
// fire. Both directions are now a test edit rather than a review comment.
func TestHookFiringStoreIssueReaderReturnsTheInnerReaderUnwrapped(t *testing.T) {
	inner := &fakeIssueReader{}
	store := &HookFiringStore{inner: readerStore{reader: inner}}

	reader, err := store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader() error = %v", err)
	}
	if reader != issueops.Reader(inner) {
		t.Fatalf("IssueReader() = %#v, want the inner store's reader unwrapped", reader)
	}
}

func TestHookFiringStoreIssueReaderPropagatesInnerError(t *testing.T) {
	want := errors.New("inner refused")
	store := &HookFiringStore{inner: readerStore{err: want}}

	reader, err := store.IssueReader()
	if !errors.Is(err, want) {
		t.Fatalf("IssueReader() error = %v, want %v", err, want)
	}
	if reader != nil {
		t.Fatalf("IssueReader() = %T, want nil", reader)
	}
}
