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
// already implementing Store are returned unchanged.
func NewStore(value interface{}) Store {
	if store, ok := value.(storage.Storage); ok {
		return &directStore{Storage: store}
	}
	if store, ok := value.(Store); ok {
		return store
	}
	return nil
}

type directStore struct{ storage.Storage }

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
		return syncLabels(ctx, tx, id, labels, actor)
	})
}

func (s *directStore) GetDependenciesWithMetadata(ctx context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.Storage.GetDependenciesWithMetadata(ctx, id)
}

func syncLabels(ctx context.Context, tx storage.Transaction, id string, desired []string, actor string) error {
	current, err := tx.GetLabels(ctx, id)
	if err != nil {
		return err
	}
	currentSet := make(map[string]struct{}, len(current))
	for _, label := range current {
		currentSet[label] = struct{}{}
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, label := range desired {
		desiredSet[label] = struct{}{}
	}
	for label := range currentSet {
		if _, ok := desiredSet[label]; !ok {
			if err := tx.RemoveLabel(ctx, id, label, actor); err != nil {
				return err
			}
		}
	}
	for label := range desiredSet {
		if _, ok := currentSet[label]; !ok {
			if err := tx.AddLabel(ctx, id, label, actor); err != nil {
				return err
			}
		}
	}
	return nil
}
