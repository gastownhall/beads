// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/ledger.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The divergence ledger, v1.
//
// engdocs/design/http-client-backend.md D9 states the discipline this file
// instantiates: "Every knowingly-degraded behavior is a ledger row with a
// pinned test... A degradation not in this table is a bug." The prose table
// lives in the design; this is the machine-readable copy the client actually
// consults, and it carries three populations rather than one:
//
//   - The D9 rows themselves (L1-L18, with L17 deleted rather than skipped —
//     see the L-sequence note in designRows), which describe whole behaviors rather
//     than single fields. They are here so that the numbering the design and
//     the review log cite resolves to something a test can read.
//   - The refuse-not-drop enumeration D8 closes with: every role-request field
//     the wire has no member for. These name a Go field on a request type and
//     are what makes "refused, never dropped" checkable rather than asserted.
//   - One row per request-shape field this package's encoder cannot put on the
//     wire. Those are the rows the encoder itself reaches for: a refusal is
//     raised BY ledger id, so a refusal with no row is a compile-time absence
//     rather than a prose gap, and the bijection gate reads the same rows back.
//
// WHY A REFUSAL CARRIES A ROW RATHER THAN A STRING. D7's third taxonomy text
// has to name the flag that produced a parameter, and D9 requires every
// degradation to cite a pinned test. Both facts belong to the divergence, not
// to the call site that discovered it, so the call site raises the id and the
// row supplies the vocabulary. It also makes the failure mode of a forgotten
// entry loud: RowByID panics on an unknown id, and the bijection gate fails on
// a row nothing references.
//
// WHAT IS NOT HERE. Rows for the surfaces this package does not encode — the
// unsupported-allowlist refusals of D8, the whole-command refusals of D7 that
// have no request field — appear only where the design names them as
// refuse-not-drop residue or as a D9 row. The store skeleton owns the rest.

// Kind classifies what a ledger row promises.
type Kind string

const (
	// KindRefuse is a divergence that FAILS rather than proceeding: a
	// populated field, or an invoked flag, that the v0 wire cannot express.
	// Refusing is the whole point — a dropped filter widens a result set
	// invisibly, which is the one failure class no server-side gate can
	// observe (D7).
	KindRefuse Kind = "refuse"
	// KindDegrade is a divergence that PROCEEDS with knowingly different
	// behavior. Every one of these is a judgment that the difference cannot be
	// misread as a narrower or wider answer than the caller asked for.
	KindDegrade Kind = "degrade"
	// KindRetired is a row the design has retired. It stays so the L-numbers
	// the design, the council log and the beads cite keep resolving, and so a
	// re-enumeration can tell "retired" from "never existed".
	KindRetired Kind = "retired"
)

// Row is one divergence: what diverges, why, which decision it comes from, and
// what pins it.
type Row struct {
	// ID is the stable citation. D9's own rows keep their L-numbers; the rest
	// are prefixed by population — E for an encoder field, W for the
	// write-side refuse-not-drop enumeration, F for a command or flag.
	ID string
	// Kind is what this row promises.
	Kind Kind

	// Type and Field name the Go request field that diverges, for the rows
	// that have one. Type is nil on a row that describes a behavior or a flag
	// rather than a field. Holding the reflect.Type rather than its name is
	// what makes a renamed or deleted field a test failure instead of a stale
	// string.
	Type  reflect.Type
	Field string

	// Flag is the user's spelling, where one exists — D7's refusal texts speak
	// flags and commands, never store method names. Empty when the divergence
	// has no single flag behind it.
	Flag string
	// Command is the cmd/bd command path this row is about, spelled the way the
	// classification tables spell it ("count", "ready", "mol ready"). Empty on a
	// row that is not about a command.
	//
	// It exists because the command was only ever in the PROSE, and prose is
	// what let F-count claim `bd count` refuses for a whole wave after the
	// command was served. cmd/bd's TestFRowsAgreeWithTheCommandClassification
	// binds this field to httpRefusedCommands/httpServedCommands, so the claim
	// is checked rather than read.
	Command string
	// Capability is the wire token behind a LATE refusal: the command is SERVED
	// and refuses only against a server that does not advertise this token.
	//
	// It is what distinguishes F-close — a refuse row whose command is served —
	// from a whole-command refusal, and it binds harder than a boolean would:
	// the gate requires the served command's own capability list to name it.
	Capability string

	// What describes the divergence for a reader. Required on every row; on a
	// field row it says what the field asked for that the wire cannot carry.
	What string
	// Why is the reason the wire cannot carry it, or the reason the
	// degradation is the right answer.
	Why string
	// SpecRow cites the decision in engdocs/design/http-client-backend.md.
	SpecRow string
	// PinnedBy names the test that holds this row honest, or carries a
	// TODO(<bead>) when the pinning test is another bead's deliverable. It is
	// never empty: D9's whole discipline is that a ledger row without a pin is
	// a wish.
	PinnedBy string
}

// pinnedByS3Conformance is the TODO sentinel for ledger rows whose claim is
// provable only from behind two seams this package (S2) does not build:
//
//   - a server wired to a real/reference backing store — multi-page reads
//     under concurrent writes, racing claims, dual-running against a second
//     implementation, actor/provenance rules an httptest fixture's canned
//     in-memory role does not enforce — and
//   - cmd/bd wired to this client — the subprocess parity corpus, the
//     pre-run classified-refused-command table, flag-mode refusals driven
//     through the real cobra tree.
//
// Both are S3's: the store/dial seam and the cmd/bd integration that gives
// this client a binary to run those suites against. Until that wiring lands
// there is no test in ANY package that exercises the claim, so each row that
// cited an enterprise test of this shape is re-pinned here rather than left
// pointing at a name with nothing behind it — the well-formedness gate
// accepts TODO(<bead>) for exactly this reason (see PinnedBy's doc comment).
//
// This package's OWN behavior — the encode/decode tables, the capability
// gate, the wire-revision floor — stays pinned to real tests in this package
// (TestEncoderTableClassifiesEveryRequestField,
// TestEncoderHonorsEveryTableDisposition,
// TestEncodedParametersRoundTripThroughTheServerDecoder, and friends); this
// sentinel covers only the rows whose evidence is a layer S2 never touches.
const pinnedByS3Conformance = "TODO(S3): store/dial-seam and cmd/bd conformance this package's own gates do not reach; see the S2 handoff notes for the full list of rows re-pinned here and engdocs/design/http-client-backend.md D9 for the discipline"

// Ledger returns divergence ledger v1, in citation order.
//
// It builds a fresh copy per call rather than handing out a package-level
// slice, so no caller can edit the ledger another caller reads. The encoder's
// own lookups go through cachedLedger below and never through this.
func Ledger() []Row {
	var rows []Row
	rows = append(rows, designRows()...)
	rows = append(rows, writeSideRows()...)
	rows = append(rows, commandRows()...)
	rows = append(rows, readyRequestRows()...)
	rows = append(rows, listRequestRows()...)
	rows = append(rows, queryRequestRows()...)
	rows = append(rows, issueFilterRows()...)
	rows = append(rows, parentWalkRows()...)
	rows = append(rows, workFilterRows()...)
	return rows
}

// RowByID returns the row a refusal cites.
//
// It PANICS on an unknown id, which is the correct severity: the ids are
// package constants used from this package's own encoder, so an unknown one is
// a programming error that would otherwise surface as a refusal with no
// vocabulary — exactly the "raw store-method refusal reaching a %v call site"
// D7's choke point exists to make impossible.
func RowByID(id string) Row {
	for _, row := range cachedLedger() {
		if row.ID == id {
			return row
		}
	}
	panic(fmt.Sprintf("encode: no divergence-ledger row %q", id))
}

// cachedLedger is what a REFUSAL reads, so raising one does not rebuild the
// whole ledger. Ledger() keeps building a fresh copy for everyone else.
var cachedLedger = sync.OnceValue(Ledger)

var (
	tyReadyRequest        = reflect.TypeOf(issueops.ReadyRequest{})
	tyListRequest         = reflect.TypeOf(issueops.ListRequest{})
	tyQueryRequest        = reflect.TypeOf(issueops.QueryRequest{})
	tyCountRequest        = reflect.TypeOf(issueops.CountRequest{})
	tyCountByGroupRequest = reflect.TypeOf(issueops.CountByGroupRequest{})
	tyGetRequest          = reflect.TypeOf(issueops.GetRequest{})
	tyIssueFilter         = reflect.TypeOf(types.IssueFilter{})
	tyWorkFilter          = reflect.TypeOf(types.WorkFilter{})

	tyUpdateRequest = reflect.TypeOf(issueops.UpdateRequest{})
	tyCloseRequest  = reflect.TypeOf(issueops.CloseRequest{})
	tyReopenRequest = reflect.TypeOf(issueops.ReopenRequest{})
	tyDeleteRequest = reflect.TypeOf(issueops.DeleteRequest{})
	tyAddDeps       = reflect.TypeOf(issueops.AddDependenciesRequest{})
	tyIssuePatch    = reflect.TypeOf(issueops.IssuePatch{})

	tyCreateRequest   = reflect.TypeOf(issueops.CreateRequest{})
	tyCreateBatch     = reflect.TypeOf(issueops.CreateBatchRequest{})
	tyBatchCreateItem = reflect.TypeOf(issueops.BatchCreateItem{})
	tyCreateDep       = reflect.TypeOf(issueops.CreateDependency{})

	tyApplyCreateItem = reflect.TypeOf(issueops.CreateItem{})
)

