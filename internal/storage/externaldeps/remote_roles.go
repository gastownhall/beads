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

// THE REMOTE COMPOSITION.
//
// A remote backend (storage.RemoteBackendStore: today the HTTP client of a
// `bd serve`) serves its ROLES natively and only a slice of the legacy
// storage.DoltStorage method seam. The local role views in role_accessors.go
// are built from that seam — storereader over this decorator, IsBlocked,
// GetDependencyTree, ClaimReadyIssue — so over a remote store they reached the
// generated ErrUnsupported stubs: `bd list` and `bd children` refused
// (SearchIssuesWithCounts), `bd show --json` hydrated its counts from stubbed
// reads and printed zeros, and the default `bd dep tree` refused
// (GetDependencyTree).
//
// So for a remote store each role view is the INNER store's own role, passed
// through, with this decorator's external-dependency policy laid over it in
// role terms. The policy itself is unchanged and is never skipped merely
// because the store is remote (design 3.6):
//
//   - a server that advertises wire.CapExternalDependencies (every bd serve
//     that composes the policy, which is every bd serve) has applied it
//     already, so every view below first asks serverEnforcesPolicy and then
//     hands the request to the inner role UNTOUCHED: no client validation, no
//     edge reads, and claim-next stays the server's atomic operation;
//   - only against a server that does not advertise it (an older bd serve, or
//     another implementation) does the policy run here, client-side. Ready reads and the
//     ready claim exclude externally blocked sources, which needs the
//     workspace-wide blocker set (loadBlockingState). Every per-issue question
//     — may this issue be claimed or closed, what blocks these listed rows,
//     which external leaves hang off this tree — reads only the edges of the
//     issues it names, through the inner EdgeReader, instead of walking every
//     edge in the workspace.

// maxRemoteEdgeAnchors is how many anchors one EdgeReader read names. It is
// the server's own per-request anchor bound (internal/httpapi's
// maxDependencyAnchors); the http client refuses past it rather than chunking.
const maxRemoteEdgeAnchors = 100

// rolesAreRemote reports whether the inner store is a remote backend whose
// roles, not its legacy method seam, are the surface to compose over.
func (s *Store) rolesAreRemote() bool {
	remote, ok := storage.UnwrapStore(s.inner).(storage.RemoteBackendStore)
	return ok && remote.IsRemoteBackendStore()
}

// externalBlockersFor answers, for exactly the named issues, which unsatisfied
// external references block each of them. An issue with none is absent from
// the map.
//
// A remote store reads only those issues' edges (one EdgeReader request per
// maxRemoteEdgeAnchors ids); a local store keeps the indexed workspace-wide
// read loadBlockingState already makes, which is cheaper there than a
// per-issue one.
func (s *Store) externalBlockersFor(ctx context.Context, ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if !s.rolesAreRemote() {
		state, err := s.loadBlockingState(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[string][]string, len(ids))
		for _, id := range ids {
			if refs := state.refsByIssue[id]; len(refs) > 0 {
				out[id] = refs
			}
		}
		return out, nil
	}
	enforced, err := s.serverEnforcesPolicy(ctx)
	if err != nil || enforced {
		return nil, err
	}
	deps, err := s.remoteEdges(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: read blocking edges: %w", err)
	}
	state, err := s.blockingStateFromRecords(ctx, deps)
	if err != nil {
		return nil, err
	}
	return state.refsByIssue, nil
}

// remoteEdges reads the stored outgoing edges of ids through the inner
// EdgeReader, keyed by source id.
func (s *Store) remoteEdges(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	reader, err := s.inner.EdgeReader()
	if err != nil {
		return nil, err
	}
	deps := make(map[string][]*types.Dependency, len(ids))
	for start := 0; start < len(ids); start += maxRemoteEdgeAnchors {
		end := min(start+maxRemoteEdgeAnchors, len(ids))
		res, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: ids[start:end]})
		if err != nil {
			return nil, err
		}
		for _, anchor := range res.Anchors {
			if len(anchor.Edges) > 0 {
				deps[anchor.ID] = append(deps[anchor.ID], anchor.Edges...)
			}
		}
	}
	return deps, nil
}

