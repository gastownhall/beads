package externaldeps

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// openCountingProvider hands out one fake unit of work and counts how many are
// open, so a foreign-store open can be caught happening inside one.
type openCountingProvider struct {
	uw   *fakeUOW
	open int
}

func (p *openCountingProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.open++
	return &openCountingUOW{fakeUOW: p.uw, p: p}, nil
}

func (p *openCountingProvider) Close(context.Context) error { return nil }

type openCountingUOW struct {
	*fakeUOW
	p      *openCountingProvider
	closed bool
}

// LabelUseCase answers the lifecycle's pre-image hydration with no labels.
func (u *openCountingUOW) LabelUseCase() domain.LabelUseCase { return noLabels{} }

type noLabels struct{ domain.LabelUseCase }

func (noLabels) GetLabels(context.Context, string) ([]string, error)     { return nil, nil }
func (noLabels) GetWispLabels(context.Context, string) ([]string, error) { return nil, nil }

func (u *openCountingUOW) Close(ctx context.Context) {
	if !u.closed {
		u.closed = true
		u.p.open--
	}
}

// edgesAfterFirstRead answers no edges on its first read of the target's own
// records and the configured ones afterwards: an edge committed between a
// mutation's pre-resolution and its write transaction. A guard on one issue
// reads only that issue's edges, so a read of the whole workspace's is counted
// separately and must not happen.
type edgesAfterFirstRead struct {
	*fakeDependencyUseCase
	reads          int
	workspaceReads int
}

func (d *edgesAfterFirstRead) GetIssueDependencyRecords(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	d.reads++
	if d.reads == 1 {
		return map[string][]*types.Dependency{}, nil
	}
	return d.fakeDependencyUseCase.GetIssueDependencyRecords(ctx, ids)
}

func (d *edgesAfterFirstRead) GetExternalBlockingDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	d.workspaceReads++
	return d.fakeDependencyUseCase.GetExternalBlockingDependencyRecords(ctx)
}

// TestGuardedMutationsResolveForeignProjectsOutsideTheWriteTransaction pins
// the fix for foreign resolution inside a claim's write transaction: the
// claim-by-id role, the lifecycle's Claim update and its close each resolve
// the target's `external:` refs — opening the foreign project's store — with
// NO unit of work open, and the check inside the transaction only looks the
// refs up. A satisfied ref still claims; an unsatisfied one still refuses.
func TestGuardedMutationsResolveForeignProjectsOutsideTheWriteTransaction(t *testing.T) {
	blocked, provided := issue("be-blocked"), issue("be-provided")
	issues := &fakeIssueUseCase{ready: []*types.Issue{blocked, provided}}
	inner := &fakeUOW{issues: issues, deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID:  {externalDep(blocked.ID, "external:remote:missing", types.DepBlocks)},
		provided.ID: {externalDep(provided.ID, "external:remote:payments", types.DepBlocks)},
	}}}
	counting := &openCountingProvider{uw: inner}
	foreign := &fakeStore{labels: map[string][]*types.Issue{
		"provides:payments": {{ID: "fx-1", Status: types.StatusClosed}},
	}}
	opens, opensInsideAUOW := 0, 0
	provider := WrapUOWProvider(counting,
		func(project ProjectName) (string, bool) { return "/projects/remote", project == "remote" },
		func(context.Context, string) (storage.DoltStorage, error) {
			opens++
			if counting.open != 0 {
				opensInsideAUOW++
			}
			return foreign, nil
		})
	ctx := t.Context()

	claimer, err := provider.(uow.IssueClaimerSource).IssueClaimer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimer.Claim(ctx, publicops.ClaimRequest{IssueID: blocked.ID, Actor: "w"}); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Fatalf("claim of %s (unsatisfied): err = %v, want the external refusal", blocked.ID, err)
	}
	if _, err := claimer.Claim(ctx, publicops.ClaimRequest{IssueID: provided.ID, Actor: "w"}); err != nil {
		t.Fatalf("claim of %s (satisfied): %v", provided.ID, err)
	}

	lifecycle, err := provider.(uow.IssueLifecycleSource).IssueLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Update(ctx, publicops.UpdateRequest{IssueID: blocked.ID, Actor: "w", Claim: true}); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Fatalf("Update(Claim) of %s: err = %v, want the external refusal", blocked.ID, err)
	}
	if _, err := lifecycle.Close(ctx, publicops.CloseRequest{IssueID: blocked.ID, Actor: "w"}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("Close of %s: err = %v, want the external refusal", blocked.ID, err)
	}

	if opens == 0 {
		t.Fatal("no foreign project was opened; the case proves nothing")
	}
	if opensInsideAUOW != 0 {
		t.Errorf("%d of %d foreign-store opens happened with a unit of work open; resolution must precede the write transaction", opensInsideAUOW, opens)
	}
	if counting.open != 0 {
		t.Errorf("%d units of work left open", counting.open)
	}
}

