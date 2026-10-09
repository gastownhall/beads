package externaldeps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// remoteStore models httpclient.Store for remote_roles.go. Each role it hands
// out is served over the embedded fakeStore's rows, the way a server that
// knows nothing of the policy answers it, and each legacy method a base
// accessor builds on is refused, so a composition that reaches one fails the
// test instead of passing on a fake that answers it. Like httpclient.Store it
// cannot express ExcludeIDs and has no BatchCloserWithPolicy.
type remoteStore struct {
	*fakeStore
	// served logs each served role call, in order.
	served []string
	// closes logs the item ids of each served batch close.
	closes [][]string
	// edgeReads logs how many anchors each served edge read named.
	edgeReads []int
	// scans counts the whole-workspace dependency scans read through it.
	scans int
	// localBlockers is the served annotation's own BlockedBy, per id.
	localBlockers map[string][]string
	// claimErr, when set, answers every served Claim.
	claimErr error
}

var errLegacyRefused = errors.New("remoteStore: legacy method refused")

// wireEdgeAnchorCap is the most anchors the wire's listDependencies accepts.
const wireEdgeAnchorCap = 100

func newRemoteStore(ready ...*types.Issue) *remoteStore {
	return &remoteStore{fakeStore: &fakeStore{
		ready:                 ready,
		deps:                  make(map[string][]*types.Dependency),
		excludeIDsUnsupported: true,
	}}
}

func (r *remoteStore) IsRemoteBackendStore() bool { return true }

// httpclient.Store serves the vocabulary a listing loads before its search.
func (r *remoteStore) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}
func (r *remoteStore) GetCustomTypes(context.Context) ([]string, error) { return nil, nil }
func (r *remoteStore) GetInfraTypes(context.Context) map[string]bool    { return nil }

func (r *remoteStore) SearchIssuesWithCounts(context.Context, string, types.IssueFilter) ([]*types.IssueWithCounts, error) {
	return nil, errLegacyRefused
}

func (r *remoteStore) IsBlocked(context.Context, string) (bool, []string, error) {
	return false, nil, errLegacyRefused
}

func (r *remoteStore) GetDependencyRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, errLegacyRefused
}

func (r *remoteStore) GetDependencyTree(context.Context, string, int, bool, bool) ([]*types.TreeNode, error) {
	return nil, errLegacyRefused
}

func (r *remoteStore) ClaimReadyIssue(context.Context, types.WorkFilter, string) (*types.Issue, error) {
	return nil, errLegacyRefused
}

func (r *remoteStore) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	r.scans++
	return r.fakeStore.GetAllDependencyRecords(ctx)
}

func (r *remoteStore) GetExternalBlockingDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	r.scans++
	return r.fakeStore.GetExternalBlockingDependencyRecords(ctx)
}

func (r *remoteStore) IssueReader() (publicops.Reader, error)   { return servedReader{r}, nil }
func (r *remoteStore) IssueClaimer() (publicops.Claimer, error) { return servedClaimer{r}, nil }
func (r *remoteStore) ReadyClaimer() (publicops.ReadyClaimer, error) {
	return servedReadyClaimer{r}, nil
}
func (r *remoteStore) TreeWalker() (publicops.TreeWalker, error)   { return servedWalker{r}, nil }
func (r *remoteStore) EdgeReader() (publicops.EdgeReader, error)   { return servedEdgeReader{r}, nil }
func (r *remoteStore) BatchCloser() (publicops.BatchCloser, error) { return servedCloser{r}, nil }

func (r *remoteStore) BlockingAnnotator() (publicops.BlockingAnnotator, error) {
	return servedAnnotator{r}, nil
}

type servedReader struct{ s *remoteStore }

func (r servedReader) Ready(context.Context, publicops.ReadyRequest) (publicops.IssuePage, error) {
	return publicops.IssuePage{}, errors.New("served Ready: ready work is the policy reader's to answer")
}

