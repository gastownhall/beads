package httpapi

import (
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

// The wire adapter over issueops.BatchApplier: an ORDERED, heterogeneous plan
// applied as one transaction or not at all.
//
// It carries the claim's posture verbatim. The actor is caller-ASSERTED
// provenance for the audit trail and not authenticated identity; hooks do not
// fire and the per-command auto-commit machinery does not run. The only durable
// effect is the single storage commit the role makes inside its own
// transaction.
//
// EVERYTHING ABOVE THE ROLE IS ARGUMENT VALIDATION. What the values MEAN — how
// a key resolves and which way it may reach, whether a precondition holds
// as-modified, whether a waits-for gate is one of the two, what the graph the
// whole request built permits — belongs to issueops.BatchApplier. This file
// decodes a body four levels deep, refuses the shapes the document refuses, and
// maps the role's TYPED refusals onto the frozen codes.

const (
	// maxApplyBatchItems is the document's cap on `items`, and the role's own
	// bound restated at the edge so an over-long request costs no database work.
	// It bounds how long one request may hold a write transaction — not batch
	// semantics, which have no size in them.
	maxApplyBatchItems = issueops.MaxApplyBatchItems
	// maxApplyBatchBodyBytes bounds the request body. A hundred items each
	// carrying a description, a design, acceptance criteria and a metadata
	// document is the shape this has to admit, so it refuses the absurd before
	// any of it is parsed. It is the batch create's bound for the same reason.
	maxApplyBatchBodyBytes = 4 << 20
)

// The document's member list at each of this body's levels. Every schema is
// additionalProperties: false, so anything else is refused BY NAME — which is
// why each level is decoded as raw members first, and why the levels below the
// request are checked in the same pass that projects them.
//
// PRESENCE IS THE SIGNAL at three of these levels and that is the second reason
// for the raw decode: an item's payload members carry the tagged union's
// disagreement cases, a patch member present is written where an absent one is
// untouched, and a metadata member present holding `null` is a value.
var (
	applyBatchRequestMembers = []string{"actor", "force_id_prefix", "items", "provenance", "skip_per_edge_cycle_check"}
	applyItemMembers         = []string{"close", "create", "dep_add", "kind", "update"}
	applyRefMembers          = []string{"id", "key"}
	applyCreateItemMembers   = []string{
		"acceptance_criteria", "assignee", "defer_until", "description", "design",
		"due_at", "ephemeral", "estimated_minutes", "external_ref", "id",
		"issue_type", "key", "labels", "metadata", "metadata_refs", "no_history",
		"notes", "owner", "priority", "sender", "status", "title",
	}
	applyUpdateItemMembers = []string{
		"expected_assignee", "expected_status", "expected_version",
		"force_assignee_transfer", "force_close_policy", "patch", "target",
	}
	applyPatchMembers = []string{
		"acceptance_criteria", "append_notes", "assignee", "defer_until",
		"description", "design", "due_at", "estimated_minutes", "external_ref",
		"issue_type", "labels", "metadata", "notes", "owner", "priority",
		"status", "title",
	}
	applyLabelPatchMembers    = []string{"add", "remove", "replace"}
	applyMetadataPatchMembers = []string{"merge", "replace", "set", "unset"}
	applyCloseItemMembers     = []string{"expected_version", "force", "reason", "session", "target"}
	applyDepAddItemMembers    = []string{"metadata", "source", "target", "type"}

	// applyItemKinds is the tag vocabulary, paired with the payload member each
	// value names. It is one map rather than a switch so the enum, the member
	// names and the agreement check cannot drift into three opinions.
	applyItemKinds = map[issueops.ItemKind]string{
		issueops.ItemCreate: "create",
		issueops.ItemUpdate: "update",
		issueops.ItemClose:  "close",
		issueops.ItemDepAdd: "dep_add",
	}

	// applyNullablePatchMembers is the closed set on which explicit `null`
	// CLEARS rather than refuses, exactly as it is for PATCH
	// /v0/beads/issues/{id}: they are the members the role models as Field[*T],
	// because a pointer is the only thing a clear has to write.
	applyNullablePatchMembers = map[string]bool{
		"estimated_minutes": true,
		"external_ref":      true,
		"due_at":            true,
		"defer_until":       true,
	}
)

// handleApplyBatch applies every item in the request body in order, or applies
// none of them.
//
// The transaction boundary, the ref resolution, the as-modified preconditions,
// the close policy, the assignee fence, the metadata splice and the end gate
// all belong to issueops.BatchApplier.
func (s *Server) handleApplyBatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.applyBatchRequest(w, r)
	if !ok {
		return
	}

	applier, err := s.batchApplier(r)
	if err != nil {
		s.failApplyBatch(w, r, request, err)
		return
	}
	result, err := applier.ApplyBatch(r.Context(), request)
	if err != nil {
		s.failApplyBatch(w, r, request, err)
		return
	}
	writeJSON(w, applyBatchResponse(result))
}

// applyBatchRequest decodes and validates the body, and reports whether the
// request may proceed. Every refusal here happens BEFORE any database work,
// which is what lets these 400s reflect the caller's own input back.
func (s *Server) applyBatchRequest(w http.ResponseWriter, r *http.Request) (issueops.ApplyBatchRequest, bool) {
	refuse := func(res *Result) (issueops.ApplyBatchRequest, bool) {
		s.fail(w, r, *res)
		return issueops.ApplyBatchRequest{}, false
	}

	members, res := decodeJSONObject(w, r, maxApplyBatchBodyBytes)
	if res != nil {
		return refuse(res)
	}
	if offender, unknown := unknownMember(members, applyBatchRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, applyBatchRequestMembers)
		return issueops.ApplyBatchRequest{}, false
	}

	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.ApplyBatchRequest{}, false
	}
	provenance, res := applyTextMember(members, "", "provenance")
	if res != nil {
		return refuse(res)
	}
	forceIDPrefix, res := applyBoolMember(members, "", "force_id_prefix")
	if res != nil {
		return refuse(res)
	}
	skipPerEdge, res := applyBoolMember(members, "", "skip_per_edge_cycle_check")
	if res != nil {
		return refuse(res)
	}
	items, res := applyItems(members)
	if res != nil {
		return refuse(res)
	}
	return issueops.ApplyBatchRequest{
		Actor:                 actor,
		Items:                 items,
		Provenance:            provenance,
		ForceIDPrefix:         forceIDPrefix,
		SkipPerEdgeCycleCheck: skipPerEdge,
	}, true
}