// designRows are D9's own table, transcribed. They describe behaviors rather
// than fields, so they carry no Type — but they are the rows the L-numbers in
// every other citation resolve to, and dropping them here would leave those
// citations pointing at prose only.
func designRows() []Row {
	// pinnedElsewhere is for the three PRE-RUN degradations of D4 — molecule
	// auto-load (L5), auto-import (L6), the ready view's parent-epic map (L13) —
	// whose observable behavior is a cmd/bd invocation's, not a store call's.
	// They are the subprocess parity tier's, and that tier is the one this wiring
	// did not build. ga-b8ddd.12 (per-request project-id enforcement) closed the
	// read-display ESCALATION — every read now carries the stamp, so a drifted
	// server refuses it rather than rendering another project's rows — but the
	// remaining fixture corpus that would PIN these pre-run displays is its
	// follow-up, ga-b8ddd.23, which owns them.
	//
	// L17 (the conditional dep-remove guard) used to be the one remaining TODO row
	// that was NOT subprocess-owned. It carried no row of its own once its
	// condition resolved: the dual run ga-b8ddd.30 wrote
	// (TestServedDependencyRecordsSurfaceUnresolvedRowsVerbatim) showed the wire
	// surfaces unresolved depends_on_id rows VERBATIM, so the guard does not
	// degrade and there was nothing to ledger. It is DELETED rather than retired,
	// because a retirement records a divergence that once existed and this one
	// never did. L1 is the retirement beside it — a row that WAS live and is kept
	// as KindRetired for exactly that reason.
	const pinnedElsewhere = "TODO(ga-b8ddd.23): a pre-run degradation, observable only through a cmd/bd invocation; the subprocess parity corpus pins it"
	return []Row{
		{
			// IT WAS DELETED OUTRIGHT for a while, against this file's own
			// retirement policy: KindRetired exists so a re-enumeration can tell
			// "retired" from "never existed", and the design keeps L1 in D9's
			// table. A deleted row makes the two disagree and turns RowByID("L1")
			// into a panic rather than an answer.
			ID: "L1", Kind: KindRetired,
			What: "RETIRED — `bd list` over http could not ask for the wisp plane",
			Why: "retired by the upstream ask the design page filed for it: \"include_ephemeral on listIssues (retires L1)\". Upstream published the parameter, the server decodes it (internal/httpapi/reads.go handleListIssues), and the encoder emits it whenever ListRequest.IncludeEphemeral is set. " +
				"THERE IS NO DIVERGENCE LEFT TO RECORD. `bd list` still shows no wisps, but it shows none LOCALLY either: the front door registers no --include-ephemeral (only `bd ready` does, ready.go), so the field is unset on both paths and both answer the durable plane. A degrade row describing behavior the local oracle shares is not a degradation, and keeping it as one would be the same falsified-premise problem the W- rows had — a ledger arguing from a document that moved",
			SpecRow: "D9 L1 (RETIRED), D8 row 1",
			// A retirement turns a refusal pin into a ROUND-TRIP pin: the
			// parameter is populated here and read back by the server's own
			// decoder, which is the fact the retirement rests on.
			PinnedBy: "TestEncodedParametersRoundTripThroughTheServerDecoder",
		},
		{
			ID: "L2", Kind: KindDegrade,
			What: "a non-created `bd list` sort, the flagless default included, costs page-to-exhaustion on the three legs that cannot push the order down: an unlimited read (--limit 0), a caller-supplied keyset position, and a server that does not advertise issues.list.sort",
			Why: "NARROWED, not retired, by the upstream ask this row named: listIssues publishes `sort` and `reverse`, and the pager's pushdown leg (list_walk.go sortedPage) answers a bounded, position-free request against an advertising server in ONE request, with the server's own LIMIT deciding which rows survive the caller's order. " +
				"What is left is the legs where that is unavailable or unsafe. An unlimited read has to cross every row whatever the order, and `limit=0` pushed down would newly meet the server's unlimited-read refusal. A keyset position is defined in the created order, and the pager's prefix discard run against a page some other order truncated would drop rows whose replacements were never fetched — a wrong answer rather than a slow one. A down-level server is the fallback the capability probe exists to reach. On all three the client still fetches every page and applies the SQL order's own Go-side mirror plus a copy of the page epilogue's comparator. " +
				"The weld itself is NOT a divergence and never was: a cursor is bound to the paged order it was minted in (`created` or `priority`) and the server refuses it under any other `sort`",
			SpecRow: "D9 L2, D8 row 1",
			// The dual run is the load-bearing half: it answers the SAME
			// request from the reference store and from the client and compares
			// the orders, so the divergence this row admits (the cost) is
			// pinned separately from the one it forbids (a different answer).
			// It now runs every arm TWICE, once down each leg, which is what
			// makes the narrowing checkable: the pushdown and the walk have to
			// agree with the reference AND with each other, truncated pages
			// included, where the truncation is the only thing a re-sort cannot
			// repair.
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L3", Kind: KindDegrade,
			What:     "a multi-page fetch has no snapshot isolation",
			Why:      "the keyset cursor pins a position, not a snapshot: rows created mid-walk are missed and row states mix instants, where local mode is one query",
			SpecRow:  "D9 L3",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L4", Kind: KindRetired,
			What:    "`bd show` dependents sections and comment bodies were degraded",
			Why:     "retired by council #1: the write-lifecycle wave landed include_dependents/include_comments on getIssue, and both the off-role text path and the on-role JSON path are wired",
			SpecRow: "D9 L4 (RETIRED), D4, D8 row 1",
			// The dual run answers a Get from both sides and compares the
			// MARSHALED bytes, over a row seeded with an edge and a comment
			// precisely so the dependents and comments the retired degradation
			// was about are non-empty on at least one side.
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L5", Kind: KindDegrade,
			What:     "molecules are not auto-loaded pre-run",
			Why:      "the loader writes, the write refuses, and the call site swallows the refusal to debug.Logf",
			SpecRow:  "D9 L5, D4",
			PinnedBy: pinnedElsewhere,
		},
		{
			ID: "L6", Kind: KindDegrade,
			What: "a pre-run auto-import ATTEMPT reaches an http workspace and fails as a best-effort stderr warning; nothing is imported",
			Why: "THE MECHANISM THIS ROW USED TO NAME DOES NOT EXIST. It said the path was \"force-disabled through an import.auto=false env override\", and there is no such override anywhere: shouldRunAutoImportJSONL (cmd/bd/main.go) reads the config key, whose default is true (internal/config/config.go), and no http path sets it false. " +
				"WHAT ACTUALLY HAPPENS is that maybeAutoImportJSONL runs — on any non-read-only, non-global, non-server-mode command — and is stopped by its own guards rather than by a switch: a missing or empty issues.jsonl, the attempt stamp, or the emptiness guard, which is a GetStatistics the server serves. Where all three pass (an empty server beside a non-empty local JSONL) the import is attempted and fails, because the client is not a jsonlImporter and the fallback importer's first write — SetConfig, or CreateIssuesWithFullOptions — is on the unsupported allowlist. The function is documented best-effort, so the failure is an stderr warning and the command proceeds. " +
				"The DEGRADATION is therefore that nothing imports, which is the outcome the design wanted for the right reason — importing a local JSONL into a shared server is not a pre-run side effect — reached by refusal rather than by configuration. It stays a degrade because the command proceeds and the answer is not narrower or wider than the caller asked for. The retirement path is a real skip on the http backend, so the warning stops being the mechanism",
			SpecRow:  "D9 L6, D4",
			PinnedBy: pinnedElsewhere,
		},
		{
			ID: "L7", Kind: KindDegrade,
			What:     "the client-side status/type vocabulary is defaults plus the status.custom/types.custom/types.infra config keys; table-backed customs are invisible client-side",
			Why:      "the custom_statuses/custom_types tables have no wire operation; server-side role validation still uses the true vocabulary",
			SpecRow:  "D9 L7, D4",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L8", Kind: KindRefuse, Flag: "--deps",
			What:     "`bd list --deps` refuses, and the pretty/--format dependency-decoration arms render undecorated",
			Why:      "GetAllDependencyRecords has no wire mapping in v1; listDependencies is an anchored read. --tree is NOT in this row: it defaults true and is `bd list`'s default text rendering",
			SpecRow:  "D9 L8, D4",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L9", Kind: KindDegrade,
			What:     "credential-bearing settings are unreadable client-side",
			Why:      "the server redacts by omission and says so on the wire in Setting.redacted; the client answers absent-WITH-A-REASON rather than the empty string a caller would read as unset",
			SpecRow:  "D9 L9",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L10", Kind: KindRefuse, Flag: "--watch",
			What:    "`bd list --watch` and `bd show --watch` refuse",
			Why:     "the loops poll every 2s, each `bd list` tick is a full cursor walk (L2), per-tick errors spam or vanish, and the decoration is refused anyway (L8); N watchers against one server is a request storm its semaphore answers with 503s the loop does not back off from",
			SpecRow: "D9 L10, D10",
			// Two halves, one pin each: the TEXT is byte-pinned in the store
			// (TestTheWatchRefusalReadsExactlyAsTheSpecWroteIt), and the
			// flag-level early refusal that renders it is exercised against the
			// real cobra tree here. The wrapper reads the text off
			// (*Store).RefuseWatch rather than keeping a second copy.
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L11", Kind: KindDegrade,
			What:     "hooks fire client-side after the server's commit with no shared transaction, on connected workspaces only",
			Why:      "the seam sits below HookFiringStore in the client process; a --server-url ephemeral invocation has no hook directory and runs none",
			SpecRow:  "D9 L11, D1",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L12", Kind: KindRefuse,
			What:     "an inexpressible filter refuses, never drops — the class rule every E- row below is an instance of",
			Why:      "a dropped filter widens a result set invisibly, and the server can only reject parameters it receives",
			SpecRow:  "D9 L12, D7",
			PinnedBy: "TestEncoderHonorsEveryTableDisposition",
		},
		{
			ID: "L13", Kind: KindDegrade,
			What:     "pretty `bd ready` shows no parent-epic context",
			Why:      "buildParentEpicMap swallows the GetDependencyRecordsForIssues refusal into a nil map; the retirement path is an anchored listDependencies plus per-parent getIssue",
			SpecRow:  "D9 L13, D4",
			PinnedBy: pinnedElsewhere,
		},
		{
			ID: "L14", Kind: KindDegrade,
			What: "against a server that does NOT advertise issues.claimNext, the composed ReadyClaimer leaves three residues: the ready-at-fetch/claim-at-dial window, a false empty under contention after the bounded refetch, and a claimed row whose CARDINALITIES are as of the listing rather than of the claiming transaction",
			Why: "claimIssue validates claimability, not readiness, so a listing plus a claim is not the local role's one transaction and cannot be made into one. " +
				"THE ROW IS NARROWER THAN IT WAS. Upstream published POST /v0/beads/issues:claimNext (#5510) and client wave ga-jpywb dials it, so on any server that advertises the token NONE of these three residues exists: selection, the compare-and-set and the hydration share the server's own transaction, an empty front is a 200 with `claimed` absent rather than a lost-races error, and the counts describe the state the claim produced. The whole ReadyClaimer contract tier runs against that leg with nothing parked, which is the measurement that says the residues were the COMPOSITION's rather than the wire's. " +
				"WHAT KEEPS THE ROW ALIVE is the DOWN-LEVEL leg, which survives on purpose: `bd ready --claim` worked against pre-#5510 servers before this port, and refusing it now would be a regression dressed as progress. It is the posture BatchCloser takes toward issues.batchClose, and it is why this row describes shipped behavior rather than history. " +
				"The third residue is stated in its IMPLEMENTED form, which is not the one the design anticipated: the design expected a follow-up getIssue to hydrate the counts, and therefore a read that could fail AFTER the claim was durable. The composition takes the counts from the ready page it already fetched instead, so that failure mode does not exist — a claim changes no dependency, dependent or comment count, and what is left is staleness bounded by the same fetch-to-dial window residue (a) already owns. " +
				"Retirement is no longer an upstream ask but a fleet fact: the row goes when no server this client may meet is older than #5510",
			SpecRow:  "D9 L14, D8 row 4",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L15", Kind: KindDegrade,
			What: "a --max-rows cap can fire over http on a query whose Limit is at or under the cap, on the `bd list` legs that still walk",
			Why: "locally the effective SQL LIMIT is min(Limit, MaxRows+1), so a window at or under the cap can never exceed it; over http a walk to exhaustion fetches far more than the window and the cap deliberately bounds WIRE ROWS FETCHED — safety wins over exit-code parity on exactly the unbounded walk the cap exists to bound. " +
				"NARROWED by the same pushdown that narrowed L2: the pushdown leg asks for min(limit, MaxRows+1) rows in one request, which IS the local expression, so a cap at or above the limit can no longer fire there and a request that used to exit 2 now answers. That is a convergence toward the contract, and it is observable — the same command changes from a refusal to a page. " +
				"THE NARROWING IS SCOPED TO THE ORDERS SQL CAN EXPRESS, and that scope is a correctness bound rather than an optimization left on the table. For a GO-SIDE sort — sqlbuild.IsGoSideSort, today `--sort id`, which needs the natural-numeric comparison (bd-9 before bd-10) no ORDER BY renders — workapi.SQLLimit pushes 0 down instead of the limit, so LOCALLY the window is MaxRows+1 whatever the limit is and the cap fires on the overage even under a limit at or below it. min(limit, MaxRows+1) is therefore NOT the local expression there, and a request bounded by the limit cannot see the overage at all. walkIssues' gate keeps a capped Go-side sort on the walk, where fetching MaxRows+1 wire rows reproduces that unbounded local window exactly and raises the same refusal — so on this one shape the walk leg is the CONVERGENT one and pushing down would have been the divergence. " +
				"What keeps the row live is the walk legs (unlimited reads, keyset positions, down-level servers), where the old accounting is unchanged and correct, and `bd ready`, which has no pushdown at all",
			SpecRow: "D9 L15, D12",
			// Re-pointed from the ready bridge's cap test to the list one,
			// because the narrowing is a `bd list` fact and the ready cap is
			// the part that did not move. The list test carries the repair
			// (a roomy cap under pushdown answers), the residue (a cap under
			// the limit still fires there) and the control (the same roomy cap
			// still fires down the walk leg) in one place.
			// TestTheReadyBridgeEnforcesMaxRowsClientSide still pins the ready
			// half and is unchanged.
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L16", Kind: KindRefuse, Flag: "--file",
			What:    "`bd dep add --file` carrying more than 100 edges refuses, naming the wire bound; it is never chunked",
			Why:     "maxAddDependencyEdges = 100 (internal/httpapi/dependency_edit.go); chunking is the one path that silently breaks the request's one-transaction/one-history-entry contract, where naive forwarding at least fails loudly",
			SpecRow: "D9 L16, D7, D8 row 18",
			// The boundary itself, both sides: exactly maxAddDependencyEdges
			// serves, one more refuses, and the refusal never dials — chunking
			// is what this row forbids, so "did not reach the server" is the
			// assertion that matters.
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-close-cap", Kind: KindRefuse,
			What:    "a batch close naming more than 100 items refuses, naming the wire bound; it is never chunked",
			Why:     "maxBatchCloseItems = 100 (internal/httpapi/batch_close.go); the item cap bounds how long one request may hold a write transaction. Chunking is the one path that silently breaks the request's one-transaction/one-history-entry contract — the same reason L16 refuses a bulk dependency add rather than splitting it — so a larger list refuses and the caller splits it, each request atomic on its own",
			SpecRow: "D9 L-close-cap, D7 F-close, D8 row 17",
			// L16's sibling: exactly maxBatchCloseItems serves, one more refuses,
			// and the refusal never dials — chunking is what the row forbids.
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-dep-endpoint", Kind: KindDegrade,
			What: "a dependency add whose source or target names no issue refuses as ErrValidation over http, not as *DependencyEndpointNotFoundError wrapping ErrDependencySourceNotFound or ErrDependencyTargetNotFound",
			Why: "the wire spells that refusal as a 400 invalid_argument with reason `invalid_value` and the offending member in `param` (internal/httpapi/dependency_edit.go). Reason and code are the same ones a malformed id earns, so the two are indistinguishable without reading `detail` prose — which is exactly what the extension-member vocabulary exists to avoid. " +
				"The refusal still FAILS the request and still writes nothing; only its classification is coarser. Upstream ask: a distinguishing code or reason for the endpoint-not-found refusal, which retires this row",
			SpecRow:  "D8 row 18",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-claim-prose", Kind: KindDegrade,
			What: "a claim refusal's MESSAGE does not carry the ClaimedBy/NotClaimableStatus fragments, so beads.ParseClaimConflict recovers nothing from it over http",
			Why: "the fragments exist so that parser can recover the conflicting assignee and status from PROSE. Over the wire both arrive as typed extension members and *ClaimConflictError is reconstructed whole, so a caller reading the FIELDS loses nothing. " +
				"Recomposing the copy client-side would mean re-implementing which copy each refusal shape gets — an open issue held by someone else deliberately omits the assignee tail, an in-progress one carries it — which is a second implementation of the very rule the fragments were introduced to keep single",
			SpecRow:  "D8 row 3",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-delete-notfound", Kind: KindDegrade,
			What: "issueops.Deleter.Delete over http refuses an absent id as issueops.ErrNotFound, not as *NotFoundError naming WHICH ids did not resolve",
			Why: "the role's error is a typo report and names every missing id; the wire deliberately does not repeat them (internal/httpapi's NotFound() carries a FIXED detail, so a 404 body cannot echo caller input back). " +
				"The refusal still FAILS the request and still deletes NOTHING — not even the ids beside the typo — so the all-or-nothing promise is intact and only the classification is coarser. " +
				"It is stated at the ROLE rather than at a command because `bd delete` is not reachable against an http workspace at all (see httpUnconsumedCapabilities); the embedder holding the role is the audience. Upstream ask: an ids extension member on the not_found problem, which retires this row",
			SpecRow:  "D8 row 13",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-delete-dependents", Kind: KindDegrade,
			What: "the unforced dependents guard refuses as issueops.ErrValidation, not as *DependentsOutsideRequestError wrapping ErrDependentsOutsideRequest",
			Why: "the wire spells that refusal as a 400 invalid_argument with no `param` (internal/httpapi's failDeleteErr) — the fix is to change the REQUEST, by sending cascade or force — so the blocked id and its dependents cannot be reconstructed without parsing detail prose, which is what the extension-member vocabulary exists to avoid. " +
				"The guard itself is untouched: the request fails and the graph is whole. Upstream ask: a dependents_outside_request code carrying issue_id and dependents, which retires this row",
			SpecRow:  "D8 row 13",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-delete-bound", Kind: KindRefuse,
			What:    "issueops.Deleter.Delete refuses a request naming more than 1000 ids, citing the wire bound; it is never split across requests",
			Why:     "maxDeleteIDs = 1000 (internal/httpapi/delete.go). Splitting is the one path that silently breaks the request's one-transaction, one-history-entry contract — and it would ask the dependents guard about half a request at a time, so a pair the caller deliberately listed together would be refused as an outside dependent. Naive forwarding at least fails loudly. Stated at the ROLE: `bd delete` cannot reach an http workspace, so the embedder is the audience",
			SpecRow: "D8 row 13, D9 L16 (the same argument, on the other bulk write)",
			// The boundary itself, both sides: exactly maxDeleteIDs serves, one
			// more refuses, and the refusal never dials.
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-batchcreate-bound", Kind: KindRefuse,
			What:     "`bd create --file` carrying more than 100 issues refuses, naming the wire bound; it is never split across requests",
			Why:      "maxBatchCreateItems = 100 (internal/httpapi/batch_create.go). The request IS the transaction, so two requests are two transactions and two history entries where the role promises one — and a failure in the second leaves half a plan created, which is the outcome all-or-nothing exists to make impossible",
			SpecRow:  "D8 row 14, D9 L16",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-edgecount-bound", Kind: KindRefuse,
			What: "issueops.GraphCounter.CountEdges refuses a request naming more than 100 DISTINCT anchors, citing the wire bound; it is never chunked across requests",
			Why: "maxDependencyAnchors = 100 (internal/httpapi/edges.go), the same bound the stored-edge read on this collection carries — the role deliberately sets none of its own and says the bound belongs to the WIRE. " +
				"Chunking is what a READ could get away with and this one cannot: CountEdges promises that an anchor's EXISTENCE and its edge count are read from one consistent view, so two requests would let an anchor be reported missing by a probe that raced a create the other chunk's count already saw — one answer contradicting itself, which is the exact failure AnchorEdgeCount.Missing exists to make impossible. " +
				"Refusing at the bound fails loudly instead, and the caller splits the question knowing it asked twice. Upstream ask: a cursor or a higher bound on the anchor list, which retires this row",
			SpecRow: "D9 L16 (the same argument, on a read), D8",
			// The boundary itself, both sides: exactly 100 anchors serve, 101
			// refuse, and the refusal never dials.
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-batchcreate-target", Kind: KindDegrade,
			What:     "a batch create whose edge target names no issue refuses as issueops.ErrValidation alone, not as ErrValidation WRAPPING ErrNotFound",
			Why:      "the wire answers a dangling edge target with a 400 invalid_argument naming `items` (internal/httpapi's failBatchCreate), and deliberately does not quote the role's own message, which arrives as a driver error naming tables and constraints. The refusal still fails the whole batch and creates nothing — the promise the row is about — and only the second sentinel is lost. Upstream ask: a distinguishing code for the absent-target refusal",
			SpecRow:  "D8 row 14",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-batchcreate-inbatch", Kind: KindRefuse,
			What: "an edge onto an EARLIER ITEM OF THE SAME BATCH cannot be expressed at all over http",
			Why: "the capability needs the earlier item to have named an id for itself, and apigen.BatchCreateItem publishes no id member — an explicit id is refused with the rest of W-BatchCreateItem.Issue. So the target of an in-batch edge can only be a row the workspace already holds. " +
				"It is a REFUSE row rather than a degrade because the request that asks for it fails: the client refuses the explicit id before the dial, and there is no second spelling that would land the edge silently. The wire's own `target_id` description names this case, so the gap is in the SCHEMA rather than in the handler — the upstream ask is an item id member, which retires this row",
			SpecRow:  "D8 row 14",
			PinnedBy: pinnedByS3Conformance,
		},
		// L17 (the conditional GH#5005 dep-remove guard) is DELETED, not present as a
		// row: ga-b8ddd.30's dual run proved the wire surfaces unresolved
		// depends_on_id rows verbatim (see pinnedElsewhere above), so the
		// degradation it was contingent on never materialized. The L-sequence skips
		// from L16 to L18 for that reason.
		{
			// IT WAS MISSING HERE while the design carried it and F-close cited
			// it TWICE, so RowByID("L18") panicked on a citation the ledger's own
			// prose made. The design's record is the one transcribed below.
			ID: "L18", Kind: KindDegrade,
			What: "DOWN-LEVEL ONLY: against a server that does not advertise issues.batchClose, a single-id `bd close` still composes onto closeIssue and records the SERVER's own close commit message rather than the batch's id-naming entry; the multi-id and --claim-next shapes refuse there",
			Why: "the composed leg is one closeIssue call, so the history entry it leaves is the one that operation writes for itself — the batch's entry names the ids it closed and this one cannot, because no batch ran. " +
				"Where issues.batchClose IS advertised the whole request is one call and one server-side transaction, so this divergence does not arise at all; the down-level leg survives on purpose, because `bd close` worked against pre-batchClose servers before this port and refusing it now would be a regression dressed as progress. It is the same posture L14 takes toward issues.claimNext. " +
				"It is a degrade rather than a refuse because the close LANDS and the answer is neither narrower nor wider than the caller asked for — only the record of it is the operation's rather than the batch's. Retirement is a fleet fact rather than an upstream ask: the row goes when no server this client may meet is older than issues.batchClose",
			SpecRow: "D9 L18, D8 row 17",
			// The down-level leg, driven against a store whose stub carries no
			// capabilities: the single-item shape composes and every other shape
			// refuses without dialing.
			PinnedBy: pinnedByS3Conformance,
		},
	}
}

