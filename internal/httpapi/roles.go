package httpapi

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/issueops"
)

// The role seam, and the one guarantee this package cannot get by asking for
// it.
//
// Two handlers hold a POINTER that a role returned and dereference it the
// moment the error is nil: handleGetIssue writes *details, handleClaim writes
// *result.Issue. Until roles became configuration those dereferences were safe
// BY CONSTRUCTION — s.reader() could only return uow.NewIssueReader(...),
// whose Get routes through workapi.GetIssueOrWisp, and that function exists
// precisely so that no caller can be handed a nil issue with a nil error. A
// role reached through Config is ordinary caller-supplied code, and the same
// dereference is a nil pointer panic on a live server.
//
// So the fold happens here instead, on EVERY role this server hands a handler,
// from either database source. Unconditionally, and that is the point: the
// dereferences above are safe because there is no other way to reach them, not
// because the provider path is separately known to be well behaved. The
// provider path pays one wrapper allocation per request for the privilege of
// making that a single sentence.
//
// The vocabulary is the one ClassifyError already documents: the shared read
// path folds "a nil issue with a nil error" into ErrNotFound, so a reader that
// answers with nothing answers the documented miss. A claim has no such
// reading — a claim that reports success has written a row, or it has not
// succeeded — so it falls to the generic 500, which is where an unrecognized
// error goes.

// checkedReader is the reader every read handler is handed.
//
// Ready and List need nothing from it: both return a VALUE, and wireItems
// already drops a nil element out of a page. Get is the whole reason the type
// exists.
//
// issueops.EdgeReader has no wrapper here for the same reason: it answers with
// a value, and wireEdges drops a nil edge.
//
// issueops.Relations has none either, and it is the case worth stating because
// it is the first role that answers with a SLICE OF POINTERS and still needs no
// wrapper. What decides it is not the pointer, it is whether a handler
// dereferences one it was handed: wireRelated skips a nil element exactly as
// wireEdges and wireItems do, so the only thing a wrapper could add is the
// appearance of a guarantee.
type checkedReader struct{ inner issueops.Reader }

// Ready passes the request through unchanged.
func (c checkedReader) Ready(ctx context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	return c.inner.Ready(ctx, req)
}

// List passes the request through unchanged.
func (c checkedReader) List(ctx context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	return c.inner.List(ctx, req)
}

// Get folds a detail view that is absent without being an error into the miss
// the document already describes, so the client gets the same 404 an absent
// issue produces rather than a panic recovered into a generic 500.
func (c checkedReader) Get(ctx context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	details, err := c.inner.Get(ctx, req)
	if err == nil && details == nil {
		return nil, fmt.Errorf("get %q: %w", req.ID, issueops.ErrNotFound)
	}
	return details, err
}

// checkedBatchCreator is the creator the batch-create handler is handed.
//
// The response body carries issue VALUES where the role answers with pointers,
// so the handler dereferences every entry — checkedClaimer's hazard, once per
// item. An entry that is nil anyway is a broken implementation, so it is the
// generic 500 with the fault in the log and not a panic.
type checkedBatchCreator struct{ inner issueops.BatchCreator }

// CreateBatch refuses a result that reports success without every issue the
// response body is built from.
func (c checkedBatchCreator) CreateBatch(ctx context.Context, req issueops.CreateBatchRequest) (issueops.CreateBatchResult, error) {
	result, err := c.inner.CreateBatch(ctx, req)
	if err != nil {
		return result, err
	}
	if len(result.Issues) != len(req.Items) {
		return issueops.CreateBatchResult{}, fmt.Errorf(
			"create batch: the creator reported success with %d issues for %d items", len(result.Issues), len(req.Items))
	}
	for i, issue := range result.Issues {
		if issue == nil {
			return issueops.CreateBatchResult{}, fmt.Errorf("create batch: the creator reported success without issue %d", i)
		}
	}
	return result, nil
}

