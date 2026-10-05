// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/write_ledger_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/issueops"
)

// The WRITE door's refuse-not-drop completeness guard — the counterpart of the
// encoder bijection the read door already has (encode/bijection_test.go).
//
// The read gate cannot cover this half and says so: its rows are query
// parameters, and it explicitly exempts "rows about the WRITE shapes" because
// "this package encodes no request bodies". That exemption left the write side
// with a promise and no check. D8's refuse-not-drop enumeration — "every
// role-request field the wire has no member for" — was a list somebody wrote
// down, so a member added to issueops tomorrow would be silently DROPPED by
// every write role in this package, which is the one failure class no
// server-side gate can observe: the server rejects only what it receives.
//
// The gate runs three ways, and each catches a different staleness:
//
//	source -> wire      every populatable field of every write shape either
//	                    names a member of the wire body that carries it, is
//	                    routed some other way, or has a divergence-ledger row
//	                    refusing it. There is no fourth arm.
//	wire -> source      every member the wire body publishes is driven by a
//	                    field. A member nobody drives is a write this client
//	                    cannot make and has not admitted to.
//	ledger -> source    every write-shape ledger row still names a field this
//	                    table treats as excluded. A row for a field that
//	                    started serving is a leftover that would make an audit
//	                    of "is this refusal real?" unanswerable.
//
// It lives in this package rather than beside the ledger because the ledger's
// package deliberately does not import apigen or the role bodies, and because
// the encoded patch document — which is not a struct at all — can only be
// driven from here.

// carriage says how one field of a role request reaches the server.
//
// Exactly one of member and routed is set. A field that is in neither state is
// not in this table at all, and that is the statement "the wire has no place for
// it" — which is what obliges a ledger row.
type carriage struct {
	// member is the JSON member of the wire body that carries the field.
	member string
	// spread is the members a field that FLATTENS carries, in place of member.
	//
	// createIssue is the whole population and the shape is the reason: it
	// publishes the create vocabulary at the top LEVEL of one flat body, so
	// CreateRequest.Issue — a whole types.Issue — drives twenty members of it
	// rather than one. batchCreateIssues has the same flattening and does not
	// need this, because its item is a nested body with a shape row of its own;
	// a create has no nesting to hang one on.
	//
	// It is not a way to leave the population unchecked. Every name here must
	// be published, a member absent from every field's spread is still
	// unreachable, and the per-member completeness against types.Issue is a
	// different question held by a different gate — the reflective sweep the
	// partial row below cites.
	spread []string
	// routed names the non-body route, with the reason. The id that becomes a
	// path segment is the whole population today.
	routed string
	// partial cites the ledger row for the SHAPE of this field the member
	// cannot carry, on a field that is carried in one shape and refused in
	// another. Labels are one: the member is a whole-set replace, so an
	// incremental add or remove has no expression. CreateRequest.Issue is the
	// other, and its excluded shape is a set of MEMBERS rather than a mode.
	partial string
}

func member(name string) carriage { return carriage{member: name} }

func spread(names ...string) carriage { return carriage{spread: names} }

// members lists every wire member a carriage drives, whichever way it drives
// them, so the two directions of the gate read one accessor.
func (c carriage) members() []string {
	if c.member != "" {
		return []string{c.member}
	}
	return c.spread
}

// writeShape is one role-request shape and the wire body that carries it.
type writeShape struct {
	name   string
	source reflect.Type
	body   reflect.Type
	// carried is the classification. A populatable field absent from it must
	// have a KindRefuse ledger row naming source.field.
	carried map[string]carriage
	// pending maps a wire member this client does not send YET to the field it
	// would drive. It is the fourth state the wire -> source arm needs, and it
	// arrived from upstream rather than from a decision here: a member the
	// document did not publish when the refusal was written, and does now.
	//
	// It is NOT a way to leave a member unaccounted for. The field it names
	// must still carry a KindRefuse row — so the refusal is ledgered, pinned
	// and auditable exactly as before — and the row's reason has to say the
	// wire publishes the member, because the alternative is a ledger that
	// argues from a document that has moved on.
	//
	// A member reaches this map only by upstream publishing it under an
	// existing refusal. Teaching the client to send one is a FLIP: the entry
	// moves to carried, the ledger row retires, and the pin that asserts the
	// refusal becomes a pin that asserts the round trip.
	pending map[string]string
}

const pathRoute = "the {id} path segment; a body member for it would be a second, contradictable spelling of the resource"

