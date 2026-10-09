package externaldeps

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// Remote role composition.
//
// The base accessors (role_accessors.go, batch_closer.go) rebuild each role
// over this store's legacy methods, which a SQL store implements in full. A
// remote store does not: httpclient.Store serves its roles over the wire and
// refuses most of the legacy methods those builds call (SearchIssuesWithCounts,
// IsBlocked, GetDependencyRecordsForIssues, GetDependencyTree,
// ClaimReadyIssue, BatchCloserWithPolicy), so the base accessors would fail
// `bd list`, `bd dep tree`, `bd ready --claim` and a policed close even in a
// workspace with no external dependency at all. Over such a store each
// accessor starts from the inner store's own role instead and layers the
// policy around it, before or after the served call.
//
// storage.RemoteBackendStore chooses that composition and nothing else.
// Whether the policy runs is still serverEnforcesPolicy's question (design
// 3.6), asked on every path that loads blocking state.

// servesRoles reports whether the inner store serves its roles rather than
// building them over its legacy methods.
func (s *Store) servesRoles() bool {
	remote, ok := storage.UnwrapStore(s.inner).(storage.RemoteBackendStore)
	return ok && remote.IsRemoteBackendStore()
}

// servedEdgeAnchors is the most anchors one served edge read names: the wire
// caps listDependencies' issue_id at 100 items, and the client passes a
// request through without chunking it.
const servedEdgeAnchors = 100

// servedEdges reads the stored outgoing edges of ids through the inner store's
// EdgeReader, the served stand-in for GetDependencyRecordsForIssues. A missing
// anchor contributes no edges.
func (s *Store) servedEdges(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	anchors := make([]string, 0, len(ids))
	named := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" && !named[id] {
			named[id] = true
			anchors = append(anchors, id)
		}
	}
	deps := make(map[string][]*types.Dependency, len(anchors))
	if len(anchors) == 0 {
		return deps, nil
	}
	reader, err := s.inner.EdgeReader()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(anchors); start += servedEdgeAnchors {
		end := min(start+servedEdgeAnchors, len(anchors))
		result, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: anchors[start:end]})
		if err != nil {
			return nil, err
		}
		for _, anchor := range result.Anchors {
			deps[anchor.ID] = append(deps[anchor.ID], anchor.Edges...)
		}
	}
	return deps, nil
}

// blockingStateFor is loadBlockingState narrowed to ids, for a role that names
// its issues and so needs only their edges.
func (s *Store) blockingStateFor(ctx context.Context, ids []string) (blockingState, error) {
	if enforced, err := s.serverEnforcesPolicy(ctx); err != nil || enforced {
		return blockingState{}, err
	}
	deps, err := s.servedEdges(ctx, ids)
	if err != nil {
		return blockingState{}, fmt.Errorf("external dependencies: list blocking records: %w", err)
	}
	return s.blockingStateFromRecords(ctx, deps)
}

// plainDownTree reports whether req is the walk the policy decorates. Reverse
// walks do not follow a source's dependencies, and the status- or row-bounded
// variants keep the backend's own traversal semantics.
func plainDownTree(req issueops.WalkTreeRequest) bool {
	return (req.Direction == "" || req.Direction == issueops.TreeDown) && req.Status == "" && req.MaxRows == 0
}

// isLostClaimRace classifies the refusals that mean someone else took a
// candidate first. ErrNotFound joins them, as in httpclient's own composed
// claim: a ready row deleted before it was claimed is the same situation for
// a caller that wants the next piece of work.
func isLostClaimRace(err error) bool {
	return errors.Is(err, issueops.ErrAlreadyClaimed) ||
		errors.Is(err, issueops.ErrNotClaimable) ||
		errors.Is(err, issueops.ErrNotFound)
}

// remoteReader serves List and Get from the inner store's reader: this store
// overrides no search or detail method, so neither loses any policy. Ready and
// a ready-flagged List go through the policy reader, whose ready listing drops
// externally blocked work.
type remoteReader struct {
	served issueops.Reader
	policy issueops.Reader
}

func (r *remoteReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	return r.policy.Ready(ctx, req)
}

func (r *remoteReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	if req.ReadyFlag {
		return r.policy.List(ctx, req)
	}
	return r.served.List(ctx, req)
}

func (r *remoteReader) Get(ctx context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	return r.served.Get(ctx, req)
}

// remoteClaimer refuses an externally blocked claim before the served one.
// Local blockers are the served claim's own answer: IsBlocked is a refused
// legacy method on a remote store, and the server checks its own graph.
type remoteClaimer struct {
	served issueops.Claimer
	policy *Store
}

func (c *remoteClaimer) Claim(ctx context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	state, err := c.policy.blockingStateFor(ctx, []string{req.IssueID})
	if err != nil {
		return issueops.ClaimResult{}, err
	}
	if refs := state.refsByIssue[req.IssueID]; len(refs) > 0 {
		return issueops.ClaimResult{}, fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, req.IssueID, refs)
	}
	return c.served.Claim(ctx, req)
}

