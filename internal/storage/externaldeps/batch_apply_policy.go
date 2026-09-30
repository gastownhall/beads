package externaldeps

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// policyBatchApplier applies the external close guard to the items of an
// apply-batch that CLOSE: every unforced close item, and every update item
// whose patch sets the status to closed without ForceClosePolicy. It is the
// BatchCloser's guard (policyBatchCloser) restated for a request that is all
// or nothing: a refused item refuses the whole request, as an *ItemError
// naming it, before anything is written.
//
// AN APPLY-BATCH CANNOT CLAIM. issueops.UpdateItem has no Claim member (the
// wire schema has none either), so no item reaches a claim-by-id compare and
// set; a patch that sets status in_progress is the plain status edit
// `bd update --status in_progress` is, which no route treats as a claim.
//
// THE POLICY RUNS ONCE PER CALL, before the write transaction: one read of the
// closing targets' OWN edges (never the whole workspace's, so an unrelated
// issue's `external:` edge costs the batch neither a foreign open nor a
// warning), and the foreign projects resolved for the closing items' refs
// only — including refs an EARLIER dep_add item of the same request gives the
// target, since the batch can add an `external:` blocker and close its source
// in one request. No foreign store is opened inside the batch's
// transaction, and the batch runs on an applier that applies no policy of its
// own, so nothing is re-read or re-resolved per item beneath it.
//
// A flagged item (an unsatisfied blocker holds it) is refused unless it is
// ALREADY closed at its position — the idempotent re-close every close path
// promises (ga-ktn9pe.4.8). That exemption is decided ATOMICALLY with the
// write, on the arm's own terms (see policyBatchCloser for the race a separate
// status read opens):
//
//   - unit-of-work arm (guarded): the flagged ids go to an applier whose units
//     of work check "already closed?" inside the batch's transaction,
//     immediately before the item's close, against the row as the request has
//     already changed it (batchCloseGuard).
//   - store arm (pin): storage.DoltStorage publishes no transaction to share,
//     so the item is refused unless a read finds it closed, and a closed one
//     is forwarded PINNED to that state — an update item with ExpectedStatus
//     closed (as-modified, so an earlier item's reopen refuses it), a close
//     item with ExpectedVersion the read saw. A concurrent reopen between the
//     read and the batch therefore fails the batch as a precondition miss; it
//     can never close the issue. A close item whose row an EARLIER item of
//     the request rewrote cannot be pinned by a read taken before the batch,
//     so it is refused where the unit-of-work arm would decide it in the
//     transaction — the store arm fails closed on that one shape.
//
// A flagged item whose target the request itself CREATES is refused on both
// arms: the row did not exist before the batch, so there is no earlier close
// to re-close.
type policyBatchApplier struct {
	inner  issueops.BatchApplier
	policy *Policy
	// own reads the closing targets' own edges, in one call for the batch.
	own OwnEdgeSource
	// guarded (unit-of-work arm) builds the applier for one call whose
	// transaction refuses each flagged id unless it is already closed there.
	guarded func(flagged map[string][]string) (issueops.BatchApplier, error)
	// current (store arm) reads a flagged target's row, from beneath every
	// decorator, so a closed one can be pinned.
	current func(ctx context.Context, id string) (*types.Issue, error)
}

// batchClosingItem is one item of a request that closes its target unforced.
type batchClosingItem struct {
	index    int
	kind     issueops.ItemKind
	target   issueops.Ref
	identity string
	created  bool
	refs     []reference
}