// externallyBlockedIDs is the workspace-wide set of issues an unsatisfied
// external reference blocks — what a ready read has to exclude. Empty when the
// server already enforces the policy (loadBlockingState's own probe).
func (s *Store) externallyBlockedIDs(ctx context.Context) (map[string]bool, error) {
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return nil, err
	}
	if len(state.refsByIssue) == 0 {
		return nil, nil
	}
	blocked := make(map[string]bool, len(state.refsByIssue))
	for id := range state.refsByIssue {
		blocked[id] = true
	}
	return blocked, nil
}

// ── Reader ──────────────────────────────────────────────────────────

type remoteReader struct {
	inner  issueops.Reader
	policy *Store
}

// Get is the inner detail read: the policy decides no detail field, and the
// server's view carries the dependency and comment counts the legacy seam
// could not hydrate.
func (r *remoteReader) Get(ctx context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	return r.inner.Get(ctx, req)
}

// List passes an ordinary listing straight through. A ready listing
// (ReadyFlag) is a ready read and gets the same exclusion as Ready.
func (r *remoteReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	if !req.ReadyFlag {
		return r.inner.List(ctx, req)
	}
	blocked, err := r.policy.externallyBlockedIDs(ctx)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	if len(blocked) == 0 {
		return r.inner.List(ctx, req)
	}
	limit := workapi.PageLimit(req)
	widened := req
	widened.Offset = 0
	widened.Limit = widenedLimit(req.Offset, limit, len(blocked))
	page, err := r.inner.List(ctx, widened)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	return excludeAndPage(page, blocked, req.Offset, limit), nil
}

// Ready excludes externally blocked sources.
//
// No remote ready read can carry an id exclusion (L12), so the exclusion is
// applied to a widened window instead: from offset 0, Offset+Limit plus one
// row per blocked id. At most that many rows of the window can be dropped,
// so the caller's page is exact whenever enough unblocked rows exist.
func (r *remoteReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	blocked, err := r.policy.externallyBlockedIDs(ctx)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	if len(blocked) == 0 {
		return r.inner.Ready(ctx, req)
	}
	limit := workapi.LimitOr(req.Limit, workapi.DefaultReadyLimit)
	widened := req
	widened.Offset = 0
	widened.Limit = widenedLimit(req.Offset, limit, len(blocked))
	page, err := r.inner.Ready(ctx, widened)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	return excludeAndPage(page, blocked, req.Offset, limit), nil
}

// widenedLimit is the window a client-side exclusion reads: 0 (unlimited)
// stays unlimited.
func widenedLimit(offset, limit, blocked int) *int {
	n := 0
	if limit > 0 {
		n = offset + limit + blocked
	}
	return &n
}

// excludeAndPage drops the blocked rows from a widened window, then cuts the
// caller's page out of what remains. HasMore is true when unblocked rows are
// left past the page, or when the window itself was cut short by the server —
// beyond it the rows are unread, so "no more" cannot be claimed.
func excludeAndPage(page issueops.IssuePage, blocked map[string]bool, offset, limit int) issueops.IssuePage {
	kept := make([]*issueops.IssueWithCounts, 0, len(page.Items))
	for _, row := range page.Items {
		if row != nil && row.Issue != nil && blocked[row.ID] {
			continue
		}
		kept = append(kept, row)
	}
	if offset >= len(kept) {
		return issueops.IssuePage{Items: []*issueops.IssueWithCounts{}, HasMore: page.HasMore}
	}
	kept = kept[offset:]
	hasMore := page.HasMore
	if limit > 0 && len(kept) > limit {
		kept = kept[:limit]
		hasMore = true
	}
	return issueops.IssuePage{Items: kept, HasMore: hasMore}
}

// ── Claimer ─────────────────────────────────────────────────────────

type remoteClaimer struct {
	inner  issueops.Claimer
	policy *Store
}

// Claim refuses a direct claim of externally blocked work before the server's
// atomic claim. Local blocking edges are the server's claim to judge. Against
// a server that applies the policy, externalBlockersFor reads nothing and the
// claim goes through untouched.
func (c *remoteClaimer) Claim(ctx context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	blockers, err := c.policy.externalBlockersFor(ctx, []string{req.IssueID})
	if err != nil {
		return issueops.ClaimResult{}, err
	}
	if refs := blockers[req.IssueID]; len(refs) > 0 {
		return issueops.ClaimResult{}, fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, req.IssueID, refs)
	}
	return c.inner.Claim(ctx, req)
}

