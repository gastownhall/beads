// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/table.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"reflect"
	"sync"
)

// The encoder table: the field-by-field disposition of every read request shape
// this client puts on the v0 wire.
//
// It exists because the encoders below it are hand-written, and a hand-written
// encoder's failure mode is silence — a filter it never learned about is a
// filter it never sends, and the server can only reject parameters it RECEIVES
// (D7). So the encoder is not the record of what it encodes; this is. The
// bijection gate reads it three ways at once:
//
//   - against reflection over the source struct, so every populatable field is
//     classified here or carries a divergence-ledger row, and an upstream field
//     added tomorrow fails CI rather than shipping as a silent drop;
//   - against internal/httpapi/spec/openapi.v0.yaml, so every parameter the
//     operation publishes is either driven by a field or explicitly reserved;
//   - against the encoders themselves, by populating one field at a time and
//     asserting the behavior this table declares.
//
// A table with no entries, or a ledger row nothing references, fails the gate
// too: completeness is the gate's own invariant, not something a reader has to
// take on trust.

// Op names a v0 operation, spelled as its operationId in the document.
type Op string

const (
	OpListReadyWork  Op = "listReadyWork"
	OpCountReadyWork Op = "countReadyWork"
	OpListIssues     Op = "listIssues"
	OpQueryIssues    Op = "queryIssues"
	OpCountIssues    Op = "countIssues"
	OpGetIssue       Op = "getIssue"

	// The write operations carry no encoder table: their requests are JSON
	// bodies rather than query strings, so there is no parameter set for the
	// bijection gate to read. They are named here anyway because a refusal has
	// to say WHICH operation refused, and the alternative is a second spelling
	// of an operationId in the store package.
	OpClaimIssue        Op = "claimIssue"
	OpCreateIssue       Op = "createIssue"
	OpCloseIssue        Op = "closeIssue"
	OpReopenIssue       Op = "reopenIssue"
	OpUpdateIssue       Op = "updateIssue"
	OpAddDependencies   Op = "addDependencies"
	OpRemoveDependency  Op = "removeDependency"
	OpBatchCloseIssues  Op = "batchCloseIssues"
	OpRememberMemory    Op = "rememberMemory"
	OpForgetMemory      Op = "forgetMemory"
	OpSweepIssues       Op = "sweepIssues"
	OpDeleteIssues      Op = "deleteIssues"
	OpBatchCreateIssues Op = "batchCreateIssues"
	OpApplyBatch        Op = "applyBatch"

	// countDependencyEdges carries no encoder table either, for a different
	// reason: its request is ANCHORED rather than a predicate — four members,
	// all four published — so it is built inline beside the three graph reads
	// that share its collection. Its refusal still has to name an operation.
	OpCountDependencyEdges Op = "countDependencyEdges"
)

// Disposition is what becomes of one request field on its way to the wire.
type Disposition string

const (
	// DispParam is encoded as the named query parameter.
	DispParam Disposition = "param"
	// DispPath is carried in the request path rather than the query string.
	DispPath Disposition = "path"
	// DispServerFixed is a field the SERVER pins: the client sends nothing and
	// the decoded request carries the server's own value. It is not a drop —
	// the value is knowable, and the gate asserts it.
	DispServerFixed Disposition = "server-fixed"
	// DispClientSide has no wire member and is honored by client-side
	// machinery — the pager, the sort comparator, the max-rows circuit breaker.
	// The gate asserts the encoder emits nothing for it, which is what keeps
	// "the client honors this" from quietly becoming "the client ignores this".
	DispClientSide Disposition = "client-side"
	// DispDropped has no wire member and is deliberately NOT honored. Every one
	// carries a divergence-ledger row arguing why the difference cannot be read
	// as a narrower or wider answer than the caller asked for.
	DispDropped Disposition = "dropped"
	// DispRefused has no wire member and FAILS when populated, carrying the
	// ledger row that says why. This is the default disposition of anything
	// inexpressible: L12, refuse-not-drop.
	DispRefused Disposition = "refused"
	// DispInverted is a DERIVED DEFAULT: a member the wire publishes no
	// parameter for, which the SERVER re-derives from the intent parameters the
	// encoder sends in its place (D4's parent-walk inversion, parentwalk.go).
	//
	// It is its own disposition rather than a param or a refusal because it is
	// neither: nothing about the member crosses the wire, and yet a populated
	// one is not an error. What decides is the VALUE — the one the same builder
	// would have derived for the intents being sent is accounted for, and every
	// other value refuses with the row named here. The gate drives it exactly as
	// a refusal, because its sentinel value is by construction not a derived
	// one.
	DispInverted Disposition = "inverted"
	// DispNested is a member that is itself a whole request shape, encoded by
	// the table over that shape rather than by this one.
	//
	// countIssues is why it exists: CountByGroupRequest carries the scalar
	// predicate BY NAME (`Filter`) plus the dimension, so its table would
	// otherwise have to restate every classification the count table
	// already makes — two copies of one partition, which is the drift every
	// other gate here is written to prevent.
	//
	// It is not a drop and not a param. The gate drives it by populating the
	// nested shape whole and asserting the query CHANGED, which is the property
	// that matters: a grouped count whose builder forgot the caller's predicate
	// answers buckets over the wrong set, and every bucket in it would look
	// perfectly plausible.
	DispNested Disposition = "nested"
)

