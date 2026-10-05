package issueops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/steveyegge/beads/internal/debug"
)

// Adaptive I/O for bd_events_journal (BEADS-JOURNAL-PLAN.md §4.2a, PR A1).
//
// The writer and reader used to name a fixed column list. That is fine on a
// table built by the current migrations, but a table built by an earlier
// lineage — gas-city-inc's is the live case — can lack an optional column
// forever: the ignored series only ever creates the table when it is wholly
// absent, and its one repair (ignored/0023) probes column TYPES, never
// EXISTENCE. A fixed INSERT naming a missing column then fails every
// journaled write, and because the journal row commits in the SAME
// transaction as the mutation it records, that failure rolls back the user's
// write along with it.
//
// The fix probes once, at activation, rather than guessing or failing per
// write:
//   - REQUIRED columns {seq, ts, op, issue_id} have existed on every lineage
//     this table has ever shipped under; a table missing one of them, or
//     missing entirely, cannot journal at all. ProbeJournalShape refuses with
//     a typed error so a store's "enable the journal" step fails at open,
//     before any mutation is attempted — never as a per-write failure after
//     the work is otherwise done, which is the gci failure mode above.
//   - OPTIONAL columns {actor, issue_json, dep_json, comment_json} may be
//     missing on an old-lineage table. The writer drops whichever of them the
//     probe did not find; the reader returns "" for them. One warning is
//     logged per probe (i.e. once per store instance, not once per write),
//     and ignored/0028 (PR A2) is what actually converges the shape — this
//     file only keeps a drifted table WRITABLE and READABLE in the meantime.
//
// A store instance probes exactly once, when storage.EventsJournalConfigurer
// activates the journal (see each backend's SetEventsJournalEnabled), and
// caches the *JournalShape for its own lifetime. ScopeEventsJournalShape
// carries that cached value onto one transaction, next to
// ScopeEventsJournalTransaction's activation switch, so insertEventRow and
// readEventsRowsInTx — both of which only ever see a tx, never the store —
// can reach it without a per-write probe (TestJournalShapeProbeOncePerOpen).

// journalTableName is the clone-local table every probe, insert and read in
// this file targets.
const journalTableName = "bd_events_journal"

// requiredJournalColumns are the columns ProbeJournalShape treats as
// non-negotiable: every lineage of bd_events_journal has always carried them,
// and the seq/ts/op/issue_id contract (nextEventSeq, the reader's ordering)
// is meaningless without them.
var requiredJournalColumns = []string{"seq", "ts", "op", "issue_id"}

// optionalJournalColumns are the columns a pre-merge lineage may lack. Order
// matches the canonical INSERT column order a healthy table has carried since
// migration 0066/ignored 0025 added actor.
var optionalJournalColumns = []string{"actor", "issue_json", "dep_json", "comment_json"}

// JournalShape records which OPTIONAL bd_events_journal columns exist on one
// store's table. A nil *JournalShape is never handed to a caller; probing a
// table missing a REQUIRED column returns an error instead (see
// ProbeJournalShape).
type JournalShape struct {
	HasActor       bool
	HasIssueJSON   bool
	HasDepJSON     bool
	HasCommentJSON bool
}

// missingOptional names the optional columns this shape does not have, in
// optionalJournalColumns order, for the doctor finding and the one-per-probe
// warning.
func (s *JournalShape) missingOptional() []string {
	if s == nil {
		return nil
	}
	var missing []string
	if !s.HasActor {
		missing = append(missing, "actor")
	}
	if !s.HasIssueJSON {
		missing = append(missing, "issue_json")
	}
	if !s.HasDepJSON {
		missing = append(missing, "dep_json")
	}
	if !s.HasCommentJSON {
		missing = append(missing, "comment_json")
	}
	return missing
}

// canonicalJournalShape is every optional column present — the shape every
// workspace created from main migration 0064 plus ignored 0022/0025 has, and
// the default journalShapeFor returns for a transaction nobody scoped. That
// default matters: it is what every call site written before this file
// existed (and every test that activates the journal through the ctx-only
// WithEventsJournal test helper rather than a real store) keeps getting, so
// adaptive I/O is additive and changes nothing for a canonical table.
var canonicalJournalShape = &JournalShape{
	HasActor:       true,
	HasIssueJSON:   true,
	HasDepJSON:     true,
	HasCommentJSON: true,
}

// ErrJournalShapeUnsupported is the sentinel errors.Is matches against any
// *JournalShapeError returned by ProbeJournalShape, regardless of which
// required columns a particular table happens to be missing.
var ErrJournalShapeUnsupported = errors.New("events journal: table shape does not support the events journal")

// JournalShapeError reports that bd_events_journal (or the table itself) is
// missing one or more REQUIRED columns. It is returned by ProbeJournalShape
// and is the typed error storage.EventsJournalConfigurer implementations
// surface from activation, so "enable the journal" fails at open rather than
// the gci failure mode: succeeding at open and then failing every write thereafter.
type JournalShapeError struct {
	Table   string
	Missing []string
}