// applyItems validates `items` and projects it onto the role's items, in
// request order — which this operation never changes.
func applyItems(members map[string]json.RawMessage) ([]issueops.ApplyItem, *Result) {
	refuse := func(detail string) *Result {
		res := InvalidArgument("items", ReasonInvalidValue, detail)
		return &res
	}
	raw, ok := members["items"]
	if !ok {
		return nil, refuse("`items` is required")
	}
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil || rawItems == nil {
		return nil, refuse("`items` must be an array of objects")
	}
	switch {
	case len(rawItems) == 0:
		return nil, refuse("`items` must carry at least one item; a request that writes nothing is refused rather than answered")
	case len(rawItems) > maxApplyBatchItems:
		return nil, refuse(fmt.Sprintf("`items` carries %d items; the limit is %d per request", len(rawItems), maxApplyBatchItems))
	}

	items := make([]issueops.ApplyItem, 0, len(rawItems))
	for i, rawItem := range rawItems {
		item, res := applyItem(i, rawItem)
		if res != nil {
			return nil, res
		}
		items = append(items, item)
	}
	return items, nil
}

// applyItem projects one tagged item onto the role's item.
//
// IT ENFORCES THE TAG BY HAND, and that is the cost of the document's own
// doctrine rather than an oversight here. The item is a single-shape object
// with a required `kind` and four optional payload members, because a schema
// alternation would need a composition keyword this document does not use — so
// nothing in the generated type stops a client sending two payloads, or a
// payload the kind does not name. Both are answered here, before the role sees
// a request it would have to refuse for the same reason.
func applyItem(index int, raw map[string]json.RawMessage) (issueops.ApplyItem, *Result) {
	prefix := applyItemParam(index, "")
	if raw == nil {
		res := InvalidArgument(prefix, ReasonInvalidValue, "an item must be a JSON object")
		return issueops.ApplyItem{}, &res
	}
	if offender, unknown := unknownMember(raw, applyItemMembers); unknown {
		return issueops.ApplyItem{}, applyUnknownMember(prefix+".", offender, applyItemMembers)
	}

	kindText, res := applyRequiredText(raw, prefix+".", "kind")
	if res != nil {
		return issueops.ApplyItem{}, res
	}
	kind := issueops.ItemKind(kindText)
	named, known := applyItemKinds[kind]
	if !known {
		res := InvalidArgument(prefix+".kind", ReasonInvalidValue,
			"`kind` must be one of "+strings.Join(applyKindNames(), ", "))
		return issueops.ApplyItem{}, &res
	}

	// The tag and the payloads have to agree in both directions: a kind with no
	// payload is an item that does nothing, and a payload the kind does not name
	// is an item whose two halves disagree.
	var present []string
	for _, member := range []string{"create", "update", "close", "dep_add"} {
		if _, ok := raw[member]; ok {
			present = append(present, member)
		}
	}
	switch {
	case len(present) == 0:
		res := InvalidArgument(prefix+"."+named, ReasonInvalidValue,
			"an item of kind `"+kindText+"` must carry its `"+named+"` payload")
		return issueops.ApplyItem{}, &res
	case len(present) > 1:
		res := InvalidArgument(prefix+"."+present[1], ReasonInvalidValue,
			"an item carries exactly one payload; this one carries "+strings.Join(present, " and "))
		return issueops.ApplyItem{}, &res
	case present[0] != named:
		res := InvalidArgument(prefix+"."+present[0], ReasonInvalidValue,
			"this item is kind `"+kindText+"` but carries the `"+present[0]+"` payload; the two must name the same verb")
		return issueops.ApplyItem{}, &res
	}

	payload, res := applyObjectMember(raw, prefix+".", named)
	if res != nil {
		return issueops.ApplyItem{}, res
	}
	item := issueops.ApplyItem{Kind: kind}
	payloadPrefix := prefix + "." + named + "."
	switch kind {
	case issueops.ItemCreate:
		create, res := applyCreateItem(payloadPrefix, raw[named], payload)
		if res != nil {
			return issueops.ApplyItem{}, res
		}
		item.Create = create
	case issueops.ItemUpdate:
		update, res := applyUpdateItem(payloadPrefix, raw[named], payload)
		if res != nil {
			return issueops.ApplyItem{}, res
		}
		item.Update = update
	case issueops.ItemClose:
		closeItem, res := applyCloseItem(payloadPrefix, payload)
		if res != nil {
			return issueops.ApplyItem{}, res
		}
		item.Close = closeItem
	case issueops.ItemDepAdd:
		depAdd, res := applyDepAddItem(payloadPrefix, payload)
		if res != nil {
			return issueops.ApplyItem{}, res
		}
		item.DepAdd = depAdd
	}
	return item, nil
}