// writeShapes enumerates every request body this client puts on the wire.
//
// The list is checked for completeness against WriteWire itself
// (TestEveryWriteWireBodyIsClassified), so an operation added to the transport
// seam cannot arrive here unclassified.
func writeShapes() []writeShape {
	return []writeShape{
		{
			name:   "claimIssue",
			source: reflect.TypeOf(issueops.ClaimRequest{}),
			body:   reflect.TypeOf(apigen.ClaimRequest{}),
			carried: map[string]carriage{
				"Actor":   member("actor"),
				"IssueID": {routed: pathRoute},
			},
		},
		{
			// The whole vocabulary of a single create, FLAT: this operation
			// publishes the issue's own members at the top level of its request
			// rather than nesting them, which is what the Issue spread below is
			// about.
			name:   "createIssue",
			source: reflect.TypeOf(issueops.CreateRequest{}),
			body:   reflect.TypeOf(apigen.CreateIssueRequest{}),
			carried: map[string]carriage{
				"Actor": member("actor"),
				"Issue": {
					spread: []string{
						"id", "title", "description", "design", "acceptance_criteria",
						"notes", "status", "priority", "issue_type", "assignee", "owner",
						"estimated_minutes", "external_ref", "due_at", "defer_until",
						"sender", "metadata", "labels", "ephemeral", "no_history",
					},
					// The members of a types.Issue this vocabulary does NOT
					// spell — the creation stamp, the workflow and
					// classification plumbing — refuse per member.
					partial: "W-CreateRequest.Issue",
				},
				"ParentID":                member("parent_id"),
				"InheritLabelsFromParent": member("inherit_labels_from_parent"),
				"Dependencies":            member("dependencies"),
				"WaitsFor":                member("waits_for"),
				"ForceIDPrefix":           member("force_id_prefix"),
			},
		},
		{
			name:   "closeIssue",
			source: reflect.TypeOf(issueops.CloseRequest{}),
			body:   reflect.TypeOf(apigen.CloseIssueRequest{}),
			carried: map[string]carriage{
				"Actor":   member("actor"),
				"IssueID": {routed: pathRoute},
				"Reason":  member("reason"),
				"Session": member("session"),
				"Force":   member("force"),
				// The wire wave published closeIssue's row-version guard
				// (#5506) and the client wave sends it (ga-jbuyf), so it left
				// the pending set for this one.
				"ExpectedVersion": member("expected_version"),
			},
		},
		{
			// The claim's inverse, and a TOTAL mapping: four request members,
			// three body members and the path. Nothing refuses on shape, so
			// there is no partial row and no W- entry — the same clean sheet
			// compareAndSetMetadata has, reached for the opposite reason (that
			// request is wide and fully published; this one is narrow).
			name:   "releaseIssue",
			source: reflect.TypeOf(issueops.ReleaseRequest{}),
			body:   reflect.TypeOf(apigen.ReleaseIssueRequest{}),
			carried: map[string]carriage{
				"Actor":   member("actor"),
				"IssueID": {routed: pathRoute},
				// The compare-and-set on the HOLDER. It is a pointer on both
				// sides and for one reason: absent selects the unconditional
				// path and empty is a refusal, so a request cannot spell "do
				// not check" any other way.
				"ExpectedAssignee": member("expected_assignee"),
				"Force":            member("force"),
			},
		},
		{
			// The narrowest body on the surface, and the only one whose ANCHOR
			// is a path segment of a SUB-RESOURCE rather than of the resource
			// itself. `author` is not `actor` and the gate reads it as its own
			// member: the value is stored on the row and read back by everyone
			// who sees the thread, where an actor is who a mutation is
			// attributed to.
			name:   "addComment",
			source: reflect.TypeOf(issueops.AddCommentRequest{}),
			body:   reflect.TypeOf(apigen.AddCommentRequest{}),
			carried: map[string]carriage{
				"Author":  member("author"),
				"IssueID": {routed: pathRoute},
				"Text":    member("text"),
			},
		},
		{
			name:   "reopenIssue",
			source: reflect.TypeOf(issueops.ReopenRequest{}),
			body:   reflect.TypeOf(apigen.ReopenIssueRequest{}),
			carried: map[string]carriage{
				"Actor":           member("actor"),
				"IssueID":         {routed: pathRoute},
				"Reason":          member("reason"),
				"ExpectedVersion": member("expected_version"),
			},
		},
		{
			name:   "updateIssue",
			source: reflect.TypeOf(issueops.UpdateRequest{}),
			body:   reflect.TypeOf(apigen.UpdateIssueRequest{}),
			carried: map[string]carriage{
				"Actor":   member("actor"),
				"IssueID": {routed: pathRoute},
				"Patch":   member("patch"),
				// The guard trio of upstream #5484, now sent. They ride at the
				// body's top level rather than inside the patch document, which
				// is where the server reads them.
				"ExpectedVersion":  member("expected_version"),
				"ExpectedStatus":   member("expected_status"),
				"ExpectedAssignee": member("expected_assignee"),
				// The third member of the FORCE trio, now sent (S3
				// reconciliation, 2026-10): the single-patch
				// updateIssue body carries `force_notes_overwrite` exactly as
				// issues:batchApply's update item already did, so the fence a
				// caller means to bypass is bypassable here too.
				// W-UpdateRequest.ForceNotesOverwrite is RETIRED, not deleted —
				// it records that this member was found unwired and then wired.
				"ForceNotesOverwrite": member("force_notes_overwrite"),
				// The other two FORCE members, and the claim, followed it (the
				// #7247 review port): each was published — the force pair by
				// upstream #5484, `claim` by upstream #6890 — and refused only
				// by this client. Their rows are RETIRED the same way. A claim
				// ALONE against a server that predates `claim` still reaches
				// claimIssue instead, as a fallback on that server's skew
				// refusal (see claimOnlyUpdate); against a current one it is
				// this body.
				"ForceAssigneeTransfer": member("force_assignee_transfer"),
				"ForceClosePolicy":      member("force_close_policy"),
				"Claim":                 member("claim"),
			},
		},
		{
			// The nested document. Its members are the wire's allowlist, and
			// TestTheEncodedPatchDocumentUsesOnlyPublishedMembers drives the
			// encoder against it — a struct comparison alone would not, since
			// the client builds this one as a map.
			name:   "updateIssue/patch",
			source: reflect.TypeOf(issueops.IssuePatch{}),
			body:   reflect.TypeOf(apigen.IssuePatchBody{}),
			carried: map[string]carriage{
				"Title":              member("title"),
				"Description":        member("description"),
				"Design":             member("design"),
				"AcceptanceCriteria": member("acceptance_criteria"),
				"Notes":              member("notes"),
				"AppendNotes":        member("append_notes"),
				"Priority":           member("priority"),
				"IssueType":          member("issue_type"),
				"EstimatedMinutes":   member("estimated_minutes"),
				"ExternalRef":        member("external_ref"),
				"DueAt":              member("due_at"),
				"DeferUntil":         member("defer_until"),
				// THE ORDERED EDIT, whole: one field driving three members, which
				// is what `spread` is for. `labels` replaces, `add_labels` adds
				// and `remove_labels` removes, applied in that order so removal
				// wins — the role's own algebra, and the same three the batchApply
				// shape has carried since client wave ga-mijra.
				"Labels": spread("labels", "add_labels", "remove_labels"),
				// The patch half of upstream #5484, carried by client wave
				// ga-7i6by. They left the pending set below with their W- rows.
				"Status":   member("status"),
				"Assignee": member("assignee"),
				"ParentID": member("parent_id"),
				"Metadata": member("metadata"),
			},
			// NOTHING IS PENDING, and the set is empty rather than absent for the
			// reason the second list in accessors.go was: it held the two
			// incremental-label members #5510 published under an existing refusal,
			// and client wave ga-jpywb moved both into `carried` — which is what
			// the map documents a flip as. The next member upstream publishes
			// under a live W- row starts it again.
		},
		{
			// The nested metadata document, and the reason it is a shape of its
			// own rather than a line in its parent: `metadata` is ONE member up
			// there, and the four arms of the algebra under it are four fields
			// of a role type that can grow. Classifying them here is what makes
			// a fifth arm added to MetadataPatch fail closed instead of being
			// dropped by an encoder that never learned about it.
			//
			// It is TOTAL in both directions. What diverges is not a member: it
			// is the replace-plus-incremental contradiction, which both sides
			// refuse as a validation failure writing nothing, so there is no
			// row to carry.
			name:   "updateIssue/patch/metadata",
			source: reflect.TypeOf(issueops.MetadataPatch{}),
			body:   reflect.TypeOf(apigen.ApplyMetadataPatch{}),
			carried: map[string]carriage{
				"Replace": member("replace"),
				"Merge":   member("merge"),
				"Set":     member("set"),
				"Unset":   member("unset"),
			},
		},
		{
			// TOTAL in both directions, and the one write on this surface whose
			// refusal is a 200: a lost race is `swapped: false` with the value
			// that refused it, so nothing about the verdict is a request member
			// and no W- row names one.
			name:   "compareAndSetMetadata",
			source: reflect.TypeOf(issueops.CompareAndSetKeyRequest{}),
			body:   reflect.TypeOf(apigen.CompareAndSetMetadataRequest{}),
			carried: map[string]carriage{
				"Actor":    member("actor"),
				"IssueID":  {routed: pathRoute},
				"Key":      member("key"),
				"Expected": member("expected"),
				"Value":    member("value"),
			},
		},
		{
			name:   "addDependencies",
			source: reflect.TypeOf(issueops.AddDependenciesRequest{}),
			body:   reflect.TypeOf(apigen.AddDependenciesRequest{}),
			carried: map[string]carriage{
				"Actor": member("actor"),
				"Edges": member("edges"),
			},
		},
		{
			name:   "addDependencies/edge",
			source: reflect.TypeOf(issueops.DependencyEdge{}),
			body:   reflect.TypeOf(apigen.DependencyEdge{}),
			carried: map[string]carriage{
				"IssueID":     member("issue_id"),
				"DependsOnID": member("depends_on_id"),
				"Type":        member("type"),
			},
		},
		{
			name:   "removeDependency",
			source: reflect.TypeOf(issueops.RemoveDependencyRequest{}),
			body:   reflect.TypeOf(apigen.RemoveDependencyRequest{}),
			carried: map[string]carriage{
				"Actor":       member("actor"),
				"IssueID":     member("issue_id"),
				"DependsOnID": member("depends_on_id"),
			},
		},
		{
			// S4 extended the sweep carriage: apigen.SweepRequest now
			// publishes protect_live_dependents and limit too, each carried
			// by the role field of the same name (behind
			// CapSweepLiveDependents and CapSweepLimit respectively —
			// see sweeper.go's refuseUnservedSweep).
			name:   "sweepIssues",
			source: reflect.TypeOf(issueops.SweepRequest{}),
			body:   reflect.TypeOf(apigen.SweepRequest{}),
			carried: map[string]carriage{
				"Actor":                 member("actor"),
				"Tier":                  member("tier"),
				"ClosedBefore":          member("closed_before"),
				"IDPattern":             member("pattern"),
				"ProtectReferenced":     member("protect_referenced"),
				"DryRun":                member("dry_run"),
				"ProtectLiveDependents": member("protect_live_dependents"),
				"Limit":                 member("limit"),
			},
		},
		{
			// Total too. What diverges on the delete is its REFUSAL vocabulary
			// (L-delete-notfound, L-delete-dependents), which is a response
			// shape rather than a request member and so is not this gate's.
			name:   "deleteIssues",
			source: reflect.TypeOf(issueops.DeleteRequest{}),
			body:   reflect.TypeOf(apigen.DeleteIssuesRequest{}),
			carried: map[string]carriage{
				"Actor":           member("actor"),
				"IDs":             member("ids"),
				"Cascade":         member("cascade"),
				"Force":           member("force"),
				"DryRun":          member("dry_run"),
				"ExpectedVersion": member("expected_version"),
			},
		},
		{
			name:   "batchCreateIssues",
			source: reflect.TypeOf(issueops.CreateBatchRequest{}),
			body:   reflect.TypeOf(apigen.BatchCreateRequest{}),
			carried: map[string]carriage{
				"Actor": member("actor"),
				"Items": member("items"),
			},
		},
		{
			// The item's ISSUE half is not classified here, and cannot be: it
			// is a whole types.Issue flattened onto eight members of this same
			// body, so a shape row would claim one member for a field that
			// spreads across eight. TestBatchCreateRefusesEveryMemberTheWireExcludes
			// owns that population instead, reflectively and exhaustively.
			name:   "batchCreateIssues/dependency",
			source: reflect.TypeOf(issueops.CreateDependency{}),
			body:   reflect.TypeOf(apigen.BatchCreateDependency{}),
			carried: map[string]carriage{
				"TargetID": member("target_id"),
				"Type":     member("type"),
			},
		},
		{
			// ClaimNext has no wire member at all: OSS's apigen.BatchCloseRequest
			// publishes actor, items, session and force only, with no composed
			// claim_next object for a *ReadyRequest to encode into. It is absent
			// from carried below, so the wire->source arm obliges a KindRefuse
			// ledger row naming CloseBatchRequest.ClaimNext
			// (W-CloseBatchRequest.ClaimNext) rather than a member.
			name:   "batchCloseIssues",
			source: reflect.TypeOf(issueops.CloseBatchRequest{}),
			body:   reflect.TypeOf(apigen.BatchCloseRequest{}),
			carried: map[string]carriage{
				"Actor":   member("actor"),
				"Items":   member("items"),
				"Session": member("session"),
				"Force":   member("force"),
			},
		},
		{
			name:   "batchCloseIssues/item",
			source: reflect.TypeOf(issueops.BatchCloseItem{}),
			body:   reflect.TypeOf(apigen.BatchCloseItem{}),
			carried: map[string]carriage{
				"IssueID": member("id"),
				"Reason":  member("reason"),
			},
		},
		{
			// The plan. TOTAL in both directions: every member of the role's
			// request has a wire member, which is unusual on this surface and
			// is the operation rather than luck — the document was written from
			// the role.
			name:   "applyBatch",
			source: reflect.TypeOf(issueops.ApplyBatchRequest{}),
			body:   reflect.TypeOf(apigen.ApplyBatchRequest{}),
			carried: map[string]carriage{
				"Actor":                 member("actor"),
				"Items":                 member("items"),
				"Provenance":            member("provenance"),
				"ForceIDPrefix":         member("force_id_prefix"),
				"SkipPerEdgeCycleCheck": member("skip_per_edge_cycle_check"),
			},
		},
		{
			// The tagged item. Kind drives the tag; the four payloads drive the
			// four optional members, and the AGREEMENT between them is what no
			// schema can state and the role checks by hand — see
			// TestApplyBatchFailsClosedOnAnItemTheClientCannotSpell.
			name:   "applyBatch/item",
			source: reflect.TypeOf(issueops.ApplyItem{}),
			body:   reflect.TypeOf(apigen.ApplyItem{}),
			carried: map[string]carriage{
				"Kind":   member("kind"),
				"Create": member("create"),
				"Update": member("update"),
				"Close":  member("close"),
				"DepAdd": member("dep_add"),
			},
		},
		{
			// The create item, whose Issue SPREADS over the wire's twenty for
			// createIssue's reason: this payload publishes the create
			// vocabulary at its own top level rather than nesting it. The
			// per-member completeness against types.Issue is the reflective
			// sweep the partial row cites, shared with the single create.
			name:   "applyBatch/item/create",
			source: reflect.TypeOf(issueops.CreateItem{}),
			body:   reflect.TypeOf(apigen.ApplyCreateItem{}),
			carried: map[string]carriage{
				"Key": member("key"),
				"Issue": {
					spread: []string{
						"id", "title", "description", "design", "acceptance_criteria", "notes",
						"status", "priority", "issue_type", "assignee", "owner", "estimated_minutes",
						"external_ref", "due_at", "defer_until", "sender", "metadata", "labels",
						"ephemeral", "no_history",
					},
					partial: "W-CreateItem.Issue",
				},
				"MetadataRefs": member("metadata_refs"),
			},
		},
		{
			// The update item. It carries every FORCE member updateIssue does:
			// this operation publishes them per item, so a plan can express
			// what a single patch over this wire can.
			name:   "applyBatch/item/update",
			source: reflect.TypeOf(issueops.UpdateItem{}),
			body:   reflect.TypeOf(apigen.ApplyUpdateItem{}),
			carried: map[string]carriage{
				"Target":                member("target"),
				"Patch":                 member("patch"),
				"ExpectedVersion":       member("expected_version"),
				"ExpectedStatus":        member("expected_status"),
				"ExpectedAssignee":      member("expected_assignee"),
				"ForceClosePolicy":      member("force_close_policy"),
				"ForceAssigneeTransfer": member("force_assignee_transfer"),
				// All three force flags are published here (S3 reconciliation,
				// 2026-10 — batchapplier.go now sends it
				// exactly as it sends the other two).
				"ForceNotesOverwrite": member("force_notes_overwrite"),
			},
		},
		{
			// The update item's patch DOCUMENT, and the second shape in this
			// table sourced from issueops.IssuePatch. The two allowlists differ
			// in both directions — owner is published here and not there,
			// parent_id there and not here — which is why the gate below reads
			// every shape over a source rather than one.
			name:   "applyBatch/update/patch",
			source: reflect.TypeOf(issueops.IssuePatch{}),
			body:   reflect.TypeOf(apigen.ApplyPatchBody{}),
			carried: map[string]carriage{
				"Title":              member("title"),
				"Description":        member("description"),
				"Design":             member("design"),
				"AcceptanceCriteria": member("acceptance_criteria"),
				"Notes":              member("notes"),
				"AppendNotes":        member("append_notes"),
				"Priority":           member("priority"),
				"IssueType":          member("issue_type"),
				"Status":             member("status"),
				"Assignee":           member("assignee"),
				"Owner":              member("owner"),
				"EstimatedMinutes":   member("estimated_minutes"),
				"ExternalRef":        member("external_ref"),
				"DueAt":              member("due_at"),
				"DeferUntil":         member("defer_until"),
				// The full ordered edit rather than updateIssue's replace-only
				// member, so there is no partial row here: a removal IS
				// expressible on this document.
				"Labels":   member("labels"),
				"Metadata": member("metadata"),
			},
		},
		{
			// The label edit, a shape of its own for the reason the metadata
			// patch is one: `labels` is ONE member up there, and the three arms
			// under it are three fields of a role type that can grow. It is
			// total in both directions.
			name:   "applyBatch/update/patch/labels",
			source: reflect.TypeOf(issueops.LabelPatch{}),
			body:   reflect.TypeOf(apigen.ApplyLabelPatch{}),
			carried: map[string]carriage{
				"Replace": member("replace"),
				"Add":     member("add"),
				"Remove":  member("remove"),
			},
		},
		{
			// The close item. TOTAL, including the row-version guard, and
			// deliberately without an expected_status on either side.
			name:   "applyBatch/item/close",
			source: reflect.TypeOf(issueops.CloseItem{}),
			body:   reflect.TypeOf(apigen.ApplyCloseItem{}),
			carried: map[string]carriage{
				"Target":          member("target"),
				"Reason":          member("reason"),
				"Session":         member("session"),
				"Force":           member("force"),
				"ExpectedVersion": member("expected_version"),
			},
		},
		{
			// The edge item. The gate normalization a waits-for edge gets is
			// the ROLE's and travels inside the blob. HasSpawner and ThreadID
			// have no wire member, so they are absent from carried below and
			// refuse under W-DepAddItem.HasSpawner and W-DepAddItem.ThreadID.
			name:   "applyBatch/item/dep_add",
			source: reflect.TypeOf(issueops.DepAddItem{}),
			body:   reflect.TypeOf(apigen.ApplyDepAddItem{}),
			carried: map[string]carriage{
				"Source":   member("source"),
				"Target":   member("target"),
				"Type":     member("type"),
				"Metadata": member("metadata"),
			},
		},
		{
			// The ref. TOTAL — and the EXACTLY-ONE rule the schema cannot state
			// is behavior rather than a member, refused client-side before the
			// dial.
			name:   "applyBatch/ref",
			source: reflect.TypeOf(issueops.Ref{}),
			body:   reflect.TypeOf(apigen.Ref{}),
			carried: map[string]carriage{
				"Key": member("key"),
				"ID":  member("id"),
			},
		},
	}
}