func (e *JournalShapeError) Error() string {
	return fmt.Sprintf(
		"events journal: table %s is missing required column(s) %s; the events journal cannot be enabled against this shape (see `bd doctor`)",
		e.Table, strings.Join(e.Missing, ", "),
	)
}

// Is makes errors.Is(err, ErrJournalShapeUnsupported) match any
// *JournalShapeError, so a caller can test for the class of failure without
// caring which columns a particular table lacks.
func (e *JournalShapeError) Is(target error) bool {
	return target == ErrJournalShapeUnsupported //nolint:errorlint // sentinel identity comparison is the documented contract of Is.
}

// journalShapeProbeCount counts how many times ProbeJournalShape has actually
// queried INFORMATION_SCHEMA in this process. It exists for
// TestJournalShapeProbeOncePerOpen, which asserts that a batch of writes
// against one already-activated store issues none of its own.
var journalShapeProbeCount atomic.Int64

// JournalShapeProbeCountForTest reports journalShapeProbeCount's current
// value. Test-only instrumentation, read by TestJournalShapeProbeOncePerOpen;
// production code never calls it.
func JournalShapeProbeCountForTest() int64 { return journalShapeProbeCount.Load() }

// ProbeJournalShape runs the one INFORMATION_SCHEMA.COLUMNS query that
// determines what q's bd_events_journal table actually supports. Callers run
// it exactly once per store/provider instance, from
// storage.EventsJournalConfigurer.SetEventsJournalEnabled, and cache the
// result for that instance's lifetime — never per write or per transaction.
//
// A table missing any required column, including a wholly absent table
// (which reads as every column missing, since a COLUMNS query against a
// nonexistent table returns zero rows — the same shape ignored/0023's
// existing probes rely on), returns *JournalShapeError instead of a shape.
func ProbeJournalShape(ctx context.Context, q DBTX) (*JournalShape, error) {
	journalShapeProbeCount.Add(1)

	rows, err := q.QueryContext(ctx, `
		SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, journalTableName)
	if err != nil {
		return nil, fmt.Errorf("journal: probe %s shape: %w", journalTableName, err)
	}
	defer rows.Close()

	present := make(map[string]bool, len(requiredJournalColumns)+len(optionalJournalColumns))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("journal: scan %s column: %w", journalTableName, err)
		}
		present[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal: read %s shape: %w", journalTableName, err)
	}

	var missing []string
	for _, col := range requiredJournalColumns {
		if !present[col] {
			missing = append(missing, col)
		}
	}
	if len(missing) > 0 {
		return nil, &JournalShapeError{Table: journalTableName, Missing: missing}
	}

	shape := &JournalShape{
		HasActor:       present["actor"],
		HasIssueJSON:   present["issue_json"],
		HasDepJSON:     present["dep_json"],
		HasCommentJSON: present["comment_json"],
	}
	warnJournalShapeDegraded(shape)
	return shape, nil
}

// warnJournalShapeDegraded writes one audit line to STDERR, suppressed only
// by quiet mode, the first (and every) time a store probes a table missing an
// optional column — one call per ProbeJournalShape invocation, which is one
// per store activation, which is "one warning per store" regardless of how
// many writes that store goes on to make.
func warnJournalShapeDegraded(shape *JournalShape) {
	missing := shape.missingOptional()
	if len(missing) == 0 {
		return
	}
	if debug.IsQuiet() {
		return
	}
	fmt.Fprintf(os.Stderr,
		"events journal: %s is missing optional column(s) %s; affected payload(s) will be dropped until it converges (see `bd doctor`)\n",
		journalTableName, strings.Join(missing, ", "))
}

// journalShapes associates a probed *JournalShape with ONE concrete
// transaction, the same per-tx lifetime journalTransactions gives the
// activation switch. Entries live for one transaction only.
var journalShapes sync.Map // map[DBTX]*JournalShape

// ScopeEventsJournalShape binds shape to tx so insertEventRow and
// readEventsRowsInTx can reach the store's already-cached probe result
// without taking the store itself as a parameter. Store implementations call
// it immediately after BeginTx, alongside ScopeEventsJournalTransaction,
// passing the *JournalShape SetEventsJournalEnabled cached at activation —
// never a fresh probe.
//
// A nil tx or nil shape is a no-op; journalShapeFor then returns
// canonicalJournalShape, today's fixed eight-column behavior.
func ScopeEventsJournalShape(tx DBTX, shape *JournalShape) func() {
	if tx == nil || shape == nil {
		return func() {}
	}
	journalShapes.Store(tx, shape)
	return func() { journalShapes.Delete(tx) }
}

// journalShapeFor returns the shape scoped to tx, or canonicalJournalShape
// when nobody scoped one — which is every call site that predates this file,
// and every test driving the journal through the ctx-only WithEventsJournal
// helper rather than a real store's activation.
func journalShapeFor(tx DBTX) *JournalShape {
	if v, ok := journalShapes.Load(tx); ok {
		if shape, ok := v.(*JournalShape); ok && shape != nil {
			return shape
		}
	}
	return canonicalJournalShape
}