// FieldEntry is one field's disposition.
type FieldEntry struct {
	// Name is the Go field on the source struct.
	Name string
	// Disposition is what becomes of it.
	Disposition Disposition
	// Param is the wire parameter (DispParam) or the path template segment
	// (DispPath).
	Param string
	// Decoded names the field the SERVER's decoder lands this value on. For the
	// request shapes the server shares with the client it is Name; for the two
	// bridges — which map a legacy filter onto a role request — it is the
	// corresponding member of the server-side type, and stating it here is what
	// lets the round-trip gate compare the two without a hand-written twin.
	Decoded string
	// Fixed is the value the server pins (DispServerFixed).
	Fixed string
	// Why is required on every disposition but DispParam and DispPath.
	Why string
	// Ledger is the divergence-ledger row id, required on DispDropped and
	// DispRefused and permitted elsewhere where a row is worth citing.
	Ledger string
}

// ReservedParam is a parameter the operation publishes that NO request field
// drives. Each one is a hole in the "every parameter comes from a field" half
// of the bijection, so each carries its reason.
type ReservedParam struct {
	Name string
	Why  string
}

// Table is one source shape's disposition against one operation.
type Table struct {
	// Op is the operation this table encodes for.
	Op Op
	// Shape names a bridge's arm, and is empty on a primary table.
	Shape string
	// Source is the struct the caller hands the client.
	Source reflect.Type
	// Target is the request the server's decoder builds.
	Target reflect.Type
	// Primary marks the ONE table per operation whose parameter set must equal
	// the operation's published parameter set. A bridge maps a legacy filter
	// onto a subset of an operation it does not own, so it carries no such
	// obligation — and saying that here is what stops a bridge from being read
	// as evidence that the operation is fully mapped.
	Primary  bool
	Reserved []ReservedParam
	Fields   []FieldEntry
}

// Tables returns the whole encoder table, one entry per (operation, source
// shape) pair.
func Tables() []Table {
	return []Table{
		readyTable(),
		readyCountTable(),
		listTable(),
		queryTable(),
		countTable(),
		countByGroupTable(),
		getTable(),
		readyBridgeTable(),
		searchExactIDsTable(),
		searchParentWalkTable(),
	}
}

// TableFor returns the table for one operation and shape.
func TableFor(op Op, shape string) (Table, bool) {
	for _, t := range cachedTables() {
		if t.Op == op && t.Shape == shape {
			return t, true
		}
	}
	return Table{}, false
}

// cachedTables is what the ENCODER reads, so a per-page walk does not rebuild
// two hundred table entries per request. Tables() keeps building a fresh copy
// for everyone else, which is what stops a caller from editing the table the
// encoder consults.
var cachedTables = sync.OnceValue(Tables)

// briefCountDropWhy is what remains of the free-form-text projection's old
// three-shape drop after upstream #5586 published `brief` on both PAGE
// operations. The two listings now send it (readyTable, listTable and the ready
// bridge), so this reason is scoped to the COUNT, which publishes no such
// parameter and has no rows to project.
const briefCountDropWhy = "countReadyWork publishes no `brief` and answers a cardinality, so there is nothing to project: the parameter chooses what is HYDRATED and never which rows match, and a count reads neither. " +
	"Dropping it is SkipLabels' and SkipCounts' argument exactly — the total is what it would have been — and refusing would make a performance flag a hard error on the one operation where it can cost nothing. " +
	"The two page operations retired this drop and send the parameter; a caller that populates Brief for a count is asking a question a cardinality does not have"

func param(name, wire string) FieldEntry {
	return FieldEntry{Name: name, Disposition: DispParam, Param: wire, Decoded: name}
}

func paramTo(name, wire, decoded string) FieldEntry {
	return FieldEntry{Name: name, Disposition: DispParam, Param: wire, Decoded: decoded}
}

func refused(name, ledger string) FieldEntry {
	return FieldEntry{Name: name, Disposition: DispRefused, Ledger: ledger}
}

func dropped(name, ledger, why string) FieldEntry {
	return FieldEntry{Name: name, Disposition: DispDropped, Ledger: ledger, Why: why}
}

func clientSide(name, why string) FieldEntry {
	return FieldEntry{Name: name, Disposition: DispClientSide, Why: why}
}

func serverFixed(name, fixed, why string) FieldEntry {
	return FieldEntry{Name: name, Disposition: DispServerFixed, Fixed: fixed, Why: why}
}

