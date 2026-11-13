package externaldeps

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/internal/workapi/storereader"
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
//     another implementation) does the policy run here, client-side — and
//     only for the reference kinds that server leaves to its clients
//     (clientKinds: a server that advertises policy.external_dependencies but
//     not policy.cross_rig_dependencies leaves the bare cross-rig ids). The
//     ready role and the ready claim judge the rows the server returned by
//     those rows' own edges, which a ready row carries (remoteRowBlockers), so
//     a page with nothing externally blocked costs no request beyond the
//     server's. So do the decorator's ready reads (GetReadyWork and its
//     counts, total and count forms, which bd ready and bd list --ready read
//     through) where only bare cross-rig ids are left to the client
//     (judgesReadyRows). Against a server that leaves the capability refs too
//     they load the blocker set from the live listing (remoteLiveSourceEdges),
//     so their totals, counts and --max-rows refusals stay exact. Every
//     per-issue question — may this issue be claimed or closed, what blocks
//     these listed rows, which external leaves hang off this tree — reads
//     only the edges of the issues it names, through the inner EdgeReader.
//     The blocked listing, which must see every live source, pages the live
//     listing too. The ready count (remoteReadyCounter) is the server's own
//     where only bare cross-rig ids are left to the client, which can count a
//     source a cross-rig blocker holds, and otherwise the decorator's exact
//     count. Nothing here walks every edge in the workspace
//     (GetAllDependencyRecords).

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
// It reads only those issues' own edges and resolves only their references:
// a remote store through the inner EdgeReader (one request per
// maxRemoteEdgeAnchors ids), a local one through GetDependencyRecordsForIssues.
// A guard about one issue (close, claim, a batch close) must not resolve
// every other reference in the workspace: that opened every foreign rig any
// cross-rig edge names, long-closed sources' included, and warned about each
// unreachable one, on every close.
func (s *Store) externalBlockersFor(ctx context.Context, ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	kinds, err := s.clientKinds(ctx)
	if err != nil || kinds.none() {
		return nil, err
	}
	return s.externalBlockersOf(ctx, kinds, ids)
}

// externalBlockersOf is externalBlockersFor under kinds the caller already
// probed.
func (s *Store) externalBlockersOf(ctx context.Context, kinds policyKinds, ids []string) (map[string][]string, error) {
	var deps map[string][]*types.Dependency
	var err error
	if s.rolesAreRemote() {
		deps, err = s.remoteEdges(ctx, ids)
	} else {
		deps, err = s.inner.GetDependencyRecordsForIssues(ctx, distinctAnchors(ids))
	}
	if err != nil {
		return nil, fmt.Errorf("external dependencies: read blocking edges: %w", err)
	}
	state, err := s.blockingStateFromRecords(ctx, deps, kinds)
	if err != nil {
		return nil, err
	}
	return state.refsByIssue, nil
}

// distinctAnchors is ids with each id named once and no empty one.
func distinctAnchors(ids []string) []string {
	anchors := make([]string, 0, len(ids))
	named := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" && !named[id] {
			named[id] = true
			anchors = append(anchors, id)
		}
	}
	return anchors
}

// remoteEdges reads the stored outgoing edges of ids through the inner
// EdgeReader, keyed by source id. Each id is named once, and an empty one not
// at all: the http EdgeReader refuses an empty anchor, which would answer a
// batch close's own validation question with an edge-read error.
func (s *Store) remoteEdges(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	anchors := distinctAnchors(ids)
	deps := make(map[string][]*types.Dependency, len(anchors))
	if len(anchors) == 0 {
		return deps, nil
	}
	reader, err := s.inner.EdgeReader()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(anchors); start += maxRemoteEdgeAnchors {
		end := min(start+maxRemoteEdgeAnchors, len(anchors))
		res, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: anchors[start:end]})
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

// remoteRowEdges is the outgoing edges of rows a remote read returned, keyed
// by row id. A ready or list row carries every edge its source stores (the
// counts query's deps_json, on the wire since before either policy token), so
// the rows themselves are the read: a row without edges has none. Only a row
// that came back without them although its blocks count says it has some (a
// server that projects them away) costs an EdgeReader read, of those rows only.
func (s *Store) remoteRowEdges(ctx context.Context, rows []*types.IssueWithCounts) (map[string][]*types.Dependency, error) {
	deps := make(map[string][]*types.Dependency)
	var unread []string
	for _, row := range rows {
		if row == nil || row.Issue == nil || row.ID == "" {
			continue
		}
		if len(row.Dependencies) == 0 {
			if row.DependencyCount > 0 {
				unread = append(unread, row.ID)
			}
			continue
		}
		for _, dep := range row.Dependencies {
			if dep != nil {
				deps[row.ID] = append(deps[row.ID], dep)
			}
		}
	}
	if len(unread) > 0 {
		read, err := s.remoteEdges(ctx, unread)
		if err != nil {
			return nil, fmt.Errorf("external dependencies: read blocking edges: %w", err)
		}
		for id, edges := range read {
			deps[id] = edges
		}
	}
	return deps, nil
}