// TestEveryWireExcludedWriteMemberCarriesALedgerRow is the source -> wire
// direction: the one that fails when a field is added to issueops and this
// client starts dropping it.
func TestEveryWireExcludedWriteMemberCarriesALedgerRow(t *testing.T) {
	shapes := writeShapes()
	if len(shapes) == 0 {
		t.Fatal("no write shapes are classified; there is nothing for this gate to check")
	}

	rows := ledgerRowsByField(t)
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			members := bodyMembers(t, shape.body)

			for name, how := range shape.carried {
				if _, ok := shape.source.FieldByName(name); !ok {
					t.Errorf("%s classifies %s.%s, which does not exist — the field was renamed or removed",
						shape.name, shape.source.Name(), name)
					continue
				}
				ways := 0
				for _, set := range []bool{how.member != "", len(how.spread) > 0, how.routed != ""} {
					if set {
						ways++
					}
				}
				switch {
				case ways > 1:
					t.Errorf("%s.%s claims more than one of a body member, a spread and a route; it has exactly one", shape.source.Name(), name)
				case ways == 0:
					t.Errorf("%s.%s is in the carried table with neither a member nor a route", shape.source.Name(), name)
				default:
					for _, wireMember := range how.members() {
						if !members[wireMember] {
							t.Errorf("%s.%s claims wire member %q, which %s does not publish: %v",
								shape.source.Name(), name, wireMember, shape.body.Name(), sortedKeys(members))
						}
					}
				}
				if how.partial != "" {
					checkRefusalRow(t, rows, shape.source, name, how.partial)
				}
			}

			for _, name := range populatableFields(shape.source) {
				if _, ok := shape.carried[name]; ok {
					continue
				}
				row, ok := rows[fieldKey{shape.source, name}]
				if !ok {
					t.Errorf("%s.%s reaches no wire member and has no divergence-ledger row.\n"+
						"Carry it in the %s table, or refuse it with a W- row — a write member this client drops is an edit "+
						"the caller believes landed. See the refuse-not-drop rows and L12 in the in-repo divergence ledger, "+
						"engdocs/design/http-divergence-ledger.md.",
						shape.source.Name(), name, shape.name)
					continue
				}
				if row.Kind != encode.KindRefuse {
					t.Errorf("%s: ledger row %s is %q; a wire-excluded write member must REFUSE, never degrade",
						shape.name, row.ID, row.Kind)
				}
			}
		})
	}
}