// checkedBatchCloser is the closer the batch-close handler is handed.
//
// It is the one role here whose result is POSITIONAL: the document promises one
// outcome per requested item in request order, and a client walks the array
// against its own argument list rather than matching ids back up. So the two
// broken shapes it folds are at different scopes, and the scope is the whole
// reasoning.
//
//   - AN OUTCOME COUNT THAT DOES NOT MATCH THE REQUEST is the whole batch's
//     fault: it cannot be walked against the caller's items at all, and
//     projecting it anyway would report one item's answer under another item's
//     id for the rest of the array — silently, inside a 200. It is
//     checkedBatchCreator's whole-batch refusal exactly, and it is the generic
//     500 with the fault in the log, never a partial projection.
//
//   - AN OUTCOME CARRYING NEITHER A ROW NOR A REFUSAL has no honest wire shape:
//     `code`'s absence promises `issue`, so projected as it stands it is a
//     success with no row — the shape closeOutcome's `default` branch already
//     refuses to produce. It is folded into THAT ITEM's Err instead, because
//     this batch is not all-or-nothing: its survivors have already committed,
//     and a whole-batch 500 would hide durable closes from a caller with no way
//     to re-read them. The handler maps it through the branch it wrote for
//     exactly this, and the rest of the outcomes are untouched.
//
// batchCloser() used to hand this role out unwrapped on the reasoning that
// CloseBatchResult is a value and its issue pointer is forwarded rather than
// dereferenced. That is true, and it is why there is no panic here to prevent —
// but it left the two shapes above reaching a client as answers rather than as
// faults.
type checkedBatchCloser struct{ inner issueops.BatchCloser }

// CloseBatch refuses a miscounted result and folds a neither-row-nor-refusal
// outcome into its own item.
func (c checkedBatchCloser) CloseBatch(ctx context.Context, req issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	result, err := c.inner.CloseBatch(ctx, req)
	if err != nil {
		return result, err
	}
	if len(result.Outcomes) != len(req.Items) {
		return issueops.CloseBatchResult{}, fmt.Errorf(
			"close batch: the closer reported %d outcomes for %d items", len(result.Outcomes), len(req.Items))
	}
	corrected := false
	for i := range result.Outcomes {
		if result.Outcomes[i].Err != nil || result.Outcomes[i].Issue != nil {
			continue
		}
		if !corrected {
			// Correct a COPY. The slice is the role's own, and a role that
			// answers from one prepared result would carry this server's
			// correction into everything it hands back afterwards.
			result.Outcomes = append([]issueops.CloseOutcome(nil), result.Outcomes...)
			corrected = true
		}
		result.Outcomes[i].Err = fmt.Errorf(
			"close batch: the closer reported outcome %d (%q) with neither an issue nor a refusal",
			i, result.Outcomes[i].IssueID)
	}
	return result, nil
}

// checkedLifecycle is the lifecycle role every mutation handler is handed.
//
// All four methods are why it exists: every handler over this role writes
// *result.Issue, which is checkedClaimer's hazard exactly.
type checkedLifecycle struct{ inner issueops.Lifecycle }

// Create refuses a result that reports success without the row the response
// body is built from, for Close's reason.
//
// It USED to pass through, because no handler on this surface reached it. The
// single create publishes one, and handleCreateIssue writes *result.Issue
// straight onto the wire, so the same nil-with-nil-error a provider-supplied
// role can return is a panic on a live server without this.
func (c checkedLifecycle) Create(ctx context.Context, req issueops.CreateRequest) (issueops.CreateResult, error) {
	result, err := c.inner.Create(ctx, req)
	if err == nil && result.Issue == nil {
		return issueops.CreateResult{}, fmt.Errorf("create: the lifecycle reported success without an issue")
	}
	return result, err
}

// Update refuses a result that reports success without the row the response
// body is built from, for Close's reason.
func (c checkedLifecycle) Update(ctx context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	result, err := c.inner.Update(ctx, req)
	if err == nil && result.Issue == nil {
		return issueops.UpdateResult{}, fmt.Errorf("update %q: the lifecycle reported success without an issue", req.IssueID)
	}
	return result, err
}

