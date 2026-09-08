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
	updateRequestMembers = []string{
		"actor", "expected_assignee", "expected_status", "expected_version",
		"force_assignee_transfer", "force_close_policy", updatePatchMember,
	}
	issuePatchMembers = []string{
		"title", "description", "design", "acceptance_criteria",
		"notes", "append_notes", "priority", "issue_type", "status",
		"assignee", "parent_id", "labels", "add_labels", "remove_labels", "metadata",
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
		s.failUpdate(w, r, request, err)
		return
	}
	result, err := lifecycle.Update(r.Context(), request)
	if err != nil {
		s.failUpdate(w, r, request, err)
		return
	}
	// `changed` is the role's own verbatim: a same-value patch is a 200 with
	// false, not an error — idempotent, like every replay answer here.
	//
	// `revision` is the row's post-write concurrency token, read off the same
	// snapshot rather than computed. It is on the wire because `expected_version`
	// is: a guard whose token no response carries is a guard a caller cannot
	// fill. types.Issue.RowVersion is `json:"-"`, so the Issue body cannot carry
	// it and this member is where it lives.
	writeJSON(w, apigen.UpdateIssueResponse{
		Issue:    *result.Issue,
		Changed:  result.Changed,
		Revision: result.Issue.RowVersion,
	})
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
	patch, ok := s.issuePatch(w, r, id, members)
	if !ok {
		return issueops.UpdateRequest{}, false
	}

	expectedVersion, res := applyVersionGuardMember(members, "")
	if res != nil {
		s.fail(w, r, *res)
		return issueops.UpdateRequest{}, false
	}
	expectedStatus, ok := s.updateExpectedStatus(w, r, members)
	if !ok {
		return issueops.UpdateRequest{}, false
	}
	expectedAssignee, ok := s.updateExpectedAssignee(w, r, members)
	if !ok {
		return issueops.UpdateRequest{}, false
	}
	forceClosePolicy, ok := s.booleanMember(w, r, members, "force_close_policy")
	if !ok {
		return issueops.UpdateRequest{}, false
	}
	forceAssigneeTransfer, ok := s.booleanMember(w, r, members, "force_assignee_transfer")
	if !ok {
		return issueops.UpdateRequest{}, false
	}
	// The role documents both combinations as invalid, and refusing them HERE
	// keeps the 400 a statement about the request rather than a translated
	// storage error — the `notes`/`append_notes` rule, applied to the two
	// members that would otherwise reach a role obliged to guess which of the
	// caller's two opinions about the assignee wins.
	if forceAssigneeTransfer {
		switch {
		case expectedAssignee != nil:
			s.fail(w, r, InvalidArgument("force_assignee_transfer", ReasonInvalidValue,
				"`expected_assignee` is the compare-and-set that replaces the fence; `force_assignee_transfer` bypasses it. Send one"))
			return issueops.UpdateRequest{}, false
		case !patch.Assignee.Set:
			s.fail(w, r, InvalidArgument("force_assignee_transfer", ReasonInvalidValue,
				"`force_assignee_transfer` bypasses the fence on an assignee TRANSFER; send `patch.assignee` with it"))
			return issueops.UpdateRequest{}, false
		}
	}

	// Claim stays ZERO — acquiring work is `{id}:claim`, which carries its own
	// eligibility rules — and so does IssuePlaneOnly, because this operation
	// resolves across both planes.
	return issueops.UpdateRequest{
		Actor:                 actor,
		IssueID:               id,
		Patch:                 patch,
		ExpectedVersion:       expectedVersion,
		ExpectedStatus:        expectedStatus,
		ExpectedAssignee:      expectedAssignee,
		ForceClosePolicy:      forceClosePolicy,
		ForceAssigneeTransfer: forceAssigneeTransfer,
		Provenance:            updateProvenance,
	}, true
}

// updateExpectedStatus reads the status precondition, preserving the difference
// between an absent guard and one that expects the empty status. The role
// models it as a POINTER for exactly that reason, so an absent member must not
// collapse into a guard on "".
func (s *Server) updateExpectedStatus(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) (*issueops.Status, bool) {
	if _, present := members["expected_status"]; !present {
		return nil, true
	}
	value, ok := s.storedTextMember(w, r, members, "expected_status")
	if !ok {
		return nil, false
	}
	status := issueops.Status(value)
	return &status, true
}

