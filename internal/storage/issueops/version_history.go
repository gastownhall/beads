package issueops

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/gowebpki/jcs"

	"github.com/steveyegge/beads/internal/types"
)

// Dual-write issue-version history records every accepted issue mutation as a
// row in issue_versions, written in the SAME transaction as the mutation
// itself (see internal/storage/schema/migrations/0067_add_versioned_beads_schema.up.sql).
// Activation mirrors the durable events journal (journal.go): a per-instance
// flag (storage.VersionedHistoryConfigurer) is bound to a concrete
// transaction via ScopeVersionedHistoryTransaction immediately after
// BeginTx, so enabling history on one store instance cannot turn it on for
// any other sharing the process.
//
// RecordVersionInTx (and its create-shaped sibling, RecordVersionForCreateInTx
// — design §16.2b's write fence, see that function's own doc) is the single
// seam both direct-SQL legs (dolt, embeddeddolt) and the domain/db package
// (used by uow) call through, from inside the same already-short-circuited
// functions that call RecordEventInTx — every accepted mutation of an
// issue's durable state (create, update, close, reopen, claim, release,
// lease reclaim, defer wake, label add/remove, dependency add/remove,
// promote, persistence move) mints exactly one row, as its LAST
// durable-state write; a no-op mints none. A
// mutation DiscardNoopIssueUpdates has already discarded never reaches either
// seam; the label and dependency helpers, which that filter does not cover,
// gate on their own inserted/deleted row instead (an idempotent re-add or an
// absent remove never reaches this seam either). Composite mutations —
// ExecuteUpdate's claim + row write + label/parent/persistence patches, and a
// batch create's rows + creation-time edges — run their constituents without
// minting and mint once at the end, so the version carries the final state.
// version_completeness_test.go pins the set of issueops functions that must
// reach this seam and the ones deliberately exempt from it.
//
// Ordinals are LOCAL, not wire addresses. issues.current_revision and
// issue_versions.revision are per-store ordinals, minted as MAX(revision)+1
// inside the writing transaction: two disconnected clones can both hold
// revision 8 for the same issue, each describing a different state. The
// only durable address of a version is version_id (migration 0068 steps
// 1-3, not yet landed). The HTTP API's RowVersion/Revision is a
// compare-and-set token, never an address; Phase 3's read surface returns
// version_id, never the ordinal.
//
// SINGLE WRITER ONLY until the version_id primary-key swap lands. With
// PRIMARY KEY (issue_id, revision), two disconnected writers that each mint
// the same ordinal for the same issue collide on merge, and
// TryAutoResolveMergeConflicts (internal/storage/versioncontrolops/mergesettle.go) fails the
// pull for a table it does not know. Enabling versioned history is therefore
// safe only with a SINGLE writer per store until migration 0068 steps 1-3
// (UUID version_id primary key, ordinal demoted to an index) land; those
// steps follow as their own PR.
//
// "Single writer" means one writer AT A TIME per store, not merely one
// clone. The MAX(revision) + 1 read against PRIMARY KEY (issue_id, revision)
// in recordVersionAtInTx is not a safe allocator for two concurrent
// transactions in one store either, and the label and dependency paths are
// not serialized on the issue row the way the claim/close CAS is; the
// RunDualWrite* contract cases exclude concurrent writers, so no test
// speaks to it. Tracked as gastownhall/beads#6379 (item 4).
//
// MINT COST, deferred deliberately: each mint adds ~6-7 round-trips inside
// the write transaction (the GetIssueInTx snapshot and its label hydration,
// the participation_generation fence read, the dependency load, the
// store_epoch read, the MAX(revision) scan, the version INSERT, the
// current_revision UPDATE), and the MAX(revision) scan grows with an issue's
// history depth -- on paths that previously ran one or two statements, so it
// lengthens write-lock hold time. Harmless while the flag is off, which is
// every build so far. Before any phase enables it, seed the next ordinal from
// the row's current_revision and retire the aggregate scan -- a natural
// pairing with the version_id UUID swap, since that swap is what makes the
// ordinal stop being the key. The snapshot does not read current_revision;
// the fence read can select it beside participation_generation, so the seed
// costs no extra round-trip. Tracked as gastownhall/beads#6379 alongside
// item 4.