// ApplyBatch guards the request's closing items, then delegates.
func (a *policyBatchApplier) ApplyBatch(ctx context.Context, req issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	// Validation runs first and without IO, so a request the role would refuse
	// costs no edge read.
	if _, err := storage.PlanApplyBatch(req); err != nil {
		return issueops.ApplyBatchResult{}, err
	}
	closing := batchClosingItems(req)
	if len(closing) == 0 {
		// Nothing closes unforced: nothing below would read the edges.
		return a.inner.ApplyBatch(ctx, req)
	}
	edges, err := a.ownEdges(ctx, closing)
	if err != nil {
		return issueops.ApplyBatchResult{}, err
	}
	var refs []reference
	for i := range closing {
		item := &closing[i]
		if !item.created {
			for _, dep := range edges[item.target.ID] {
				if dep != nil && dep.Type.IsBlockingEdge() && isExternalReference(dep.DependsOnID) {
					item.refs = append(item.refs, parseReference(dep.DependsOnID))
				}
			}
		}
		item.refs = append(item.refs, batchAddedExternalRefs(req, item.index, item.identity)...)
		refs = append(refs, item.refs...)
	}
	satisfied, err := a.policy.resolveReferences(ctx, refs)
	if err != nil {
		return issueops.ApplyBatchResult{}, fmt.Errorf("external dependencies: resolve blockers: %w", err)
	}

	forwarded := req
	pinned := false
	var flagged map[string][]string
	for _, item := range closing {
		var blockers []string
		for _, ref := range item.refs {
			if !satisfied[ref.raw] {
				blockers = appendUnique(blockers, ref.raw)
			}
		}
		if len(blockers) == 0 {
			continue
		}
		if item.created {
			return issueops.ApplyBatchResult{}, closingItemError(item, externallyBlocked(refLabel(item.target), blockers))
		}
		if a.guarded != nil {
			if flagged == nil {
				flagged = make(map[string][]string)
			}
			for _, blocker := range blockers {
				flagged[item.target.ID] = appendUnique(flagged[item.target.ID], blocker)
			}
			continue
		}
		if !pinned {
			forwarded.Items = append([]issueops.ApplyItem(nil), req.Items...)
			pinned = true
		}
		if err := a.pinReClose(ctx, forwarded.Items, item, blockers); err != nil {
			return issueops.ApplyBatchResult{}, err
		}
	}

	inner := a.inner
	if len(flagged) > 0 {
		if inner, err = a.guarded(flagged); err != nil {
			return issueops.ApplyBatchResult{}, err
		}
	}
	return inner.ApplyBatch(ctx, forwarded)
}