// remoteRowBlockers is externalBlockersFor for rows a remote read already
// returned: which unsatisfied references block each row, read from the rows'
// own edges (remoteRowEdges). Only a gating reference is resolved, so a row
// whose edges are all local or non-blocking costs no request; a gating one
// costs its resolution on every evaluation, satisfied or not (a cross-rig
// target's read through its route).
func (s *Store) remoteRowBlockers(ctx context.Context, kinds policyKinds, rows []*types.IssueWithCounts) (map[string][]string, error) {
	deps, err := s.remoteRowEdges(ctx, rows)
	if err != nil || len(deps) == 0 {
		return nil, err
	}
	state, err := s.blockingStateFromRecords(ctx, deps, kinds)
	if err != nil {
		return nil, err
	}
	return state.refsByIssue, nil
}

// remoteLiveSourceEdges is loadBlockingState's candidate read over a remote
// store: the outgoing edges of every live issue, the only sources a ready,
// blocked, count or claim evaluation can return. It pages the server's
// default listing (closed and pinned rows excluded, ephemeral rows included),
// whose rows carry their own edges, so it costs one request per page of live
// issues and no edge read; GetAllDependencyRecords' walk crossed every closed
// issue too and then read every edge in the workspace.
func (s *Store) remoteLiveSourceEdges(ctx context.Context) (map[string][]*types.Dependency, error) {
	reader, err := s.inner.IssueReader()
	if err != nil {
		return nil, err
	}
	unlimited := 0
	page, err := reader.List(ctx, issueops.ListRequest{Limit: &unlimited, IncludeEphemeral: true})
	if err != nil {
		return nil, err
	}
	live := page.Items[:0:0]
	for _, row := range page.Items {
		if row != nil && row.Issue != nil && row.Status != types.StatusClosed && row.Status != types.StatusPinned {
			live = append(live, row)
		}
	}
	return s.remoteRowEdges(ctx, live)
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
// (ReadyFlag, `bd list --ready`) is the ready question, which a remote
// listing cannot carry (httpclient ledger row E-ListRequest.ReadyFlag: over
// the wire it is listReadyWork, a different operation). The policy store's
// own reader answers it through the ready reads, which the remote store
// serves and the policy filters, so it is the same answer the CLI gets
// locally; against a server that applies the policy those reads stand down.
func (r *remoteReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	if !req.ReadyFlag {
		return r.inner.List(ctx, req)
	}
	ready, err := storereader.New(r.policy)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	return ready.List(ctx, req)
}

// Ready excludes externally blocked sources, judged from the rows the server
// returns rather than from a workspace-wide blocker set.
//
// Against a server that applies the whole policy it is the server's read,
// untouched. Otherwise each returned row is judged by its own edges, which a
// ready row carries (remoteRowBlockers), so a page holding no externally
// blocked row costs exactly the server's one request: the request budgets
// Gas City's probes are pinned to hold against an older server too.
//
// No remote ready read can carry an id exclusion (L12), so a page that loses
// rows is refilled from a widened window instead: from offset 0, Offset+Limit
// plus one row per blocked id found so far, judging only rows not judged yet,
// until a window turns up no newly blocked row (it then holds the caller's
// page) or the ready set ends.
func (r *remoteReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	kinds, err := r.policy.clientKinds(ctx)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	if kinds.none() {
		return r.inner.Ready(ctx, req)
	}
	limit := workapi.LimitOr(req.Limit, workapi.DefaultReadyLimit)
	window := req
	if req.Offset > 0 {
		// The rows before the page decide where it starts, so they are read
		// and judged too.
		window.Offset = 0
		window.Limit = widenedLimit(req.Offset, limit, 0)
	}
	blocked := make(map[string]bool)
	judged := make(map[string]bool)
	for {
		page, err := r.inner.Ready(ctx, window)
		if err != nil {
			return issueops.IssuePage{}, err
		}
		grew, err := r.policy.judgeNewRows(ctx, kinds, page.Items, judged, blocked)
		if err != nil {
			return issueops.IssuePage{}, err
		}
		if !grew || limit <= 0 || !page.HasMore {
			return excludeAndPage(page, blocked, req.Offset, limit), nil
		}
		window.Offset = 0
		window.Limit = widenedLimit(req.Offset, limit, len(blocked))
	}
}

// judgeNewRows judges the rows of a remote read that no earlier read of the
// same evaluation judged, by their own edges (remoteRowBlockers), marks them
// judged, and adds the externally blocked ones to blocked. grew reports
// whether any row joined blocked.
func (s *Store) judgeNewRows(ctx context.Context, kinds policyKinds, rows []*types.IssueWithCounts, judged, blocked map[string]bool) (grew bool, err error) {
	fresh := make([]*types.IssueWithCounts, 0, len(rows))
	for _, row := range rows {
		if row != nil && row.Issue != nil && !judged[row.ID] {
			judged[row.ID] = true
			fresh = append(fresh, row)
		}
	}
	refs, err := s.remoteRowBlockers(ctx, kinds, fresh)
	if err != nil {
		return false, err
	}
	for id := range refs {
		if !blocked[id] {
			blocked[id] = true
			grew = true
		}
	}
	return grew, nil
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
	ctx = storage.WithLivePolicyProbe(ctx)
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

// remoteClaimPage is how many externally unblocked candidates one pass of
// remoteReadyClaimer reads, matching httpclient's composed ready claim.
const remoteClaimPage = 25

type remoteReadyClaimer struct {
	inner  issueops.ReadyClaimer
	policy *Store
}

// ClaimNext is the server's atomic claim-next, untouched, against a server
// that applies the policy itself. An older server cannot be told what to
// skip, so there the choice is made here, among the candidates a local
// claim-next selects (ClaimReadyIssueInTx): read the open, unassigned ready
// rows in the requested order and judge each by its own edges
// (remoteRowBlockers, no workspace-wide read).
//
// While nothing judged is externally blocked, the server's atomic claim-next
// is still the one request that claims, and its pick is checked
// (serverClaimNext): it re-reads the ready set when it claims, so it can take
// a row this read never judged, which is then judged and given back if it is
// blocked. Otherwise, and after such a release, the blocked rows are skipped
// and each remaining candidate is claimed by id until one is won. A window
// whose rows are all blocked is widened past them. A candidate another actor
// took in between is a lost race and the next one is tried. A full window
// lost end to end is read once more: claimed rows leave the unassigned set,
// so the second pass reads the candidates behind them. Only a claim that
// tried candidates and won none is an error.
func (c *remoteReadyClaimer) ClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	ctx = storage.WithLivePolicyProbe(ctx)
	kinds, err := c.policy.clientKinds(ctx)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	if kinds.none() {
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
	// Unassigned whatever assignee the request names, as locally: a row
	// another actor holds is never a candidate. Each window holds
	// remoteClaimPage candidates past the blocked rows found so far.
	filter := req.Filter
	filter.Unassigned = true
	filter.Assignee = ""
	filter.Offset = 0
	window := remoteClaimPage
	filter.Limit = &window
	blocked := make(map[string]bool)
	judged := make(map[string]bool)
	lost, passes := 0, 0
	for {
		page, err := reader.Ready(ctx, filter)
		if err != nil {
			return issueops.ClaimNextResult{}, err
		}
		if _, err := c.policy.judgeNewRows(ctx, kinds, page.Items, judged, blocked); err != nil {
			return issueops.ClaimNextResult{}, err
		}
		if len(blocked) == 0 {
			res, kept, err := c.serverClaimNext(ctx, kinds, req, judged, blocked)
			if err != nil || kept {
				return res, err
			}
		}
		claimed, tried, err := claimFirstCandidate(ctx, claimer, req.Actor, page.Items, blocked)
		if err != nil {
			return issueops.ClaimNextResult{}, err
		}
		if claimed != nil {
			return issueops.ClaimNextResult{Claimed: claimed}, nil
		}
		full := len(page.Items) >= window
		if tried == 0 {
			if !full {
				// Nothing unblocked is left to take, whatever an earlier pass lost.
				return issueops.ClaimNextResult{}, nil
			}
			// Every row of a full window is blocked: read past them. The
			// window grows with the blocked set, and the ready set is finite.
			window = remoteClaimPage + len(blocked)
			filter.Limit = &window
			continue
		}
		lost += tried
		passes++
		if passes >= 2 || !full {
			// A short window is the whole front: reading it again would only
			// re-read the rows that just refused.
			break
		}
		window = remoteClaimPage + len(blocked)
		filter.Limit = &window
	}
	// There was work, and every candidate went to another actor first: an
	// empty answer would say there is no work.
	return issueops.ClaimNextResult{}, fmt.Errorf("claim ready: lost %d races for externally unblocked ready work; re-run to take the next one", lost)
}

// serverClaimNext claims through the server's atomic claim-next and checks
// what it took. The server re-reads the ready set when it claims and applies
// none of the client's policy, so it can take a row this evaluation never
// judged: rivals took the judged rows first, or a row became ready ahead of
// them. Such a row is judged now, by an edge read of it alone. kept reports
// that res is the answer: no work, the server's error, a judged pick, or an
// unjudged one that nothing external blocks. An unjudged pick that an
// unsatisfied reference blocks, or that cannot be judged, is not kept: its
// claim is released (failing closed), and a blocked one joins blocked so the
// caller claims a judged candidate by id instead.
func (c *remoteReadyClaimer) serverClaimNext(ctx context.Context, kinds policyKinds, req issueops.ClaimNextRequest, judged, blocked map[string]bool) (res issueops.ClaimNextResult, kept bool, err error) {
	res, err = c.inner.ClaimNext(ctx, req)
	if err != nil || res.Claimed == nil || res.Claimed.Issue == nil || judged[res.Claimed.ID] {
		return res, true, err
	}
	id := res.Claimed.ID
	refs, judgeErr := c.policy.externalBlockersOf(ctx, kinds, []string{id})
	if judgeErr == nil && len(refs[id]) == 0 {
		return res, true, nil
	}
	cause := judgeErr
	if cause == nil {
		cause = fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, id, refs[id])
	}
	if err := c.release(ctx, req.Actor, res.Claimed); err != nil {
		return issueops.ClaimNextResult{}, true, fmt.Errorf("claim ready: the server claimed %s, which this claim must not keep (%w), and releasing it failed: %w", id, cause, err)
	}
	if judgeErr != nil {
		return issueops.ClaimNextResult{}, true, fmt.Errorf("claim ready: the server claimed %s, and judging it failed, so the claim was released: %w", id, judgeErr)
	}
	judged[id] = true
	blocked[id] = true
	return issueops.ClaimNextResult{}, false, nil
}

// release gives back the claim the server's claim-next made on claimed. It
// names the holder that claim set (ExpectedAssignee), so it releases exactly
// that claim and never one that has moved since.
func (c *remoteReadyClaimer) release(ctx context.Context, actor string, claimed *issueops.IssueWithCounts) error {
	releaser, err := c.policy.inner.Releaser()
	if err != nil {
		return err
	}
	req := issueops.ReleaseRequest{Actor: actor, IssueID: claimed.ID}
	if holder := claimed.Assignee; holder != "" {
		req.ExpectedAssignee = &holder
	}
	_, err = releaser.Release(ctx, req)
	return err
}

// claimFirstCandidate claims, by id and in order, the first row that is not
// externally blocked and that no other actor takes first. tried counts the
// candidates it attempted, so with nothing claimed it is the races lost.
func claimFirstCandidate(ctx context.Context, claimer issueops.Claimer, actor string, rows []*issueops.IssueWithCounts, blocked map[string]bool) (*issueops.IssueWithCounts, int, error) {
	tried := 0
	for _, row := range rows {
		if row == nil || row.Issue == nil || blocked[row.ID] {
			continue
		}
		tried++
		res, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: actor, IssueID: row.ID})
		if isLostClaimRace(err) {
			continue
		}
		if err != nil {
			return nil, tried, err
		}
		claimed := *row
		if res.Issue != nil {
			claimed.Issue = res.Issue
		}
		return &claimed, tried, nil
	}
	return nil, tried, nil
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

