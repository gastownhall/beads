// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/counter.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// httpCounter is issueops.Counter over countIssues (GET /v0/beads/issues:count)
// — the operation behind `bd count`, and the TWENTY-FIRST wire-backed accessor.
//
// ONE OPERATION, TWO METHODS. `group_by` chooses between them, which is the
// role's own shape rather than this layer's: a grouped count is the scalar
// count plus a dimension, asked of the same set. So both methods encode through
// one builder and dial one path, and the only thing that differs is the one
// parameter and the one response member.
//
// WHAT A COUNT MAKES DIFFERENT from every other read here is that its answer
// carries no evidence of the set it came from. A listing that quietly widened
// hands back rows a caller can look at; a count hands back a number, and a
// number computed from a filter this client forgot to send is indistinguishable
// from the right one. That is why the encoder's refusal walk runs on a request
// whose every member the wire publishes: today it refuses nothing, and the day
// upstream adds a twenty-fourth member it refuses rather than answering about a
// wider set.
type httpCounter struct{ store *Store }

// Count returns how many issues match.
func (c httpCounter) Count(ctx context.Context, req issueops.CountRequest) (issueops.CountResult, error) {
	if err := c.refuseUnservedScope(ctx, "Counter.Count", req); err != nil {
		return issueops.CountResult{}, err
	}
	params, err := encode.CountParams(req)
	if err != nil {
		return issueops.CountResult{}, c.store.inexpressible("Counter.Count", err)
	}
	var body apigen.IssueCount
	if err := c.store.dispatch(ctx, wire.Request{
		Op:     wire.OpCountIssues,
		Method: http.MethodGet,
		Path:   wire.PathIssuesCount,
		Query:  params,
	}, &body); err != nil {
		return issueops.CountResult{}, err
	}
	// Total is int64 end to end — apigen's member, the role's result and the
	// document's `format: int64` are one width. A workspace's row count is not
	// bounded by 2^53, and a lossy read would answer a number NEAR the
	// cardinality, which on a count is worse than an error because nothing
	// downstream can tell.
	return issueops.CountResult{Total: body.Total}, nil
}

// CountByGroup returns the same count bucketed by one dimension, plus the
// scalar total of the whole matching set.
func (c httpCounter) CountByGroup(ctx context.Context, req issueops.CountByGroupRequest) (issueops.CountByGroupResult, error) {
	if err := validateCountGroup(req.GroupBy); err != nil {
		return issueops.CountByGroupResult{}, err
	}
	if err := c.refuseUnservedScope(ctx, "Counter.CountByGroup", req.Filter); err != nil {
		return issueops.CountByGroupResult{}, err
	}
	params, err := encode.CountByGroupParams(req)
	if err != nil {
		return issueops.CountByGroupResult{}, c.store.inexpressible("Counter.CountByGroup", err)
	}
	var body apigen.IssueCount
	if err := c.store.dispatch(ctx, wire.Request{
		Op:     wire.OpCountIssues,
		Method: http.MethodGet,
		Path:   wire.PathIssuesCount,
		Query:  params,
	}, &body); err != nil {
		return issueops.CountByGroupResult{}, err
	}
	groups, err := decodeCountGroups(body.Groups)
	if err != nil {
		return issueops.CountByGroupResult{}, err
	}
	// Total is NOT the sum of the buckets and is not recomputed from them here:
	// label buckets overlap, so an issue carrying three labels is one row in the
	// total and one row in three buckets. The server answers both numbers and
	// this reads both.
	return issueops.CountByGroupResult{Groups: groups, Total: body.Total}, nil
}

