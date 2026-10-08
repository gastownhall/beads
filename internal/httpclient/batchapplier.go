// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/batchapplier.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// httpBatchApplier serves issueops.BatchApplier over issues:batchApply — the
// widest write on this surface, and the only one that is ALL OR NOTHING across
// several verbs at once.
//
// WHAT MAKES IT DIFFERENT FROM ITS NEIGHBORS HERE, and what the port had to
// carry:
//
// THE ITEM IS A TAGGED UNION AND THE WIRE CANNOT ENFORCE THE TAG. The document
// uses no composition keyword, so an item is one object with a required `kind`
// and four optional payloads: nothing in any generated type stops a request
// carrying two payloads, or a payload the kind does not name, or a kind this
// client has never heard of. All four disagreements are refused HERE, before
// the dial, because the role's own contract calls each of them ErrValidation
// and because a kind added upstream must FAIL CLOSED rather than be dropped
// into a request that silently does less than the caller asked.
//
// THERE ARE NO PER-ITEM OUTCOMES TO WALK. batchClose answers a refused item
// inside a 200 and the role reads the outcome array; here a refused item took
// the whole transaction down, so every other item's outcome would be a
// statement about work that rolled back. The offender travels in the problem
// document's `item_*` members instead, read off the role's own typed
// *issueops.ItemError inside the refusing transaction, and rebuilt here so a
// caller classifies a remote refusal with the errors.As arm it already has.
//
// THE RESULT IS POSITIONAL, which is the hazard this file guards hardest. The
// wire answers one entry per requested item in request order, and a client that
// trusted that without checking would attribute one item's `changed` and
// `revision` to a different item's row — a misattribution no assertion about
// the row would catch, because both rows exist and both were written.
// decodeApplyBatchResult checks the length AND the kind at every index.
//
// THE SNAPSHOT STOPS AT THE WIRE. issueops.ItemResult carries a post-item
// *Issue and ApplyItemResult does not, deliberately (the role's own leaf says
// so: hooks never fire on this surface, and a hundred hydrated issues would be
// a response an order of magnitude larger than the request). So Issue is nil on
// every item this role answers with — ledger row L-apply-snapshot.
type httpBatchApplier struct {
	store *Store
	wire  WriteWire
}

var _ issueops.BatchApplier = (*httpBatchApplier)(nil)

// ApplyBatch dials POST /v0/beads/issues:batchApply.
func (b *httpBatchApplier) ApplyBatch(ctx context.Context, req issueops.ApplyBatchRequest) (result issueops.ApplyBatchResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError —
	// however deeply applyBatchBody/encodeApplyPatch nested it inside an
	// "items[%d]..." or "Issue.%s:" prefix — into *InexpressibleError so
	// errors.As(err, &unsupported) reaches *storage.ErrUnsupported, same as
	// inexpressible does for a read role. The original composite message
	// (including that prefix) is preserved in the decorated error's own text.
	defer func() { err = b.store.inexpressible("BatchApplier.ApplyBatch", err) }()
	if err := requireActor(req.Actor); err != nil {
		return issueops.ApplyBatchResult{}, err
	}
	// The two bounds the ROLE states, applied before the dial for the reason
	// every client-side validation here is applied before one: a request the
	// role's own contract calls invalid must not consume a write slot to be
	// told so. Neither is a wire divergence — the document publishes the same
	// hundred — and the cap REFUSES rather than splitting, because a split plan
	// is several transactions where the caller asked for one and the end gate
	// only ever sees a single request.
	if len(req.Items) == 0 {
		return issueops.ApplyBatchResult{}, invalid("a batch apply names no items")
	}
	if len(req.Items) > issueops.MaxApplyBatchItems {
		return issueops.ApplyBatchResult{}, invalid(
			"a batch apply carries %d items; the limit is %d per request", len(req.Items), issueops.MaxApplyBatchItems)
	}
	if err := b.refuseUnservedDepAddLineage(ctx, req); err != nil {
		return issueops.ApplyBatchResult{}, err
	}

	body, err := applyBatchBody(req)
	if err != nil {
		return issueops.ApplyBatchResult{}, err
	}
	resp, err := b.wire.ApplyBatch(ctx, body)
	if err != nil {
		return issueops.ApplyBatchResult{}, applyBatchRefusal(err)
	}
	return decodeApplyBatchResult(req, resp)
}