func (r servedReader) List(context.Context, publicops.ListRequest) (publicops.IssuePage, error) {
	r.s.served = append(r.s.served, "List")
	items := make([]*types.IssueWithCounts, 0, len(r.s.ready))
	for _, issue := range r.s.ready {
		items = append(items, &types.IssueWithCounts{Issue: issue})
	}
	return publicops.IssuePage{Items: items}, nil
}

func (r servedReader) Get(ctx context.Context, req publicops.GetRequest) (*publicops.IssueDetails, error) {
	r.s.served = append(r.s.served, "Get "+req.ID)
	issue, err := r.s.GetIssue(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, publicops.ErrNotFound
	}
	return &publicops.IssueDetails{Issue: *issue}, nil
}

type servedClaimer struct{ s *remoteStore }

func (c servedClaimer) Claim(ctx context.Context, req publicops.ClaimRequest) (publicops.ClaimResult, error) {
	c.s.served = append(c.s.served, "Claim "+req.IssueID)
	if c.s.claimErr != nil {
		return publicops.ClaimResult{}, c.s.claimErr
	}
	if err := c.s.ClaimIssue(ctx, req.IssueID, req.Actor); err != nil {
		return publicops.ClaimResult{}, err
	}
	issue, err := c.s.GetIssue(ctx, req.IssueID)
	return publicops.ClaimResult{Issue: issue, Changed: true}, err
}

// servedReadyClaimer claims the first unassigned ready row, blind to the
// policy as a claim-next with no exclusion parameter is.
type servedReadyClaimer struct{ s *remoteStore }

func (c servedReadyClaimer) ClaimNext(ctx context.Context, req publicops.ClaimNextRequest) (publicops.ClaimNextResult, error) {
	c.s.served = append(c.s.served, "ClaimNext")
	for _, issue := range c.s.ready {
		if issue.Assignee != "" {
			continue
		}
		if err := c.s.ClaimIssue(ctx, issue.ID, req.Actor); err != nil {
			return publicops.ClaimNextResult{}, err
		}
		return publicops.ClaimNextResult{Claimed: &types.IssueWithCounts{Issue: issue}}, nil
	}
	return publicops.ClaimNextResult{}, nil
}

type servedAnnotator struct{ s *remoteStore }

func (a servedAnnotator) AnnotateBlocking(_ context.Context, req publicops.BlockingRequest) (publicops.BlockingResult, error) {
	a.s.served = append(a.s.served, "AnnotateBlocking")
	items := make([]publicops.IssueBlocking, 0, len(req.IDs))
	for _, id := range req.IDs {
		items = append(items, publicops.IssueBlocking{ID: id, BlockedBy: slices.Clone(a.s.localBlockers[id])})
	}
	return publicops.BlockingResult{Items: items}, nil
}

type servedWalker struct{ s *remoteStore }

func (w servedWalker) WalkTree(context.Context, publicops.WalkTreeRequest) (publicops.TreeResult, error) {
	w.s.served = append(w.s.served, "WalkTree")
	return publicops.TreeResult{Nodes: slices.Clone(w.s.tree)}, nil
}

type servedEdgeReader struct{ s *remoteStore }

func (e servedEdgeReader) ReadEdges(_ context.Context, req publicops.EdgeReadRequest) (publicops.EdgeReadResult, error) {
	if len(req.IDs) > wireEdgeAnchorCap {
		return publicops.EdgeReadResult{}, fmt.Errorf("served ReadEdges: %d anchors, past the wire's %d", len(req.IDs), wireEdgeAnchorCap)
	}
	e.s.edgeReads = append(e.s.edgeReads, len(req.IDs))
	anchors := make([]publicops.AnchorEdges, 0, len(req.IDs))
	for _, id := range req.IDs {
		anchors = append(anchors, publicops.AnchorEdges{ID: id, Edges: e.s.deps[id]})
	}
	return publicops.EdgeReadResult{Anchors: anchors}, nil
}

type servedCloser struct{ s *remoteStore }