// TestEveryWireBodyMemberIsDrivenByARequestField is the wire -> source
// direction. A member the document publishes that no field drives is a write
// this client cannot make, and the ledger has no row for it because nothing
// refused anything — it is simply unreachable.
func TestEveryWireBodyMemberIsDrivenByARequestField(t *testing.T) {
	for _, shape := range writeShapes() {
		t.Run(shape.name, func(t *testing.T) {
			driven := map[string]string{}
			for name, how := range shape.carried {
				for _, wireMember := range how.members() {
					if other, dup := driven[wireMember]; dup {
						t.Errorf("%s and %s both drive wire member %q", other, name, wireMember)
					}
					driven[wireMember] = name
				}
			}
			rows := ledgerRowsByField(t)
			for wireMember := range bodyMembers(t, shape.body) {
				if driven[wireMember] != "" {
					continue
				}
				field, deferred := shape.pending[wireMember]
				if !deferred {
					t.Errorf("%s publishes %q and no field of %s drives it; that member is unreachable from this client.\n"+
						"Carry it in the %s table, or — if the wire only just published it under an existing refusal — name the refused field in that table's pending set",
						shape.body.Name(), wireMember, shape.source.Name(), shape.name)
					continue
				}
				// A pending member is only accounted for while its field is
				// still refused. The day the field starts serving without the
				// member being carried, this fails rather than going quiet.
				checkRefusalRowExists(t, rows, shape.source, field, wireMember)
			}
			for wireMember, field := range shape.pending {
				if driven[wireMember] != "" {
					t.Errorf("%s.%s is both carried and pending on %q; a member is one or the other",
						shape.source.Name(), field, wireMember)
				}
				if !bodyMembers(t, shape.body)[wireMember] {
					t.Errorf("%s names pending member %q, which %s does not publish; the entry outlived the member",
						shape.name, wireMember, shape.body.Name())
				}
			}
		})
	}
}

