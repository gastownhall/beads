// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/deleter.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/issueops"
)

// maxDeleteIDs mirrors the wire's own bound (internal/httpapi's delete.go, and
// the document's maxItems). It is checked HERE as well as there for
// maxAddDependencyEdges' reason, which is sharper on this operation: the whole
// delete is ONE transaction with at most one history entry, so a client that
// split a long id list into two requests would turn one all-or-nothing erasure
// into two — and the dependents guard, which can only see the ids of the request
// in front of it, would refuse a pair the caller had deliberately listed
// together. Refusing names the bound; chunking would quietly change the
// semantics.
const maxDeleteIDs = 1000

// httpDeleter serves issueops.Deleter from the deleteIssues custom method
// (design D8 row 13) — the capability behind `bd delete`.
//
// The REQUEST maps WHOLE, ExpectedVersion included: deleteIssues publishes the
// compare-and-delete precondition and this client sends it, absent staying
// absent so an unguarded delete stays unguarded.
//
// What else diverges is the REFUSAL VOCABULARY, in two places, and both are
// ledgered rather than papered over:
//
//   - an id naming no row arrives as issueops.ErrNotFound without the
//     *NotFoundError that names WHICH ids (L-delete-notfound);
//   - the unforced dependents guard arrives as issueops.ErrValidation rather
//     than *DependentsOutsideRequestError (L-delete-dependents).
//
// Both still FAIL the request and still delete nothing — the outcome is the
// contract's, only the classification is coarser — and both retire the day the
// wire grows a distinguishing code and the extension members to rebuild them
// from. `bd delete` still names the ids it could not resolve, because the
// server's own role does and the CLI is talking to the person who typed them.
type httpDeleter struct {
	store *Store
	wire  WriteWire
}

var _ issueops.Deleter = (*httpDeleter)(nil)

// Delete dials POST /v0/beads/issues:delete.
//
// The ids are sent VERBATIM — not trimmed, not deduplicated, not sorted. The
// role promises the caller's slice is read and never written through, and the
// SERVER's role performs exactly the same normalization a local one would, so
// doing it again here would be a second implementation of a rule that already
// has one, on the operation where a disagreement about which ids were named
// deletes the wrong rows.
func (d *httpDeleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	if err := checkDeleteIDs(req.IDs); err != nil {
		return issueops.DeleteResult{}, err
	}
	// The guard is sent as the caller spelled it and the ARITY rule that comes
	// with it is left to the server. The wire refuses a guard beside more than
	// one DISTINCT id, and distinctness there is measured after trimming and
	// collapsing duplicates — the same normalization this method sends its ids
	// verbatim to avoid owning a second copy of.
	body := apigen.DeleteIssuesRequest{
		Ids:             append([]string(nil), req.IDs...),
		Cascade:         &req.Cascade,
		Force:           &req.Force,
		DryRun:          &req.DryRun,
		ExpectedVersion: revisionGuard(req.ExpectedVersion),
	}
	if strings.TrimSpace(req.Actor) != "" {
		// Omitted rather than sent blank, for SweepRequest.Actor's reason: the
		// role accepts an empty Actor and the server refuses one that is empty
		// after trimming.
		body.Actor = &req.Actor
	}

	res, err := d.wire.DeleteIssues(ctx, body)
	if err != nil {
		return issueops.DeleteResult{}, err
	}
	out := issueops.DeleteResult{
		DryRun:            res.DryRun,
		Deleted:           res.Deleted,
		Dependencies:      res.Dependencies,
		Labels:            res.Labels,
		Events:            res.Events,
		ReferencesUpdated: res.ReferencesUpdated,
	}
	if res.Orphaned != nil {
		out.Orphaned = append([]string(nil), *res.Orphaned...)
	}
	return out, nil
}

// checkDeleteIDs applies the request-intrinsic refusals before the dial.
//
// Every one of them is decidable from the request alone and every one is a
// refusal the role's own contract states, so they must not cost a round trip to
// a shared server — and the empty case must not be answered as a cheerful
// "deleted 0", which is how a caller whose id list came out empty learns
// nothing at all.
func checkDeleteIDs(ids []string) error {
	if len(ids) == 0 {
		return invalid("a delete names no beads")
	}
	for i, id := range ids {
		if strings.TrimSpace(id) == "" {
			return invalid("ids[%d] is blank", i)
		}
	}
	if len(ids) > maxDeleteIDs {
		return refuse(encode.OpDeleteIssues, "L-delete-bound")
	}
	return nil
}