// readyFilterEntries are the filters listReadyWork and countReadyWork share.
//
// They are one list for the reason internal/httpapi's readyFilters is one
// function: the count answers with the SIZE of the page the listing returns, and
// a filter one of them sent and the other did not would make that identity false
// for any client that set it.
func readyFilterEntries() []FieldEntry {
	return []FieldEntry{
		paramTo("IssueType", "type", "IssueType"),
		param("Assignee", "assignee"),
		param("Unassigned", "unassigned"),
		param("Labels", "label"),
		param("LabelsAny", "label_any"),
		param("ExcludeLabels", "exclude_label"),
		param("LabelPattern", "label_pattern"),
		param("LabelRegex", "label_regex"),
		param("Priority", "priority"),
		paramTo("ParentID", "parent", "ParentID"),
		refused("MolType", "E-ReadyRequest.MolType"),
		param("IncludeDeferred", "include_deferred"),
		param("IncludeEphemeral", "include_ephemeral"),
		paramTo("ExcludeTypes", "exclude_type", "ExcludeTypes"),
		paramTo("MetadataFields", "metadata_field", "MetadataFields"),
		param("HasMetadataKey", "has_metadata_key"),
		// [Removed: an enterprise-only `refused("ExcludeIDs", ...)` row stood
		// here, naming a field issueops.ReadyRequest does not have in OSS
		// (confirmed via reflection: TestEncoderTableClassifiesEveryRequestField
		// fails "names a field that does not exist"). OSS's ReadyRequest
		// (issueops/reader.go) carries no id-exclusion filter at all — unlike
		// workapi.WorkFilter, whose own, separate ExcludeIDs refusal stays below
		// in readyBridgeTable as "E-WorkFilter.ExcludeIDs". Do not re-add this
		// row unless issueops.ReadyRequest grows the field.]
	}
}

func readyTable() Table {
	return Table{
		Op: OpListReadyWork, Source: tyReadyRequest, Target: tyReadyRequest, Primary: true,
		Fields: append(readyFilterEntries(),
			param("Brief", "brief"),
			// The sort policy is sent EXPLICITLY, always — including for a
			// request that names none. An absent `sort` is "priority" to the
			// server and hybrid to the storage layer, and those two answer with
			// different item SETS once a limit truncates, so an empty policy is
			// encoded as the concrete `hybrid` it means rather than omitted.
			paramTo("Sort", "sort", "Sort"),
			param("Limit", "limit"),
			refused("Offset", "E-ReadyRequest.Offset"),
		),
	}
}

func readyCountTable() Table {
	return Table{
		Op: OpCountReadyWork, Source: tyReadyRequest, Target: tyReadyRequest, Primary: true,
		Fields: append(readyFilterEntries(),
			dropped("Brief", "E-ReadyRequest.Brief@countReadyWork", briefCountDropWhy),
			serverFixed("Sort", "priority",
				"countReadyWork publishes no `sort`: a cardinality has no order, and the handler sends the listing's own default into the builder so the request stays one the builder accepts. "+
					"Dropping the caller's policy changes no answer — CountReady's total is order-independent by contract"),
			refused("Limit", "E-ReadyRequest.Limit@countReadyWork"),
			refused("Offset", "E-ReadyRequest.Offset"),
		),
	}
}

