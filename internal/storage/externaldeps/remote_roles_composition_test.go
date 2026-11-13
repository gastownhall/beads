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
// cannot express ExcludeIDs, has no BatchCloserWithPolicy and no narrow
// external-edge query, and its ready and list rows carry their own edges.
type remoteStore struct {
	*fakeStore
	// GetExternalBlockingDependencyRecords hides the embedded fakeStore's
	// narrow query: httpclient.Store has none (storage.ExternalDependencyQueryStore),
	// and a field at this depth keeps the promoted method out of the method set.
	GetExternalBlockingDependencyRecords struct{}
	// omitRowEdges models a server whose ready and list rows come back with
	// their blocks count but without the edges themselves.
	omitRowEdges bool
	// served logs each served role call, in order.
	served []string
	// closes logs the item ids of each served batch close.
	closes [][]string
	// edgeReads logs how many anchors each served edge read named.
	edgeReads []int
	// scans counts the whole-workspace dependency walks asked of it, each
	// refused (GetAllDependencyRecords).
	scans int
	// localBlockers is the served annotation's own BlockedBy, per id.
	localBlockers map[string][]string
	// claimErr, when set, answers every served Claim.
	claimErr error
	// takenFirst names the rows a rival actor claims just before a served
	// Claim of them lands: that claim is a lost race, and the row leaves the
	// unassigned set.
	takenFirst map[string]bool
	// getErrs, per id, fails the GetIssue read of that id.
	getErrs map[string]error
	// arriving are rows that become ready after the client's read and sort
	// ahead of the rows it read: the served claim-next, which re-reads the
	// ready set when it claims, sees them first.
	arriving []*types.Issue
	// releaseErr, when set, answers every served Release.
	releaseErr error
}

var (
	errLegacyRefused = errors.New("remoteStore: legacy method refused")
	errWorkspaceWalk = errors.New("remoteStore: the workspace-wide dependency walk is refused")
)

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

func (r *remoteStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	if err := r.getErrs[id]; err != nil {
		return nil, err
	}
	return r.fakeStore.GetIssue(ctx, id)
}

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

// GetAllDependencyRecords is httpclient.Store's walk of every issue in the
// workspace, closed ones included, and then of every edge: about 150 requests
// at 10k issues. No policy read may cost that, so the walk is counted and
// refused.
func (r *remoteStore) GetAllDependencyRecords(context.Context) (map[string][]*types.Dependency, error) {
	r.scans++
	return nil, errWorkspaceWalk
}

// GetReadyWorkWithCounts is httpclient's legacy ready listing, whose rows
// come off the same wire as the served Ready's and carry their edges too.
func (r *remoteStore) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	rows, err := r.fakeStore.GetReadyWorkWithCounts(ctx, filter)
	return r.servedRows(rows), err
}

// GetReadyWorkWithCountsAndTotal is GetReadyWorkWithCounts with the total.
func (r *remoteStore) GetReadyWorkWithCountsAndTotal(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, int, error) {
	rows, total, err := r.fakeStore.GetReadyWorkWithCountsAndTotal(ctx, filter)
	return r.servedRows(rows), total, err
}

func (r *remoteStore) servedRows(rows []*types.IssueWithCounts) []*types.IssueWithCounts {
	for i, row := range rows {
		if row != nil && row.Issue != nil {
			rows[i] = r.servedRow(row.Issue)
		}
	}
	return rows
}

