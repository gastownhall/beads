package externaldeps

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The unit-of-work roles run whole transactions, so the fakes grow just the
// methods a read, a count and a claim reach.

func (u *fakeUOW) Close(context.Context)                                        {}
func (u *fakeUOW) Commit(context.Context, string) error                         { return nil }
func (u *fakeUOW) CommentUseCase() domain.CommentUseCase                        { return fakeCommentUseCase{} }
func (u *fakeIssueUseCase) WakeExpiredDefers(context.Context) (int, int, error) { return 0, 0, nil }

func (u *fakeIssueUseCase) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	page, err := u.GetReadyWork(ctx, filter)
	if err != nil {
		return domain.SearchCountsPage{}, err
	}
	items := make([]*types.IssueWithCounts, 0, len(page.Items))
	for _, issue := range page.Items {
		items = append(items, &types.IssueWithCounts{Issue: issue})
	}
	return domain.SearchCountsPage{Items: items}, nil
}

func (u *fakeIssueUseCase) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (domain.ClaimReadyResult, error) {
	page, err := u.GetReadyWork(ctx, filter)
	if err != nil {
		return domain.ClaimReadyResult{}, err
	}
	for _, issue := range page.Items {
		if issue.Assignee == "" {
			issue.Assignee = actor
			return domain.ClaimReadyResult{Issue: issue, Claimed: true}, nil
		}
	}
	return domain.ClaimReadyResult{}, nil
}

func (u *fakeDependencyUseCase) CountsByIssueIDs(context.Context, []string) (map[string]*types.DependencyCounts, error) {
	return map[string]*types.DependencyCounts{}, nil
}

func (u *fakeDependencyUseCase) GetForIssueIDs(context.Context, []string) (map[string][]*types.Dependency, error) {
	return map[string][]*types.Dependency{}, nil
}

type fakeCommentUseCase struct{ domain.CommentUseCase }

func (fakeCommentUseCase) GetCommentCounts(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

// TestUOWReadyRolesApplyThePolicyOnce pins the unit-of-work arm of the shared
// role wrappers: each call reads the external edges exactly once (a role
// rebuilt over the policy provider would read them twice — once in the
// wrapper, once in the use-case override), and the externally blocked row is
// excluded from the page, the count and the claim.
func TestUOWReadyRolesApplyThePolicyOnce(t *testing.T) {
	blocked, ready := issue("be-blocked"), issue("be-ready")
	deps := &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
	}}
	counting := &countingDependencyUseCase{fakeDependencyUseCase: deps}
	inner := &fakeUOW{issues: &fakeIssueUseCase{ready: []*types.Issue{blocked, ready}}, deps: counting}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) { return "", false }, nil)

	reader, err := provider.(uow.IssueReaderSource).IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	counter, err := provider.(uow.ReadyCounterSource).ReadyCounter()
	if err != nil {
		t.Fatal(err)
	}
	claimer, err := provider.(uow.ReadyClaimerSource).ReadyClaimer()
	if err != nil {
		t.Fatal(err)
	}

	counting.reads = 0
	page, err := reader.Ready(t.Context(), publicops.ReadyRequest{Sort: "priority"})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ids := pageIDs(page); !slices.Equal(ids, []string{ready.ID}) {
		t.Fatalf("Ready = %v, want [%s]", ids, ready.ID)
	}
	assertReads(t, "Ready", counting, 1)

	total, err := counter.CountReady(t.Context(), publicops.ReadyRequest{Sort: "priority"})
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if total.Total != 1 {
		t.Fatalf("CountReady = %d, want 1", total.Total)
	}
	assertReads(t, "CountReady", counting, 1)

	lister, err := provider.(uow.ReadyListerSource).ReadyLister()
	if err != nil {
		t.Fatal(err)
	}
	listing, err := lister.ListReady(t.Context(), publicops.ReadyListRequest{ReadyRequest: publicops.ReadyRequest{Sort: "priority"}})
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if ids := pageIDs(publicops.IssuePage{Items: listing.Items}); !slices.Equal(ids, []string{ready.ID}) || listing.Total != 1 || listing.HasMore {
		t.Fatalf("ListReady = %v (total=%d more=%v), want [%s] total 1", ids, listing.Total, listing.HasMore, ready.ID)
	}
	assertReads(t, "ListReady", counting, 1)

	claimed, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "w", Filter: publicops.ReadyRequest{Sort: "priority"}})
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.Claimed == nil || claimed.Claimed.ID != ready.ID {
		t.Fatalf("ClaimNext = %v, want %s", claimed.Claimed, ready.ID)
	}
	assertReads(t, "ClaimNext", counting, 1)
}

type countingDependencyUseCase struct {
	*fakeDependencyUseCase
	reads int
	// ownReads counts reads of named issues' edges (GetIssueDependencyRecords).
	ownReads int
	// ownIDs records the ids of every such read, in order.
	ownIDs [][]string
	// onOwnRead, when set, sees every such read's ids.
	onOwnRead func(ids []string)
}

func (c *countingDependencyUseCase) GetIssueDependencyRecords(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	c.ownReads++
	c.ownIDs = append(c.ownIDs, slices.Clone(ids))
	if c.onOwnRead != nil {
		c.onOwnRead(ids)
	}
	return c.fakeDependencyUseCase.GetIssueDependencyRecords(ctx, ids)
}

func (c *countingDependencyUseCase) GetExternalBlockingDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	c.reads++
	return c.fakeDependencyUseCase.GetExternalBlockingDependencyRecords(ctx)
}

func assertReads(t *testing.T, op string, c *countingDependencyUseCase, want int) {
	t.Helper()
	if c.reads != want {
		t.Errorf("%s read the external edges %d times, want %d", op, c.reads, want)
	}
	c.reads = 0
}
