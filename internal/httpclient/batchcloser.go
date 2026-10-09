// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/batchcloser.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// readySortPolicies names the only three sort policies a ClaimNext filter
// accepts, mirroring internal/workapi.BuildReadyFilter's vocabulary. It is
// redeclared here rather than imported for the reason list_walk.go, order.go
// and releaser.go each give: depguard denies internal/workapi to this package.
//
// A value outside this list is a deterministic request-validation failure —
// RunBatchCloserClaimFilterValueFailureIsARequestValidationFailure — and that
// holds REGARDLESS of whether the v0 wire can carry a ClaimNext at all: a bad
// sort policy is caught before the capability question is even asked, the same
// way an empty actor or a blank item id is.
var readySortPolicies = []string{"hybrid", "oldest", "priority"}

// validateReadySortPolicy refuses a ClaimNext filter's sort policy if it names
// anything outside readySortPolicies. An empty policy is the caller leaving it
// to the default and is always legal here.
func validateReadySortPolicy(policy string) error {
	if policy == "" || slices.Contains(readySortPolicies, policy) {
		return nil
	}
	return invalid("invalid sort policy '%s'. Valid values: hybrid, priority, oldest", policy)
}

// maxWireBatchCloseItems is the wire's cap on one batch close: maxBatchCloseItems
// in internal/httpapi/batch_close.go, redeclared here because importing the
// server would drag the storage engine into a client. A batch over the bound
// REFUSES rather than chunking — see ledger row L-close-cap and CloseBatch below.
const maxWireBatchCloseItems = 100

// httpBatchCloser serves issueops.BatchCloser over the v0 wire.
//
// WHERE THE WIRE CARRIES issues.batchClose, the whole CloseBatchRequest is one
// wire call: many items, per-item outcomes, and the atomic ClaimNext included.
// That is the operation this role was always waiting for — `bd close a b c` is
// one transaction with at most one history entry, and only a single wire call
// preserves that where a loop of closeIssue could not.
//
// WHERE IT DOES NOT — a server too old to advertise the capability — the role
// falls back to the ONE shape a bare closeIssue can honestly compose: a single
// item with no ClaimNext, which is the shape `bd close <id>` issues for the
// common case. Every other shape refuses in that leg rather than looping, because
// N sequential closes are N transactions and N history entries where the contract
// promises one, and ClaimNext runs INSIDE the closes' transaction and can see an
// unblocking the batch itself produced. See refuseUnservedCloseShape.
//
// CLIENT-SIDE VALIDATION happens first on both legs: the role contract calls an
// empty actor, an empty batch or a blank item id invalid, so they must not
// consume a write slot to be told so. The item cap is enforced here too, and it
// refuses — never chunks — for the reason L16's dependency-add cap does.
type httpBatchCloser struct {
	store *Store
	wire  WriteWire
}

var _ issueops.BatchCloser = (*httpBatchCloser)(nil)

