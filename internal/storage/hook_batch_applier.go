package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// BatchApplier returns the inner store's apply-many surface with this
// decorator's completion hooks layered over it.
//
// It recurses instead of delegating, for the reason BatchCreator does: every
// verb this role composes fires a hook on its own path, so a blind delegation
// would hand back the inner store's applier and silently stop firing all four
// vocabularies at once.
func (h *HookFiringStore) BatchApplier() (issueops.BatchApplier, error) {
	inner, err := h.inner.BatchApplier()
	if err != nil {
		return nil, err
	}
	return &hookBatchApplier{inner: inner, hooks: h}, nil
}

type hookBatchApplier struct {
	inner issueops.BatchApplier
	hooks issueOperationHooks
}

// ApplyBatch fires each item's own hook, in request order, and fires nothing at
// all when the batch refused — the request is all or nothing, so a refusal left
// no row for a script to be told about.
//
// IT FIRES ON LANDED, NOT ON "no error". There are no per-item errors here to
// make that mistake with, and ItemResult.Changed is the fact a script cares
// about, so a no-op update, an idempotent re-close and an edge that was already
// there fire nothing. hookBatchCloser once tested a nil per-item Err instead
// and announced every idempotent re-close, running the workspace's on_close
// script on every replayed teardown (ga-2yaqp.1); it now fires on Changed too,
// so the two siblings state one rule rather than two.
//
// A CREATE ALWAYS FIRES because a create always landed; the role has no
// idempotent create.
//
// AN EDGE FIRES THE UPDATE HOOK ONCE PER DISTINCT SOURCE, in first-appearance
// order and AFTER the row verbs, which is hookDependencyEditor's rule reached
// through this role. Silence would be the exact regression that wrapper's
// header warns about: a dependency change is an update to the issue the edge
// leaves, and the legacy path fires for it.
//
// AN ISSUE THIS REQUEST CREATED FIRES UPDATE FOR ITS EDGES TOO. Its create
// hook is handed the row as its create item wrote it, before any edge item
// ran, so that payload carries no dependencies: the update is the one firing
// that shows a script the edges the request gave the row. It is also what the
// other two plumbings fire for a create with an edge — a guarded create fires
// update for the edges it wrote (hookIssueOperations.Create), and the
// unit-of-work recorder for every source a batch edge leaves
// (uow.recordingDepUC.AddDependencies) — so a script sees the same firings on
// every route. bd set-state's event is such a row, and its parent edge is how
// a script tells which bead changed state (GH#5637).
//
// The result's snapshots are what the hooks are handed, hydrated inside the
// transaction that wrote them. That is why ItemResult carries an Issue at all;
// see issueops.ItemResult.Issue.
//
// A ROW THAT CAME BACK WITHOUT ITS SNAPSHOT STILL LANDED. The http leg answers
// lean (ledger row L-apply-snapshot), and a connected workspace runs its hooks
// client-side over that leg (L11), so such a row fires through completeRowByID,
// which reads it back after the commit, as the edge firings below do. Silence
// there was the bug: bd set-state over bd connect fired only its edge's update.
func (o *hookBatchApplier) ApplyBatch(ctx context.Context, request issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	result, err := o.inner.ApplyBatch(ctx, request)
	if err != nil {
		return result, err
	}
	for _, item := range result.Items {
		if !item.Changed {
			continue
		}
		if item.Issue == nil {
			o.completeRowByID(ctx, item)
			continue
		}
		switch item.Kind {
		case issueops.ItemCreate:
			o.hooks.CompleteIssueOperationCreate(ctx, item.Issue, nil)
		case issueops.ItemUpdate:
			o.hooks.CompleteIssueOperationUpdate(item.Issue)
		case issueops.ItemClose:
			o.hooks.CompleteIssueOperationClose(item.Issue)
		}
	}
	fired := make(map[string]struct{}, len(result.Items))
	for _, item := range result.Items {
		if item.Kind != issueops.ItemDepAdd || !item.Changed {
			continue
		}
		if _, already := fired[item.IssueID]; already {
			continue
		}
		fired[item.IssueID] = struct{}{}
		o.hooks.CompleteIssueOperationDependency(ctx, item.IssueID)
	}
	return result, nil
}

// completeRowByID fires a landed row verb's hook from the row read back by id,
// for a leg that answered without its snapshot. The by-id completions read only
// for a hook that would run, and what they read is the row as the whole request
// left it rather than as this item did. A dep_add carries no snapshot on any
// leg and matches no case here: ApplyBatch fires its edges on their own.
func (o *hookBatchApplier) completeRowByID(ctx context.Context, item issueops.ItemResult) {
	switch item.Kind {
	case issueops.ItemCreate:
		o.hooks.CompleteIssueOperationCreateByID(ctx, item.IssueID)
	case issueops.ItemUpdate:
		o.hooks.CompleteIssueOperationUpdateByID(ctx, item.IssueID)
	case issueops.ItemClose:
		o.hooks.CompleteIssueOperationCloseByID(ctx, item.IssueID)
	}
}
