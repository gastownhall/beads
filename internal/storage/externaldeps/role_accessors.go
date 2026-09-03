package externaldeps

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/internal/workapi/storereader"
	"github.com/steveyegge/beads/issueops"
)

// IssueReader builds the read role on the policy store. In particular, Ready
// and List(ReadyFlag) must call this store's filtered ready methods rather than
// promoted methods on the undecorated store.
func (s *Store) IssueReader() (issueops.Reader, error) { return storereader.New(s) }

// IssueClaimer rejects a direct claim of externally blocked work before the
// backend's atomic claim operation. ReadyClaimer below handles selection among
// candidates; this method covers callers that already name an issue.
func (s *Store) IssueClaimer() (issueops.Claimer, error) {
	inner, err := s.inner.IssueClaimer()
	if err != nil {
		return nil, err
	}
	return &issueClaimer{inner: inner, policy: s}, nil
}

type issueClaimer struct {
	inner  issueops.Claimer
	policy *Store
}

func (c *issueClaimer) Claim(ctx context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	blocked, blockers, err := c.policy.IsBlocked(ctx, req.IssueID)
	if err != nil {
		return issueops.ClaimResult{}, err
	}
	if blocked {
		return issueops.ClaimResult{}, fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, req.IssueID, blockers)
	}
	return c.inner.Claim(ctx, req)
}

// ReadyClaimer keeps external blockers out of the ready-claim selection used
// by HTTP serving. The local compare-and-swap remains inside ClaimReadyIssue.
func (s *Store) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return &readyClaimer{policy: s}, nil
}

type readyClaimer struct{ policy *Store }

func (c *readyClaimer) ClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	if err := storageissueops.ValidateClaimNextRequest(req); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	filter, err := workapi.BuildReadyFilter(req.Filter)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	// ClaimReadyIssue does not perform the lazy wake owned by backend ready
	// roles. Preserve it before selection, including through telemetry/hooks.
	if waker, ok := storage.UnwrapStore(c.policy.inner).(storage.ExpiredDeferWaker); ok {
		waker.WakeExpiredDefersAdvisory(ctx)
	}
	claimed, err := c.policy.ClaimReadyIssue(ctx, filter, req.Actor)
	if err != nil || claimed == nil {
		return issueops.ClaimNextResult{}, err
	}
	rows, err := c.policy.SearchIssuesWithCounts(ctx, "", types.IssueFilter{IDs: []string{claimed.ID}})
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	if len(rows) != 1 {
		return issueops.ClaimNextResult{}, fmt.Errorf("claim ready: hydrate %s: expected one row, got %d", claimed.ID, len(rows))
	}
	return issueops.ClaimNextResult{Claimed: rows[0]}, nil
}

// BlockingAnnotator augments the backend's derived local answer with the
// external blockers that the policy resolves for the same ids.
func (s *Store) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	inner, err := s.inner.BlockingAnnotator()
	if err != nil {
		return nil, err
	}
	return &blockingAnnotator{inner: inner, policy: s}, nil
}

type blockingAnnotator struct {
	inner  issueops.BlockingAnnotator
	policy *Store
}

func (a *blockingAnnotator) AnnotateBlocking(ctx context.Context, req issueops.BlockingRequest) (issueops.BlockingResult, error) {
	result, err := a.inner.AnnotateBlocking(ctx, req)
	if err != nil {
		return issueops.BlockingResult{}, err
	}
	for i := range result.Items {
		blocked, blockers, err := a.policy.IsBlocked(ctx, result.Items[i].ID)
		if err != nil {
			return issueops.BlockingResult{}, err
		}
		if blocked {
			result.Items[i].BlockedBy = blockers
		}
	}
	return result, nil
}

// TreeWalker preserves synthetic external leaves for the normal down-tree
// request. Reverse walks do not follow a source's dependencies, and the
// combined/up variants retain the backend's existing traversal semantics.
func (s *Store) TreeWalker() (issueops.TreeWalker, error) {
	inner, err := s.inner.TreeWalker()
	if err != nil {
		return nil, err
	}
	return &treeWalker{inner: inner, policy: s}, nil
}

type treeWalker struct {
	inner  issueops.TreeWalker
	policy *Store
}

func (t *treeWalker) WalkTree(ctx context.Context, req issueops.WalkTreeRequest) (issueops.TreeResult, error) {
	if (req.Direction != "" && req.Direction != issueops.TreeDown) || req.Status != "" || req.MaxRows != 0 {
		return t.inner.WalkTree(ctx, req)
	}
	nodes, err := t.policy.GetDependencyTree(ctx, req.RootID, req.MaxDepth, false, false)
	if err != nil {
		return issueops.TreeResult{}, err
	}
	return issueops.TreeResult{Nodes: nodes}, nil
}

var (
	_ issueops.Claimer           = (*issueClaimer)(nil)
	_ issueops.ReadyClaimer      = (*readyClaimer)(nil)
	_ issueops.BlockingAnnotator = (*blockingAnnotator)(nil)
	_ issueops.TreeWalker        = (*treeWalker)(nil)
)
