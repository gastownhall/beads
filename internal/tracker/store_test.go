package tracker

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

type contractStoreStub struct{}

func (contractStoreStub) GetConfig(context.Context, string) (string, error)        { return "", nil }
func (contractStoreStub) GetAllConfig(context.Context) (map[string]string, error)  { return nil, nil }
func (contractStoreStub) GetLocalMetadata(context.Context, string) (string, error) { return "", nil }
func (contractStoreStub) SetLocalMetadata(context.Context, string, string) error   { return nil }
func (contractStoreStub) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return nil, nil
}
func (contractStoreStub) GetIssueByExternalRef(context.Context, string) (*types.Issue, error) {
	return nil, nil
}
func (contractStoreStub) GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (contractStoreStub) GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (contractStoreStub) CreateIssue(context.Context, *types.Issue, string) error { return nil }
func (contractStoreStub) UpdateIssue(context.Context, string, map[string]interface{}, string) error {
	return nil
}
func (contractStoreStub) AddDependency(context.Context, *types.Dependency, string) error { return nil }

func TestNewStoreNilIsSafe(t *testing.T) {
	if got := NewStore(nil); got != nil {
		t.Fatalf("NewStore(nil) = %T, want nil", got)
	}
}

func TestNewStorePreservesNarrowStore(t *testing.T) {
	stub := contractStoreStub{}
	if got := NewStore(stub); got == nil {
		t.Fatal("NewStore returned nil for a Store implementation")
	}
}

type failingUOWProvider struct{ err error }

func (p failingUOWProvider) NewUOW(context.Context) (uow.UnitOfWork, error) { return nil, p.err }
func (p failingUOWProvider) Close(context.Context) error                    { return nil }

func TestUOWStorePropagatesProviderFailure(t *testing.T) {
	want := errors.New("provider unavailable")
	store := NewUOWStore(failingUOWProvider{err: want})
	_, err := store.GetConfig(context.Background(), "linear.team_id")
	if !errors.Is(err, want) {
		t.Fatalf("GetConfig error = %v, want %v", err, want)
	}
}

func TestNewUOWStoreTypedNilIsSafe(t *testing.T) {
	var provider *failingUOWProvider
	if got := NewUOWStore(provider); got != nil {
		t.Fatalf("typed nil provider returned %T", got)
	}
}

type lifecycleStoreFake struct {
	storage.Storage
	labels    []string
	updateErr error
	// degraded records a direct UpdateIssue that bypassed the lifecycle
	// transaction. That is the label-dropping fallback ApplyIssueUpdate takes
	// when its inner value is not a storage.IssueLifecycleStore.
	degraded bool
}

func (s *lifecycleStoreFake) RunInIssueLifecycleTransaction(ctx context.Context, _ string, fn func(storage.IssueLifecycleTransaction) error) error {
	return fn(&lifecycleTxFake{store: s})
}

func (s *lifecycleStoreFake) UpdateIssue(context.Context, string, map[string]interface{}, string) error {
	s.degraded = true
	return s.updateErr
}

type lifecycleTxFake struct {
	storage.Transaction
	store *lifecycleStoreFake
}

