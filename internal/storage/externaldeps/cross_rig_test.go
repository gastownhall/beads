package externaldeps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// fakeIssueResolver stands in for the cross-rig route lookup: it answers each
// asked id from issues, else from unavailable, and records every call.
type fakeIssueResolver struct {
	issues      map[string]*types.Issue
	unavailable map[string]string
	calls       [][]string
}

func (r *fakeIssueResolver) resolve(_ context.Context, ids []string) (map[string]*types.Issue, map[string]string, error) {
	r.calls = append(r.calls, slices.Clone(ids))
	found := make(map[string]*types.Issue)
	unavailable := make(map[string]string)
	for _, id := range ids {
		if issue, ok := r.issues[id]; ok {
			found[id] = issue
			continue
		}
		if reason, ok := r.unavailable[id]; ok {
			unavailable[id] = reason
		}
	}
	return found, unavailable, nil
}

func foreignIssue(id string, status types.Status) *types.Issue {
	return &types.Issue{ID: id, Title: "foreign " + id, Status: status, IssueType: types.TypeTask}
}

// crossRigStore is the direct decorator over raw with resolver as its
// cross-rig route lookup and no external_projects configured.
func crossRigStore(raw *fakeStore, resolver *fakeIssueResolver) *Store {
	return New(raw, func(ProjectName) (string, bool) { return "", false }, nil, WithIssueResolver(resolver.resolve))
}

// crossRigProvider is the bd serve unit-of-work arm over inner with resolver
// as its cross-rig route lookup.
func crossRigProvider(t *testing.T, inner *fakeUOW, resolver *fakeIssueResolver) *unitOfWork {
	t.Helper()
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil, WithIssueResolver(resolver.resolve))
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	return uw.(*unitOfWork)
}

// GH#5784 AC-1: a bare cross-rig blocks or conditional-blocks edge to an open
// foreign issue keeps its source out of ready work and lists it blocked by
// that id, on the direct store and on bd serve's unit-of-work arm.
func TestCrossRigOpenBlockerExcludesSourceFromReady(t *testing.T) {
	for _, depType := range []types.DependencyType{types.DepBlocks, types.DepConditionalBlocks} {
		t.Run(string(depType)+"/direct", func(t *testing.T) {
			source, other := issue("aa-1"), issue("aa-2")
			raw := &fakeStore{
				ready: []*types.Issue{source, other},
				deps: map[string][]*types.Dependency{
					source.ID: {externalDep(source.ID, "bb-1", depType)},
				},
			}
			resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": foreignIssue("bb-1", types.StatusOpen)}}
			store := crossRigStore(raw, resolver)

			ready, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if ids := issueIDs(ready); !slices.Equal(ids, []string{"aa-2"}) {
				t.Fatalf("ready IDs = %v, want [aa-2]", ids)
			}
			blocked, err := store.GetBlockedIssues(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetBlockedIssues: %v", err)
			}
			if len(blocked) != 1 || blocked[0].ID != "aa-1" || !slices.Equal(blocked[0].BlockedBy, []string{"bb-1"}) {
				t.Fatalf("blocked = %+v, want aa-1 blocked by [bb-1]", blocked)
			}
			isBlocked, blockers, err := store.IsBlocked(t.Context(), "aa-1")
			if err != nil || !isBlocked || !slices.Equal(blockers, []string{"bb-1"}) {
				t.Fatalf("IsBlocked = %v %v %v, want true [bb-1]", isBlocked, blockers, err)
			}
		})
		t.Run(string(depType)+"/served", func(t *testing.T) {
			source, other := issue("aa-1"), issue("aa-2")
			inner := &fakeUOW{
				issues: &fakeIssueUseCase{ready: []*types.Issue{source, other}},
				deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
					source.ID: {externalDep(source.ID, "bb-1", depType)},
				}},
			}
			resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": foreignIssue("bb-1", types.StatusOpen)}}
			uw := crossRigProvider(t, inner, resolver)

			ready, err := uw.IssueUseCase().GetReadyWork(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if ids := issueIDs(ready.Items); !slices.Equal(ids, []string{"aa-2"}) {
				t.Fatalf("served ready IDs = %v, want [aa-2]", ids)
			}
			blocked, err := uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetBlockedIssues: %v", err)
			}
			if len(blocked) != 1 || blocked[0].ID != "aa-1" || !slices.Equal(blocked[0].BlockedBy, []string{"bb-1"}) {
				t.Fatalf("served blocked = %+v, want aa-1 blocked by [bb-1]", blocked)
			}
		})
	}
}