// applyCreateItem projects a create payload onto the role's create item.
//
// It decodes into the GENERATED struct after the raw member check, which is
// what makes a member's type the DOCUMENT's type: `priority: "high"` is refused
// here rather than reaching a role that would have to guess what the caller
// meant.
func applyCreateItem(prefix string, encoded json.RawMessage, raw map[string]json.RawMessage) (*issueops.CreateItem, *Result) {
	refuse := func(member, detail string) *Result {
		res := InvalidArgument(applyParam(prefix, member), ReasonInvalidValue, detail)
		return &res
	}
	if offender, unknown := unknownMember(raw, applyCreateItemMembers); unknown {
		return nil, applyUnknownMember(prefix, offender, applyCreateItemMembers)
	}
	var wire apigen.ApplyCreateItem
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return nil, refuse("", "a `create` member carries the wrong JSON type")
	}
	if strings.TrimSpace(wire.Title) == "" {
		return nil, refuse("title", "`title` is required and must not be blank")
	}
	// The role validates the type, the status and the id prefix against the
	// workspace's own configuration, which this server cannot read without a
	// transaction; what is checked here is only what the schema declares. A
	// SLICE and not a map, so an item breaking two rules always names the same
	// offender: `param` is what a client dispatches on and it must not depend on
	// map order.
	for _, bounded := range []struct {
		member string
		value  *string
	}{
		{"title", &wire.Title}, {"id", wire.Id}, {"key", wire.Key},
		{"issue_type", wire.IssueType}, {"status", wire.Status},
		{"assignee", wire.Assignee}, {"owner", wire.Owner},
		{"external_ref", wire.ExternalRef}, {"sender", wire.Sender},
	} {
		if res := applyBoundedText(prefix, bounded.member, bounded.value); res != nil {
			return nil, res
		}
	}
	if wire.Ephemeral != nil && *wire.Ephemeral && wire.NoHistory != nil && *wire.NoHistory {
		return nil, refuse("no_history", "`ephemeral` and `no_history` select different retention modes; send one")
	}

	issue := &types.Issue{
		ID:                 derefString(wire.Id),
		Title:              wire.Title,
		Description:        derefString(wire.Description),
		Design:             derefString(wire.Design),
		AcceptanceCriteria: derefString(wire.AcceptanceCriteria),
		Notes:              derefString(wire.Notes),
		Status:             types.Status(derefString(wire.Status)),
		IssueType:          types.IssueType(derefString(wire.IssueType)),
		Assignee:           derefString(wire.Assignee),
		Owner:              derefString(wire.Owner),
		EstimatedMinutes:   wire.EstimatedMinutes,
		ExternalRef:        wire.ExternalRef,
		DueAt:              wire.DueAt,
		DeferUntil:         wire.DeferUntil,
		Sender:             derefString(wire.Sender),
		Metadata:           wire.Metadata,
	}
	if wire.Priority != nil {
		if *wire.Priority < 0 || *wire.Priority > 4 {
			return nil, refuse("priority", fmt.Sprintf("`priority` is %d; the range is 0 to 4", *wire.Priority))
		}
		issue.Priority = *wire.Priority
	}
	if wire.Labels != nil {
		if res := applyBoundedLabels(prefix, "labels", *wire.Labels); res != nil {
			return nil, res
		}
		issue.Labels = *wire.Labels
	}
	if wire.Ephemeral != nil {
		issue.Ephemeral = *wire.Ephemeral
	}
	if wire.NoHistory != nil {
		issue.NoHistory = *wire.NoHistory
	}

	item := &issueops.CreateItem{Key: derefString(wire.Key), Issue: issue}
	refs, res := applyMetadataRefs(prefix, raw)
	if res != nil {
		return nil, res
	}
	item.MetadataRefs = refs
	return item, nil
}