// checkRefusalRowExists is checkRefusalRow without a caller-supplied row id:
// the pending set names a FIELD, and what has to hold is that the ledger still
// refuses it under some row.
func checkRefusalRowExists(t *testing.T, rows map[fieldKey]encode.Row, owner reflect.Type, field, wireMember string) {
	t.Helper()
	row, ok := rows[fieldKey{owner, field}]
	if !ok {
		t.Errorf("wire member %q is pending on %s.%s, which carries no divergence-ledger row; a member nobody sends and nobody refused is unaccounted for",
			wireMember, owner.Name(), field)
		return
	}
	if row.Kind != encode.KindRefuse {
		t.Errorf("wire member %q is pending on %s.%s, whose ledger row %s is %q; a member this client does not send must REFUSE the field, never degrade it",
			wireMember, owner.Name(), field, row.ID, row.Kind)
	}
}

// TestNoWriteLedgerRowSurvivesTheFieldItRefuses is the ledger -> source
// direction: a W- row whose field started serving, or stopped existing.
//
// The rows this owns are exactly the ones encode/bijection_test.go exempts —
// "rows about the WRITE shapes are deliberately exempt: this package encodes no
// request bodies" — so between the two gates every field-shaped row in the
// ledger is now held to a table.
// ONE SOURCE TYPE MAY HAVE SEVERAL SHAPES, and since client wave ga-mijra one
// does: issueops.IssuePatch is the body of updateIssue's patch document AND of
// an issues:batchApply update item's, and the two allowlists differ in BOTH
// directions — `owner` is published by the batch's and not the single patch's,
// `parent_id` by the single patch's and not the batch's. So a row is a leftover
// only when EVERY shape over its source carries the field; a row refusing a
// field one shape carries and another refuses is the whole point of having two.
func TestNoWriteLedgerRowSurvivesTheFieldItRefuses(t *testing.T) {
	shapes := map[reflect.Type][]writeShape{}
	for _, shape := range writeShapes() {
		shapes[shape.source] = append(shapes[shape.source], shape)
	}

	var seen int
	for _, row := range encode.Ledger() {
		if row.Type == nil {
			continue
		}
		over, ok := shapes[row.Type]
		if !ok {
			continue // a read shape; the encoder bijection owns it.
		}
		// A RETIRED row is not a leftover, it is the record OF one: it says the
		// wire published the member and this client now sends it, so "every
		// shape carries the field" is precisely the state it describes. It keeps
		// its Type and Field rather than dropping them — the reflect.Type binding
		// is what makes a renamed field a compile error instead of a stale string
		// — so the skip is by KIND and not by an absent coordinate.
		if row.Kind == encode.KindRetired {
			continue
		}
		seen++
		var carriedBy []string
		for _, shape := range over {
			how, carried := shape.carried[row.Field]
			switch {
			case !carried:
				// The ordinary case: an excluded field with its row.
			case how.partial == row.ID:
				// The partial case: carried in one SHAPE, refused in one MODE.
			default:
				carriedBy = append(carriedBy, fmt.Sprintf("%s as %v", shape.name, how.members()))
			}
		}
		if len(carriedBy) == len(over) {
			t.Errorf("ledger row %s refuses %s.%s, which every shape over that type carries: %s.\n"+
				"Either the row is a leftover from before the member landed, or the table is wrong.",
				row.ID, row.Type.Name(), row.Field, strings.Join(carriedBy, "; "))
		}
	}
	if seen == 0 {
		t.Fatal("no write-shape ledger rows were found; either the W- population was deleted or the shape types moved")
	}
}