// ── ReadyCounter ────────────────────────────────────────────────────

type remoteReadyCounter struct {
	inner    issueops.ReadyCounter
	fallback issueops.ReadyCounter
	policy   *Store
}

// CountReady is the server's count when the server applies the policy, and
// also when it applies all of it but the bare cross-rig ids (a server that
// advertises policy.external_dependencies without
// policy.cross_rig_dependencies). There an exact client-side count would have
// to judge every ready row's edges, a read the size of the ready set, for a
// number that only decorates the page; so the count, unlike the page, can
// include a source an open cross-rig blocker holds. Against a server that
// applies none of the policy it is the decorator's count (CountReadyWork),
// exact over the blocker set loaded from the live listing (judgesReadyRows).
func (c *remoteReadyCounter) CountReady(ctx context.Context, req issueops.ReadyRequest) (issueops.ReadyCountResult, error) {
	kinds, err := c.policy.clientKinds(ctx)
	if err != nil {
		return issueops.ReadyCountResult{}, err
	}
	if !kinds.capability {
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
		if req.IncludeIsBlocked && len(blockers[result.Items[i].ID]) > 0 {
			result.Items[i].IsBlocked = boolPtr(true)
		}
	}
	return result, nil
}

// remoteExternalBlocked adds the external-dependency invariant to a remote
// store's IsBlockedBatch answer: an id with an unsatisfied external blocker
// is blocked. A server that enforces the policy already folded it in.
func (s *Store) remoteExternalBlocked(ctx context.Context, ids []string, blocked map[string]bool) (map[string]bool, error) {
	if enforced, err := s.serverEnforcesPolicy(ctx); err != nil || enforced {
		if err != nil {
			return nil, err
		}
		return blocked, nil
	}
	external, err := s.externalBlockersFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for id, refs := range external {
		if len(refs) > 0 {
			blocked[id] = true
		}
	}
	return blocked, nil
}