// applyMetadataRefs decodes the one member whose keys may reach FORWARD. The
// refs are read raw rather than off the generated map so an unknown member
// inside one is refused by name like every other one.
func applyMetadataRefs(prefix string, raw map[string]json.RawMessage) (map[string]issueops.Ref, *Result) {
	member, ok := raw["metadata_refs"]
	if !ok {
		return nil, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(member, &entries); err != nil || entries == nil {
		res := InvalidArgument(prefix+"metadata_refs", ReasonInvalidValue,
			"`metadata_refs` must be an object whose values are refs")
		return nil, &res
	}
	refs := make(map[string]issueops.Ref, len(entries))
	for key, encoded := range entries {
		ref, res := applyRef(prefix+"metadata_refs."+key, encoded)
		if res != nil {
			return nil, res
		}
		refs[key] = ref
	}
	return refs, nil
}

// applyUpdateItem projects an update payload onto the role's update item.
func applyUpdateItem(prefix string, encoded json.RawMessage, raw map[string]json.RawMessage) (*issueops.UpdateItem, *Result) {
	if offender, unknown := unknownMember(raw, applyUpdateItemMembers); unknown {
		return nil, applyUnknownMember(prefix, offender, applyUpdateItemMembers)
	}
	var wire apigen.ApplyUpdateItem
	if err := json.Unmarshal(encoded, &wire); err != nil {
		res := InvalidArgument(applyParam(prefix, ""), ReasonInvalidValue, "an `update` member carries the wrong JSON type")
		return nil, &res
	}
	target, res := applyRequiredRef(prefix, raw, "target")
	if res != nil {
		return nil, res
	}
	for _, bounded := range []struct {
		member string
		value  *string
	}{{"expected_status", wire.ExpectedStatus}, {"expected_assignee", wire.ExpectedAssignee}} {
		if res := applyBoundedText(prefix, bounded.member, bounded.value); res != nil {
			return nil, res
		}
	}
	patchRaw, res := applyObjectMember(raw, prefix, "patch")
	if res != nil {
		return nil, res
	}
	patch, res := applyPatch(prefix+"patch.", raw["patch"], patchRaw)
	if res != nil {
		return nil, res
	}

	item := &issueops.UpdateItem{Target: target, Patch: patch, ExpectedVersion: wire.ExpectedVersion}
	if wire.ExpectedStatus != nil {
		status := issueops.Status(*wire.ExpectedStatus)
		item.ExpectedStatus = &status
	}
	item.ExpectedAssignee = wire.ExpectedAssignee
	if wire.ForceClosePolicy != nil {
		item.ForceClosePolicy = *wire.ForceClosePolicy
	}
	if wire.ForceAssigneeTransfer != nil {
		item.ForceAssigneeTransfer = *wire.ForceAssigneeTransfer
	}
	return item, nil
}

// applyPatch projects the decoded `patch` member onto the role's IssuePatch.
//
// MEMBER PRESENCE IS THE SIGNAL, which is why this level is read as raw members
// too: a member present sets the role's Field, a member absent leaves the field
// untouched, and the generated struct cannot tell those apart because it models
// both as a nil pointer. Explicit `null` is a third state read straight off the
// raw bytes — a clear on the four nullable members, and a 400 naming the member
// everywhere else.
func applyPatch(prefix string, encoded json.RawMessage, raw map[string]json.RawMessage) (issueops.IssuePatch, *Result) {
	refuse := func(member, detail string) (issueops.IssuePatch, *Result) {
		res := InvalidArgument(applyParam(prefix, member), ReasonInvalidValue, detail)
		return issueops.IssuePatch{}, &res
	}
	if len(raw) == 0 {
		// A write that writes nothing is a client bug, not a no-op to answer.
		return refuse("", "`patch` must carry at least one field; an update that updates nothing is refused rather than answered")
	}
	if offender, unknown := unknownMember(raw, applyPatchMembers); unknown {
		return issueops.IssuePatch{}, applyUnknownMember(prefix, offender, applyPatchMembers)
	}
	// Explicit null, before any typed decode: unmarshaling null into *T yields
	// nil, which is indistinguishable from the member being absent, so a null on
	// a non-nullable member would otherwise slide through as "untouched" — a
	// write the client asked for and the server silently dropped.
	for name, value := range raw {
		if isJSONNull(value) && !applyNullablePatchMembers[name] {
			return refuse(name, "`"+name+"` is not nullable; omit it to leave the field unchanged")
		}
	}

	var wire apigen.ApplyPatchBody
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return refuse("", "a `patch` member carries the wrong JSON type")
	}
	for _, bounded := range []struct {
		member string
		value  *string
	}{
		{"issue_type", wire.IssueType}, {"status", wire.Status},
		{"assignee", wire.Assignee}, {"owner", wire.Owner},
		{"external_ref", wire.ExternalRef},
	} {
		if res := applyBoundedText(prefix, bounded.member, bounded.value); res != nil {
			return issueops.IssuePatch{}, res
		}
	}

	patch := issueops.IssuePatch{}
	set := func(name string) bool { _, present := raw[name]; return present }

	if set("title") {
		title := *wire.Title
		if strings.TrimSpace(title) == "" {
			return refuse("title", "`title` must not be blank")
		}
		if res := applyBoundedText(prefix, "title", &title); res != nil {
			return issueops.IssuePatch{}, res
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
	// The role refuses both together too; refusing here keeps the 400 a
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
		patch.IssueType = issueops.Field[issueops.IssueType]{Set: true, Value: issueops.IssueType(*wire.IssueType)}
	}
	if set("status") {
		patch.Status = issueops.Field[issueops.Status]{Set: true, Value: issueops.Status(*wire.Status)}
	}
	if set("assignee") {
		patch.Assignee = issueops.Field[string]{Set: true, Value: *wire.Assignee}
	}
	if set("owner") {
		patch.Owner = issueops.Field[string]{Set: true, Value: *wire.Owner}
	}
	// The four nullable members. Set is true whenever the member is present; the
	// VALUE is a nil pointer for an explicit null, which is how a clear reaches
	// the role.
	if set("estimated_minutes") {
		patch.EstimatedMinutes = issueops.Field[*int]{Set: true, Value: wire.EstimatedMinutes}
	}
	if set("external_ref") {
		patch.ExternalRef = issueops.Field[*string]{Set: true, Value: wire.ExternalRef}
	}
	if set("due_at") {
		patch.DueAt = issueops.Field[*time.Time]{Set: true, Value: wire.DueAt}
	}
	if set("defer_until") {
		patch.DeferUntil = issueops.Field[*time.Time]{Set: true, Value: wire.DeferUntil}
	}
	if set("labels") {
		labels, res := applyLabelPatch(prefix+"labels.", raw["labels"])
		if res != nil {
			return issueops.IssuePatch{}, res
		}
		patch.Labels = labels
	}
	if set("metadata") {
		metadata, res := applyMetadataPatch(prefix+"metadata.", raw["metadata"])
		if res != nil {
			return issueops.IssuePatch{}, res
		}
		patch.Metadata = metadata
	}
	return patch, nil
}

// applyLabelPatch projects the label edit, which is the FULL patch here rather
// than PATCH /v0/beads/issues/{id}'s complete replacement: a plan edits a label
// set it did not compose, and replacement would mean reading it back first.
func applyLabelPatch(prefix string, encoded json.RawMessage) (issueops.LabelPatch, *Result) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil || raw == nil {
		res := InvalidArgument(applyParam(prefix, ""), ReasonInvalidValue, "`labels` must be a JSON object")
		return issueops.LabelPatch{}, &res
	}
	if offender, unknown := unknownMember(raw, applyLabelPatchMembers); unknown {
		return issueops.LabelPatch{}, applyUnknownMember(prefix, offender, applyLabelPatchMembers)
	}
	var wire apigen.ApplyLabelPatch
	if err := json.Unmarshal(encoded, &wire); err != nil {
		res := InvalidArgument(applyParam(prefix, ""), ReasonInvalidValue, "a `labels` member carries the wrong JSON type")
		return issueops.LabelPatch{}, &res
	}

	patch := issueops.LabelPatch{}
	for _, edit := range []struct {
		member string
		value  *[]string
		assign func([]string)
	}{
		{"replace", wire.Replace, func(v []string) {
			// Presence is the signal, and an EMPTY array clears every label —
			// which is why this is a Field rather than a plain slice.
			patch.Replace = issueops.Field[[]string]{Set: true, Value: v}
		}},
		{"add", wire.Add, func(v []string) { patch.Add = v }},
		{"remove", wire.Remove, func(v []string) { patch.Remove = v }},
	} {
		if _, present := raw[edit.member]; !present {
			continue
		}
		if edit.value == nil {
			res := InvalidArgument(prefix+edit.member, ReasonInvalidValue, "`"+edit.member+"` must be an array of strings")
			return issueops.LabelPatch{}, &res
		}
		if res := applyBoundedLabels(prefix, edit.member, *edit.value); res != nil {
			return issueops.LabelPatch{}, res
		}
		edit.assign(*edit.value)
	}
	return patch, nil
}

// applyMetadataPatch projects the metadata edit.
//
// `set` IS READ RAW AND NEVER THROUGH THE GENERATED MAP, and that is a
// correctness fix rather than a shortcut: the generator models a map value as
// *MetadataValue, and encoding/json answers a JSON null against a pointer by
// setting the pointer to nil before any UnmarshalJSON runs — so a caller
// writing a key to JSON null would have it collapse into an absent key, which
// on this plane is the opposite request. The raw bytes carry the literal.
func applyMetadataPatch(prefix string, encoded json.RawMessage) (issueops.MetadataPatch, *Result) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil || raw == nil {
		res := InvalidArgument(applyParam(prefix, ""), ReasonInvalidValue, "`metadata` must be a JSON object")
		return issueops.MetadataPatch{}, &res
	}
	if offender, unknown := unknownMember(raw, applyMetadataPatchMembers); unknown {
		return issueops.MetadataPatch{}, applyUnknownMember(prefix, offender, applyMetadataPatchMembers)
	}

	patch := issueops.MetadataPatch{}
	if value, present := raw["replace"]; present {
		if len(raw) > 1 {
			res := InvalidArgument(prefix+"replace", ReasonInvalidValue,
				"`replace` writes the whole document and cannot be combined with `merge`, `set` or `unset`")
			return issueops.MetadataPatch{}, &res
		}
		patch.Replace = issueops.Field[json.RawMessage]{Set: true, Value: applyRawCopy(value)}
	}
	if value, present := raw["merge"]; present {
		patch.Merge = issueops.Field[json.RawMessage]{Set: true, Value: applyRawCopy(value)}
	}
	if value, present := raw["set"]; present {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(value, &entries); err != nil || entries == nil {
			res := InvalidArgument(prefix+"set", ReasonInvalidValue, "`set` must be an object of metadata values")
			return issueops.MetadataPatch{}, &res
		}
		patch.Set = make(map[string]json.RawMessage, len(entries))
		for key, keyValue := range entries {
			patch.Set[key] = applyRawCopy(keyValue)
		}
	}
	if value, present := raw["unset"]; present {
		var keys []string
		if err := json.Unmarshal(value, &keys); err != nil || keys == nil {
			res := InvalidArgument(prefix+"unset", ReasonInvalidValue, "`unset` must be an array of strings")
			return issueops.MetadataPatch{}, &res
		}
		patch.Unset = keys
	}
	return patch, nil
}

