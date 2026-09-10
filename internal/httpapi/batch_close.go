package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

const (
	// maxBatchCloseItems is the document's cap on `items`. It is
	// maxBatchCreateItems for that constant's reason — it bounds how long one
	// request may hold a write transaction — and it is a constant of its own
	// rather than a reference to that one because the two operations are free
	// to move independently and a shared name would make one move both.
	maxBatchCloseItems = 100
	// maxBatchCloseBodyBytes bounds the request body. A hundred items of an id
	// and a bounded reason is a fraction of what a create carries, so the
	// create's budget is generous here rather than tight.
	maxBatchCloseBodyBytes = 4 << 20
)

// batchCloseRequestMembers and batchCloseItemMembers are the document's member
// lists at each level, refused BY NAME for the reason every other body on this
// surface is.
var (
	batchCloseRequestMembers = []string{claimActorMember, "items", "session", "force"}
	batchCloseItemMembers    = []string{"id", "reason"}
)

// handleBatchClose closes many issues and commits them together.
//
// IT IS THE ONE OPERATION ON THIS SURFACE WHOSE 200 CARRIES REFUSALS, and that
// shape is the ROLE's rather than this handler's arrangement. An id the batch
// refuses is skipped and the survivors commit, because an agent that finishes
// four of five steps and mistypes the fifth should keep the four — so a refusal
// is a RESULT of the batch, not an error of it.
//
// The division that falls out of that is the contract a client codes against: a
// non-2xx means the batch NEVER RAN, and a per-item `code` means that item did
// not land while others may have. batchCreateIssues is the deliberate opposite
// and says so in its own document.
//
// A collection-level custom method, spelled the way issues:batchCreate is.
// Hooks do not fire and the per-command auto-commit machinery does not run.
func (s *Server) handleBatchClose(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.batchCloseRequest(w, r)
	if !ok {
		return
	}

	closer, err := s.batchCloser(r)
	if err != nil {
		s.failBatchClose(w, r, err)
		return
	}
	result, err := closer.CloseBatch(r.Context(), request)
	if err != nil {
		s.failBatchClose(w, r, err)
		return
	}
	// The role promises one outcome per requested item in request order, and
	// this projection preserves that: a client walks the array against its own
	// argument list, so dropping or reordering an entry would silently
	// misattribute every outcome after it.
	outcomes := make([]apigen.CloseOutcome, 0, len(result.Outcomes))
	for _, outcome := range result.Outcomes {
		outcomes = append(outcomes, closeOutcome(outcome))
	}
	writeJSON(w, apigen.BatchCloseResponse{Outcomes: outcomes})
}

// closeOutcome projects one role outcome onto the wire.
//
// `code`'s PRESENCE is the discriminator the document publishes, so the two
// branches here are disjoint by construction rather than by convention: a
// refused item carries the code and nothing else, and a successful one carries
// the row, the idempotence flag and the count.
//
// THE REFUSAL BRANCH READS TYPED FIELDS, never prose — the same rule every 409
// on this surface follows, applied one scope down. `open_children` comes from
// *issueops.CloseOpenChildrenError's own field, filled inside the transaction
// that refused, and its ABSENCE is what tells a client the other not_closable
// refusal (a live blocker) apart from this one.
//
// AN UNRECOGNIZED ITEM ERROR IS NOT SILENTLY DROPPED. The role documents three
// refusals and this maps three; anything else becomes a `not_closable` carrying
// no members would be a lie, so it takes the outcome's only honest shape — a
// refusal with the generic internal code — rather than being reported as a
// success with no row, which is the shape a `default: success` would produce.
func closeOutcome(outcome issueops.CloseOutcome) apigen.CloseOutcome {
	wire := apigen.CloseOutcome{IssueId: outcome.IssueID}
	if outcome.Err == nil {
		alreadyClosed := !outcome.Changed
		openChildren := outcome.OpenChildren
		issue := outcome.Issue
		wire.AlreadyClosed = &alreadyClosed
		wire.OpenChildren = &openChildren
		if issue != nil {
			wire.Issue = issue
		}
		return wire
	}

	var openChildren *issueops.CloseOpenChildrenError
	var (
		code   Code
		detail string
	)
	switch {
	case errors.As(outcome.Err, &openChildren):
		code, detail = CodeNotClosable, "this issue has open children; close them first, or send `force`"
		count := openChildren.OpenChildren
		wire.OpenChildren = &count

	case errors.Is(outcome.Err, issueops.ErrCloseBlocked):
		// No `open_children`, and its ABSENCE is what tells a client which of
		// the two close-policy refusals this item got.
		code, detail = CodeNotClosable, "this issue is blocked; clear the blocker, or send `force`"

	case errors.Is(outcome.Err, storage.ErrNotFound):
		code, detail = CodeNotFound, "no issue with this id in either plane"

	default:
		code, detail = CodeInternal, staticDetail[CodeInternal]
	}
	wire.Code = ptrTo(string(code))
	wire.Detail = ptrTo(detail)
	return wire
}

func ptrTo[T any](v T) *T { return &v }