// TestTheEncodedPatchDocumentUsesOnlyPublishedMembers drives the real encoder.
//
// The patch is the one write body this client builds as a DOCUMENT rather than
// a struct — presence is the signal and an explicit null is the clear, which no
// struct of `omitempty` pointers can express — so the compiler checks nothing
// about its member names. A typo would produce a 400 from a live server and
// pass every test in this package that does not dial one.
func TestTheEncodedPatchDocumentUsesOnlyPublishedMembers(t *testing.T) {
	published := bodyMembers(t, reflect.TypeOf(apigen.IssuePatchBody{}))

	patch := issueops.IssuePatch{}
	setEvery := reflect.ValueOf(&patch).Elem()
	var wantMembers []string
	for name, how := range patchShape(t).carried {
		setPatchField(t, setEvery.FieldByName(name))
		// members() rather than .member, because one field can drive several:
		// the ordered label edit is three flat siblings on this body, and reading
		// the singular would have wanted an empty name and missed two.
		wantMembers = append(wantMembers, how.members()...)
	}

	document, err := encodeIssuePatch(patch)
	if err != nil {
		t.Fatalf("a patch setting every carried member refused: %v", err)
	}
	for name := range document {
		if !published[name] {
			t.Errorf("the encoded patch carries %q, which IssuePatchBody does not publish: %v",
				name, sortedKeys(published))
		}
	}
	sort.Strings(wantMembers)
	got := sortedKeys(toSet(document))
	if strings.Join(got, ",") != strings.Join(wantMembers, ",") {
		t.Errorf("the encoded patch carries\n  %v\nwant the table's carried members\n  %v", got, wantMembers)
	}
}

