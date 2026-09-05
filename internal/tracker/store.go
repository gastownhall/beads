package tracker

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Store is the persistence contract required by the tracker synchronization
// engine. It deliberately names only tracker operations; direct stores and
// unit-of-work providers can each adapt to this seam without pretending to
// implement the other backend's full storage surface.
type Store interface {
	GetConfig(context.Context, string) (string, error)
	GetAllConfig(context.Context) (map[string]string, error)
	GetLocalMetadata(context.Context, string) (string, error)
	SetLocalMetadata(context.Context, string, string) error
	SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error)
	GetIssueByExternalRef(context.Context, string) (*types.Issue, error)
	GetDependentsWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error)
	GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error)
	CreateIssue(context.Context, *types.Issue, string) error
	UpdateIssue(context.Context, string, map[string]interface{}, string) error
	AddDependency(context.Context, *types.Dependency, string) error
}

// IssueUpdater is the optional atomic lifecycle-plus-label update capability.
type IssueUpdater interface {
	ApplyIssueUpdate(context.Context, string, map[string]interface{}, []string, string) error
}

// ExternalRefHistoryStore is an optional capability used for precise
// relink/conflict detection.
type ExternalRefHistoryStore interface {
	PreviousExternalRef(context.Context, string, time.Time) (string, bool, error)
}

// NewStore adapts a classic storage.Storage to the tracker contract. Values
// already implementing Store are returned unchanged, so NewStore is idempotent
// and safe to call on a value that a caller has already adapted.
//
// Branch order is load-bearing, and neither interface alone can discriminate:
//
//   - storage.Storage is a strict superset of Store, so a raw direct store also
//     satisfies Store. Testing Store first would return it un-adapted and drop
//     ApplyIssueUpdate entirely.
//   - directStore embeds the storage.Storage interface, so *directStore
//     satisfies storage.Storage too. Testing storage.Storage first re-wraps an
//     already-adapted value as &directStore{Storage: &directStore{...}}, and
//     the inner *directStore can never satisfy storage.IssueLifecycleStore
//     (RunInIssueLifecycleTransaction is not part of storage.Storage). That
//     silently degrades ApplyIssueUpdate to UpdateIssue — no labels, no
//     lifecycle transaction.
//
// The tracker's own adapters are therefore matched by concrete type first: no
// storage backend can accidentally satisfy that check.
func NewStore(value interface{}) Store {
	switch store := value.(type) {
	case *directStore:
		return store
	case *uowStore:
		return store
	}
	if store, ok := value.(storage.Storage); ok {
		return &directStore{Storage: store}
	}
	if store, ok := value.(Store); ok {
		return store
	}
	return nil
}

type directStore struct{ storage.Storage }

var _ Store = (*directStore)(nil)
var _ IssueUpdater = (*directStore)(nil)

func (s *directStore) ApplyIssueUpdate(ctx context.Context, id string, updates map[string]interface{}, labels []string, actor string) error {
	dolt, ok := s.Storage.(storage.IssueLifecycleStore)
	if !ok {
		return s.Storage.UpdateIssue(ctx, id, updates, actor)
	}
	return dolt.RunInIssueLifecycleTransaction(ctx, "bd: tracker update "+id, func(tx storage.IssueLifecycleTransaction) error {
		if err := tx.UpdateIssue(ctx, id, updates, actor); err != nil {
			return err
		}
		if labels == nil {
			return nil
		}
		// syncIssueLabels, not a local copy: it normalizes both label sets
		// (trim + drop empty), which is the semantics the pull path had before
		// this seam moved behind ApplyIssueUpdate. Comparing raw strings makes
		// a whitespace-only difference churn remove+add on every sync and lets
		// an empty label reach AddLabel.
		return syncIssueLabels(ctx, tx, id, labels, actor)
	})
}

func (s *directStore) GetDependenciesWithMetadata(ctx context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.Storage.GetDependenciesWithMetadata(ctx, id)
}
