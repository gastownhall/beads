package externaldeps

import (
	"context"
	"errors"
	"maps"
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

// GH#5784: a closed source's cross-rig edge is no candidate, and a guard
// about one issue reads only that issue's own edges. aa-9 is closed and names
// zz-9; live aa-5 names zz-5; neither target can be read. A ready read
// resolves zz-5 alone (holding aa-5), and claiming or closing unrelated aa-2
// resolves nothing at all, so no foreign rig is opened and nothing warns
// "remains blocking" for an edge that cannot change the answer. On the direct
// store, bd serve's unit-of-work arm and the served store of an old-token
// server, whose blocked listing pages the live listing (a closed row the
// server returns anyway is dropped).
func TestCrossRigClosedSourceAndUnrelatedGuardsResolveNothing(t *testing.T) {
	closedEdge := map[string][]*types.Dependency{"aa-9": {externalDep("aa-9", "zz-9", types.DepBlocks)}}
	liveEdge := map[string][]*types.Dependency{"aa-5": {externalDep("aa-5", "zz-5", types.DepBlocks)}}
	allEdges := func() map[string][]*types.Dependency {
		edges := maps.Clone(closedEdge)
		maps.Copy(edges, liveEdge)
		return edges
	}
	closedSource := func() *types.Issue {
		closed := issue("aa-9")
		closed.Status = types.StatusClosed
		return closed
	}
	unreadable := func() *fakeIssueResolver {
		return &fakeIssueResolver{unavailable: map[string]string{"zz-5": "no route for prefix zz", "zz-9": "no route for prefix zz"}}
	}
	readZZ5Only := func(t *testing.T, what string, resolver *fakeIssueResolver) {
		t.Helper()
		for _, call := range resolver.calls {
			if !slices.Equal(call, []string{"zz-5"}) {
				t.Fatalf("%s: resolver calls %v, want zz-5 alone (aa-9 is closed)", what, resolver.calls)
			}
		}
		if len(resolver.calls) == 0 {
			t.Fatalf("%s: zz-5 was never resolved, want it to hold aa-5", what)
		}
		resolver.calls = nil
	}
	readNothing := func(t *testing.T, what string, resolver *fakeIssueResolver) {
		t.Helper()
		if len(resolver.calls) != 0 {
			t.Fatalf("%s: resolver calls %v, want none for unrelated aa-2", what, resolver.calls)
		}
	}

	t.Run("direct", func(t *testing.T) {
		resolver := unreadable()
		raw := &fakeStore{
			lifecycle: &fakeLifecycle{}, batch: &fakeBatchCloser{},
			ready: []*types.Issue{issue("aa-2"), issue("aa-5")}, others: []*types.Issue{closedSource()}, deps: allEdges(),
		}
		store := crossRigStore(raw, resolver)
		ready, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
		if err != nil || !slices.Equal(issueIDs(ready), []string{"aa-2"}) {
			t.Fatalf("GetReadyWork = %v, %v; want [aa-2]", issueIDs(ready), err)
		}
		readZZ5Only(t, "direct ready", resolver)
		lc, err := store.IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle: %v", err)
		}
		if _, err := lc.Update(t.Context(), publicops.UpdateRequest{IssueID: "aa-2", Claim: true}); err != nil {
			t.Fatalf("claim aa-2: %v", err)
		}
		if _, err := lc.Close(t.Context(), publicops.CloseRequest{IssueID: "aa-2"}); err != nil {
			t.Fatalf("close aa-2: %v", err)
		}
		closer, err := store.BatchCloser()
		if err != nil {
			t.Fatalf("BatchCloser: %v", err)
		}
		if _, err := closer.CloseBatch(t.Context(), publicops.CloseBatchRequest{Actor: "tester", Items: closeItems("aa-2")}); err != nil {
			t.Fatalf("batch close aa-2: %v", err)
		}
		readNothing(t, "direct guards", resolver)
	})

	t.Run("serve unit of work", func(t *testing.T) {
		resolver := unreadable()
		// The narrow read is the SQL one, which keeps live sources only, so
		// aa-9's edge is a stored record alone.
		uw := crossRigProvider(t, &fakeUOW{
			issues: &fakeIssueUseCase{ready: []*types.Issue{issue("aa-2"), issue("aa-5")}},
			deps:   &fakeDependencyUseCase{external: liveEdge, records: closedEdge},
		}, resolver)
		ready, err := uw.IssueUseCase().GetReadyWork(t.Context(), types.WorkFilter{})
		if err != nil || !slices.Equal(issueIDs(ready.Items), []string{"aa-2"}) {
			t.Fatalf("GetReadyWork = %v, %v; want [aa-2]", issueIDs(ready.Items), err)
		}
		readZZ5Only(t, "served ready", resolver)
		if _, err := uw.IssueUseCase().ApplyUpdate(t.Context(), "aa-2", domain.UpdateSpec{Claim: true}, "tester"); err != nil {
			t.Fatalf("served claim aa-2: %v", err)
		}
		if _, err := uw.IssueUseCase().CloseIssueChecked(t.Context(), "aa-2", domain.CloseIssueParams{}, "tester", false); err != nil {
			t.Fatalf("served close aa-2: %v", err)
		}
		readNothing(t, "served guards", resolver)
	})

	t.Run("old-token served store", func(t *testing.T) {
		resolver := unreadable()
		raw := newRemoteStore(issue("aa-2"), issue("aa-5"))
		raw.enforced, raw.capabilityOnly = true, true
		raw.others = []*types.Issue{closedSource()}
		raw.deps = allEdges()
		store := New(crossRigServedRemote{crossRigRemote{raw}}, func(ProjectName) (string, bool) { return "", false }, nil,
			WithIssueResolver(resolver.resolve))
		ctx := t.Context()
		reader, err := store.IssueReader()
		if err != nil {
			t.Fatalf("IssueReader: %v", err)
		}
		page, err := reader.Ready(ctx, publicops.ReadyRequest{})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != "aa-2" {
			t.Fatalf("Ready = %+v, %v; want aa-2 alone", page.Items, err)
		}
		readZZ5Only(t, "old-token ready", resolver)
		lister, err := store.BlockedLister()
		if err != nil {
			t.Fatalf("BlockedLister: %v", err)
		}
		listed, err := lister.ListBlocked(ctx, publicops.BlockedRequest{})
		if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != "aa-5" {
			t.Fatalf("ListBlocked = %+v, %v; want aa-5 alone", listed.Items, err)
		}
		readZZ5Only(t, "old-token blocked listing", resolver)
		claimer, err := store.IssueClaimer()
		if err != nil {
			t.Fatalf("IssueClaimer: %v", err)
		}
		if _, err := claimer.Claim(ctx, publicops.ClaimRequest{Actor: "worker", IssueID: "aa-2"}); err != nil {
			t.Fatalf("claim aa-2: %v", err)
		}
		closer, err := store.BatchCloser()
		if err != nil {
			t.Fatalf("BatchCloser: %v", err)
		}
		if _, err := closer.CloseBatch(ctx, publicops.CloseBatchRequest{Actor: "worker", Items: closeItems("aa-2")}); err != nil {
			t.Fatalf("batch close aa-2: %v", err)
		}
		readNothing(t, "old-token guards", resolver)
		if raw.scans != 0 {
			t.Fatalf("workspace-wide dependency walks = %d, want none", raw.scans)
		}
	})
}