// ── ReadyClaimer ────────────────────────────────────────────────────

type remoteReadyClaimer struct {
	inner  issueops.ReadyClaimer
	policy *Store
}

// ClaimNext is the server's atomic claim-next, untouched, against a server
// that applies the policy itself, and against any server while nothing is
// externally blocked. Only an older server with externally blocked work
// cannot be told what to skip, so there the choice is made here: read the
// ready candidates in the requested order, skip the blocked ones, and claim
// each remaining candidate by id until one is won. A candidate another actor
// took in between is a lost race, not a failure.
func (c *remoteReadyClaimer) ClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	// externallyBlockedIDs is empty against a server that applies the policy
	// (loadBlockingState's probe), so that server's atomic claim answers.
	blocked, err := c.policy.externallyBlockedIDs(ctx)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	if len(blocked) == 0 {
		return c.inner.ClaimNext(ctx, req)
	}
	if err := storageissueops.ValidateClaimNextRequest(req); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	reader, err := c.policy.inner.IssueReader()
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	claimer, err := c.policy.inner.IssueClaimer()
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	filter := req.Filter
	filter.Offset = 0
	filter.Limit = widenedLimit(0, 1, len(blocked))
	page, err := reader.Ready(ctx, filter)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	for _, row := range page.Items {
		if row == nil || row.Issue == nil || blocked[row.ID] {
			continue
		}
		res, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: req.Actor, IssueID: row.ID})
		if errors.Is(err, issueops.ErrAlreadyClaimed) || errors.Is(err, issueops.ErrNotClaimable) || errors.Is(err, issueops.ErrNotFound) {
			continue
		}
		if err != nil {
			return issueops.ClaimNextResult{}, err
		}
		claimed := *row
		if res.Issue != nil {
			claimed.Issue = res.Issue
		}
		return issueops.ClaimNextResult{Claimed: &claimed}, nil
	}
	return issueops.ClaimNextResult{}, nil
}

// ── ReadyCounter ────────────────────────────────────────────────────

type remoteReadyCounter struct {
	inner    issueops.ReadyCounter
	fallback issueops.ReadyCounter
	policy   *Store
}

// CountReady is the server's count when the server applies the policy. Against
// any other server it is the decorator's externally filtered count.
func (c *remoteReadyCounter) CountReady(ctx context.Context, req issueops.ReadyRequest) (issueops.ReadyCountResult, error) {
	enforced, err := c.policy.serverEnforcesPolicy(ctx)
	if err != nil {
		return issueops.ReadyCountResult{}, err
	}
	if enforced {
		return c.inner.CountReady(ctx, req)
	}
	return c.fallback.CountReady(ctx, req)
}

// ── BlockingAnnotator ───────────────────────────────────────────────

type remoteBlockingAnnotator struct {
	inner  issueops.BlockingAnnotator
	policy *Store
}