// THE MINT'S REFUSAL IS AN INTEGRITY BACKSTOP, NOT AN ADMISSION POLICY, AND IT
// BINDS THE ROWS THAT PARTICIPATE IN HISTORY.
// canonicalDurableState refuses what it cannot record faithfully -- a number
// outside the I-JSON exact-integer range, wherever in the issue it sits (in
// metadata, or in a gate's timeout, which is written as nanoseconds), and
// duplicate keys -- and it does so inside the mutating transaction, so with
// history on a write that introduces such a value to a row that participates
// fails atomically and commits nothing: refusing at the mint IS refusing the
// write, for every store-mediated write to such a row. That is what keeps a
// version's content token meaning what it says. It is not a rule about what an
// issue may hold, which stays open while history is off, and that is the case to
// plan for: a row that participates in history (participation_generation is set),
// written while history was off or by a path that does not mint (such as bd sql,
// an older bd or a pull from a clone that had history off), that already holds
// such a value would fail every later write to it once history is on. `bd config
// set versioned-history.enabled true` therefore checks the store first, with the
// check the mint runs (CheckIssueVersionable, over every issue the mint would
// version), and refuses, writing nothing, while any of them holds a value it would
// refuse.
//
// A row that does not participate is outside all of this. Design §16.2b makes an
// update-shaped write to a legacy record (participation_generation NULL) a skip,
// not a refusal: the fence returns before the snapshot is canonicalized, so no
// version is minted and the gate never runs. Such a write succeeds whatever the
// row's metadata holds, exactly as it would with history off, because no version
// row, and so no content token, comes of it. A create-shaped mint never meets a
// legacy row (its call site has just inserted the row), so it runs the gate
// whenever it mints; a new row flagged no-history is stamped but not minted, so
// nothing is canonicalized for it. So a legacy row blocks no write to itself, and
// the switch's check keeps the rows that participate (ParticipatingIssueIDsInTx)
// and, of the legacy rows, only the templates: `bd mol pour` and `bd mol bond`
// copy a proto's metadata and gate timeouts into every row they create, that
// create-shaped mint runs the gate, and bd update, the in-place fix, refuses a
// template. It warns of the other legacy issues of a proto, which can be fixed in
// place. Where the store cannot say which rows participate, the check keeps legacy
// rows too, an over-approximation that can only refuse more.
//
// That check ran once, at that moment. Nothing keeps a writer that does not mint
// from adding such a row afterwards, even right after the switch is on; if the
// row participates, the first write to it then fails with the same refusal, and
// the same fix applies. Making the check and the write of the setting one
// transaction would narrow that window, not close it. Turning history on through
// the environment or config.yaml does not pass through that command at all, and
// nothing scans when a store is opened either (those planes are read on every open
// of every process); for those planes this refusal is the control on the rows that
// participate, and finding the rows ahead of time is a bd doctor check
// (gastownhall/beads#6379 item 3).
//
// Duplicate keys are the exception to "the case to plan for". A Dolt JSON column
// collapses duplicate keys when it stores a document, so on a Dolt store neither
// the mint nor the scan can ever see one, and the refusal is a defense for a
// backend registered through backends.Lookup that keeps them. It is worth
// checking again whenever Dolt is upgraded.

var versionedHistoryTransactions sync.Map // map[DBTX]bool; entries live for one transaction

// ScopeVersionedHistoryTransaction associates versioned-history activation
// with one concrete transaction and returns a cleanup function. Store
// implementations call it immediately after BeginTx, alongside (not instead
// of) ScopeEventsJournalTransaction. This is instance/project scoped even
// when many stores share a process; there is no process-wide activation
// switch.
func ScopeVersionedHistoryTransaction(tx DBTX, enabled bool) func() {
	if tx == nil {
		return func() {}
	}
	versionedHistoryTransactions.Store(tx, enabled)
	return func() { versionedHistoryTransactions.Delete(tx) }
}

func versionedHistoryEnabled(tx DBTX) bool {
	enabled, _ := versionedHistoryTransactions.Load(tx)
	on, _ := enabled.(bool)
	return on
}

// VersionedHistoryEnabledFor reports whether versioned history is active on
// tx, the transaction ScopeVersionedHistoryTransaction bound. A commit path
// that stages a NAMED set of tables uses it to add
// VersionedHistoryStagedTables only when the transaction could have minted.
func VersionedHistoryEnabledFor(tx DBTX) bool {
	return versionedHistoryEnabled(tx)
}

// VersionedHistoryStagedTables names every table RecordVersionInTx writes, so
// a staging path can add them to whatever it was already going to stage.
//
// THE PLANE DECISION, stated once here because the reviewer of #6675 rightly
// refused to infer it: issue_versions and store_epoch REPLICATE. They are
// ordinary synced Dolt tables, deliberately NOT dolt_ignore'd the way the
// events journal's tables are (migration 0064), because a bead's version
// history is part of the bead and has to travel with it — and because the
// single-writer constraint this package documents at length is a statement
// about what happens when two clones MERGE these tables, which is only
// meaningful for a table that replicates at all.
//
// Replicating means every operation that mints has to STAGE what it minted,
// or the rows sit dirty in the working set: unreplicated, absent from the
// DOLT_COMMIT of the very mutation they describe, swept into whichever later
// unrelated commit stages broadly, and able to trip DirtyTablesError on the
// next migration. That is the same-transaction durability promise this seam
// exists to keep, so staging is not an optimization here.
//
// `issues` is in the list because the mint's LAST write is
// `UPDATE issues SET current_revision`. On issue-plane mutations that table is
// already staged; on the dependency paths it is staged only when the edge
// flipped a durable is_blocked (DependencyWriteResult, GH#6647), so a
// non-flipping dependency write that minted still needs it from here. Staging
// `issues` there is gated on versioned history being active (see the
// store-side callers).
func VersionedHistoryStagedTables() []string {
	return []string{"issue_versions", "store_epoch", "issues"}
}

// issue_versions.attribution_status is a NOT NULL column (migration 0068
// step 6) whose vocabulary aligns to BDP's carried-attribution status
// (gastownhall/bdp#18, merged 2026-09-07): status ∈ {claimed, unknown}. A
// status is an assertion about the actor the mutation arrived with —
// "claimed" when one was supplied, "unknown" when the mutation path had none
// — and is derived from the same actor string every RecordVersionInTx call
// site already passes (no call site carries any other attribution signal).
//
// "imported" is deliberately NOT a status: it is provenance (where a row
// came from), not an assertion about who performed the mutation. It returns
// as a separate provenance marker in the phase that first imports history;
// no writer for it exists yet — Phase 2 is this table's sole writer, and
// every row it mints originated as an accepted mutation.
const (
	attributionStatusClaimed = "claimed"
	attributionStatusUnknown = "unknown"
)

// attributionStatusForActor derives issue_versions.attribution_status from
// the same actor string every RecordVersionInTx call site already passes: a
// non-empty actor is "claimed", an empty one is "unknown". There is no
// third value — an empty actor means the path had no identity to assert,
// which is exactly what "unknown" says; any stronger reading (a deliberate,
// confirmed absence of attribution) is a claim no call site makes.
func attributionStatusForActor(actor string) string {
	if actor == "" {
		return attributionStatusUnknown
	}
	return attributionStatusClaimed
}