// CloseBatch closes the batch, on the wire's batchClose operation where the
// server advertises it and by composing a single closeIssue where it does not.
func (b *httpBatchCloser) CloseBatch(ctx context.Context, req issueops.CloseBatchRequest) (result issueops.CloseBatchResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError into
	// *InexpressibleError so errors.As(err, &unsupported) reaches
	// *storage.ErrUnsupported, same as inexpressible does for a read role.
	defer func() { err = b.store.inexpressible("BatchCloser.CloseBatch", err) }()
	// Request validation first, and all of it client-side. A non-nil error here
	// carries no outcomes, which the method's contract requires.
	if err := requireActor(req.Actor); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if len(req.Items) == 0 {
		return issueops.CloseBatchResult{}, invalid("a batch close names no items")
	}
	for i, item := range req.Items {
		if item.IssueID == "" {
			return issueops.CloseBatchResult{}, invalid("items[%d].issue_id is required", i)
		}
	}
	// ClaimNext's OWN request rules run before the capability question is asked,
	// the same ordering ReadyClaimer.ClaimNext uses: a filter that is invalid on
	// every backend (a limit, an offset, a brief projection, or an unknown sort
	// policy) is ErrValidation here, not a refusal of a capability the wire
	// might have had. RunBatchCloserClaimFilterValueFailureIsARequestValidationFailure
	// pins this — the item is real and closeable, so a backend that ran the
	// batch before discovering the bad filter would still have to undo it.
	if req.ClaimNext != nil {
		if err := storageops.ValidateClaimNextRequest(issueops.ClaimNextRequest{Actor: req.Actor, Filter: *req.ClaimNext}); err != nil {
			return issueops.CloseBatchResult{}, err
		}
		if err := validateReadySortPolicy(req.ClaimNext.Sort); err != nil {
			return issueops.CloseBatchResult{}, err
		}
		// Past its own validation, ClaimNext still refuses unconditionally: OSS's
		// apigen.BatchCloseRequest and BatchCloseResponse publish no
		// claim_next/claimed_next member at all, so there is no shape — valid or
		// not — for this field to encode into. See ledger row
		// W-CloseBatchRequest.ClaimNext.
		return issueops.CloseBatchResult{}, refuse(encode.OpBatchCloseIssues, "W-CloseBatchRequest.ClaimNext")
	}
	// The wire's item cap, enforced before the dial and NEVER by chunking: a
	// split batch is N transactions where the caller asked for one. L-close-cap.
	if len(req.Items) > maxWireBatchCloseItems {
		return issueops.CloseBatchResult{}, refuse(encode.OpBatchCloseIssues, "L-close-cap")
	}

	served, err := b.servesBatchClose(ctx)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	if served {
		return b.serveBatch(ctx, req)
	}
	return b.serveComposedSingle(ctx, req)
}

// servesBatchClose reports whether the server advertises issues.batchClose. It
// forces the one lazy handshake the store already owns and reads the cached
// capability list, so the check costs at most one round trip and none once the
// handshake has run.
//
// IT RETURNS THE HANDSHAKE'S ERROR, and servesListSort (list_walk.go) — the
// same probe against the same snapshot — deliberately swallows its own and
// reports false. The asymmetry is the design, and neither should be "fixed"
// into the other: this is a WRITE whose only legs both dial, so a handshake
// that cannot be obtained means the close cannot be attempted at all and
// hiding that would turn a transport failure into a silent down-level route.
// listIssues is a baseline READ whose fallback leg needs no handshake and
// worked before the capability existed, so propagating there would newly fail
// a `bd list` that succeeds today.
func (b *httpBatchCloser) servesBatchClose(ctx context.Context) (bool, error) {
	snap, err := b.store.snapshot(ctx)
	if err != nil {
		return false, err
	}
	if snap == nil {
		return false, nil
	}
	token, _ := wire.CapabilityFor(wire.OpBatchCloseIssues)
	return slices.Contains(snap.Capabilities, token), nil
}

// serveBatch sends the whole request on one issues:batchClose call and reads the
// per-item outcomes back.
func (b *httpBatchCloser) serveBatch(ctx context.Context, req issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	body := batchCloseBody(req)
	resp, err := b.wire.BatchCloseIssues(ctx, body)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
	return decodeBatchCloseResult(req, resp)
}

// serveComposedSingle is the down-level fallback: the single-item, no-ClaimNext
// shape composed onto closeIssue, with every other shape refused citing the
// missing capability. It is TODAY's serve, reached only when issues.batchClose
// is absent.
func (b *httpBatchCloser) serveComposedSingle(ctx context.Context, req issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
	if err := b.store.refuseUnservedCloseShape(req); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	item := req.Items[0]
	res, err := b.wire.CloseIssue(ctx, item.IssueID, closeBody(req.Actor, item.Reason, req.Session, req.Force))
	if err != nil {
		if outcome, ok := perItemCloseRefusal(item.IssueID, err); ok {
			// A per-item refusal is a RESULT and never the method's error. The
			// batch of one has nothing left to commit, but the caller still reads
			// its answer out of Outcomes like any other batch.
			return issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{outcome}}, nil
		}
		return issueops.CloseBatchResult{}, err
	}

	issue := res.Issue
	return issueops.CloseBatchResult{Outcomes: []issueops.CloseOutcome{{
		IssueID:      item.IssueID,
		Issue:        &issue,
		Changed:      !res.AlreadyClosed,
		OpenChildren: res.OpenChildren,
	}}}, nil
}