// Close refuses a result that reports success without the row the response body
// is built from.
//
// There is no wire code for it and there must not be one: `already_closed` says
// the issue was closed earlier and the response still carries the row, and a
// 404 would tell a client the issue does not exist when nothing here knows
// that. It is a broken implementation, so it is the generic 500 — with the
// fault in the log as an error and a request_error line beside it, which is
// what the panic it replaces did not produce.
func (c checkedLifecycle) Close(ctx context.Context, req issueops.CloseRequest) (issueops.CloseResult, error) {
	result, err := c.inner.Close(ctx, req)
	if err == nil && result.Issue == nil {
		return issueops.CloseResult{}, fmt.Errorf("close %q: the lifecycle reported success without an issue", req.IssueID)
	}
	return result, err
}

// Reopen refuses a result that reports success without the row the response
// body is built from, for Close's reason: handleReopen dereferences it, and a
// broken implementation should be the generic 500 with the fault in the log
// rather than a panic on a live server.
func (c checkedLifecycle) Reopen(ctx context.Context, req issueops.ReopenRequest) (issueops.ReopenResult, error) {
	result, err := c.inner.Reopen(ctx, req)
	if err == nil && result.Issue == nil {
		return issueops.ReopenResult{}, fmt.Errorf("reopen %q: the lifecycle reported success without an issue", req.IssueID)
	}
	return result, err
}

// checkedClaimer is the claimer the claim handler is handed.
type checkedClaimer struct{ inner issueops.Claimer }

// Claim refuses a result that reports success without the row the response
// body is built from.
//
// There is no wire code for it and there must not be one: `already_claimed`
// says a claim landed earlier and the response still carries the row, and a
// 404 would tell a client the issue does not exist when nothing here knows
// that. It is a broken implementation, so it is the generic 500 — with the
// fault in the log as an error and a request_error line beside it, which is
// what the panic it replaces did not produce.
func (c checkedClaimer) Claim(ctx context.Context, req issueops.ClaimRequest) (issueops.ClaimResult, error) {
	result, err := c.inner.Claim(ctx, req)
	if err == nil && result.Issue == nil {
		return issueops.ClaimResult{}, fmt.Errorf("claim %q: the claimer reported success without an issue", req.IssueID)
	}
	return result, err
}

// checkedCommenter is the commenter the add-comment handler is handed.
//
// It exists for checkedClaimer's reason exactly: handleAddComment writes
// *result.Comment onto the wire, so a role that reported success without the row
// would panic on a live server.
type checkedCommenter struct{ inner issueops.Commenter }

// AddComment refuses a result that reports success without the row the response
// body is built from.
//
// The generic 500, for checkedClaimer's reason. There is no wire code that fits
// and there must not be: a 404 would say the issue does not exist when the role
// just said it appended a comment to it, and this operation has no conflict code
// at all. It is a broken implementation.
func (c checkedCommenter) AddComment(ctx context.Context, req issueops.AddCommentRequest) (issueops.AddCommentResult, error) {
	result, err := c.inner.AddComment(ctx, req)
	if err == nil && result.Comment == nil {
		return issueops.AddCommentResult{}, fmt.Errorf("add comment %q: the commenter reported success without a comment", req.IssueID)
	}
	return result, err
}

// checkedReleaser is the releaser the release handler is handed.
//
// It exists for checkedClaimer's reason exactly: handleRelease writes
// *result.Issue and reads its RowVersion, so a role that reported success
// without the row would panic on a live server.
type checkedReleaser struct{ inner issueops.Releaser }

// Release refuses a result that reports success without the row the response
// body is built from.
//
// The generic 500, for checkedClaimer's reason and one of its own: there is no
// wire code that fits and there must not be. A 409 would say the row refused
// the release when the role said it did not, and a 404 would say the issue does
// not exist when nothing here knows that. It is a broken implementation.
func (c checkedReleaser) Release(ctx context.Context, req issueops.ReleaseRequest) (issueops.ReleaseResult, error) {
	result, err := c.inner.Release(ctx, req)
	if err == nil && result.Issue == nil {
		return issueops.ReleaseResult{}, fmt.Errorf("release %q: the releaser reported success without an issue", req.IssueID)
	}
	return result, err
}
