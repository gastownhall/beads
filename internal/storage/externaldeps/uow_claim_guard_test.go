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

func (u *fakeIssueUseCase) ClaimIssue(_ context.Context, id, actor string) (domain.ClaimResult, error) {
	for _, issue := range u.ready {
		if issue.ID == id {
			issue.Assignee = actor
			issue.Status = types.StatusInProgress
			return domain.ClaimResult{}, nil
		}
	}
	return domain.ClaimResult{}, publicops.ErrNotFound
}

// TestUOWIssueClaimerRefusesExternallyBlockedWork pins claim-by-id on the
// unit-of-work arm to what the store arm has always done: an issue held back
// by an unsatisfied external blocker refuses with ErrClaimBlocked, and an
// unblocked one claims. It drives the provider's IssueClaimer accessor; the
// guard itself is the issueUseCase.ClaimIssue override, so
// TestUOWRawClaimIssueRefusesExternallyBlockedWork drives the same refusal
// through a raw unit of work, and internal/httpapi's
// TestServedClaimRefusesExternallyBlockedWork through serve's claim endpoint.
func TestUOWIssueClaimerRefusesExternallyBlockedWork(t *testing.T) {
	blocked, ready := issue("be-blocked"), issue("be-ready")
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{ready: []*types.Issue{blocked, ready}},
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) { return "", false }, nil)
	claimer, err := provider.(uow.IssueClaimerSource).IssueClaimer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimer.Claim(t.Context(), publicops.ClaimRequest{IssueID: blocked.ID, Actor: "w"}); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Fatalf("claim of externally blocked %s: err = %v, want ErrClaimBlocked", blocked.ID, err)
	}
	if blocked.Assignee != "" {
		t.Fatalf("externally blocked %s was claimed by %q", blocked.ID, blocked.Assignee)
	}
	if _, err := claimer.Claim(t.Context(), publicops.ClaimRequest{IssueID: ready.ID, Actor: "w"}); err != nil {
		t.Fatalf("claim of unblocked %s: %v", ready.ID, err)
	}
	if ready.Assignee != "w" {
		t.Fatalf("unblocked %s assignee = %q, want w", ready.ID, ready.Assignee)
	}
}

// TestUOWRawClaimIssueRefusesExternallyBlockedWork drives the override with no
// role in between — the path a raw-UOW caller, and any role built over this
// provider, reaches — and pins that it reads only the claimed issue's own
// edges, once, in the claim's own unit of work: never the workspace's.
func TestUOWRawClaimIssueRefusesExternallyBlockedWork(t *testing.T) {
	blocked := issue("be-blocked")
	deps := &countingDependencyUseCase{fakeDependencyUseCase: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
	}}}
	inner := &fakeUOW{issues: &fakeIssueUseCase{ready: []*types.Issue{blocked}}, deps: deps}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) { return "", false }, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uw.IssueUseCase().ClaimIssue(t.Context(), blocked.ID, "w"); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Fatalf("raw ClaimIssue of externally blocked %s: err = %v, want ErrClaimBlocked", blocked.ID, err)
	}
	if blocked.Assignee != "" {
		t.Fatalf("externally blocked %s was claimed by %q", blocked.ID, blocked.Assignee)
	}
	if deps.ownReads != 1 {
		t.Errorf("ClaimIssue read the claimed issue's own edges %d times, want 1", deps.ownReads)
	}
	assertReads(t, "ClaimIssue", deps, 0)
}