// applyCloseItem projects a close payload onto the role's close item.
func applyCloseItem(prefix string, raw map[string]json.RawMessage) (*issueops.CloseItem, *Result) {
	if offender, unknown := unknownMember(raw, applyCloseItemMembers); unknown {
		return nil, applyUnknownMember(prefix, offender, applyCloseItemMembers)
	}
	target, res := applyRequiredRef(prefix, raw, "target")
	if res != nil {
		return nil, res
	}
	reason, res := applyTextMember(raw, prefix, "reason")
	if res != nil {
		return nil, res
	}
	session, res := applyTextMember(raw, prefix, "session")
	if res != nil {
		return nil, res
	}
	force, res := applyBoolMember(raw, prefix, "force")
	if res != nil {
		return nil, res
	}
	expectedVersion, res := applyVersionGuardMember(raw, prefix)
	if res != nil {
		return nil, res
	}
	// There is deliberately no `expected_status` here: a close is idempotent, so
	// a guard spelled to refuse an already-closed row asks for a refusal where
	// this verb answers with a no-op. See the schema.
	return &issueops.CloseItem{
		Target:          target,
		Reason:          reason,
		Session:         session,
		Force:           force,
		ExpectedVersion: expectedVersion,
	}, nil
}

// applyDepAddItem projects a dep_add payload onto the role's edge item.
//
// The gate normalization a waits-for edge gets, and the refusal a bad gate
// earns, belong to the role: this checks only that the type IS a value the
// column can hold, never that it is a member of a known-types list, because the
// edge vocabulary is OPEN and a workspace's own type must pass.
func applyDepAddItem(prefix string, raw map[string]json.RawMessage) (*issueops.DepAddItem, *Result) {
	if offender, unknown := unknownMember(raw, applyDepAddItemMembers); unknown {
		return nil, applyUnknownMember(prefix, offender, applyDepAddItemMembers)
	}
	source, res := applyRequiredRef(prefix, raw, "source")
	if res != nil {
		return nil, res
	}
	target, res := applyRequiredRef(prefix, raw, "target")
	if res != nil {
		return nil, res
	}
	edgeType, res := applyRequiredText(raw, prefix, "type")
	if res != nil {
		return nil, res
	}
	if !types.DependencyType(edgeType).IsValid() {
		res := InvalidArgument(prefix+"type", ReasonInvalidValue,
			fmt.Sprintf("`type` must be 1 to %d characters", types.MaxDependencyTypeLen))
		return nil, &res
	}
	item := &issueops.DepAddItem{
		Source: source,
		Target: target,
		Type:   types.DependencyType(edgeType),
	}
	// The blob travels as the bytes the caller sent. The role is the single
	// definition of what it will accept, and a re-encode here would be a second
	// one — the metadata compare-and-set's rule, applied to an edge.
	if metadata, present := raw["metadata"]; present {
		item.Metadata = string(metadata)
	}
	return item, nil
}

// applyRequiredRef reads one ref member that must be present.
func applyRequiredRef(prefix string, raw map[string]json.RawMessage, member string) (issueops.Ref, *Result) {
	encoded, ok := raw[member]
	if !ok {
		res := InvalidArgument(prefix+member, ReasonInvalidValue, "`"+member+"` is required")
		return issueops.Ref{}, &res
	}
	return applyRef(prefix+member, encoded)
}

// applyRef decodes one ref and applies the exactly-one rule the schema cannot
// state: `oneOf` is unavailable in this document, so a client can construct a
// ref with both members or neither and only the server can say no.
//
// Whether a key RESOLVES — and whether it reaches backward far enough — is the
// role's question, because only the role can see the whole request's key index.
func applyRef(param string, encoded json.RawMessage) (issueops.Ref, *Result) {
	refuse := func(detail string) (issueops.Ref, *Result) {
		res := InvalidArgument(param, ReasonInvalidValue, detail)
		return issueops.Ref{}, &res
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil || raw == nil {
		return refuse("a ref must be a JSON object naming a `key` or an `id`")
	}
	if offender, unknown := unknownMember(raw, applyRefMembers); unknown {
		return issueops.Ref{}, applyUnknownMember(param+".", offender, applyRefMembers)
	}
	var wire apigen.Ref
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return refuse("a ref's `key` and `id` must be strings")
	}
	for _, bounded := range []struct {
		member string
		value  *string
	}{{"key", wire.Key}, {"id", wire.Id}} {
		if res := applyBoundedText(param+".", bounded.member, bounded.value); res != nil {
			return issueops.Ref{}, res
		}
	}
	key, id := derefString(wire.Key), derefString(wire.Id)
	switch {
	case key == "" && id == "":
		return refuse("a ref must name a `key` or an `id`")
	case key != "" && id != "":
		return refuse("a ref names a `key` or an `id`, not both")
	}
	return issueops.Ref{Key: key, ID: id}, nil
}