// canonicalDurableState renders issue as the bytes RecordVersionInTx stores
// in issue_versions.durable_state: encoding/json's marshal of the issue,
// canonicalized per RFC 8785 (JCS). The canonical form is a function of the
// issue's content alone -- keys sorted, numbers in their one ES6 form
// (1.0 is 1, 1e300 is 1e+300), only the escapes RFC 8785 requires -- so the
// same issue state always yields the same bytes, whatever encoding/json's
// formatting happens to be. Numbers are canonicalized as IEEE-754 doubles
// (RFC 8785 section 3.2.2.3), so canonicalization alone would round a number
// past 2^53 here, once, by the writer, before anything hashes it; the admission
// gate (refuseUnrepresentableIntegers) refuses such a number instead, so the
// stored bytes and the hashed bytes are the same bytes and nothing is rounded
// silently. Two fields of types.Issue can reach that magnitude: Metadata, and
// Timeout, a time.Duration that encoding/json writes as a count of nanoseconds,
// so a gate timeout above 2^53-1 ns (about 104.25 days) is refused. Its other
// integers are priorities, ordinals and counts held in 32-bit columns.
//
// A refusal names the top-level field it was found in (nameRefusedField). The
// success path reads the document once; the field is located only after a
// refusal.
//
// Two properties worth stating because a hand-rolled canonicalizer would get
// them wrong. First, jcs.Transform normalizes away encoding/json's HTML
// escaping of <, > and &, so the token is a function of content, not of
// Go's escaping policy. Second, a snapshot that cannot be canonicalized
// deliberately FAILS the mutation: types.Issue.Metadata is a json.RawMessage
// passed through verbatim, RFC 8785 rejects duplicate keys, and this seam
// returns the error to its caller, which aborts the transaction. A backend that
// keeps duplicate keys in a metadata document would therefore let an issue
// mutate with the flag off and make it unmutatable with it on, if it participates
// in history; a legacy row stays writable, because the fence skips it before this
// function runs. Dolt does not keep them (see the note above), so on a Dolt
// store this refusal is never reached. Fail-closed is the policy for a backend
// that does -- bytes that could canonicalize two ways would not be a content
// token -- and whether to normalize such metadata first, or to find such rows with
// bd doctor, is gastownhall/beads#6379 (item 3).
func canonicalDurableState(issue any) ([]byte, error) {
	marshaled, err := json.Marshal(issue)
	if err != nil {
		return nil, err
	}
	if err := refuseUnrepresentableIntegers(marshaled); err != nil {
		return nil, nameRefusedField(marshaled, err)
	}
	canonical, err := jcsCanonicalize(marshaled)
	if err != nil {
		return nil, nameRefusedField(marshaled, err)
	}
	return canonical, nil
}

// refusedFieldError is a refusal from canonicalDurableState that has been traced
// to the top-level field of the issue it was found in. It unwraps to the refusal
// itself, so errors.Is(err, ErrIntegerNotRepresentable) still answers.
type refusedFieldError struct {
	field string
	err   error
}

func (e *refusedFieldError) Error() string { return fmt.Sprintf("%v (field %q)", e.err, e.field) }

func (e *refusedFieldError) Unwrap() error { return e.err }

// nameRefusedField finds the top-level field of marshaled, one issue as a JSON
// object, that the two checks canonicalDurableState runs refuse on their own, and
// returns err naming it. It runs only after a refusal, so what a successful
// canonicalization costs does not change. A document it cannot attribute, one
// that is not an object or in which no single field is refused alone, comes back
// unchanged.
func nameRefusedField(marshaled []byte, err error) error {
	dec := json.NewDecoder(bytes.NewReader(marshaled))
	if open, derr := dec.Token(); derr != nil || open != json.Delim('{') {
		return err
	}
	for dec.More() {
		keyToken, derr := dec.Token()
		key, isString := keyToken.(string)
		if derr != nil || !isString {
			return err
		}
		var value json.RawMessage
		if derr := dec.Decode(&value); derr != nil {
			return err
		}
		if refuseUnrepresentableIntegers(value) != nil {
			return &refusedFieldError{field: key, err: err}
		}
		if _, jerr := jcsCanonicalize(value); jerr != nil {
			return &refusedFieldError{field: key, err: err}
		}
	}
	return err
}

// jcsCanonicalize is canonicalDurableState's RFC 8785 (JCS) step alone, below
// the admission gate rather than behind it.
//
// IT MUST EXIST SEPARATELY because refuseUnrepresentableIntegers and RFC 8785
// disagree on purpose, not by oversight: RFC 8785 section 3.2.2.3 canonicalizes
// ANY number as a double, whatever its magnitude, while the admission gate
// refuses any number whose nearest double is past 2^53-1 outright (see
// refuseUnrepresentableIntegers's doc comment on why that rule has no
// notation carve-out). One of the RFC 8785 vectors this store pins conformance
// against, testdata/jcs/input/values.json, both ships from the spec's own
// reference suite AND contains 1E30 -- a value the gate refuses and RFC 8785
// requires canonicalizing to 1e+30. Routing that vector through
// canonicalDurableState would force a choice between weakening spec-conformance
// coverage and putting a magnitude exception in the gate, and neither is
// right: the gate's refusal is a policy decision about what this store admits,
// not a claim about what JCS can canonicalize. jcsCanonicalize lets a caller
// ask the second question without the first, so TestCanonicalDurableStateIsJCS
// can hold the RFC 8785 vectors to the spec's own bytes -- including
// values.json -- while refuseUnrepresentableIntegers's refusal of the same
// magnitude is pinned separately, in the admission tests
// (version_history_integer_admission_test.go), where it belongs.
func jcsCanonicalize(marshaled []byte) ([]byte, error) {
	canonical, err := jcs.Transform(marshaled)
	if err != nil {
		return nil, fmt.Errorf("canonicalize (RFC 8785): %w", err)
	}
	return canonical, nil
}

