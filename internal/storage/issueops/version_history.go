package issueops

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"time"

	"github.com/gowebpki/jcs"
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
// RecordVersionInTx is the single seam both direct-SQL legs (dolt,
// embeddeddolt) and the domain/db package (used by uow) call through, from
// inside the same already-short-circuited functions that call
// RecordEventInTx — every accepted mutation of an issue's durable state
// (create, update, close, reopen, claim, release, lease reclaim, defer wake,
// label add/remove, dependency add/remove, promote, persistence move) mints
// exactly one row, as its LAST durable-state write; a no-op mints none. A
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
// clone. SELECT COALESCE(MAX(revision), 0) + 1 against PRIMARY KEY
// (issue_id, revision) is not a safe allocator for two concurrent
// transactions in one store either, and the label and dependency paths are
// not serialized on the issue row the way the claim/close CAS is; the
// RunDualWrite* contract cases exclude concurrent writers, so no test
// speaks to it. Tracked as gastownhall/beads#6379 (item 4).

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
// (RFC 8785 section 3.2.2.3), so an integer past 2^53 is rounded here, once,
// by the writer, before anything hashes it: the stored bytes and the hashed
// bytes are the same bytes. types.Issue carries no such magnitudes (its
// integers are ordinals and priorities), but the rule is stated so nobody
// expects int64 fidelity from the token.
//
// Two properties worth stating because a hand-rolled canonicalizer would get
// them wrong. First, jcs.Transform normalizes away encoding/json's HTML
// escaping of <, > and &, so the token is a function of content, not of
// Go's escaping policy. Second, a snapshot that cannot be canonicalized
// deliberately FAILS the mutation: types.Issue.Metadata is a json.RawMessage
// passed through verbatim, RFC 8785 rejects duplicate keys, and this seam
// returns the error to its caller, which aborts the transaction. An issue
// whose metadata column already holds duplicate keys (reachable through bd
// sql or a hand-written JSONL import) therefore mutates fine with the flag
// off and becomes unmutatable with it on. Fail-closed is the policy for now
// -- bytes that could canonicalize two ways would not be a content token --
// and whether to normalize such metadata first, or to find such rows with
// bd doctor, is gastownhall/beads#6379 (item 3).
func canonicalDurableState(issue any) ([]byte, error) {
	marshaled, err := json.Marshal(issue)
	if err != nil {
		return nil, err
	}
	if err := refuseUnrepresentableIntegers(marshaled); err != nil {
		return nil, err
	}
	return jcsCanonicalize(marshaled)
}

// jcsCanonicalize is canonicalDurableState's RFC 8785 (JCS) step alone, below
// the admission gate rather than behind it.
//
// IT MUST EXIST SEPARATELY because refuseUnrepresentableIntegers and RFC 8785
// disagree on purpose, not by oversight: RFC 8785 section 3.2.2.3 canonicalizes
// ANY number as a double, whatever its magnitude, while the admission gate
// refuses an integer-valued literal past 2^53-1 outright (see
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

// ErrIntegerNotRepresentable reports a JSON integer literal that binary64
// cannot hold exactly, which RFC 8785 would silently round.
var ErrIntegerNotRepresentable = errors.New("integer literal is not exactly representable as an IEEE 754 binary64 value")

// maxExactJSONInteger is 2^53-1, the largest integer binary64 represents
// exactly. RFC 7493 (I-JSON) section 2.2 bounds interoperable integers here,
// and BDP's admission law (gastownhall/bdp#21) makes a literal past it
// invalid rather than merely lossy.
const maxExactJSONInteger = 1<<53 - 1

// maxExactJSONIntegerBig is maxExactJSONInteger as a *big.Int, so
// refuseUnrepresentableIntegers can compare exact integer values without
// ever routing through float64.
var maxExactJSONIntegerBig = big.NewInt(maxExactJSONInteger)