// errServedClaimNextRefused is the served closer's answer to a next claim.
// httpclient refuses one on every server: the v0 wire's batch close has no
// member to carry it (ledger row W-CloseBatchRequest.ClaimNext).
var errServedClaimNextRefused = &storage.ErrUnsupported{Op: "BatchCloser.CloseBatch", Backend: "http"}

func (c servedCloser) CloseBatch(ctx context.Context, req publicops.CloseBatchRequest) (publicops.CloseBatchResult, error) {
	if req.ClaimNext != nil {
		c.s.served = append(c.s.served, "CloseBatch ClaimNext")
		return publicops.CloseBatchResult{}, errServedClaimNextRefused
	}
	ids := make([]string, 0, len(req.Items))
	outcomes := make([]publicops.CloseOutcome, 0, len(req.Items))
	for _, item := range req.Items {
		ids = append(ids, item.IssueID)
		result, err := c.s.CloseIssueChecked(ctx, item.IssueID, req.Actor, storage.CloseIssueOptions{})
		outcomes = append(outcomes, publicops.CloseOutcome{IssueID: item.IssueID, Changed: err == nil && !result.Unchanged, Err: err})
	}
	c.s.closes = append(c.s.closes, ids)
	return publicops.CloseBatchResult{Outcomes: outcomes}, nil
}

const paymentsRef = "external:remote:payments"

func blockOnPayments(raw *remoteStore, ids ...string) {
	for _, id := range ids {
		raw.deps[id] = []*types.Dependency{externalDep(id, paymentsRef, types.DepBlocks)}
	}
}

func closeItems(ids ...string) []publicops.BatchCloseItem {
	items := make([]publicops.BatchCloseItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, publicops.BatchCloseItem{IssueID: id})
	}
	return items
}

// TestRemoteRolesAnswerWithoutTheLegacyMethods pins the blocker the base
// accessors had over httpclient.Store: built over legacy methods a remote
// store refuses, they failed bd list, bd show, bd dep tree, bd ready --claim
// and bd close in a workspace where nothing had an external dependency.
func TestRemoteRolesAnswerWithoutTheLegacyMethods(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := newRemoteStore(a, b)
	raw.tree = []*types.TreeNode{{Issue: *a}}
	store := testStore(raw, &fakeStore{}, true)
	ctx := t.Context()

	reader, err := store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader: %v", err)
	}
	if page, err := reader.List(ctx, publicops.ListRequest{}); err != nil || len(page.Items) != 2 {
		t.Fatalf("List = %d items, %v; want 2", len(page.Items), err)
	}
	if details, err := reader.Get(ctx, publicops.GetRequest{ID: a.ID}); err != nil || details.ID != a.ID {
		t.Fatalf("Get = %v, %v; want %s", details, err, a.ID)
	}
	if page, err := reader.Ready(ctx, publicops.ReadyRequest{}); err != nil || len(page.Items) != 2 {
		t.Fatalf("Ready = %d items, %v; want 2", len(page.Items), err)
	}

	claimer, err := store.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer: %v", err)
	}
	if _, err := claimer.Claim(ctx, publicops.ClaimRequest{Actor: "worker", IssueID: a.ID}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	readyClaimer, err := store.ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer: %v", err)
	}
	if next, err := readyClaimer.ClaimNext(ctx, publicops.ClaimNextRequest{Actor: "worker"}); err != nil || next.Claimed == nil || next.Claimed.ID != b.ID {
		t.Fatalf("ClaimNext = %+v, %v; want %s", next.Claimed, err, b.ID)
	}

	annotator, err := store.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}
	if result, err := annotator.AnnotateBlocking(ctx, publicops.BlockingRequest{IDs: []string{a.ID, b.ID}}); err != nil || len(result.Items) != 2 {
		t.Fatalf("AnnotateBlocking = %+v, %v; want 2 items", result.Items, err)
	}
	walker, err := store.TreeWalker()
	if err != nil {
		t.Fatalf("TreeWalker: %v", err)
	}
	if tree, err := walker.WalkTree(ctx, publicops.WalkTreeRequest{RootID: a.ID, MaxDepth: 50}); err != nil || len(tree.Nodes) != 1 {
		t.Fatalf("WalkTree = %d nodes, %v; want the served root alone", len(tree.Nodes), err)
	}

	closer, err := store.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser: %v", err)
	}
	result, err := closer.CloseBatch(ctx, publicops.CloseBatchRequest{Actor: "worker", Items: closeItems(a.ID, b.ID)})
	if err != nil || len(result.Outcomes) != 2 {
		t.Fatalf("CloseBatch = %+v, %v; want 2 outcomes", result.Outcomes, err)
	}

	if want := []string{"List", "Get be-a", "Claim be-a", "ClaimNext", "AnnotateBlocking", "WalkTree"}; !slices.Equal(raw.served, want) {
		t.Fatalf("served calls = %v, want %v", raw.served, want)
	}
	if want := [][]string{{a.ID, b.ID}}; !slices.EqualFunc(raw.closes, want, slices.Equal[[]string]) {
		t.Fatalf("served closes = %v, want %v", raw.closes, want)
	}
}