// writeSideRows are the refuse-not-drop enumeration D8 closes with: role-request
// fields on the SERVED writes that the wire publishes no member for.
//
// This package encodes none of these shapes — they are request bodies, not
// query strings — but the rows live here because the ledger is one artifact,
// and because a renamed or deleted field is caught by the same reflection the
// read shapes get.
func writeSideRows() []Row {
	const updateSpec = "D8 refuse-not-drop, D7"
	// The lifecycle wiring landed, and with it the sweep that drives every
	// excluded member one at a time and asserts each fails WITHOUT dialing. The
	// completeness of this population — that no member reaches the wire
	// unclassified in the first place — is the store package's write-ledger
	// gate, which reflects these rows against the apigen bodies.
	const pinned = pinnedByS3Conformance
	return []Row{
		{
			ID: "W-UpdateRequest.Claim", Kind: KindRefuse,
			Type: tyUpdateRequest, Field: "Claim",
			What: "a claim combined with any other UpdateRequest member refuses; a claim ALONE does not — it dials claimIssue directly (see httpLifecycle.claimOnlyUpdate)",
			Why: "a claim is claimIssue's own operation and updateIssue cannot perform one, on this wire or on any other; it is the one row here that upstream #5484 did not touch. " +
				"claimIssue's own request is the actor alone, so a claim-only UpdateRequest IS that request and is served through the Claimer role instead of refusing — that is gc's exclusive claim path, `bd update <id> --claim --json`. " +
				"What still refuses is PATCH claim: a claim folded into one atomic transaction WITH a patch, a guard or a force override, which no operation on this wire publishes and which is not synthesized as two calls (claimIssue then updateIssue), since that would let a caller observe an issue claimed but not yet patched. Upstream gastownhall/beads#6890 tracks the capability that would carry both in one call",
			SpecRow:  updateSpec,
			PinnedBy: pinned,
		},
		{
			// What/Why below are plain and short DELIBERATELY: RefusedError.Error()
			// prints both verbatim to whatever reads the returned error, all the
			// way out to a CLI user running `bd update <wisp-id> --claim` over an
			// http workspace — so this is user-facing prose, not an internal
			// engineering note, and it should read that way.
			//
			// The engineering rationale, for a future reader of this file: the v0
			// wire's claimIssue operation (issueops.Claimer's own contract)
			// answers a wisp id with ErrNotFound on every backend, including the
			// direct route's own claim-by-id — the wisp plane is not claimable
			// through that role at all. But claimOnlyUpdate's caller is
			// `bd update <id> --claim`, whose Lifecycle.Update DOES claim wisps
			// locally (issue-or-wisp routing). Over http, that command is served
			// through claimIssue rather than through updateIssue's general
			// wisp-aware routing, so a wisp id would otherwise surface the wire's
			// generic not-found — a live row reported as though it does not
			// exist, indistinguishable from an id naming nothing at all, which a
			// not-found-sensitive caller (gc's Claim among them) would read as
			// missing rather than as an unclaimable wisp. This refuses by name
			// instead, once the client itself recognizes the id names a wisp —
			// which it learns only AFTER claimIssue itself answers not_found (see
			// claimOnlyUpdate), so an ordinary claim of a real, non-wisp issue
			// never pays this probe. Serving a wisp-aware claim (a server-side
			// ClaimWisp capability) is tracked as a pending decision, not
			// implemented here.
			ID: "W-ClaimRequest.Wisp", Kind: KindRefuse,
			What:     "claiming a wisp is not supported over http",
			Why:      "the v0 wire's claim operation excludes the wisp plane on every backend; claim this issue from a local workspace instead",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-UpdateRequest.ForceAssigneeTransfer", Kind: KindRefuse, Flag: "--force",
			Type: tyUpdateRequest, Field: "ForceAssigneeTransfer",
			What: "bypassing the anti-steal assignee fence refuses",
			Why: "the wire publishes `force_assignee_transfer` (upstream #5484) and this client does not send it. What the refusal STANDS ON changed with client wave ga-7i6by and the row is narrower for it: the `assignee` the fence guards IS now sent (W-IssuePatch.Assignee), so what is left is the bypass alone. " +
				"The fence itself is unbypassable rather than absent — a transfer away from a live foreign in-progress owner refuses with already_claimed, and a matched `expected_assignee` is the OTHER bypass, which this client does send. Carrying this one is a two-member port tracked as ga-2ltro.15",
			SpecRow:  updateSpec,
			PinnedBy: pinned,
		},
		{
			ID: "W-UpdateRequest.ForceClosePolicy", Kind: KindRefuse, Flag: "--force",
			Type: tyUpdateRequest, Field: "ForceClosePolicy",
			What: "bypassing close policy on a status-crossing update refuses",
			Why: "the wire publishes `force_close_policy` (upstream #5484) and this client does not send it; see W-UpdateRequest.ForceAssigneeTransfer for what the refusal now stands on, since client wave ga-7i6by sends the `status` whose crossing the policy gates. " +
				"The policy is therefore enforced and not bypassable here: a status crossing into the done category with open children or a live blocker refuses with not_closable, typed, and writes nothing. Tracked as ga-2ltro.15",
			SpecRow:  updateSpec,
			PinnedBy: pinned,
		},
		{
			ID: "W-UpdateRequest.ExpectedVersion", Kind: KindRetired,
			What: "the compare-and-set row-version precondition used to refuse",
			Why: "RETIRED by the client wave that sends it (ga-jbuyf). The refusal was always the CLIENT's rather than the document's — upstream #5484 published `expected_version` and this client had not been taught to emit it — and the row said so in as many words: 'flipping it is a port, not a decision: send the member, retire this row, and turn the refusal pin into a round-trip pin'. " +
				"That is what happened. The member is now sent as a POINTER, so absent stays absent: 0 is a legal token (the migration-0054 backfill left rows holding it) and encoding 'no guard' as 0 would have armed a guard on every unguarded update. The pin below asserts the round trip in both directions",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-UpdateRequest.ExpectedAssignee", Kind: KindRetired,
			What: "the compare-and-set assignee precondition used to refuse",
			Why: "RETIRED with W-UpdateRequest.ExpectedVersion (ga-jbuyf). Its own trap is the mirror image of that one's: the EMPTY assignee is a real guard — it is how a caller says 'only if nobody holds it' — so this member cannot be omitted when it is empty either. " +
				"A pointer expresses both, and the pin drives the empty-string guard as its own case",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-UpdateRequest.ExpectedStatus", Kind: KindRetired,
			What:     "the compare-and-set status precondition used to refuse",
			Why:      "RETIRED with W-UpdateRequest.ExpectedVersion (ga-jbuyf). The status guard is the readable one — `Issue.status` is on every read of this surface, so a caller can guard a transition with no token at all — and it is sent as the workspace's own status vocabulary, verbatim",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-UpdateRequest.IssuePlaneOnly", Kind: KindRefuse,
			Type: tyUpdateRequest, Field: "IssuePlaneOnly",
			What: "restricting an update to the issue plane refuses",
			Why: "updateIssue publishes no plane restriction and the server's role auto-resolves both planes, so dropping the flag would EDIT the wisp the caller asked to be told did not exist — the widening refuse-not-drop exists to stop. " +
				"Not in the design's own enumeration: the page lists the UpdateRequest members it had reviewed, and this one was found while wiring the role",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-UpdateRequest.Provenance", Kind: KindRefuse,
			Type: tyUpdateRequest, Field: "Provenance",
			What: "labeling an update's history entry refuses",
			Why: "updateIssue publishes no provenance member and the server writes its own label, so a dropped Provenance would leave the history entry naming the SERVER's surface while the caller believed it named theirs. " +
				"It is refused rather than degraded because the field's whole purpose is the label, so dropping it drops the request",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-CloseRequest.ExpectedVersion", Kind: KindRetired,
			What:     "the compare-and-set row-version precondition on a close used to refuse",
			Why:      "RETIRED by the client wave that sends it (ga-jbuyf). CloseIssueRequest publishes expected_version (upstream #5506) and this client now emits it, so the guard reaches the server rather than refusing here — and the ordering the role promises, that the precondition is checked BEFORE the idempotent re-close, is the server's to keep and the served tier's to assert",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-ReopenRequest.ExpectedVersion", Kind: KindRetired,
			What:     "the compare-and-set row-version precondition on a reopen used to refuse",
			Why:      "RETIRED with W-CloseRequest.ExpectedVersion (ga-jbuyf). The design's enumeration named the close half only, and the reopen half is the same field on the same verb pair — it retires the same way",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-DeleteRequest.ExpectedVersion", Kind: KindRetired,
			What: "the compare-and-delete row-version precondition used to refuse",
			Why: "RETIRED with W-CloseRequest.ExpectedVersion (ga-jbuyf), and it is the one whose stakes made the refusal worth having: dropping this guard erases a row the caller had asked not to erase, with nothing left to compare afterwards. " +
				"The multi-id refusal the wire attaches to it is deliberately NOT anticipated client-side: distinctness is measured after trimming and collapsing duplicates, which is the normalization this role sends its ids verbatim to avoid re-implementing",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-ReopenRequest.Provenance", Kind: KindRefuse,
			Type: tyReopenRequest, Field: "Provenance",
			What:     "labeling a reopen's history entry refuses",
			Why:      "reopenIssue publishes no provenance member and the server writes its own fixed label (`bd serve: reopen issue`); see W-UpdateRequest.Provenance",
			SpecRow:  updateSpec,
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-AddDependenciesRequest.SkipPerEdgeCycleCheck", Kind: KindRefuse, Flag: "--no-cycle-check",
			Type: tyAddDeps, Field: "SkipPerEdgeCycleCheck",
			What:     "`bd dep add --no-cycle-check` refuses on the bulk --file path",
			Why:      "the wire deliberately leaves SkipPerEdgeCycleCheck UNPUBLISHED and therefore false (internal/httpapi/dependency_edit.go). The single-edge spelling forwards false and only gates the post-hoc cycle warning, which the wire-backed CycleDetector serves, so it is served-with-note rather than refused",
			SpecRow:  "D7, D8 row 18, D9 L16",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-IssuePatch.Status", Kind: KindRetired,
			What: "`bd update -s` used to refuse",
			Why: "RETIRED with client wave ga-7i6by. IssuePatchBody published `status` with upstream #5484 and this client's encoder now emits it; close and reopen still carry the lifecycle semantics a status write has nowhere to put — the reason and session under first-close-wins, the done-status normalization, the already-closed idempotence flag — and stay the operations to reach for. " +
				"What this member serves is the status moved ALONGSIDE other fields in one transaction, which is the thing two calls cannot do",
			SpecRow:  "D8 refuse-not-drop",
			PinnedBy: patchMemberPin,
		},
		{
			ID: "W-IssuePatch.Assignee", Kind: KindRetired,
			What: "assignment and unassignment through update used to refuse",
			Why: "RETIRED with W-IssuePatch.Status (ga-7i6by). The EMPTY STRING is the half that makes this member more than a rename: it UNASSIGNS, so a client that skipped an empty value would turn a real edit into no edit at all. " +
				"The anti-steal fence around it is the server's and is unchanged — a transfer away from a live foreign in-progress owner still refuses with already_claimed, and the two bypasses (`force_assignee_transfer` and a matched `expected_assignee`) are W-UpdateRequest.ForceAssigneeTransfer and the guard wave's own",
			SpecRow:  "D8 refuse-not-drop",
			PinnedBy: patchMemberPin,
		},
		{
			ID: "W-IssuePatch.Metadata", Kind: KindRetired,
			What: "a metadata patch used to refuse",
			Why: "RETIRED with W-IssuePatch.Status (ga-7i6by). IssuePatchBody publishes the same replace/merge/set/unset algebra MetadataPatch carries, member for member, so the client projects it rather than translating it and the ordering rule — merge, then set in key order, then unset, with replace refusing beside the other three — stays the ROLE's, applied by the same body a local workspace runs. " +
				"The CLEAR is the one state the wire's own struct cannot spell (`omitempty` omits it), which is why the document is built as a map and an empty replacement travels as the empty document. The compare-and-set door is a different question and is served by its own accessor (issues.casMetadata, ga-7i6by's other half)",
			SpecRow:  "D8 refuse-not-drop",
			PinnedBy: patchMemberPin,
		},
		// Four of these are refused by BOTH patch documents — updateIssue's and
		// issues:batchApply's — so one row covers both operations rather than a
		// second reason being minted for one fact. Owner is the exception and
		// says so: ApplyPatchBody publishes it, and the batch's own encoder
		// carries it.
		patchRow("SpecID", "a spec-id edit refuses", "neither IssuePatchBody nor ApplyPatchBody publishes spec_id"),
		patchRow("AwaitID", "an await-id edit refuses", "neither IssuePatchBody nor ApplyPatchBody publishes await_id"),
		patchRow("Owner", "an owner edit refuses ON updateIssue",
			"IssuePatchBody excludes owner — which the document itself calls an accident of order rather than a decision, since ApplyPatchBody DOES publish it and issues:batchApply's update item carries it"),
		patchRow("ClosedBySession", "a closed-by-session edit refuses", "neither IssuePatchBody nor ApplyPatchBody publishes closed_by_session"),
		patchRow("Persistence", "a persistence-mode edit refuses",
			"neither body publishes persistence: moving a row between planes mid-plan is a different act from writing its fields"),
		{
			ID: "W-IssuePatch.ParentID", Kind: KindRetired,
			What: "a parent re-hang through update used to refuse",
			Why: "RETIRED with W-IssuePatch.Status (ga-7i6by). It is ONE call rather than a remove-then-add pair, which is the whole reason the member exists: the two-call spelling leaves the issue parentless if the second call fails, and dependencies:add/remove — still the general write side of the graph over this wire — cannot express the atomic replacement. " +
				"The empty string removes every parent-child edge, so it travels like `assignee`'s empty string rather than being skipped. The graph refusals it earns are the server's and arrive typed: a cycle through the issue's own descendant as the PLAIN dependency_cycle, and an existing edge of another type as dependency_exists",
			SpecRow:  "D8 refuse-not-drop",
			PinnedBy: patchMemberPin,
		},
		{
			ID: "W-CreateBatchRequest.Provenance", Kind: KindRefuse,
			Type: tyCreateBatch, Field: "Provenance",
			What:     "labeling a batch create's history entry refuses",
			Why:      "batchCreateIssues publishes actor and items only, and the server writes its own label; see W-UpdateRequest.Provenance. It is the one Provenance whose ABSENCE is visible in shipped history — the surface this role serves has always named its source file in the entry (`bd: create 3 issue(s) from plan.md`)",
			SpecRow:  "D8 row 14 refuse-not-drop",
			PinnedBy: batchCreatePin,
		},
		{
			ID: "W-CreateBatchRequest.ForceIDPrefix", Kind: KindRefuse, Flag: "--force",
			Type: tyCreateBatch, Field: "ForceIDPrefix",
			What:     "permitting explicit ids outside the workspace's configured prefix refuses",
			Why:      "the wire publishes no such member — and could not honor one, since it publishes no explicit id for the flag to permit (W-BatchCreateItem.Issue). Dropping it would answer a request about WHERE ids may be written with a batch whose ids the server chose",
			SpecRow:  "D8 row 14 refuse-not-drop",
			PinnedBy: batchCreatePin,
		},
		{
			ID: "W-BatchCreateItem.Issue", Kind: KindRefuse,
			Type: tyBatchCreateItem, Field: "Issue",
			What: "an item whose Issue populates any member outside the wire's eight refuses, naming the member",
			Why: "apigen.BatchCreateItem carries title, description, design, acceptance_criteria, priority, issue_type, assignee and labels and nothing else, while the role accepts far more of a types.Issue: an explicit id, the wisp flags, metadata, the storage class, every timestamp, the gate/molecule/event fields. " +
				"ONE row rather than one per member because the reason is one reason — the wire's item vocabulary — and forty rows repeating it would say nothing a reader does not learn here. Exhaustiveness is held by REFLECTION instead: the client's carried and role-ignored tables are checked against types.Issue field by field, so a member added upstream is refused the day it lands rather than dropped until someone notices. " +
				"The members the role ITSELF ignores on a create (ContentHash, RowVersion, lease, compaction, routing overrides, hydration flags) are not in this population and are not refused: a local create drops them too",
			SpecRow:  "D8 row 14 refuse-not-drop",
			PinnedBy: batchCreatePin,
		},
		// The three edge rows are BATCH-CREATE's, and the qualifier is
		// load-bearing now that a second operation writes the same shape:
		// createIssue's own edge publishes `reverse` and `metadata` (its issue
		// HAS an id for a target to point back at, which a batch item does
		// not), so the client sends both there. Only ThreadID is refused on
		// both, because no operation publishes a thread member.
		createDependencyRow("Reverse", "an edge written from the target back to the new issue refuses ON A BATCH CREATE",
			"BatchCreateDependency carries target_id and type only; dropping Reverse would write the edge in the OPPOSITE direction from the one asked for, which is a different graph. createIssue's CreateIssueDependency DOES publish it, and the single-create path sends it"),
		createDependencyRow("Metadata", "typed edge metadata refuses ON A BATCH CREATE",
			"BatchCreateDependency publishes no metadata member, and a waits-for gate whose metadata was dropped is a readiness rule that silently does not hold. createIssue's edge publishes it, and the single-create path sends it verbatim"),
		createDependencyRow("ThreadID", "associating an edge with a discussion thread refuses",
			"neither BatchCreateDependency nor CreateIssueDependency publishes a thread member"),
		{
			ID: "W-CreateRequest.IDPrefix", Kind: KindRefuse,
			Type: tyCreateRequest, Field: "IDPrefix",
			What: "overriding the prefix an explicit id is checked against refuses",
			Why: "createIssue publishes no `id_prefix`, and the omission is the SERVER's decision rather than a gap (internal/httpapi/create.go): the field exists because a workspace's own config.yaml prefix wins over the database's and only a local front door can read that file, so a remote caller's config.yaml describes a workspace this server does not serve. " +
				"Publishing it would let a caller override the served workspace's prefix rule from outside it. Dropping it instead would check the id against the SERVER's prefix while the caller believed it was checked against theirs — the same request, two different answers to 'may this workspace mint this id'",
			SpecRow:  "D8 row 16 refuse-not-drop",
			PinnedBy: createPin,
		},
		{
			ID: "W-CreateRequest.Issue", Kind: KindRefuse,
			Type: tyCreateRequest, Field: "Issue",
			What: "a create whose Issue populates any member outside the wire's twenty refuses, naming the member",
			Why: "createIssue publishes the whole create VOCABULARY — id, title, description, design, acceptance_criteria, notes, status, issue_type, priority, assignee, owner, estimated_minutes, external_ref, due_at, defer_until, sender, metadata, labels, ephemeral, no_history — and deliberately not the rest of a types.Issue: the creation stamp (created_at, created_by), because a caller-supplied creation time makes the row disagree with the journal entry that records it and re-dating history is what an import is for; and spec_id, await_*, mol_type, wisp_type, work_type, storage_class, source_*, pinned, is_template and the event quartet, which this surface publishes on no operation, read or write. " +
				"ONE row rather than one per member for W-BatchCreateItem.Issue's reason — the reason is one reason — and exhaustiveness is held by the same REFLECTION over types.Issue, sharing the role-ignored table with the batch so the two operations cannot disagree about what the ROLE drops. " +
				"Issue.Comments and Issue.Dependencies are not in this population: the role itself refuses them, so a local create fails too and this is validation rather than divergence",
			SpecRow:  "D8 row 16 refuse-not-drop",
			PinnedBy: createPin,
		},
		{
			ID: "L-create-notfound", Kind: KindDegrade,
			What: "a create whose dependency or waits-for target names no row refuses as issueops.ErrValidation ALONE — not as ErrValidation wrapping ErrNotFound — and does not name the target",
			Why: "the wire answers a dangling target with a 400 invalid_argument naming `dependencies` and a FIXED detail (internal/httpapi's failCreateIssue), which deliberately does not quote the role's own message: that message arrives as a driver error naming tables and constraints, and 4xx details on this surface reflect the caller's own input back rather than server internals. " +
				"The refusal still fails the whole request and creates NOTHING — the promise the row is about — and only the second sentinel and the target's name are lost. It is batchCreateIssues' L-batchcreate-notfound on the single create, and it retires the same way: an upstream code that distinguishes the absent-target refusal and carries the target. " +
				"A missing --parent target already retired out of this row: failCreateIssue's errors.As(&parentNotFound) arm answers it with a distinguishing 404 not_found that names the target, and the client maps that to issueops.ErrNotFound (see TestServedCreateRefusesAnAbsentParentAsNotFound) — the row now covers only dependency and waits-for targets",
			SpecRow:  "D8 row 16",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-create-prefix", Kind: KindDegrade,
			What: "an unforced explicit id outside the workspace's configured prefix refuses as issueops.ErrValidation rather than as storage.ErrPrefixMismatch",
			Why: "the wire spells that refusal as a 400 invalid_argument with `param: \"id\"` and `reason: invalid_value` (internal/httpapi's failCreateIssue), which is the SAME pair a malformed or oversized `id` earns — so nothing on the wire tells the two apart, and reconstructing the typed sentinel from the pair would misclassify every other refusal of that member. " +
				"The guard itself is untouched: the id is refused, nothing is created, and the detail names `force_id_prefix` as the bypass, which is the recovery a caller needs. What is lost is the errors.Is arm both local front doors use to decide whether to re-offer the create with --force. Upstream ask: a prefix_mismatch code, or a reason that distinguishes it, which retires this row",
			SpecRow:  "D8 row 16",
			PinnedBy: pinnedByS3Conformance,
		},
		// The issues:batchApply population, from client wave ga-mijra. It is
		// SMALL, and that is the operation rather than an oversight: the create
		// item publishes createIssue's whole twenty, the close item maps whole,
		// the edge item maps whole, and the update item's patch is WIDER than
		// updateIssue's in the two places that matter to a plan (a full label
		// patch, and `owner`). What is left is one patch member and two facts
		// about the RESULT.
		{
			ID: "W-CreateItem.Issue", Kind: KindRefuse,
			Type: tyApplyCreateItem, Field: "Issue",
			What: "a create ITEM whose Issue populates any member outside the wire's twenty refuses, naming the member",
			Why: "ApplyCreateItem publishes exactly createIssue's create vocabulary — id, title, description, design, acceptance_criteria, notes, status, issue_type, priority, assignee, owner, estimated_minutes, external_ref, due_at, defer_until, sender, metadata, labels, ephemeral, no_history — and deliberately not the rest of a types.Issue, for the reasons W-CreateRequest.Issue gives in full. " +
				"It is a row of its OWN rather than a citation of that one because the operations are different: a caller auditing why their PLAN refuses must not be sent to a row about a single create, and the two can diverge the day either vocabulary moves. The partition itself is SHARED — one carried table, one role-ignored table, held against types.Issue by the same reflection — so they cannot disagree about what the wire carries or about what the role drops. " +
				"Issue.Comments and Issue.Dependencies are not in this population and are refused as validation on both sides: edges in this role are ITEMS, so a create item has nowhere to put one at all",
			SpecRow:  "D8 refuse-not-drop",
			PinnedBy: applyPin,
		},
		{
			ID: "W-ApplyPatch.ParentID", Kind: KindRefuse,
			Type: tyIssuePatch, Field: "ParentID",
			What: "a parent re-hang inside an update ITEM refuses, where the same member serves on updateIssue",
			Why: "ApplyPatchBody deliberately publishes no parent_id, and the absence is this operation's ONE-EDGE-ONE-SPELLING rule rather than a gap: a parent is a dep_add item of type parent-child, so the ORDER of every edge in a plan stays total and there is exactly one place an edge is written. " +
				"The single patch has no ordering to express and publishes the member directly (W-IssuePatch.ParentID, retired by client wave ga-7i6by), which is why the same Go field is carried by one document and refused by the other. " +
				"Refusing is not a loss of capability, only of spelling: a plan re-hangs a parent with a dep_add item, which is the atomic replacement in the position the caller declared it",
			SpecRow:  "D8 refuse-not-drop",
			PinnedBy: applyPin,
		},
		{
			ID: "L-apply-snapshot", Kind: KindDegrade,
			What: "every ItemResult of a batch apply comes back with Issue nil, where a local backend hydrates a post-item snapshot",
			Why: "the WIRE result is lean by the document's own decision and the role's leaf says why: ApplyItemResult carries ids, `changed` and `revision` and no issue, because the Go contract carries the snapshot for its COMPLETION HOOKS — which hand a script the row they are telling it about — and hooks never fire on the http surface at all. A hundred hydrated issues with their labels and edges would be a response an order of magnitude larger than the request that produced it. " +
				"What a caller loses is a read it can make: the ids ARE published, per item and through Keys, so the rows are one getIssue away and the plan's next step is composed from ids rather than from snapshots. It is a degradation rather than a refusal because nothing about the WRITE differs — every item landed exactly as asked — and refusing would make the whole operation unavailable over http to keep a member no consumer on this surface can use",
			SpecRow:  "D8 refuse-not-drop (result half)",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-apply-ref", Kind: KindDegrade,
			What: "an unresolvable metadata_refs key comes back as a *RefError naming the MEMBER rather than the key, and the reconstructed error replaces the server's problem envelope",
			Why: "the wire's 400 names the offending member in `param` — `items[3].create.metadata_refs` — and carries the item index, the item's key and `declared_later`, but not WHICH entry of the refs map failed: the role's RefError.Member is diagnostic prose (`metadata_ref <key>`) rather than a vocabulary, so the server maps it onto the document's own member names instead of publishing it. The two ADDRESSING refs, target and source, are named exactly. " +
				"The envelope is dropped because issueops.RefError has nowhere to put it: its Unwrap is hardcoded to ErrValidation and it carries no member for a cause, which is the role type's shape rather than a choice made here. The discriminator a caller ACTS on — an ordering mistake against a typo — survives whole, in both polarities. Upstream ask: a typed ref-key member on the problem document, which retires this row",
			SpecRow:  "D8 refuse-not-drop (result half)",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			// The COMMENT population, from client wave ga-f352s, and it is the
			// one divergence on that role: the request maps member for member,
			// so nothing refuses on shape and there is no W- row.
			ID: "L-comment-author", Kind: KindDegrade,
			What: "a comment's AUTHOR is subject to the server's own actor rule, which no local leg applies: it is stored TRIMMED where a local backend stores it verbatim, and one carrying a control character is refused (issueops.ErrValidation) where a local backend stores it",
			Why: "the operation validates `author` with the `actor` rules whole (internal/httpapi's validateNameMember: trim, refuse empty, refuse past 256 bytes or 255 characters, refuse any control rune) and passes the TRIMMED value on to the role, while issueops.ValidateAddCommentRequest checks only that the field is non-empty and stores what it was given. So the same request produces a different STORED VALUE on the two legs, and a value one leg stores the other refuses. " +
				"IT IS THE SERVER'S RULE AND THE DOCUMENT PUBLISHES IT, which is why this is a ledger row rather than a bug on either side: the schema states the trim and the character rule in as many words, and the reason is the column — `author` is 255 characters wide and every renderer of the thread prints it, where an unfiltered C1 introducer is an escape-sequence payload. The refusal half is a NARROWING the wire makes on purpose. " +
				"THE CLIENT DOES NOT ANTICIPATE EITHER HALF, and that is the decision this row records. Trimming here would make the client agree with the server about the stored value and disagree with the role's own contract — and it would silently rewrite a caller's request, which is the one thing this seam never does. Refusing the control rune here would be a second copy of a server rule that is free to move. So the value travels as the caller wrote it and the server's answer is reported unchanged, which keeps the divergence visible instead of laundering it. " +
				"IT IS A DEGRADE RATHER THAN A REFUSE because the ordinary path PROCEEDS: a well-formed author is stored, and the difference cannot be misread as a wider or narrower answer — a comment lands on the thread the caller named either way. The refusal half is the SERVER's, not this client's refuse-not-drop, which is what KindRefuse classifies. " +
				"What a caller loses is an author whose surrounding space survives, and the recovery is the one the role's own contract already implies: compose the author from a value you would be willing to see printed. Upstream ask: either the role trimming its own author on every leg, or the operation storing it verbatim — the rows agree the day the two rules become one",
			SpecRow:  "D8 row 19",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			// The RELEASE population, from client wave ga-f352s, and it has
			// exactly one row. releaseIssue's request maps member for member —
			// actor, the guard, force and the path — so nothing refuses on
			// shape; what diverges is one REFUSAL VOCABULARY, which is the same
			// shape as the two create parks and retires the same way.
			ID: "L-release-notclaimed", Kind: KindDegrade,
			What: "a release of a row that holds NO CLAIM answers issueops.ErrNotReleasable rather than issueops.ErrNotClaimed; a status that will not accept a release answers ErrNotReleasable as it does everywhere",
			Why: "the role splits the two — ErrNotClaimed for a row nobody holds, ErrNotReleasable for a status the transition is not defined over — and the wire spells BOTH `not_releasable` under one code, with no member telling them apart. That is the server's deliberate choice (internal/httpapi's CodeNotReleasable) rather than an omission, and its detail names both conditions instead of guessing between them. " +
				"So this client maps to the WIDER of the two, which is the only honest read: reconstructing ErrNotClaimed would assert a fact about the row that the wire never sent, and scraping the detail for it would bind a sentinel to prose. " +
				"NOTHING ELSE ABOUT THE REFUSAL MOVES — nothing is written, the row and its version are untouched, and the conditional path is unaffected: a caller that named a holder gets ErrAssigneeMismatch on both sides, because that refusal has a code of its own. " +
				"The caller who loses something is the one that told 'I already released this' from 'the row was never claimed', and the recovery is the one the server's own detail prescribes: READ THE ROW rather than retrying blind. Upstream ask: a code, or a member, that distinguishes the unheld row from the unreleasable status — which retires this row",
			SpecRow:  "D8 (Releaser), D9",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "L-release-notowner", Kind: KindDegrade,
			What: "an unforced, unconditional release by an actor that is NOT the holder answers issueops.ErrAlreadyClaimed — carrying *issueops.ClaimConflictError — rather than issueops.ErrNotOwner",
			Why: "the wire answers the ownership fence with `already_claimed`, which is the code updateIssue already gives the same situation (a live foreign owner refusing a write, with a force bypass and a name-the-holder bypass), and this client maps that code to the sentinel every other operation means by it. The two sentinels are distinct values in issueops and neither wraps the other, so the arm a caller writes differs. " +
				"REPORTING IT AS ErrNotOwner WOULD BE THE WORSE TRADE, not merely a different one: the code is shared with the claim refusals, so a client that special-cased it for this operation would be reading the OPERATION to decide the sentinel rather than the code — and would then answer ErrNotOwner for a genuine already_claimed the day the server reuses the code here for anything else. " +
				"WHAT SURVIVES IS EVERYTHING A CALLER ACTS ON: the refusal is typed, it names the row (the request supplied the id the wire leaves out), nothing is written, and BOTH bypasses answer exactly as the role says — force releases the foreign claim, and naming the holder in expected_assignee releases it too, because a match replaces the fence. The wire deliberately sends no `assignee` member here, so the holder is not named: absence means 're-read the row', never 'nobody holds it'. Upstream ask: a code, or a member, that separates the release's fence from a claim conflict — which retires this row",
			SpecRow:  "D8 (Releaser), D9",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			ID: "W-IssuePatch.Labels", Kind: KindRetired,
			Type: tyIssuePatch, Field: "Labels",
			What: "RETIRED — the whole ordered label edit crosses: `labels` replaces, `add_labels` adds, `remove_labels` removes",
			Why: "upstream #5510 published add_labels and remove_labels on IssuePatchBody and client wave ga-jpywb emits both, which is the retirement path this row named. " +
				"The three travel as FLAT siblings where applyBatch nests them under one object, and the server assembles all three into ONE issueops.LabelPatch — so the algebra the role states (replace, then add, then remove; removal wins) is applied server-side and is never this client's to arrange. " +
				"WHAT THE REFUSAL WAS PROTECTING is worth keeping after the refusal is gone: degrading an incremental edit onto the replace-only member would have meant reading the set, adding to it and writing it back, which silently drops any label another writer added in between. `bd label add` and every agent that tags work concurrently are exactly that caller, so the refusal was right for as long as the members did not exist — and it is the members that retire it, not a change of mind about the read-modify-write",
			SpecRow:  "D8 refuse-not-drop, open questions (tracked-refusal roadmap)",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			// The CONFIG-WRITE population, from client wave ga-jpywb, and it has
			// exactly one row. Both verbs map member for member — a key in the
			// path, a value in the body, nothing else — so nothing refuses on
			// shape; what diverges is a pair of EDGE BOUNDS the wire applies
			// and no local leg does.
			ID: "L-config-bounds", Kind: KindDegrade,
			What: "a setting VALUE past types.MaxTextBytes bytes, and a setting KEY past types.MaxFieldLen characters on the WRITE, are refused with issueops.ErrValidation naming the member; on a local leg the same request reaches the column and fails with the engine's own `too large for column` error, which is no sentinel at all",
			Why: "the bound belongs to the OPERATION rather than to the role: internal/httpapi's settingWriteKey and settingValue check both before the role is called, because a request the caller could have fixed should be a 400 naming the member rather than the 500 a column overflow would otherwise produce. workapi.ValidateSettingWrite — the rule every leg shares — applies no length bound at all, so below the wire the column is the only check there is. " +
				"MEASURED, not inferred: a 65536-byte value answers `400 invalid_argument: \\`value\\` is 65536 bytes; storage holds at most 65535` over http and `Error 1105: string ... is too large for column 'value'` on the embedded leg, and a 306-character key splits the same way. Exactly 65535 bytes is accepted on both. " +
				"THIS CLIENT RESTATES NEITHER BOUND, and that is the decision this row records. They are the SERVER's policy on a value whose shape the role accepted, one release away from moving, so a second copy here would either drift or refuse a request a newer server would take — the same argument L-comment-author makes about the author rule, and the same posture. What travels back is the operation's own 400, which the problem mapper turns into ErrValidation. " +
				"IT IS A DEGRADE RATHER THAN A REFUSE because the ordinary path PROCEEDS and because the divergence runs in the CALLER'S FAVOR: over http an oversized write is a typed, member-named refusal that wrote nothing, where a local leg hands back a driver error no caller can classify. What a caller loses is nothing; what a caller must not assume is that a value a local workspace stored will store here. " +
				"THE TWO BOUNDS ARE NOT SYMMETRIC ACROSS THE VERBS, and the asymmetry is the operation's: DELETE checks only that the key names something, so removing a 306-character key succeeds over http exactly as it does locally. Upstream ask: the length rules moving onto the shared validator, which retires this row by making the two legs one",
			SpecRow:  "D8 row 11, D9",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			// The UPDATE-BOUNDS row, and L-config-bounds' sibling one operation
			// over. It is not the same row because the two are not the same
			// situation: there the role applies no length rule at all and the
			// column is the only check below the wire, HERE THE ROLE APPLIES THE
			// SAME RULE with the same constant — so what is lost is a sentinel
			// rather than a bound.
			ID: "L-update-fieldlen", Kind: KindDegrade,
			What: "an updateIssue patch member past types.MaxFieldLen characters — a label on any of the three label members, `title`, `external_ref` — is refused with issueops.ErrValidation naming the member; on a local leg the same request answers types.ErrFieldTooLong",
			Why: "the bound is applied TWICE and identically: internal/httpapi/update.go checks types.CheckFieldLen at the edge before the role is called, and internal/storage/issueops applies the same function below it. So this is a classification divergence and not a policy one — a value one leg stores the other stores, and a value one leg refuses the other refuses. " +
				"WHAT COARSENS IT is the spelling: the operation refuses with `invalid_argument` and reason `invalid_value` (internal/httpapi's refuse on the patch member), which is the SAME pair a malformed status, an unparseable timestamp or any other bad value on the same member earns. Nothing on the wire tells them apart, so reconstructing ErrFieldTooLong from the pair would misclassify every other refusal of that member — the argument L-create-prefix makes about the id, on the patch. " +
				"MEASURED, not inferred: a 256-character label answers `400 invalid_argument: \\`add_labels[0]\\` is 256 characters; storage holds at most 255` over http and ErrFieldTooLong through the reference store's own Lifecycle role, and exactly 255 characters is accepted on both. " +
				"THIS CLIENT RESTATES THE BOUND NOWHERE, and the reason differs from L-config-bounds': not that the rule is the server's to move, but that a second copy of a rule two layers already apply is a third place for it to drift. What travels back is the operation's own 400. " +
				"IT IS A DEGRADE RATHER THAN A REFUSE because the path PROCEEDS and the outcome is identical on both legs — the request fails, the row is untouched, and no truncated value is stored — while the detail names the offending member and its length, which is the recovery a caller needs. What is lost is the errors.Is arm a caller writes to tell 'too long' from 'malformed'. Upstream ask: a `too_long` reason, or a code of its own, which retires this row",
			SpecRow:  "D8 row 16, D9",
			PinnedBy: pinnedByS3Conformance,
		},
		{
			// The EVENTS-JOURNAL population, from client wave ga-jpywb, and it
			// has exactly one row. The read maps member for member — a
			// checkpoint, a bound, a page of records and a head — and the typed
			// truncation crosses whole through the problem document's three
			// window members. What diverges is the one promise a PAGED transport
			// cannot make about an UNPAGED read.
			ID: "L-events-paging", Kind: KindDegrade,
			What: "an UNCAPPED journal read (limit 0) is served as a loop of handler-sized requests rather than one, so its rows and its head no longer come from a single instant; a bounded read is one request and is unaffected",
			Why: "the role imposes no ceiling of its own — journalops.Journal says a limit of 0 means uncapped, because the caller that pages a hundred thousand records out to a file is as legitimate as the one polling for ten — and the OPERATION refuses `limit=0` by value, capping a page at 10000 (internal/httpapi's maxEventsLimit). That cap is the handler's promise to its clients rather than a narrowing of the role, and this client is on the other side of it. " +
				"SO THE LOOP IS THE ONLY HONEST READING. Refusing an uncapped read would refuse something the role permits; capping one silently at 10000 would answer a partial page beside a head saying there is more, which is a consumer stalling on a gap nobody told it about. Both are worse than N transactions. " +
				"WHAT SURVIVES IS WHAT A CONSUMER ACTS ON: rows stay contiguous and seq-ascending, because each page continues exactly where the last ended; the head is the LAST page's, so it is the freshest and can only be at or ahead of the last row served; and a truncation on any page is the whole read's failure, never a shorter answer — a loop that returned the prefix it had already gathered would answer a gap with a plausible-looking suffix, which is the one failure a replay feed must never ship. " +
				"WHAT DOES NOT SURVIVE is atomicity across the whole answer: a mutation committing mid-loop appears in a later page of the SAME call rather than in the next one. A consumer cannot observe that as a gap or a duplicate — it reads as records it would have received on its next poll, arriving early — which is why this is a degrade rather than a refusal. " +
				"IT IS A DEGRADE RATHER THAN A REFUSE for the ordinary reason too: the path proceeds, and the answer cannot be misread as narrower or wider than the caller asked for. Upstream ask: an unlimited spelling on the operation, or a cursor the page can hand back — either retires this row",
			SpecRow:  "D8 (Journal), D9",
			PinnedBy: pinnedByS3Conformance,
		},
	}
}

