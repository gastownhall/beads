// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/ledger_fields.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import "reflect"

// The per-field half of divergence ledger v1: one row for every request-shape
// field this package's encoder cannot put on the v0 wire, plus the two it
// deliberately drops.
//
// These are instances of L12 — "an inexpressible filter refuses, never drops" —
// enumerated one field at a time, because a class rule that is not enumerated
// is a rule nothing can check. The bijection gate reads them back against
// reflection over the same structs, so a field added upstream lands here or
// fails CI.

const (
	fieldSpec = "D9 L12, D7, D8"
	// fieldPin and dropPin are the same test because it is the same property:
	// the sweep populates one field at a time and asserts the behavior the
	// table declares, so a refusal that stopped refusing and a drop that
	// started sending both fail there.
	fieldPin   = "TestEncoderHonorsEveryTableDisposition"
	dropPin    = "TestEncoderHonorsEveryTableDisposition"
	dropSpec   = "D9 (a degradation not in this table is a bug)"
	noWireList = "listIssues publishes no such parameter (internal/httpapi/reads.go handleListIssues; internal/httpapi/spec/openapi.v0.yaml)"
)

// briefRetiredWhy is the one retirement story behind the three rows #5586's
// `brief` parameter retired — the ready listing's, the issue listing's, and the
// ready bridge's Lite, which workapi assigns to the same field.
//
// IT IS A CONSTANT BECAUSE IT WAS THREE COPIES, and the three drifted the way
// copies do: the closing sentence stayed true of the WIRE and false of the
// CALLER for a whole wave, in triplicate, and correcting it meant finding all
// three. One string is one thing to keep true.
const briefRetiredWhy = "upstream #5586 published `brief` on GET /v0/beads/ready and GET /v0/beads/issues, which is the retirement path this row named. " +
	"Both page encoders send the parameter now — readyTable, listTable and the ready bridge all map it — so the client asks for the projection it was handed and the server leaves the text columns unselected. " +
	"The COUNT keeps the drop under E-ReadyRequest.Brief@countReadyWork, because countReadyWork publishes no such parameter and a cardinality has no rows to project. " +
	"WHAT DID NOT RETIRE WITH IT IS THE WIRE HALF OF THE MARKER, and only that half: types.Issue.IsLitePartial is `json:\"-\"` and never crosses, so a projected page arrives on the transport byte-identical to a page of genuinely textless rows — the same absence getIssue's brief_deps carries, deferred upstream by #5549. " +
	"THE CALLER-VISIBLE AMBIGUITY IS CLOSED, by client wave ga-f352s: `brief` is a parameter to the page decode rather than a field on anything, so httpReader hands it to wireRows (role_reader.go) and this client stamps IsLitePartial on every row of a page it ASKED to be projected — which is precisely what issueops.ListRequest.Brief's own leaf says a wire consumer distinguishes the two by — and never on a hydrated one. `bd list --long --brief` therefore prints \"Description: (omitted by --brief)\" over http exactly as it does locally, and TestHTTPCommentAndBriefRenderEndToEnd pins that line through the real binary"

func refusal(id string, ty reflect.Type, field, what, why, spec string) Row {
	return Row{
		ID: id, Kind: KindRefuse,
		Type: ty, Field: field,
		What: what, Why: why, SpecRow: spec, PinnedBy: fieldPin,
	}
}