// remoteClaimPage is how many externally unblocked candidates one pass of
// remoteReadyClaimer reads, matching httpclient's composed ready claim.
const remoteClaimPage = 25

// remoteReadyClaimer hands the claim to the served ReadyClaimer when no issue
// in the workspace holds an unsatisfied external ref. Otherwise the served
// claim cannot be told what to skip — the wire's claim-next has no exclusion
// parameter — so it reads the policy's filtered ready page and claims down it
// through the served Claimer, as httpclient's own composition does for an
// older server. The same window applies: a row that gains a blocker between
// the listing and its claim can still be claimed.
//
// A closed holder counts too, as it does in every backend's blocking state.
// It can never be a candidate, so the composed claim takes the same issue the
// served one would. Leaving it out would cost a status read per holder, since
// the wire has no batch form of one.
type remoteReadyClaimer struct{ policy *Store }

func (c *remoteReadyClaimer) ClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	if err := storageissueops.ValidateClaimNextRequest(req); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	filter, err := workapi.BuildReadyFilter(req.Filter)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	state, err := c.policy.loadBlockingState(ctx)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	if len(state.refsByIssue) == 0 {
		served, err := c.policy.inner.ReadyClaimer()
		if err != nil {
			return issueops.ClaimNextResult{}, err
		}
		return served.ClaimNext(ctx, req)
	}
	claimer, err := c.policy.inner.IssueClaimer()
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	// The candidates a local claim-next selects (ClaimReadyIssueInTx): open,
	// unassigned, with no row cap on a scan that delivers one row.
	filter.Status = types.StatusOpen
	filter.Unassigned = true
	filter.Assignee = nil
	filter.MaxRows = 0
	filter.MaxRowsSource = ""
	filter.Limit = remoteClaimPage
	// A second pass only after a full page was lost: claimed rows leave the
	// unassigned set, so it reads the candidates behind them.
	var lost int
	for range 2 {
		rows, err := c.policy.readyWorkWithCounts(ctx, filter, state)
		if err != nil {
			return issueops.ClaimNextResult{}, err
		}
		if len(rows) == 0 {
			return issueops.ClaimNextResult{}, nil
		}
		for _, row := range rows {
			if row == nil || row.Issue == nil {
				continue
			}
			res, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: req.Actor, IssueID: row.ID})
			switch {
			case err == nil:
				return issueops.ClaimNextResult{Claimed: &types.IssueWithCounts{
					Issue:           res.Issue,
					DependencyCount: row.DependencyCount,
					DependentCount:  row.DependentCount,
					CommentCount:    row.CommentCount,
					Parent:          row.Parent,
				}}, nil
			case isLostClaimRace(err):
				lost++
			default:
				return issueops.ClaimNextResult{}, err
			}
		}
		if len(rows) < remoteClaimPage {
			break
		}
	}
	return issueops.ClaimNextResult{}, fmt.Errorf("claim ready: lost %d races for externally unblocked ready work; re-run to take the next one", lost)
}

// remoteBlockingAnnotator merges the external blockers the policy resolves
// into the served annotation's BlockedBy.
type remoteBlockingAnnotator struct {
	served issueops.BlockingAnnotator
	policy *Store
}

