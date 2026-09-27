package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The request body's member vocabulary. The schema is
// additionalProperties: false, so anything else is refused BY NAME, the posture
// every body on this surface takes.
const (
	// addCommentAuthorMember is the signature on the row, and it is deliberately
	// NOT `actor`. Every other mutation here names the principal a write is
	// attributed to; this value is part of what the write STORED, read back by
	// everyone who sees the thread, and it is spelled the way the `Comment`
	// element that carries it back spells it. issueops.AddCommentRequest.Author
	// owns the distinction.
	addCommentAuthorMember = "author"
	// addCommentTextMember is the comment body.
	addCommentTextMember = "text"
)

// addCommentRequestMembers is the document's member list for AddCommentRequest.
var addCommentRequestMembers = []string{addCommentAuthorMember, addCommentTextMember}

// handleAddComment appends one comment to the thread an issue owns:
// POST /v0/beads/issues/{id}/comments, and the write `bd comment` spells.
//
// WHAT IS NOT HERE. Which plane the anchor lives on, whether a history entry is
// recorded, and the atomicity of the append are all issueops.Commenter's, held
// to on three legs by its own contract. This file is a decoder, a path bound and
// a projection.
//
// THE AUTHOR IS CALLER-ASSERTED and is not the authenticated principal, on the
// claim's terms: a configured bearer is shared and surface-wide, so it admits a
// client to everything and names nobody. What is new here is that the value does
// not merely attribute the write — it IS part of the row, so a reader of the
// thread is reading a claim the writer made about itself.
//
// PLANES: the id resolves across BOTH, so a WISP IS A LEGAL TARGET. The comment
// lands on the ephemeral thread and reads back from it; only the durable history
// entry is absent, because the wisp tables are dolt-ignored. Nothing here
// decides that — the role resolves the plane inside the transaction it writes
// in, which is what stops a comment landing on a row an earlier read saw.
func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	// The id is bounded HERE, before the request buys a concurrency slot and a
	// database round trip, exactly as handleGetIssue and the custom-method
	// dispatcher bound their own. The column is VARCHAR(255) and the document
	// calls this an exact canonical id, so a longer one — or one carrying a
	// control character, which a percent-escape in the path decodes to — names
	// no row that can exist. It is the SAME 404 a real miss gets, for the
	// dispatcher's reason: a distinct refusal would let a caller map this
	// server's notion of a well-formed id and there is nothing to learn from it.
	id := r.PathValue("id")
	if !s.acceptIssueID(w, r, id) {
		return
	}
	request, ok := s.addCommentRequest(w, r, id)
	if !ok {
		return
	}

	commenter, err := s.commenter(r)
	if err != nil {
		s.failAddComment(w, r, err)
		return
	}
	result, err := commenter.AddComment(r.Context(), request)
	if err != nil {
		s.failAddComment(w, r, err)
		return
	}
	// The row as STORED: the id the insert minted and created_at at the column's
	// precision, never the request reflected back. checkedCommenter is what makes
	// this dereference safe on a caller-supplied role.
	//
	// An ASSIGNMENT rather than a conversion, which is pinning.go's idiom: the
	// generated type IS types.Comment, so there is no second wire struct here,
	// and this line stops compiling the day a regenerated mirror appears.
	var comment apigen.Comment = *result.Comment
	writeJSON(w, comment)
}

// addCommentRequest decodes and validates the body, and reports whether the
// request may proceed. Every refusal here happens BEFORE any database work.
//
// The two members are validated to DIFFERENT depths, and the difference is the
// column each lands in. `author` is a 255-character field that renderers print,
// so it gets `actor`'s rules whole. `text` is a TEXT column stored verbatim, so
// it is checked for SHAPE only and its one content rule — blank after trimming —
// is left to the role, which is rememberMemory's posture for the same reason:
// routing it through the role keeps one definition of what a comment is, so
// `bd comment` and this endpoint refuse the same input with the same sentence.
func (s *Server) addCommentRequest(w http.ResponseWriter, r *http.Request, id string) (issueops.AddCommentRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.AddCommentRequest{}, false
	}
	if offender, unknown := unknownMember(members, addCommentRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, addCommentRequestMembers)
		return issueops.AddCommentRequest{}, false
	}

	author, ok := s.requiredNameMember(w, r, members, addCommentAuthorMember)
	if !ok {
		return issueops.AddCommentRequest{}, false
	}
	text, ok := s.requiredVerbatimMember(w, r, members, addCommentTextMember)
	if !ok {
		return issueops.AddCommentRequest{}, false
	}

	return issueops.AddCommentRequest{Author: author, IssueID: id, Text: text}, true
}