// readyRequestRows are the ready vocabulary's two inexpressible members.
//
// [Removed: a third row, "E-ReadyRequest.ExcludeIDs", stood here for an
// enterprise-only id-exclusion filter. issueops.ReadyRequest carries no
// ExcludeIDs member in OSS (issueops/reader.go), so the row named a struct
// field that does not exist and TestEncoderTableClassifiesEveryRequestField
// failed "names a field that does not exist" against it. The matching table.go
// entry in readyFilterEntries() was removed alongside this row. The separate,
// still-real "E-WorkFilter.ExcludeIDs" row below (workFilterBridgeRows) is
// unaffected: workapi.WorkFilter genuinely carries that field.]
func readyRequestRows() []Row {
	return []Row{
		refusal("E-ReadyRequest.MolType", tyReadyRequest, "MolType",
			"a molecule-type restriction on ready work refuses",
			"neither listReadyWork nor countReadyWork publishes a mol-type parameter, and the ready query applies molecule typing inside — so a dropped restriction would answer with the wider set of every molecule type",
			fieldSpec),
		refusal("E-ReadyRequest.Offset", tyReadyRequest, "Offset",
			"a non-zero Offset on ready work refuses",
			"the wire publishes no offset parameter, and the store-backed role refuses a non-zero Offset itself with a typed *ErrUnsupported. A ready request carries no keyset position, so there is no portable way to page ready work at all — a caller that must page pages a ListRequest instead",
			fieldSpec+", D12"),
		{
			ID: "E-ReadyRequest.Brief", Kind: KindRetired,
			What:     "RETIRED — the ready listing SENDS the projection; only the count still drops it",
			Why:      briefRetiredWhy,
			SpecRow:  dropSpec,
			PinnedBy: dropPin,
		},
		{
			// The count's half of the retired row above, and it is a separate
			// row rather than a narrowing of that one because the two answer
			// different operations: countReadyWork publishes no `brief` at all,
			// so its drop survives the parameter that retired the listing's.
			ID: "E-ReadyRequest.Brief@countReadyWork", Kind: KindDegrade,
			Type: tyReadyRequest, Field: "Brief",
			What:     "the free-form-text projection is DROPPED on the COUNT, not refused: a cardinality has no rows to project",
			Why:      briefCountDropWhy,
			SpecRow:  dropSpec,
			PinnedBy: dropPin,
		},
		{
			ID: "E-ReadyRequest.Limit@countReadyWork", Kind: KindRefuse,
			Type: tyReadyRequest, Field: "Limit",
			What: "a Limit on a ready COUNT refuses",
			Why: "countReadyWork publishes no limit parameter and ReadyCounter.CountReady refuses one itself: a cardinality has no page, and CountReady's identity with the listing it sizes stops being true the moment one is accepted. " +
				"An explicit zero is refused with the rest — an unlimited count is the only kind there is",
			SpecRow:  fieldSpec + ", D8 row 2",
			PinnedBy: fieldPin,
		},
	}
}