// AnnotateBlocking adds each listed row's unsatisfied external blockers to the
// server's derived answer, reading the edges of those rows only. A server that
// applies the policy has already added them, and its answer is returned as is.
func (a *remoteBlockingAnnotator) AnnotateBlocking(ctx context.Context, req issueops.BlockingRequest) (issueops.BlockingResult, error) {
	result, err := a.inner.AnnotateBlocking(ctx, req)
	if err != nil {
		return issueops.BlockingResult{}, err
	}
	if enforced, err := a.policy.serverEnforcesPolicy(ctx); err != nil || enforced {
		return result, err
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	blockers, err := a.policy.externalBlockersFor(ctx, ids)
	if err != nil {
		return issueops.BlockingResult{}, err
	}
	for i := range result.Items {
		for _, ref := range blockers[result.Items[i].ID] {
			result.Items[i].BlockedBy = appendUnique(result.Items[i].BlockedBy, ref)
		}
	}
	return result, nil
}

// ── TreeWalker ──────────────────────────────────────────────────────

type remoteTreeWalker struct {
	inner  issueops.TreeWalker
	policy *Store
}

// WalkTree is the server's walk. For the plain down-tree request it also hangs
// the synthetic external leaves the local walker shows — unless the server
// enforces the external-dependency policy, whose own walker already does.
func (t *remoteTreeWalker) WalkTree(ctx context.Context, req issueops.WalkTreeRequest) (issueops.TreeResult, error) {
	result, err := t.inner.WalkTree(ctx, req)
	if err != nil || len(result.Nodes) == 0 {
		return result, err
	}
	if (req.Direction != "" && req.Direction != issueops.TreeDown) || req.Status != "" || req.MaxRows != 0 {
		return result, nil
	}
	enforced, err := t.policy.serverEnforcesPolicy(ctx)
	if err != nil || enforced {
		return result, err
	}
	ids := make([]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		if node != nil && !isExternalReference(node.ID) {
			ids = append(ids, node.ID)
		}
	}
	deps, err := t.policy.remoteEdges(ctx, ids)
	if err != nil {
		return issueops.TreeResult{}, fmt.Errorf("external dependencies: load tree edges: %w", err)
	}
	nodes, err := t.policy.appendTreeExternalReferences(ctx, result.Nodes, deps, req.MaxDepth, false)
	if err != nil {
		return issueops.TreeResult{}, err
	}
	return issueops.TreeResult{Nodes: nodes}, nil
}

// ── BatchCloser ─────────────────────────────────────────────────────

type remoteBatchCloser struct {
	inner  issueops.BatchCloser
	policy *Store
}

// CloseBatch applies the close policy around the server's batch close. A
// remote store has no policy-snapshot batch closer
// (storage.PolicyBatchCloserSource), so an unforced batch that names an
// externally blocked live issue is split: that item gets the policy refusal as
// its outcome — exactly the outcome the local batch body records — and the
// rest go to the server in one request. A blocked item that is already closed
// still travels, so its idempotent re-close and the server's not-found
// precedence are the server's answer, as they are locally.
//
// A forced batch has nothing to judge. A ClaimNext batch is passed through
// whole: no remote batch close carries a claim, so the server's refusal is the
// answer, not a claim made without the external exclusions. Against a server
// that applies the policy, externalBlockersFor reads nothing and the whole
// request goes through untouched.
func (c *remoteBatchCloser) CloseBatch(ctx context.Context, request issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	if request.Force || request.ClaimNext != nil {
		return c.inner.CloseBatch(ctx, request)
	}
	ids := make([]string, 0, len(request.Items))
	for _, item := range request.Items {
		ids = append(ids, item.IssueID)
	}
	blockers, err := c.policy.externalBlockersFor(ctx, ids)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if len(blockers) == 0 {
		return c.inner.CloseBatch(ctx, request)
	}
	if err := storageissueops.ValidateCloseBatchRequest(request); err != nil {
		return issueops.CloseBatchResult{}, err
	}

	outcomes := make([]issueops.CloseOutcome, len(request.Items))
	forward := request
	forward.Items = nil
	forwarded := make([]int, 0, len(request.Items))
	policy := storage.NewBatchClosePolicy(blockers)
	for i, item := range request.Items {
		if refusal := policy.CheckClose(item.IssueID, false); refusal != nil {
			issue, getErr := c.policy.inner.GetIssue(ctx, item.IssueID)
			if getErr == nil && issue != nil && issue.Status != types.StatusClosed {
				outcomes[i] = issueops.CloseOutcome{IssueID: item.IssueID, Err: refusal}
				continue
			}
		}
		forward.Items = append(forward.Items, item)
		forwarded = append(forwarded, i)
	}
	if len(forward.Items) > 0 {
		res, err := c.inner.CloseBatch(ctx, forward)
		if err != nil {
			return issueops.CloseBatchResult{}, err
		}
		if len(res.Outcomes) != len(forwarded) {
			return issueops.CloseBatchResult{}, fmt.Errorf("close batch: server answered %d outcomes for %d items", len(res.Outcomes), len(forwarded))
		}
		for j, i := range forwarded {
			outcomes[i] = res.Outcomes[j]
		}
	}
	return issueops.CloseBatchResult{Outcomes: outcomes}, nil
}

var (
	_ issueops.BatchCloser       = (*remoteBatchCloser)(nil)
	_ issueops.Reader            = (*remoteReader)(nil)
	_ issueops.Claimer           = (*remoteClaimer)(nil)
	_ issueops.ReadyClaimer      = (*remoteReadyClaimer)(nil)
	_ issueops.BlockingAnnotator = (*remoteBlockingAnnotator)(nil)
	_ issueops.TreeWalker        = (*remoteTreeWalker)(nil)
	_ issueops.ReadyCounter      = (*remoteReadyCounter)(nil)
)