// batchCloseBody projects the role request onto the wire body. Reasons ride per
// item, and session and force are request-wide. ClaimNext has no member to
// project onto — CloseBatch refuses it unconditionally before this body is
// ever built (W-CloseBatchRequest.ClaimNext), so req.ClaimNext is always nil
// here.
func batchCloseBody(req issueops.CloseBatchRequest) apigen.BatchCloseRequest {
	items := make([]apigen.BatchCloseItem, len(req.Items))
	for i, item := range req.Items {
		items[i] = apigen.BatchCloseItem{Id: item.IssueID}
		if item.Reason != "" {
			reason := item.Reason
			items[i].Reason = &reason
		}
	}
	body := apigen.BatchCloseRequest{Actor: req.Actor, Items: items}
	if req.Session != "" {
		session := req.Session
		body.Session = &session
	}
	if req.Force {
		force := true
		body.Force = &force
	}
	return body
}

// decodeBatchCloseResult reads the wire response back into the role's result.
//
// The server's contract is one outcome per item in request order; a length that
// disagrees is a broken server, which is the METHOD's failure and carries no
// outcomes rather than a per-item one the caller might trust.
//
// The result's ClaimedNext always stays nil: OSS's apigen.BatchCloseResponse
// publishes no claimed_next member for this client to decode, and CloseBatch
// refuses any request that asked for one before a body is ever sent (see
// ledger row W-CloseBatchRequest.ClaimNext), so there is nothing a server
// could answer here that this client would read.
func decodeBatchCloseResult(req issueops.CloseBatchRequest, resp *apigen.BatchCloseResponse) (issueops.CloseBatchResult, error) {
	if len(resp.Outcomes) != len(req.Items) {
		return issueops.CloseBatchResult{}, fmt.Errorf(
			"bd serve returned %d batch-close outcomes for %d items", len(resp.Outcomes), len(req.Items))
	}
	outcomes := make([]issueops.CloseOutcome, len(resp.Outcomes))
	for i := range resp.Outcomes {
		outcomes[i] = decodeBatchCloseOutcome(req.Actor, resp.Outcomes[i])
	}
	return issueops.CloseBatchResult{Outcomes: outcomes}, nil
}

// decodeBatchCloseOutcome reads ONE wire outcome.
//
// `code` IS THE DISCRIMINATOR, which is the wire's own rule: present means the
// item refused and nothing was written for it; absent means it succeeded and
// the snapshot, `already_closed` and `open_children` are all present. The
// members are FLAT on the outcome rather than nested under an error object, so
// `open_children` is read from the same field in both branches and means two
// different things depending on which one — see the schema.
func decodeBatchCloseOutcome(actor string, item apigen.CloseOutcome) issueops.CloseOutcome {
	out := issueops.CloseOutcome{IssueID: item.IssueId}
	if item.Code != nil {
		out.Err = perItemCloseError(item.IssueId, actor, item)
		return out
	}
	// A successful outcome carries the snapshot; already_closed is the idempotent
	// re-close (Changed false), and open_children is what a forced close observed.
	out.Issue = item.Issue
	if item.OpenChildren != nil {
		out.OpenChildren = *item.OpenChildren
	}
	out.Changed = item.AlreadyClosed == nil || !*item.AlreadyClosed
	return out
}