// listRequestRows are the listing vocabulary's inexpressible members. It is the
// longest population in the ledger, and deliberately so: `bd list` publishes
// far more filters than the v0 listing does, and every one of them is a set
// this client would widen if it dropped it.
func listRequestRows() []Row {
	listRefusal := func(field, what, why string) Row {
		return refusal("E-ListRequest."+field, tyListRequest, field, what, why, fieldSpec)
	}
	rows := []Row{
		listRefusal("TitleSearch", "a title search refuses", noWireList),
		listRefusal("SpecPrefix", "a spec-id prefix restriction refuses", noWireList),
		listRefusal("IDFilter", "an explicit id set on the LISTING refuses",
			noWireList+"; the exact-ids question is getIssue's, reached through the SearchIssues bridge's exact-ids shape (D11), not through a listing filter"),
		listRefusal("LabelPattern", "a glob label filter refuses",
			noWireList+" — listReadyWork does publish label_pattern, which is exactly why the absence here has to refuse rather than fall through"),
		listRefusal("LabelRegex", "a regex label filter refuses",
			noWireList+" — as for LabelPattern, listReadyWork publishes label_regex and the listing does not"),
		listRefusal("TitleContains", "a title substring filter refuses", noWireList),
		listRefusal("DescContains", "a description substring filter refuses", noWireList),
		listRefusal("NotesContains", "a notes substring filter refuses", noWireList),
		listRefusal("ExternalContains", "an external-ref substring filter refuses", noWireList),
		listRefusal("ExternalRef", "an exact external-ref filter refuses", noWireList),
		listRefusal("UpdatedAfter", "an updated-after bound refuses", noWireList+" — only the created bounds are published"),
		listRefusal("UpdatedBefore", "an updated-before bound refuses", noWireList+" — only the created bounds are published"),
		listRefusal("ClosedAfter", "a closed-after bound refuses", noWireList),
		listRefusal("ClosedBefore", "a closed-before bound refuses", noWireList),
		listRefusal("DeferAfter", "a defer-after bound refuses", noWireList),
		listRefusal("DeferBefore", "a defer-before bound refuses", noWireList),
		listRefusal("DueAfter", "a due-after bound refuses", noWireList),
		listRefusal("DueBefore", "a due-before bound refuses", noWireList),
		listRefusal("EmptyDesc", "the empty-description predicate refuses", noWireList),
		listRefusal("NoAssignee", "the unassigned predicate refuses",
			noWireList+" — listReadyWork publishes `unassigned`; the listing does not"),
		listRefusal("NoLabels", "the no-labels predicate refuses", noWireList),
		listRefusal("Priority", "an exact priority filter refuses",
			noWireList+" — listReadyWork publishes `priority`; the listing does not"),
		listRefusal("PriorityMin", "a minimum-priority bound refuses", noWireList),
		listRefusal("PriorityMax", "a maximum-priority bound refuses", noWireList),
		listRefusal("PinnedFlag", "selecting the pinned-FLAG rows refuses",
			noWireList+". The pinned STATUS is reachable through `status`; the flag is a separate predicate at any status and has no parameter"),
		listRefusal("NoPinnedFlag", "holding the unflagged predicate in place refuses",
			noWireList+". It changes nothing on a default listing and NARROWS under `all` or a pinned/hooked status — which is precisely when dropping it would widen the answer"),
		listRefusal("ExcludeTypes", "a type exclusion refuses",
			noWireList+" — listReadyWork publishes exclude_type; the listing publishes only the four include_* toggles"),
		listRefusal("NoParent", "the top-level-only predicate refuses", noWireList),
		listRefusal("MolType", "a molecule-type restriction refuses", noWireList),
		listRefusal("WispType", "a wisp-type restriction refuses",
			noWireList+". The listing now ADMITS the wisp plane — include_ephemeral landed upstream and the encoder sends it (L1 retired) — so this refusal is no longer redundant with an invisible plane: it is the only thing standing between a caller and a listing that returns every wisp type when they named one"),
		listRefusal("DeferredFlag", "the deferred predicate refuses", noWireList),
		listRefusal("OverdueFlag", "the overdue predicate refuses", noWireList),
		listRefusal("ReadyFlag", "switching a listing onto the ready set refuses",
			"ReadyFlag is not a filter but a change of QUESTION: it selects the blocker-aware ready query, which reads a narrower filter vocabulary than a ListRequest can describe. Over the wire that question is listReadyWork, a different operation with a different parameter table. "+
				"Routing it there would have to decide, silently, which of this request's fields survive the crossing — so v1 refuses and the retirement path is an explicit route in the store, not a re-encode here"),
		listRefusal("IncludeComments", "hydrating every row's comment bodies refuses",
			noWireList+". listIssues documents `comments` as always absent on its rows; a caller that needs an issue's comments reads getIssue with include_comments, one issue at a time"),
		listRefusal("Offset", "a non-zero Offset refuses",
			noWireList+". The portable page is the keyset position, which the pager walks (D8 row 1); the store-backed role refuses a non-zero Offset itself"),
	}
	return append(rows,
		Row{
			ID: "E-ListRequest.SkipLabels", Kind: KindDegrade,
			Type: tyListRequest, Field: "SkipLabels",
			What: "the label-hydration opt-out is DROPPED, not refused: the wire hydrates labels either way",
			Why: "the listing publishes no hydration parameter, so the client cannot ask the server to skip the labels JOIN. Dropping it hands the caller MORE data than it asked for and never fewer — the row set, its order, Parent and the has-more verdict are exactly what they would have been — so it cannot be misread as a narrower or wider answer. " +
				"It is a degradation rather than a refusal because refusing would make `bd list --skip-labels` a hard error for a performance flag whose only observable effect over http is that the server pays for a join. The retirement path is a hydration parameter on the wire",
			SpecRow:  dropSpec,
			PinnedBy: dropPin,
		},
		Row{
			ID: "E-ListRequest.Brief", Kind: KindRetired,
			What:     "RETIRED — `bd list` over http sends the projection",
			Why:      briefRetiredWhy,
			SpecRow:  dropSpec,
			PinnedBy: dropPin,
		},
		Row{
			ID: "E-ListRequest.IncludeAllTypes", Kind: KindRefuse,
			Type: tyListRequest, Field: "IncludeAllTypes",
			What: "the never-hide-a-bead intent refuses",
			Why: "it is the UNION of four intents the wire does publish — include_templates, include_gates, include_infra, include_ephemeral — and encoding it as those four would be a client-side restatement of a union whose whole point is that a FIFTH suppression added to workapi is lifted by it automatically. " +
				"The day that fifth lands, the restatement silently stops lifting it and the listing hides beads from a caller whose contract is that it hides none. Dropping it narrows the answer for the same reason, so it refuses until the wire publishes the intent itself",
			SpecRow:  fieldSpec,
			PinnedBy: fieldPin,
		},
		Row{
			ID: "E-ListRequest.SkipCounts", Kind: KindDegrade,
			Type: tyListRequest, Field: "SkipCounts",
			What: "the cardinality-hydration opt-out is DROPPED, not refused: the wire hydrates the three counts either way",
			Why: "the same reasoning as SkipLabels, and here refusing would be actively wrong: EVERY text rendering of `bd list` sets SkipCounts (cmd/bd/list.go:284, and its proxied twin), so a refusal would kill the default listing over http. " +
				"Dropping it means real counts arrive where zeros were expected, and a caller that must read a zero as UNKNOWN is safe reading a true count. The cost is the three aggregate joins — including the reverse-blocker one the embedded planner cannot index — paid on a page that prints none of them",
			SpecRow:  dropSpec,
			PinnedBy: dropPin,
		},
	)
}