// TestGuardedClaimFailsClosedOnAnEdgeResolutionDidNotSee pins the window the
// pre-resolution leaves: an `external:` edge committed after the refs were
// resolved but before the claim's transaction read them is unsatisfied, not
// ignored.
func TestGuardedClaimFailsClosedOnAnEdgeResolutionDidNotSee(t *testing.T) {
	target := issue("be-late")
	deps := &edgesAfterFirstRead{fakeDependencyUseCase: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		target.ID: {externalDep(target.ID, "external:remote:payments", types.DepBlocks)},
	}}}
	inner := &fakeUOW{issues: &fakeIssueUseCase{ready: []*types.Issue{target}}, deps: deps}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) { return "", false }, nil)
	claimer, err := provider.(uow.IssueClaimerSource).IssueClaimer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimer.Claim(t.Context(), publicops.ClaimRequest{IssueID: target.ID, Actor: "w"}); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Fatalf("claim with a late edge: err = %v, want the external refusal", err)
	}
	if target.Assignee != "" {
		t.Fatalf("%s was claimed past an edge the transaction saw", target.ID)
	}
	if deps.reads != 2 {
		t.Errorf("reads of the claimed issue's edges = %d, want 2 (pre-resolution, then the transaction's own)", deps.reads)
	}
	if deps.workspaceReads != 0 {
		t.Errorf("a single-id claim read the whole workspace's external edges %d times, want 0", deps.workspaceReads)
	}
}

// TestSingleIDGuardsReadOnlyTheTargetsEdges pins the unit-of-work arm's
// single-id guards — the claim-by-id role, the lifecycle's claim and close,
// and a PreResolve'd step claim — to the target's OWN edges: the
// pre-resolution and the in-transaction check each read that issue's records,
// and neither reads the whole workspace's external edges, which each
// used to scan for one id.
func TestSingleIDGuardsReadOnlyTheTargetsEdges(t *testing.T) {
	blocked := issue("be-blocked")
	deps := &countingDependencyUseCase{fakeDependencyUseCase: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
		"be-other": {externalDep("be-other", "external:remote:other", types.DepBlocks)},
	}}}
	inner := &fakeUOW{issues: &fakeIssueUseCase{ready: []*types.Issue{blocked}}, deps: deps}
	provider := WrapUOWProvider(&openCountingProvider{uw: inner}, func(ProjectName) (string, bool) { return "", false }, nil)
	ctx := t.Context()

	claimer, err := provider.(uow.IssueClaimerSource).IssueClaimer()
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := provider.(uow.IssueLifecycleSource).IssueLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
		want error
	}{
		{"Claim", func() error {
			_, err := claimer.Claim(ctx, publicops.ClaimRequest{IssueID: blocked.ID, Actor: "w"})
			return err
		}, storage.ErrClaimBlocked},
		{"Update(Claim)", func() error {
			_, err := lifecycle.Update(ctx, publicops.UpdateRequest{IssueID: blocked.ID, Actor: "w", Claim: true})
			return err
		}, storage.ErrClaimBlocked},
		{"Close", func() error {
			_, err := lifecycle.Close(ctx, publicops.CloseRequest{IssueID: blocked.ID, Actor: "w"})
			return err
		}, storage.ErrCloseBlocked},
		{"PreResolve + ClaimIssueIfOpen", func() error {
			prepared, err := PreResolve(ctx, provider, blocked.ID)
			if err != nil {
				return err
			}
			uw, err := prepared.NewUOW(ctx)
			if err != nil {
				return err
			}
			defer uw.Close(ctx)
			_, err = uw.IssueUseCase().ClaimIssueIfOpen(ctx, blocked.ID, "w")
			return err
		}, storage.ErrClaimBlocked},
	} {
		deps.reads, deps.ownReads = 0, 0
		if err := tc.run(); !errors.Is(err, tc.want) {
			t.Fatalf("%s of externally blocked %s: err = %v, want %v", tc.name, blocked.ID, err, tc.want)
		}
		if deps.reads != 0 {
			t.Errorf("%s read the whole workspace's external edges %d times, want 0", tc.name, deps.reads)
		}
		// At least the pre-resolution and the transaction's check; the
		// lifecycle's pre-image hydration reads the same records once more.
		if deps.ownReads < 2 {
			t.Errorf("%s read the target's own edges %d times, want at least 2 (pre-resolution, then the transaction's check)", tc.name, deps.ownReads)
		}
	}
}