// servedRow is issue as a served ready or list row: like the counts query's
// deps_json it carries every edge its source stores, with its blocks count
// (dependency_count counts type blocks only), unless omitRowEdges leaves the
// edges out.
func (r *remoteStore) servedRow(issue *types.Issue) *types.IssueWithCounts {
	row := &types.IssueWithCounts{Issue: issue}
	edges := r.deps[issue.ID]
	for _, dep := range edges {
		if dep.Type == types.DepBlocks {
			row.DependencyCount++
		}
	}
	if len(edges) > 0 && !r.omitRowEdges {
		served := *issue
		served.Dependencies = slices.Clone(edges)
		row.Issue = &served
	}
	return row
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

// Ready serves the ready rows the way a server that knows nothing of the
// policy does: in order, narrowed by the request's assignee rule (Unassigned
// wins over Assignee, as in workapi.BuildReadyFilter), up to its limit, with
// has_more past it, each row carrying its edges (servedRow).
func (r servedReader) Ready(_ context.Context, req publicops.ReadyRequest) (publicops.IssuePage, error) {
	r.s.served = append(r.s.served, "Ready")
	items := make([]*types.IssueWithCounts, 0, len(r.s.ready))
	for _, issue := range r.s.ready {
		if req.Unassigned {
			if issue.Assignee != "" {
				continue
			}
		} else if req.Assignee != "" && issue.Assignee != req.Assignee {
			continue
		}
		items = append(items, r.s.servedRow(issue))
	}
	if req.Limit != nil && *req.Limit >= 0 && *req.Limit < len(items) {
		return publicops.IssuePage{Items: items[:*req.Limit], HasMore: true}, nil
	}
	return publicops.IssuePage{Items: items}, nil
}

func (r servedReader) List(_ context.Context, req publicops.ListRequest) (publicops.IssuePage, error) {
	if req.ReadyFlag {
		// Like httpclient: listIssues cannot carry ReadyFlag (ledger row
		// E-ListRequest.ReadyFlag); a ready listing is listReadyWork's question.
		r.s.served = append(r.s.served, "List ready")
		return publicops.IssuePage{}, &storage.ErrUnsupported{Op: "ListRequest.ReadyFlag", Backend: "http"}
	}
	r.s.served = append(r.s.served, "List")
	items := make([]*types.IssueWithCounts, 0, len(r.s.ready)+len(r.s.others))
	for _, issue := range slices.Concat(r.s.ready, r.s.others) {
		items = append(items, r.s.servedRow(issue))
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
	if c.s.takenFirst[req.IssueID] {
		if err := c.s.ClaimIssue(ctx, req.IssueID, "rival"); err != nil {
			return publicops.ClaimResult{}, err
		}
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
	if len(c.s.arriving) > 0 {
		c.s.ready = slices.Concat(c.s.arriving, c.s.ready)
		c.s.arriving = nil
	}
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

func (r *remoteStore) Releaser() (publicops.Releaser, error) { return servedReleaser{r}, nil }

// servedReleaser gives a claim back as the served release does: only while
// the holder the request names (ExpectedAssignee, else the actor) holds it.
type servedReleaser struct{ s *remoteStore }

func (rl servedReleaser) Release(_ context.Context, req publicops.ReleaseRequest) (publicops.ReleaseResult, error) {
	rl.s.served = append(rl.s.served, "Release "+req.IssueID)
	if rl.s.releaseErr != nil {
		return publicops.ReleaseResult{}, rl.s.releaseErr
	}
	holder := req.Actor
	if req.ExpectedAssignee != nil {
		holder = *req.ExpectedAssignee
	}
	for _, issue := range rl.s.ready {
		if issue.ID != req.IssueID {
			continue
		}
		if issue.Assignee == "" || issue.Assignee != holder {
			return publicops.ReleaseResult{}, publicops.ErrAssigneeMismatch
		}
		issue.Assignee, issue.Status = "", types.StatusOpen
		released := *issue
		return publicops.ReleaseResult{Issue: &released, Changed: true}, nil
	}
	return publicops.ReleaseResult{}, publicops.ErrNotFound
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
	if slices.Contains(req.IDs, "") {
		// As httpclient's validateAnchors refuses one.
		return publicops.EdgeReadResult{}, fmt.Errorf("%w: an anchor id must not be empty", publicops.ErrValidation)
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

	// The claim-next reads the front of the queue first ("Ready"): this
	// server does not apply the policy, so the rows it would claim from are
	// judged by their own edges before its atomic claim-next takes one.
	if want := []string{"List", "Get be-a", "Ready", "Claim be-a", "Ready", "ClaimNext", "AnnotateBlocking", "WalkTree"}; !slices.Equal(raw.served, want) {
		t.Fatalf("served calls = %v, want %v", raw.served, want)
	}
	if want := [][]string{{a.ID, b.ID}}; !slices.EqualFunc(raw.closes, want, slices.Equal[[]string]) {
		t.Fatalf("served closes = %v, want %v", raw.closes, want)
	}
	if raw.scans != 0 {
		t.Fatalf("workspace-wide dependency walks = %d, want none", raw.scans)
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

// TestRemoteReaderAnswersAReadyListingWithoutTheWireFlag: `bd list --ready`
// is a List with ReadyFlag, which the served listing refuses. Over a remote
// store it is the ready question, answered by the policy store's own reader
// with externally blocked work left out, and never sent to the served List as
// a ready listing. How the blocker is found depends on what the server leaves
// to the client (judgesReadyRows). A server that applies none of the policy
// leaves the capability refs too, so the blocker set comes from one plain
// listing of the live issues, whose rows carry their edges; that keeps the
// ready totals and counts exact. A server that applies the capability refs
// and leaves only the bare cross-rig ids gets a ready read judged by its own
// rows' edges, which lists nothing. Neither walks every issue and edge in the
// workspace.
func TestRemoteReaderAnswersAReadyListingWithoutTheWireFlag(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	for _, server := range []struct {
		name string
		// oldToken models policy.external_dependencies without
		// policy.cross_rig_dependencies.
		oldToken bool
		// blocker is a reference kind this server leaves to the client.
		blocker string
		served  []string
	}{
		{name: "a server that applies none of the policy", blocker: paymentsRef, served: []string{"List"}},
		{name: "an old-token server", oldToken: true, blocker: "bb-1"},
	} {
		for _, tc := range []struct {
			name    string
			blocked bool
			want    []string
		}{
			{name: "no external refs", want: []string{a.ID, b.ID}},
			{name: "an externally blocked issue", blocked: true, want: []string{b.ID}},
		} {
			t.Run(server.name+"/"+tc.name, func(t *testing.T) {
				raw := newRemoteStore(a, b)
				raw.enforced, raw.capabilityOnly = server.oldToken, server.oldToken
				if tc.blocked {
					raw.deps[a.ID] = []*types.Dependency{externalDep(a.ID, server.blocker, types.DepBlocks)}
				}
				// crossRigRemote serves the BatchGetter a cross-rig check
				// reads the source row through.
				store := testStore(crossRigRemote{raw}, &fakeStore{}, true)
				resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": foreignIssue("bb-1", types.StatusOpen)}}
				WithIssueResolver(resolver.resolve)(store)
				reader, err := store.IssueReader()
				if err != nil {
					t.Fatalf("IssueReader: %v", err)
				}
				listed, err := reader.List(t.Context(), publicops.ListRequest{ReadyFlag: true})
				if err != nil {
					t.Fatalf("List(ReadyFlag): %v", err)
				}
				got := make([]string, 0, len(listed.Items))
				for _, row := range listed.Items {
					got = append(got, row.ID)
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("List(ReadyFlag) = %v, want %v", got, tc.want)
				}
				if !slices.Equal(raw.served, server.served) {
					t.Fatalf("served calls = %v, want %v: the ready listing never reaches the served List, and only a server that leaves the capability refs to the client gets one plain listing", raw.served, server.served)
				}
				if raw.scans != 0 {
					t.Fatalf("workspace-wide dependency walks = %d, want none", raw.scans)
				}
			})
		}
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
	if want := []string{"Ready", "Claim be-b"}; !slices.Equal(raw.served, want) {
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

// TestRemoteReadyClaimerTakesOnlyUnassignedWork: the composed claim selects
// what a local claim-next selects (ClaimReadyIssueInTx), open and unassigned
// whatever assignee the request names. A row another actor holds, sorted
// first, is not a race to lose, and a request naming the caller does not try
// to re-take the caller's own row.
func TestRemoteReadyClaimerTakesOnlyUnassignedWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter publicops.ReadyRequest
	}{
		{name: "any assignee"},
		{name: "the caller as assignee", filter: publicops.ReadyRequest{Assignee: "worker"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, blocked, mine, free := issue("be-held"), issue("be-blocked"), issue("be-mine"), issue("be-free")
			held.Assignee, mine.Assignee = "alice", "worker"
			raw := newRemoteStore(held, blocked, mine, free)
			blockOnPayments(raw, blocked.ID)
			claimer, err := testStore(raw, &fakeStore{}, true).ReadyClaimer()
			if err != nil {
				t.Fatalf("ReadyClaimer: %v", err)
			}

			next, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "worker", Filter: tc.filter})
			if err != nil {
				t.Fatalf("ClaimNext: %v", err)
			}
			if next.Claimed == nil || next.Claimed.ID != free.ID || free.Assignee != "worker" {
				t.Fatalf("ClaimNext claimed %+v, want %s for worker", next.Claimed, free.ID)
			}
			if want := []string{"Ready", "Claim be-free"}; !slices.Equal(raw.served, want) {
				t.Fatalf("served calls = %v, want %v", raw.served, want)
			}
			if held.Assignee != "alice" {
				t.Fatalf("be-held assignee = %q, want alice's claim untouched", held.Assignee)
			}
		})
	}
}

// TestRemoteReadyClaimerWalksAContendedFront: candidates a rival takes between
// the read and the claim are passed over, however many lead the front. A full
// window lost end to end is read once more, and the rows the rivals took have
// left it; only when that pass is lost too is it an error, and a second pass
// with nothing left to take is no work.
func TestRemoteReadyClaimerWalksAContendedFront(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rivals    int  // leading candidates a rival takes first
		last      bool // be-last follows them
		lastTaken bool // and a rival takes it first too
		wantLast  bool
		wantErr   string
		wantReads int
	}{
		{name: "three lost ahead", rivals: 3, last: true, wantLast: true, wantReads: 1},
		{name: "a full window lost", rivals: remoteClaimPage, last: true, wantLast: true, wantReads: 2},
		{name: "both passes lost", rivals: remoteClaimPage, last: true, lastTaken: true, wantErr: "lost 26 races", wantReads: 2},
		// be-blocked takes the first window's last slot, so its other rows
		// are every candidate there is.
		{name: "nothing behind a lost window", rivals: remoteClaimPage - 1, wantReads: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocked, last := issue("be-blocked"), issue("be-last")
			raw := newRemoteStore(blocked)
			blockOnPayments(raw, blocked.ID)
			raw.takenFirst = make(map[string]bool)
			for i := range tc.rivals {
				taken := issue(fmt.Sprintf("be-taken-%02d", i))
				raw.ready = append(raw.ready, taken)
				raw.takenFirst[taken.ID] = true
			}
			if tc.last {
				raw.ready = append(raw.ready, last)
				raw.takenFirst[last.ID] = tc.lastTaken
			}
			claimer, err := testStore(raw, &fakeStore{}, true).ReadyClaimer()
			if err != nil {
				t.Fatalf("ReadyClaimer: %v", err)
			}

			next, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "worker"})
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || next.Claimed != nil {
					t.Fatalf("ClaimNext = %+v, %v; want a %q error", next.Claimed, err, tc.wantErr)
				}
			case err != nil:
				t.Fatalf("ClaimNext: %v", err)
			case tc.wantLast:
				if next.Claimed == nil || next.Claimed.ID != last.ID || last.Assignee != "worker" {
					t.Fatalf("ClaimNext claimed %+v, want %s for worker", next.Claimed, last.ID)
				}
			case next.Claimed != nil:
				t.Fatalf("ClaimNext claimed %+v, want no work: rivals took every candidate", next.Claimed)
			}
			reads := 0
			for _, call := range raw.served {
				if call == "Ready" {
					reads++
				}
			}
			if reads != tc.wantReads {
				t.Fatalf("served ready reads = %d, want %d", reads, tc.wantReads)
			}
		})
	}
}

// TestRemoteReadyClaimerChecksTheServersPick: with nothing judged blocked the
// claim is still the server's one atomic claim-next, but the server re-reads
// the ready set when it claims and applies no cross-rig policy, so it can take
// a row the client never judged (rivals took the judged rows, or a row became
// ready ahead of them). Against an old-token server that leaves bare cross-rig
// ids to the client (GH#5784 AC-8), such a pick is judged by an edge read of it
// alone: kept when nothing blocks it, else released and replaced by a judged
// candidate claimed by id. A pick that can be neither kept nor released is an
// error naming its blocker. A judged pick costs no read.
func TestRemoteReadyClaimerChecksTheServersPick(t *testing.T) {
	for _, tc := range []struct {
		name        string
		arriving    string // the row the served claim-next sees first, unread by the client
		blocked     bool   // an open cross-rig blocker holds it
		releaseErr  error
		wantClaimed string
		wantServed  []string
		wantReads   []int
		wantErr     string
	}{
		{name: "a judged pick", wantClaimed: "aa-1", wantServed: []string{"Ready", "ClaimNext"}},
		{
			name: "an unjudged pick nothing blocks", arriving: "aa-new", wantClaimed: "aa-new",
			wantServed: []string{"Ready", "ClaimNext"}, wantReads: []int{1},
		},
		{
			name: "an unjudged pick a cross-rig blocker holds", arriving: "aa-late", blocked: true, wantClaimed: "aa-1",
			wantServed: []string{"Ready", "ClaimNext", "Release aa-late", "Claim aa-1"}, wantReads: []int{1},
		},
		{
			name: "a blocked pick that cannot be released", arriving: "aa-late", blocked: true,
			releaseErr: errors.New("release refused"), wantErr: "releasing it failed",
			wantServed: []string{"Ready", "ClaimNext", "Release aa-late"}, wantReads: []int{1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := newRemoteStore(issue("aa-1"), issue("aa-2"))
			raw.enforced, raw.capabilityOnly = true, true
			raw.releaseErr = tc.releaseErr
			var late *types.Issue
			if tc.arriving != "" {
				late = issue(tc.arriving)
				raw.arriving = []*types.Issue{late}
				if tc.blocked {
					raw.deps[late.ID] = []*types.Dependency{externalDep(late.ID, "bb-1", types.DepBlocks)}
				}
			}
			resolver := &fakeIssueResolver{issues: map[string]*types.Issue{"bb-1": foreignIssue("bb-1", types.StatusOpen)}}
			store := New(crossRigRemote{raw}, func(ProjectName) (string, bool) { return "", false }, nil,
				WithIssueResolver(resolver.resolve))
			claimer, err := store.ReadyClaimer()
			if err != nil {
				t.Fatalf("ReadyClaimer: %v", err)
			}

			next, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "worker"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "bb-1") || next.Claimed != nil {
					t.Fatalf("ClaimNext = %+v, %v; want a %q error naming the blocker bb-1", next.Claimed, err, tc.wantErr)
				}
			} else if err != nil || next.Claimed == nil || next.Claimed.ID != tc.wantClaimed || next.Claimed.Assignee != "worker" {
				t.Fatalf("ClaimNext = %+v, %v; want %s claimed for worker", next.Claimed, err, tc.wantClaimed)
			}
			if !slices.Equal(raw.served, tc.wantServed) {
				t.Fatalf("served calls = %v, want %v", raw.served, tc.wantServed)
			}
			if !slices.Equal(raw.edgeReads, tc.wantReads) || raw.scans != 0 {
				t.Fatalf("edge reads = %v (anchors each), workspace walks = %d; want %v and none", raw.edgeReads, raw.scans, tc.wantReads)
			}
			if tc.blocked && tc.releaseErr == nil && (late.Assignee != "" || late.Status != types.StatusOpen) {
				t.Fatalf("%s = %q/%s, want its claim released: an open cross-rig blocker holds it", late.ID, late.Assignee, late.Status)
			}
		})
	}
}