// ── TreeWalker ──────────────────────────────────────────────────────

type remoteTreeWalker struct {
	inner  issueops.TreeWalker
	policy *Store
}

// WalkTree is the server's walk. For the plain down-tree request it also hangs
// the external leaves the local walker shows, of the reference kinds the
// server leaves to the client (clientKinds): the server's own walker hangs the
// leaves of each kind it applies, so a server that applies them all is
// answered as is, and an older one that applies only capability refs has its
// external: leaves left alone, unresolved here.
//
// The probe only spares reads: a leaf is display, not enforcement, and
// appendTreeExternalReferences skips an edge the tree already holds. So a
// probe that fails renders every kind's leaves here rather than failing a walk
// the server has already answered.
func (t *remoteTreeWalker) WalkTree(ctx context.Context, req issueops.WalkTreeRequest) (issueops.TreeResult, error) {
	result, err := t.inner.WalkTree(ctx, req)
	if err != nil || len(result.Nodes) == 0 {
		return result, err
	}
	if (req.Direction != "" && req.Direction != issueops.TreeDown) || req.Status != "" || req.MaxRows != 0 {
		return result, nil
	}
	kinds, err := t.policy.clientKinds(ctx)
	if err != nil {
		kinds = allKinds
	}
	if kinds.none() {
		return result, nil
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
	nodes, err := t.policy.appendTreeExternalReferences(ctx, kinds, result.Nodes, deps, req.MaxDepth)
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
// or missing still travels, so its idempotent re-close and the server's
// not-found precedence are the server's answer, as they are locally; one whose
// state cannot be read gets that read's error (liveRefusal).
//
// A forced batch has nothing to judge. A ClaimNext batch goes to
// closeAndClaimNext, Force or not. Against a server that applies the policy,
// externalBlockersFor reads nothing and the whole request goes through
// untouched.
func (c *remoteBatchCloser) CloseBatch(ctx context.Context, request issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	ctx = storage.WithLivePolicyProbe(ctx)
	if request.ClaimNext != nil {
		return c.closeAndClaimNext(ctx, request)
	}
	if request.Force {
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
			if err := c.liveRefusal(ctx, item.IssueID, refusal); err != nil {
				outcomes[i] = issueops.CloseOutcome{IssueID: item.IssueID, Err: err}
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

// liveRefusal is the outcome of an externally blocked item, or nil when the
// item travels to the server. As in the local close only a live issue is
// refused, so an already closed one keeps its idempotent re-close and a
// missing one the server's not-found. Any other failed read is the item's
// outcome: it is neither refused nor closed on a guess.
func (c *remoteBatchCloser) liveRefusal(ctx context.Context, issueID string, refusal error) error {
	issue, err := c.policy.inner.GetIssue(ctx, issueID)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil
	case err != nil:
		return err
	case issue != nil && issue.Status != types.StatusClosed:
		return refusal
	}
	return nil
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

// closeAndClaimNext answers a batch with a next claim. A request that is
// invalid on every backend is still ErrValidation. Against a server that does
// not apply the policy the batch is refused whole, before anything is read or
// closed: a served close cannot keep externally blocked work out of its claim,
// and the v0 wire's batch close cannot carry a next claim at all (ledger row
// W-CloseBatchRequest.ClaimNext). A server that applies the policy gets the
// request as it came; over the v0 wire its closer refuses the claim too.
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
			"close with a next claim: the served batch close cannot carry a next claim, so nothing was closed; close without --claim-next, then run `bd ready --claim` (%w)",
			&storage.ErrUnsupported{Op: "CloseBatchRequest.ClaimNext", Backend: fmt.Sprintf("%T", storage.UnwrapStore(c.policy.inner))})
	}
	return c.inner.CloseBatch(ctx, request)
}