// queryRequestRows: the boolean-query surface publishes almost its whole
// vocabulary, so there is one member left over.
func queryRequestRows() []Row {
	return []Row{
		refusal("E-QueryRequest.Offset", tyQueryRequest, "Offset",
			"a non-zero Offset on a boolean query refuses",
			"queryIssues publishes no offset parameter — the document deliberately omits one, because the two database sources this server can be built on disagree about whether they can honor it — and the store-backed role refuses one uniformly",
			fieldSpec),
	}
}

// bridgeWhy is the SearchIssues bridge's whole rule, written once. Every field
// row below is an instance of it.
const bridgeWhy = "the off-role SearchIssues bridge serves TWO shapes and no others: the exact-ids fast path D11 special-cases to getIssue, and the ParentID-only descendant walk D4 maps to paged listIssues?parent=<id>. " +
	"Any other populated field is a filter the bridge would have to drop to answer at all, and a dropped filter widens the set invisibly — so it refuses"

// issueFilterRows classify every member of types.IssueFilter against the
// SearchIssues bridge.
//
// The three fields the bridge DOES read — IDs, ParentID and Limit — still get
// rows, because each is refused in the shape it does not belong to: an IDs set
// alongside a ParentID is neither shape, and a ParentID or a Limit alongside an
// exact-ids lookup is a filter getIssue cannot apply.
func issueFilterRows() []Row {
	bridgeRefusal := func(field string) Row {
		return refusal("E-IssueFilter."+field, tyIssueFilter, field,
			"a populated IssueFilter."+field+" refuses on the SearchIssues bridge", bridgeWhy,
			"D8 (off-role raw methods), D4, D11, D9 L12")
	}
	rows := make([]Row, 0, len(issueFilterBridgeRefusals)+4)
	for _, field := range issueFilterBridgeRefusals {
		rows = append(rows, bridgeRefusal(field))
	}
	return append(rows,
		Row{
			// No Type/Field: this row is about a PAIR of fields, and pinning it
			// to either one would make the refusal read as if the other were
			// fine on its own.
			ID: "E-IssueFilter.shapeConflict", Kind: KindRefuse,
			What: "an id set and a ParentID together refuse — the pair is NEITHER expressible shape",
			Why: "the exact-ids shape dials getIssue per id and applies no filter at all, so honoring the parent restriction would mean discarding answers client-side; the descendant walk dials listIssues?parent=<id>, which publishes no id filter, so honoring the ids would mean intersecting two answers no single wire call asked for. " +
				"Both readings are drops wearing a filter's clothes, so the combination refuses before a shape is chosen",
			SpecRow:  "D4, D11, D9 L12",
			PinnedBy: "TestASearchFilterMatchingNeitherShapeRefuses",
		},
		refusal("E-IssueFilter.Limit@exactIDs", tyIssueFilter, "Limit",
			"a Limit alongside an id set refuses",
			"getIssue answers one row per call, so a Limit could only be applied by discarding answers the caller named explicitly",
			"D11"),
		Row{
			ID: "E-IssueFilter.IDs@bound", Kind: KindRefuse,
			Type: tyIssueFilter, Field: "IDs",
			What:     "an id set larger than MaxExactIDs refuses",
			Why:      "the exact-ids shape costs one getIssue per id, so an unbounded set is an unbounded burst at one shared server. The bound is 100 and is a GUESS: open question 6 asks for the real call-site fan-out to be measured before GA",
			SpecRow:  "D11, open question 6",
			PinnedBy: "TestTheExactIDsFanOutIsBounded",
		},
		Row{
			ID: "E-IssueFilter.noShape", Kind: KindRefuse,
			What:     "a raw SearchIssues call matching neither expressible shape refuses",
			Why:      bridgeWhy + ". A filter naming no ids and no parent — the unbounded `everything` shape included — is not one of them",
			SpecRow:  "D8 (off-role raw methods), D4, D11, D9 L12",
			PinnedBy: "TestASearchFilterMatchingNeitherShapeRefuses",
		},
		Row{
			ID: "E-bridge-parent-shape", Kind: KindRetired,
			What: "RETIRED — the ParentID-only shape was unreachable from `bd list --parent` as the CLI builds it, and the walk refused over http",
			Why: "the escalation this row raised (ga-2ieek) was resolved by spec revision 6b428bf63 as semantic inversion: the wire publishes INTENTS (`all`, `include_templates`, `include_gates`, `include_infra`) rather than materialized exclusions, and the server's role re-derives the exclusions from the same builder against its own authoritative vocabulary. " +
				"The bridge therefore inverts the derived members back to intents instead of encoding them, and the objection recorded here — that recognizing the baseline would be an unsanctioned second encoder — is answered by VERIFYING each candidate through workapi.BuildListFilter itself rather than reimplementing it. The live rules are the P-IssueFilter.* population",
			SpecRow:  "D4 (`bd list --parent` hierarchical walk), D9 L12, escalation ga-2ieek",
			PinnedBy: "TestTheParentWalkInvertsTheDerivedDefaults",
		},
	)
}