// TestRemoteBlockingAnnotatorMergesExternalBlockers: the served annotation's
// own blockers stay, the external ones join them without an is_blocked flag
// the request did not ask for, and the edges behind them are read in chunks
// the wire accepts.
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
		if item.IsBlocked != nil {
			t.Errorf("%s is_blocked = %v unasked, want no flag", item.ID, *item.IsBlocked)
		}
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

// TestRemoteEdgesNamesEachAnchorOnce: the edge read behind every per-issue
// policy question names each id once and never an empty one, which the http
// EdgeReader refuses; nothing to name reads nothing.
func TestRemoteEdgesNamesEachAnchorOnce(t *testing.T) {
	raw := newRemoteStore(issue("be-a"))
	blockOnPayments(raw, "be-a")
	store := testStore(raw, &fakeStore{}, true)

	deps, err := store.remoteEdges(t.Context(), []string{"be-a", "", "be-a"})
	if err != nil {
		t.Fatalf("remoteEdges: %v", err)
	}
	if len(deps["be-a"]) != 1 {
		t.Fatalf("be-a edges = %v, want its one edge", deps["be-a"])
	}
	if _, err := store.remoteEdges(t.Context(), []string{""}); err != nil {
		t.Fatalf("remoteEdges of an empty id: %v", err)
	}
	if want := []int{1}; !slices.Equal(raw.edgeReads, want) {
		t.Fatalf("served edge reads named %v anchors, want %v", raw.edgeReads, want)
	}
}

