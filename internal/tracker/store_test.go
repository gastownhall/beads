package tracker

import (
	"context"
	"errors"
	"testing"

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