func listTable() Table {
	return Table{
		Op: OpListIssues, Source: tyListRequest, Target: tyListRequest, Primary: true,
		Reserved: []ReservedParam{{
			Name: "cursor",
			Why: "the opaque keyset token. It is minted by the SERVER and echoed by the pager from the previous page's next_cursor; no request field encodes one, and a client that minted its own would be inventing a position in an order it cannot observe. " +
				"ListRequest's own decoded position (AfterCreatedAt/AfterID) is client-side for exactly that reason — see D8 row 1 and the upstream ask for after_created_at/after_id parameters",
		}, {
			Name: "sort",
			Why: "PAGER-OWNED, like cursor beside it. The operation publishes the display order the upstream ask named, but whether a request may carry it depends on the SHAPE OF THE WALK — a caller-supplied keyset position and an unlimited read must stay on the created-order pager — and on the server advertising issues.list.sort. " +
				"None of that is visible to a per-field encoder, so ListRequest.SortBy stays serverFixed here and the key is written by the pager's pushdown leg (list_walk.go sortedPage), which is the only place that can see all four conditions at once",
		}},
		Fields: []FieldEntry{
			param("Status", "status"),
			paramTo("IssueType", "type", "IssueType"),
			param("Assignee", "assignee"),
			refused("TitleSearch", "E-ListRequest.TitleSearch"),
			refused("SpecPrefix", "E-ListRequest.SpecPrefix"),
			refused("IDFilter", "E-ListRequest.IDFilter"),
			param("Labels", "label"),
			param("LabelsAny", "label_any"),
			param("ExcludeLabels", "exclude_label"),
			refused("LabelPattern", "E-ListRequest.LabelPattern"),
			refused("LabelRegex", "E-ListRequest.LabelRegex"),
			refused("TitleContains", "E-ListRequest.TitleContains"),
			refused("DescContains", "E-ListRequest.DescContains"),
			refused("NotesContains", "E-ListRequest.NotesContains"),
			refused("ExternalContains", "E-ListRequest.ExternalContains"),
			refused("ExternalRef", "E-ListRequest.ExternalRef"),
			paramTo("CreatedBefore", "created_before", "CreatedBefore"),
			paramTo("CreatedAfter", "created_after", "CreatedAfter"),
			refused("UpdatedAfter", "E-ListRequest.UpdatedAfter"),
			refused("UpdatedBefore", "E-ListRequest.UpdatedBefore"),
			refused("ClosedAfter", "E-ListRequest.ClosedAfter"),
			refused("ClosedBefore", "E-ListRequest.ClosedBefore"),
			refused("DeferAfter", "E-ListRequest.DeferAfter"),
			refused("DeferBefore", "E-ListRequest.DeferBefore"),
			refused("DueAfter", "E-ListRequest.DueAfter"),
			refused("DueBefore", "E-ListRequest.DueBefore"),
			refused("EmptyDesc", "E-ListRequest.EmptyDesc"),
			refused("NoAssignee", "E-ListRequest.NoAssignee"),
			refused("NoLabels", "E-ListRequest.NoLabels"),
			dropped("SkipLabels", "E-ListRequest.SkipLabels",
				"the wire hydrates labels either way; dropping the opt-out costs a join and hands the caller more data, never less"),
			dropped("SkipCounts", "E-ListRequest.SkipCounts",
				"the wire hydrates the three cardinalities either way. Every text rendering of `bd list` sets this, so refusing would kill the default listing over http"),
			param("Brief", "brief"),
			refused("IncludeComments", "E-ListRequest.IncludeComments"),
			refused("IncludeAllTypes", "E-ListRequest.IncludeAllTypes"),
			refused("Priority", "E-ListRequest.Priority"),
			refused("PriorityMin", "E-ListRequest.PriorityMin"),
			refused("PriorityMax", "E-ListRequest.PriorityMax"),
			refused("PinnedFlag", "E-ListRequest.PinnedFlag"),
			refused("NoPinnedFlag", "E-ListRequest.NoPinnedFlag"),
			param("IncludeTemplates", "include_templates"),
			param("IncludeGates", "include_gates"),
			param("IncludeInfra", "include_infra"),
			param("IncludeEphemeral", "include_ephemeral"),
			refused("ExcludeTypes", "E-ListRequest.ExcludeTypes"),
			paramTo("ParentID", "parent", "ParentID"),
			refused("NoParent", "E-ListRequest.NoParent"),
			refused("MolType", "E-ListRequest.MolType"),
			refused("WispType", "E-ListRequest.WispType"),
			refused("DeferredFlag", "E-ListRequest.DeferredFlag"),
			refused("OverdueFlag", "E-ListRequest.OverdueFlag"),
			paramTo("MetadataFields", "metadata_field", "MetadataFields"),
			param("HasMetadataKey", "has_metadata_key"),
			paramTo("AllFlag", "all", "AllFlag"),
			refused("ReadyFlag", "E-ListRequest.ReadyFlag"),
			serverFixed("SortBy", "created",
				"a request that names no `sort` is welded to created order, because the cursor is a keyset position in that order — a first page under `bd list`'s priority-first default would make the second page skip and duplicate rows. "+
					"The operation publishes `sort`, but it is a PAGER-OWNED key (see the Reserved row) rather than a per-field one, so THE ENCODER emits nothing for this field and the decoded request carries the server's own `created` whenever the pager stays on the walk. "+
					"The pager's pushdown leg names the caller's order instead, on the bounded, position-free, capability-advertised requests where the server can serve it in one page; the legs it cannot take keep the client-side comparator and the fetch-to-exhaustion cost L2 now records for them alone"),
			clientSide("Reverse",
				"unlike SortBy, this one never reaches the wire at all: OSS listIssues publishes no `reverse` parameter under any capability — issues.list.sort (routes.go) gates `sort` alone, and handleListIssues (reads.go) never reads a reverse flag, so there is no pushdown leg for this field to ride. "+
					"[Removed from this table's Reserved set: an enterprise-only `reverse` row previously claimed it was PAGER-OWNED like `sort`, pairing with the upstream ask's sort+reverse pushdown. The published OpenAPI parameter list for GET /v0/beads/issues has no such member, and CapIssuesListSort's own doc says it gates `sort` only — so the row described behavior this OSS server does not serve. ListRequest.Reverse is honored purely by the client-side comparator on every leg (L2), never by the wire, until an upstream ask adds the parameter.]"),
			param("Limit", "limit"),
			refused("Offset", "E-ListRequest.Offset"),
			clientSide("AfterCreatedAt",
				"the caller-supplied keyset position is honored by the pager's SKIP-FORWARD WALK (D8 row 1): it pages the created-order wire and discards rows at or before the position. "+
					"The encoder deliberately mints NO created_before jump for it — created_before is strictly exclusive (internal/storage/sqlbuild/filter.go) while the keyset upper bound is inclusive, so a jump computed from an instant whose storage granularity the client cannot observe would silently drop the same-instant tail. That is the drop L3 and L12 exist to prevent, traded for an optimization; the jump becomes safe when the wire publishes after_created_at/after_id (upstream ask)"),
			clientSide("AfterID",
				"the same position's tie-break, discarded against client-side; the wire carries it only inside the opaque cursor, which the client must not mint"),
			clientSide("AfterPriority",
				"the priority half of a keyset position minted under sort=priority (upstream #5666); like AfterCreatedAt/AfterID it reaches the wire only inside the opaque cursor, which the client must not mint, so the pager's skip-forward walk discards against it client-side"),
			clientSide("MaxRows",
				"the wire publishes no max_rows, so the client enforces the cap during page accumulation and synthesizes issueops.ErrTooManyRows — the exact sentinel `bd list` already classifies into exit 2 (D12). The cap bounds WIRE ROWS FETCHED, which is why it can fire where local mode would not (L15)"),
			clientSide("MaxRowsSource",
				"the cap's attribution, read back by the synthesized refusal's text; it decides no answer (D12)"),
		},
	}
}