// TestUpdateClaimRefusesExternallyBlockedWorkOnBothArms pins `bd update
// --claim`'s path — Lifecycle.Update with Claim set — on both arms. On the
// unit-of-work arm it is ApplyUpdate, whose undecorated body claims through its
// OWN ClaimIssue and so never reached the ClaimIssue override; on the store arm
// it is the policy lifecycle's Update, which used to guard only a close. A
// blocked claim refuses and reaches nothing; an unblocked one goes through; a
// ForceClosePolicy (what --force sets) does not bypass the claim guard.
func TestUpdateClaimRefusesExternallyBlockedWorkOnBothArms(t *testing.T) {
	locate := func(ProjectName) (string, bool) { return "", false }
	edges := func(id string) map[string][]*types.Dependency {
		return map[string][]*types.Dependency{id: {externalDep(id, "external:remote:payments", types.DepBlocks)}}
	}

	t.Run("unit-of-work arm", func(t *testing.T) {
		blocked, ready := issue("be-blocked"), issue("be-ready")
		issues := &fakeIssueUseCase{ready: []*types.Issue{blocked, ready}}
		inner := &fakeUOW{issues: issues, deps: &fakeDependencyUseCase{external: edges(blocked.ID)}}
		provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, locate, nil)
		uw, err := provider.NewUOW(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := uw.IssueUseCase().ApplyUpdate(t.Context(), blocked.ID, domain.UpdateSpec{Claim: true}, "w"); !errors.Is(err, storage.ErrClaimBlocked) {
			t.Fatalf("ApplyUpdate(Claim) of externally blocked %s: err = %v, want the external refusal", blocked.ID, err)
		}
		if slices.Contains(issues.closed, blocked.ID) {
			t.Fatalf("the refused claim reached the undecorated ApplyUpdate: %v", issues.closed)
		}
		if _, err := uw.IssueUseCase().ApplyUpdate(t.Context(), ready.ID, domain.UpdateSpec{Claim: true}, "w"); err != nil {
			t.Fatalf("ApplyUpdate(Claim) of unblocked %s: %v", ready.ID, err)
		}
		if _, err := uw.IssueUseCase().ClaimWisp(t.Context(), blocked.ID, "w"); !errors.Is(err, storage.ErrClaimBlocked) {
			t.Fatalf("ClaimWisp of externally blocked %s: err = %v, want the external refusal", blocked.ID, err)
		}
	})

	t.Run("store arm", func(t *testing.T) {
		blocked, ready := issue("be-blocked"), issue("be-ready")
		lifecycle := &fakeLifecycle{}
		raw := &fakeStore{ready: []*types.Issue{blocked, ready}, lifecycle: lifecycle, deps: edges(blocked.ID)}
		ops, err := testStore(raw, &fakeStore{}, false).IssueLifecycle()
		if err != nil {
			t.Fatal(err)
		}
		for _, force := range []bool{false, true} {
			if _, err := ops.Update(t.Context(), publicops.UpdateRequest{IssueID: blocked.ID, Actor: "w", Claim: true, ForceClosePolicy: force}); !errors.Is(err, storage.ErrClaimBlocked) {
				t.Fatalf("Update(Claim, force=%v) of externally blocked %s: err = %v, want the external refusal", force, blocked.ID, err)
			}
		}
		if lifecycle.updated != 0 {
			t.Fatalf("a refused claim reached the inner lifecycle %d times", lifecycle.updated)
		}
		if _, err := ops.Update(t.Context(), publicops.UpdateRequest{IssueID: ready.ID, Actor: "w", Claim: true}); err != nil {
			t.Fatalf("Update(Claim) of unblocked %s: %v", ready.ID, err)
		}
		if lifecycle.updated != 1 {
			t.Fatalf("unblocked claim reached the inner lifecycle %d times, want 1", lifecycle.updated)
		}
	})
}

func (u *fakeIssueUseCase) ClaimWisp(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	return u.ClaimIssue(ctx, id, actor)
}

type recordingClaimer struct{ claimed []string }

func (c *recordingClaimer) Claim(_ context.Context, req publicops.ClaimRequest) (publicops.ClaimResult, error) {
	c.claimed = append(c.claimed, req.IssueID)
	return publicops.ClaimResult{}, nil
}

type claimerStore struct {
	*fakeStore
	claimer *recordingClaimer
}

func (s claimerStore) IssueClaimer() (publicops.Claimer, error) { return s.claimer, nil }