// treeDependencyUseCase answers the walk with tree; the rest is the fake.
type treeDependencyUseCase struct {
	*fakeDependencyUseCase
	tree []*types.TreeNode
}

func (u *treeDependencyUseCase) GetDependencyTree(context.Context, string, domain.DepTreeOpts) ([]*types.TreeNode, error) {
	return slices.Clone(u.tree), nil
}

// crossRigRemote is the served store with a BatchGetter, which the policy
// reads a differently prefixed source row through first.
type crossRigRemote struct{ *remoteStore }

func (r crossRigRemote) BatchGetter() (publicops.BatchGetter, error) {
	return crossRigBatchGetter{r.remoteStore}, nil
}

type crossRigBatchGetter struct{ s *remoteStore }

func (g crossRigBatchGetter) GetMany(ctx context.Context, req publicops.GetManyRequest) (publicops.GetManyResult, error) {
	issues, err := g.s.GetIssuesByIDs(ctx, req.IDs)
	return publicops.GetManyResult{Issues: issues}, err
}

// GH#5784 AC-7: a foreign edge of every type hangs a marked leaf under its
// source in the dependency tree: the observed foreign row when the target
// reads, else an unresolved leaf naming the stored id and the reason with no
// status. Display reads warn nothing and leaves are never walked further, on
// the direct store, bd serve's unit-of-work arm and the served walker.
func TestCrossRigTreeShowsMarkedForeignLeavesOfEveryType(t *testing.T) {
	edges := []*types.Dependency{
		externalDep("aa-1", "bb-1", types.DepTracks),
		externalDep("aa-1", "bb-2", types.DepRelated),
		externalDep("aa-1", "bb-3", types.DepParentChild),
		externalDep("aa-1", "bb-4", types.DepWaitsFor),
		externalDep("aa-1", "bb-5", types.DepBlocks),
		externalDep("aa-1", "aa-2", types.DepBlocks),
	}
	foreignDeps := map[string][]*types.Dependency{
		"bb-1": {externalDep("bb-1", "bb-9", types.DepBlocks)},
	}
	newResolver := func() *fakeIssueResolver {
		// bb-1's row carries fields its leaf must not show (foreignLeafIssue):
		// a served tree answers every client the token admits.
		bb1 := foreignIssue("bb-1", types.StatusOpen)
		bb1.Description, bb1.Notes, bb1.Assignee = "foreign description", "foreign notes", "foreign-agent"
		bb1.Metadata = []byte(`{"secret":true}`)
		return &fakeIssueResolver{
			issues: map[string]*types.Issue{
				"bb-1": bb1,
				"bb-2": foreignIssue("bb-2", types.StatusClosed),
				"bb-3": foreignIssue("bb-3", types.StatusInProgress),
			},
			unavailable: map[string]string{"bb-4": `no route for prefix "bb"`, "bb-5": "database is locked"},
		}
	}
	tree := func() []*types.TreeNode {
		return []*types.TreeNode{
			{Issue: *issue("aa-1")},
			{Issue: *issue("aa-2"), Depth: 1, ParentID: "aa-1", EdgeFromParent: types.DepBlocks},
		}
	}
	check := func(t *testing.T, nodes []*types.TreeNode, resolver *fakeIssueResolver, warned []string) {
		t.Helper()
		var got []string
		for _, node := range nodes {
			got = append(got, node.ID+" "+string(node.EdgeFromParent)+" "+string(node.Status)+" "+node.Title+" "+node.Unresolved)
			if node.ID == "bb-9" {
				t.Fatalf("the walk descended into the foreign graph: %+v", node)
			}
			if strings.HasPrefix(node.ID, "bb-") && (!node.External || node.Depth != 1 || node.ParentID != "aa-1") {
				t.Errorf("foreign leaf %s = %+v, want an External leaf under aa-1", node.ID, node)
			}
			if strings.HasPrefix(node.ID, "aa-") && node.External {
				t.Errorf("local node %s marked External", node.ID)
			}
			if node.ID == "bb-1" && (node.Description != "" || node.Notes != "" || node.Assignee != "" || len(node.Metadata) != 0) {
				t.Errorf("foreign leaf bb-1 = %+v, want only the row's display fields", node.Issue)
			}
		}
		want := []string{
			"aa-1  open aa-1 ",
			"aa-2 blocks open aa-2 ",
			"bb-1 tracks open foreign bb-1 ",
			"bb-2 related closed foreign bb-2 ",
			"bb-3 parent-child in_progress foreign bb-3 ",
			`bb-4 waits-for  bb-4 no route for prefix "bb"`,
			"bb-5 blocks  bb-5 database is locked",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("tree =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if want := [][]string{{"bb-1", "bb-2", "bb-3", "bb-4", "bb-5"}}; !slices.EqualFunc(resolver.calls, want, slices.Equal[[]string]) {
			t.Fatalf("resolver calls = %v, want %v", resolver.calls, want)
		}
		if len(warned) != 0 {
			t.Fatalf("tree display warned %v, want nothing", warned)
		}
	}

	t.Run("direct", func(t *testing.T) {
		resolver := newResolver()
		raw := &fakeStore{tree: tree(), deps: map[string][]*types.Dependency{"aa-1": edges, "bb-1": foreignDeps["bb-1"]}}
		store := crossRigStore(raw, resolver)
		var warned []string
		store.warnings.issue = func(id, _ string) { warned = append(warned, id) }
		nodes, err := store.GetDependencyTree(t.Context(), "aa-1", 10, false, false)
		if err != nil {
			t.Fatalf("GetDependencyTree: %v", err)
		}
		check(t, nodes, resolver, warned)
		if reversed, err := store.GetDependencyTree(t.Context(), "aa-1", 10, false, true); err != nil || len(reversed) != 2 {
			t.Fatalf("reverse tree = %d nodes, %v; want the walk as it came", len(reversed), err)
		}
	})

	t.Run("serve unit of work", func(t *testing.T) {
		resolver := newResolver()
		uw := crossRigProvider(t, &fakeUOW{
			issues: &fakeIssueUseCase{},
			deps: &treeDependencyUseCase{
				fakeDependencyUseCase: &fakeDependencyUseCase{records: map[string][]*types.Dependency{"aa-1": edges}},
				tree:                  tree(),
			},
		}, resolver)
		var warned []string
		uw.policy.warnings.issue = func(id, _ string) { warned = append(warned, id) }
		nodes, err := uw.DependencyUseCase().GetDependencyTree(t.Context(), "aa-1", domain.DepTreeOpts{MaxDepth: 10})
		if err != nil {
			t.Fatalf("GetDependencyTree: %v", err)
		}
		check(t, nodes, resolver, warned)
	})

	t.Run("served walker", func(t *testing.T) {
		resolver := newResolver()
		raw := newRemoteStore()
		raw.tree = tree()
		raw.deps["aa-1"] = edges
		store := New(crossRigRemote{raw}, func(ProjectName) (string, bool) { return "", false }, nil, WithIssueResolver(resolver.resolve))
		var warned []string
		store.warnings.issue = func(id, _ string) { warned = append(warned, id) }
		walker, err := store.TreeWalker()
		if err != nil {
			t.Fatalf("TreeWalker: %v", err)
		}
		result, err := walker.WalkTree(t.Context(), publicops.WalkTreeRequest{RootID: "aa-1", MaxDepth: 50})
		if err != nil {
			t.Fatalf("WalkTree: %v", err)
		}
		check(t, result.Nodes, resolver, warned)
	})
}

// crossRigServedRemote adds the served blocked listing, which answers what a
// server that does not apply the cross-rig policy derives: nothing blocked.
type crossRigServedRemote struct{ crossRigRemote }

func (r crossRigServedRemote) BlockedLister() (publicops.BlockedLister, error) {
	return crossRigServedBlocked{r.remoteStore}, nil
}

type crossRigServedBlocked struct{ s *remoteStore }

func (b crossRigServedBlocked) ListBlocked(context.Context, publicops.BlockedRequest) (publicops.BlockedResult, error) {
	b.s.served = append(b.s.served, "ListBlocked")
	return publicops.BlockedResult{Items: []*publicops.BlockedIssue{}}, nil
}

// GH#5784 AC-8: a server that advertises capability-reference enforcement but
// not cross-rig enforcement leaves the bare cross-rig edge to the client.
// Ready, the blocked listing, is-blocked and the tree each succeed and report
// the source blocked by the open foreign target: the old token neither
// bypasses the client's enforcement nor turns those reads into refusals.
func TestCrossRigOldTokenServerGetsClientComposedReads(t *testing.T) {
	source, other := issue("aa-1"), issue("aa-2")
	raw := newRemoteStore(source, other)
	raw.enforced, raw.capabilityOnly = true, true
	raw.deps["aa-1"] = []*types.Dependency{externalDep("aa-1", "bb-1", types.DepBlocks)}
	raw.tree = []*types.TreeNode{{Issue: *source}}
	resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": foreignIssue("bb-1", types.StatusOpen)}}
	store := New(crossRigServedRemote{crossRigRemote{raw}}, func(ProjectName) (string, bool) { return "", false }, nil,
		WithIssueResolver(resolver.resolve))
	ctx := t.Context()

	reader, err := store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader: %v", err)
	}
	page, err := reader.Ready(ctx, publicops.ReadyRequest{})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	var ready []string
	for _, row := range page.Items {
		ready = append(ready, row.ID)
	}
	if !slices.Equal(ready, []string{"aa-2"}) {
		t.Fatalf("ready IDs = %v, want [aa-2]", ready)
	}

	lister, err := store.BlockedLister()
	if err != nil {
		t.Fatalf("BlockedLister: %v", err)
	}
	listed, err := lister.ListBlocked(ctx, publicops.BlockedRequest{})
	if err != nil {
		t.Fatalf("ListBlocked: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != "aa-1" || !slices.Equal(listed.Items[0].BlockedBy, []string{"bb-1"}) {
		t.Fatalf("blocked = %+v, want aa-1 blocked by [bb-1]", listed.Items)
	}

	annotator, err := store.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}
	annotated, err := annotator.AnnotateBlocking(ctx, publicops.BlockingRequest{IDs: []string{"aa-1", "aa-2"}, IncludeIsBlocked: true})
	if err != nil {
		t.Fatalf("AnnotateBlocking: %v", err)
	}
	if len(annotated.Items) != 2 || annotated.Items[0].IsBlocked == nil || !*annotated.Items[0].IsBlocked ||
		!slices.Equal(annotated.Items[0].BlockedBy, []string{"bb-1"}) || len(annotated.Items[1].BlockedBy) != 0 {
		t.Fatalf("AnnotateBlocking = %+v, want aa-1 alone blocked by [bb-1]", annotated.Items)
	}
	isBlocked, err := store.IsBlockedBatch(ctx, []string{"aa-1", "aa-2"})
	if err != nil || !isBlocked["aa-1"] || isBlocked["aa-2"] {
		t.Fatalf("IsBlockedBatch = %v, %v; want aa-1 alone blocked", isBlocked, err)
	}

	walker, err := store.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker: %v", err)
	}
	tree, err := walker.WalkTree(ctx, publicops.WalkTreeRequest{RootID: "aa-1", MaxDepth: 10})
	if err != nil {
		t.Fatalf("WalkTree: %v", err)
	}
	if len(tree.Nodes) != 2 || tree.Nodes[1].ID != "bb-1" || !tree.Nodes[1].External ||
		tree.Nodes[1].Status != types.StatusOpen || tree.Nodes[1].EdgeFromParent != types.DepBlocks {
		t.Fatalf("tree = %+v, want aa-1 with the open foreign blocker bb-1 as its leaf", tree.Nodes)
	}

	// The request count against an old-token server: each read is the
	// server's own request plus, for the blocked listing, one page of the
	// live listing (its rows carry their edges), and an edge read only for
	// the rows a per-issue question names. Nothing walks every issue and edge
	// in the workspace.
	if want := []string{"Ready", "ListBlocked", "List", "AnnotateBlocking", "WalkTree"}; !slices.Equal(raw.served, want) {
		t.Fatalf("served calls = %v, want %v", raw.served, want)
	}
	if want := []int{2, 2, 1}; !slices.Equal(raw.edgeReads, want) || raw.scans != 0 {
		t.Fatalf("edge reads = %v (anchors each), workspace walks = %d; want %v and none", raw.edgeReads, raw.scans, want)
	}
}