func (tx *lifecycleTxFake) UpdateIssue(context.Context, string, map[string]interface{}, string) error {
	return tx.store.updateErr
}
func (tx *lifecycleTxFake) GetLabels(context.Context, string) ([]string, error) {
	return append([]string(nil), tx.store.labels...), nil
}
func (tx *lifecycleTxFake) AddLabel(_ context.Context, _, label, _ string) error {
	tx.store.labels = append(tx.store.labels, label)
	return nil
}
func (tx *lifecycleTxFake) RemoveLabel(_ context.Context, _, label, _ string) error {
	filtered := tx.store.labels[:0]
	for _, existing := range tx.store.labels {
		if existing != label {
			filtered = append(filtered, existing)
		}
	}
	tx.store.labels = filtered
	return nil
}
func (tx *lifecycleTxFake) ReopenIssueWithResult(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func TestDirectStoreApplyIssueUpdateDistinguishesOmittedAndEmptyLabels(t *testing.T) {
	underlying := &lifecycleStoreFake{labels: []string{"keep"}}
	store := NewStore(underlying).(*directStore)
	if err := store.ApplyIssueUpdate(context.Background(), "bd-1", nil, nil, "actor"); err != nil {
		t.Fatal(err)
	}
	if len(underlying.labels) != 1 || underlying.labels[0] != "keep" {
		t.Fatalf("nil labels changed state: %v", underlying.labels)
	}
	if err := store.ApplyIssueUpdate(context.Background(), "bd-1", nil, []string{}, "actor"); err != nil {
		t.Fatal(err)
	}
	if len(underlying.labels) != 0 {
		t.Fatalf("empty labels did not clear state: %v", underlying.labels)
	}
}

func TestDirectStoreApplyIssueUpdateFailureDoesNotTouchLabels(t *testing.T) {
	underlying := &lifecycleStoreFake{labels: []string{"keep"}, updateErr: errors.New("update failed")}
	store := NewStore(underlying).(*directStore)
	if err := store.ApplyIssueUpdate(context.Background(), "bd-1", nil, []string{}, "actor"); !errors.Is(err, underlying.updateErr) {
		t.Fatalf("error = %v", err)
	}
	if len(underlying.labels) != 1 || underlying.labels[0] != "keep" {
		t.Fatalf("failed update changed labels: %v", underlying.labels)
	}
}

// TestNewEngineKeepsDirectStoreAdaptationIntact pins the production wiring that
// no other test covers: trackerStoreForCommand adapts the direct store itself
// and hands the adapter to NewEngine, which adapts again. Re-wrapping produces
// &directStore{Storage: &directStore{...}}, whose inner value can never satisfy
// storage.IssueLifecycleStore, so ApplyIssueUpdate silently degrades to a
// label-less, transaction-less UpdateIssue. The conformance harness cannot see
// this: its store implements tracker.Store directly, which is the one shape
// direct mode never produces.
func TestNewEngineKeepsDirectStoreAdaptationIntact(t *testing.T) {
	underlying := &lifecycleStoreFake{labels: []string{"stale"}}
	adapted := NewStore(underlying) // what trackerStoreForCommand returns in direct mode

	engine := NewEngine(nil, adapted, "actor")

	if engine.Store != adapted {
		t.Fatalf("NewEngine re-adapted an already-adapted store: %T", engine.Store)
	}
	updater, ok := engine.Store.(IssueUpdater)
	if !ok {
		t.Fatalf("engine store %T lost the IssueUpdater capability", engine.Store)
	}
	if err := updater.ApplyIssueUpdate(context.Background(), "bd-1", nil, []string{"fresh"}, "actor"); err != nil {
		t.Fatal(err)
	}
	if underlying.degraded {
		t.Fatal("ApplyIssueUpdate bypassed the lifecycle transaction")
	}
	if len(underlying.labels) != 1 || underlying.labels[0] != "fresh" {
		t.Fatalf("labels did not survive the pull: %v", underlying.labels)
	}
}

func TestNewStoreIsIdempotent(t *testing.T) {
	direct := NewStore(&lifecycleStoreFake{})
	if got := NewStore(direct); got != direct {
		t.Fatalf("NewStore(directStore) = %T, want the same value", got)
	}
	uowBacked := NewUOWStore(failingUOWProvider{err: errors.New("unused")})
	if got := NewStore(uowBacked); got != uowBacked {
		t.Fatalf("NewStore(uowStore) = %T, want the same value", got)
	}
	stub := contractStoreStub{}
	if got := NewStore(NewStore(stub)); got != Store(stub) {
		t.Fatalf("NewStore(narrow Store) = %T, want the same value", got)
	}
}

func TestDirectStoreApplyIssueUpdateNormalizesLabels(t *testing.T) {
	underlying := &lifecycleStoreFake{labels: []string{"keep"}}
	store := NewStore(underlying).(*directStore)

	// " keep " differs from "keep" only in whitespace and must not churn;
	// the empty label must be dropped rather than reaching AddLabel.
	if err := store.ApplyIssueUpdate(context.Background(), "bd-1", nil, []string{" keep ", ""}, "actor"); err != nil {
		t.Fatal(err)
	}
	if len(underlying.labels) != 1 || underlying.labels[0] != "keep" {
		t.Fatalf("labels churned or admitted an empty value: %v", underlying.labels)
	}
}