// parentWalkRows classify types.IssueFilter against D4's descendant walk, which
// reads the SAME struct as the exact-ids shape and reads it differently.
//
// Three populations, and the split is the whole content of the ga-2ieek
// resolution:
//
//   - the six DERIVED DEFAULTS workapi.BuildListFilter populates from the
//     intent flags. A value the same builder would have derived for the intents
//     being sent is accounted for and sends nothing; every other value refuses
//     here. These are the rows the inversion raises by id;
//   - the four members the walk carries that decide nothing, DROPPED with the
//     argument that says why the difference cannot be read as a narrower or
//     wider answer;
//   - everything else, refused exactly as it is on the other shape — the walk
//     just reaches it from a filter that has more populated than an id lookup
//     ever does.
const parentWalkSpec = "D4 (`bd list --parent` hierarchical walk), D9 L12, escalation ga-2ieek"

func parentWalkRows() []Row {
	const pin = "TestTheParentWalkInvertsTheDerivedDefaults"
	derived := func(field, what, why string) Row {
		return Row{
			ID: "P-IssueFilter." + field, Kind: KindRefuse,
			Type: tyIssueFilter, Field: field,
			What: what, Why: why, SpecRow: parentWalkSpec, PinnedBy: pin,
		}
	}
	walkRefusal := func(field string) Row {
		return Row{
			ID: "P-IssueFilter." + field, Kind: KindRefuse,
			Type: tyIssueFilter, Field: field,
			What: "a populated IssueFilter." + field + " refuses on the descendant walk",
			Why: "listIssues publishes no parameter for it, and the walk's inversion accounts only for the members BuildListFilter DERIVES plus the explicit filters the operation does publish. " +
				"Anything left over is a restriction the bridge would have to drop to answer at all, and a dropped filter widens the set invisibly",
			SpecRow: parentWalkSpec, PinnedBy: pin,
		}
	}
	drop := func(field, what, why string) Row {
		return Row{
			ID: "P-IssueFilter." + field, Kind: KindDegrade,
			Type: tyIssueFilter, Field: field,
			What: what, Why: why, SpecRow: parentWalkSpec + ", D9", PinnedBy: pin,
		}
	}

	rows := []Row{
		derived("ExcludeStatus", "a status exclusion that is not the derived default refuses",
			"listIssues publishes no exclude_status: the wire publishes the INTENT (`all`) and the server re-derives the exclusions from the same builder against its own authoritative vocabulary, which additionally heals L7's degraded client-side set for the walk. "+
				"An exclusion list no intent reproduces is therefore unstatable, and dropping it would widen the listing to closed work"),
		derived("Pinned", "a pinned predicate that is not the derived default refuses",
			"the pinned default is the FOURTH derived member and has no parameter of its own: `--pinned` selects the flagged rows and `--no-pinned` holds the unflagged predicate in place under `--all`, and the wire can express neither. "+
				"Its derived value is a function of the `all` intent alone — upstream #5333 stopped an every-status selector from forcing it, so `--status all` and `--all` are now one filter and one wire question — and a value that intent does not reproduce is a user's own predicate, which NARROWS, and that is precisely when dropping it would answer a different question"),
		derived("IsTemplate", "a template predicate that is not the derived default refuses",
			"listIssues publishes include_templates, an intent, not an is_template predicate. `&false` is what the absent flag derives and sends nothing; nil is what `--include-templates` derives and sends the flag; `&true` selects templates ONLY, which no intent on this operation can say"),
		derived("ExcludeTypes", "a type exclusion beyond the derived gate and infra members refuses",
			"listIssues publishes include_gates and include_infra — intents — and no exclude_type. The gate member and the whole infra vocabulary are what their absent flags derive; a leftover member is a user's `--exclude-type`, and dropping it would return the type they asked to hide"),
		derived("Ephemeral", "an ephemeral predicate that is not the derived default refuses",
			"BuildListFilter sets it only for an infra `--type`, from the same flag the walk already encodes; the wire publishes no ephemeral PREDICATE on the listing — include_ephemeral is an intent that ADMITS the plane, not a selector that restricts to it — so any other value is unstatable"),
		derived("SkipWisps", "a wisp-merge opt-out that is not the derived default refuses",
			"NARROWED by client wave ga-mijra, which closed the client-side gap this row used to record. The walk now encodes include_ephemeral — SkipWisps going false IS that intent, and it is read back off the member the way every other derived default is — so a wisp-inclusive descendant listing crosses instead of refusing, which is the shape gc's TierBoth reads take. "+
				"What still refuses is a SkipWisps no set of intents reproduces: holding the plane out while include_infra or an infra `--type` admits it. BuildListFilter never builds that pair, so it is a hand-made filter rather than a flag combination, and dropping the opt-out would merge a plane the caller asked to skip"),
		drop("SortBy", "the walk's sort key is DROPPED",
			"findAllDescendants accumulates every level into a map keyed by id, so no per-level order reaches the answer: the walk's own assembly discards it before the caller sees it. This is a drop with nothing to drop, and the rendering order is the tree renderer's"),
		drop("SortDesc", "the walk's sort direction is DROPPED",
			"the other half of the order findAllDescendants discards; see P-IssueFilter.SortBy"),
		drop("SkipLabels", "the label-hydration opt-out is DROPPED",
			"the listing publishes no hydration parameter, so the client cannot ask the server to skip the labels join. Dropping it hands the caller MORE data and never fewer rows, so it cannot be misread as a narrower or wider answer"),
		drop("SkipCounts", "the cardinality-hydration opt-out is DROPPED",
			"the same absence as SkipLabels, and here refusing would be actively wrong: every text rendering of `bd list` sets it, so a refusal would kill the walk's DEFAULT rendering — the one behavior D4 classifies the walk as served in order to keep"),
	}

	for _, field := range parentWalkRefusals {
		rows = append(rows, walkRefusal(field))
	}
	return append(rows, Row{
		// No Type/Field: this row is about the whole recognition failing, not
		// about one member. It fires when the builder rejects every candidate
		// intent, which means the filter names vocabulary this client cannot
		// reproduce at all.
		ID: "P-IssueFilter.unrecognized", Kind: KindRefuse,
		What: "a descendant-walk filter whose intents this client cannot reproduce at all refuses",
		Why: "the inversion proves a candidate by RE-RUNNING workapi.BuildListFilter and comparing the derived members, so a candidate the builder itself rejects — an unknown status, a vocabulary the client's degraded copy does not carry (L7) — is never compared. " +
			"When every candidate is rejected that way there is no member to blame, and refusing on the recognition is the only honest answer",
		SpecRow:  parentWalkSpec + ", D9 L7",
		PinnedBy: pin,
	})
}