// TestRemoteReaderPolicesReadyWorkOnly: an externally blocked issue leaves the
// ready listing and stays in a plain one, which claims nothing about
// readiness.
func TestRemoteReaderPolicesReadyWorkOnly(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := newRemoteStore(a, b)
	blockOnPayments(raw, a.ID)
	reader, err := testStore(raw, &fakeStore{}, true).IssueReader()
	if err != nil {
		t.Fatalf("IssueReader: %v", err)
	}

	ready, err := reader.Ready(t.Context(), publicops.ReadyRequest{})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(ready.Items) != 1 || ready.Items[0].ID != b.ID {
		t.Fatalf("Ready = %d items, want %s alone (%s is externally blocked)", len(ready.Items), b.ID, a.ID)
	}
	listed, err := reader.List(t.Context(), publicops.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("List = %d items, want both", len(listed.Items))
	}
}

func TestRemoteClaimerRefusesExternallyBlockedWork(t *testing.T) {
	for _, enforced := range []bool{false, true} {
		t.Run(fmt.Sprintf("server enforces %v", enforced), func(t *testing.T) {
			a := issue("be-a")
			raw := newRemoteStore(a)
			raw.enforced = enforced
			blockOnPayments(raw, a.ID)
			claimer, err := testStore(raw, &fakeStore{}, true).IssueClaimer()
			if err != nil {
				t.Fatalf("IssueClaimer: %v", err)
			}

			_, err = claimer.Claim(t.Context(), publicops.ClaimRequest{Actor: "worker", IssueID: a.ID})
			if enforced {
				// The server already applied the policy, so the claim is its
				// to answer.
				if err != nil || !slices.Equal(raw.served, []string{"Claim be-a"}) {
					t.Fatalf("Claim = %v, served %v; want the served claim", err, raw.served)
				}
				return
			}
			if !errors.Is(err, storage.ErrCloseBlocked) || !strings.Contains(err.Error(), paymentsRef) {
				t.Fatalf("Claim = %v, want ErrCloseBlocked naming %s", err, paymentsRef)
			}
			if len(raw.served) != 0 {
				t.Fatalf("served calls = %v, want none for a refused claim", raw.served)
			}
		})
	}
}

// TestRemoteReadyClaimerSkipsExternallyBlockedWork: the served claim-next
// cannot be told what to skip, so with anything externally blocked the claim
// walks the policy's ready page through the served Claimer instead. be-a sorts
// first, where the served claim-next would take it.
func TestRemoteReadyClaimerSkipsExternallyBlockedWork(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := newRemoteStore(a, b)
	blockOnPayments(raw, a.ID)
	claimer, err := testStore(raw, &fakeStore{}, true).ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer: %v", err)
	}

	next, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "worker"})
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if next.Claimed == nil || next.Claimed.ID != b.ID || b.Assignee != "worker" {
		t.Fatalf("ClaimNext claimed %+v, want %s for worker", next.Claimed, b.ID)
	}
	if want := []string{"Claim be-b"}; !slices.Equal(raw.served, want) {
		t.Fatalf("served calls = %v, want %v", raw.served, want)
	}
}