// TestClaimByIDRefusesTheSameSetOnBothArms pins the claim-by-id alignment: the
// store arm's claimer used to ask IsBlocked, which counts LOCAL blockers too,
// so serve's store arm refused a claim the provider arm, the CLI and the
// unpoliced backend allow. Both arms now refuse external blockers only, with
// ErrClaimBlocked (which wraps ErrNotClaimable) rather than the close refusal.
func TestClaimByIDRefusesTheSameSetOnBothArms(t *testing.T) {
	external, local := issue("be-external"), issue("be-local")
	deps := map[string][]*types.Dependency{
		external.ID: {externalDep(external.ID, "external:remote:payments", types.DepBlocks)},
	}

	// Store arm. The fake's IsBlocked reports a LOCAL blocker for every id.
	inner := &recordingClaimer{}
	raw := claimerStore{fakeStore: &fakeStore{ready: []*types.Issue{external, local}, deps: deps, isBlocked: true, blockerIDs: []string{"be-local-blocker"}}, claimer: inner}
	store := New(raw, func(ProjectName) (string, bool) { return "", false }, nil)
	claimer, err := store.IssueClaimer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimer.Claim(t.Context(), publicops.ClaimRequest{IssueID: external.ID, Actor: "w"}); !errors.Is(err, storage.ErrClaimBlocked) || !errors.Is(err, storage.ErrNotClaimable) || errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("store arm, external blocker: err = %v, want ErrClaimBlocked (and ErrNotClaimable, not ErrCloseBlocked)", err)
	}
	if _, err := claimer.Claim(t.Context(), publicops.ClaimRequest{IssueID: local.ID, Actor: "w"}); err != nil {
		t.Fatalf("store arm, local blocker only: %v, want the claim to go through as on the provider arm", err)
	}
	if !slices.Equal(inner.claimed, []string{local.ID}) {
		t.Fatalf("store arm claims that reached the backend = %v, want [%s]", inner.claimed, local.ID)
	}

	// Unit-of-work arm, same workspace shape: only the external blocker refuses.
	issues := &fakeIssueUseCase{ready: []*types.Issue{external, local}}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: &fakeUOW{issues: issues, deps: &fakeDependencyUseCase{external: deps}}},
		func(ProjectName) (string, bool) { return "", false }, nil)
	uowClaimer, err := provider.(uow.IssueClaimerSource).IssueClaimer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uowClaimer.Claim(t.Context(), publicops.ClaimRequest{IssueID: external.ID, Actor: "w"}); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Fatalf("uow arm, external blocker: err = %v, want ErrClaimBlocked", err)
	}
	if _, err := uowClaimer.Claim(t.Context(), publicops.ClaimRequest{IssueID: local.ID, Actor: "w"}); err != nil {
		t.Fatalf("uow arm, no external blocker: %v", err)
	}
}

// wrappingUOW stands for an outer unit-of-work decorator (the notifying
// layer) over the policy's.
type wrappingUOW struct{ uow.UnitOfWork }

func (w wrappingUOW) Unwrap() uow.UnitOfWork { return w.UnitOfWork }

// TestGuardClaimInUOWAnswersWithoutClaiming pins the check `bd close --continue
// --no-auto` makes before suggesting a step: ErrClaimBlocked for a step an
// unsatisfied `external:` blocker holds, nil for a free one, found through an
// outer decorator, with nothing claimed and only the step's own edges read. A
// unit of work without the policy answers nil.
func TestGuardClaimInUOWAnswersWithoutClaiming(t *testing.T) {
	blocked, free := issue("be-blocked"), issue("be-free")
	deps := &countingDependencyUseCase{fakeDependencyUseCase: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
	}}}
	inner := &fakeUOW{issues: &fakeIssueUseCase{ready: []*types.Issue{blocked, free}}, deps: deps}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) { return "", false }, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := GuardClaimInUOW(t.Context(), wrappingUOW{uw}, blocked.ID); !errors.Is(err, storage.ErrClaimBlocked) {
		t.Errorf("GuardClaimInUOW(%s) = %v, want ErrClaimBlocked", blocked.ID, err)
	}
	if err := GuardClaimInUOW(t.Context(), wrappingUOW{uw}, free.ID); err != nil {
		t.Errorf("GuardClaimInUOW(%s) = %v, want nil", free.ID, err)
	}
	if blocked.Assignee != "" || free.Assignee != "" {
		t.Errorf("the check claimed: %s=%q %s=%q", blocked.ID, blocked.Assignee, free.ID, free.Assignee)
	}
	if !slices.EqualFunc(deps.ownIDs, [][]string{{blocked.ID}, {free.ID}}, slices.Equal) {
		t.Errorf("own-edge reads %v, want each step's own", deps.ownIDs)
	}
	assertReads(t, "GuardClaimInUOW", deps, 0)
	if err := GuardClaimInUOW(t.Context(), inner, blocked.ID); err != nil {
		t.Errorf("GuardClaimInUOW on an unpoliced unit of work = %v, want nil", err)
	}
}