// TestRemoteTreeWalkerAddsExternalLeaves: the served down-tree gains the leaf
// its stored edges name, a leaf the server rendered is not repeated, and any
// other walk is the served answer as it came. A policy probe that fails does
// not fail the walk the server answered: its leaves are rendered here.
func TestRemoteTreeWalkerAddsExternalLeaves(t *testing.T) {
	root := &types.TreeNode{Issue: *issue("be-a")}
	served := &types.TreeNode{Issue: types.Issue{ID: paymentsRef, Title: "served leaf"}, Depth: 1, ParentID: "be-a"}
	for _, tc := range []struct {
		name     string
		req      publicops.WalkTreeRequest
		tree     []*types.TreeNode
		probeErr error
		wantIDs  []string
	}{
		{name: "down walk", req: publicops.WalkTreeRequest{RootID: "be-a", MaxDepth: 50}, tree: []*types.TreeNode{root}, wantIDs: []string{"be-a", paymentsRef}},
		{name: "server rendered the leaf", req: publicops.WalkTreeRequest{RootID: "be-a", MaxDepth: 50}, tree: []*types.TreeNode{root, served}, wantIDs: []string{"be-a", paymentsRef}},
		{name: "up walk", req: publicops.WalkTreeRequest{RootID: "be-a", MaxDepth: 50, Direction: publicops.TreeUp}, tree: []*types.TreeNode{root}, wantIDs: []string{"be-a"}},
		{name: "probe fails", req: publicops.WalkTreeRequest{RootID: "be-a", MaxDepth: 50}, tree: []*types.TreeNode{root}, probeErr: errors.New("probe failed"), wantIDs: []string{"be-a", paymentsRef}},
		{name: "probe fails, server rendered the leaf", req: publicops.WalkTreeRequest{RootID: "be-a", MaxDepth: 50}, tree: []*types.TreeNode{root, served}, probeErr: errors.New("probe failed"), wantIDs: []string{"be-a", paymentsRef}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := newRemoteStore()
			raw.tree = tc.tree
			raw.enforcedErr = tc.probeErr
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
			if leaf := result.Nodes[len(result.Nodes)-1]; tc.name == "down walk" || tc.name == "probe fails" {
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

// TestRemoteBatchCloserAnswersAFailedReadWithIt: whether an externally blocked
// item is live is a read, and a read that fails is that item's outcome. Passing
// the item to the server's close instead would close blocked work on a guess.
// A missing item still travels, for the server's own not-found.
func TestRemoteBatchCloserAnswersAFailedReadWithIt(t *testing.T) {
	errRead := errors.New("served read failed")
	for _, tc := range []struct {
		name       string
		present    bool
		readErr    error
		wantErr    error
		wantServed []string
	}{
		{name: "failed read", present: true, readErr: errRead, wantErr: errRead, wantServed: []string{"be-free"}},
		{name: "missing", readErr: fmt.Errorf("%w: issue be-blocked", storage.ErrNotFound), wantErr: storage.ErrNotFound, wantServed: []string{"be-blocked", "be-free"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocked, free := issue("be-blocked"), issue("be-free")
			raw := newRemoteStore(free)
			if tc.present {
				raw.ready = append(raw.ready, blocked)
			}
			blockOnPayments(raw, blocked.ID)
			raw.getErrs = map[string]error{blocked.ID: tc.readErr}
			closer, err := testStore(raw, &fakeStore{}, true).BatchCloser()
			if err != nil {
				t.Fatalf("BatchCloser: %v", err)
			}

			result, err := closer.CloseBatch(t.Context(), publicops.CloseBatchRequest{Actor: "worker", Items: closeItems(blocked.ID, free.ID)})
			if err != nil {
				t.Fatalf("CloseBatch: %v", err)
			}
			if len(result.Outcomes) != 2 || result.Outcomes[0].IssueID != blocked.ID || !errors.Is(result.Outcomes[0].Err, tc.wantErr) || result.Outcomes[1].Err != nil {
				t.Fatalf("outcomes = %+v; want be-blocked answered %v and be-free closed", result.Outcomes, tc.wantErr)
			}
			if blocked.Status != types.StatusOpen {
				t.Fatalf("be-blocked is %s, want it still open", blocked.Status)
			}
			if want := [][]string{tc.wantServed}; !slices.EqualFunc(raw.closes, want, slices.Equal[[]string]) {
				t.Fatalf("served closes = %v, want %v", raw.closes, want)
			}
		})
	}
}

// TestRemoteBatchCloserRefusesANextClaim: the served batch close cannot carry
// a next claim, so a batch with one is refused whole, the same way whether the
// workspace holds an open holder of an external ref, only a closed one, or
// none, and with Force too. The refusal leads with that wire fact, reads
// nothing, closes nothing and names the commands that do the same work. A
// server that enforces the policy itself gets the request as it came, and its
// closer answers.
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
				if msg := err.Error(); !strings.Contains(msg, "cannot carry a next claim") || !strings.Contains(msg, "nothing was closed") || !strings.Contains(msg, "then run `bd ready --claim`") {
					t.Fatalf("refusal %q does not say why, that nothing closed, and what to run instead", msg)
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