// parentWalkRefusals enumerates the IssueFilter members that refuse on the
// descendant walk: everything BuildListFilter can carry that is neither a
// derived default nor a filter listIssues publishes.
//
// Written out rather than derived, for the reason issueFilterBridgeRefusals is:
// a list computed from the struct would absorb a field added upstream, and the
// bijection gate exists to catch exactly that arrival.
var parentWalkRefusals = []string{
	"Priority", "LabelPattern", "LabelRegex",
	"TitleSearch", "IDPrefix", "SpecIDPrefix",
	"TitleContains", "DescriptionContains", "NotesContains", "ExternalRefContains", "ExternalRef",
	"UpdatedAfter", "UpdatedBefore", "ClosedAfter", "ClosedBefore", "StartedAfter", "StartedBefore",
	"AfterCreatedAt", "AfterID", "AfterPriority",
	"EmptyDescription", "NoAssignee", "NoLabels", "PriorityMin", "PriorityMax",
	"SourceRepo", "EphemeralTier", "IsBlocked", "NoParent", "MolType", "WispType",
	"Deferred", "DeferAfter", "DeferBefore", "DueAfter", "DueBefore", "Overdue",
	"IncludeDependencies", "NoIDShrink", "Offset", "Lite",
}

// issueFilterBridgeRefusals enumerates the IssueFilter members that refuse in
// BOTH bridge shapes, in declaration order.
//
// It is written out rather than reflected FROM types.IssueFilter on purpose: a
// list derived from the struct would silently absorb a field added upstream,
// which is the one event this whole gate exists to catch. Both the ledger and
// the encoder tables read this one enumeration, so the two cannot disagree.
var issueFilterBridgeRefusals = []string{
	"Status", "Statuses", "Priority", "IssueType", "Assignee",
	"Labels", "LabelsAny", "ExcludeLabels", "LabelPattern", "LabelRegex",
	"TitleSearch", "IDPrefix", "SpecIDPrefix",
	"TitleContains", "DescriptionContains", "NotesContains", "ExternalRefContains", "ExternalRef",
	"CreatedAfter", "CreatedBefore", "UpdatedAfter", "UpdatedBefore",
	"ClosedAfter", "ClosedBefore", "StartedAfter", "StartedBefore",
	"AfterCreatedAt", "AfterID", "AfterPriority",
	"EmptyDescription", "NoAssignee", "NoLabels", "PriorityMin", "PriorityMax",
	"SourceRepo", "Ephemeral", "EphemeralTier", "Pinned", "IsBlocked", "IsTemplate",
	"NoParent", "MolType", "WispType", "ExcludeStatus", "ExcludeTypes",
	"Deferred", "DeferAfter", "DeferBefore", "DueAfter", "DueBefore", "Overdue",
	"MetadataFields", "HasMetadataKey",
	"IncludeDependencies", "SkipLabels", "SkipCounts", "SkipWisps", "NoIDShrink",
	"Offset", "SortBy", "SortDesc", "MaxRows", "MaxRowsSource", "Lite",
}

