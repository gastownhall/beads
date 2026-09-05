package tracker

import (
	"context"
	"reflect"

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
	v := reflect.ValueOf(provider)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		if v.IsNil() {
			return nil
		}
	}
	if v.IsValid() == false {
		return nil
	}
	return &uowStore{provider: provider}
}

type uowStore struct{ provider uow.UnitOfWorkProvider }

var _ Store = (*uowStore)(nil)
var _ IssueUpdater = (*uowStore)(nil)

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

// GetIssueByExternalRef resolves the ISSUES PLANE FIRST and only then falls
// back to the wisp plane, because that ordering is the direct backend's
// deliberate contract, not an accident of its implementation:
// issueops.GetIssueByExternalRefInTx queries `issues` and only falls through to
// `wisps` on sql.ErrNoRows, and says why ("so that pushed ephemeral beads are
// found during pull dedup").
//
// A single merged search cannot express that. SearchIssues with neither plane
// flag set routes to searchUnion, a UNION ALL over the two legs ordered by
// content and terminating in `id ASC` — no plane preference. So for an
// external_ref present on BOTH planes, direct mode returns the issue and a
// merged search returns whichever row happens to sort first. The consumer is
// the pull dedup in engine.go, which then UPDATES the returned row with remote
// content, making the divergence a silent write to the wrong local bead in
// proxied mode only.
//
// Both plane reads share ONE unit of work. They must: uow.RunTxRead opens a
// fresh UOW per call, so issuing them as two s.SearchIssues calls would split
// one logical lookup across two read transactions, and the fallback's premise
// ("the issues plane is already known empty") would no longer be
// snapshot-guaranteed. A concurrent same-ref insert into the issues plane
// landing between the two reads would let the merged fallback return the wisp,
// re-creating exactly the wrong-row write this ordering exists to prevent.
// The direct backend resolves both planes inside a single withReadTx, so the
// shared snapshot is also the parity-preserving shape.
func (s *uowStore) GetIssueByExternalRef(ctx context.Context, ref string) (*types.Issue, error) {
	return uow.RunTxRead(ctx, s.provider, func(ctx context.Context, uw uow.UnitOfWork) (*types.Issue, error) {
		// Issues plane. SkipWisps selects the plane without contributing a WHERE
		// clause, so this is the exact analog of the direct backend's
		// `SELECT id FROM issues WHERE external_ref = ?`.
		page, err := uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{ExternalRef: &ref, Limit: 1, SkipWisps: true})
		if err != nil {
			return nil, err
		}
		if len(page.Items) > 0 {
			return page.Items[0], nil
		}

		// Wisp fallback, expressed as the merged search rather than Ephemeral=true.
		// Ephemeral is the only flag that routes to the wisps plane alone, but it
		// ALSO contributes `ephemeral = 1`, a predicate the direct backend's
		// `SELECT id FROM wisps WHERE external_ref = ?` does not have — it would
		// drop wisp-plane rows carrying ephemeral=0 (NoHistory beads, and typed
		// wisps minted without the flag; see types.IssueFilter.EphemeralTier),
		// trading this divergence for a narrower one. The merged search adds no
		// predicate, and its issues leg is already known empty from the search
		// above — in the same snapshot, now that both reads share this UOW — so a
		// hit here can only be a wisp, which is the fallback the direct backend
		// performs.
		page, err = uw.IssueUseCase().SearchIssues(ctx, "", types.IssueFilter{ExternalRef: &ref, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(page.Items) == 0 {
			return nil, storage.ErrNotFound
		}
		return page.Items[0], nil
	})
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