// ErrIntegerNotRepresentable is the refusal for a JSON number outside the I-JSON
// exact-integer range: one whose nearest binary64 has magnitude above 2^53-1, or
// that overflows binary64.
//
// The name is historical. It began as the refusal for an integer literal binary64
// cannot hold exactly, and it now also covers a literal spelled as a fraction whose
// nearest double lies at or beyond 2^53, which RFC 8785 would round to an integer
// just the same. It is kept rather than renamed so that nothing matching it with
// errors.Is has to change.
var ErrIntegerNotRepresentable = errors.New("number is outside the I-JSON exact-integer range (magnitude above 2^53-1)")

// maxExactJSONInteger is 2^53-1, the largest magnitude in the I-JSON
// interoperable integer range (RFC 7493 section 2.2), and BDP's admission law
// (gastownhall/bdp#21) makes a literal past it invalid rather than merely lossy.
// binary64 does represent 2^53 exactly, but 2^53 and 2^53+1 round to the same
// double, so from 2^53 up a literal can no longer be told from its neighbor.
const maxExactJSONInteger = 1<<53 - 1

// refuseUnrepresentableIntegers rejects marshaled if any JSON number literal in
// it is outside the I-JSON exact-integer range: the binary64 nearest to the
// literal has magnitude above 2^53-1, or the literal overflows binary64. Which of
// JSON's number forms spells the value -- bare integer, decimal or exponential --
// does not matter.
//
// WHY REFUSE RATHER THAN ROUND. jcs.Transform canonicalizes numbers as
// IEEE-754 doubles (RFC 8785 section 3.2.2.3), so 9007199254740993 becomes
// 9007199254740992 on the way in. That is not a formatting difference, it is
// a DIFFERENT VALUE, and it collapses two distinct issue states onto one
// durable_state and therefore one content token -- the token can no longer
// tell them apart, and a reader comparing tokens concludes the second write
// was a no-op. Rounding is silent; refusing is not.
//
// This seam is the admission gate, one layer above RecordVersionInTx, which
// is where BDP puts it: an authority adapting an existing store "MUST map or
// refuse values outside this contract before it serves them as BDP
// Resources" (gastownhall/bdp#20 section 5.1).
//
// THE NEAREST DOUBLE, NOT THE LITERAL'S SPELLING. Each literal is classified
// with strconv.ParseFloat, which returns the correctly rounded binary64 in time
// linear in the length of the literal, and that is exactly the value RFC 8785
// canonicalizes the literal to. So "the nearest double is past the range" and
// "canonicalization would move this literal past the range" are one question,
// and the gate is closed under its own canonicalization: whatever it admits
// canonicalizes to a form it also admits. A rule keyed to integer VALUES was
// not: 9007199254740991.5 is not an integer, so it was admitted, yet it rounds
// to 2^53 and its canonical bytes were then refused. Here 9007199254740993,
// 9007199254740993.0 and 9.007199254740993e15 are refused alike, and so is
// 9007199254740993.5, whose nearest double is 2^53+2. An overflow
// (strconv.ErrRange) is a refusal, not a syntax error; a magnitude too small for
// binary64 rounds to zero and is admitted.
//
// NOTHING IS BUILT PER EXPONENT. ParseFloat never constructs the number, so a
// literal such as 1e1000000 is an overflow refused in the time it takes to read
// it, not a million-digit integer built first and then compared. The gate runs
// inside the writer's transaction, so a classifier whose cost grew with an
// exponent would be a way to hold that transaction open with a few
// exponent-heavy metadata values.
//
// SCOPE. Ordinary fractions are admitted: 0.1 is canonicalized to its nearest
// double, which is how the store holds it. Whether two literals that share a
// nearest double can be told apart inside the store is a property of the store,
// not of this gate, and it is the one thing a Dolt upgrade could change: the
// store-level tests on each leg assert that no two literals the store reads back
// as different numbers are both admitted with one canonical form, and log what
// each Dolt does to a number, rather than assuming it. An integer past 2^53 is
// refused whatever the store does to it: a store may keep it exactly, where RFC
// 8785 would round it, and a store that already rounds it hands the gate an
// integer-valued double, which the rule refuses just the same. A JSON null is a
// token, not a number: only json.Number tokens are classified. Of types.Issue's
// own fields only Metadata (a json.RawMessage passed through verbatim) and
// Timeout (a time.Duration, written as nanoseconds) can reach this bound; its
// other integers are priorities, ordinals and counts held in 32-bit columns.
func refuseUnrepresentableIntegers(marshaled []byte) error {
	dec := json.NewDecoder(bytes.NewReader(marshaled))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("scan for unrepresentable integers: %w", err)
		}
		num, ok := tok.(json.Number)
		if !ok {
			continue
		}
		lit := num.String()
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return fmt.Errorf("scan for unrepresentable integers: invalid JSON number literal %q: %w", lit, err)
		}
		if math.IsInf(f, 0) || math.Abs(f) > maxExactJSONInteger {
			return fmt.Errorf("%w: %s", ErrIntegerNotRepresentable, lit)
		}
	}
}

