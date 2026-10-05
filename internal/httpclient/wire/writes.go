// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/writes.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"context"
	"net/http"
	"net/url"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// defaultApplyBatchItemCap is the item cap this client enforces against a
// server that does NOT advertise CapBatchApplyLarge — the original,
// always-supported shape every v0 server answers regardless of build. It is
// deliberately not the only cap this client ever applies: see ApplyBatch,
// which reads the handshake snapshot on every call and raises the ceiling to
// issueops.MaxApplyBatchItems the moment the server says it can take it. A
// compiled constant used UNCONDITIONALLY is exactly what task #3 forbids —
// this one is the floor a server might not have raised, not the client's own
// opinion of where the line should be.
const defaultApplyBatchItemCap = 100

// The v0 write operations, one method per operationId.
//
// Each one preflights ITSELF. Do deliberately does not — a transport test must
// be able to drive one request without standing up a handshake — but the
// capability check is not a policy the CALLER should be able to forget: an
// unrouted path on an older server answers a bare 404 that reads as "no such
// issue", and D6 requires the advertised list to be consulted before the dial.
// The layer that knows an operation's id is the layer that can never omit it,
// so it lives here rather than in each role's dispatch site.
//
// The request carries IssueID and DependsOnID where the operation has them.
// They are not sent: the problem mapper needs them to rebuild the typed
// conflicts whose extension members the server leaves out because the request
// already said them (see problem.go's target).

