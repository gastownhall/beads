package storage_test

import (
	"context"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// mockHookRunner records hook invocations synchronously for testing.
type mockHookRunner struct {
	mu      sync.Mutex
	invoked []hookInvocation
}

type hookInvocation struct {
	event string
	issue *types.Issue
}

func (m *mockHookRunner) Run(event string, issue *types.Issue) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invoked = append(m.invoked, hookInvocation{event, issue})
}

func (m *mockHookRunner) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.invoked)
}

func (m *mockHookRunner) get(i int) hookInvocation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.invoked[i]
}

func TestHookFiringStoreCompileTimeChecks(t *testing.T) {
	// Verify the decorator satisfies DoltStorage at compile time.
	// This is also checked via var _ declarations in the source.
	var _ storage.DoltStorage = (*storage.HookFiringStore)(nil)
}

func TestNewHookFiringStoreNilRunnerPreservesWrappedStore(t *testing.T) {
	raw := &stubDoltStore{}
	wrapped := storage.NewHookFiringStore(raw, nil)

	if wrapped == nil {
		t.Fatal("NewHookFiringStore() returned nil")
	}
	if got := wrapped.Unwrap(); got != raw {
		t.Errorf("NewHookFiringStore(...).Unwrap() = %T, want original store %T", got, raw)
	}
}

type transactionRecordingStore struct {
	storage.DoltStorage
	commitMessage string
	callbackRan   bool
}

func (s *transactionRecordingStore) RunInTransaction(_ context.Context, commitMessage string, fn func(storage.Transaction) error) error {
	s.commitMessage = commitMessage
	s.callbackRan = true
	return fn(nil)
}

func TestHookFiringStoreNilRunnerDelegatesTransaction(t *testing.T) {
	raw := &transactionRecordingStore{}
	wrapped := storage.NewHookFiringStore(raw, nil)

	callbackRan := false
	if err := wrapped.RunInTransaction(context.Background(), "test transaction", func(storage.Transaction) error {
		callbackRan = true
		return nil
	}); err != nil {
		t.Fatalf("RunInTransaction() = %v", err)
	}
	if !raw.callbackRan || !callbackRan {
		t.Error("RunInTransaction() did not invoke the wrapped store and callback")
	}
	if raw.commitMessage != "test transaction" {
		t.Errorf("wrapped transaction message = %q, want %q", raw.commitMessage, "test transaction")
	}
}

// stubDoltStore is a typed stand-in for a concrete DoltStorage implementation.
// Tests must not invoke any of its methods (interface-promoted calls would
// panic on the embedded nil); only its identity is used.
type stubDoltStore struct {
	storage.DoltStorage
}

// fakeUnwrappableDecorator is a minimal Unwrapper used to verify that
// UnwrapStore peels arbitrary decorator chains, not just HookFiringStore.
type fakeUnwrappableDecorator struct {
	storage.DoltStorage
	inner storage.DoltStorage
}

func (d *fakeUnwrappableDecorator) Unwrap() storage.DoltStorage { return d.inner }

func TestUnwrapStore_NoDecorator(t *testing.T) {
	raw := &stubDoltStore{}
	if got := storage.UnwrapStore(raw); got.(*stubDoltStore) != raw {
		t.Errorf("UnwrapStore on a non-decorator returned %T; want input unchanged", got)
	}
}

func TestUnwrapStore_HookFiringStore(t *testing.T) {
	raw := &stubDoltStore{}
	wrapped := storage.NewHookFiringStore(raw, nil)
	if got := storage.UnwrapStore(wrapped); got.(*stubDoltStore) != raw {
		t.Errorf("UnwrapStore did not peel HookFiringStore; got %T want %T", got, raw)
	}
}

// Catches the regression where adding a new decorator layer (e.g.
// telemetry.InstrumentedStorage) silently breaks UnwrapStore for
// optional-interface type assertions across cmd/bd.
func TestUnwrapStore_PeelsMultipleLayers(t *testing.T) {
	raw := &stubDoltStore{}
	mid := &fakeUnwrappableDecorator{inner: raw}
	outer := storage.NewHookFiringStore(mid, nil)
	if got := storage.UnwrapStore(outer); got.(*stubDoltStore) != raw {
		t.Errorf("UnwrapStore did not peel all decorator layers; got %T want %T", got, raw)
	}
}