func queryTable() Table {
	return Table{
		Op: OpQueryIssues, Source: tyQueryRequest, Target: tyQueryRequest, Primary: true,
		Fields: []FieldEntry{
			paramTo("Expression", "q", "Expression"),
			paramTo("IncludeClosed", "all", "IncludeClosed"),
			paramTo("SortBy", "sort", "SortBy"),
			param("Reverse", "reverse"),
			param("Limit", "limit"),
			refused("Offset", "E-QueryRequest.Offset"),
		},
	}
}

// countTable is the issue count's predicate, and it is the one table on this
// surface with NOTHING TO REFUSE.
//
// That is worth stating, because a count is the shape where a dropped filter
// does the most damage: a listing that widened returns rows a caller can look
// at, and a count that widened returns a NUMBER, which carries no evidence of
// the set it came from. So the partition here was measured against the
// document rather than assumed — every one of CountRequest's members is published by GET /v0/beads/issues:count, and the count's
// vocabulary is total.
//
// THE COUNT'S PLANE VOCABULARY IS NARROWER THAN THE LISTING'S — `include_infra`
// and `include_ephemeral` where listIssues additionally has `include_templates`,
// `include_gates` and `all` — and that narrowing is the ROLE's, not the wire's.
// issueops.CountRequest has no member for those remaining three, so there is no
// field here to refuse: `bd count` and `bd list` answer about different sets on
// EVERY backend, and this client reproduces that difference exactly rather than
// inventing a wire divergence to describe it. What IncludeInfra alone moves is
// four things at once, and the role documents them; a caller that wants a plane
// union the count cannot express is asking a question issueops.Counter does not
// have.
//
// [Corrected: this comment previously claimed CountRequest carried only
// IncludeInfra from the plane vocabulary ("has no member for the other four"),
// and the table below sent no `has_metadata_key` or `include_ephemeral` to
// match. Both are real CountRequest members (issueops/counter.go) and both are
// published by GET /v0/beads/issues:count in the OSS OpenAPI spec, so the
// table was silently dropping two caller-supplied filters on every count
// request. Mapped below as ordinary params; see TestEveryEncoderTableParameterIsPublished.]
func countTable() Table {
	return Table{
		Op: OpCountIssues, Source: tyCountRequest, Target: tyCountRequest, Primary: true,
		Reserved: []ReservedParam{{
			Name: "group_by",
			Why: "the bucketing dimension is not part of the predicate: no member of CountRequest carries it, because a scalar count has none. " +
				"It is issueops.CountByGroupRequest.GroupBy, classified by the countIssues/byGroup table beside this one, which carries the same filter by reference rather than by copy",
		}},
		Fields: []FieldEntry{
			param("Status", "status"),
			paramTo("IssueType", "type", "IssueType"),
			param("Assignee", "assignee"),

			param("Priority", "priority"),
			paramTo("PriorityMin", "priority_min", "PriorityMin"),
			paramTo("PriorityMax", "priority_max", "PriorityMax"),

			param("Labels", "label"),
			param("LabelsAny", "label_any"),

			paramTo("TitleSearch", "title", "TitleSearch"),
			// The comma-separated string, sent as the caller wrote it: the ROLE
			// splits, trims and de-duplicates it, and a client that pre-split it
			// would be deciding what an id set means — the same reading the
			// server's own handler gives the parameter.
			paramTo("IDFilter", "id", "IDFilter"),

			paramTo("TitleContains", "title_contains", "TitleContains"),
			paramTo("DescContains", "desc_contains", "DescContains"),
			paramTo("NotesContains", "notes_contains", "NotesContains"),

			paramTo("CreatedAfter", "created_after", "CreatedAfter"),
			paramTo("CreatedBefore", "created_before", "CreatedBefore"),
			paramTo("UpdatedAfter", "updated_after", "UpdatedAfter"),
			paramTo("UpdatedBefore", "updated_before", "UpdatedBefore"),
			paramTo("ClosedAfter", "closed_after", "ClosedAfter"),
			paramTo("ClosedBefore", "closed_before", "ClosedBefore"),

			paramTo("EmptyDesc", "empty_description", "EmptyDesc"),
			paramTo("NoAssignee", "no_assignee", "NoAssignee"),
			paramTo("NoLabels", "no_labels", "NoLabels"),

			param("IncludeInfra", "include_infra"),
			// [Added: OSS publishes `include_ephemeral` on GET
			// /v0/beads/issues:count (same plane knob ListRequest.IncludeEphemeral
			// already maps on listIssues) and CountRequest carries the matching
			// member (issueops/counter.go). The table previously had no entry for
			// it at all, silently dropping a caller-supplied filter.]
			param("IncludeEphemeral", "include_ephemeral"),

			paramTo("MetadataFields", "metadata_field", "MetadataFields"),
			// [Added: OSS publishes `has_metadata_key` on GET
			// /v0/beads/issues:count, spelled and meaning the same thing as
			// ListRequest.HasMetadataKey/ReadyRequest.HasMetadataKey. Missing
			// here for the same reason IncludeEphemeral was: no entry at all.]
			param("HasMetadataKey", "has_metadata_key"),

			// [Added: the four scope members of upstream #7199 (behavior token
			// issues.count.scope). The server decodes them in reads.go
			// countFilters exactly as spelled here — parent and exclude_type as
			// the listing reads them, no_parent and exclude_status by this
			// surface's own boolean/list conventions. Against a server that does
			// not advertise issues.count.scope these parameters answer 400
			// unknown_parameter (skew.go case 3); the capability pre-flight that
			// refuses them LOCALLY before the dial belongs to the count role
			// client, which reads wire.CapCountScope off the handshake snapshot.]
			paramTo("ParentID", "parent", "ParentID"),
			paramTo("NoParent", "no_parent", "NoParent"),
			paramTo("ExcludeTypes", "exclude_type", "ExcludeTypes"),
			paramTo("ExcludeStatus", "exclude_status", "ExcludeStatus"),
		},
	}
}