func (a *remoteBlockingAnnotator) AnnotateBlocking(ctx context.Context, req issueops.BlockingRequest) (issueops.BlockingResult, error) {
	result, err := a.served.AnnotateBlocking(ctx, req)
	if err != nil || len(result.Items) == 0 {
		return result, err
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	state, err := a.policy.blockingStateFor(ctx, ids)
	if err != nil {
		return issueops.BlockingResult{}, err
	}
	for i := range result.Items {
		for _, ref := range state.refsByIssue[result.Items[i].ID] {
			result.Items[i].BlockedBy = appendUnique(result.Items[i].BlockedBy, ref)
		}
	}
	return result, nil
}

// remoteTreeWalker adds external leaves to the served down-tree. A leaf the
// server already rendered is kept as it came (appendTreeExternalReferences
// skips an id the tree holds), and like GetDependencyTree the leaves are
// rendered whether or not the server enforces the policy: a leaf is display,
// not enforcement.
type remoteTreeWalker struct {
	served issueops.TreeWalker
	policy *Store
}

func (t *remoteTreeWalker) WalkTree(ctx context.Context, req issueops.WalkTreeRequest) (issueops.TreeResult, error) {
	result, err := t.served.WalkTree(ctx, req)
	if err != nil || !plainDownTree(req) || len(result.Nodes) == 0 {
		return result, err
	}
	ids := make([]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		if node != nil && !isExternalReference(node.ID) {
			ids = append(ids, node.ID)
		}
	}
	deps, err := t.policy.servedEdges(ctx, ids)
	if err != nil {
		return issueops.TreeResult{}, fmt.Errorf("external dependencies: load tree edges: %w", err)
	}
	nodes, err := t.policy.appendTreeExternalReferences(ctx, result.Nodes, deps, req.MaxDepth, false)
	if err != nil {
		return issueops.TreeResult{}, err
	}
	result.Nodes = nodes
	return result, nil
}

// remoteBatchCloser refuses each externally blocked, still-open item the way
// the local policy close does, then sends the rest to the served closer as one
// request, so they still close in one server transaction. The external answer
// is read before that request rather than inside it, the same window
// remoteReadyClaimer has.
//
// A batch that also claims the next issue is refused whole, before anything
// closes. The local closer claims inside the closing transaction with the
// policy's exclusions in its filter. A served close has no parameter to carry
// them, and claiming after the close commits would split the role's one
// transaction in two. The refusal does not read the workspace, so the command
// fails the same way whether or not anything in it is externally blocked.
type remoteBatchCloser struct{ policy *Store }

func (c *remoteBatchCloser) CloseBatch(ctx context.Context, request issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	if request.ClaimNext != nil {
		return c.closeAndClaimNext(ctx, request)
	}
	if request.Force {
		// Force waives the policy, so the base closer reaches the served one
		// with an empty policy.
		return (&batchCloser{policy: c.policy}).CloseBatch(ctx, request)
	}
	if err := storageissueops.ValidateCloseBatchRequest(request); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	ids := make([]string, 0, len(request.Items))
	for _, item := range request.Items {
		ids = append(ids, item.IssueID)
	}
	state, err := c.policy.blockingStateFor(ctx, ids)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	served, err := c.policy.inner.BatchCloser()
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if len(state.refsByIssue) == 0 {
		return served.CloseBatch(ctx, request)
	}

	policy := storage.NewBatchClosePolicy(state.refsByIssue)
	outcomes := make([]issueops.CloseOutcome, len(request.Items))
	forward := request
	forward.Items = make([]issueops.BatchCloseItem, 0, len(request.Items))
	forwarded := make([]int, 0, len(request.Items))
	for i, item := range request.Items {
		if refusal := policy.CheckClose(item.IssueID, false); refusal != nil {
			// As in the local close: only a live target is refused, so an
			// idempotent re-close and a not-found keep their own answers.
			issue, err := c.policy.inner.GetIssue(ctx, item.IssueID)
			if err != nil {
				outcomes[i] = issueops.CloseOutcome{IssueID: item.IssueID, Err: err}
				continue
			}
			if issue != nil && issue.Status != types.StatusClosed {
				outcomes[i] = issueops.CloseOutcome{IssueID: item.IssueID, Err: refusal}
				continue
			}
		}
		forward.Items = append(forward.Items, item)
		forwarded = append(forwarded, i)
	}
	if len(forward.Items) == 0 {
		return issueops.CloseBatchResult{Outcomes: outcomes}, nil
	}
	result, err := served.CloseBatch(ctx, forward)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if len(result.Outcomes) != len(forward.Items) {
		return issueops.CloseBatchResult{}, fmt.Errorf("close batch: served closer answered %d outcomes for %d items", len(result.Outcomes), len(forward.Items))
	}
	for j, outcome := range result.Outcomes {
		outcomes[forwarded[j]] = outcome
	}
	return issueops.CloseBatchResult{Outcomes: outcomes}, nil
}

// closeAndClaimNext answers a batch with a next claim. A request that is
// invalid on every backend is still ErrValidation. A server that enforces the
// policy itself gets the request as it came; over the v0 wire its closer
// refuses a next claim too (ledger row W-CloseBatchRequest.ClaimNext).
func (c *remoteBatchCloser) closeAndClaimNext(ctx context.Context, request issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	if err := storageissueops.ValidateCloseBatchRequest(request); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if _, err := workapi.BuildReadyFilter(*request.ClaimNext); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	enforced, err := c.policy.serverEnforcesPolicy(ctx)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if !enforced {
		return issueops.CloseBatchResult{}, fmt.Errorf(
			"close with a next claim: a served close cannot keep externally blocked work out of its claim, so nothing was closed; close without --claim-next, then run `bd ready --claim` (%w)",
			&storage.ErrUnsupported{Op: "CloseBatchRequest.ClaimNext", Backend: fmt.Sprintf("%T", storage.UnwrapStore(c.policy.inner))})
	}
	served, err := c.policy.inner.BatchCloser()
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	return served.CloseBatch(ctx, request)
}

var (
	_ issueops.Reader            = (*remoteReader)(nil)
	_ issueops.Claimer           = (*remoteClaimer)(nil)
	_ issueops.ReadyClaimer      = (*remoteReadyClaimer)(nil)
	_ issueops.BlockingAnnotator = (*remoteBlockingAnnotator)(nil)
	_ issueops.TreeWalker        = (*remoteTreeWalker)(nil)
	_ issueops.BatchCloser       = (*remoteBatchCloser)(nil)
)