// CheckIssueVersionable reports whether issue could be minted into a version's
// durable_state. It is canonicalDurableState's own refusal -- the admission gate,
// then RFC 8785 canonicalization, over the whole marshaled issue -- so it refuses
// a number outside the I-JSON exact-integer range (ErrIntegerNotRepresentable)
// wherever in the issue it sits, in metadata or in a gate's timeout, and it also
// refuses duplicate keys, which RFC 8785 cannot canonicalize. The refusal names
// the top-level field it found the value in.
//
// It exists so that the check made when versioned history is switched on and
// the check the mint makes cannot disagree: FindUnversionable runs this over
// every issue the mint would version, and a store that passes it cannot then
// refuse the first write to a row it cleared. It is the one such check. A check
// over only part of an issue, its metadata say, can pass what the mint refuses (a
// gate's timeout can be past the range while its metadata is fine), so none may
// be exported beside it.
//
// The issue it is handed need not be the one the mint sees. The scan reads a
// lite projection, with no heavy text columns, no labels and no dependencies,
// and that is sound only because none of what it leaves out can hold a number.
// The tests in version_history_preenable_scan_test.go pin that.
func CheckIssueVersionable(issue *types.Issue) error {
	_, err := canonicalDurableState(issue)
	return err
}

// RecordVersionInTx mints one issue_versions row for issueID and advances
// issues.current_revision to match, as of tx (read-your-writes within the
// same transaction). A no-op when versioned history is disabled for tx, or
// when issueID resolves to a wisp -- a row the wisps table holds, whatever
// its flags say: wisps carry current_revision for shape parity only and are
// never versioned this phase (design FR-8). IsWisp is
// Ephemeral || NoHistory, so a promoted no-history bead -- a durable
// issues-plane row with NoHistory=true -- is also never versioned; that is
// what no-history means, not an FR-8 wisp rule, and the write-path doc's
// row 13 says so.
//
// The revision it mints is a local ordinal, not an address — see the
// package comment above on ordinals versus version_id, and on the
// single-writer constraint that holds until version_id lands.
//
// actor is the acting identity that performed the mutation, recorded as the
// version row's attribution — "" when the mutation path genuinely has none,
// matching RecordEventInTx's own convention. It also drives
// attribution_status via attributionStatusForActor.
//
// This is the UPDATE-shaped entry point: design §16.2b's write fence. A
// record whose participation_generation is NULL is legacy — never
// positively promoted — and an update-shaped mutation against it mints
// nothing, neither half of this seam's write (no issue_versions row, no
// current_revision bump). A record that already carries a non-NULL
// participation_generation proceeds normally. Use RecordVersionForCreateInTx
// for a create-shaped mutation instead: a brand-new row has no legacy state
// to preserve, so that entry point stamps the column rather than checking
// it.
//
// The skip is whole: nothing about a legacy record is read, canonicalized or
// admitted, so such a write is neither versioned nor refused (design §16.2b: a
// skip, not a refusal).
func RecordVersionInTx(ctx context.Context, tx DBTX, issueID, actor string) error {
	if !versionedHistoryEnabled(tx) {
		return nil
	}
	return recordVersionAtInTx(ctx, tx, issueID, actor, time.Now().UTC(), mintUpdate)
}

// RecordVersionForCreateInTx is RecordVersionInTx's create-shaped sibling
// (design §16.2b). The write fence does not apply to a brand-new row, so
// instead of checking participation_generation, this stamps it — sourced
// from store_epoch.epoch, the same source the fence reads — as part of the
// row's first mint, whenever versioned history is enabled. A create with the
// flag off never reaches the stamp (this whole seam no-ops while the flag is
// off, same as RecordVersionInTx), leaving the column NULL — indistinguishable
// from a true legacy row (FR-7). A wisp arriving on the issues plane, by
// promotion or a persistence move out of the wisps table, mints through here
// too when it carries no participation: its issues row is just as new, and a
// row that began as a wisp never declares any (FR-8). A row a demotion took
// off the issues plane while history was on is different: it carries the
// participation it had (carryParticipationInTx), so its arrival restores that
// and mints update-shaped, and a legacy row stays legacy (R2.3). A new
// issues-table row that carries NoHistory (or a
// stray Ephemeral) is stamped but not versioned: no-history keeps it out of
// history for now, but it was created with the flag on, so it is not
// legacy once that flag is repaired. A row the wisps table holds mints and
// stamps nothing, whatever its flags say: it has no issues row to stamp, and
// a wisp is never versioned (FR-8). That covers an ordinary wisp create and
// an unflagged wisps-table row (import's "wisp_plane" marker) alike.
func RecordVersionForCreateInTx(ctx context.Context, tx DBTX, issueID, actor string) error {
	if !versionedHistoryEnabled(tx) {
		return nil
	}
	return recordVersionAtInTx(ctx, tx, issueID, actor, time.Now().UTC(), mintCreate)
}

// RecordVersionAtInTx is RecordVersionInTx's test-support twin: it mints a
// version row at a caller-chosen change_at instead of time.Now(), and --
// deliberately -- does not gate on versionedHistoryEnabled at all, since its
// whole purpose is controlled-timestamp minting for conformance fixtures
// (R7.1's AsOfReadFixture.MintAt, backend/conformance/versioned_read_contract.go)
// regardless of a store's activation state. Production code never calls
// this; only per-leg as-of-read fixtures do, so their bare issue-create step
// can stay history-off (no unwanted real-time row) while still minting the
// exact versions a test needs at the instants it needs them.
//
// It bypasses design §16.2b's write fence exactly as it bypasses the gate:
// it ignores participation_generation and never stamps it, so it mints for
// a legacy row and a participating row alike and leaves the column as it
// found it. The fence and the stamp belong to the production entry points
// above, never to a fixture's controlled-timestamp mint. The plane rule is
// not the fence, and it does apply: a row the wisps table holds, or one
// flagged Ephemeral or NoHistory, mints nothing here either.
func RecordVersionAtInTx(ctx context.Context, tx DBTX, issueID, actor string, at time.Time) error {
	return recordVersionAtInTx(ctx, tx, issueID, actor, at, mintUnfenced)
}

