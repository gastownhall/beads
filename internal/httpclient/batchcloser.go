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
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

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
func (b *httpBatchCloser) CloseBatch(ctx context.Context, req issueops.CloseBatchRequest) (issueops.CloseBatchResult, error) {
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
	// The ClaimNext filter is validated client-side, because the wire's claim_next
	// carries neither a page bound nor an order, so the server never sees the
	// members a bad one would carry. The role contract calls these ErrValidation.
	if req.ClaimNext != nil {
		if err := validateClaimNext(req.Actor, *req.ClaimNext); err != nil {
			return issueops.CloseBatchResult{}, err
		}
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

// validateClaimNext applies the ClaimNext rules the wire cannot enforce, because
// its claim_next object carries neither a page bound nor an order: the shared
// unset-Limit / unset-Offset rule (issueops ValidateClaimNextRequest) plus the
// sort vocabulary listReadyWork would have refused on the URL query but claim_next
// never sends. All three are the role contract's ErrValidation, raised before any
// dial so a refused request closes nothing.
//
// MolType is NOT here: it is a legal ready filter the wire simply cannot express,
// so it refuses through the shared ready builder (encode.ClaimNextBody, ledger
// row E-ReadyRequest.MolType) rather than as a validation failure.
func validateClaimNext(actor string, req issueops.ReadyRequest) error {
	// The shared rules first, from the validator every ReadyClaimer runs, so a
	// rule upstream adds to a claim reaches this arm too — the fork's claim_next
	// is the SAME claim, taken in the transaction that committed the closes.
	if err := storageops.ValidateClaimNextRequest(issueops.ClaimNextRequest{Actor: actor, Filter: req}); err != nil {
		return err
	}
	if req.Sort != "" && !types.SortPolicy(req.Sort).IsValid() {
		return invalid("claim next sort policy %q is not one of hybrid, priority or oldest", req.Sort)
	}
	return nil
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
	body, err := batchCloseBody(req)
	if err != nil {
		return issueops.CloseBatchResult{}, err
	}
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
// item, session and force are request-wide, and a nil ClaimNext stays absent —
// the shared ready builder refuses a claim the wire cannot express before this
// body is ever sent.
func batchCloseBody(req issueops.CloseBatchRequest) (apigen.BatchCloseRequest, error) {
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
	if req.ClaimNext != nil {
		claim, err := encode.ClaimNextBody(*req.ClaimNext)
		if err != nil {
			return apigen.BatchCloseRequest{}, err
		}
		body.ClaimNext = &claim
	}
	return body, nil
}

// decodeBatchCloseResult reads the wire response back into the role's result.
//
// The server's contract is one outcome per item in request order; a length that
// disagrees is a broken server, which is the METHOD's failure and carries no
// outcomes rather than a per-item one the caller might trust. The claim is held
// to its own half of that contract by checkServedClaim, on the same terms.
func decodeBatchCloseResult(req issueops.CloseBatchRequest, resp *apigen.BatchCloseResponse) (issueops.CloseBatchResult, error) {
	if len(resp.Outcomes) != len(req.Items) {
		return issueops.CloseBatchResult{}, fmt.Errorf(
			"bd serve returned %d batch-close outcomes for %d items", len(resp.Outcomes), len(req.Items))
	}
	outcomes := make([]issueops.CloseOutcome, len(resp.Outcomes))
	for i := range resp.Outcomes {
		outcomes[i] = decodeBatchCloseOutcome(resp.Outcomes[i])
	}
	if err := checkServedClaim(req, resp.ClaimedNext, outcomes); err != nil {
		return issueops.CloseBatchResult{}, err
	}
	// ClaimedNext IS the generated pointer type (the schema is x-go-type-pinned to
	// types.IssueWithCounts), so it passes through unrewritten: nil means no claim
	// was asked for, nothing closed, or nothing was eligible.
	return issueops.CloseBatchResult{Outcomes: outcomes, ClaimedNext: resp.ClaimedNext}, nil
}

// checkServedClaim holds a served `claimed_next` to the role contract, and
// answers the outcome count's diagnosis when it does not: a broken server, the
// METHOD's failure, carrying no outcomes rather than an answer the caller might
// trust.
//
// A CLAIM CARRYING NO ROW is the count's failure one member over, and it is
// here for a harder reason than the count is. types.IssueWithCounts holds the
// row as an EMBEDDED POINTER, so a `claimed_next` object that carries the
// cardinalities and none of the issue's own members decodes to a non-nil claim
// whose Issue is nil — a shape nothing on the wire tells apart from a claim
// that landed. Passing it through is not a wrong answer but a PANIC in the
// caller: this store is a registered storage.DoltStorage, so `bd close
// --claim-next` takes the direct arm and dereferences the claimed row's ID with
// no nil check.
//
// A CLAIM THE REQUEST NEVER EARNED is refused for a different harm, and the
// rule is the role contract's own: a claim requires that the request ASKED for
// one and that at least one item LANDED, Changed being the test for landed. It
// is the SAME rule `bd serve` holds its own closer to (internal/httpapi's
// claimCheckedBatchCloser), stated twice on purpose — the server's copy
// protects that server's clients, and this one protects THIS client from a
// server that is not it. They must not drift: a rule stricter here than there
// would refuse a legitimate answer, which is what the served conformance tier's
// earned/unearned pair pins.
//
// WHAT IT PREVENTS IS A WRITE AND A SUBPROCESS, not a bad printout. The row
// `claimed_next` names is assigned to this actor, and the client's decorator
// chain puts HookFiringStore above this store: hookBatchCloser fires the
// workspace's on_update hook on the member's PRESENCE alone, re-deriving
// nothing, because below the wire it has no view of what was asked or what
// landed. Refusing here is what denies it both preconditions.
//
// THE CLAIMED ID IS CHECKED AGAINST NOTHING, and that absence is the
// operation's shape rather than an omission. The outcomes are checked against
// the request because they are POSITIONAL — they answer the ids the caller sent
// — and the claim answers no id at all: it names the next READY row, which the
// caller never enumerated and which this client cannot evaluate readiness for,
// since running the selection inside the closes' transaction is the whole
// reason the member exists. The one membership rule that LOOKS checkable — "the
// claim is none of the ids I sent" — is false, and a batch of a parent and one
// of its children is the counterexample: the child closes, the parent refuses
// for its remaining open children, and that parent is open, unblocked and
// eligible, so claiming it is correct. Refusing it would reject a right answer,
// which is worse than these checks declining to make a promise they cannot
// keep.
func checkServedClaim(req issueops.CloseBatchRequest, claimed *types.IssueWithCounts, outcomes []issueops.CloseOutcome) error {
	if claimed == nil {
		return nil
	}
	// The row FIRST, because the two refusals below NAME it.
	if claimed.Issue == nil {
		return fmt.Errorf("bd serve returned a batch-close claim carrying no issue")
	}
	if req.ClaimNext == nil {
		return fmt.Errorf("bd serve returned a batch-close claim of %q for a request that asked for none", claimed.ID)
	}
	if !closeBatchLanded(outcomes) {
		return fmt.Errorf("bd serve returned a batch-close claim of %q for a batch that closed nothing", claimed.ID)
	}
	return nil
}

// closeBatchLanded reports whether any item persisted a mutation, which is what
// the claim's "at least one item closed" means: an idempotent re-close is a
// per-item success that wrote nothing, and a refused item wrote nothing either.
//
// It is the server-side wrapper's function of the same name, restated rather
// than shared for the reason maxWireBatchCloseItems is restated: importing the
// server package to reach it would drag the storage engine into a client.
func closeBatchLanded(outcomes []issueops.CloseOutcome) bool {
	for _, outcome := range outcomes {
		if outcome.Err == nil && outcome.Changed {
			return true
		}
	}
	return false
}

// decodeBatchCloseOutcome reads ONE wire outcome.
//
// `code` IS THE DISCRIMINATOR, which is the wire's own rule: present means the
// item refused and nothing was written for it; absent means it succeeded and
// the snapshot, `already_closed` and `open_children` are all present. The
// members are FLAT on the outcome rather than nested under an error object, so
// `open_children` is read from the same field in both branches and means two
// different things depending on which one — see the schema.
func decodeBatchCloseOutcome(item apigen.CloseOutcome) issueops.CloseOutcome {
	out := issueops.CloseOutcome{IssueID: item.IssueId}
	if item.Code != nil {
		out.Err = perItemCloseError(item.IssueId, item)
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
//	an unknown code              -> *wire.UnknownItemCodeError (the generic
//	                                typed per-item error, so version skew lands
//	                                as a typed refusal rather than a silent gap)
//
// The member-presence discriminator on not_closable is the same one the single
// close's 409 uses, and it comes from the typed field, never from parsing prose.
//
// It is only ever called with a refused outcome — Code non-nil — which the one
// caller above guarantees.
func perItemCloseError(issueID string, item apigen.CloseOutcome) error {
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

// The two per-item refusal codes this outcome can carry. They are the
// `Problem.code` spellings, restated here rather than imported because
// importing the server package would drag the storage engine into a client.
const (
	codeNotFound    = "not_found"
	codeNotClosable = "not_closable"
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
func (s *Store) refuseUnservedCloseShape(req issueops.CloseBatchRequest) error {
	if len(req.Items) > 1 {
		return s.unsupported("BatchCloser.CloseBatch(multi-item)")
	}
	if req.ClaimNext != nil {
		return s.unsupported("BatchCloser.CloseBatch(ClaimNext)")
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
	default:
		return issueops.CloseOutcome{}, false
	}
	return issueops.CloseOutcome{IssueID: issueID, Err: err}, true
}