// TestRemoteReadyClaimerReportsLostRaces: a front that had work but lost every
// candidate is an error, not the empty answer that means there is no work.
func TestRemoteReadyClaimerReportsLostRaces(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := newRemoteStore(a, b)
	blockOnPayments(raw, a.ID)
	raw.claimErr = storage.ErrAlreadyClaimed
	claimer, err := testStore(raw, &fakeStore{}, true).ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer: %v", err)
	}

	next, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "worker"})
	if err == nil || !strings.Contains(err.Error(), "lost 1 races") || next.Claimed != nil {
		t.Fatalf("ClaimNext = %+v, %v; want a lost-races error", next.Claimed, err)
	}
}

// TestRemoteBlockingAnnotatorMergesExternalBlockers: the served annotation's
// own blockers stay, the external ones join them, and the edges behind them
// are read in chunks the wire accepts.
func TestRemoteBlockingAnnotatorMergesExternalBlockers(t *testing.T) {
	issues := make([]*types.Issue, 0, 150)
	ids := make([]string, 0, 150)
	for i := range 150 {
		issues = append(issues, issue(fmt.Sprintf("be-%03d", i)))
		ids = append(ids, issues[i].ID)
	}
	raw := newRemoteStore(issues...)
	blockOnPayments(raw, "be-000", "be-149")
	raw.localBlockers = map[string][]string{"be-000": {"be-local"}}
	annotator, err := testStore(raw, &fakeStore{}, true).BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}

	result, err := annotator.AnnotateBlocking(t.Context(), publicops.BlockingRequest{IDs: ids})
	if err != nil {
		t.Fatalf("AnnotateBlocking: %v", err)
	}
	blockedBy := make(map[string][]string, len(result.Items))
	for _, item := range result.Items {
		blockedBy[item.ID] = item.BlockedBy
	}
	if want := []string{"be-local", paymentsRef}; !slices.Equal(blockedBy["be-000"], want) {
		t.Errorf("be-000 blocked by %v, want %v", blockedBy["be-000"], want)
	}
	if want := []string{paymentsRef}; !slices.Equal(blockedBy["be-149"], want) {
		t.Errorf("be-149 blocked by %v, want %v", blockedBy["be-149"], want)
	}
	if len(blockedBy["be-001"]) != 0 {
		t.Errorf("be-001 blocked by %v, want nothing", blockedBy["be-001"])
	}
	if want := []int{100, 50}; !slices.Equal(raw.edgeReads, want) {
		t.Errorf("served edge reads named %v anchors, want %v", raw.edgeReads, want)
	}
}