// batchCreatePin is the one sweep behind every batch-create refusal row: it
// drives each excluded member one at a time and asserts the call fails WITHOUT
// dialing, and it holds the carried/ignored partition against types.Issue by
// reflection so the population cannot go stale by omission.
const batchCreatePin = pinnedByS3Conformance

// createPin is the single-create counterpart: the same shape of sweep, driving
// every excluded member of a CreateRequest one at a time and asserting the call
// fails WITHOUT dialing, with the carried/ignored partition held against
// types.Issue by reflection.
const createPin = pinnedByS3Conformance

func createDependencyRow(field, what, why string) Row {
	return Row{
		ID: "W-CreateDependency." + field, Kind: KindRefuse,
		Type: tyCreateDep, Field: field,
		What:     what,
		Why:      why + " (internal/httpapi/spec BatchCreateDependency)",
		SpecRow:  "D8 row 14 refuse-not-drop",
		PinnedBy: batchCreatePin,
	}
}

// applyPin is the sweep behind every batch-apply refusal row: it drives each
// excluded member one at a time and asserts the call fails WITHOUT dialing,
// with the create item's carried/ignored partition held against types.Issue by
// the same reflection the two other create paths use.
const applyPin = pinnedByS3Conformance

// patchMemberPin is the round trip behind the four patch members client wave
// ga-7i6by carries: it drives each one into the document the role builds and
// reads it back off the transport, which is the one thing about this operation
// no result can show.
const patchMemberPin = pinnedByS3Conformance

