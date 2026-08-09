package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

const (
	// updatePatchMember is the member carrying the fields to write. Nesting them
	// rather than spreading them beside `actor` keeps the patch vocabulary a
	// closed set that can grow without ever colliding with a request-level
	// member.
	updatePatchMember = "patch"
	// maxUpdateBodyBytes bounds the request body. A patch can carry a
	// description, a design and acceptance criteria at once, so the claim's
	// megabyte is too tight and the batch's four is the right order.
	maxUpdateBodyBytes = 4 << 20
)

// updateRequestMembers and issuePatchMembers are the document's member lists at
// each level, refused BY NAME for the reason every other body on this surface
// is: encoding/json's DisallowUnknownFields reports the offender only inside an
// error string.
var (
	updateRequestMembers = []string{"actor", updatePatchMember}
	issuePatchMembers    = []string{
		"title", "description", "design", "acceptance_criteria",
		"notes", "append_notes", "priority", "issue_type", "labels",
		"estimated_minutes", "external_ref", "due_at", "defer_until",
	}
	// nullablePatchMembers is the closed set on which explicit `null` CLEARS
	// rather than refuses. They are exactly the members the role models as
	// Field[*T], because a pointer is the only thing a clear has to write.
	nullablePatchMembers = map[string]bool{
		"estimated_minutes": true,
		"external_ref":      true,
		"due_at":            true,
		"defer_until":       true,
	}
)

// handleUpdate edits the fields of one issue.
//
// It carries the claim's posture verbatim. The actor is caller-ASSERTED
// provenance for the audit trail and not authenticated identity; hooks do not
// fire and the per-command auto-commit machinery does not run, exactly as for
// POST /v0/beads/issues/{id}:claim. The only durable effect is the single
// storage commit the role makes inside its own transaction.
//
// A PLAIN PATCH on the issue-detail path rather than a custom method, so the id
// arrives from the router's own wildcard and there is no suffix to split. The
// id bound is this handler's, because it is not on the dispatcher's pattern.
//
// PLANES, as for close and reopen: the id resolves across both.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.updateTarget(w, r)
	if !ok {
		return
	}
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.updateRequest(w, r, id)
	if !ok {
		return
	}

	lifecycle, err := s.lifecycle(r)
	if err != nil {
		s.failUpdate(w, r, err)
		return
	}
	result, err := lifecycle.Update(r.Context(), request)
	if err != nil {
		s.failUpdate(w, r, err)
		return
	}
	// `changed` is the role's own verbatim: a same-value patch is a 200 with
	// false, not an error — idempotent, like every replay answer here.
	writeJSON(w, apigen.UpdateIssueResponse{Issue: *result.Issue, Changed: result.Changed})
}

// updateTarget reads and bounds the id this operation addresses.
//
// The bound is the dispatcher's, applied here because this route is not on the
// dispatcher's pattern: an id longer than the column, or carrying a control
// character a percent-escape decoded to, names no row that can exist, and it
// gets the SAME 404 a real miss gets so a caller cannot map the server's notion
// of a well-formed id.
func (s *Server) updateTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if id == "" || types.CheckFieldLen("id", id) != nil || strings.ContainsFunc(id, isControlChar) {
		s.fail(w, r, NotFound())
		return "", false
	}
	return id, true
}

// updateRequest decodes and validates the body, and reports whether the request
// may proceed. Every refusal here happens BEFORE any database work.
func (s *Server) updateRequest(w http.ResponseWriter, r *http.Request, id string) (issueops.UpdateRequest, bool) {
	members, res := decodeJSONObject(w, r, maxUpdateBodyBytes)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.UpdateRequest{}, false
	}
	if offender, unknown := unknownMember(members, updateRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, updateRequestMembers)
		return issueops.UpdateRequest{}, false
	}

	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.UpdateRequest{}, false
	}
	patch, ok := s.issuePatch(w, r, members)
	if !ok {
		return issueops.UpdateRequest{}, false
	}

	// Claim, the force flags and every Expected* precondition stay ZERO:
	// unpublished on this surface. Publishing a precondition means minting a
	// frozen conflict code and putting `row_version` on the issue body for
	// clients to echo, which is its own decision rather than a rider on this.
	return issueops.UpdateRequest{
		Actor:      actor,
		IssueID:    id,
		Patch:      patch,
		Provenance: updateProvenance,
	}, true
}