// ClaimIssue claims one issue. Unlike the other first-slice operations it is
// NOT exempt from the pre-flight (ga-b8ddd.11): a claim is a write, and the
// dispatch below forces the handshake so the project-identity gate runs before
// the claim can land. issues.claim is first-slice on every v0 server, so that
// gate costs one handshake round trip and never refuses a claim on capability.
func (c *Client) ClaimIssue(ctx context.Context, id string, body apigen.ClaimRequest) (*apigen.ClaimResponse, error) {
	path, err := IssueMethodPath(id, MethodClaim)
	if err != nil {
		return nil, err
	}
	var out apigen.ClaimResponse
	r := Request{Op: OpClaimIssue, Method: http.MethodPost, Path: path, Body: body, IssueID: id}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateIssue creates one issue, with its parent, its explicit edges and its
// waits-for gate, as one transaction.
//
// A PLAIN COLLECTION POST, and the response is the bare issue rather than an
// envelope: this operation creates one member of the collection its path names,
// so what comes back is the member — the row as STORED, carrying the id the
// server minted, the status it defaulted and the timestamps it persisted.
//
// It carries no IssueID, unlike the four operations that name one in their path.
// A create's id may not exist yet, and the problem mapper's target exists to
// rebuild conflicts the server left endpoints out of because the REQUEST said
// them — a create's `already_exists` is about an id the caller may never have
// spelled, so supplying one here would name a row this request did not choose.
// The role puts the caller's own id back where there was one.
func (c *Client) CreateIssue(ctx context.Context, body apigen.CreateIssueRequest) (*apigen.Issue, error) {
	var out apigen.Issue
	r := Request{Op: OpCreateIssue, Method: http.MethodPost, Path: PathIssues, Body: body}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReleaseIssue gives the claim on one issue back, and is the claim's inverse on
// the same custom-method dispatcher.
//
// It carries IssueID like the four operations that name one in their path, and
// on this one the reconstruction it feeds is not decorative: the ownership fence
// answers `already_claimed`, whose typed *issueops.ClaimConflictError is rebuilt
// from the request's id plus the members the server sends. Leaving the id off
// would name the empty string in a refusal about a specific row.
//
// The RESPONSE carries the post-release revision beside the issue, because
// types.Issue.RowVersion is `json:"-"` and the token cannot ride the row. The
// role stitches the two back together; see httpReleaser.Release.
func (c *Client) ReleaseIssue(ctx context.Context, id string, body apigen.ReleaseIssueRequest) (*apigen.ReleaseIssueResponse, error) {
	path, err := IssueMethodPath(id, MethodRelease)
	if err != nil {
		return nil, err
	}
	var out apigen.ReleaseIssueResponse
	r := Request{Op: OpReleaseIssue, Method: http.MethodPost, Path: path, Body: body, IssueID: id}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddComment appends one comment to the thread an issue owns.
//
// A PLAIN COLLECTION POST onto a SUB-RESOURCE, which is a shape no other method
// here has: the path names the collection (`/issues/{id}/comments`) rather than
// the resource, and what comes back is the member — the comment row as stored,
// with the id the insert minted and created_at at the column's own precision.
// That is the single create's posture, one level down.
//
// It carries IssueID for the ANCHOR rather than for a conflict: this operation
// raises none — a thread is append-only and the write touches no field of the
// issue — so the id travels only so a refusal names the row the request named.
func (c *Client) AddComment(ctx context.Context, id string, body apigen.AddCommentRequest) (*apigen.Comment, error) {
	path, err := IssueCommentsPath(id)
	if err != nil {
		return nil, err
	}
	var out apigen.Comment
	r := Request{Op: OpAddComment, Method: http.MethodPost, Path: path, Body: body, IssueID: id}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimNextIssue takes the next ready issue the filter admits, as ONE act.
//
// IT NAMES NO ID BECAUSE IT NAMES NO ROW, which is what makes it a
// collection-level custom method rather than a mode of issues/{id}:claim: the
// caller asks a question and the server's role picks the answer, inside the
// transaction that commits it.
//
// THE FILTER IS THE QUERY AND THE ACTOR IS THE BODY, which is the operation's
// split rather than this method's arrangement of it. The filter vocabulary is
// GET /v0/beads/ready's and goes through the same server-side decode, so a body
// object would be a second expression of one predicate; the actor is provenance
// that lands in a column, and this surface has always carried that in a body.
//
// It carries no IssueID for CreateIssue's reason, one step further along: there
// is no id the caller could have supplied, so a refusal has none to name. The
// operation raises no per-row conflict anyway — a row a racing agent already
// took is simply not in the set the claim scanned.
func (c *Client) ClaimNextIssue(ctx context.Context, params url.Values, body apigen.ClaimNextRequest) (*apigen.ClaimNextResponse, error) {
	var out apigen.ClaimNextResponse
	r := Request{Op: OpClaimNextIssue, Method: http.MethodPost, Path: PathIssuesClaimNext, Query: params, Body: body}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CloseIssue closes one issue.
func (c *Client) CloseIssue(ctx context.Context, id string, body apigen.CloseIssueRequest) (*apigen.CloseIssueResponse, error) {
	path, err := IssueMethodPath(id, MethodClose)
	if err != nil {
		return nil, err
	}
	var out apigen.CloseIssueResponse
	r := Request{Op: OpCloseIssue, Method: http.MethodPost, Path: path, Body: body, IssueID: id}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReopenIssue reopens one issue.
func (c *Client) ReopenIssue(ctx context.Context, id string, body apigen.ReopenIssueRequest) (*apigen.ReopenIssueResponse, error) {
	path, err := IssueMethodPath(id, MethodReopen)
	if err != nil {
		return nil, err
	}
	var out apigen.ReopenIssueResponse
	r := Request{Op: OpReopenIssue, Method: http.MethodPost, Path: path, Body: body, IssueID: id}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateGuards are updateIssue's three compare-and-set preconditions, as the
// caller hands them to the transport.
//
// EVERY MEMBER IS A POINTER AND NIL MEANS ABSENT. That is not a style choice on
// any of the three: `expected_version` 0 is a legal token — the migration-0054
// backfill left rows holding it — and `expected_assignee` "" is a legal guard,
// the one that says "only if nobody holds it". A zero value is a REQUEST on
// both, so the only encoding that can express "no guard" is the absent member,
// and `omitempty` on a pointer omits exactly the nil.
//
// It is a type of its own rather than three arguments because they travel
// together and mean one thing, and because a fourth parameter of the same
// primitive type beside `actor` is the shape a caller transposes.
type UpdateGuards struct {
	// ExpectedVersion is the row's `revision`, compared for equality alone. It
	// is a STRING on the wire — the token's decimal spelling, rendered by
	// types.RevisionToken from the int64 the role holds — because the token
	// spans the full 64-bit range and a JSON number would be rounded past 2^53
	// by any IEEE-754-double consumer. The server refuses a bare number on this
	// member with a 400, so the int64 never reaches the body.
	ExpectedVersion *string `json:"expected_version,omitempty"`
	// ExpectedStatus is the status the request guards on, spelled as the
	// workspace's own vocabulary.
	ExpectedStatus *string `json:"expected_status,omitempty"`
	// ExpectedAssignee is the assignee the request guards on. A pointer to the
	// empty string guards on UNASSIGNED and is a different request from nil.
	ExpectedAssignee *string `json:"expected_assignee,omitempty"`
}

// updateBody is updateIssue's request body as this client has to build it.
//
// It is deliberately NOT apigen.UpdateIssueRequest. That type spells the patch
// as pointer members with `omitempty`, which collapses two different requests
// into one document: on this operation a member's PRESENCE is the signal to
// write it and an explicit null on one of the four nullable members is the
// CLEAR, so an omitted member and a null member must be distinguishable — and a
// nil pointer under `omitempty` is neither. The patch therefore arrives already
// decided by its builder, and this layer marshals it verbatim.
//
// The GUARDS are embedded rather than spelled again, so their json tags — and
// the absent-is-not-zero rule those tags encode — have one definition.
//
// The RESPONSE stays apigen's: nothing about reading one is ambiguous.
type updateBody struct {
	Actor string         `json:"actor"`
	Patch map[string]any `json:"patch"`
	UpdateGuards
}

// UpdateIssue patches one issue. It is the only PATCH on this surface: the
// operation's pattern equals its spec path, so no custom-method suffix is
// involved and the id is an ordinary escaped segment.
//
// The guards ride BESIDE the patch, at the body's top level, which is where the
// server reads them: one smuggled into the patch document would be an unknown
// member of `patch` and a 400.
func (c *Client) UpdateIssue(ctx context.Context, id, actor string, patch map[string]any, guards UpdateGuards) (*apigen.UpdateIssueResponse, error) {
	path, err := IssuePath(id)
	if err != nil {
		return nil, err
	}
	var out apigen.UpdateIssueResponse
	body := updateBody{Actor: actor, Patch: patch, UpdateGuards: guards}
	r := Request{Op: OpUpdateIssue, Method: http.MethodPatch, Path: path, Body: body, IssueID: id}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CompareAndSetMetadata swaps one metadata key of one issue if and only if it
// currently holds the request's `expected`, and reports what it found either
// way.
//
// IT IS THE ONE WRITE HERE WHOSE REFUSAL IS A SUCCESS. A lost race answers 200
// with `swapped: false` and the value that refused the swap, so there is no 409
// for the problem mapper to classify and nothing about the verdict reaches this
// layer at all: the caller dispatches on the body. That is deliberate on the
// server's side — a conflict code would put the ordinary path of a retry loop
// into the error channel, and the value the loop needs next would have to
// travel in a problem extension member.
//
// It carries IssueID like the four operations that name one in their path: the
// resource is one row, and a conflict the problem mapper rebuilds for it should
// name the row the request named.
func (c *Client) CompareAndSetMetadata(ctx context.Context, id string, body apigen.CompareAndSetMetadataRequest) (*apigen.CompareAndSetMetadataResponse, error) {
	path, err := IssueMethodPath(id, MethodCASMetadata)
	if err != nil {
		return nil, err
	}
	var out apigen.CompareAndSetMetadataResponse
	r := Request{Op: OpCompareAndSetMetadata, Method: http.MethodPost, Path: path, Body: body, IssueID: id}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddDependencies asserts a set of edges as one transaction.
//
// issueID and dependsOnID name the SINGLE edge a one-edge request carries, and
// are empty for a multi-edge one. That is not a shortcut: the wire's
// dependency_exists refusal does not say which edge collided, so a batch's
// reconstructed conflict has to leave its endpoints empty — and inventing the
// first edge's ids there would name the wrong pair.
func (c *Client) AddDependencies(ctx context.Context, body apigen.AddDependenciesRequest) (*apigen.AddDependenciesResponse, error) {
	r := Request{Op: OpAddDependencies, Method: http.MethodPost, Path: PathDependenciesAdd, Body: body}
	if len(body.Edges) == 1 {
		r.IssueID = body.Edges[0].IssueId
		r.DependsOnID = body.Edges[0].DependsOnId
	}
	var out apigen.AddDependenciesResponse
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveDependency removes exactly one edge.
func (c *Client) RemoveDependency(ctx context.Context, body apigen.RemoveDependencyRequest) (*apigen.RemoveDependencyResponse, error) {
	r := Request{
		Op: OpRemoveDependency, Method: http.MethodPost, Path: PathDependenciesRemove, Body: body,
		IssueID: body.IssueId, DependsOnID: body.DependsOnId,
	}
	var out apigen.RemoveDependencyResponse
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SweepIssues clears the closed beads of one tier.
//
// It carries no IssueID, unlike the operations above: a sweep names no
// resource — it describes a SET and the server resolves it — so there is
// nothing for the problem mapper to rebuild a per-issue conflict from.
func (c *Client) SweepIssues(ctx context.Context, body apigen.SweepRequest) (*apigen.SweepResult, error) {
	r := Request{Op: OpSweepIssues, Method: http.MethodPost, Path: PathIssuesSweep, Body: body}
	var out apigen.SweepResult
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteIssues erases the named beads.
//
// The ids travel in the BODY rather than the path, so no id is escaped here:
// this is a collection-level custom method, and one request deletes many rows
// as one transaction.
func (c *Client) DeleteIssues(ctx context.Context, body apigen.DeleteIssuesRequest) (*apigen.DeleteIssuesResult, error) {
	r := Request{Op: OpDeleteIssues, Method: http.MethodPost, Path: PathIssuesDelete, Body: body}
	var out apigen.DeleteIssuesResult
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BatchCreateIssues creates every issue in the body, or none of them.
func (c *Client) BatchCreateIssues(ctx context.Context, body apigen.BatchCreateRequest) (*apigen.BatchCreateResponse, error) {
	r := Request{Op: OpBatchCreateIssues, Method: http.MethodPost, Path: PathIssuesBatchCreate, Body: body}
	var out apigen.BatchCreateResponse
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BatchCloseIssues closes a set of issues the request names as ONE act, and is
// the write side of `bd close a b c`. It is NOT all-or-nothing: a refused id is
// a per-item outcome inside a 200, so the response carries one entry per item
// and the survivors still commit.
//
// The response's TOP level goes through the existing problem mapper like every
// other operation: a 400 (bad body), a 503 (busy) or a 5xx is the method's own
// typed error. The per-item outcomes inside a 200 are the CALLER's to walk —
// each BatchCloseItemError carries its own code — because the close vocabulary a
// per-item refusal answers with is the role's, not the transport's.
func (c *Client) BatchCloseIssues(ctx context.Context, body apigen.BatchCloseRequest) (*apigen.BatchCloseResponse, error) {
	var out apigen.BatchCloseResponse
	r := Request{Op: OpBatchCloseIssues, Method: http.MethodPost, Path: PathIssuesBatchClose, Body: body}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ApplyUpdateItem is one `update` item of a plan, as this client has to build
// it.
//
// It is deliberately NOT apigen.ApplyUpdateItem, for updateBody's reason and
// only that reason: the patch is a DOCUMENT rather than a struct, because on
// this member — as on PATCH /v0/beads/issues/{id} — presence is the signal to
// write a field and an explicit null on one of the four nullable members is the
// CLEAR, and a struct of pointers with `omitempty` collapses those two states
// into one. The builder is encodeApplyPatch; this layer marshals what it
// decided.
//
// Every other member is spelled exactly as apigen.ApplyUpdateItem spells it,
// and the write-ledger gate classifies this shape against THAT type so a member
// published upstream and missing here cannot go unnoticed.
type ApplyUpdateItem struct {
	Target apigen.Ref     `json:"target"`
	Patch  map[string]any `json:"patch"`
	// The guard trio, per item and pointers for UpdateGuards' reason: "0" is a
	// legal row version and "" is the guard that says "only if nobody holds
	// it", so absent has to stay absent on all three. The version is the
	// token's decimal string, spelled by types.RevisionToken, for the reason
	// UpdateGuards.ExpectedVersion gives.
	ExpectedVersion       *string `json:"expected_version,omitempty"`
	ExpectedStatus        *string `json:"expected_status,omitempty"`
	ExpectedAssignee      *string `json:"expected_assignee,omitempty"`
	ForceClosePolicy      *bool   `json:"force_close_policy,omitempty"`
	ForceAssigneeTransfer *bool   `json:"force_assignee_transfer,omitempty"`
}

// ApplyItem is one item of a plan: a `kind` and exactly one payload naming it.
//
// THE TAG IS ENFORCED BY THE CALLER and cannot be enforced here, which is the
// document's own doctrine rather than an omission on this side: the schema
// carries four optional payload members and a required tag rather than a schema
// alternation, so nothing in any generated type stops a request carrying two
// payloads or a payload the kind does not name. The role builds these, and it
// refuses all three disagreements before the dial.
type ApplyItem struct {
	Kind   string                  `json:"kind"`
	Create *apigen.ApplyCreateItem `json:"create,omitempty"`
	Update *ApplyUpdateItem        `json:"update,omitempty"`
	Close  *apigen.ApplyCloseItem  `json:"close,omitempty"`
	DepAdd *apigen.ApplyDepAddItem `json:"dep_add,omitempty"`
}

// ApplyBatchRequest is issues:batchApply's body, carrying the item type above.
type ApplyBatchRequest struct {
	Actor                 string      `json:"actor"`
	Items                 []ApplyItem `json:"items"`
	Provenance            *string     `json:"provenance,omitempty"`
	ForceIDPrefix         *bool       `json:"force_id_prefix,omitempty"`
	SkipPerEdgeCycleCheck *bool       `json:"skip_per_edge_cycle_check,omitempty"`
}

// ApplyBatch applies an ordered, heterogeneous plan as ONE transaction, or
// applies none of it.
//
// IT IS THE WIDEST WRITE ON THIS SURFACE and the only one that is all-or-nothing
// across several verbs. That shapes what this layer can carry: there are no
// per-item outcomes to walk on a refusal — an item that refused took the whole
// request down, and every other item's outcome would be a statement about a
// transaction that rolled back — so the offender travels in the problem
// document's `item_*` members instead, which the mapper decodes and the role
// rebuilds into an *issueops.ItemError.
//
// It carries no IssueID and no DependsOnID, for the batch add's reason and then
// some: the request names as many rows as it has items, so a conflict the
// problem mapper rebuilt from ONE of them would name an arbitrary row. The role
// puts the offender back from `item_issue_id`, which the server reads inside the
// refusing transaction.
func (c *Client) ApplyBatch(ctx context.Context, body ApplyBatchRequest) (*apigen.ApplyBatchResponse, error) {
	// The batch cap, read off the handshake snapshot rather than a bare
	// compiled constant (task #3): a server that has not raised its own
	// ceiling only ever promised the original 100-item shape, and sending it
	// more would earn a 400 this client can refuse before paying the round
	// trip for. Handshake is cached after the first call, so this costs no
	// extra dial on the common path — ApplyBatch is never a baseline
	// operation, so Preflight below would force the same fetch anyway.
	snap, err := c.Handshake(ctx)
	if err != nil {
		return nil, err
	}
	limit := defaultApplyBatchItemCap
	if snap.Has(CapBatchApplyLarge) {
		limit = issueops.MaxApplyBatchItems
	}
	if n := len(body.Items); n > limit {
		if !snap.Has(CapBatchApplyLarge) && n <= issueops.MaxApplyBatchItems {
			// Raising the ceiling would admit this plan: the exact "a
			// capability it does not advertise" shape the skew matrix already
			// covers (D7 case 2), caught here before the dial instead of read
			// back off the 400 an unaware server would otherwise answer with
			// its own un-raised cap.
			return nil, NewCapabilityError(OpApplyBatch, CapBatchApplyLarge, c.base.Redacted(), &snap.Context)
		}
		// Over the absolute ceiling even with the capability present: no
		// token any server could advertise raises this further, so it is a
		// validation refusal rather than a skew one — see BatchTooLargeError.
		return nil, &BatchTooLargeError{Op: OpApplyBatch, ServerURL: c.base.Redacted(), Count: n, Limit: issueops.MaxApplyBatchItems}
	}

	var out apigen.ApplyBatchResponse
	r := Request{Op: OpApplyBatch, Method: http.MethodPost, Path: PathIssuesBatchApply, Body: body}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RememberMemory stores one memory. An absent key asks the server to derive
// one, which is why the body's Key is a pointer all the way down: an empty
// string and an omitted member mean different things here.
func (c *Client) RememberMemory(ctx context.Context, body apigen.RememberRequest) (*apigen.RememberedMemory, error) {
	r := Request{Op: OpRememberMemory, Method: http.MethodPost, Path: PathMemories, Body: body}
	var out apigen.RememberedMemory
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RecallMemory reads one memory. A miss — including a row stored as the empty
// string — is a 404, which the problem mapper turns into issueops.ErrNotFound;
// the memory role converts that to its own Found-false result, because
// memoryops has deliberately no ErrNotFound.
func (c *Client) RecallMemory(ctx context.Context, key string) (*apigen.Memory, error) {
	path, err := MemoryPath(key)
	if err != nil {
		return nil, err
	}
	var out apigen.Memory
	r := Request{Op: OpGetMemory, Method: http.MethodGet, Path: path}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ForgetMemory removes one memory and answers with the value it removed, read
// in the deletion's own transaction.
func (c *Client) ForgetMemory(ctx context.Context, key string) (*apigen.Memory, error) {
	path, err := MemoryPath(key)
	if err != nil {
		return nil, err
	}
	var out apigen.Memory
	r := Request{Op: OpForgetMemory, Method: http.MethodDelete, Path: path}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMemories enumerates the memory plane. The narrowing parameter is spelled
// `search` — not `q`, which is queryIssues' spelling.
func (c *Client) ListMemories(ctx context.Context, search string) (*apigen.MemoriesPage, error) {
	var query url.Values
	if search != "" {
		query = url.Values{"search": []string{search}}
	}
	var out apigen.MemoriesPage
	r := Request{Op: OpListMemories, Method: http.MethodGet, Path: PathMemories, Query: query}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListReadyWork reads one page of the ready front.
//
// It lives beside the writes because the composed ReadyClaimer is what needed it
// first, and that is now a fact about ONE LEG rather than about the wire:
// ClaimNextIssue above dials the operation where the server advertises it, and
// the composition — this read followed by claimIssue — survives only as the
// down-level leg for a server older than #5510. The reader role wants the same
// operation, and this method is the one it uses: there must never be a second
// spelling of one operation on this client, which is why a read sits in a file
// of writes rather than being copied into one.
func (c *Client) ListReadyWork(ctx context.Context, params url.Values) (*apigen.ReadyPage, error) {
	var out apigen.ReadyPage
	r := Request{Op: OpListReadyWork, Method: http.MethodGet, Path: PathReady, Query: params}
	if err := c.dispatch(ctx, r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// dispatch is preflight-then-Do, the pairing every operation method above owes
// and none of them may skip.
func (c *Client) dispatch(ctx context.Context, r Request, out any) error {
	if err := c.Preflight(ctx, r.Op); err != nil {
		return err
	}
	return c.Do(ctx, r, out)
}