// versionMintKind says how recordVersionAtInTx treats design §16.2b's write
// fence, the one thing RecordVersionInTx, RecordVersionForCreateInTx and
// RecordVersionAtInTx do not share.
type versionMintKind int

const (
	// mintUpdate is an update-shaped mutation (RecordVersionInTx): the fence
	// applies, and a record whose participation_generation is NULL mints nothing.
	mintUpdate versionMintKind = iota
	// mintCreate is a create-shaped mutation (RecordVersionForCreateInTx): a
	// brand-new row has no legacy state to preserve, so the fence does not
	// apply and the mint stamps participation_generation instead.
	mintCreate
	// mintUnfenced is RecordVersionAtInTx's test-support mint: no fence and no
	// stamp, exactly as it behaved before the fence existed. Like the other
	// two, it mints nothing for a row that is not on the issues plane.
	mintUnfenced
)

// recordVersionAtInTx is RecordVersionInTx, RecordVersionForCreateInTx and
// RecordVersionAtInTx's shared body, taking at where the callers differ:
// time.Now().UTC() for the production gate-checked paths, a caller-chosen
// instant for test minting; and kind, which says whether design §16.2b's
// write fence applies (mintUpdate), the mint stamps participation_generation
// (mintCreate) or neither (mintUnfenced). The no-op rules documented on
// RecordVersionInTx above (wisps) apply here too, since this is that
// function's entire mechanism minus the gate.
func recordVersionAtInTx(ctx context.Context, tx DBTX, issueID, actor string, at time.Time, kind versionMintKind) error {
	// change_at is DATETIME(6) as of migration 0069, and this function
	// deliberately does NOT floor `at` to the second.
	//
	// It used to. While 0067's plain DATETIME (precision 0) was in force that
	// floor was load-bearing: Dolt's datetime(0) does not truncate sub-second
	// input, it ROUNDS half-up, so a change_at of 13:32:04.77 stored as
	// 13:32:05 -- strictly LATER than the instant it recorded. That breaks
	// AsOfReadInTx's "latest version accepted at or before T"
	// (resolveAsOfRevisionInTx, asof_read.go) for any T between the true
	// instant and the rounded-up one. Flooring first left nothing to round.
	//
	// 0069 widened change_at and removed_at to DATETIME(6). That moves the
	// rounding below the microsecond rather than removing it: GMS still rounds
	// DATETIME(6) input to the microsecond (.1234567 is stored as .123457).
	// The remainder is harmless to that lookup (resolveAsOfRevisionInTx),
	// because the instant it compares against is rounded the same way, so a
	// stored value still compares equal to the instant it was minted at. What the widening does
	// make is the floor actively harmful: it threw away the microseconds the
	// widening exists to keep. Two versions minted in the same second
	// collapsed onto one stored value and became indistinguishable
	// by change_at -- which defeats --at, the only PORTABLE selector the read
	// surface offers while revision stays a local ordinal and no durable
	// version address has shipped. Measured before this change on a store
	// carrying both: four versions minted in one second all read back
	// 15:40:38.000000.
	//
	// So the column's precision is now relied on rather than worked around.
	// Pinned by TestRecordVersionKeepsSubSecondChangeAt below; the column's
	// own half of the contract is pinned by
	// TestMigration0069ChangeAtSurvivesSubSecondPrecisionThroughDoltCLI in
	// internal/storage/schema. MarkLatestVersionRemovedInTx (asof_read.go)
	// needs no matching change -- it already passes time.Now().UTC() straight
	// through, so removed_at was never floored.

	issue, err := GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		return fmt.Errorf("versioned history: snapshot %s: %w", issueID, err)
	}
	// A flagged row (Ephemeral || NoHistory) is never versioned, so an
	// update-shaped call, or the test-support mint, has nothing to do. A
	// create-shaped one may still have to declare participation; see the
	// IsWisp branch below.
	if IsWisp(issue) && kind != mintCreate {
		return nil
	}

	// THE TABLE THAT HOLDS THE ROW decides its plane, not its flags. IsWisp
	// reads Ephemeral || NoHistory, and a wisps-table row can carry neither:
	// bd mol squash clears a wisp root's flag in place, and import's
	// "wisp_plane" marker routes an unflagged row to wisps. Such a row passes
	// IsWisp, but there is no issues row for it, so this read finds nothing.
	// No issues row means nothing to version (FR-8), for an update- or a
	// create-shaped call alike, and for the test-support mint too, since this
	// is the plane rule and not the fence: mint nothing and let the mutation
	// proceed, rather than abort it on sql.ErrNoRows. GetIssueInTx resolved
	// the snapshot above, so the row exists; ErrNoRows here can only mean it
	// is in the wisps table.
	//
	// The same read is design §16.2b's write fence. participation_generation
	// is read straight off the row rather than through types.Issue: it is
	// dual-write bookkeeping, not part of the issue's own content model, so it
	// stays out of the durable_state snapshot canonicalDurableState marshals
	// below. Only an update-shaped call (mintUpdate) acts on its value: a
	// brand-new row (mintCreate) has no legacy state to preserve, and this
	// same function stamps the column itself further down; the test-support
	// mint (mintUnfenced) ignores it. An update-shaped call against a NULL
	// (legacy) value returns here, before either half of this seam's write
	// runs. The fence runs before the admission gate (canonicalDurableState)
	// on purpose, so a legacy record is skipped whole: nothing about it is
	// canonicalized, admitted or refused.
	// BD_IGNORE_SCHEMA_SKEW (internal/storage/schema/schema.go) has no
	// bearing on this check: it downgrades CheckForwardDrift's
	// migration-cursor refusal, a different plane than this column's real
	// per-row data.
	var participationGeneration sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT participation_generation FROM issues WHERE id = ?", issueID,
	).Scan(&participationGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("versioned history: read participation_generation for %s: %w", issueID, err)
	}
	if IsWisp(issue) {
		// Only a create-shaped call gets here: an issues-table row that
		// carries NoHistory (or a stray Ephemeral). It is not versioned while
		// its flags say so, so this mints nothing. But it is brand new on the
		// issues plane with the flag on, so it is not legacy, and the create
		// still declares its participation. Import keeps a promoted
		// no-history row on the issues plane exactly this way (bd-r9uce).
		// When bd update --persistent later repairs the flag in place, that
		// repair mints update-shaped: through the guarded update's one
		// end-of-update mint (executeUpdate) on dolt and embeddeddolt, or
		// MoveIssuePersistenceInTx's own on the unit-of-work path. Left NULL
		// here, the fence would read the row as legacy for good. Same source
		// and same updated_at rule as the stamp at the end of this function.
		epoch, err := readStoreEpochInTx(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE issues SET participation_generation = ?, updated_at = updated_at WHERE id = ?",
			epoch, issueID,
		); err != nil {
			return fmt.Errorf("versioned history: stamp participation_generation for %s: %w", issueID, err)
		}
		return nil
	}
	if kind == mintUpdate && !participationGeneration.Valid {
		return nil
	}

	// durable_state is the RFC 8785 (JCS) canonical form of the marshaled
	// issue, stored as bytes -- issue_versions.durable_state is a LONGBLOB
	// (migration 0068 step 7), not a JSON column. The invariant that buys,
	// the one donnabox asked for on #6358 item 4: sha256(stored bytes) is a
	// stable content token, because the bytes read back are exactly the
	// bytes the writer produced. A Dolt JSON column could not provide that.
	// It parses and renormalizes what it stores (measured on dolt 2.2.3:
	// 1.0 reads back as 1, 9007199254740993 as 9007199254740992, 1e300 as
	// 1e+300, while a LONGBLOB returns the same bytes) -- so the design's
	// verbatim-bytes promise (R5.1) and any content-derived token (#5898's
	// sha256-jcs) broke between the writer and the disk. JCS pins the bytes
	// on the way in (canonicalDurableState); LONGBLOB keeps them as written.
	//
	// The snapshot itself is the mutated issue (design §15.3, corrected by
	// §17.1): the write-path loader GetIssueInTx never hydrates Dependencies
	// (types.Issue.Dependencies is omitempty and unrelated to this snapshot's
	// own read), so it is populated here, once, for every caller of this
	// seam — in GetDependencyRecordsForIssuesInTx's own ordering (issue_id,
	// depends_on_id, type, id).
	deps, err := GetDependencyRecordsForIssuesInTx(ctx, tx, []string{issueID})
	if err != nil {
		return fmt.Errorf("versioned history: load dependencies for %s: %w", issueID, err)
	}
	issue.Dependencies = deps[issueID]

	epoch, err := readStoreEpochInTx(ctx, tx)
	if err != nil {
		return err
	}

	// The scalar-subquery form is deliberate. On Dolt 2.4.0 through 2.4.2,
	// go-mysql-server's aggregate-over-index rewrite
	// (dolthub/go-mysql-server#3907) returns zero rows, not one NULL row, for an
	// ungrouped MAX over an empty primary-key prefix (dolthub/dolt#12093, fixed
	// in 2.5.0), so "SELECT COALESCE(MAX(revision), 0) + 1 FROM issue_versions
	// WHERE issue_id = ?" fails every first mint there. Embedded mode links a
	// fixed engine, but server mode runs whatever dolt sql-server the user has.
	// The outer SELECT has no FROM and yields exactly one row on every engine.
	// Both forms pass the embedded tests on a fixed engine, so the sqlmock pins
	// in version_history_preenable_scan_test.go are what hold this text.
	var newRevision int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE((SELECT MAX(revision) FROM issue_versions WHERE issue_id = ?), 0) + 1", issueID,
	).Scan(&newRevision); err != nil {
		return fmt.Errorf("versioned history: compute next revision for %s: %w", issueID, err)
	}

	durableState, err := canonicalDurableState(issue)
	if err != nil {
		return fmt.Errorf("versioned history: marshal durable state for %s: %w", issueID, err)
	}

	// durableState is bound as []byte, a LONGBLOB parameter -- never
	// string(durableState), which would ask the driver to treat it as text.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO issue_versions
			(issue_id, revision, epoch, durable_state, change_actor, change_agent, change_message, change_at, attribution_status)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		issueID, newRevision, epoch, durableState, actor, at, attributionStatusForActor(actor),
	); err != nil {
		return fmt.Errorf("versioned history: insert version row for %s: %w", issueID, err)
	}

	if kind == mintCreate {
		// Stamps participation_generation in the same statement that advances
		// current_revision — design §16.2b's "positive declaration sourced
		// from store_epoch.epoch," minted here from the epoch already read
		// above for this same transaction's version row. Setting updated_at to
		// itself keeps the column's ON UPDATE CURRENT_TIMESTAMP clause from
		// replacing the timestamp the row was created with, which an import
		// supplies.
		if _, err := tx.ExecContext(ctx,
			"UPDATE issues SET current_revision = ?, participation_generation = ?, updated_at = updated_at WHERE id = ?",
			newRevision, epoch, issueID,
		); err != nil {
			return fmt.Errorf("versioned history: advance current_revision for %s: %w", issueID, err)
		}
		return nil
	}
	// The same updated_at rule for every update-shaped mint. Advancing
	// current_revision is bookkeeping, not an edit, but on its own it fires
	// the ON UPDATE CURRENT_TIMESTAMP clause, so a label or dependency write
	// that never touches updated_at would move it to the wall clock only
	// while history is on, and an import's stale-upsert check would then
	// reject the source's next edit. History on must leave updated_at
	// exactly where history off does (the same reason #4298 and
	// blocked_state.go's mark/unmark templates assign it to itself).
	if _, err := tx.ExecContext(ctx,
		"UPDATE issues SET current_revision = ?, updated_at = updated_at WHERE id = ?", newRevision, issueID,
	); err != nil {
		return fmt.Errorf("versioned history: advance current_revision for %s: %w", issueID, err)
	}
	return nil
}

