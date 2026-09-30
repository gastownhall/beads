package externaldeps

import (
	"context"
	"fmt"
	"slices"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// The role wrappers below are the ONE place the external capability policy
// meets the ready vocabulary. Each computes the exclusions once per call,
// unions them into a CLONE of the caller's ReadyRequest.ExcludeIDs, and
// delegates to a role that applies no policy of its own: the inner store's
// role on the store arm, a role over the undecorated provider on the
// unit-of-work arm. Delegating to a role rebuilt over a policy decorator would
// run the exclusion query twice; delegating to one that ignores ExcludeIDs
// would drop the policy. Both are pinned by tests in this package.
//
// Validation runs first and without IO, so a request the role would refuse
// costs no edge read.
//
// THE EXCLUSIONS ARE READ BEFORE THE ROLE'S OWN TRANSACTION, not inside it, and
// that is a documented trade rather than an oversight. On the store arm there
// is no choice: storage.DoltStorage publishes no transaction a wrapper could
// share. On the unit-of-work arm the read is one read-only unit of work of the
// undecorated provider, and the claim, count or listing then runs in its own.
// Reading them INSIDE the role's transaction would mean running the inner role
// over the policy provider, whose use-case overrides would then apply the
// policy a second time — the double application this wrapper exists to avoid.
// It would also buy little: the other half of the verdict, whether a FOREIGN
// project has shipped the capability, lives in another database that no local
// transaction can pin, so the policy was never atomic with a claim.
//
// The window this leaves is narrow and named: an `external:` edge committed,
// or a foreign capability withdrawn, between the two transactions is not seen
// by that one call, which answers as of the read. Callers that need the check
// inside their own transaction have it — the use-case overrides on the
// unit-of-work arm (issueUseCase in uow_decorator.go) read the edges in the
// caller's unit of work, which is why claim-by-id and the lifecycle's claim
// and close are built over the policy provider rather than wrapped here. Those
// roles resolve the FOREIGN half first, outside the write transaction
// (preResolved), so the in-transaction check reads only local edges.

// readyPolicy binds the policy to the edge sources of one seam: edges reads
// the whole workspace's external edges, which only narrowing a READY set
// needs; own reads named issues' edges, which is all a guard on those issues
// needs.
type readyPolicy struct {
	policy *Policy
	edges  EdgeSource
	own    OwnEdgeSource
}

// ClosedSource reports whether an issue is already closed. A miss is false.
type ClosedSource func(ctx context.Context, id string) (bool, error)

// closeRefused reports whether the external close guard refuses closing id
// with blockers. Re-closing an ALREADY-CLOSED issue is not refused: it is the
// idempotent no-op every close path promises (ga-ktn9pe.4.8), and an external
// blocker that did not stop the first close has nothing left to protect.
func closeRefused(ctx context.Context, closed ClosedSource, id string, blockers []string) (bool, error) {
	if len(blockers) == 0 {
		return false, nil
	}
	if closed == nil {
		return true, nil
	}
	already, err := closed(ctx, id)
	if err != nil {
		return false, err
	}
	return !already, nil
}

// narrow returns req with every externally held-back issue excluded. req is a
// value and ExcludeIDs is freshly allocated, so the caller's request is never
// written through.
func (p readyPolicy) narrow(ctx context.Context, req issueops.ReadyRequest) (issueops.ReadyRequest, error) {
	refs, err := p.policy.Exclusions(ctx, p.edges)
	if err != nil {
		return issueops.ReadyRequest{}, err
	}
	req.ExcludeIDs = unionExcludedIDs(req.ExcludeIDs, refs)
	return req, nil
}

// policyReader applies the policy to Ready. List and Get are answered by the
// embedded reader, which the accessor chooses.
type policyReader struct {
	issueops.Reader
	ready  issueops.Reader
	policy readyPolicy
}

func newPolicyReader(rest, ready issueops.Reader, policy readyPolicy) issueops.Reader {
	return &policyReader{Reader: rest, ready: ready, policy: policy}
}

func (r *policyReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	if _, err := workapi.BuildReadyFilter(req); err != nil {
		return issueops.IssuePage{}, err
	}
	narrowed, err := r.policy.narrow(ctx, req)
	if err != nil {
		return issueops.IssuePage{}, err
	}
	return r.ready.Ready(ctx, narrowed)
}

type policyReadyCounter struct {
	inner  issueops.ReadyCounter
	policy readyPolicy
}

func newPolicyReadyCounter(inner issueops.ReadyCounter, policy readyPolicy) issueops.ReadyCounter {
	return &policyReadyCounter{inner: inner, policy: policy}
}

func (c *policyReadyCounter) CountReady(ctx context.Context, req issueops.ReadyRequest) (issueops.ReadyCountResult, error) {
	if _, err := workapi.BuildReadyCountFilter(req); err != nil {
		return issueops.ReadyCountResult{}, err
	}
	narrowed, err := c.policy.narrow(ctx, req)
	if err != nil {
		return issueops.ReadyCountResult{}, err
	}
	return c.inner.CountReady(ctx, narrowed)
}

// policyReadyLister narrows a listing exactly as policyReadyCounter narrows a
// count: one edge read per call, the exclusions unioned into a clone of the
// request's ExcludeIDs, and the request delegated to a lister that applies no
// policy of its own — so the page and its total describe one narrowed set, in
// the inner body's single pass.
type policyReadyLister struct {
	inner  issueops.ReadyLister
	policy readyPolicy
}

func newPolicyReadyLister(inner issueops.ReadyLister, policy readyPolicy) issueops.ReadyLister {
	return &policyReadyLister{inner: inner, policy: policy}
}

func (l *policyReadyLister) ListReady(ctx context.Context, req issueops.ReadyListRequest) (issueops.ReadyListing, error) {
	if _, err := workapi.BuildReadyFilter(req.ReadyRequest); err != nil {
		return issueops.ReadyListing{}, err
	}
	narrowed, err := l.policy.narrow(ctx, req.ReadyRequest)
	if err != nil {
		return issueops.ReadyListing{}, err
	}
	req.ReadyRequest = narrowed
	return l.inner.ListReady(ctx, req)
}

type policyReadyClaimer struct {
	inner  issueops.ReadyClaimer
	policy readyPolicy
}

func newPolicyReadyClaimer(inner issueops.ReadyClaimer, policy readyPolicy) issueops.ReadyClaimer {
	return &policyReadyClaimer{inner: inner, policy: policy}
}

func (c *policyReadyClaimer) ClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	if err := storageissueops.ValidateClaimNextRequest(req); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	if _, err := workapi.BuildReadyFilter(req.Filter); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	filter, err := c.policy.narrow(ctx, req.Filter)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	req.Filter = filter
	return c.inner.ClaimNext(ctx, req)
}

