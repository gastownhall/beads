// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/batchgetter.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// httpBatchGetter serves batchGetIssues (POST /v0/beads/issues:batchGet) —
// the role behind gc's ready veto and the CLI's own id-resolution passes,
// once they move off one Get per id.
//
// IT IS A READ, DIALED AS A POST: the request carries an `ids` array no GET
// query string can hold cleanly at the operation's own cap (MaxGetManyIDs),
// the same reason queryIssues dials POST rather than GET. Nothing about that
// changes the role's nature — it writes nothing, takes no actor and no
// version guard — so this goes through dispatch directly, the way Counter
// and Querier do, rather than through roleWire: there is no write envelope
// here for a WriteWire method to carry.
//
// THE CAP AND THE BLANK-ID CHECK ARE DONE HERE, BEFORE THE DIAL, and that is
// a deliberate divergence from how every other validation on this surface
// works: everywhere else, the role's own ErrValidation reaches the wire
// through the server's failure translation and this client decodes it back.
// batch_get.go's failBatchGetErr does not do that — it collapses EVERY
// validation failure, the cap included, into one generic
// InvalidArgument/invalid_value problem with no distinguishing code, so a
// server round trip can never hand this client the exact
// *issueops.TooManyIDsError{Requested, Cap} the role's own contract and the
// conformance suite's errors.As checks require. checkGetManyIDs reproduces
// internal/storage/issueops.ValidateGetManyRequest's exact two checks, in
// its exact order (the cap, counted before deduplication; then the first
// blank entry) — not by importing that package, which is the storage
// engine's shared body and does not belong in a transport client, but by
// restating its two rules against the same public issueops types the role
// itself is spelled in.
type httpBatchGetter struct{ store *Store }

var _ issueops.BatchGetter = (*httpBatchGetter)(nil)

// GetMany dials POST /v0/beads/issues:batchGet.
//
// The ids are sent VERBATIM — not trimmed, not deduplicated, not reordered —
// checkDeleteIDs' sibling reason: the wire's own `ids` member document says
// duplicates collapse and the answer comes back in first-mention order,
// which is exactly the normalization ValidateGetManyRequest's callers
// (ExecuteGetMany, via DedupeGetManyIDs and FinishGetMany) apply server-side.
// Redoing it here would be a second implementation of a rule that already
// has one, on the operation where a disagreement about which id was "first"
// would reorder the answer rather than merely duplicate work.
func (g *httpBatchGetter) GetMany(ctx context.Context, req issueops.GetManyRequest) (issueops.GetManyResult, error) {
	if err := checkGetManyIDs(req.IDs); err != nil {
		return issueops.GetManyResult{}, err
	}
	if len(req.IDs) == 0 {
		// Answered locally rather than dialed: an empty request is legal and
		// the role promises an empty, non-nil answer, so there is nothing a
		// round trip could add — the same early return ExecuteGetMany itself
		// takes after deduplication.
		return issueops.GetManyResult{Issues: []*issueops.Issue{}, Missing: []string{}}, nil
	}

	var body apigen.BatchGetIssuesResult
	if err := g.store.dispatch(ctx, wire.Request{
		Op:     wire.OpBatchGetIssues,
		Method: http.MethodPost,
		Path:   wire.PathIssuesBatchGet,
		Body: apigen.BatchGetIssuesRequest{
			Ids: append([]string(nil), req.IDs...),
		},
	}, &body); err != nil {
		return issueops.GetManyResult{}, err
	}

	issues := make([]*issueops.Issue, 0, len(body.Issues))
	for i := range body.Issues {
		entry := body.Issues[i]
		// BatchGetIssue.Revision is this operation's only wire spelling of the
		// row's token — Issue.RowVersion is json:"-", the same stitch
		// getIssuesByExactID and role_reader.go's getIssue make for their own
		// responses. Without it, every resolved issue would carry a RowVersion
		// of 0, and a caller that fed one into a guarded write would get a
		// spurious precondition_failed instead of none.
		version, err := parseRevision("BatchGetter.GetMany", entry.Revision)
		if err != nil {
			return issueops.GetManyResult{}, err
		}
		issue := entry.Issue
		issue.RowVersion = version
		issues = append(issues, &issue)
	}

	missing := make([]string, len(body.Missing))
	copy(missing, body.Missing)

	return issueops.GetManyResult{
		Issues:  issues,
		Missing: missing,
	}, nil
}

// checkGetManyIDs applies the request-intrinsic refusals before the dial,
// in the SAME ORDER internal/storage/issueops.ValidateGetManyRequest applies
// them server-side (batch_get.go's batchGetRequest comment says so
// explicitly): the cap first, counted on the request as sent, before any
// deduplication; then the first blank entry.
//
// The blank check is a LITERAL id == "", not a trimmed one: GetManyRequest.IDs
// are exact ids, and DedupeGetManyIDs' own doc says this role does not trim
// whitespace the way DeleteRequest.IDs does, because a caller that padded an
// id is naming a genuinely different, unresolvable id rather than making a
// typo this role should forgive. checkDeleteIDs' TrimSpace check does not
// apply here for that reason.
func checkGetManyIDs(ids []string) error {
	if len(ids) > issueops.MaxGetManyIDs {
		return &issueops.TooManyIDsError{Requested: len(ids), Cap: issueops.MaxGetManyIDs}
	}
	for i, id := range ids {
		if id == "" {
			return fmt.Errorf("%w: get many id at position %d is empty", issueops.ErrValidation, i)
		}
	}
	return nil
}
