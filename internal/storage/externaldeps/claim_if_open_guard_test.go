package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (u *fakeIssueUseCase) ClaimIssueIfOpen(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	return u.ClaimIssue(ctx, id, actor)
}

func (u *fakeIssueUseCase) ClaimWispIfOpen(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	return u.ClaimWisp(ctx, id, actor)
}

// TestUOWClaimIfOpenRefusesExternallyBlockedWork pins the molecule port's step
// claim (ClaimIssueIfOpen / ClaimWispIfOpen, reached by `bd close --continue`
// over a proxied server): a step an unsatisfied `external:` blocker holds is
// refused with the claim-by-id refusal on both planes — wisps carry external
// edges too — and an unblocked step claims.
func TestUOWClaimIfOpenRefusesExternallyBlockedWork(t *testing.T) {
	blocked, free := issue("be-blocked"), issue("be-free")
	blockedWisp, freeWisp := issue("be-wisp-blocked"), issue("be-wisp-free")
	issues := &fakeIssueUseCase{ready: []*types.Issue{blocked, free, blockedWisp, freeWisp}, wisps: []*types.Issue{blockedWisp, freeWisp}}
	inner := &fakeUOW{issues: issues, deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID:     {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
		blockedWisp.ID: {externalDep(blockedWisp.ID, "external:remote:payments", types.DepBlocks)},
	}}}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) { return "", false }, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	use := uw.IssueUseCase()
	ctx := t.Context()
	if _, err := use.ClaimIssueIfOpen(ctx, blocked.ID, "w"); !errors.Is(err, storage.ErrClaimBlocked) || !errors.Is(err, storage.ErrNotClaimable) {
		t.Errorf("ClaimIssueIfOpen(%s): err = %v, want ErrClaimBlocked (an ErrNotClaimable)", blocked.ID, err)
	}
	if _, err := use.ClaimWispIfOpen(ctx, blockedWisp.ID, "w"); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Errorf("ClaimWispIfOpen(%s): err = %v, want ErrClaimBlocked", blockedWisp.ID, err)
	}
	if blocked.Assignee != "" || blockedWisp.Assignee != "" {
		t.Fatalf("an externally blocked step was claimed (%q, %q)", blocked.Assignee, blockedWisp.Assignee)
	}
	if _, err := use.ClaimIssueIfOpen(ctx, free.ID, "w"); err != nil {
		t.Errorf("ClaimIssueIfOpen(%s): %v", free.ID, err)
	}
	if _, err := use.ClaimWispIfOpen(ctx, freeWisp.ID, "w"); err != nil {
		t.Errorf("ClaimWispIfOpen(%s): %v", freeWisp.ID, err)
	}
}

// TestPreResolveKeepsForeignIOOutOfTheWriteTransaction pins PreResolve, the
// hook `bd close --continue` over a proxied server uses before its post-close
// transaction: the candidate steps' `external:` refs are resolved with no unit
// of work open, so the step claim inside the transaction opens no foreign
// store; a satisfied ref still claims and an unsatisfied one still refuses. A
// provider without the policy is returned unchanged.
func TestPreResolveKeepsForeignIOOutOfTheWriteTransaction(t *testing.T) {
	blocked, provided := issue("be-blocked"), issue("be-provided")
	issues := &fakeIssueUseCase{ready: []*types.Issue{blocked, provided}}
	inner := &fakeUOW{issues: issues, deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID:  {externalDep(blocked.ID, "external:remote:missing", types.DepBlocks)},
		provided.ID: {externalDep(provided.ID, "external:remote:payments", types.DepBlocks)},
	}}}
	counting := &openCountingProvider{uw: inner}
	foreign := &fakeStore{labels: map[string][]*types.Issue{"provides:payments": {{ID: "fx-1", Status: types.StatusClosed}}}}
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
	prepared, err := PreResolve(ctx, provider, blocked.ID, provided.ID)
	if err != nil {
		t.Fatal(err)
	}
	uw, err := prepared.NewUOW(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uw.IssueUseCase().ClaimIssueIfOpen(ctx, blocked.ID, "w"); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Errorf("ClaimIssueIfOpen(%s): err = %v, want the external refusal", blocked.ID, err)
	}
	if _, err := uw.IssueUseCase().ClaimIssueIfOpen(ctx, provided.ID, "w"); err != nil {
		t.Errorf("ClaimIssueIfOpen(%s) (satisfied): %v", provided.ID, err)
	}
	uw.Close(ctx)
	if opens == 0 {
		t.Fatal("no foreign project was opened; the case proves nothing")
	}
	if opensInsideAUOW != 0 {
		t.Errorf("%d of %d foreign-store opens happened with a unit of work open", opensInsideAUOW, opens)
	}

	plain := &fakeUOWProvider{uw: inner}
	if got, err := PreResolve(ctx, plain, blocked.ID); err != nil || got != plain {
		t.Errorf("PreResolve on an unpoliced provider = (%v, %v), want it unchanged", got, err)
	}
}

// unwrappingStore is a decorator above the policy, as HookFiringStore is on
// the direct route's chain.
type unwrappingStore struct{ storage.DoltStorage }

func (s unwrappingStore) Unwrap() storage.DoltStorage { return s.DoltStorage }

// TestGuardClaimFindsThePolicyBeneathDecorators pins GuardClaim, the direct
// route's step-claim guard (storeMolWriter.ClaimStepIfOpen): it finds the
// policy beneath the decorators above it, refuses an externally blocked id
// with the claim refusal, lets an unblocked one through, and answers nil for a
// chain with no policy.
func TestGuardClaimFindsThePolicyBeneathDecorators(t *testing.T) {
	blocked, free := issue("be-blocked"), issue("be-free")
	raw := &fakeStore{
		ready: []*types.Issue{blocked, free},
		deps:  map[string][]*types.Dependency{blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)}},
	}
	chain := unwrappingStore{DoltStorage: testStore(raw, &fakeStore{}, true)}
	ctx := t.Context()
	if err := GuardClaim(ctx, chain, blocked.ID); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Errorf("GuardClaim(%s): err = %v, want ErrClaimBlocked", blocked.ID, err)
	}
	if err := GuardClaim(ctx, chain, free.ID); err != nil {
		t.Errorf("GuardClaim(%s): %v", free.ID, err)
	}
	if err := GuardClaim(ctx, unwrappingStore{DoltStorage: raw}, blocked.ID); err != nil {
		t.Errorf("GuardClaim over an unpoliced chain: %v, want nil", err)
	}
	if slices.Contains(raw.claimed, blocked.ID) {
		t.Fatal("GuardClaim claimed")
	}
}
