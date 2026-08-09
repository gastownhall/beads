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

// checkedLifecycle is the lifecycle role every mutation handler is handed.
//
// Update, Close and Reopen are why it exists: all three handlers write
// *result.Issue, which is checkedClaimer's hazard exactly. Create passes
// through because no handler on this surface reaches it — a single create is
// unpublished here, and batchCreateIssues goes to issueops.BatchCreator.
type checkedLifecycle struct{ inner issueops.Lifecycle }

// Create passes the request through unchanged: this surface publishes no
// create-one operation (batchCreateIssues reaches issueops.BatchCreator).
func (c checkedLifecycle) Create(ctx context.Context, req issueops.CreateRequest) (issueops.CreateResult, error) {
	return c.inner.Create(ctx, req)
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