// GH#5784 AC-2: the policy keeps no answer between evaluations, so closing,
// pinning or reopening the foreign blocker changes the next query with no
// write or sync in the source workspace.
func TestCrossRigBlockerStatusIsReadAtEachQuery(t *testing.T) {
	source := issue("aa-1")
	raw := &fakeStore{
		ready: []*types.Issue{source},
		deps: map[string][]*types.Dependency{
			source.ID: {externalDep(source.ID, "bb-1", types.DepBlocks)},
		},
	}
	blocker := foreignIssue("bb-1", types.StatusOpen)
	resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": blocker}}
	store := crossRigStore(raw, resolver)

	for i, step := range []struct {
		status    types.Status
		wantReady bool
	}{
		{types.StatusOpen, false},
		{types.StatusClosed, true},
		{types.StatusOpen, false}, // reopened
		{types.StatusPinned, true},
		{types.StatusInProgress, false},
	} {
		blocker.Status = step.status
		ready, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
		if err != nil {
			t.Fatalf("step %d GetReadyWork: %v", i, err)
		}
		if got := slices.Contains(issueIDs(ready), "aa-1"); got != step.wantReady {
			t.Fatalf("step %d: bb-1 %s, aa-1 ready = %v, want %v", i, step.status, got, step.wantReady)
		}
		blocked, _, err := store.IsBlocked(t.Context(), "aa-1")
		if err != nil || blocked == step.wantReady {
			t.Fatalf("step %d: bb-1 %s, IsBlocked = %v %v, want %v", i, step.status, blocked, err, !step.wantReady)
		}
	}
	if len(resolver.calls) != 10 {
		t.Fatalf("resolver calls = %d, want one per evaluation (10)", len(resolver.calls))
	}
}

// GH#5784 AC-3: a cross-rig blocker the resolver cannot read fails closed
// without failing the query: its source stays out of ready work, and one
// warning (stderr, never the JSON stream) names the target and the reason.
func TestCrossRigUnreadableBlockerStaysBlockingWithOneWarning(t *testing.T) {
	source, other := issue("aa-1"), issue("aa-2")
	raw := &fakeStore{
		ready: []*types.Issue{source, other},
		deps: map[string][]*types.Dependency{
			source.ID: {externalDep(source.ID, "bb-1", types.DepBlocks)},
		},
	}
	resolver := &fakeIssueResolver{unavailable: map[string]string{"bb-1": `no route for prefix "bb"`}}
	store := crossRigStore(raw, resolver)
	var warned [][2]string
	store.warnings.issue = func(id, reason string) { warned = append(warned, [2]string{id, reason}) }

	for range 2 {
		ready, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if ids := issueIDs(ready); !slices.Equal(ids, []string{"aa-2"}) {
			t.Fatalf("ready IDs = %v, want [aa-2]", ids)
		}
	}
	if want := [][2]string{{"bb-1", `no route for prefix "bb"`}}; !slices.Equal(warned, want) {
		t.Fatalf("warnings = %v, want %v once", warned, want)
	}

	// Without any resolver (a policy built with no routes) the target is
	// still blocking, with its own reason.
	bare := New(raw, func(ProjectName) (string, bool) { return "", false }, nil)
	warned = nil
	bare.warnings.issue = func(id, reason string) { warned = append(warned, [2]string{id, reason}) }
	ready, err := bare.GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork without resolver: %v", err)
	}
	if ids := issueIDs(ready); !slices.Equal(ids, []string{"aa-2"}) {
		t.Fatalf("ready IDs without resolver = %v, want [aa-2]", ids)
	}
	if len(warned) != 1 || warned[0][0] != "bb-1" || warned[0][1] == "" {
		t.Fatalf("warnings without resolver = %v, want one naming bb-1 with a reason", warned)
	}
}

