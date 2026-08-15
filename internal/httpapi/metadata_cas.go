package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// metadataCASRequestMembers is the document's member list for
// CompareAndSetMetadataRequest, read as raw members for the reason
// closeRequestMembers is — and for one more that is this operation's alone:
// the two value members are optional and their OMISSION is meaningful, so the
// handler has to see presence rather than a decoded zero value.
var metadataCASRequestMembers = []string{"actor", "key", "expected", "value"}

// handleCompareAndSetMetadata conditionally sets one metadata key.
//
// It carries the claim's posture verbatim. The actor is caller-ASSERTED
// provenance for the audit trail and not authenticated identity; hooks do not
// fire and the per-command auto-commit machinery does not run. The only durable
// effect is the single storage commit the role makes inside its own
// transaction.
//
// A LOST RACE IS A 200. `swapped: false` with `current` carrying the value that
// refused the swap is the answer to the question that was asked, and the reason
// this operation has no 409: a conflict code would put the ordinary path of a
// retry loop into the error channel, and the value the loop needs next would
// have to travel in a problem extension member. The document says so for
// clients; this comment says so for the next handler author who reaches for
// ClassifyError here.
//
// Everything above the role is argument validation. What the values MEAN — the
// canonical equality rule, absence versus a stored null, which transitions
// write — belongs to issueops.MetadataCAS.
func (s *Server) handleCompareAndSetMetadata(w http.ResponseWriter, r *http.Request) {
	// The custom-method dispatcher split the id off the segment and bounded it
	// before this handler was chosen at all; see customMethodTarget.
	id := r.PathValue(customMethodIDValue)
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.compareAndSetMetadataRequest(w, r, id)
	if !ok {
		return
	}

	cas, err := s.metadataCAS(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := cas.CompareAndSetKey(r.Context(), request)
	if err != nil {
		s.failCompareAndSetMetadata(w, r, err)
		return
	}
	writeJSON(w, apigen.CompareAndSetMetadataResponse{
		Swapped: result.Swapped,
		Current: metadataCASWireValue(result.Current),
	})
}

// failCompareAndSetMetadata answers a failed swap, mapping the role's own
// validation refusal onto the 400 the document promises.
//
// It is failUpdate's sibling and follows its rule exactly: the role's
// ErrValidation is answered HERE rather than in ClassifyError, and neither
// branch QUOTES the role's message — a refusal from the metadata-key syntax or
// from the workspace's metadata schema arrives as prose about a key this
// surface has already reflected back, and 4xx details here describe the
// caller's own input rather than server internals. The real error goes to the
// log with the request id.
//
// The ErrNotFound check runs first because this operation addresses a resource
// by path, so an id that names nothing is a genuine 404.
//
// THERE IS NO CONFLICT BRANCH, and that is this operation's posture rather than
// an omission: a lost race never reaches here at all. It is a 200 carrying
// `swapped: false`.
func (s *Server) failCompareAndSetMetadata(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		s.fail(w, r, NotFound())
		return
	}
	if !errors.Is(err, storage.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	// The 4xx path does not log by default, so this is the one place the real
	// refusal is recorded for the operator.
	s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
	s.fail(w, r, InvalidArgument("key", ReasonInvalidValue,
		"the request was refused by this workspace's own metadata validation; nothing was written"))
}

// compareAndSetMetadataRequest decodes and validates the body, and reports
// whether the request may proceed.
func (s *Server) compareAndSetMetadataRequest(w http.ResponseWriter, r *http.Request, id string) (issueops.CompareAndSetKeyRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.CompareAndSetKeyRequest{}, false
	}
	if offender, unknown := unknownMember(members, metadataCASRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, metadataCASRequestMembers)
		return issueops.CompareAndSetKeyRequest{}, false
	}

	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.CompareAndSetKeyRequest{}, false
	}
	// requiredEndpointMember is named for an edge endpoint and is exactly the
	// bound this member needs: a required string, non-empty, within what
	// storage holds, and free of control characters — which a metadata key must
	// be for the same reason an id must, since it is printed by everything that
	// renders a row. The role applies the key SYNTAX on top of it.
	key, keyRes := requiredEndpointMember(members, "key")
	if keyRes != nil {
		s.fail(w, r, *keyRes)
		return issueops.CompareAndSetKeyRequest{}, false
	}

	// The two value members are passed through UNPARSED. Their syntax is
	// checked by the decode that produced these raw members, their SHAPE is
	// whatever JSON the caller sent — the metadata plane holds any JSON value —
	// and the role's own validation is the single definition of what it will
	// accept. A second parse here would be a second definition.
	return issueops.CompareAndSetKeyRequest{
		Actor:    actor,
		IssueID:  id,
		Key:      key,
		Expected: metadataCASMember(members, "expected"),
		Value:    metadataCASMember(members, "value"),
	}, true
}

// metadataCASMember reads one optional value member, preserving the difference
// between OMITTED and present. That difference is the whole request shape here:
// an omitted member means the key is absent on that side of the transition, and
// a member present holding `null` means it holds JSON null.
//
// It COPIES the raw bytes rather than aliasing the decoded body, so nothing
// downstream can be surprised by the request buffer's lifetime.
func metadataCASMember(members map[string]json.RawMessage, name string) *json.RawMessage {
	raw, ok := members[name]
	if !ok {
		return nil
	}
	value := json.RawMessage(append([]byte(nil), raw...))
	return &value
}

// metadataCASWireValue converts the role's optional current value to the wire's
// optional member. nil stays nil, so an absent key is an ABSENT member rather
// than a null one — the same distinction the request side makes, answered the
// same way.
//
// THE WIRE MEMBER IS NOT A POINTER, and that is load-bearing rather than tidy.
// apigen.MetadataValue is an alias of json.RawMessage, which is an Unmarshaler,
// so a client decoding a present `null` gets the literal stored; a
// *json.RawMessage would have been set to nil by encoding/json before the
// Unmarshaler ran, collapsing the two states this operation reports. See the
// schema's x-go-type-skip-optional-pointer note and
// TestCASMetadataResponseDecodesAPresentNull.
func metadataCASWireValue(current *json.RawMessage) apigen.MetadataValue {
	if current == nil {
		return nil
	}
	return *current
}
