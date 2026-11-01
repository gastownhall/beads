package externaldeps

import (
	"context"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// BlockedLister lists the blocked issues through GetBlockedIssues above, so
// the role names unsatisfied external blockers exactly as the legacy read
// does, and `bd blocked` answers the same on either. It must be overridden
// here: the embedded passthrough would hand back the inner store's lister,
// which knows nothing of external refs.
//
// The inner layers get their turn back the way ReadyCounter gives it: the
// finished lister is wrapped by a decorator beneath that offers
// WrapBlockedLister (telemetry), asserted rather than imported.
//
// A remote store answers with its own lister (the server's blocked
// operation) whenever its server applies the policy itself, since that
// server's own decorator stack has already named the external blockers;
// against any other server the server's listing is decorated here, through
// reads a served store answers (EdgeReader, BatchGetter). remoteBlockedLister
// decides per call, since the handshake is read lazily, and a failed probe
// fails the call rather than skipping the policy. Unlike ReadyCounter's, the
// remote lister keeps no local leg to fall back on, so no local lister is
// built for it: the inner store's lister already carries the inner layers.
// Nor is remoteBlockedLister wrapped again: with telemetry on, the inner
// lister already carries its span around the server's listing, and wrapping
// would nest a second span of the same name. The policy probe, and the
// decoration against a server that leaves the policy to its clients, run
// outside that span.
func (s *Store) BlockedLister() (issueops.BlockedLister, error) {
	if s.rolesAreRemote() {
		inner, err := s.inner.BlockedLister()
		if err != nil {
			return nil, err
		}
		return &remoteBlockedLister{inner: inner, policy: s}, nil
	}
	lister, err := storageissueops.NewBlockedListerOver(s)
	if err != nil {
		return nil, err
	}
	if wrapper, ok := s.inner.(blockedListerWrapper); ok {
		lister = wrapper.WrapBlockedLister(lister)
	}
	return lister, nil
}

// blockedListerWrapper is how a decorator beneath this one adds its layer to
// a blocked lister it did not construct.
type blockedListerWrapper interface {
	WrapBlockedLister(issueops.BlockedLister) issueops.BlockedLister
}

type remoteBlockedLister struct {
	inner  issueops.BlockedLister
	policy *Store
}

// ListBlocked is the server's listing when the server applies the policy.
// Against any other server it is that listing with the external blockers
// added here, the decoration GetBlockedIssues makes over a local store.
func (l *remoteBlockedLister) ListBlocked(ctx context.Context, req issueops.BlockedRequest) (issueops.BlockedResult, error) {
	enforced, err := l.policy.serverEnforcesPolicy(ctx)
	if err != nil {
		return issueops.BlockedResult{}, err
	}
	result, err := l.inner.ListBlocked(ctx, req)
	if err != nil || enforced {
		return result, err
	}
	items, err := l.policy.decorateBlocked(ctx, storageissueops.BlockedFilter(req), result.Items,
		l.policy.remoteEdges, l.policy.remoteIssuesByIDs)
	if err != nil {
		return issueops.BlockedResult{}, err
	}
	return storageissueops.BlockedResultOf(items), nil
}

// remoteIssuesByIDs reads rows by id through the inner BatchGetter, which a
// served store answers.
func (s *Store) remoteIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	getter, err := s.inner.BatchGetter()
	if err != nil {
		return nil, err
	}
	return getManyChunked(ctx, getter, ids)
}

// getManyChunked reads rows by id through getter, in requests of at most
// issueops.MaxGetManyIDs ids, both planes, labels hydrated (GetMany's one
// hydration, the label filter's input). A missing id is simply absent, as
// GetIssuesByIDs leaves it.
func getManyChunked(ctx context.Context, getter issueops.BatchGetter, ids []string) ([]*types.Issue, error) {
	var out []*types.Issue
	for start := 0; start < len(ids); start += issueops.MaxGetManyIDs {
		end := min(start+issueops.MaxGetManyIDs, len(ids))
		res, err := getter.GetMany(ctx, issueops.GetManyRequest{IDs: ids[start:end]})
		if err != nil {
			return nil, err
		}
		out = append(out, res.Issues...)
	}
	return out, nil
}

var _ issueops.BlockedLister = (*remoteBlockedLister)(nil)