// refuseUnservedScope is the pre-dial half of S8's client-skew note
// (internal/httpapi/routes.go, beside CapIssuesCountScope): a request that
// sets ParentID, NoParent, ExcludeTypes or ExcludeStatus asks for something
// only a server advertising issues.count.scope answers, and an older server
// predating the token answers all four with a guaranteed 400
// invalid_argument/unknown_parameter.
//
// It is called BEFORE encode.CountParams/CountByGroupParams, so a server that
// cannot serve the scope never sees the request at all — never a wasted round
// trip for a guaranteed refusal, and never a silent drop of the fields that
// would answer a wider count than the caller asked for. The dispatch path's
// own Preflight (dispatch.go) only gates the OPERATION as a whole via its
// coarse per-op token (issues.count); it has no visibility into which fields
// a particular request populates, which is why this finer-grained behavior
// check has to live here, explicitly, rather than ride the generic path.
//
// A request with none of the four fields set never dials the handshake for
// this check at all — snapshot() is only consulted when there is something to
// gate, so a plain, scope-free count against an old server pays nothing extra.
func (c httpCounter) refuseUnservedScope(ctx context.Context, op string, req issueops.CountRequest) error {
	if req.ParentID == "" && !req.NoParent && len(req.ExcludeTypes) == 0 && len(req.ExcludeStatus) == 0 {
		return nil
	}
	snap, err := c.store.snapshot(ctx)
	if err != nil {
		return err
	}
	// snap is nil, nil whenever Store.snapshot has no transport AND no cached
	// handshake (a Store built with a nil wire): nothing was ever advertised,
	// so this falls straight through to the refusal below rather than
	// dereferencing a nil *apigen.ContextResponse.
	if snap != nil && slices.Contains(snap.Capabilities, wire.CapCountScope) {
		return nil
	}
	return c.store.unsupportedCapability(op, wire.CapCountScope)
}

// countGroups is the closed bucketing vocabulary, spelled with the ROLE's
// constants rather than as strings.
//
// It is the client-side twin of internal/httpapi's list of the same name, and
// it is redeclared rather than imported for list_walk.go's reason:
// internal/workapi — where ValidateCountGroup lives — is denied to this package
// by depguard. TestTheCountGroupVocabularyMatchesTheSharedOne pins it against
// that validator from a test file, where the rule does not apply.
var countGroups = []issueops.CountGroup{
	issueops.CountGroupStatus,
	issueops.CountGroupPriority,
	issueops.CountGroupType,
	issueops.CountGroupAssignee,
	issueops.CountGroupLabel,
}

// validateCountGroup answers the role's own dimension refusal before the dial.
//
// An empty or unknown GroupBy is ErrValidation on THIS ROLE at every backend —
// counter.go says so, and a caller that misspelled a dimension and got zero
// buckets back has no way to tell that from a workspace with nothing in it. The
// operation refuses it too, naming the parameter, and that row is not redundant:
// what a server-side refusal cannot do is tell a caller their REQUEST is wrong
// rather than their server, which is validateCountReadyPage's argument exactly.
//
// The EMPTY dimension is refused with the rest and is the sharper half. The
// encoder would omit an empty `group_by`, the server would answer the scalar
// shape with no `groups` member, and a caller that asked for buckets would read
// "no buckets" — a wrong answer wearing the shape of a right one.
func validateCountGroup(group issueops.CountGroup) error {
	if slices.Contains(countGroups, group) {
		return nil
	}
	if group == "" {
		return fmt.Errorf("%w: a grouped count needs a dimension (one of %v): a caller that wanted a number calls Count",
			issueops.ErrValidation, countGroups)
	}
	return fmt.Errorf("%w: %q is not a count dimension (one of %v)", issueops.ErrValidation, group, countGroups)
}

// decodeCountGroups reads the buckets off a grouped answer.
//
// PRESENCE IS THE CONTRACT and it is checked rather than trusted. `groups` is
// published as present exactly when the request carried `group_by`, and this
// request always does — so an absent or null member is a server that did not
// bucket. Reading either as an empty map would answer "nothing matched" to a
// question that may have matched thousands, which is the one failure mode a
// bucketed count has that a scalar one does not: every bucket that should have
// been there is simply gone, and the total beside it still looks right.
//
// The map is COPIED rather than handed back, so the answer cannot alias the
// decoded response body, and it is non-nil for the same reason the role promises
// non-nil: a caller ranges over it without checking.
func decodeCountGroups(groups *map[string]int) (map[string]int, error) {
	if groups == nil || *groups == nil {
		return nil, fmt.Errorf("bd serve answered a grouped count with no `groups` member: " +
			"the operation publishes it whenever the request carries group_by, and an absent one cannot be told from a count with no buckets")
	}
	out := make(map[string]int, len(*groups))
	for key, count := range *groups {
		out[key] = count
	}
	return out, nil
}
