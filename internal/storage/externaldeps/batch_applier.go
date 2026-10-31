package externaldeps

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// BatchApplier preserves external close policy on apply-many. Only the close
// targets a batch names are scoped into the policy, so an unrelated blocked issue
// does not demand policy support of a backend that lacks it.
func (s *Store) BatchApplier() (issueops.BatchApplier, error) {
	return &batchApplier{policy: s}, nil
}

type batchApplier struct{ policy *Store }

func (a *batchApplier) ApplyBatch(ctx context.Context, request issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	blockers, err := a.policy.closeTargetBlockers(ctx, request)
	if err != nil {
		return issueops.ApplyBatchResult{}, err
	}
	inner, err := storage.BatchApplierWithPolicy(a.policy.inner, storage.NewBatchClosePolicy(blockers))
	if err != nil {
		return issueops.ApplyBatchResult{}, err
	}
	return inner.ApplyBatch(ctx, request)
}

// closeTargetBlockers snapshots external blockers for the non-forced close
// targets the request names by ID. A batch with none reads no blocking state.
func (s *Store) closeTargetBlockers(ctx context.Context, request issueops.ApplyBatchRequest) (map[string][]string, error) {
	var targets []string
	for _, item := range request.Items {
		if item.Kind != issueops.ItemClose || item.Close == nil || item.Close.Force || item.Close.Target.ID == "" {
			continue
		}
		targets = append(targets, item.Close.Target.ID)
	}
	if len(targets) == 0 {
		return nil, nil
	}
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return nil, err
	}
	blockers := make(map[string][]string, len(targets))
	for _, id := range targets {
		blockers[id] = state.refsByIssue[id]
	}
	return blockers, nil
}

var _ issueops.BatchApplier = (*batchApplier)(nil)
