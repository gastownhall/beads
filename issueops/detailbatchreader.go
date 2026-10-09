package issueops

import (
	"context"
	"fmt"
	"strings"
)

// DetailBatchRequest asks for issue details for an ordered list of exact IDs.
// The three options have the same meaning as their GetRequest counterparts.
// IDs are not resolved, trimmed or case-folded. An empty or whitespace-only
// entry is ErrValidation; an empty list asks for an empty result. There is no
// role-level size cap. Repeated IDs remain separate items in request order.
type DetailBatchRequest struct {
	IDs               []string
	IncludeDependents bool
	IncludeComments   bool
	BriefDeps         bool
}

// DetailBatchItem is one requested ID's detail view, or its absence. Found is
// false only when neither the durable nor the wisp plane holds the ID. Issue
// is non-nil exactly when Found is true; a missing ID does not fail the call.
type DetailBatchItem struct {
	ID    string
	Found bool
	Issue *IssueDetails
}

// DetailBatchResult has one item per input ID, in input order, including repeats.
// Items is non-nil on success, even for an empty request. A failed read returns
// an error and no items, never a partial answer or silently blank fields.
// Items can share memory: repeated IDs, and relation rows for the same issue,
// may point to the same slices. Treat a result as read-only, or copy an item
// before changing it.
type DetailBatchResult struct {
	Items []DetailBatchItem
}

// ValidateDetailBatchRequest applies the shared entry rules before an accessor
// opens its read transaction. It does not change caller-owned request values.
func ValidateDetailBatchRequest(request DetailBatchRequest) error {
	for i, id := range request.IDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: detail batch id %d is empty or whitespace-only", ErrValidation, i)
		}
	}
	return nil
}

// DetailBatchReader reads many issue-detail views from one consistent snapshot.
// It is a separate role rather than a method appended to Reader: Reader.Get
// still answers one ID, whereas this role preserves an ordered batch, repeats
// and per-item absence without making the caller assemble several snapshots.
//
// Successful items match Reader.Get on the same route and options, including
// relation multiplicity and plane selection. Relation rows come in a fixed
// order: the batch keeps Reader.Get's sort keys where it has them and breaks
// ties by edge, and it sorts by edge where Reader.Get leaves the order to the
// database. Subjects are durable-first. Edge counts include all types across
// both edge tables and sum duplicates. Optional rows, parent, epic progress, brief dependencies, comment
// omission and revision retain the existing detail-view semantics.
//
// This is a read: it changes no rows, records no history and fires no completion
// hooks. Implementations snapshot the request without mutating caller-owned
// values. Validation failures match ErrValidation. An empty list and invalid
// IDs return before opening a transaction; every other failed read returns an
// error and no items. The CLI and HTTP front doors land in a later PR.
type DetailBatchReader interface {
	GetBatch(ctx context.Context, request DetailBatchRequest) (DetailBatchResult, error)
}