// TestEveryWriteWireBodyIsClassified keeps the table above from going stale by
// omission: a write operation added to the transport seam with no entry here
// would leave its request shape entirely unguarded, and nothing else would say
// so.
//
// It reflects WriteWire's REAL method set rather than a hand-copied name slice,
// so a write method added to the transport seam tomorrow cannot escape the guard
// by being left off a second list — the failure the hand-copied version could
// not see.
func TestEveryWriteWireBodyIsClassified(t *testing.T) {
	classified := map[string]bool{}
	for _, shape := range writeShapes() {
		classified[shape.name] = true
	}

	// The WriteWire methods that carry NO request body to classify, each with the
	// reason. They are excluded by SHAPE, not by choice: RecallMemory, ForgetMemory
	// and ListMemories take a path or a query only, ListReadyWork takes a query,
	// and RememberMemory's body is memoryops' own type, pinned by the memory role's
	// own contract. The set is held to the real interface below, so a rename cannot
	// leave a phantom that silences a future body.
	bodiless := map[string]string{
		"RememberMemory": "carries memoryops.RememberRequest, pinned by the memory role's own contract",
		"RecallMemory":   "takes a key path only",
		"ForgetMemory":   "takes a key path only",
		"ListMemories":   "takes a search query only",
		"ListReadyWork":  "takes a url.Values query only",
		// The one method whose body is a SCALAR and whose interesting half is a
		// query. apigen.ClaimNextRequest publishes `actor` and nothing else — the
		// operation says so in as many words — so there is no role struct to
		// partition and no member a shape could hide. What COULD hide a dropped
		// member is the filter, and that travels as the ready listing's query
		// string through the ready encoder table, which the bijection gate above
		// already owns. Classifying a one-scalar body here would add a row that
		// can only ever say "actor is carried".
		"ClaimNextIssue": "carries apigen.ClaimNextRequest, whose only member is the actor; the FILTER travels as listReadyWork's query and is owned by the encoder bijection",
	}

	wireType := reflect.TypeOf((*WriteWire)(nil)).Elem()
	if wireType.NumMethod() == 0 {
		t.Fatal("WriteWire reflects no methods; the write door would be unguarded")
	}
	checked := 0
	for i := range wireType.NumMethod() {
		method := wireType.Method(i).Name
		if _, excluded := bodiless[method]; excluded {
			continue
		}
		checked++
		op := lowerFirst(method)
		if !classified[op] {
			t.Errorf("WriteWire.%s puts a body on the wire and no writeShape classifies it (op %q): "+
				"add it to writeShapes, or exclude it by shape in bodiless with a reason", method, op)
		}
	}
	if checked == 0 {
		t.Fatal("every WriteWire method was excluded; the reflective guard checked nothing")
	}

	// The exclusion list cannot outlive its methods: a name here that WriteWire no
	// longer declares is a stale phantom that would silence a future body reusing it.
	for name, why := range bodiless {
		if _, ok := wireType.MethodByName(name); !ok {
			t.Errorf("bodiless names %q (%s), which WriteWire no longer declares; drop it", name, why)
		}
	}
}