// TestRemoteTreeWalkerAddsExternalLeaves: the served down-tree gains the leaf
// its stored edges name, a leaf the server rendered is not repeated, and any
// other walk is the served answer as it came.
func TestRemoteTreeWalkerAddsExternalLeaves(t *testing.T) {
	root := &types.TreeNode{Issue: *issue("be-a")}
	served := &types.TreeNode{Issue: types.Issue{ID: paymentsRef, Title: "served leaf"}, Depth: 1, ParentID: "be-a"}
	for _, tc := range []struct {
		name    string
		req     publicops.WalkTreeRequest
		tree    []*types.TreeNode
		wantIDs []string
	}{
		{name: "down walk", req: publicops.WalkTreeRequest{RootID: "be-a", MaxDepth: 50}, tree: []*types.TreeNode{root}, wantIDs: []string{"be-a", paymentsRef}},
		{name: "server rendered the leaf", req: publicops.WalkTreeRequest{RootID: "be-a", MaxDepth: 50}, tree: []*types.TreeNode{root, served}, wantIDs: []string{"be-a", paymentsRef}},
		{name: "up walk", req: publicops.WalkTreeRequest{RootID: "be-a", MaxDepth: 50, Direction: publicops.TreeUp}, tree: []*types.TreeNode{root}, wantIDs: []string{"be-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := newRemoteStore()
			raw.tree = tc.tree
			blockOnPayments(raw, "be-a")
			walker, err := testStore(raw, &fakeStore{}, true).TreeWalker()
			if err != nil {
				t.Fatalf("TreeWalker: %v", err)
			}

			result, err := walker.WalkTree(t.Context(), tc.req)
			if err != nil {
				t.Fatalf("WalkTree: %v", err)
			}
			ids := make([]string, 0, len(result.Nodes))
			for _, node := range result.Nodes {
				ids = append(ids, node.ID)
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Fatalf("tree = %v, want %v", ids, tc.wantIDs)
			}
			if leaf := result.Nodes[len(result.Nodes)-1]; tc.name == "down walk" {
				if leaf.Status != types.StatusOpen || leaf.ParentID != "be-a" || leaf.Depth != 1 || leaf.Title != "○ payments" {
					t.Fatalf("leaf = %+v, want the open payments leaf under be-a", leaf)
				}
			}
		})
	}
}

// TestRemoteBatchCloserRefusesExternallyBlockedItems: a still-open externally
// blocked item is refused the way the local policy close refuses it and the
// rest close in one served request. An item already closed keeps the served
// answer, Force waives the policy, and a server that enforces the policy gets
// the whole batch.
func TestRemoteBatchCloserRefusesExternallyBlockedItems(t *testing.T) {
	for _, tc := range []struct {
		name        string
		force       bool
		enforced    bool
		wantRefused bool
	}{
		{name: "policy", wantRefused: true},
		{name: "force", force: true},
		{name: "server enforces", enforced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocked, free, done := issue("be-blocked"), issue("be-free"), issue("be-done")
			done.Status = types.StatusClosed
			raw := newRemoteStore(blocked, free, done)
			raw.enforced = tc.enforced
			blockOnPayments(raw, blocked.ID, done.ID)
			closer, err := testStore(raw, &fakeStore{}, true).BatchCloser()
			if err != nil {
				t.Fatalf("BatchCloser: %v", err)
			}

			request := publicops.CloseBatchRequest{Actor: "worker", Force: tc.force, Items: closeItems(blocked.ID, free.ID, done.ID)}
			result, err := closer.CloseBatch(t.Context(), request)
			if err != nil {
				t.Fatalf("CloseBatch: %v", err)
			}
			if len(result.Outcomes) != len(request.Items) {
				t.Fatalf("CloseBatch answered %d outcomes for %d items", len(result.Outcomes), len(request.Items))
			}
			for i, outcome := range result.Outcomes {
				if outcome.IssueID != request.Items[i].IssueID {
					t.Fatalf("outcome %d is %s's, want %s's", i, outcome.IssueID, request.Items[i].IssueID)
				}
			}
			wantServed := []string{blocked.ID, free.ID, done.ID}
			if tc.wantRefused {
				refusal := result.Outcomes[0].Err
				want := "cannot close blocked issue: be-blocked is blocked by [" + paymentsRef + "]"
				if !errors.Is(refusal, storage.ErrCloseBlocked) || refusal.Error() != want {
					t.Fatalf("be-blocked outcome = %v, want %q", refusal, want)
				}
				wantServed = []string{free.ID, done.ID}
			} else if result.Outcomes[0].Err != nil {
				t.Fatalf("be-blocked outcome = %v, want it closed", result.Outcomes[0].Err)
			}
			if result.Outcomes[1].Err != nil || result.Outcomes[2].Err != nil {
				t.Fatalf("be-free, be-done outcomes = %v, %v; want the served answers", result.Outcomes[1].Err, result.Outcomes[2].Err)
			}
			if want := [][]string{wantServed}; !slices.EqualFunc(raw.closes, want, slices.Equal[[]string]) {
				t.Fatalf("served closes = %v, want %v", raw.closes, want)
			}
		})
	}
}