// readStoreEpochInTx reads store_epoch.epoch as of tx, the source of both a
// version row's epoch and a participation_generation stamp.
//
// store_epoch is one shared row (id = 1) that every minting transaction
// reads. Read first and seed only when the row is absent, so the seed
// INSERT happens once per store rather than once per mint: an INSERT IGNORE
// on every mint put that shared row into every write transaction's
// footprint for nothing.
func readStoreEpochInTx(ctx context.Context, tx DBTX) (int, error) {
	var epoch int
	err := tx.QueryRowContext(ctx, "SELECT epoch FROM store_epoch WHERE id = 1").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		if _, seedErr := tx.ExecContext(ctx, "INSERT IGNORE INTO store_epoch (id, epoch) VALUES (1, 1)"); seedErr != nil {
			return 0, fmt.Errorf("versioned history: seed store epoch: %w", seedErr)
		}
		err = tx.QueryRowContext(ctx, "SELECT epoch FROM store_epoch WHERE id = 1").Scan(&epoch)
	}
	if err != nil {
		return 0, fmt.Errorf("versioned history: read store epoch: %w", err)
	}
	return epoch, nil
}

// A PLANE ROUND TRIP KEEPS THE ROW'S PARTICIPATION (design §16.2b, R2.3).
//
// The fence reads participation off the issues row, and a plane move replaces
// that row: a demotion deletes it, and the move back out inserts a new one,
// which a wisp never declared anything for. Read off the new row alone, every
// arrival looks brand new, so a legacy row made a wisp and then persistent
// again would mint create-shaped and start being versioned through two
// ordinary writes, which R2.3 forbids. So the demotion records the row's
// participation in wisps.participation_generation (the column 0070 and
// ignored/0028 added there for shape parity) and the arrival restores it:
//
//   - NULL: nothing was carried. The row began as a wisp, or it became one
//     while history was off. Its arrival mints create-shaped and begins its
//     history, as before.
//   - carriedLegacyParticipation (0): the issues row was legacy. The arrival
//     leaves the new issues row's column NULL, so its update-shaped mint is
//     fenced and the row stays legacy.
//   - a generation G (always >= 1): the issues row participated. The arrival
//     writes G back, and its update-shaped mint continues the row's history.
//
// Both halves run only while versioned history is on for tx, like the seam
// itself: with history off nothing here reads or writes either column (FR-7,
// pinned by the historyoffequiv differential). So a move made with history off
// carries nothing: a row demoted then comes back as a wisp does, and a row
// that arrives then is legacy, as a create made then is.
//
// The writes keep updated_at (the column's ON UPDATE CURRENT_TIMESTAMP would
// otherwise move it) and leave row_lock alone: the plane move inserted the
// row with a fresh row_lock in this transaction, so reminting would only
// double-bump it.