// perItemCloseError is the shared code -> error table that turns a refused wire
// outcome into the canonical close vocabulary a local batch would have
// returned, so a caller classifies a remote per-item refusal with the same
// errors.Is/errors.As arms it already has:
//
//	not_found                    -> issueops.ErrNotFound
//	not_closable + open_children -> *issueops.CloseOpenChildrenError
//	not_closable                 -> issueops.ErrCloseBlocked
//	template_read_only           -> *issueops.TemplateReadOnlyError
//	issue_pinned                 -> *issueops.PinnedError
//	not_assignee + assignee      -> *issueops.CloseNotAssigneeError (the
//	                                closing actor is the request's own)
//	an unknown code              -> *wire.UnknownItemCodeError (the generic
//	                                typed per-item error, so version skew lands
//	                                as a typed refusal rather than a silent gap)
//
// The member-presence discriminator on not_closable is the same one the single
// close's 409 uses, and it comes from the typed field, never from parsing prose.
//
// It is only ever called with a refused outcome — Code non-nil — which the one
// caller above guarantees.
func perItemCloseError(issueID, actor string, item apigen.CloseOutcome) error {
	var code string
	if item.Code != nil {
		code = *item.Code
	}
	switch code {
	case codeNotFound:
		return issueops.ErrNotFound
	case codeNotClosable:
		if item.OpenChildren != nil {
			return &issueops.CloseOpenChildrenError{IssueID: issueID, OpenChildren: *item.OpenChildren}
		}
		return issueops.ErrCloseBlocked
	case codeTemplateReadOnly:
		return &issueops.TemplateReadOnlyError{IssueID: issueID}
	case codeIssuePinned:
		return &issueops.PinnedError{IssueID: issueID}
	case codeNotAssignee:
		var assignee string
		if item.Assignee != nil {
			assignee = stripItemControlRunes(*item.Assignee)
		}
		return &issueops.CloseNotAssigneeError{IssueID: issueID, Assignee: assignee, Actor: actor}
	default:
		var detail string
		if item.Detail != nil {
			detail = stripItemControlRunes(*item.Detail)
		}
		return &wire.UnknownItemCodeError{
			IssueID: issueID,
			Code:    stripItemControlRunes(code),
			Detail:  detail,
		}
	}
}

// The per-item refusal codes this outcome can carry. They are the
// `Problem.code` spellings, restated here rather than imported because
// importing the server package would drag the storage engine into a client.
const (
	codeNotFound         = "not_found"
	codeNotClosable      = "not_closable"
	codeTemplateReadOnly = "template_read_only"
	codeIssuePinned      = "issue_pinned"
	codeNotAssignee      = "not_assignee"
)

// stripItemControlRunes removes control runes from a server-controlled per-item
// string before it becomes a typed error, mirroring the wire problem mapper's
// own source-layer strip: the unknown-code carrier reaches the same per-id
// stderr sinks a decoded ProblemError does.
func stripItemControlRunes(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// refuseUnservedCloseShape names the shape that refused in the DOWN-LEVEL leg, so
// the refusal says which of the two reasons it is rather than "batch close is
// unsupported". It survives only in that leg: where issues.batchClose is
// advertised, every shape is served on one wire call and nothing reaches here.
//
// ClaimNext is not checked here: CloseBatch refuses it unconditionally, on
// both legs, before either serving method is called (see ledger row
// W-CloseBatchRequest.ClaimNext), so req.ClaimNext is always nil by the time
// a request reaches this leg.
func (s *Store) refuseUnservedCloseShape(req issueops.CloseBatchRequest) error {
	if len(req.Items) > 1 {
		return s.unsupported("BatchCloser.CloseBatch(multi-item)")
	}
	return nil
}

// perItemCloseRefusal separates the close vocabulary an ITEM answers with from
// the failures the METHOD answers with, for the composed single-item leg.
//
// The three it recognizes are exactly what Lifecycle.Close returns, and exactly
// what the wire's problem mapper reconstructs: not_found becomes ErrNotFound,
// not_closable carrying open_children becomes *CloseOpenChildrenError, and
// not_closable without it becomes ErrCloseBlocked. Everything else — a transport
// failure, a 503, an unknown 4xx — is the method's, because it says nothing about
// this item and would be a lie as a per-item outcome.
func perItemCloseRefusal(issueID string, err error) (issueops.CloseOutcome, bool) {
	var openChildren *issueops.CloseOpenChildrenError
	switch {
	case errors.As(err, &openChildren):
	case errors.Is(err, issueops.ErrNotFound):
	case errors.Is(err, issueops.ErrCloseBlocked):
	case errors.Is(err, issueops.ErrTemplateReadOnly):
	case errors.Is(err, issueops.ErrPinned):
	case errors.As(err, new(*issueops.CloseNotAssigneeError)):
	default:
		return issueops.CloseOutcome{}, false
	}
	return issueops.CloseOutcome{IssueID: issueID, Err: err}, true
}