// applyBatchResponse projects the role's result onto the LEAN wire result.
//
// ItemResult.Issue is deliberately dropped. The Go contract carries a post-item
// snapshot because the completion hooks over this role hand a script the row it
// is being told about — and hooks never fire on this surface, so the snapshot
// has no consumer here and a hundred hydrated issues would be a response an
// order of magnitude larger than the request that produced it. See
// issueops.ItemResult.Issue.
func applyBatchResponse(result issueops.ApplyBatchResult) apigen.ApplyBatchResponse {
	// Allocated rather than passed through: `keys` is a required member and a
	// request whose creates named nothing must answer with an empty object, not
	// the `null` a nil map marshals to.
	keys := make(map[string]string, len(result.Keys))
	for key, id := range result.Keys {
		keys[key] = id
	}
	items := make([]apigen.ApplyItemResult, 0, len(result.Items))
	for _, item := range result.Items {
		wire := apigen.ApplyItemResult{
			Kind:     apigen.ApplyItemResultKind(item.Kind),
			IssueId:  item.IssueID,
			Changed:  item.Changed,
			Revision: item.RowVersion,
		}
		if item.DependsOnID != "" {
			dependsOn := item.DependsOnID
			wire.DependsOnId = &dependsOn
		}
		items = append(items, wire)
	}
	return apigen.ApplyBatchResponse{Keys: keys, Items: items}
}

// failApplyBatch answers a refused plan, mapping the role's TYPED refusals onto
// the frozen codes and naming the item each one came from.
//
// EVERY BRANCH READS THE ROLE'S TYPED FIELDS, never its prose. That matters more
// here than anywhere else on the surface: the request is all or nothing, so
// there is no per-item result array a client could find the offender in, and
// the `item_*` problem members are the only place it exists. They come from
// *issueops.ItemError, raised inside the transaction that refused.
//
// Nothing here quotes a role message. 4xx details on this surface reflect the
// caller's own input back rather than server internals, and the real error goes
// to the log with the request id.
func (s *Server) failApplyBatch(w http.ResponseWriter, r *http.Request, request issueops.ApplyBatchRequest, err error) {
	var (
		refErr       *issueops.RefError
		itemErr      *issueops.ItemError
		typeConflict *issueops.DependencyTypeConflictError
		hierarchy    *issueops.DependencyHierarchyConflictError
		endpoint     *issueops.DependencyEndpointNotFoundError
		openChildren *issueops.CloseOpenChildrenError
		claimed      *issueops.ClaimConflictError
	)
	// The item wrapper is read once and shared by every branch below: a refusal
	// that names an item names it whatever the inner sentinel turns out to be.
	errors.As(err, &itemErr)
	at := func(res Result, member string) Result {
		if itemErr == nil {
			// A refusal the role raised without naming an item — the request's
			// own, or a wrapper that lost the identity. `param` still names the
			// member that carries them all, because the document promises one on
			// every 400 but the body that failed to parse.
			if member != "" && res.Problem.Param == nil {
				param := "items"
				res.Problem.Param = &param
			}
			return res
		}
		res = res.WithBatchItem(itemErr.Index, string(itemErr.Kind), itemErr.Key, itemErr.IssueID)
		if member != "" {
			param := applyItemParam(itemErr.Index, string(itemErr.Kind)+"."+member)
			res.Problem.Param = &param
		}
		return res
	}

	switch {
	// An unresolvable ref is a *RefError and it is the one 400 that carries a
	// discriminator: `declared_later` tells an ORDERING mistake from a typo, and
	// it is emitted in both polarities so an absent member cannot be misread as
	// false. It is matched before ErrValidation because it unwraps to it.
	case errors.As(err, &refErr):
		s.refusedApplyBatch(r, err)
		s.fail(w, r, InvalidArgument(applyRefParam(request, refErr), ReasonInvalidValue,
			applyRefDetail(refErr)).
			WithBatchItem(refErr.Index, applyKindAt(request, refErr.Index), refErr.Key, "").
			WithDeclaredLater(refErr.DeclaredLater))

	case errors.As(err, &typeConflict):
		s.fail(w, r, at(newResult(CodeDependencyExists,
			"this pair already carries an edge of a different type; remove it before re-adding").
			WithDependencyTypeConflict(typeConflict.ExistingType, typeConflict.RequestedType), ""))

	case errors.As(err, &hierarchy):
		s.fail(w, r, at(newResult(CodeDependencyCycle,
			"a blocking edge against the issue's own ancestor or descendant would never clear").
			WithHierarchyConflict(hierarchy.IssueID, hierarchy.BlockerID, hierarchy.BlockerIsAncestor), ""))

	case errors.Is(err, issueops.ErrDependencyCycle):
		// No hierarchy members: this is the plain scheduling cycle, and their
		// ABSENCE is what tells a client which of the two refusals it got. It may
		// come from the per-edge probe or from the END GATE, which is the only
		// place an edge that is legal alone and illegal in the graph this request
		// built is caught.
		s.fail(w, r, at(newResult(CodeDependencyCycle,
			"the plan's edges would create a dependency cycle; nothing was written"), ""))

	// The edge existence refusals are 400s rather than 404s, conforming to
	// POST /v0/beads/dependencies:add: an edge describes a relation rather than
	// acting on a row, and its target may legitimately be an "external:"
	// reference this database holds nothing for.
	case errors.As(err, &endpoint):
		s.refusedApplyBatch(r, err)
		member, detail := "source", "an edge's source names no issue in this workspace; nothing was written"
		if errors.Is(err, issueops.ErrDependencyTargetNotFound) {
			member, detail = "target", "an edge's target names no issue this workspace can see; nothing was written"
		}
		s.fail(w, r, at(InvalidArgument("", ReasonInvalidValue, detail), member))

	case errors.Is(err, issueops.ErrSelfDependency):
		s.refusedApplyBatch(r, err)
		s.fail(w, r, at(InvalidArgument("", ReasonInvalidValue, "an issue cannot depend on itself"), "target"))

	// An occupied explicit id is a 409: the body is well-formed and stays
	// well-formed, and what refuses it is STATE the client cannot see without
	// reading it — so recovery is to look at that state (adopt the row, pick
	// another id, or stop) rather than to fix a malformed request. The identical
	// body succeeded before the id was taken. Matched before ErrValidation
	// because the create path wraps both.
	case errors.Is(err, storage.ErrAlreadyExists):
		s.refusedApplyBatch(r, err)
		s.fail(w, r, at(newResult(CodeAlreadyExists,
			"a create item's `id` already names a stored row; nothing was written"), "id"))

	case errors.Is(err, issueops.ErrCloseOpenChildren):
		res := at(newResult(CodeNotClosable,
			"an item closes an issue with open children; close them first, or send the item's force flag"), "")
		if errors.As(err, &openChildren) {
			res = res.WithOpenChildren(openChildren.OpenChildren)
		}
		s.fail(w, r, res)

	case errors.Is(err, issueops.ErrCloseBlocked):
		s.fail(w, r, at(newResult(CodeNotClosable,
			"an item closes a blocked issue; clear the blocker, or send the item's force flag"), ""))

	case errors.Is(err, storage.ErrAlreadyClaimed):
		res := at(newResult(CodeAlreadyClaimed,
			"an update transfers work away from a live foreign owner; send `force_assignee_transfer`, or guard with `expected_assignee`"), "assignee")
		if errors.As(err, &claimed) {
			if claimed.Assignee != "" {
				res = res.WithAssignee(claimed.Assignee)
			}
			if claimed.Status != "" {
				res = res.WithIssueStatus(string(claimed.Status))
			}
		}
		s.fail(w, r, res)

	case errors.Is(err, issueops.ErrVersionMismatch),
		errors.Is(err, issueops.ErrStatusMismatch),
		errors.Is(err, issueops.ErrAssigneeMismatch):
		s.fail(w, r, applyPreconditionResult(request, itemErr, err, at))

	// A target an update or a close NAMED is a resource this request failed to
	// address, which is POST /v0/beads/issues:delete's 404 rather than the edge
	// refusal's 400 above.
	case errors.Is(err, storage.ErrNotFound):
		s.fail(w, r, at(NotFound(), ""))

	case errors.Is(err, storage.ErrValidation):
		s.refusedApplyBatch(r, err)
		s.fail(w, r, at(InvalidArgument("items", ReasonInvalidValue,
			"an item was refused by this workspace's own validation; nothing was written"), ""))

	default:
		s.failErr(w, r, err)
	}
}