// refuseUnservedDepAddLineage scans the whole plan once, before the dial, for
// a dep_add item naming ThreadID, or HasSpawner on a waits-for edge (the one
// type the role reads it on; see depAddNamesSpawner). Neither member is served
// unless the handshake snapshot advertises wire.CapBatchApplyDepAddLineage:
// an older server has never heard of either and would answer them with its
// generic unknown-member 400 at best, or (once a server DOES know the
// members but this gate were skipped) silently honor a field the caller
// never meant to send past an unupgraded fleet. So this checks the
// capability once for the whole request — never per item, and never after
// the dial.
func (b *httpBatchApplier) refuseUnservedDepAddLineage(ctx context.Context, req issueops.ApplyBatchRequest) error {
	carriesLineage := false
	for _, item := range req.Items {
		if item.Kind == issueops.ItemDepAdd && depAddCarriesLineage(item.DepAdd) {
			carriesLineage = true
			break
		}
	}
	if !carriesLineage {
		return nil
	}
	snap, err := b.store.snapshot(ctx)
	if err != nil {
		return err
	}
	// snap is nil, nil whenever Store.snapshot has no transport AND no cached
	// handshake (a Store built with a nil wire): nothing was ever advertised,
	// so this falls straight through to the refusal below rather than
	// dereferencing a nil *apigen.ContextResponse.
	if snap != nil && slices.Contains(snap.Capabilities, wire.CapBatchApplyDepAddLineage) {
		return nil
	}
	return b.store.unsupportedCapability("BatchApplier.ApplyBatch", wire.CapBatchApplyDepAddLineage)
}

// applyBatchBody projects the role's request onto the wire body.
func applyBatchBody(req issueops.ApplyBatchRequest) (wire.ApplyBatchRequest, error) {
	items := make([]wire.ApplyItem, 0, len(req.Items))
	for i, item := range req.Items {
		encoded, err := applyItemBody(item, req.Actor)
		if err != nil {
			return wire.ApplyBatchRequest{}, fmt.Errorf("items[%d]: %w", i, err)
		}
		items = append(items, encoded)
	}

	body := wire.ApplyBatchRequest{Actor: req.Actor, Items: items}
	setItemString(&body.Provenance, req.Provenance)
	// The two booleans are sent only when TRUE, createBody's rule: each selects
	// a bypass, so an explicit false is the default said twice.
	setItemBool(&body.ForceIDPrefix, req.ForceIDPrefix)
	setItemBool(&body.SkipPerEdgeCycleCheck, req.SkipPerEdgeCycleCheck)
	return body, nil
}

// applyItemKinds is the tag vocabulary this client knows, paired with the
// payload each value names. It is a map rather than a switch so the closed set
// is ENUMERABLE, and so the agreement check below and the projection cannot
// drift into two opinions about which payload a kind reads.
var applyItemKinds = map[issueops.ItemKind]string{
	issueops.ItemCreate: "create",
	issueops.ItemUpdate: "update",
	issueops.ItemClose:  "close",
	issueops.ItemDepAdd: "dep_add",
}

