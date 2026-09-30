package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func (f *fakeStore) GetLabels(_ context.Context, id string) ([]string, error) {
	return []string{"label-of-" + id}, nil
}

func (f *fakeStore) GetDependencyRecords(_ context.Context, id string) ([]*types.Dependency, error) {
	return f.deps[id], nil
}

// TestStoreArmBatchCloseNeverSendsAFlaggedItem pins the store arm's half of the
// re-close fix: an item an unsatisfied external blocker holds is never sent to
// the inner closer, so no concurrent reopen between a status read and the
// inner close can turn a "re-close" into a real close. Already closed, it gets
// the idempotent outcome from a read (Changed false, the row hydrated with its
// labels and dependency records); open, it is refused; an unflagged item is
// sent as before.
func TestStoreArmBatchCloseNeverSendsAFlaggedItem(t *testing.T) {
	closed, open, free := issue("be-closed"), issue("be-open"), issue("be-free")
	closed.Status = types.StatusClosed
	raw := &fakeStore{
		ready: []*types.Issue{closed, open, free},
		deps: map[string][]*types.Dependency{
			closed.ID: {externalDep(closed.ID, "external:remote:payments", types.DepBlocks)},
			open.ID:   {externalDep(open.ID, "external:remote:payments", types.DepBlocks)},
		},
	}
	store := testStore(raw, &fakeStore{}, true)
	closer, err := store.BatchCloser()
	if err != nil {
		t.Fatal(err)
	}
	result, err := closer.CloseBatch(t.Context(), publicops.CloseBatchRequest{Actor: "w", Items: []publicops.BatchCloseItem{
		{IssueID: closed.ID}, {IssueID: open.ID}, {IssueID: free.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Outcomes
	if got[0].Err != nil || got[0].Changed || got[0].Issue == nil || got[0].Issue.Status != types.StatusClosed ||
		!slices.Equal(got[0].Issue.Labels, []string{"label-of-be-closed"}) || len(got[0].Issue.Dependencies) != 1 {
		t.Errorf("already-closed flagged item: outcome %+v, want the idempotent re-close with the hydrated row", got[0])
	}
	if !errors.Is(got[1].Err, storage.ErrCloseBlocked) {
		t.Errorf("open flagged item: err = %v, want the external refusal", got[1].Err)
	}
	if got[2].Err != nil || got[2].IssueID != free.ID {
		t.Errorf("unflagged item: outcome %+v, want it closed by the inner closer", got[2])
	}
	sentIDs := func() []string {
		var ids []string
		for _, req := range raw.batchReqs {
			for _, item := range req.Items {
				ids = append(ids, item.IssueID)
			}
		}
		return ids
	}()
	if !slices.Equal(sentIDs, []string{free.ID}) {
		t.Errorf("items sent to the inner closer = %v, want only %s", sentIDs, free.ID)
	}
}

// TestUOWArmBatchCloseDecidesTheReCloseInsideTheBatch pins the unit-of-work
// arm's half: flagged items go INTO the batch and the already-closed check runs
// in the batch's own transaction, immediately before the close, so it is
// atomic with it. The whole call opens exactly two units of work — the edge
// read and the batch — however many items are flagged; the old per-item
// status read opened one more per flagged item, outside the batch.
func TestUOWArmBatchCloseDecidesTheReCloseInsideTheBatch(t *testing.T) {
	closed, open, free := issue("be-closed"), issue("be-open"), issue("be-free")
	closed.Status = types.StatusClosed
	issues := &fakeIssueUseCase{ready: []*types.Issue{closed, open, free}}
	inner := &fakeUOW{issues: issues, deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		closed.ID: {externalDep(closed.ID, "external:remote:payments", types.DepBlocks)},
		open.ID:   {externalDep(open.ID, "external:remote:payments", types.DepBlocks)},
	}}}
	counting := &countingUOWProvider{openCountingProvider: &openCountingProvider{uw: inner}}
	provider := WrapUOWProvider(counting, func(ProjectName) (string, bool) { return "", false }, nil)
	closer, err := provider.(uow.BatchCloserSource).BatchCloser()
	if err != nil {
		t.Fatal(err)
	}
	result, err := closer.CloseBatch(t.Context(), publicops.CloseBatchRequest{Actor: "w", Items: []publicops.BatchCloseItem{
		{IssueID: closed.ID}, {IssueID: open.ID}, {IssueID: free.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcomes[0].Err != nil {
		t.Errorf("already-closed flagged item: err = %v, want the idempotent re-close", result.Outcomes[0].Err)
	}
	if !errors.Is(result.Outcomes[1].Err, storage.ErrCloseBlocked) {
		t.Errorf("open flagged item: err = %v, want the external refusal", result.Outcomes[1].Err)
	}
	if result.Outcomes[2].Err != nil {
		t.Errorf("unflagged item: err = %v", result.Outcomes[2].Err)
	}
	if !slices.Equal(issues.closed, []string{closed.ID, free.ID}) {
		t.Errorf("closes that reached the use case = %v, want [%s %s] (never the open flagged one)", issues.closed, closed.ID, free.ID)
	}
	if counting.opened != 2 {
		t.Errorf("units of work opened = %d, want 2 (edge read, batch)", counting.opened)
	}
}

type countingUOWProvider struct {
	*openCountingProvider
	opened int
}

func (p *countingUOWProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	p.opened++
	return p.openCountingProvider.NewUOW(ctx)
}

var _ domain.IssueUseCase = (*fakeIssueUseCase)(nil)

// landingIssues reports every close as landed, so a batch earns its claim.
type landingIssues struct{ *fakeIssueUseCase }

func (u landingIssues) CloseIssueChecked(ctx context.Context, id string, params domain.CloseIssueParams, actor string, force bool) (domain.CloseIssueResult, error) {
	if _, err := u.fakeIssueUseCase.CloseIssueChecked(ctx, id, params, actor, force); err != nil {
		return domain.CloseIssueResult{}, err
	}
	return domain.CloseIssueResult{Closed: true}, nil
}

// TestUOWBatchCloserAppliesThePolicyExactlyOnce pins the unit-of-work arm's
// batch closer to ONE application of the policy per call: one external-edge
// read serves the close guard and the ClaimNext narrowing, with and without a
// flagged item, and the claim the batch earns skips the externally blocked
// issue. The inner closer runs over the UNDECORATED provider (plus, when an
// item is flagged, the close-only batchCloseGuard); built over the policy
// provider, the claim's ClaimReadyIssue would hit the policy's use-case
// override and read the edges a second time, inside the write transaction.
func TestUOWBatchCloserAppliesThePolicyExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []string
	}{
		{"no flagged item", []string{"be-done"}},
		{"with a flagged item", []string{"be-done", "be-held"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done, held, next := issue("be-done"), issue("be-held"), issue("be-next")
			held.Priority, next.Priority = 0, 1
			base := &fakeIssueUseCase{ready: []*types.Issue{held, next, done}}
			deps := &countingDependencyUseCase{fakeDependencyUseCase: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
				held.ID: {externalDep(held.ID, "external:remote:payments", types.DepBlocks)},
			}}}
			inner := &fakeUOW{issues: landingIssues{base}, deps: deps}
			provider := WrapUOWProvider(&openCountingProvider{uw: inner}, func(ProjectName) (string, bool) { return "", false }, nil)
			closer, err := provider.(uow.BatchCloserSource).BatchCloser()
			if err != nil {
				t.Fatal(err)
			}
			items := make([]publicops.BatchCloseItem, 0, len(tc.items))
			for _, id := range tc.items {
				items = append(items, publicops.BatchCloseItem{IssueID: id})
			}
			result, err := closer.CloseBatch(t.Context(), publicops.CloseBatchRequest{
				Actor: "w", Items: items, ClaimNext: &publicops.ReadyRequest{Sort: "priority"},
			})
			if err != nil {
				t.Fatal(err)
			}
			assertReads(t, "CloseBatch+ClaimNext", deps, 1)
			if result.ClaimedNext == nil || result.ClaimedNext.ID == held.ID {
				t.Errorf("claimed %+v, want a claim that skips the externally blocked %s", result.ClaimedNext, held.ID)
			}
			if len(tc.items) > 1 && !errors.Is(result.Outcomes[1].Err, storage.ErrCloseBlocked) {
				t.Errorf("flagged item outcome = %+v, want the external refusal", result.Outcomes[1])
			}
		})
	}
}
