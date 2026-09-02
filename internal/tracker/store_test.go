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
}

func (s *lifecycleStoreFake) RunInIssueLifecycleTransaction(ctx context.Context, _ string, fn func(storage.IssueLifecycleTransaction) error) error {
	return fn(&lifecycleTxFake{store: s})
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