// applyItemBody projects one tagged item, refusing all four ways a tag and its
// payloads can disagree.
//
// IT FAILS CLOSED ON AN UNKNOWN KIND, and that is the arm worth stating out
// loud: ItemKind is a closed set today, and a fifth verb added to issueops
// tomorrow would arrive here as a value this map does not carry. Sending it
// anyway would put an item on the wire with a tag the server refuses — a round
// trip to be told what is knowable here — and, worse, sending it with NO
// payload would be a plan that silently did less than the caller composed.
// Refusing names the kind.
func applyItemBody(item issueops.ApplyItem, actor string) (wire.ApplyItem, error) {
	named, known := applyItemKinds[item.Kind]
	if !known {
		return wire.ApplyItem{}, invalid("item kind %q is not one of create, update, close, dep_add", item.Kind)
	}

	// The tag and the payloads have to agree in BOTH directions, exactly as
	// they do at the server's own door: a kind with no payload is an item that
	// does nothing, a payload the kind does not name is an item whose two
	// halves disagree, and two payloads is an item that cannot say which it
	// meant. The role calls all three ErrValidation.
	var carried []string
	for _, present := range []struct {
		member string
		set    bool
	}{
		{"create", item.Create != nil},
		{"update", item.Update != nil},
		{"close", item.Close != nil},
		{"dep_add", item.DepAdd != nil},
	} {
		if present.set {
			carried = append(carried, present.member)
		}
	}
	switch {
	case len(carried) == 0:
		return wire.ApplyItem{}, invalid("an item of kind %q carries no %s payload", item.Kind, named)
	case len(carried) > 1:
		return wire.ApplyItem{}, invalid("an item carries exactly one payload; this one carries %v", carried)
	case carried[0] != named:
		return wire.ApplyItem{}, invalid("an item of kind %q carries the %q payload; the two must name the same verb",
			item.Kind, carried[0])
	}

	out := wire.ApplyItem{Kind: named}
	switch item.Kind {
	case issueops.ItemCreate:
		create, err := applyCreateItemBody(item.Create, actor)
		if err != nil {
			return wire.ApplyItem{}, err
		}
		out.Create = create
	case issueops.ItemUpdate:
		update, err := applyUpdateItemBody(item.Update)
		if err != nil {
			return wire.ApplyItem{}, err
		}
		out.Update = update
	case issueops.ItemClose:
		closeItem, err := applyCloseItemBody(item.Close)
		if err != nil {
			return wire.ApplyItem{}, err
		}
		out.Close = closeItem
	case issueops.ItemDepAdd:
		depAdd, err := applyDepAddItemBody(item.DepAdd)
		if err != nil {
			return wire.ApplyItem{}, err
		}
		out.DepAdd = depAdd
	}
	return out, nil
}

// applyCreateItemBody projects one create item.
//
// The wire's create vocabulary here is EXACTLY createIssue's twenty — the same
// members, spelled the same way — so the allowlist and the refusal sweep are
// the single create's, shared rather than copied. What is different is the
// ledger row the refusal cites: this is a different operation, and a reader
// auditing "why does my plan refuse?" must not be sent to a row about
// createIssue.
func applyCreateItemBody(item *issueops.CreateItem, actor string) (*apigen.ApplyCreateItem, error) {
	if item == nil || item.Issue == nil {
		return nil, invalid("a create item names no issue")
	}
	issue := item.Issue
	if len(issue.Comments) > 0 || len(issue.Dependencies) > 0 {
		// The ROLE's own rule rather than a wire divergence, and here it is
		// stronger than it is on the single create: edges in this role are
		// ITEMS, so a create item has nowhere to put one at all.
		return nil, invalid("a create item's Issue carries comments or dependencies; edges are dep_add items")
	}
	if err := refuseUnwirableIssueMembers(issue, actor, encode.OpApplyBatch, "W-CreateItem.Issue"); err != nil {
		return nil, err
	}

	// Priority is sent ALWAYS, createBody's reason: 0 is P0 and a real request,
	// so an absent member — which the server reads as the workspace default —
	// would silently reprioritize every critical issue a plan creates.
	priority := issue.Priority
	out := &apigen.ApplyCreateItem{Title: issue.Title, Priority: &priority}

	setItemString(&out.Key, item.Key)
	setItemString(&out.Id, issue.ID)
	setItemString(&out.Description, issue.Description)
	setItemString(&out.Design, issue.Design)
	setItemString(&out.AcceptanceCriteria, issue.AcceptanceCriteria)
	setItemString(&out.Notes, issue.Notes)
	setItemString(&out.Status, string(issue.Status))
	setItemString(&out.IssueType, string(issue.IssueType))
	setItemString(&out.Assignee, issue.Assignee)
	setItemString(&out.Owner, issue.Owner)
	setItemString(&out.Sender, issue.Sender)
	if issue.ExternalRef != nil {
		ref := *issue.ExternalRef
		out.ExternalRef = &ref
	}
	if issue.EstimatedMinutes != nil {
		minutes := *issue.EstimatedMinutes
		out.EstimatedMinutes = &minutes
	}
	out.DueAt = copyTime(issue.DueAt)
	out.DeferUntil = copyTime(issue.DeferUntil)
	if len(issue.Labels) > 0 {
		labels := append([]string(nil), issue.Labels...)
		out.Labels = &labels
	}
	if len(issue.Metadata) > 0 {
		// The blob travels as the bytes the caller sent, and only its
		// well-formedness is this layer's question. See requireJSON.
		if err := requireJSON("Issue.Metadata", issue.Metadata); err != nil {
			return nil, err
		}
		out.Metadata = append(apigen.MetadataValue(nil), issue.Metadata...)
	}
	setItemBool(&out.Ephemeral, issue.Ephemeral)
	setItemBool(&out.NoHistory, issue.NoHistory)

	if len(item.MetadataRefs) > 0 {
		refs := make(map[string]apigen.Ref, len(item.MetadataRefs))
		for key, ref := range item.MetadataRefs {
			encoded, err := applyRefBody(ref)
			if err != nil {
				return nil, fmt.Errorf("metadata_refs[%q]: %w", key, err)
			}
			refs[key] = encoded
		}
		out.MetadataRefs = &refs
	}
	return out, nil
}