// countByGroupTable is the bucketed count: the scalar predicate plus one
// dimension.
//
// It is NOT primary — the count table above owns the operation's parameter set
// — and it classifies two members, because that is all CountByGroupRequest has.
// The predicate travels through the count table by delegation (DispNested)
// rather than by a second enumeration of the same fields.
func countByGroupTable() Table {
	return Table{
		Op: OpCountIssues, Shape: "byGroup",
		Source: tyCountByGroupRequest, Target: tyCountByGroupRequest,
		Fields: []FieldEntry{
			{Name: "Filter", Disposition: DispNested, Decoded: "Filter",
				Why: "the same predicate a scalar count takes, encoded by the countIssues table. Sharing it rather than restating it is what makes the two questions provably one: a grouped count is a scalar count plus a dimension, and a builder that narrowed one and not the other would bucket a set the caller never asked about"},
			paramTo("GroupBy", "group_by", "GroupBy"),
		},
	}
}

func getTable() Table {
	return Table{
		Op: OpGetIssue, Source: tyGetRequest, Target: tyGetRequest, Primary: true,
		Fields: []FieldEntry{
			{Name: "ID", Disposition: DispPath, Param: "{id}", Decoded: "ID"},
			param("IncludeDependents", "include_dependents"),
			param("IncludeComments", "include_comments"),
			param("BriefDeps", "brief_deps"),
		},
	}
}