// TestRemoteBatchCloserRefusesANextClaim: a served close cannot keep
// externally blocked work out of its claim, so a batch with a next claim is
// refused whole, the same way whether the workspace holds an open holder of
// an external ref, only a closed one, or none, and with Force too. The
// refusal reads nothing, closes nothing and names the commands that do the
// same work. A server that enforces the policy itself gets the request as it
// came, and its closer answers.
func TestRemoteBatchCloserRefusesANextClaim(t *testing.T) {
	for _, tc := range []struct {
		name       string
		holder     types.Status
		force      bool
		enforced   bool
		wantServed []string
	}{
		{name: "open holder", holder: types.StatusOpen},
		{name: "closed holder", holder: types.StatusClosed},
		{name: "no holder"},
		{name: "force", holder: types.StatusOpen, force: true},
		{name: "server enforces", holder: types.StatusOpen, enforced: true, wantServed: []string{"CloseBatch ClaimNext"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			free, holder := issue("be-free"), issue("be-holder")
			raw := newRemoteStore(free, holder)
			raw.enforced = tc.enforced
			if tc.holder != "" {
				holder.Status = tc.holder
				blockOnPayments(raw, holder.ID)
			}
			closer, err := testStore(raw, &fakeStore{}, true).BatchCloser()
			if err != nil {
				t.Fatalf("BatchCloser: %v", err)
			}

			_, err = closer.CloseBatch(t.Context(), publicops.CloseBatchRequest{
				Actor: "worker", Force: tc.force, Items: closeItems(free.ID), ClaimNext: &publicops.ReadyRequest{},
			})
			if tc.enforced {
				if !errors.Is(err, errServedClaimNextRefused) {
					t.Fatalf("CloseBatch with ClaimNext = %v, want the served closer's answer", err)
				}
			} else {
				var unsupported *storage.ErrUnsupported
				if !errors.As(err, &unsupported) || unsupported.Op != "CloseBatchRequest.ClaimNext" {
					t.Fatalf("CloseBatch with ClaimNext = %v, want the CloseBatchRequest.ClaimNext refusal", err)
				}
				if msg := err.Error(); !strings.Contains(msg, "nothing was closed") || !strings.Contains(msg, "then run `bd ready --claim`") {
					t.Fatalf("refusal %q does not say that nothing closed and what to run instead", msg)
				}
			}
			if !slices.Equal(raw.served, tc.wantServed) {
				t.Fatalf("served calls = %v, want %v", raw.served, tc.wantServed)
			}
			if len(raw.closes) != 0 || len(raw.edgeReads) != 0 || raw.scans != 0 {
				t.Fatalf("closes %v, edge reads %v, scans %d; want none", raw.closes, raw.edgeReads, raw.scans)
			}
		})
	}
}

// TestRemoteBatchCloserValidatesANextClaimFirst: a next claim that is invalid
// on every backend is ErrValidation, not the refusal.
func TestRemoteBatchCloserValidatesANextClaimFirst(t *testing.T) {
	for name, claim := range map[string]*publicops.ReadyRequest{
		"offset":   {Offset: 1},
		"bad sort": {Sort: "bogus"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := newRemoteStore(issue("be-free"))
			closer, err := testStore(raw, &fakeStore{}, true).BatchCloser()
			if err != nil {
				t.Fatalf("BatchCloser: %v", err)
			}

			_, err = closer.CloseBatch(t.Context(), publicops.CloseBatchRequest{
				Actor: "worker", Items: closeItems("be-free"), ClaimNext: claim,
			})
			var unsupported *storage.ErrUnsupported
			if !errors.Is(err, storage.ErrValidation) || errors.As(err, &unsupported) {
				t.Fatalf("CloseBatch with ClaimNext %+v = %v, want ErrValidation", claim, err)
			}
		})
	}
}
