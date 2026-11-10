package batchfixtures

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// UncappedPlan builds a storage.ApplyBatchPlan directly from req, bypassing
// storage.PlanApplyBatch's issueops.MaxApplyBatchItems cap check.
//
// IT EXISTS SO B0's STATEMENT-COUNT MEASUREMENT CAN RUN BEFORE B1 RAISES THE
// CAP. issueops.ApplyBatchInTx itself enforces no item-count limit — the
// limit is a request-validation POLICY that PlanApplyBatch applies before a
// plan ever reaches ApplyBatchInTx — so measuring ApplyBatchInTx's real
// statement count on a 356- or 712-item plan is a true measurement of the
// mechanism either way. Routing it through this helper rather than
// PlanApplyBatch means the B0 measurement commit does not have to be
// sequenced after, or squashed with, the B1 commit that raises the cap.
//
// It replicates PlanApplyBatch's key-index construction — the one piece of
// its output ApplyBatchInTx's own key resolution depends on — but NONE of
// its other validation (ref ordering, guard rules, edge-metadata
// normalization). A caller building a request for this helper must already
// satisfy every other rule PlanApplyBatch checks; every shape in this
// package does, which TestShapeItemCounts confirms for the one shape
// (ShapeClassic40) small enough to run through PlanApplyBatch directly under
// the current, pre-B1 cap.
func UncappedPlan(req issueops.ApplyBatchRequest) storage.ApplyBatchPlan {
	keyIndex := make(map[string]int, len(req.Items))
	for i, item := range req.Items {
		if item.Kind == issueops.ItemCreate && item.Create != nil && item.Create.Key != "" {
			keyIndex[item.Create.Key] = i
		}
	}
	items := make([]issueops.ApplyItem, len(req.Items))
	copy(items, req.Items)
	return storage.ApplyBatchPlan{
		Actor:                 req.Actor,
		Provenance:            req.Provenance,
		ForceIDPrefix:         req.ForceIDPrefix,
		SkipPerEdgeCycleCheck: req.SkipPerEdgeCycleCheck,
		Items:                 items,
		KeyIndex:              keyIndex,
	}
}