// carriedLegacyParticipation is the value a demotion records in
// wisps.participation_generation for a legacy row. store_epoch.epoch is
// seeded at 1 and only grows (readStoreEpochInTx, BumpEpochInTx), so no
// declared generation is 0, and NULL keeps meaning "nothing carried".
const carriedLegacyParticipation = 0

// CarryParticipationToWispInTx is the demotion half for a plane move outside
// this package (the dolt leg's DemoteToWisp). Call it after the wisps row for
// id is inserted and before its issues row is deleted.
func CarryParticipationToWispInTx(ctx context.Context, tx DBTX, id string) error {
	_, err := carryParticipationInTx(ctx, tx, id, true)
	return err
}

// carryParticipationInTx carries id's participation across a plane move, in
// the window where both of its rows exist: into the wisps row on a demotion
// (toWisps), back onto the new issues row on an arrival. For an arrival it
// reports whether the wisps row carried anything; a caller that mints for the
// arrival mints create-shaped only when it did not.
func carryParticipationInTx(ctx context.Context, tx DBTX, id string, toWisps bool) (restored bool, err error) {
	if !versionedHistoryEnabled(tx) {
		return false, nil
	}
	if toWisps {
		var generation sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			"SELECT participation_generation FROM issues WHERE id = ?", id,
		).Scan(&generation); err != nil {
			return false, fmt.Errorf("versioned history: read participation_generation of demoted %s: %w", id, err)
		}
		carried := int64(carriedLegacyParticipation)
		if generation.Valid {
			carried = generation.Int64
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE wisps SET participation_generation = ?, updated_at = updated_at WHERE id = ?", carried, id,
		); err != nil {
			return false, fmt.Errorf("versioned history: carry participation_generation of demoted %s: %w", id, err)
		}
		return false, nil
	}
	var carried sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT participation_generation FROM wisps WHERE id = ?", id,
	).Scan(&carried); err != nil {
		return false, fmt.Errorf("versioned history: read carried participation_generation of %s: %w", id, err)
	}
	if !carried.Valid {
		return false, nil
	}
	if carried.Int64 > carriedLegacyParticipation {
		if _, err := tx.ExecContext(ctx,
			"UPDATE issues SET participation_generation = ?, updated_at = updated_at WHERE id = ?", carried.Int64, id,
		); err != nil {
			return false, fmt.Errorf("versioned history: restore participation_generation of %s: %w", id, err)
		}
	}
	return true, nil
}