// TestCrossRigOldTokenTreeLeavesCapabilityLeavesToTheServer: a server that
// advertises policy.external_dependencies but not the cross-rig token hangs
// the external: leaves itself, so the client hangs only the bare cross-rig
// ones. It resolves no capability ref (its locator is never asked, so a client
// without that external_projects entry opens and warns nothing) and keeps the
// server's leaf as it came.
func TestCrossRigOldTokenTreeLeavesCapabilityLeavesToTheServer(t *testing.T) {
	raw := newRemoteStore()
	raw.enforced, raw.capabilityOnly = true, true
	raw.deps["aa-1"] = []*types.Dependency{
		externalDep("aa-1", paymentsRef, types.DepBlocks),
		externalDep("aa-1", "bb-1", types.DepBlocks),
	}
	raw.tree = []*types.TreeNode{
		{Issue: *issue("aa-1")},
		{
			Issue: types.Issue{ID: paymentsRef, Title: "✓ payments", Status: types.StatusClosed, IssueType: types.TypeTask},
			Depth: 1, ParentID: "aa-1", EdgeFromParent: types.DepBlocks,
		},
	}
	resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": foreignIssue("bb-1", types.StatusOpen)}}
	var located []ProjectName
	store := New(crossRigRemote{raw}, func(project ProjectName) (string, bool) {
		located = append(located, project)
		return "", false
	}, func(context.Context, string) (storage.DoltStorage, error) {
		t.Error("the tree opened an external project")
		return nil, errors.New("unexpected open")
	}, WithIssueResolver(resolver.resolve))
	walker, err := store.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker: %v", err)
	}

	result, err := walker.WalkTree(t.Context(), publicops.WalkTreeRequest{RootID: "aa-1", MaxDepth: 10})
	if err != nil {
		t.Fatalf("WalkTree: %v", err)
	}
	var got []string
	for _, node := range result.Nodes {
		got = append(got, node.ID+" "+node.Title)
	}
	if want := []string{"aa-1 aa-1", paymentsRef + " ✓ payments", "bb-1 foreign bb-1"}; !slices.Equal(got, want) {
		t.Fatalf("tree = %v, want the server's capability leaf as it came and the client's foreign leaf", got)
	}
	if len(located) != 0 {
		t.Fatalf("located external projects %v, want none: the server applies capability refs", located)
	}
	if want := [][]string{{"bb-1"}}; !slices.EqualFunc(resolver.calls, want, slices.Equal[[]string]) {
		t.Fatalf("resolver calls = %v, want %v", resolver.calls, want)
	}
}