// updateExpectedAssignee is updateExpectedStatus's twin, and the difference
// between nil and a pointer to "" is load-bearing here too: a guard on the
// EMPTY assignee is how a caller says "only if nobody holds it".
func (s *Server) updateExpectedAssignee(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) (*string, bool) {
	if _, present := members["expected_assignee"]; !present {
		return nil, true
	}
	value, ok := s.storedTextMember(w, r, members, "expected_assignee")
	if !ok {
		return nil, false
	}
	return &value, true
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
func (s *Server) issuePatch(w http.ResponseWriter, r *http.Request, id string, members map[string]json.RawMessage) (issueops.IssuePatch, bool) {
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
	// Only what the SCHEMA declares is checked for these four. Whether a value is
	// in this workspace's configured vocabulary is the ROLE's question and cannot
	// be asked without a transaction; failUpdate answers that one. A SLICE and
	// not a map, so a patch breaking two rules always names the same offender.
	for _, bounded := range []struct {
		member string
		value  *string
	}{
		{"issue_type", wire.IssueType}, {"status", wire.Status},
		{"assignee", wire.Assignee}, {"parent_id", wire.ParentId},
	} {
		if res := applyBoundedText(updatePatchMember+".", bounded.member, bounded.value); res != nil {
			s.fail(w, r, *res)
			return issueops.IssuePatch{}, false
		}
	}
	if set("issue_type") {
		patch.IssueType = issueops.Field[issueops.IssueType]{Set: true, Value: issueops.IssueType(*wire.IssueType)}
	}
	if set("status") {
		patch.Status = issueops.Field[issueops.Status]{Set: true, Value: issueops.Status(*wire.Status)}
	}
	if set("assignee") {
		patch.Assignee = issueops.Field[string]{Set: true, Value: *wire.Assignee}
	}
	if set("parent_id") {
		// A self-parent is refused HERE, and it is the one graph rule this
		// handler can apply: the child is the id in the PATH, so the request
		// contradicts itself without any state being read. The two legs of the
		// role disagree about the error they raise for it — one a bare
		// fmt.Errorf, one ErrSelfDependency — so refusing at the edge is also
		// what makes the answer the same whichever backend served it.
		if *wire.ParentId == id {
			return refuse("parent_id", "`parent_id` names this issue; an issue cannot be its own parent")
		}
		patch.ParentID = issueops.Field[string]{Set: true, Value: *wire.ParentId}
	}
	if set("metadata") {
		// applyMetadataPatch, unchanged and not respelled: the replace/merge/
		// set/unset algebra is one definition, and a second copy here would be a
		// second one. `set` in particular has to be read RAW so a key written to
		// JSON null survives — see that function.
		metadata, res := applyMetadataPatch(patchParam("metadata")+".", fields["metadata"])
		if res != nil {
			s.fail(w, r, *res)
			return issueops.IssuePatch{}, false
		}
		patch.Metadata = metadata
	}
	// THE THREE LABEL MEMBERS ARE ONE PATCH, assembled together because the role
	// models them as one: LabelPatch applies Replace, then Add, then Remove, so
	// removal wins where a label appears in more than one of them. They are NOT
	// mutually exclusive, which is the difference from `notes`/`append_notes`
	// above — that pair is a contradiction the role refuses, and this trio has a
	// defined order.
	//
	// Building the value in one place is what keeps the wire from inventing a
	// fourth combination rule: a member decoded into its own LabelPatch would
	// overwrite the others' fields, and the last one written would silently win.
	labelEdit := issueops.LabelPatch{}
	for _, member := range []struct {
		name   string
		values *[]string
		apply  func([]string)
	}{
		// COMPLETE REPLACEMENT, and an empty array clears every label — which is
		// why this sets Replace rather than Add.
		{"labels", wire.Labels, func(v []string) {
			labelEdit.Replace = issueops.Field[[]string]{Set: true, Value: v}
		}},
		{"add_labels", wire.AddLabels, func(v []string) { labelEdit.Add = v }},
		{"remove_labels", wire.RemoveLabels, func(v []string) { labelEdit.Remove = v }},
	} {
		if !set(member.name) {
			continue
		}
		values := *member.values
		// The role refuses an over-long label on ANY of the three and writes
		// nothing, so the edge refuses it on all three too. It is a rule about
		// what a label may BE rather than about whether this row carries one,
		// which is why `remove_labels` is bounded like the other two even though
		// a value the column could not hold could never have been stored.
		for i, label := range values {
			if types.CheckFieldLen("label", label) != nil {
				return refuse(member.name, fmt.Sprintf("`%s[%d]` is %d characters; storage holds at most %d",
					member.name, i, utf8.RuneCountInString(label), types.MaxFieldLen))
			}
		}
		member.apply(values)
	}
	patch.Labels = labelEdit

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

// failUpdate answers a failed update, mapping the role's TYPED refusals onto
// the frozen codes and its own validation refusal onto the 400 the document
// promises.
//
// EVERY 409 BRANCH READS TYPED FIELDS, never prose, and every one of them is
// matched BEFORE the ErrValidation and ErrNotFound arms. That order is the
// whole correctness of this function, and the hazard it avoids is worse than a
// disagreement between backends: NEITHER LEG WRAPS THESE FIVE FAMILIES IN
// ErrValidation. The store legs return ExecuteUpdate's error through
// runIssueOperationTx unchanged (internal/storage/dolt/issue_operations.go) and
// the unit of work returns ApplyUpdate's unchanged
// (internal/storage/uow/issue_operations.go), so a precondition miss, a close-
// policy refusal, the assignee fence and both graph refusals reach here as bare
// sentinels. Below the ErrValidation arm they would all fall into `!Is(...)`
// and be swallowed into failErr — a generic 500, on BOTH legs, for five
// conditions this document names by code.
//
// NEITHER BRANCH QUOTES THE ROLE'S MESSAGE. A refusal from the workspace's
// configured vocabulary arrives as prose about statuses and types, and a
// refused edge as a driver error naming tables and constraints; 4xx details on
// this surface reflect the caller's own input back rather than server
// internals. The real error goes to the log with the request id.
//
// The ErrNotFound arm is LAST among the misses and means the PATH id, which is
// the divergence from failBatchCreate: this operation addresses a resource, so
// an id that names nothing is a genuine 404. A missing new PARENT is not that —
// it is an edge endpoint, and it is a 400 on `patch.parent_id`, conforming to
// POST /v0/beads/dependencies:add.
func (s *Server) failUpdate(w http.ResponseWriter, r *http.Request, request issueops.UpdateRequest, err error) {
	var (
		typeConflict *issueops.DependencyTypeConflictError
		hierarchy    *issueops.DependencyHierarchyConflictError
		endpoint     *issueops.DependencyEndpointNotFoundError
		openChildren *issueops.CloseOpenChildrenError
		claimed      *issueops.ClaimConflictError
	)
	// The 4xx path does not log by default, so the refusals whose real reason
	// the response replaces with the server's own words are recorded here.
	refused := func() {
		s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
	}
	named := func(res Result, param string) Result {
		res.Problem.Param = &param
		return res
	}

	switch {
	case errors.Is(err, issueops.ErrVersionMismatch),
		errors.Is(err, issueops.ErrStatusMismatch),
		errors.Is(err, issueops.ErrAssigneeMismatch):
		s.fail(w, r, updatePreconditionResult(request, err))

	case errors.Is(err, issueops.ErrCloseOpenChildren):
		res := named(newResult(CodeNotClosable,
			"`patch.status` closes an issue with open children; close them first, or send `force_close_policy`"),
			patchParam("status"))
		if errors.As(err, &openChildren) {
			res = res.WithOpenChildren(openChildren.OpenChildren)
		}
		s.fail(w, r, res)

	case errors.Is(err, issueops.ErrCloseBlocked):
		// No `open_children` member, and its ABSENCE is what tells a client
		// which of the two close-policy refusals it got.
		s.fail(w, r, named(newResult(CodeNotClosable,
			"`patch.status` closes a blocked issue; clear the blocker, or send `force_close_policy`"),
			patchParam("status")))

	case errors.Is(err, storage.ErrAlreadyClaimed):
		res := named(newResult(CodeAlreadyClaimed,
			"`patch.assignee` transfers work away from a live foreign owner; send `force_assignee_transfer`, or guard with `expected_assignee`"),
			patchParam("assignee"))
		// The assignee fence refuses without naming the holder
		// (AuthorizeAssigneeTransferWithPools), so these members are attached
		// only when an implementation did report one. Absent means "re-read the
		// row", never "nobody holds it".
		if errors.As(err, &claimed) {
			if claimed.Assignee != "" {
				res = res.WithAssignee(claimed.Assignee)
			}
			if claimed.Status != "" {
				res = res.WithIssueStatus(string(claimed.Status))
			}
		}
		s.fail(w, r, res)

	case errors.As(err, &typeConflict):
		s.fail(w, r, named(newResult(CodeDependencyExists,
			"this pair already carries an edge of a different type; remove it before reparenting").
			WithDependencyTypeConflict(typeConflict.ExistingType, typeConflict.RequestedType),
			patchParam("parent_id")))

	// DEFENSIVE, AND UNREACHABLE TODAY. CheckBlockingHierarchyInTx returns nil
	// for anything but a `blocks` or `conditional-blocks` edge
	// (internal/storage/issueops/dependencies.go), and the only edge this
	// operation writes is the `parent-child` one patch.parent_id names — on both
	// legs, since the unit of work reaches the same function through
	// depRepo.Insert's ValidateBlockingHierarchy. The document says so and tells
	// clients not to dispatch on these members here.
	//
	// The arm stays because of what its ABSENCE would cost if that ever changes:
	// a hierarchy refusal reaching this handler would otherwise fall into the
	// plain-cycle arm below and be answered with the same code carrying NO
	// members, which is the one shape a client reading the discriminator cannot
	// tell from a scheduling cycle. Publishing a member is free; losing one
	// silently is not.
	case errors.As(err, &hierarchy):
		s.fail(w, r, named(newResult(CodeDependencyCycle,
			"this reparent would put the issue under its own descendant, or gate it on its own ancestor").
			WithHierarchyConflict(hierarchy.IssueID, hierarchy.BlockerID, hierarchy.BlockerIsAncestor),
			patchParam("parent_id")))

	// THE ARM EVERY REPARENT CYCLE ACTUALLY TAKES, carrying no hierarchy
	// members. CheckDependencyCycleInTx does cover parent-child — it is a
	// scheduling edge — so a move under the issue's own descendant lands here.
	case errors.Is(err, issueops.ErrDependencyCycle):
		s.fail(w, r, named(newResult(CodeDependencyCycle,
			"this reparent would create a dependency cycle; nothing was written"),
			patchParam("parent_id")))

	// An edge endpoint, not the resource this request addresses: the 400 that
	// conforms to POST /v0/beads/dependencies:add. It is matched before the
	// generic ErrNotFound so a missing PARENT can never be reported as a missing
	// ISSUE, which would send a client looking for the wrong row.
	case errors.As(err, &endpoint):
		refused()
		s.fail(w, r, InvalidArgument(patchParam("parent_id"), ReasonInvalidValue,
			"`parent_id` names no issue this workspace holds; nothing was written"))

	case errors.Is(err, storage.ErrNotFound):
		s.fail(w, r, NotFound())

	case !errors.Is(err, storage.ErrValidation):
		s.failErr(w, r, err)

	default:
		refused()
		s.fail(w, r, InvalidArgument(updatePatchMember, ReasonInvalidValue,
			"a patch value was refused by this workspace's own validation; nothing was written"))
	}
}

// updatePreconditionResult builds the 409 for a guard that missed, naming the
// guard member and echoing what the request asked for.
//
// applyPreconditionResult's rule unchanged: the expected value comes from the
// REQUEST rather than from a read, and the observed value is absent, because
// the refusal rolled its transaction back and a read afterwards would describe
// a row the refusal never saw. See PreconditionFailed.
func updatePreconditionResult(request issueops.UpdateRequest, err error) Result {
	res := PreconditionFailed()
	switch {
	case errors.Is(err, issueops.ErrVersionMismatch):
		res.Problem.Param = updateGuardParam("expected_version")
		if request.ExpectedVersion != nil {
			res = res.WithExpectedVersion(*request.ExpectedVersion)
		}
	case errors.Is(err, issueops.ErrStatusMismatch):
		res.Problem.Param = updateGuardParam("expected_status")
		if request.ExpectedStatus != nil {
			res = res.WithExpectedStatus(string(*request.ExpectedStatus))
		}
	default:
		res.Problem.Param = updateGuardParam("expected_assignee")
		if request.ExpectedAssignee != nil {
			res = res.WithExpectedAssignee(*request.ExpectedAssignee)
		}
	}
	return res
}

// updateGuardParam names a request-level guard member for `param`. The guards
// sit BESIDE `patch` rather than inside it, so they are spelled bare where a
// patch member is qualified by patchParam.
func updateGuardParam(member string) *string { return &member }