// readyBridgeTable is the reverse types.WorkFilter -> ready-parameters mapper.
//
// It exists only because `bd ready`'s listing is still raw at tip, and it dies
// with the upstream front-door migration onto issueops.Reader. Until then it is
// the seam's least elegant component, and the only thing keeping it honest is
// that it refuses every field it cannot express rather than answering a wider
// question than was asked (L12).
//
// Status is the one exception, and it is a DERIVED DEFAULT rather than a refusal
// (D4's parent-walk inversion, applied to this bridge's single derived member):
// listReadyWork publishes no `status`, and the server re-derives ready work's
// status from a status-LESS request through workapi.BuildReadyFilter —
// unconditionally StatusOpen. Every real `bd ready` carries exactly that value,
// because BuildReadyFilter stamps it on the filter it always builds, so the
// encoder recognizes StatusOpen as the server's own default and sends nothing.
// Any other status, and any populated Statuses OR-set, still refuses (L12) — the
// refuse-not-drop guard for the hand-built shapes BuildReadyFilter never emits.
func readyBridgeTable() Table {
	const statusInversionWhy = "ready work's one derived default: listReadyWork publishes no `status`, and the server re-derives open-only from a status-less request through workapi.BuildReadyFilter (unconditionally StatusOpen), so the encoder sends that value as the absence the server reads it back from (D4 parent-walk inversion)"
	return Table{
		Op: OpListReadyWork, Shape: "workFilterBridge",
		Source: tyWorkFilter, Target: tyReadyRequest,
		Fields: []FieldEntry{
			{Name: "Status", Disposition: DispInverted, Ledger: "E-WorkFilter.Status", Why: statusInversionWhy},
			refused("Statuses", "E-WorkFilter.Statuses"),
			paramTo("Type", "type", "IssueType"),
			paramTo("Priority", "priority", "Priority"),
			paramTo("Assignee", "assignee", "Assignee"),
			paramTo("Unassigned", "unassigned", "Unassigned"),
			paramTo("Labels", "label", "Labels"),
			paramTo("LabelsAny", "label_any", "LabelsAny"),
			paramTo("ExcludeLabels", "exclude_label", "ExcludeLabels"),
			paramTo("LabelPattern", "label_pattern", "LabelPattern"),
			paramTo("LabelRegex", "label_regex", "LabelRegex"),
			paramTo("Limit", "limit", "Limit"),
			paramTo("SortPolicy", "sort", "Sort"),
			paramTo("ParentID", "parent", "ParentID"),
			refused("MoleculeID", "E-WorkFilter.MoleculeID"),
			refused("MolType", "E-WorkFilter.MolType"),
			refused("WispType", "E-WorkFilter.WispType"),
			paramTo("IncludeDeferred", "include_deferred", "IncludeDeferred"),
			paramTo("IncludeEphemeral", "include_ephemeral", "IncludeEphemeral"),
			paramTo("ExcludeTypes", "exclude_type", "ExcludeTypes"),
			paramTo("MetadataFields", "metadata_field", "MetadataFields"),
			paramTo("HasMetadataKey", "has_metadata_key", "HasMetadataKey"),
			paramTo("Lite", "brief", "Brief"),
			refused("ExcludeIDs", "E-WorkFilter.ExcludeIDs"),
			refused("Offset", "E-WorkFilter.Offset"),
			clientSide("MaxRows",
				"enforced during page accumulation exactly as ListRequest.MaxRows is, so `bd ready --max-rows` exits 2 over http where it exits 2 locally (D12)"),
			clientSide("MaxRowsSource", "the cap's attribution; it decides no answer (D12)"),
		},
	}
}

// searchExactIDsTable is D11's exact-ids fast path: the shape
// ResolvePartialID's first probe takes, special-cased to one getIssue per id.
func searchExactIDsTable() Table {
	return Table{
		Op: OpGetIssue, Shape: "searchExactIDs",
		Source: tyIssueFilter, Target: tyGetRequest,
		Fields: bridgeEntries(
			FieldEntry{Name: "IDs", Disposition: DispPath, Param: "{id}", Decoded: "ID",
				Why: "one getIssue per id, bounded at MaxExactIDs; a 404 maps to the empty result the resolver expects, so resolution proceeds instead of erroring"},
			refused("ParentID", "E-IssueFilter.shapeConflict"),
			refused("Limit", "E-IssueFilter.Limit@exactIDs"),
		),
	}
}