// applyUpdateItemBody projects one update item.
func applyUpdateItemBody(item *issueops.UpdateItem) (*wire.ApplyUpdateItem, error) {
	if item == nil {
		return nil, invalid("an update item carries no payload")
	}
	target, err := applyRefBody(item.Target)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	patch, err := encodeApplyPatch(item.Patch)
	if err != nil {
		return nil, err
	}
	if len(patch) == 0 {
		// The role and the server both refuse an empty patch; saying it here
		// keeps a write that writes nothing off the wire entirely.
		return nil, invalid("an update item names no field to write")
	}

	out := &wire.ApplyUpdateItem{
		Target:          target,
		Patch:           patch,
		ExpectedVersion: revisionGuard(item.ExpectedVersion),
	}
	// The two string guards are pointers on both sides and the copy is a nil
	// check, updateGuards' rule: an empty `expected_assignee` is the guard that
	// says "only if nobody holds it", so absent has to stay absent.
	if item.ExpectedStatus != nil {
		status := string(*item.ExpectedStatus)
		out.ExpectedStatus = &status
	}
	if item.ExpectedAssignee != nil {
		assignee := *item.ExpectedAssignee
		out.ExpectedAssignee = &assignee
	}
	// All three force flags ARE published on this operation, unlike
	// updateIssue's body where they are still refused — so a plan can express
	// what a single patch over this wire cannot. They are sent only when true.
	setItemBool(&out.ForceClosePolicy, item.ForceClosePolicy)
	setItemBool(&out.ForceAssigneeTransfer, item.ForceAssigneeTransfer)
	setItemBool(&out.ForceNotesOverwrite, item.ForceNotesOverwrite)
	return out, nil
}

// applyCloseItemBody projects one close item.
//
// THE MAPPING IS TOTAL: every member of the role's CloseItem has a wire member,
// including the row-version guard, so no W- row names one. There is deliberately
// no expected_status on either side — a close is idempotent, so a guard spelled
// to refuse an already-closed row asks for a refusal where the verb answers
// with a no-op.
func applyCloseItemBody(item *issueops.CloseItem) (*apigen.ApplyCloseItem, error) {
	if item == nil {
		return nil, invalid("a close item carries no payload")
	}
	target, err := applyRefBody(item.Target)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	out := &apigen.ApplyCloseItem{Target: target, ExpectedVersion: revisionGuard(item.ExpectedVersion)}
	setItemString(&out.Reason, item.Reason)
	setItemString(&out.Session, item.Session)
	setItemBool(&out.Force, item.Force)
	return out, nil
}