func patchRow(field, what, why string) Row {
	return Row{
		ID: "W-IssuePatch." + field, Kind: KindRefuse,
		Type: tyIssuePatch, Field: field,
		What:     what,
		Why:      why + " (internal/httpapi/update.go issuePatchMembers)",
		SpecRow:  "D8 refuse-not-drop",
		PinnedBy: pinnedByS3Conformance,
	}
}

// commandRows are D7's named members of the refused class that carry no request
// field: whole commands and the flag modes of served commands.
func commandRows() []Row {
	// The cmd/bd wiring landed. Two pins, split the way the mechanism is: a
	// whole command is early-refused from the classification table and every
	// entry in it is driven against the REAL cobra tree, while a served
	// command's refused flag mode is a per-flag invocation.
	const refusedCommand = pinnedByS3Conformance
	const refusedFlagMode = pinnedByS3Conformance
	// Every row this helper builds cites D7 and nothing else: D7 IS the
	// whole-command-and-flag-mode decision, and a row citing a second decision
	// is a row that wants writing out in full (F-count and F-close both do).
	row := func(id, command, flag, what, why string) Row {
		pinned := refusedCommand
		if flag != "" {
			pinned = refusedFlagMode
		}
		return Row{ID: id, Kind: KindRefuse, Command: command, Flag: flag, What: what, Why: why, SpecRow: "D7", PinnedBy: pinned}
	}
	return []Row{
		row("F-ready-explain", "ready", "--explain", "`bd ready --explain` refuses",
			"it dispatches onto raw GetBlockedIssues/GetIssuesByIDs/GetDependencyCounts/DetectCycles, three of which are on the unsupported allowlist"),
		row("F-ready-mol", "ready", "--mol", "`bd ready --mol` refuses",
			"its molecule subgraph load reaches findHierarchicalChildren, an IDPrefix SearchIssues shape the parent-walk bridge cannot express (E-IssueFilter.noShape). GetDependencyRecords and GetDependentsWithMetadata, which the same load also calls, stopped being the blocker when ga-b8ddd.30 flipped them onto the wire"),
		row("F-ready-gated", "ready", "--gated", "`bd ready --gated` refuses",
			"it dispatches onto a filtered SearchIssues shape the bridge cannot express, plus GetDependents"),
		row("F-show-thread", "show", "--thread", "`bd show --thread` refuses",
			"showMessageThread renders each reply's sender, recipient, body and timestamp off GetDependentsWithMetadata, but ga-b8ddd.30 flipped that read onto getIssue's include_dependents parameter, which carries the collectDependents SHALLOW projection (id/status/type/priority/title + edge type only, be-4d36f2). Those fields come back zeroed over http and zero-timestamp replies sort ahead of the root — a silent-wrong degrade the refuse-over-degrade doctrine forbids. Retires with a full-dependent wire shape, a server-side ask"),
		row("F-show-refs", "show", "--refs", "`bd show --refs` refuses",
			"showIssueRefs's --json marshals the full GetDependentsWithMetadata rows, but the wire answers the same collectDependents shallow projection, so created_at/assignee/description come back zeroed and the JSON differs from a local workspace. The text render consumes only the shallow fields, but the flag cannot be split from its --json mode, so the whole flag refuses rather than serve a divergent JSON silently"),
		row("F-show-children", "show", "--children", "`bd show --children` refuses",
			"showIssueChildren's --json marshals the full GetDependentsWithMetadata rows, the same collectDependents shallow projection as --refs, so created_at/assignee/description come back zeroed over http; `bd children` refuses as a whole command for the same reason"),
		row("F-mol-ready", "mol ready", "", "`bd mol ready` refuses",
			"it is the same runMolReadyGatedCore body as `bd ready --gated`"),
		row("F-blocked", "blocked", "", "`bd blocked` refuses",
			"it is raw GetBlockedIssues, which is on the unsupported allowlist"),
		{
			ID: "F-count", Kind: KindRetired, Command: "count",
			What: "RETIRED — `bd count` is SERVED whole",
			Why: "it said the command refuses because \"issueops.Counter has no wire operation; a counts operation is a filed upstream ask\", and that ask was DELIVERED: upstream #5508 published countIssues, client wave ga-icks1 wired the Counter accessor, and `count` moved from httpRefusedCommands into httpServedCommands. D8 row 20 and docs/reference/http-workspaces.md have said so since; this row was the last place that did not. " +
				"Every flag the command publishes reaches a wire member — the whole predicate, the six instant bounds, the three emptiness checks, --include-infra, and the five --by-* flags as the one group_by parameter — so there is no refused mode left to name either. " +
				"HOW IT ROTTED IS THE PART WORTH KEEPING. Its pin was TestClassifiedRefusedCommandsRefuseBeforeTheOSSPreRun, which walks httpRefusedCommands and drives every entry. The moment `count` left that table the pin stopped covering this row — it stayed LIVE BY NAME, so the well-formedness gate that resolves PinnedBy to a real test kept passing, and DEAD BY COVERAGE, so nothing anywhere asserted the row's claim. A ledger row can be false for a whole wave under a green pin, which is what TestFRowsAgreeWithTheCommandClassification now makes impossible",
			SpecRow: "D7 (RETIRED), D8 row 20",
			// The retirement's own evidence: the corpus replay compares `bd
			// count`'s answer byte-for-byte over http against the same command
			// over a local Dolt workspace, and a count's whole answer is a
			// number — a predicate narrowed or widened on the way out prints a
			// different integer and nothing else differs.
			PinnedBy: pinnedByS3Conformance,
		},
		row("F-serve", "serve", "", "`bd serve` refuses against an http workspace",
			"the OSS classifier would happily serve it from the opened store, and a bd serve re-serving a remote bd serve is a proxy chain nobody designed: run serve where the database is"),
		{
			ID: "F-close", Kind: KindRefuse, Command: "close", Capability: "issues.batchClose",
			What: "`bd close` is SERVED whole — single id, several ids and --claim-next — over issues:batchClose; it refuses only against a DOWN-LEVEL server that does not advertise issues.batchClose",
			Why: "the CLI's close front door routes every id, single-id included, through BatchCloser.CloseBatch, and Lifecycle.Close has zero CLI callers at tip. The wire now carries issues:batchClose, so the whole CloseBatchRequest — multi-item and the atomic ClaimNext included — is one wire call and one server-side transaction; nothing is composed or refused where the capability is present. " +
				"Against a server too old to advertise issues.batchClose, servesBatchClose() reads the handshake snapshot and routes around the batch dial entirely, so no Preflight runs and no wire.ErrCapabilityAbsent is raised: the down-level leg's refuseUnservedCloseShape raises the STORE's own *ErrHTTPUnsupported (batchcloser.go), and cmd/bd's renderEscapedHTTPRefusal turns it into the capability-absent taxonomy (case 2) by scanning the served command's own capability list for the first token the server does not advertise — issues.batchClose, which is the token it then names to upgrade to. The single-item, no-ClaimNext shape still composes onto closeIssue in that down-level leg (see L18); the shapes that cannot compose refuse there. The item cap is L-close-cap",
			SpecRow: "D8 row 17 (resolved, Wire), D9 L18, D9 L-close-cap",
			// The command is served; the down-level refusal is the capability gate's,
			// exercised against a server without issues.batchClose.
			PinnedBy: refusedFlagMode,
		},
		{
			ID: "F-partial-id", Kind: KindRefuse,
			What:     "partial-id resolution refuses with its own taxonomy text",
			Why:      "SearchIssueIDs has no wire operation, and the client cannot tell a partial id from a full id that does not exist — so the refusal text covers both outcomes rather than falling through to a raw search error",
			SpecRow:  "D11",
			PinnedBy: pinnedByS3Conformance,
		},
	}
}