// requiredNameMember reads a required member that names someone, under the
// actor rules. It is bodyActor generalized over the member's spelling: the
// claim's `actor` and this operation's `author` are the same validation with
// different names, and one body is what keeps them the same validation.
//
// Decoded through a POINTER so that `null` reaches the type-mismatch branch,
// for bodyActor's reason: unmarshaling JSON null into a string is a no-op,
// which would report a null as "empty after trimming" — the right status
// attached to prose that misdescribes what the client sent.
func (s *Server) requiredNameMember(
	w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage, name string,
) (string, bool) {
	raw, ok := members[name]
	if !ok {
		s.fail(w, r, InvalidArgument(name, ReasonInvalidValue, "`"+name+"` is required"))
		return "", false
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		s.fail(w, r, InvalidArgument(name, ReasonInvalidValue, "`"+name+"` must be a string"))
		return "", false
	}
	trimmed, res := validateNameMember(name, *value)
	if res != nil {
		s.fail(w, r, *res)
		return "", false
	}
	return trimmed, true
}

// requiredVerbatimMember reads a required string member that is stored as sent.
//
// NOTHING IS TRIMMED, BOUNDED OR FILTERED. This is storedTextMember's opposite
// number and the difference is deliberate: that helper serves 255-character
// columns whose values renderers print, and this one serves a LONGTEXT column
// whose value is a document. A length bound would refuse a stack trace, and a
// control rule would refuse the newlines a comment is written in. The only cap
// is decodeJSONObjectBody's 1 MiB, which every body here shares.
//
// THAT IS TRUE ON BOTH PLANES ONLY BECAUSE MIGRATION 0065 MADE IT SO. The
// ephemeral column was left at TEXT's 65535 bytes when the durable one was
// widened, so this handler's "no bound" was a bound of 65535 for any anchor that
// happened to be a wisp — and this operation resolves its anchor across both
// planes, so nothing above the storage seam could have told the caller which one
// it had. There is no length check to add here; the fix was the column.
//
// Explicit `null` is a 400 naming the member rather than an empty value, for
// requiredNameMember's reason.
func (s *Server) requiredVerbatimMember(
	w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage, name string,
) (string, bool) {
	raw, ok := members[name]
	if !ok {
		s.fail(w, r, InvalidArgument(name, ReasonInvalidValue, "`"+name+"` is required"))
		return "", false
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		s.fail(w, r, InvalidArgument(name, ReasonInvalidValue, "`"+name+"` must be a string"))
		return "", false
	}
	return *value, true
}

// acceptIssueID bounds a path id before any database work and reports whether
// the request may proceed, answering the 404 a real miss gets.
//
// It is customMethodTarget's bound, lifted for the sub-resource paths that do
// not go through the custom-method dispatcher and therefore never met it.
func (s *Server) acceptIssueID(w http.ResponseWriter, r *http.Request, id string) bool {
	if types.CheckFieldLen("id", id) != nil || strings.ContainsFunc(id, isControlChar) {
		s.fail(w, r, NotFound())
		return false
	}
	return true
}

// failAddComment answers a failed append.
//
// ONE ARM, and the absences are the reasoning. There is no ErrNotFound case
// because ClassifyError already carries that row, and adding one here would be a
// second definition of the miss that could only ever drift from the first; the
// tests drive an anchor that names nothing to prove the 404 arrives through the
// shared mapping. There is no conflict arm because the role has no conflict to
// raise: a thread is append-only and this write touches no field of the issue.
//
// The 400 NAMES `text` unconditionally, and that is a claim about reachability
// rather than a guess. issueops.Commenter has exactly three ErrValidation
// refusals: an empty author, which the edge refuses first under strictly
// stronger rules; an empty issue id, which cannot arrive because a ServeMux
// wildcard does not match an empty segment and a bad id is already the 404; and
// a blank body, which is the only one a client can reach. It carries the role's
// own sentence, which is what makes `bd comment` and this endpoint say the same
// thing about the same input.
func (s *Server) failAddComment(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, storage.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	requestInfo(r.Context()).refuse(addCommentTextMember)
	s.fail(w, r, InvalidArgument(addCommentTextMember, ReasonInvalidValue, err.Error()))
}