// ownEdges reads the existing closing targets' own edges in ONE call. A
// target the request creates has no stored edges to read; its refs come only
// from the request's dep_add items.
func (a *policyBatchApplier) ownEdges(ctx context.Context, closing []batchClosingItem) (map[string][]*types.Dependency, error) {
	var ids []string
	for _, item := range closing {
		if !item.created && item.target.ID != "" && !slices.Contains(ids, item.target.ID) {
			ids = append(ids, item.target.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	edges, err := a.own(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: list blocking records: %w", err)
	}
	return edges, nil
}

// pinReClose is the store arm's answer for one flagged item: refused unless a
// read finds its target closed, and otherwise rewritten in items (a copy the
// caller does not own) so the batch applies it only if the target is STILL
// closed when the item runs.
func (a *policyBatchApplier) pinReClose(ctx context.Context, items []issueops.ApplyItem, item batchClosingItem, blockers []string) error {
	refused := closingItemError(item, externallyBlocked(item.target.ID, blockers))
	if a.current == nil {
		// Neither arm was wired: no read can answer the exemption, so refuse
		// plainly rather than dereferencing a nil read inside a write path. This
		// is policyBatchCloser's default: arm, which degrades the same way for
		// the same mis-wiring. Unreachable from either construction site today.
		return refused
	}
	current, err := a.current(ctx, item.target.ID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	closedNow := current != nil && current.Status == types.StatusClosed
	switch item.kind {
	case issueops.ItemUpdate:
		// ExpectedStatus is evaluated AS-MODIFIED, so the pin alone decides
		// atomically; the read only answers the common case with the external
		// refusal rather than a status mismatch. A row an earlier item rewrote
		// (say, a forced close) is pinned whatever the read saw.
		if !closedNow && !identityTouchedBefore(items, item.index, item.identity) {
			return refused
		}
		update := *items[item.index].Update
		closed := types.StatusClosed
		switch {
		case update.ExpectedStatus == nil:
			update.ExpectedStatus = &closed
		case *update.ExpectedStatus != closed:
			// The caller's own guard admits a row that is NOT closed at this
			// item, which is not a re-close.
			return refused
		}
		items[item.index].Update = &update
	case issueops.ItemClose:
		if !closedNow {
			return refused
		}
		// A version guard on a row an earlier item already rewrote is not one a
		// read taken before the batch can supply, so such an item is refused.
		if identityTouchedBefore(items, item.index, item.identity) {
			return refused
		}
		closeItem := *items[item.index].Close
		switch {
		case closeItem.ExpectedVersion == nil:
			version := current.RowVersion
			closeItem.ExpectedVersion = &version
		case *closeItem.ExpectedVersion != current.RowVersion:
			// Not the closed row the read saw: whatever it names, it is not a
			// state this check proved closed.
			return refused
		}
		items[item.index].Close = &closeItem
	}
	return nil
}

// batchClosingItems lists the items that close their target without a force:
// close items, and update items whose patch sets the status to closed.
func batchClosingItems(req issueops.ApplyBatchRequest) []batchClosingItem {
	created := batchCreatedIdentities(req)
	var out []batchClosingItem
	for i, item := range req.Items {
		var target issueops.Ref
		switch {
		case item.Kind == issueops.ItemClose && item.Close != nil && !item.Close.Force:
			target = item.Close.Target
		case item.Kind == issueops.ItemUpdate && item.Update != nil && !item.Update.ForceClosePolicy &&
			item.Update.Patch.Status.Set && item.Update.Patch.Status.Value == types.StatusClosed:
			target = item.Update.Target
		default:
			continue
		}
		identity := batchRefIdentity(req, target)
		out = append(out, batchClosingItem{
			index:    i,
			kind:     item.Kind,
			target:   target,
			identity: identity,
			created:  created[identity],
		})
	}
	return out
}

// batchRefIdentity names the ROW a ref reaches: a key of a create item that
// gives an explicit id is that id, so a key ref and an id ref to the same new
// row are one identity.
func batchRefIdentity(req issueops.ApplyBatchRequest, ref issueops.Ref) string {
	if ref.Key == "" {
		return "id:" + ref.ID
	}
	for _, item := range req.Items {
		if item.Kind == issueops.ItemCreate && item.Create != nil && item.Create.Key == ref.Key &&
			item.Create.Issue != nil && item.Create.Issue.ID != "" {
			return "id:" + item.Create.Issue.ID
		}
	}
	return "key:" + ref.Key
}

// batchCreatedIdentities is every row the request creates.
func batchCreatedIdentities(req issueops.ApplyBatchRequest) map[string]bool {
	created := make(map[string]bool)
	for _, item := range req.Items {
		if item.Kind != issueops.ItemCreate || item.Create == nil {
			continue
		}
		if item.Create.Issue != nil && item.Create.Issue.ID != "" {
			created["id:"+item.Create.Issue.ID] = true
		}
		if item.Create.Key != "" {
			created[batchRefIdentity(req, issueops.Ref{Key: item.Create.Key})] = true
		}
	}
	return created
}

// batchAddedExternalRefs are the `external:` blocking edges the dep_add items
// BEFORE index give the row identity names.
func batchAddedExternalRefs(req issueops.ApplyBatchRequest, index int, identity string) []reference {
	var refs []reference
	for _, item := range req.Items[:index] {
		if item.Kind != issueops.ItemDepAdd || item.DepAdd == nil {
			continue
		}
		edge := item.DepAdd
		if edge.Target.Key != "" || !isExternalReference(edge.Target.ID) || !edge.Type.IsBlockingEdge() {
			continue
		}
		if batchRefIdentity(req, edge.Source) == identity {
			refs = append(refs, parseReference(edge.Target.ID))
		}
	}
	return refs
}

// identityTouchedBefore reports whether an item before index rewrote the row
// identity names (an update or a close of it; dep_add rewrites no row).
func identityTouchedBefore(items []issueops.ApplyItem, index int, identity string) bool {
	req := issueops.ApplyBatchRequest{Items: items}
	for _, item := range items[:index] {
		var target issueops.Ref
		switch {
		case item.Kind == issueops.ItemUpdate && item.Update != nil:
			target = item.Update.Target
		case item.Kind == issueops.ItemClose && item.Close != nil:
			target = item.Close.Target
		default:
			continue
		}
		if batchRefIdentity(req, target) == identity {
			return true
		}
	}
	return false
}

func closingItemError(item batchClosingItem, err error) error {
	id := item.target.ID
	return &issueops.ItemError{Index: item.index, Kind: item.kind, Key: item.target.Key, IssueID: id, Err: err}
}

func refLabel(ref issueops.Ref) string {
	if ref.Key != "" {
		return fmt.Sprintf("key %q", ref.Key)
	}
	return ref.ID
}

var _ issueops.BatchApplier = (*policyBatchApplier)(nil)