// workFilterRows classify types.WorkFilter against the reverse ready bridge.
//
// The bridge exists because `bd ready`'s listing is still raw at tip, so the
// client has to map a WorkFilter back onto ready parameters until the front
// door moves onto issueops.Reader. It is deliberately throwaway; what is not
// throwaway is that it refuses every field it cannot express (L12).
func workFilterRows() []Row {
	bridge := func(field, what, why string) Row {
		return refusal("E-WorkFilter."+field, tyWorkFilter, field, what, why,
			"D8 (off-role ready bridge), D9 L12")
	}
	return []Row{
		{
			// The one DEGRADE on this bridge, and it is here rather than among
			// the refusals because it is not a filter: it is the same free-form
			// text projection ReadyRequest.Brief carries, reached from the
			// legacy shape. WorkFilter.Lite IS ReadyRequest.Brief — workapi
			// assigns one to the other — so the two dispositions have to agree
			// or the same request answers differently depending on which door
			// it came in.
			ID: "E-WorkFilter.Lite", Kind: KindRetired,
			What:     "RETIRED — the ready bridge maps Lite onto `brief`, the same parameter ReadyRequest.Brief now sends",
			Why:      briefRetiredWhy,
			SpecRow:  dropSpec,
			PinnedBy: dropPin,
		},
		{
			// A DERIVED DEFAULT, not a flat refusal (D4's parent-walk inversion,
			// applied here): the one value the server re-derives drops, every
			// other refuses. KindRefuse because the disposition is DispInverted,
			// which the encoder drives exactly as a refusal — its sentinel value
			// is by construction not the derived one.
			ID: "E-WorkFilter.Status", Kind: KindRefuse,
			Type: tyWorkFilter, Field: "Status",
			What:     "a ready-work status that is not the derived default refuses; the default (open only) is DERIVED server-side and drops",
			Why:      "listReadyWork publishes no status parameter: handleReady decodes a status-LESS request and the server re-derives ready work's status through workapi.BuildReadyFilter, which is unconditionally StatusOpen — open work only, the set `bd list --ready` shows. The client sends that one derived value as the absence the server reads it back from, so every real `bd ready` and `bd ready --json` (whose filter BuildReadyFilter always stamps StatusOpen) round-trips to the identical rows. Any other status is unstatable and refuses: a named status would answer over the query's open-only default rather than the set named, and the empty status is the storage layer's own open+in_progress default, which this operation cannot express either",
			SpecRow:  "D8 (off-role ready bridge), D9 L12, D4 (derived-default inversion, escalation ga-2ieek)",
			PinnedBy: fieldPin,
		},
		bridge("Statuses", "a multi-status OR set on ready work refuses",
			"listReadyWork publishes no status parameter, and an OR-set is neither the server's derived default (open only) nor anything the wire can carry — dropping it would widen the answer to statuses the caller did not ask for, so it refuses alongside a non-default Status (L12)"),
		bridge("MoleculeID", "restricting ready work to one molecule's direct children refuses",
			"listReadyWork publishes `parent` for the RECURSIVE descendant restriction and nothing for direct membership; the two are different sets, so `parent` is not a substitute"),
		bridge("MolType", "a molecule-type restriction on ready work refuses",
			"no wire parameter — the same absence E-ReadyRequest.MolType records, reached from the other source shape"),
		bridge("WispType", "a wisp-type restriction on ready work refuses",
			"listReadyWork publishes include_ephemeral, which admits the wisp plane wholesale, and nothing that selects a wisp TYPE within it"),
		bridge("ExcludeIDs", "an id exclusion set on ready work refuses",
			"listReadyWork publishes no id-exclusion parameter; the set is what the external-dependency policy decorator (upstream #4753) injects to hide sources behind unsatisfied external blockers, and dropping it would widen the answer to exactly the issues the caller's policy excluded (L12). A server that advertises policy.external_dependencies applies the policy itself, so the client never builds the set for it"),
		bridge("Offset", "a non-zero Offset on ready work refuses",
			"no wire parameter, and ready work carries no keyset position to page by; see E-ReadyRequest.Offset"),
	}
}