// searchParentWalkTable is D4's hierarchical descendant walk: per-level paged
// listIssues?parent=<id> calls, with the derived-default inversion the ga-2ieek
// escalation resolved the shape contradiction with.
//
// It is written out in full rather than spread from issueFilterBridgeRefusals,
// because this shape is where the two bridges stop agreeing: the walk carries
// `bd list`'s whole built filter, so six of its members are DERIVED defaults
// the server re-derives (DispInverted) and eleven more are explicit user filters
// listIssues publishes parameters for. The exact-ids shape still refuses every
// one of them, which is why the enumeration cannot be shared.
func searchParentWalkTable() Table {
	const inversionWhy = "a derived default of workapi.BuildListFilter, re-derived server-side from the intent parameters the encoder sends in its place (D4 parent-walk inversion)"
	inverted := func(name, ledger string) FieldEntry {
		return FieldEntry{Name: name, Disposition: DispInverted, Ledger: ledger, Why: inversionWhy}
	}
	walkRefusal := func(name string) FieldEntry {
		return refused(name, "P-IssueFilter."+name)
	}
	return Table{
		Op: OpListIssues, Shape: "searchParentWalk",
		Source: tyIssueFilter, Target: tyListRequest,
		Fields: []FieldEntry{
			paramTo("ParentID", "parent", "ParentID"),
			refused("IDs", "E-IssueFilter.shapeConflict"),

			// The derived defaults, in the order the inversion compares them.
			inverted("ExcludeStatus", "P-IssueFilter.ExcludeStatus"),
			inverted("Pinned", "P-IssueFilter.Pinned"),
			inverted("IsTemplate", "P-IssueFilter.IsTemplate"),
			inverted("ExcludeTypes", "P-IssueFilter.ExcludeTypes"),
			inverted("Ephemeral", "P-IssueFilter.Ephemeral"),
			inverted("SkipWisps", "P-IssueFilter.SkipWisps"),

			// The explicit user filters, under their documented parameters. The
			// two status members share one parameter because the wire's `status`
			// is the comma-separated spelling BOTH of them came from: a single
			// name lands on Status, several land on Statuses, and the operation's
			// decoder splits the value back the same way.
			paramTo("Status", "status", "Status"),
			paramTo("Statuses", "status", "Status"),
			paramTo("IssueType", "type", "IssueType"),
			paramTo("Assignee", "assignee", "Assignee"),
			paramTo("Labels", "label", "Labels"),
			paramTo("LabelsAny", "label_any", "LabelsAny"),
			paramTo("ExcludeLabels", "exclude_label", "ExcludeLabels"),
			paramTo("CreatedBefore", "created_before", "CreatedBefore"),
			paramTo("CreatedAfter", "created_after", "CreatedAfter"),
			paramTo("MetadataFields", "metadata_field", "MetadataFields"),
			paramTo("HasMetadataKey", "has_metadata_key", "HasMetadataKey"),

			clientSide("Limit",
				"the walk asks for every descendant at each level (the CLI sets Limit=0 so no level is truncated), and unlimited maps to PAGING TO EXHAUSTION rather than to limit=0, which the server refuses outright off loopback. The pager owns the per-page `limit`"),
			clientSide("MaxRows",
				"the wire publishes no max_rows, so the pager enforces the cap during page accumulation and synthesizes issueops.ErrTooManyRows — the sentinel `bd list` classifies into exit 2 (D12)"),
			clientSide("MaxRowsSource", "the cap's attribution, read back by the synthesized refusal's text; it decides no answer (D12)"),

			dropped("SortBy", "P-IssueFilter.SortBy",
				"the walk accumulates every level into a map keyed by id (findAllDescendants), so no per-level order survives into the answer at all — this is a drop with nothing to drop"),
			dropped("SortDesc", "P-IssueFilter.SortDesc",
				"the same absence as SortBy, from the other half of the same order"),
			dropped("SkipLabels", "P-IssueFilter.SkipLabels",
				"the wire hydrates labels either way; dropping the opt-out costs a join and hands the caller more data, never less"),
			dropped("SkipCounts", "P-IssueFilter.SkipCounts",
				"the wire hydrates the three cardinalities either way, and EVERY text rendering of `bd list` sets this — refusing would kill the walk's default rendering, which is the one thing D4 requires it to serve"),

			walkRefusal("Priority"),
			walkRefusal("LabelPattern"),
			walkRefusal("LabelRegex"),
			walkRefusal("TitleSearch"),
			walkRefusal("IDPrefix"),
			walkRefusal("SpecIDPrefix"),
			walkRefusal("TitleContains"),
			walkRefusal("DescriptionContains"),
			walkRefusal("NotesContains"),
			walkRefusal("ExternalRefContains"),
			walkRefusal("ExternalRef"),
			walkRefusal("UpdatedAfter"),
			walkRefusal("UpdatedBefore"),
			walkRefusal("ClosedAfter"),
			walkRefusal("ClosedBefore"),
			walkRefusal("StartedAfter"),
			walkRefusal("StartedBefore"),
			walkRefusal("AfterCreatedAt"),
			walkRefusal("AfterID"),
			walkRefusal("AfterPriority"),
			walkRefusal("EmptyDescription"),
			walkRefusal("NoAssignee"),
			walkRefusal("NoLabels"),
			walkRefusal("PriorityMin"),
			walkRefusal("PriorityMax"),
			walkRefusal("SourceRepo"),
			walkRefusal("EphemeralTier"),
			walkRefusal("IsBlocked"),
			walkRefusal("NoParent"),
			walkRefusal("MolType"),
			walkRefusal("WispType"),
			walkRefusal("Deferred"),
			walkRefusal("DeferAfter"),
			walkRefusal("DeferBefore"),
			walkRefusal("DueAfter"),
			walkRefusal("DueBefore"),
			walkRefusal("Overdue"),
			walkRefusal("IncludeDependencies"),
			walkRefusal("NoIDShrink"),
			walkRefusal("Offset"),
			walkRefusal("Lite"),
		},
	}
}

// bridgeEntries prefixes a shape's own entries to the shared IssueFilter
// refusal enumeration.
//
// The enumeration it expands is written out by hand in ledger_fields.go, and it
// derives nothing from the struct — which is what leaves a newly added upstream
// field unclassified and therefore caught. Only the exact-ids shape uses it now:
// the descendant walk stopped sharing the enumeration when D4's inversion gave
// six of those members a derived-default reading and eleven more a documented
// parameter, and a helper that spread one list across two shapes that no longer
// agree would be hiding the disagreement rather than recording it.
func bridgeEntries(shape ...FieldEntry) []FieldEntry {
	out := make([]FieldEntry, 0, len(issueFilterBridgeRefusals)+len(shape))
	out = append(out, shape...)
	for _, name := range issueFilterBridgeRefusals {
		out = append(out, refused(name, "E-IssueFilter."+name))
	}
	return out
}