var (
	_ issueops.Reader       = (*policyReader)(nil)
	_ issueops.ReadyCounter = (*policyReadyCounter)(nil)
	_ issueops.ReadyLister  = (*policyReadyLister)(nil)
	_ issueops.ReadyClaimer = (*policyReadyClaimer)(nil)
)

// policyBatchCloser enforces the external close guard on every item and the
// ready exclusions on the claim a batch earns, then delegates to a closer that
// applies neither. One edge read serves both.
//
// An item an unsatisfied external blocker holds ("flagged") is refused unless
// it is ALREADY closed — the idempotent re-close every close path promises
// (ga-ktn9pe.4.8). That exemption must be decided ATOMICALLY with the close:
// deciding it on a status read taken before the batch, and then sending the
// item, let a concurrent reopen between the two turn the "re-close" into a
// real close of externally blocked work without --force. Each arm therefore
// decides it where it can be atomic:
//
//   - unit-of-work arm (guarded): the flagged ids go into the batch, and the
//     closer built for THIS call checks "already closed?" inside the batch's
//     own transaction, immediately before the close (batchCloseGuard). The
//     check costs nothing for an unflagged item and opens no extra unit of
//     work for a flagged one.
//   - store arm (settle): storage.DoltStorage publishes no transaction a
//     wrapper could share with the inner closer, so a flagged item is NEVER
//     sent to it. settle answers it from a read: an already-closed item gets
//     the idempotent re-close outcome (Changed false, the hydrated row), any
//     other is refused. Nothing is written for it, so a concurrent reopen can
//     at worst make the report "already closed" describe the moment of the
//     read — never close the issue.
//
// A refused item is SKIPPED, not sent: the inner batch closes the survivors
// in one transaction and the refusal lands at the item's own index, which is
// the BatchCloser contract for any per-item refusal. A batch that sends
// nothing never reaches the inner closer, so it lands nothing and earns no
// claim — exactly what the contract says a batch that closed nothing does.
type policyBatchCloser struct {
	inner  issueops.BatchCloser
	policy readyPolicy
	// guarded (unit-of-work arm) builds the closer for one call whose
	// transaction refuses each flagged id unless it is already closed there.
	guarded func(flagged map[string][]string) (issueops.BatchCloser, error)
	// settle (store arm) answers one flagged item without sending it.
	settle func(ctx context.Context, item issueops.BatchCloseItem, blockers []string) issueops.CloseOutcome
}

