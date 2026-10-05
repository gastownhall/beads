// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/relations.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// httpRelations serves issueops.Relations from listRelatedIssues (GET
// /v0/beads/issues/{id}/related) — the TWENTY-FIFTH wire-backed accessor, and
// the first read on this surface anchored on ONE issue in the PATH.
//
// IT IS SINGLE-ANCHOR, which is the difference from every other graph role here
// and the reason its miss is shaped differently. EdgeReader and GraphCounter
// take a LIST of anchors, so an id that names nothing has to be reported per
// anchor — `missing: true` beside the ones that answered. This request names
// exactly one, so there is nowhere to put a per-anchor flag and no need for one:
// an anchor on neither plane is a 404, which the shared problem mapper has
// already turned into issueops.ErrNotFound. That is the role's own promise —
// "this issue has no dependencies" and "there is no such issue" are different
// facts — and it costs this client nothing but the discipline of not inventing a
// Missing member the wire does not have.
//
// BOTH HALVES ARE TWO-PLANE and neither half is this client's work. The edges
// come from `dependencies` and `wisp_dependencies`, the far ends are hydrated
// from `issues` and `wisps`, and a WISP ID IS A LEGAL ANCHOR — all three decided
// inside the server's own transaction. What this layer must not do is narrow any
// of them, which is why there is no plane parameter here to forget: the
// operation publishes none, because the role has none.
//
// THE ELEMENT CARRIES NO REVISION BY CONSTRUCTION, so this role is the one place
// on the wave-2b surface with no revision-token stitch. types.Issue.RowVersion
// is `json:"-"` and IssueWithDependencyMetadata publishes no sibling member for
// it, unlike releaseIssue's answer — a neighbor list is not a
// read-modify-write's starting point, and nothing here composes a following
// expected_version.
//
// THE ORDER IS THE SERVER'S and is deliberately not re-applied. The role pins
// ascending by the neighbor's id with the edge type breaking a tie, and
// FinishRelatedPage — the one body both store legs run — produces exactly that
// before the rows are marshaled. Re-sorting here would be a second definition of
// one ordering, which is the drift the shared helper exists to prevent.
type httpRelations struct{ store *Store }

var _ issueops.Relations = httpRelations{}

// Related returns the anchor's neighbors in the requested direction.
//
// THE VALIDATION IS THE ROLE'S OWN BODY, not a second copy of it: this calls
// storageops.ValidateRelatedRequest, the same function the two Dolt legs and the
// unit-of-work leg run, so the three refusals — an empty anchor id, a direction
// outside the closed set, an unusable type filter — are one definition with one
// ORDER. The order is part of the answer: the id is checked FIRST, so a request
// carrying two mistakes names the same offender here as it does on every other
// backend.
//
// TWO OF THE THREE ARE ALSO REACHABLE ON THE SERVER and the third is not, which
// is why validating first is not merely an optimization. The path bound turns an
// unusable id into a 404 before the role is ever called, so the empty-id refusal
// exists ONLY on this side of the wire — a client that forwarded an empty id
// would earn `ErrNotFound` where every other backend answers `ErrValidation`.
func (r httpRelations) Related(ctx context.Context, req issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	if err := storageops.ValidateRelatedRequest(req); err != nil {
		return nil, err
	}

	path, err := wire.IssueRelatedPath(req.ID)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	// Required and always sent, including a value the role has already accepted.
	// The operation has no default direction and an absent one is a 400 rather
	// than a walk of the other end — which is the whole reason the vocabulary is
	// closed and the zero value invalid.
	//
	// The constants are the ListRelatedIssuesParamsDirection* family, spelled
	// through relationDirection below rather than as bare strings: apigen carries
	// a SECOND [out, in] enum for countDependencyEdges, so the two are one rename
	// away from silently answering each other's question.
	q.Set("direction", relationDirection(req.Direction))
	// Repeated rather than comma-joined, listDependencies' rule: the operation
	// reads `type` with the repeatable list decoder and does no splitting, so a
	// comma would travel into a type name.
	for _, depType := range req.Types {
		q.Add("type", string(depType))
	}

	var body apigen.RelatedIssues
	if err := r.store.dispatch(ctx, wire.Request{
		Op:      wire.OpListRelatedIssues,
		Method:  http.MethodGet,
		Path:    path,
		Query:   q,
		IssueID: req.ID,
	}, &body); err != nil {
		return nil, err
	}

	// Never nil for a successful call, which the role promises and a caller
	// ranges over without checking. The wire promises `items` is an empty array
	// and never null, so this is the belt to that braces — and it is the one
	// shape a decode CAN produce that the document forbids, since an absent
	// member unmarshals to a nil slice.
	items := make([]*issueops.RelatedIssue, 0, len(body.Items))
	for i := range body.Items {
		items = append(items, &body.Items[i])
	}
	return items, nil
}

// relationDirection maps the role's direction onto the parameter value, through
// the generated constants of THIS operation.
//
// It exists to make one class of mistake impossible rather than to convert two
// strings. apigen carries two [out, in] enums — this operation's
// ListRelatedIssuesParamsDirection* and countDependencyEdges'
// CountDependencyEdgesParamsDirection* — and the second one's arrival RENAMED
// the bare In/Out constants the first draft of a client like this would reach
// for. The two happen to spell the same values today, so a cross-wired reference
// would compile, pass, and answer the inverse graph the day either enum's
// values changed.
//
// A direction outside the closed set cannot reach here: ValidateRelatedRequest
// refuses it above, in the role's own order. The default arm therefore sends the
// value through UNTRANSLATED rather than guessing a direction — if the
// unreachable ever becomes reachable, the server's own 400 naming `direction` is
// a far better answer than a walk of whichever end this function picked.
func relationDirection(direction issueops.RelationDirection) string {
	switch direction {
	case issueops.RelationOut:
		return string(apigen.ListRelatedIssuesParamsDirectionOut)
	case issueops.RelationIn:
		return string(apigen.ListRelatedIssuesParamsDirectionIn)
	default:
		return string(direction)
	}
}