// batchCloseRequest decodes and validates the body. Every refusal here happens
// BEFORE any database work, and every one of them means the batch never ran.
func (s *Server) batchCloseRequest(w http.ResponseWriter, r *http.Request) (issueops.CloseBatchRequest, bool) {
	members, res := decodeJSONObject(w, r, maxBatchCloseBodyBytes)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.CloseBatchRequest{}, false
	}
	if offender, unknown := unknownMember(members, batchCloseRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, batchCloseRequestMembers)
		return issueops.CloseBatchRequest{}, false
	}

	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.CloseBatchRequest{}, false
	}
	session, ok := s.storedTextMember(w, r, members, "session")
	if !ok {
		return issueops.CloseBatchRequest{}, false
	}
	force, ok := s.booleanMember(w, r, members, "force")
	if !ok {
		return issueops.CloseBatchRequest{}, false
	}
	items, ok := s.batchCloseItems(w, r, members)
	if !ok {
		return issueops.CloseBatchRequest{}, false
	}
	// ClaimNext stays unset, and the document says why: expressing it would
	// need a second, body-shaped spelling of the ready-filter vocabulary that
	// listReadyWork and claimNext both express as query parameters.
	return issueops.CloseBatchRequest{
		Actor:   actor,
		Items:   items,
		Session: session,
		Force:   force,
	}, true
}

// batchCloseItems validates `items` and projects it onto the role's items. It
// is batchCreateItems' body over a narrower item, and it refuses the same three
// request shapes for the same reasons.
func (s *Server) batchCloseItems(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) ([]issueops.BatchCloseItem, bool) {
	raw, ok := members["items"]
	if !ok {
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue, "`items` is required"))
		return nil, false
	}
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil || rawItems == nil {
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue, "`items` must be an array of objects"))
		return nil, false
	}
	switch {
	case len(rawItems) == 0:
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue,
			"`items` must carry at least one issue; a close that closes nothing is refused rather than answered"))
		return nil, false
	case len(rawItems) > maxBatchCloseItems:
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue,
			fmt.Sprintf("`items` carries %d issues; the limit is %d per request", len(rawItems), maxBatchCloseItems)))
		return nil, false
	}

	items := make([]issueops.BatchCloseItem, 0, len(rawItems))
	for i, rawItem := range rawItems {
		if rawItem == nil {
			s.fail(w, r, InvalidArgument(batchCloseItemParam(i, ""), ReasonInvalidValue, "an item must be a JSON object"))
			return nil, false
		}
		if offender, unknown := unknownMember(rawItem, batchCloseItemMembers); unknown {
			s.failUnknownMember(w, r, batchCloseItemParam(i, offender), batchCloseItemMembers)
			return nil, false
		}
		item, res := batchCloseItem(i, rawItem)
		if res != nil {
			s.fail(w, r, *res)
			return nil, false
		}
		items = append(items, item)
	}
	return items, true
}

// batchCloseItem projects one decoded item onto the role's item.
//
// The id is BOUNDED HERE, which the single close gets from the custom-method
// dispatcher and this operation has no dispatcher to get it from: an id longer
// than the column, or carrying a control character a percent-escape decoded to,
// names no row that can exist. It is a 400 rather than a per-item `not_found`
// because it is a statement about the REQUEST — the batch never ran — and
// because reporting it as a miss would let a caller map this server's notion of
// a well-formed id.
func batchCloseItem(index int, raw map[string]json.RawMessage) (issueops.BatchCloseItem, *Result) {
	refuse := func(member, detail string) *Result {
		res := InvalidArgument(batchCloseItemParam(index, member), ReasonInvalidValue, detail)
		return &res
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return issueops.BatchCloseItem{}, refuse("", "an item must be a JSON object")
	}
	var wire apigen.BatchCloseItem
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return issueops.BatchCloseItem{}, refuse("", "an item member carries the wrong JSON type")
	}
	// The custom-method dispatcher's own two checks, applied here because this
	// operation is not on its pattern.
	switch {
	case wire.Id == "":
		return issueops.BatchCloseItem{}, refuse("id", "`id` is required")
	case types.CheckFieldLen("id", wire.Id) != nil:
		return issueops.BatchCloseItem{}, refuse("id",
			fmt.Sprintf("`id` is %d characters; storage holds at most %d",
				utf8.RuneCountInString(wire.Id), types.MaxFieldLen))
	case strings.ContainsFunc(wire.Id, isControlChar):
		return issueops.BatchCloseItem{}, refuse("id", "`id` must not contain control characters")
	}
	reason := ""
	if wire.Reason != nil {
		// storedTextMember's rules, applied to an item member: the value lands
		// in a column a renderer prints, so an unfiltered C1 introducer would
		// make a close reason an escape-sequence payload.
		switch {
		case types.CheckFieldLen("reason", *wire.Reason) != nil:
			return issueops.BatchCloseItem{}, refuse("reason",
				fmt.Sprintf("`reason` is %d characters; storage holds at most %d",
					utf8.RuneCountInString(*wire.Reason), types.MaxFieldLen))
		case strings.ContainsFunc(*wire.Reason, isControlChar):
			return issueops.BatchCloseItem{}, refuse("reason", "`reason` must not contain control characters")
		}
		reason = *wire.Reason
	}
	return issueops.BatchCloseItem{IssueID: wire.Id, Reason: reason}, nil
}

// batchCloseItemParam names an item member the way a client reads it back off
// `param`: indexed, so an offender in a hundred-item request is found without a
// search. It is batchCreateItemParam's spelling, deliberately.
func batchCloseItemParam(index int, member string) string {
	if member == "" {
		return fmt.Sprintf("items[%d]", index)
	}
	return fmt.Sprintf("items[%d].%s", index, member)
}

// failBatchClose answers a request that never ran.
//
// The role's own error is reserved for request validation, cancellation and
// infrastructure — a non-nil error and populated outcomes are mutually
// exclusive — so there is nothing here to classify per item. The role's
// ErrValidation is defensively unreachable: an empty actor, an empty item list
// and a blank item id are all refused at the edge.
func (s *Server) failBatchClose(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, storage.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
	s.fail(w, r, InvalidArgument("", ReasonInvalidValue,
		"the request was refused by this workspace's own validation; nothing was written"))
}