// updateProvenance labels the history entry an update records, naming this
// surface for reopenProvenance's reason: the role's implementations disagree
// about their own default, so a spelled label is what makes an entry read the
// same whichever backend answered. Not wire-visible.
const updateProvenance = "bd serve: update issue"

// issuePatch projects the decoded `patch` member onto the role's IssuePatch.
//
// MEMBER PRESENCE IS THE SIGNAL, which is the whole reason the body is decoded
// as raw members: a member present sets the role's Field, a member absent
// leaves the field untouched, and the generated struct cannot tell those apart
// because it models both as a nil pointer. Explicit `null` is a third state
// this reads directly off the raw bytes — a clear on the four nullable members,
// and a 400 naming the member everywhere else.
func (s *Server) issuePatch(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) (issueops.IssuePatch, bool) {
	refuse := func(member, detail string) (issueops.IssuePatch, bool) {
		s.fail(w, r, InvalidArgument(patchParam(member), ReasonInvalidValue, detail))
		return issueops.IssuePatch{}, false
	}

	raw, ok := members[updatePatchMember]
	if !ok {
		return refuse("", "`"+updatePatchMember+"` is required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return refuse("", "`"+updatePatchMember+"` must be a JSON object")
	}
	if len(fields) == 0 {
		// A write that writes nothing is a client bug, not a no-op to answer —
		// the batch-create empty-items judgement, applied to a patch.
		return refuse("", "`"+updatePatchMember+"` must carry at least one field; an update that updates nothing is refused rather than answered")
	}
	if offender, unknown := unknownMember(fields, issuePatchMembers); unknown {
		s.failUnknownMember(w, r, patchParam(offender), issuePatchMembers)
		return issueops.IssuePatch{}, false
	}

	// Explicit null, before any typed decode: unmarshaling null into *T yields
	// nil, which is indistinguishable from the member being absent, so a null on
	// a non-nullable member would otherwise slide through as "untouched" — a
	// write the client asked for and the server silently dropped.
	for name, value := range fields {
		if isJSONNull(value) && !nullablePatchMembers[name] {
			return refuse(name, "`"+name+"` is not nullable; omit it to leave the field unchanged")
		}
	}

	// The typed decode, which is what makes a member's type the DOCUMENT's
	// type: `priority: "high"` is refused here rather than reaching a role that
	// would have to guess what the caller meant.
	var wire apigen.IssuePatchBody
	if err := json.Unmarshal(raw, &wire); err != nil {
		return refuse("", "a `"+updatePatchMember+"` member carries the wrong JSON type")
	}

	patch := issueops.IssuePatch{}
	set := func(name string) bool { _, present := fields[name]; return present }

	if set("title") {
		title := *wire.Title
		if strings.TrimSpace(title) == "" {
			return refuse("title", "`title` must not be blank")
		}
		if types.CheckFieldLen("title", title) != nil {
			return refuse("title", fmt.Sprintf("`title` is %d characters; storage holds at most %d",
				utf8.RuneCountInString(title), types.MaxFieldLen))
		}
		patch.Title = issueops.Field[string]{Set: true, Value: title}
	}
	if set("description") {
		patch.Description = issueops.Field[string]{Set: true, Value: *wire.Description}
	}
	if set("design") {
		patch.Design = issueops.Field[string]{Set: true, Value: *wire.Design}
	}
	if set("acceptance_criteria") {
		patch.AcceptanceCriteria = issueops.Field[string]{Set: true, Value: *wire.AcceptanceCriteria}
	}
	// The role refuses both together too, but refusing here keeps the 400 a
	// statement about the request rather than a translated storage error.
	if set("notes") && set("append_notes") {
		return refuse("append_notes", "`notes` replaces the notes and `append_notes` adds to them; send one")
	}
	if set("notes") {
		patch.Notes = issueops.Field[string]{Set: true, Value: *wire.Notes}
	}
	if set("append_notes") {
		patch.AppendNotes = issueops.Field[string]{Set: true, Value: *wire.AppendNotes}
	}
	if set("priority") {
		priority := *wire.Priority
		if priority < 0 || priority > 4 {
			return refuse("priority", fmt.Sprintf("`priority` is %d; the range is 0 to 4", priority))
		}
		patch.Priority = issueops.Field[int]{Set: true, Value: priority}
	}
	if set("issue_type") {
		issueType := *wire.IssueType
		// Only what the SCHEMA declares is checked here. Whether the value is in
		// this workspace's configured vocabulary is the role's question, and it
		// cannot be asked without a transaction; failUpdate answers that one.
		if types.CheckFieldLen("issue_type", issueType) != nil {
			return refuse("issue_type", fmt.Sprintf("`issue_type` is %d characters; storage holds at most %d",
				utf8.RuneCountInString(issueType), types.MaxFieldLen))
		}
		patch.IssueType = issueops.Field[issueops.IssueType]{Set: true, Value: issueops.IssueType(issueType)}
	}
	if set("labels") {
		labels := *wire.Labels
		for i, label := range labels {
			if types.CheckFieldLen("label", label) != nil {
				return refuse("labels", fmt.Sprintf("`labels[%d]` is %d characters; storage holds at most %d",
					i, utf8.RuneCountInString(label), types.MaxFieldLen))
			}
		}
		// COMPLETE REPLACEMENT, and an empty array clears every label — which is
		// why this sets Replace rather than Add.
		patch.Labels = issueops.LabelPatch{Replace: issueops.Field[[]string]{Set: true, Value: labels}}
	}

	// The four nullable members. Set is true whenever the member is present;
	// the VALUE is a nil pointer for an explicit null, which is how a clear
	// reaches the role.
	if set("estimated_minutes") {
		patch.EstimatedMinutes = issueops.Field[*int]{Set: true, Value: wire.EstimatedMinutes}
	}
	if set("external_ref") {
		if ref := wire.ExternalRef; ref != nil && types.CheckFieldLen("external_ref", *ref) != nil {
			return refuse("external_ref", fmt.Sprintf("`external_ref` is %d characters; storage holds at most %d",
				utf8.RuneCountInString(*ref), types.MaxFieldLen))
		}
		patch.ExternalRef = issueops.Field[*string]{Set: true, Value: wire.ExternalRef}
	}
	if set("due_at") {
		patch.DueAt = issueops.Field[*time.Time]{Set: true, Value: wire.DueAt}
	}
	if set("defer_until") {
		patch.DeferUntil = issueops.Field[*time.Time]{Set: true, Value: wire.DeferUntil}
	}
	return patch, true
}

// isJSONNull reports whether a raw member is the literal `null`.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// patchParam names a patch member the way a client reads it back off `param`:
// qualified by the member that carries it, so `patch.title` is unambiguous
// against a request-level member of the same name.
func patchParam(member string) string {
	if member == "" {
		return updatePatchMember
	}
	return updatePatchMember + "." + member
}

// failUpdate answers a failed update, mapping the role's own validation refusal
// onto the 400 the document promises.
//
// It is failBatchCreate's sibling and follows its rule exactly: the role's
// ErrValidation is answered HERE rather than in ClassifyError, and NEITHER
// BRANCH QUOTES THE ROLE'S MESSAGE. A refusal from the workspace's configured
// vocabulary arrives as prose about statuses and types, and 4xx details on this
// surface reflect the caller's own input back rather than server internals. The
// real error goes to the log with the request id.
//
// The ErrNotFound check runs FIRST and is the divergence from failBatchCreate:
// this operation DOES address a resource by path, so an id that names nothing
// is a genuine 404 rather than a statement about the body.
func (s *Server) failUpdate(w http.ResponseWriter, r *http.Request, err error) {
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
	s.fail(w, r, InvalidArgument(updatePatchMember, ReasonInvalidValue,
		"a patch value was refused by this workspace's own validation; nothing was written"))
}