func newPolicyBatchCloser(inner issueops.BatchCloser, policy readyPolicy) *policyBatchCloser {
	return &policyBatchCloser{inner: inner, policy: policy}
}

func (c *policyBatchCloser) CloseBatch(ctx context.Context, req issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	if err := storageissueops.ValidateCloseBatchRequest(req); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if req.ClaimNext != nil {
		if _, err := workapi.BuildReadyFilter(*req.ClaimNext); err != nil {
			return issueops.CloseBatchResult{}, err
		}
	}
	if req.Force && req.ClaimNext == nil {
		// Forced, and no claim to narrow: nothing below would read the edges.
		return c.inner.CloseBatch(ctx, req)
	}
	refs, err := c.blockers(ctx, req)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}

	outcomes := make([]issueops.CloseOutcome, len(req.Items))
	sent := make([]int, 0, len(req.Items))
	forwarded := req
	forwarded.Items = make([]issueops.BatchCloseItem, 0, len(req.Items))
	var flagged map[string][]string
	for i, item := range req.Items {
		if blockers := refs[item.IssueID]; !req.Force && len(blockers) > 0 {
			switch {
			case c.guarded != nil:
				if flagged == nil {
					flagged = make(map[string][]string)
				}
				flagged[item.IssueID] = blockers
			case c.settle != nil:
				outcomes[i] = c.settle(ctx, item, blockers)
				continue
			default:
				outcomes[i] = issueops.CloseOutcome{IssueID: item.IssueID, Err: externallyBlocked(item.IssueID, blockers)}
				continue
			}
		}
		sent = append(sent, i)
		forwarded.Items = append(forwarded.Items, item)
	}
	if len(sent) == 0 {
		return issueops.CloseBatchResult{Outcomes: outcomes}, nil
	}
	if req.ClaimNext != nil {
		claim := *req.ClaimNext
		claim.ExcludeIDs = unionExcludedIDs(claim.ExcludeIDs, refs)
		forwarded.ClaimNext = &claim
	}

	inner := c.inner
	if len(flagged) > 0 {
		if inner, err = c.guarded(flagged); err != nil {
			return issueops.CloseBatchResult{}, err
		}
	}
	result, err := inner.CloseBatch(ctx, forwarded)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	for j, outcome := range result.Outcomes {
		outcomes[sent[j]] = outcome
	}
	return issueops.CloseBatchResult{Outcomes: outcomes, ClaimedNext: result.ClaimedNext}, nil
}

// blockers answers which of the batch's items an unsatisfied external blocker
// holds. With a claim to narrow, that is the workspace-wide exclusion set —
// the claim may land on any ready issue, and the same read also guards the
// items. Without one only the items' own edges are read and only their refs
// resolved, the way policyBatchApplier resolves: `bd close X` used to open
// every foreign project ANY issue referenced, and warn about an unrelated
// issue's unavailable project on every close.
func (c *policyBatchCloser) blockers(ctx context.Context, req issueops.CloseBatchRequest) (map[string][]string, error) {
	if req.ClaimNext != nil || c.policy.own == nil {
		return c.policy.policy.Exclusions(ctx, c.policy.edges)
	}
	ids := make([]string, 0, len(req.Items))
	for _, item := range req.Items {
		if !slices.Contains(ids, item.IssueID) {
			ids = append(ids, item.IssueID)
		}
	}
	return c.policy.policy.blockersOf(ctx, c.policy.own, ids)
}

// externallyBlocked is the refusal every CLOSE guard in this package returns,
// in the typed close vocabulary callers already classify with errors.Is.
func externallyBlocked(id string, blockers []string) error {
	return fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, id, blockers)
}

// externallyBlockedClaim is the refusal every CLAIM guard in this package
// returns: ErrClaimBlocked, which wraps ErrNotClaimable. It used to be the
// close refusal, so a served claim answered `not_closable` with "close with
// force" advice for an operation that has neither a close nor a force.
func externallyBlockedClaim(id string, blockers []string) error {
	return fmt.Errorf("%w: %s is blocked by %v", storage.ErrClaimBlocked, id, blockers)
}

var (
	_ issueops.BatchCloser = (*policyBatchCloser)(nil)
)