// GH#5784 AC-9: tracks, related, parent-child and waits-for edges to a foreign
// issue never gate, so an otherwise-ready source stays ready and no foreign
// workspace is read for them: on the direct store, bd serve's unit-of-work arm
// and the served reader against a server that leaves the policy to the client.
func TestCrossRigNonBlockingTypesNeverReadTheForeignRig(t *testing.T) {
	nonBlocking := []types.DependencyType{types.DepTracks, types.DepRelated, types.DepParentChild, types.DepWaitsFor}
	edges := make([]*types.Dependency, 0, len(nonBlocking))
	for i, depType := range nonBlocking {
		edges = append(edges, externalDep("aa-1", "bb-"+string(rune('1'+i)), depType))
	}
	open := func() *fakeIssueResolver {
		issues := make(map[string]*types.Issue)
		for _, dep := range edges {
			issues[dep.DependsOnID] = foreignIssue(dep.DependsOnID, types.StatusOpen)
		}
		return &fakeIssueResolver{issues: issues}
	}
	check := func(t *testing.T, what string, ready []string, resolver *fakeIssueResolver) {
		t.Helper()
		if !slices.Equal(ready, []string{"aa-1"}) {
			t.Fatalf("%s ready IDs = %v, want [aa-1]", what, ready)
		}
		if len(resolver.calls) != 0 {
			t.Fatalf("%s read the foreign rig: resolver calls %v, want none", what, resolver.calls)
		}
	}

	t.Run("direct", func(t *testing.T) {
		resolver := open()
		store := crossRigStore(&fakeStore{ready: []*types.Issue{issue("aa-1")}, deps: map[string][]*types.Dependency{"aa-1": edges}}, resolver)
		ready, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		check(t, "direct", issueIDs(ready), resolver)
		if blocked, _, err := store.IsBlocked(t.Context(), "aa-1"); err != nil || blocked {
			t.Fatalf("IsBlocked = %v, %v; want unblocked", blocked, err)
		}
		check(t, "direct is-blocked", issueIDs(ready), resolver)
	})

	t.Run("serve unit of work", func(t *testing.T) {
		resolver := open()
		uw := crossRigProvider(t, &fakeUOW{
			issues: &fakeIssueUseCase{ready: []*types.Issue{issue("aa-1")}},
			deps:   &fakeDependencyUseCase{external: map[string][]*types.Dependency{"aa-1": edges}},
		}, resolver)
		ready, err := uw.IssueUseCase().GetReadyWork(t.Context(), types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		check(t, "served unit of work", issueIDs(ready.Items), resolver)
	})

	t.Run("served reader", func(t *testing.T) {
		resolver := open()
		raw := newRemoteStore(issue("aa-1"))
		raw.deps["aa-1"] = edges
		store := New(crossRigRemote{raw}, func(ProjectName) (string, bool) { return "", false }, nil, WithIssueResolver(resolver.resolve))
		reader, err := store.IssueReader()
		if err != nil {
			t.Fatalf("IssueReader: %v", err)
		}
		page, err := reader.Ready(t.Context(), publicops.ReadyRequest{})
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		var ready []string
		for _, row := range page.Items {
			ready = append(ready, row.ID)
		}
		check(t, "served reader", ready, resolver)
	})
}

// GH#5784 AC-10: against a server that enforces capability references but not
// cross-rig ids, a close with a next claim cannot keep the foreign-blocked
// source out of its claim atomically, so the client refuses it as unsupported
// before anything reaches the server: nothing is closed and nothing claimed.
// A server that enforces both gets the request as it came.
func TestCrossRigOldTokenServerRefusesCloseWithClaimNext(t *testing.T) {
	newRaw := func(capabilityOnly bool) *remoteStore {
		raw := newRemoteStore(issue("aa-1"), issue("aa-2"))
		raw.enforced, raw.capabilityOnly = true, capabilityOnly
		raw.deps["aa-1"] = []*types.Dependency{externalDep("aa-1", "bb-1", types.DepBlocks)}
		return raw
	}
	closeAndClaim := func(t *testing.T, raw *remoteStore, resolver *fakeIssueResolver) error {
		t.Helper()
		store := New(crossRigRemote{raw}, func(ProjectName) (string, bool) { return "", false }, nil, WithIssueResolver(resolver.resolve))
		closer, err := store.BatchCloser()
		if err != nil {
			t.Fatalf("BatchCloser: %v", err)
		}
		_, err = closer.CloseBatch(t.Context(), publicops.CloseBatchRequest{
			Actor: "worker", Items: closeItems("aa-2"), ClaimNext: &publicops.ReadyRequest{},
		})
		return err
	}

	raw := newRaw(true)
	resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": foreignIssue("bb-1", types.StatusOpen)}}
	err := closeAndClaim(t, raw, resolver)
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) || unsupported.Op != "CloseBatchRequest.ClaimNext" {
		t.Fatalf("CloseBatch with ClaimNext = %v, want the client's ErrUnsupported for CloseBatchRequest.ClaimNext", err)
	}
	if len(raw.served) != 0 || len(raw.closes) != 0 || len(raw.closed) != 0 || len(raw.claimed) != 0 {
		t.Fatalf("a refused close reached the server: served=%v closes=%v closed=%v claimed=%v",
			raw.served, raw.closes, raw.closed, raw.claimed)
	}
	for _, row := range raw.ready {
		if row.Status != types.StatusOpen || row.Assignee != "" {
			t.Fatalf("%s = %s assigned %q after a refused close, want open and unassigned", row.ID, row.Status, row.Assignee)
		}
	}

	both := newRaw(false)
	if err := closeAndClaim(t, both, resolver); !errors.Is(err, errServedClaimNextRefused) || !slices.Equal(both.served, []string{"CloseBatch ClaimNext"}) {
		t.Fatalf("enforcing server: CloseBatch = %v, served %v; want the request handed to the server", err, both.served)
	}
}