// applyDepAddItemBody projects one edge item.
//
// The gate normalization a waits-for edge gets, and the refusal a bad gate
// earns, are the ROLE's and stay there: the blob travels as the caller's own
// bytes, and a second parse here would be a second definition of what the edge
// metadata plane accepts.
func applyDepAddItemBody(item *issueops.DepAddItem) (*apigen.ApplyDepAddItem, error) {
	if item == nil {
		return nil, invalid("a dep_add item carries no payload")
	}
	source, err := applyRefBody(item.Source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	target, err := applyRefBody(item.Target)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	if item.Type == "" {
		return nil, invalid("a dep_add item names no edge type")
	}
	out := &apigen.ApplyDepAddItem{Source: source, Target: target, Type: string(item.Type)}
	// A BLANK blob is ABSENT, not malformed, and reading it any other way is a
	// real bug rather than strictness: the role's own rule is that an absent,
	// blank or `{}` metadata on a waits-for edge is stored as the all-children
	// gate, so a client that refused whitespace would fail a normalization the
	// role performs. Only a NON-blank blob is checked for being JSON at all,
	// which is this layer's one question about it.
	if blob := strings.TrimSpace(item.Metadata); blob != "" {
		if err := requireJSON("DepAdd.Metadata", []byte(blob)); err != nil {
			return nil, err
		}
		out.Metadata = apigen.MetadataValue(blob)
	}
	// HasSpawner and ThreadID are gated by CapBatchApplyDepAddLineage
	// (checked in ApplyBatch, before the dial, over the whole request) —
	// this projection only encodes what the gate already cleared, which is
	// why HasSpawner is sent only where depAddNamesSpawner holds.
	setItemBool(&out.HasSpawner, depAddNamesSpawner(item))
	setItemString(&out.ThreadId, item.ThreadID)
	return out, nil
}

// depAddCarriesLineage reports whether a dep_add item names a member
// CapBatchApplyDepAddLineage gates: ThreadID on any edge, HasSpawner only
// where depAddNamesSpawner holds. Used to scan a whole request once before
// the dial, rather than discovering the gap one item at a time after bytes
// already left for the wire.
func depAddCarriesLineage(item *issueops.DepAddItem) bool {
	return item != nil && (depAddNamesSpawner(item) || item.ThreadID != "")
}

// depAddNamesSpawner reports whether a dep_add item's HasSpawner means
// anything: the role reads it on a waits-for edge only and ignores it on
// every other type. Off a waits-for edge the flag is therefore dropped, not
// sent, and never needs the capability — dropping it loses nothing the role
// would store, where refusing it would fail a mixed-version request the
// flag cannot change.
func depAddNamesSpawner(item *issueops.DepAddItem) bool {
	return item.HasSpawner && item.Type == issueops.DepWaitsFor
}

// applyRefBody projects one ref, applying the exactly-one rule the schema
// cannot state.
//
// Both members set is a caller that cannot say which it meant and neither is a
// reference to nothing; the role calls both ErrValidation before anything is
// written, so both are refused here rather than dialed. WHETHER a key RESOLVES,
// and whether it reaches backward far enough, is the role's question — only the
// server can see the whole request's key index — and comes back as a *RefError.
func applyRefBody(ref issueops.Ref) (apigen.Ref, error) {
	switch {
	case ref.Key == "" && ref.ID == "":
		return apigen.Ref{}, invalid("a ref names neither a key nor an id")
	case ref.Key != "" && ref.ID != "":
		return apigen.Ref{}, invalid("a ref names a key and an id; it must name exactly one")
	}
	var out apigen.Ref
	setItemString(&out.Key, ref.Key)
	setItemString(&out.Id, ref.ID)
	return out, nil
}

// applyPatchExcludedMembers is the refusal half of the apply patch's allowlist,
// in the order encodeApplyPatch decides them.
//
// THE ALLOWLIST IS NOT updateIssue's, and the two differences run in opposite
// directions, which is why this cannot share refuseExcludedPatchMembers:
//
//	owner       IS published here and refused there. ApplyPatchBody carries it;
//	            IssuePatchBody does not, which the document calls an accident of
//	            order rather than a decision.
//	parent_id   is published THERE and absent here, and that one is deliberate:
//	            a parent is a dep_add item of type parent-child, so the order of
//	            every edge in a plan stays total and there is one spelling for
//	            an edge.
//
// The other three — spec_id, await_id, closed_by_session — and persistence are
// absent from both bodies.
var applyPatchExcludedMembers = []struct {
	member string
	ledger string
	set    func(issueops.IssuePatch) bool
}{
	// ParentID is this document's OWN exclusion and gets its own row; the other
	// four are absent from both patch bodies, so they cite the rows that
	// already refuse them rather than minting a second reason for one fact.
	{"ParentID", "W-ApplyPatch.ParentID", func(p issueops.IssuePatch) bool { return p.ParentID.Set }},
	{"SpecID", "W-IssuePatch.SpecID", func(p issueops.IssuePatch) bool { return p.SpecID.Set }},
	{"AwaitID", "W-IssuePatch.AwaitID", func(p issueops.IssuePatch) bool { return p.AwaitID.Set }},
	{"ClosedBySession", "W-IssuePatch.ClosedBySession", func(p issueops.IssuePatch) bool { return p.ClosedBySession.Set }},
	{"Persistence", "W-IssuePatch.Persistence", func(p issueops.IssuePatch) bool { return p.Persistence.Set }},
}

// encodeApplyPatch turns the role's patch into an update item's patch document.
//
// It is a map for encodeIssuePatch's reason and no other: the four NULLABLE
// members wrap a POINTER, and a set nil pointer has to reach the server as a
// literal null — the clear — where an unset one has to be absent entirely.
// apigen.ApplyPatchBody spells them `omitempty`, which collapses the two.
//
// REFUSE FIRST, THEN ENCODE, so a patch that both names an excluded member and
// carries a malformed metadata blob reports the fact about the WIRE rather than
// whichever error the encoder happened to reach first.
//
// LABELS ARE THE FULL PATCH HERE, which is the one place this document is WIDER
// than PATCH /v0/beads/issues/{id}'s: a plan edits a label set it did not
// compose, so an incremental add or remove has to be expressible without
// reading the set back first. W-IssuePatch.Labels is updateIssue's row and does
// not reach this operation.
func encodeApplyPatch(patch issueops.IssuePatch) (map[string]any, error) {
	for _, excluded := range applyPatchExcludedMembers {
		if excluded.set(patch) {
			return nil, refuse(encode.OpApplyBatch, excluded.ledger)
		}
	}

	out := map[string]any{}
	setString(out, "title", patch.Title)
	setString(out, "description", patch.Description)
	setString(out, "design", patch.Design)
	setString(out, "acceptance_criteria", patch.AcceptanceCriteria)
	setString(out, "notes", patch.Notes)
	setString(out, "append_notes", patch.AppendNotes)
	setString(out, "assignee", patch.Assignee)
	setString(out, "owner", patch.Owner)
	if patch.Priority.Set {
		out["priority"] = patch.Priority.Value
	}
	if patch.IssueType.Set {
		out["issue_type"] = string(patch.IssueType.Value)
	}
	if patch.Status.Set {
		out["status"] = string(patch.Status.Value)
	}
	if metadataPatched(patch.Metadata) {
		document, err := encodeMetadataPatch(patch.Metadata)
		if err != nil {
			return nil, err
		}
		out["metadata"] = document
	}

	// The nullable four. A set member always lands, and lands as null when its
	// pointer is nil, because that is the clear.
	setNullable(out, "estimated_minutes", patch.EstimatedMinutes)
	setNullable(out, "external_ref", patch.ExternalRef)
	setNullableTime(out, "due_at", patch.DueAt)
	setNullableTime(out, "defer_until", patch.DeferUntil)

	if labels := encodeApplyLabelPatch(patch.Labels); len(labels) > 0 {
		out["labels"] = labels
	}
	return out, nil
}

// encodeApplyLabelPatch projects the ordered label edit whole.
//
// Replace is a Field rather than a plain slice on both sides, and that is the
// state a struct could not carry: a SET replacement holding no labels CLEARS
// every label, where an unset one leaves the current set as the starting point.
func encodeApplyLabelPatch(patch issueops.LabelPatch) map[string]any {
	out := map[string]any{}
	if patch.Replace.Set {
		// The copy starts from an EMPTY SLICE rather than a nil one, and that is
		// the clear rather than a style choice: a set Replace holding no labels
		// is how a caller says "remove every label", and a nil []string marshals
		// to JSON `null`, which this member does not accept. Starting from
		// []string{} makes the clear travel as the empty ARRAY the document
		// spells it with.
		out["replace"] = append([]string{}, patch.Replace.Value...)
	}
	if len(patch.Add) > 0 {
		out["add"] = append([]string(nil), patch.Add...)
	}
	if len(patch.Remove) > 0 {
		out["remove"] = append([]string(nil), patch.Remove...)
	}
	return out
}

// refuseUnwirableIssueMembers is the other half of createCarriedIssueMembers'
// allowlist: it refuses the first populated member of an issue that the wire's
// create vocabulary does not carry, the role does not ignore, and the server
// does not stamp from the actor (actorStampedCreateMember).
//
// It answers the FIRST offender in declaration order, which is deterministic —
// a caller fixing one member at a time must not see the reported member depend
// on map iteration. It is generalized over the LEDGER ROW because two
// operations publish the identical twenty members — createIssue and
// batchApply's create item — and a caller must be sent to the row for the
// operation they actually called. The partition itself is one partition and is
// shared, so the two cannot disagree about what the wire carries or about what
// the role drops.
func refuseUnwirableIssueMembers(issue *issueops.Issue, actor string, op encode.Op, ledgerID string) error {
	value := reflect.ValueOf(*issue)
	shape := value.Type()
	for i := range shape.NumField() {
		field := shape.Field(i)
		if !field.IsExported() {
			continue
		}
		if _, carried := createCarriedIssueMembers[field.Name]; carried {
			continue
		}
		if _, ignored := roleIgnoredCreateIssueMembers[field.Name]; ignored {
			continue
		}
		if value.Field(i).IsZero() || actorStampedCreateMember(field.Name, issue, actor) {
			continue
		}
		// The ledger row is the vocabulary; the member name is the fact the row
		// cannot carry, because one row covers the whole population.
		return fmt.Errorf("Issue.%s: %w", field.Name, refuse(op, ledgerID))
	}
	return nil
}

// applyBatchRefusal rebuilds the role's typed refusals from the problem
// document's item members.
//
// IT READS TYPED MEMBERS AND NEVER PROSE, which matters more on this operation
// than anywhere else on the surface: the request is all or nothing, so there is
// no per-item result array a caller could find the offender in, and `item_*`
// is the only place it exists.
//
// THE REF REFUSAL IS MATCHED FIRST, on `declared_later`'s PRESENCE. It is the
// one 400 that carries a discriminator — an ordering mistake reads differently
// from a typo — and it is emitted in both polarities precisely so absence can
// mean "this refusal was not about a key".
//
// A *RefError REPLACES the problem envelope rather than wrapping it, and that
// is the role type's shape rather than a choice here: RefError.Unwrap is
// hardcoded to ErrValidation and the type carries no member for a cause. Ledger
// row L-apply-ref records what that costs.
func applyBatchRefusal(err error) error {
	var problem *wire.ProblemError
	if !errors.As(err, &problem) {
		return err
	}
	if problem.DeclaredLater != nil {
		if problem.ItemIndex == nil {
			// A key refusal that named no item. Unreachable against this
			// server — the ref refusal is raised inside a request whose items
			// are indexed — but RefError.Index has no absent state, so
			// answering 0 would name the FIRST item as the offender on a
			// refusal that named none. The problem travels unwrapped instead.
			return err
		}
		return &issueops.RefError{
			Index:         *problem.ItemIndex,
			Member:        applyRefMember(problem.Param),
			Key:           derefStringPtr(problem.ItemKey),
			DeclaredLater: *problem.DeclaredLater,
		}
	}
	if problem.ItemIndex == nil {
		// A refusal the role raised without naming an item — the request's own
		// validation, or a transport-level answer. It travels unwrapped: an
		// *ItemError naming index 0 would be a claim about an item this refusal
		// says nothing about.
		return err
	}
	return &issueops.ItemError{
		Index:   *problem.ItemIndex,
		Kind:    issueops.ItemKind(derefStringPtr(problem.ItemKind)),
		Key:     derefStringPtr(problem.ItemKey),
		IssueID: derefStringPtr(problem.ItemIssueID),
		// The whole ProblemError, so errors.Is still reaches the sentinel its
		// code mapped to and the request id survives for a 5xx.
		Err: err,
	}
}

// applyRefMember maps the refusal's `param` back onto the role's Member, which
// is diagnostic prose rather than a vocabulary on either side.
//
// The wire names the MEMBER that held the bad ref — `items[3].create.metadata_refs`
// — where the role names the member and, for a metadata ref, the key inside it.
// The key is not recoverable from `param`, so this answers the member alone; see
// ledger row L-apply-ref.
func applyRefMember(param string) string {
	switch {
	case strings.HasSuffix(param, ".target"):
		return "target"
	case strings.HasSuffix(param, ".source"):
		return "source"
	default:
		return "metadata_refs"
	}
}

// decodeApplyBatchResult reads the wire response back into the role's result.
//
// THE ARRAY IS POSITIONAL AND IS CHECKED AS ONE, in the two ways the answer
// makes checkable. The wire promises one entry per requested item in request
// order, and a misattribution is the failure no assertion about a ROW would
// catch: both rows exist and both were written, so one item's `changed` and
// `revision` simply read as another's.
//
//	length and kind   a count that disagrees, or a kind that does not echo the
//	                  item at its index, is caught by comparing the answer with
//	                  the request the client itself composed.
//	the key map       `keys` binds each NAMED create to the id it was bound to,
//	                  independently of the array's order, so a keyed create's
//	                  result has a second source to agree with. This is what
//	                  catches a SAME-KIND SWAP, which the kind comparison above
//	                  cannot see at all: two transposed creates both answer
//	                  "create" at both indexes. A key the request declared and
//	                  the answer does not carry is refused for the same reason —
//	                  the binding is the one fact the request cannot carry, so a
//	                  missing one leaves a result that cannot be checked rather
//	                  than one to pass along.
//
// WHAT REMAINS UNCHECKABLE, said plainly rather than left as an implication: an
// UNKEYED same-kind swap. Nothing in the response distinguishes two unnamed
// creates and nothing in the request could — naming them is exactly what makes
// them distinguishable — so a transposition between two unnamed items of one
// kind passes. It sits in the same trust class as a server that answered about
// a row the request never named, and the served assembly is provably in item
// order (internal/httpapi/batch_apply.go's applyBatchResponse walks
// result.Items). A caller that needs the guarantee names its items.
//
// Every refusal here is the METHOD's rather than a per-item one, because a
// broken answer says nothing trustworthy about any single item.
func decodeApplyBatchResult(req issueops.ApplyBatchRequest, resp *apigen.ApplyBatchResponse) (issueops.ApplyBatchResult, error) {
	if resp == nil {
		return issueops.ApplyBatchResult{}, fmt.Errorf("bd serve returned no batch-apply result")
	}
	if len(resp.Items) != len(req.Items) {
		return issueops.ApplyBatchResult{}, fmt.Errorf(
			"bd serve returned %d batch-apply results for %d items", len(resp.Items), len(req.Items))
	}

	items := make([]issueops.ItemResult, len(resp.Items))
	for i, item := range resp.Items {
		kind := issueops.ItemKind(item.Kind)
		if kind != req.Items[i].Kind {
			return issueops.ApplyBatchResult{}, fmt.Errorf(
				"bd serve returned a %q result at index %d for a %q item; the results are positional",
				kind, i, req.Items[i].Kind)
		}
		if err := checkApplyKeyBinding(req.Items[i], i, item, resp.Keys); err != nil {
			return issueops.ApplyBatchResult{}, err
		}
		// The token is a decimal string on the wire (types.RevisionToken) and
		// an int64 on the Go contract; parseRevision is the one place it is
		// read back, and a token the server spelled in a way this client
		// cannot read refuses the whole result rather than stitching a 0 that
		// would only surface as a precondition failure on the NEXT request.
		revision, err := parseRevision(fmt.Sprintf("batchApply item %d", i), item.Revision)
		if err != nil {
			return issueops.ApplyBatchResult{}, err
		}
		items[i] = issueops.ItemResult{
			Kind:       kind,
			IssueID:    item.IssueId,
			Changed:    item.Changed,
			RowVersion: revision,
			// Issue stays nil. The wire result is lean by design and the role's
			// own leaf says why; L-apply-snapshot records it.
			Issue: nil,
		}
		if item.DependsOnId != nil {
			items[i].DependsOnID = *item.DependsOnId
		}
	}

	// Allocated rather than passed through, so nothing downstream holds a
	// window onto a decoded response body — and so a request whose creates
	// named nothing answers with an empty map rather than a nil one.
	keys := make(map[string]string, len(resp.Keys))
	for key, id := range resp.Keys {
		keys[key] = id
	}
	return issueops.ApplyBatchResult{Keys: keys, Items: items}, nil
}

// checkApplyKeyBinding cross-checks one item's result against the key map.
//
// It applies to KEYED CREATES and to nothing else, which is the whole of what
// the answer makes checkable: `keys` carries only the keys the request NAMED,
// an unnamed create item is in `items` and not there, and an update or a close
// resolves a ref the caller already knew the id of.
func checkApplyKeyBinding(item issueops.ApplyItem, index int, result apigen.ApplyItemResult, keys map[string]string) error {
	if item.Kind != issueops.ItemCreate || item.Create == nil || item.Create.Key == "" {
		return nil
	}
	key := item.Create.Key
	bound, named := keys[key]
	if !named {
		return fmt.Errorf(
			"bd serve bound no id to key %q, which items[%d] declares; the key binding is the one fact the request cannot carry",
			key, index)
	}
	if bound != result.IssueId {
		return fmt.Errorf(
			"bd serve bound key %q to %s and answered items[%d] with %s; the results are positional and this pair disagrees",
			key, bound, index, result.IssueId)
	}
	return nil
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func derefStringPtr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