// applyPreconditionResult builds the 409 for a guard that missed, naming the
// guard member and echoing what the request asked for.
//
// The expected value comes from the REQUEST rather than from a read, and the
// observed value is absent, because this operation's refusal rolled its
// transaction back: a read afterwards would describe a row the refusal never
// saw. See PreconditionFailed.
func applyPreconditionResult(request issueops.ApplyBatchRequest, itemErr *issueops.ItemError, err error, at func(Result, string) Result) Result {
	res := PreconditionFailed()
	switch {
	case errors.Is(err, issueops.ErrVersionMismatch):
		res = at(res, "expected_version")
		if expected := applyExpectedVersion(request, itemErr); expected != nil {
			res = res.WithExpectedVersion(*expected)
		}
	case errors.Is(err, issueops.ErrStatusMismatch):
		res = at(res, "expected_status")
		if item := applyUpdateAt(request, itemErr); item != nil && item.ExpectedStatus != nil {
			res = res.WithExpectedStatus(string(*item.ExpectedStatus))
		}
	default:
		res = at(res, "expected_assignee")
		if item := applyUpdateAt(request, itemErr); item != nil && item.ExpectedAssignee != nil {
			res = res.WithExpectedAssignee(*item.ExpectedAssignee)
		}
	}
	return res
}

// applyExpectedVersion reads the version guard off the refused item. Both an
// update and a close carry one, which is why this is not applyUpdateAt's caller.
func applyExpectedVersion(request issueops.ApplyBatchRequest, itemErr *issueops.ItemError) *int64 {
	item := applyItemAt(request, itemErr)
	switch {
	case item == nil:
		return nil
	case item.Update != nil:
		return item.Update.ExpectedVersion
	case item.Close != nil:
		return item.Close.ExpectedVersion
	}
	return nil
}

// applyUpdateAt reads the refused item's update payload, or nil when the
// refusal named no item or named one of another kind.
func applyUpdateAt(request issueops.ApplyBatchRequest, itemErr *issueops.ItemError) *issueops.UpdateItem {
	if item := applyItemAt(request, itemErr); item != nil {
		return item.Update
	}
	return nil
}

// applyItemAt reads the refused item out of the request the handler built.
//
// It is bounds-checked rather than trusted: the index comes from the role, and
// a server that indexed a slice on a number another package computed would turn
// a contract drift into a panic on a live request.
func applyItemAt(request issueops.ApplyBatchRequest, itemErr *issueops.ItemError) *issueops.ApplyItem {
	if itemErr == nil || itemErr.Index < 0 || itemErr.Index >= len(request.Items) {
		return nil
	}
	return &request.Items[itemErr.Index]
}

// applyKindAt names the kind of the item at index, for a refusal that carries
// an index but no kind of its own.
func applyKindAt(request issueops.ApplyBatchRequest, index int) string {
	if index < 0 || index >= len(request.Items) {
		return ""
	}
	return string(request.Items[index].Kind)
}

// applyRefParam spells `param` for an unresolvable ref.
//
// RefError.Member is DIAGNOSTIC PROSE — "target", "source", or "metadata_ref
// <key>" — rather than a vocabulary, so it is mapped onto the document's own
// member names rather than published. Anything that is not one of the two
// addressing refs is a metadata ref, and the whole member is named because the
// key inside it came from the caller's own object.
func applyRefParam(request issueops.ApplyBatchRequest, refErr *issueops.RefError) string {
	kind := applyKindAt(request, refErr.Index)
	if kind == "" {
		return applyItemParam(refErr.Index, "")
	}
	member := "metadata_refs"
	if refErr.Member == "target" || refErr.Member == "source" {
		member = refErr.Member
	}
	return applyItemParam(refErr.Index, kind+"."+member)
}

// applyRefDetail says which of the two key diagnoses this is, in the server's
// own words. The machine-readable half is `declared_later`.
func applyRefDetail(refErr *issueops.RefError) string {
	if refErr.DeclaredLater {
		return "this ref names a key declared LATER in the request; a key reaches backward only, so move the item that declares it earlier"
	}
	return "this ref names a key no item in the request declares"
}

// refusedApplyBatch records the real refusal for the operator. The 4xx path does
// not log by default, and the role's message is the only place the underlying
// reason survives once the response carries the server's own words.
func (s *Server) refusedApplyBatch(r *http.Request, err error) {
	s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
}