// refuseUnrepresentableIntegers rejects marshaled if any JSON number literal
// in it denotes an integer VALUE whose magnitude exceeds what binary64 holds
// exactly, regardless of which of JSON's number forms -- bare integer,
// decimal, or exponential -- spells that value.
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
// VALUE, NOT FORM. Each number literal is parsed exactly with big.Rat (never
// float64 -- float64 rounding is the exact thing being guarded against), and
// the check is whether the VALUE is a mathematical integer (big.Rat.IsInt),
// not whether the literal's spelling contains '.', 'e', or 'E'.
// 9007199254740993, 9007199254740993.0, and 9.007199254740993e15 all denote
// the same integer value and must all be refused alike; a check keyed on
// spelling instead of value would let the last two through.
//
// SCOPE, deliberately narrow to integers. A non-integer VALUE such as 0.1 is
// not exactly representable either, but ES6's shortest-round-trip formatting
// is a bijection on doubles: it always returns to the same double, so two
// non-integer literals collide only if they already denote the same double
// before this gate ever runs -- not the failure this gate exists to close.
// The asymmetry is deliberate on its own terms too, not just a side effect of
// the bijection: a non-integer literal already carries an expectation of
// binary64 approximation -- 0.1 was never a promise of exact decimal storage
// -- so canonicalizing it to the nearest double honors that expectation,
// while an integer literal carries the opposite one, exactness, and silently
// changing its VALUE rather than merely its formatting would corrupt an
// identity the caller had every reason to believe was preserved. BDP's
// admission rule (gastownhall/bdp#21 item 2) is itself stated only for
// integers. types.Issue's own integer fields are ordinals and priorities and
// cannot reach this bound -- the reachable path is Metadata, a
// json.RawMessage passed through verbatim.
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
		rat, ok := new(big.Rat).SetString(lit)
		if !ok {
			return fmt.Errorf("scan for unrepresentable integers: invalid JSON number literal %q", lit)
		}
		if !rat.IsInt() {
			continue // not an integer value; see SCOPE above
		}
		abs := new(big.Int).Abs(rat.Num())
		if abs.Cmp(maxExactJSONIntegerBig) > 0 {
			return fmt.Errorf("%w: %s", ErrIntegerNotRepresentable, lit)
		}
	}
}

// RecordVersionInTx mints one issue_versions row for issueID and advances
// issues.current_revision to match, as of tx (read-your-writes within the
// same transaction). A no-op when versioned history is disabled for tx, or
// when issueID resolves to a wisp: wisps carry current_revision for shape
// parity only and are never versioned this phase (design FR-8). IsWisp is
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
func RecordVersionInTx(ctx context.Context, tx DBTX, issueID, actor string) error {
	if !versionedHistoryEnabled(tx) {
		return nil
	}

	issue, err := GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		return fmt.Errorf("versioned history: snapshot %s: %w", issueID, err)
	}
	if IsWisp(issue) {
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

	// store_epoch is one shared row (id = 1) that every minting transaction
	// reads. Read first and seed only when the row is absent, so the seed
	// INSERT happens once per store rather than once per mint: an INSERT IGNORE
	// on every mint put that shared row into every write transaction's
	// footprint for nothing.
	var epoch int
	err = tx.QueryRowContext(ctx, "SELECT epoch FROM store_epoch WHERE id = 1").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		if _, seedErr := tx.ExecContext(ctx, "INSERT IGNORE INTO store_epoch (id, epoch) VALUES (1, 1)"); seedErr != nil {
			return fmt.Errorf("versioned history: seed store epoch: %w", seedErr)
		}
		err = tx.QueryRowContext(ctx, "SELECT epoch FROM store_epoch WHERE id = 1").Scan(&epoch)
	}
	if err != nil {
		return fmt.Errorf("versioned history: read store epoch: %w", err)
	}

	var newRevision int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(revision), 0) + 1 FROM issue_versions WHERE issue_id = ?", issueID,
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
		issueID, newRevision, epoch, durableState, actor, time.Now().UTC(), attributionStatusForActor(actor),
	); err != nil {
		return fmt.Errorf("versioned history: insert version row for %s: %w", issueID, err)
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE issues SET current_revision = ? WHERE id = ?", newRevision, issueID,
	); err != nil {
		return fmt.Errorf("versioned history: advance current_revision for %s: %w", issueID, err)
	}
	return nil
}