// lowerFirst maps a WriteWire method name onto its operationId, which is the
// same name with a lowercase initial: ClaimIssue -> claimIssue.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func patchShape(t *testing.T) writeShape {
	t.Helper()
	return shapeNamed(t, "updateIssue/patch")
}

// shapeNamed selects one classified shape BY NAME, which is what the two
// document gates need now that issueops.IssuePatch has two of them: selecting
// by source type would hand whichever came first in the table, and the two
// publish different members.
func shapeNamed(t *testing.T, name string) writeShape {
	t.Helper()
	for _, shape := range writeShapes() {
		if shape.name == name {
			return shape
		}
	}
	t.Fatalf("no write shape is named %q", name)
	return writeShape{}
}

// setPatchField sets one issueops.Field member so the encoder sees it as
// present. Two members are not Fields at all: Labels is a LabelPatch, whose
// three arms are all served, and Metadata is a MetadataPatch, whose four arms
// are classified as a shape of their own.
func setPatchField(t *testing.T, v reflect.Value) {
	t.Helper()
	if !v.CanSet() {
		t.Fatalf("cannot set a %s", v.Type())
	}
	switch v.Type() {
	case reflect.TypeOf(issueops.LabelPatch{}):
		// ALL THREE ARMS, because all three are carried now: the document gate
		// compares the encoded members against the table's spread, so driving
		// only the replace would let a dropped add_labels pass.
		replace := v.FieldByName("Replace")
		replace.FieldByName("Set").SetBool(true)
		replace.FieldByName("Value").Set(reflect.ValueOf([]string{"gate-sentinel"}))
		v.FieldByName("Add").Set(reflect.ValueOf([]string{"gate-added"}))
		v.FieldByName("Remove").Set(reflect.ValueOf([]string{"gate-removed"}))
		return
	case reflect.TypeOf(issueops.MetadataPatch{}):
		// The CLEAR, for the reason the comment below gives about a zero
		// nullable: a set Replace holding no bytes is the state a struct of
		// `omitempty` members cannot spell, so it is the arm worth driving here.
		replace := v.FieldByName("Replace")
		replace.FieldByName("Set").SetBool(true)
		return
	}
	set := v.FieldByName("Set")
	if !set.IsValid() {
		t.Fatalf("%s is neither a Field, a LabelPatch nor a MetadataPatch", v.Type())
	}
	set.SetBool(true)
	// The Value stays zero. Presence is what the encoder reads, and a zero
	// pointer on one of the nullable members is the CLEAR — which still has to
	// produce the member, so leaving it zero exercises the harder arm.
}

type fieldKey struct {
	owner reflect.Type
	field string
}

func ledgerRowsByField(t *testing.T) map[fieldKey]encode.Row {
	t.Helper()
	rows := map[fieldKey]encode.Row{}
	for _, row := range encode.Ledger() {
		if row.Type == nil {
			continue
		}
		rows[fieldKey{row.Type, row.Field}] = row
	}
	if len(rows) == 0 {
		t.Fatal("the divergence ledger carries no field-shaped rows")
	}
	return rows
}

func checkRefusalRow(t *testing.T, rows map[fieldKey]encode.Row, owner reflect.Type, field, id string) {
	t.Helper()
	row, ok := rows[fieldKey{owner, field}]
	if !ok {
		t.Errorf("%s.%s cites ledger row %s, which names no such field", owner.Name(), field, id)
		return
	}
	if row.ID != id {
		t.Errorf("%s.%s cites ledger row %s; the row naming that field is %s", owner.Name(), field, id, row.ID)
	}
	if row.Kind != encode.KindRefuse {
		t.Errorf("ledger row %s is %q; a partially-carried member must REFUSE the shape it cannot express", row.ID, row.Kind)
	}
}

// bodyMembers reads a wire body's published members off its JSON tags — the
// generated type is the document's own output, so this is the document talking.
func bodyMembers(t *testing.T, body reflect.Type) map[string]bool {
	t.Helper()
	if body == nil || body.Kind() != reflect.Struct {
		t.Fatalf("wire body %v is not a struct type", body)
	}
	out := map[string]bool{}
	for i := range body.NumField() {
		f := body.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Errorf("%s.%s carries no json member name", body.Name(), f.Name)
			continue
		}
		out[name] = true
	}
	if len(out) == 0 {
		t.Fatalf("wire body %s publishes no members", body.Name())
	}
	return out
}

// populatableFields lists the fields of a request shape a caller can set. It is
// the same rule encode/bijection_test.go applies to the read shapes: unexported
// fields are excluded because no caller outside their package can set one.
func populatableFields(rt reflect.Type) []string {
	var out []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				out = append(out, populatableFields(et)...)
				continue
			}
		}
		out = append(out, f.Name)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func toSet(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
