package tracker

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// NewUOWStore adapts a proxied unit-of-work provider to the tracker Store
// contract. Every operation uses a request-scoped unit of work; lifecycle
// updates and label replacement remain one atomic transaction.
func NewUOWStore(provider uow.UnitOfWorkProvider) Store {
	if provider == nil {
		return nil
	}
	return &uowStore{provider: provider}
}

type uowStore struct{ provider uow.UnitOfWorkProvider }

func (s *uowStore) GetConfig(ctx context.Context, key string) (string, error) {
	return uow.RunTxRead(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return uw.ConfigUseCase().GetConfig(ctx, key)
	})
}

func (s *uowStore) GetAllConfig(ctx context.Context) (map[string]string, error) {
	return uow.RunTxRead(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (map[string]string, error) {
		return uw.ConfigUseCase().GetAllConfig(ctx)
	})
}

func (s *uowStore) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	return uow.RunTxRead(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		return uw.ConfigUseCase().GetLocalMetadata(ctx, key)
	})
}

func (s *uowStore) SetLocalMetadata(ctx context.Context, key, value string) error {
	_, err := uow.RunTxEphemeral(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (struct{}, error) {
		return struct{}{}, uw.ConfigUseCase().SetLocalMetadata(ctx, key, value)
	})
	return err
}

func (s *uowStore) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	page, err := uow.RunTxRead(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (domain.SearchPage, error) {
		return uw.IssueUseCase().SearchIssues(ctx, query, filter)
	})
	return page.Items, err
}

func (s *uowStore) GetIssueByExternalRef(ctx context.Context, ref string) (*types.Issue, error) {
	filter := types.IssueFilter{ExternalRef: &ref, Limit: 1}
	issues, err := s.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, storage.ErrNotFound
	}
	return issues[0], nil
}

func (s *uowStore) GetDependentsWithMetadata(ctx context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	return uow.RunTxRead(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.IssueWithDependencyMetadata, error) {
		return uw.DependencyUseCase().ListWithIssueMetadata(ctx, id, domain.DepListFilter{Direction: domain.DepDirectionIn})
	})
}

func (s *uowStore) GetDependenciesWithMetadata(ctx context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	return uow.RunTxRead(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) ([]*types.IssueWithDependencyMetadata, error) {
		return uw.DependencyUseCase().ListWithIssueMetadata(ctx, id, domain.DepListFilter{Direction: domain.DepDirectionOut})
	})
}

func (s *uowStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	return uow.RunTx(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: issue}, actor)
		return "bd: tracker create", err
	})
}

func (s *uowStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	return s.ApplyIssueUpdate(ctx, id, updates, nil, actor)
}

func (s *uowStore) ApplyIssueUpdate(ctx context.Context, id string, updates map[string]interface{}, labels []string, actor string) error {
	fields := make(map[string]any, len(updates))
	for key, value := range updates {
		fields[key] = value
	}
	var setLabels *[]string
	if labels != nil {
		copy := append([]string(nil), labels...)
		setLabels = &copy
	}
	return uow.RunTx(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		_, err := uw.IssueUseCase().ApplyUpdate(ctx, id, domain.UpdateSpec{Fields: fields, SetLabels: setLabels}, actor)
		return "bd: tracker update", err
	})
}

func (s *uowStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return uow.RunTx(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		_, err := uw.DependencyUseCase().AddDependencies(ctx, []*types.Dependency{dep}, actor, domain.BulkAddDepsOpts{})
		return "bd: tracker dependency", err
	})
}