// applyItemParam spells the `param` member for a refusal inside `items`, so a
// client dispatching on it learns WHICH item and WHICH member.
func applyItemParam(index int, member string) string {
	param := fmt.Sprintf("items[%d]", index)
	if member == "" {
		return param
	}
	return param + "." + member
}

// applyParam joins a level's dotted prefix to a member, and names the LEVEL
// itself when the member is empty — a refusal about the whole object rather
// than about one of its members. Without the trim that case would spell a
// `param` ending in a dot, which is a member name no schema declares.
func applyParam(prefix, member string) string {
	if member == "" {
		return strings.TrimSuffix(prefix, ".")
	}
	return prefix + member
}

// applyUnknownMember answers an unknown member below the request level, where
// the offender's name has to be qualified by the path that reached it.
func applyUnknownMember(prefix, offender string, allowed []string) *Result {
	res := InvalidArgument(prefix+offender, ReasonUnknownParameter,
		"this member carries "+strings.Join(allowed, ", ")+" and nothing else")
	return &res
}

// applyKindNames lists the tag vocabulary in the document's order, for a
// refusal that has to spell it.
func applyKindNames() []string {
	return []string{"create", "update", "close", "dep_add"}
}

// applyObjectMember reads a required member that must be a JSON object, as raw
// members so the level below it can be checked by name.
func applyObjectMember(raw map[string]json.RawMessage, prefix, member string) (map[string]json.RawMessage, *Result) {
	encoded, ok := raw[member]
	if !ok {
		res := InvalidArgument(prefix+member, ReasonInvalidValue, "`"+member+"` is required")
		return nil, &res
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &out); err != nil || out == nil {
		res := InvalidArgument(prefix+member, ReasonInvalidValue, "`"+member+"` must be a JSON object")
		return nil, &res
	}
	return out, nil
}

// applyRequiredText reads a required string member, bounded the way every
// stored string on this surface is.
func applyRequiredText(raw map[string]json.RawMessage, prefix, member string) (string, *Result) {
	if _, ok := raw[member]; !ok {
		res := InvalidArgument(prefix+member, ReasonInvalidValue, "`"+member+"` is required")
		return "", &res
	}
	value, res := applyTextMember(raw, prefix, member)
	if res != nil {
		return "", res
	}
	if value == "" {
		res := InvalidArgument(prefix+member, ReasonInvalidValue, "`"+member+"` must not be empty")
		return "", &res
	}
	return value, nil
}

// applyTextMember, applyBoolMember and applyVersionGuardMember are the pure twins of
// Server.storedTextMember and Server.booleanMember, with their rules unchanged:
// an absent member is the zero value the role reads as "not supplied", an
// explicit `null` is a 400 naming the member rather than a third state, and a
// string is bounded by what storage holds and refused for control characters —
// because these values land in columns that renderers print.
//
// They are functions rather than methods because this body nests four levels
// deep, and threading a ResponseWriter through every level so each could fail
// in place would put the response machinery in the middle of the projection.
func applyTextMember(raw map[string]json.RawMessage, prefix, member string) (string, *Result) {
	encoded, ok := raw[member]
	if !ok {
		return "", nil
	}
	var value *string
	if err := json.Unmarshal(encoded, &value); err != nil || value == nil {
		res := InvalidArgument(prefix+member, ReasonInvalidValue, "`"+member+"` must be a string")
		return "", &res
	}
	if res := applyBoundedText(prefix, member, value); res != nil {
		return "", res
	}
	return *value, nil
}

func applyBoolMember(raw map[string]json.RawMessage, prefix, member string) (bool, *Result) {
	encoded, ok := raw[member]
	if !ok {
		return false, nil
	}
	var value *bool
	if err := json.Unmarshal(encoded, &value); err != nil || value == nil {
		res := InvalidArgument(prefix+member, ReasonInvalidValue, "`"+member+"` must be a boolean")
		return false, &res
	}
	return *value, nil
}

// applyVersionGuardMember is the family's int64 reader, and it names the member
// itself rather than taking one.
//
// Its siblings above are generic because they read many members; this one has
// read exactly one on every operation that has ever published a 64-bit member —
// the row-version guard — so the member name lives in the function instead of
// at five call sites that could disagree about how to spell it. A second int64
// member would re-generalize it, which is a two-line change and not a reason to
// carry a parameter nothing varies.
//
// prefix stays, because a batch item's guard is reported qualified by the item
// that carried it where a single operation's is spelled bare.
func applyVersionGuardMember(raw map[string]json.RawMessage, prefix string) (*int64, *Result) {
	encoded, ok := raw[expectedVersionMember]
	if !ok {
		return nil, nil
	}
	var value *int64
	if err := json.Unmarshal(encoded, &value); err != nil || value == nil {
		res := InvalidArgument(prefix+expectedVersionMember, ReasonInvalidValue,
			"`"+expectedVersionMember+"` must be an integer")
		return nil, &res
	}
	return value, nil
}

// applyBoundedText applies the bounds a stored string carries wherever it is
// spelled, so every level of this body refuses the same values. A nil pointer is
// an absent member and passes.
func applyBoundedText(prefix, member string, value *string) *Result {
	if value == nil {
		return nil
	}
	refuse := func(detail string) *Result {
		res := InvalidArgument(prefix+member, ReasonInvalidValue, detail)
		return &res
	}
	switch {
	case types.CheckFieldLen(member, *value) != nil:
		return refuse(fmt.Sprintf("`%s` is %d characters; storage holds at most %d",
			member, utf8.RuneCountInString(*value), types.MaxFieldLen))
	case strings.ContainsFunc(*value, isControlChar):
		return refuse("`" + member + "` must not contain control characters")
	}
	return nil
}

// applyBoundedLabels applies the same bound to every entry of a label list.
func applyBoundedLabels(prefix, member string, labels []string) *Result {
	for i, label := range labels {
		if types.CheckFieldLen("label", label) != nil {
			res := InvalidArgument(prefix+member, ReasonInvalidValue,
				fmt.Sprintf("`%s[%d]` is %d characters; storage holds at most %d",
					member, i, utf8.RuneCountInString(label), types.MaxFieldLen))
			return &res
		}
	}
	return nil
}

// applyRawCopy copies a raw member rather than aliasing the decoded body, so
// nothing downstream can be surprised by the request buffer's lifetime.
func applyRawCopy(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(append([]byte(nil), raw...))
}
