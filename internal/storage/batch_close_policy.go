package storage

import (
	"fmt"
	"slices"
	"sort"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// BatchClosePolicy is an immutable snapshot of blockers resolved outside the
// local database. Its zero value adds no restrictions. Local close checks and
// claim selection still run inside the backend's single batch transaction.
type BatchClosePolicy struct {
	blockers map[string][]string
}

// NewBatchClosePolicy copies the snapshot so later caller edits cannot alter it.
// Include every externally blocked issue in the workspace, not just batch items:
// FilterClaim also needs to exclude blocked candidates for the next claim.
func NewBatchClosePolicy(blockers map[string][]string) BatchClosePolicy {
	snapshot := make(map[string][]string, len(blockers))
	for id, refs := range blockers {
		if len(refs) > 0 {
			snapshot[id] = slices.Clone(refs)
		}
	}
	return BatchClosePolicy{blockers: snapshot}
}

// CheckClose leaves the existing explicit close-policy override intact.
func (p BatchClosePolicy) CheckClose(id string, force bool) error {
	if refs := p.blockers[id]; !force && len(refs) > 0 {
		return fmt.Errorf("%w: %s is blocked by %v", ErrCloseBlocked, id, refs)
	}
	return nil
}

// FilterClaim excludes blocked work even when Force waived closure policy.
func (p BatchClosePolicy) FilterClaim(filter types.WorkFilter) types.WorkFilter {
	filter.ExcludeIDs = slices.Clone(filter.ExcludeIDs)
	ids := make([]string, 0, len(p.blockers))
	for id := range p.blockers {
		if !slices.Contains(filter.ExcludeIDs, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	filter.ExcludeIDs = append(filter.ExcludeIDs, ids...)
	return filter
}

// PolicyBatchCloserSource carries a policy through storage decorators without
// exposing internal filters in the public BatchCloser request or unwrapping
// away hooks and telemetry. Backends consume it inside the existing batch.
type PolicyBatchCloserSource interface {
	BatchCloserWithPolicy(BatchClosePolicy) (issueops.BatchCloser, error)
}

// BatchCloserWithPolicy refuses unsupported policy rather than bypassing it.
func BatchCloserWithPolicy(store DoltStorage, policy BatchClosePolicy) (issueops.BatchCloser, error) {
	if len(policy.blockers) == 0 {
		return store.BatchCloser()
	}
	source, ok := store.(PolicyBatchCloserSource)
	if !ok {
		return nil, &ErrUnsupported{Op: "BatchCloserWithPolicy", Backend: fmt.Sprintf("%T", store)}
	}
	return source.BatchCloserWithPolicy(policy)
}