// GH#5784 AC-4: claiming or closing an issue an open cross-rig issue blocks
// is refused, naming the foreign id, on the direct store and on bd serve's
// unit-of-work arm; a forced close still goes through.
func TestCrossRigOpenBlockerRefusesClaimAndClose(t *testing.T) {
	newRaw := func() (*fakeStore, *fakeLifecycle) {
		lifecycle := &fakeLifecycle{}
		return &fakeStore{
			lifecycle: lifecycle,
			ready:     []*types.Issue{issue("aa-1")},
			deps: map[string][]*types.Dependency{
				"aa-1": {externalDep("aa-1", "bb-1", types.DepBlocks)},
			},
		}, lifecycle
	}
	resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": foreignIssue("bb-1", types.StatusOpen)}}
	const refusal = "cannot close blocked issue: aa-1 is blocked by [bb-1]"
	refused := func(t *testing.T, what string, err error) {
		t.Helper()
		if !errors.Is(err, storage.ErrCloseBlocked) || !strings.Contains(err.Error(), "bb-1") {
			t.Fatalf("%s: err = %v, want ErrCloseBlocked naming bb-1", what, err)
		}
	}

	raw, lifecycle := newRaw()
	store := crossRigStore(raw, resolver)
	lc, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	_, err = lc.Close(t.Context(), publicops.CloseRequest{IssueID: "aa-1"})
	refused(t, "lifecycle close", err)
	if err.Error() != refusal {
		t.Fatalf("lifecycle close refusal = %q, want %q", err.Error(), refusal)
	}
	_, err = lc.Update(t.Context(), publicops.UpdateRequest{IssueID: "aa-1", Claim: true})
	refused(t, "lifecycle claim", err)
	_, err = store.CloseIssueChecked(t.Context(), "aa-1", "tester", storage.CloseIssueOptions{})
	refused(t, "checked close", err)
	if claimed, err := store.ClaimReadyIssue(t.Context(), types.WorkFilter{}, "tester"); err != nil || claimed != nil {
		t.Fatalf("ClaimReadyIssue = %v %v, want nothing claimable", claimed, err)
	}
	if lifecycle.closed != 0 || lifecycle.updated != 0 || len(raw.closed) != 0 || len(raw.claimed) != 0 {
		t.Fatalf("a refused write reached the store: lifecycle closed=%d updated=%d, closed=%v claimed=%v",
			lifecycle.closed, lifecycle.updated, raw.closed, raw.claimed)
	}

	if _, err := lc.Close(t.Context(), publicops.CloseRequest{IssueID: "aa-1", Force: true}); err != nil || lifecycle.closed != 1 {
		t.Fatalf("forced lifecycle close = %v (closes %d), want it through", err, lifecycle.closed)
	}
	if _, err := store.CloseIssueChecked(t.Context(), "aa-1", "tester", storage.CloseIssueOptions{Force: true}); err != nil || !slices.Equal(raw.closed, []string{"aa-1"}) {
		t.Fatalf("forced checked close = %v (closed %v), want aa-1 closed", err, raw.closed)
	}

	issues := &fakeIssueUseCase{ready: []*types.Issue{issue("aa-1")}}
	uw := crossRigProvider(t, &fakeUOW{
		issues: issues,
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			"aa-1": {externalDep("aa-1", "bb-1", types.DepBlocks)},
		}},
	}, resolver)
	_, err = uw.IssueUseCase().CloseIssueChecked(t.Context(), "aa-1", domain.CloseIssueParams{}, "tester", false)
	refused(t, "served close", err)
	_, err = uw.IssueUseCase().ClaimIssue(t.Context(), "aa-1", "tester")
	refused(t, "served claim", err)
	if len(issues.closed) != 0 {
		t.Fatalf("served refused close reached the store: %v", issues.closed)
	}
}
