package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
)

// The durable events journal records every committed bead mutation as a row in
// the clone-local bd_events_journal table, written in the SAME transaction as
// the mutation itself. Because the row and the mutation commit atomically, the
// journal can never lag the data or produce a false record.
//
// The seq is NOT an AUTO_INCREMENT. AUTO_INCREMENT assigns a value at INSERT,
// not at commit, so under concurrent transactions (the shared SQL server)
// commit-visibility order can invert seq order: a lower seq can commit after a
// higher seq is already visible, and a consumer tailing WHERE seq > cursor
// would permanently skip it. Instead each seq is drawn from the single-row
// counter table bd_events_seq inside the mutation's own transaction (see
// nextEventSeq). The shared counter row makes concurrent allocators conflict,
// so exactly one commit order survives; the surviving seqs are gapless and
// commit-ordered (a rolled-back allocator burns no seq — the increment rolls
// back with it). This holds on both of bd's Dolt concurrency models: the SQL
// server aborts the losing commit with a serialization error (retried by the
// write path), while the embedded engine serializes writers on the counter row.
//
// See internal/storage/schema/migrations/0064_create_events_journal.up.sql.
//
// Emission lives here, at the issueops seam, because both write plumbings — the
// DoltStorage decorator chain and the unit-of-work path — bottom out in these
// *InTx functions, and both funnel their INSERT through insertEventRow, so
// the seq mechanism is shared by construction and cannot drift between plumbings.
// Instrumenting the seam makes coverage structural: every mutation path
// (including wisps, ready-claims, lease reclaim, renames, and cascade deletes)
// flows through it. TestEveryMutationFunctionJournals guards against a new
// mutation path silently skipping the journal.
//
// The journal is a machine replay feed and is deliberately independent of the
// human-facing `events` audit table: emission here is unconditional and is never
// gated by EmitEvent, so suppressing audit noise cannot silently punch a hole in
// a consumer's cursor. Both rows land in the same transaction.

// EventOp names the kind of mutation a journal row records.
type EventOp string

// The closed set of journalled mutation kinds.
const (
	EventCreate       EventOp = "create"
	EventUpdate       EventOp = "update"
	EventClose        EventOp = "close"
	EventDelete       EventOp = "delete"
	EventDepAdd       EventOp = "dep_add"
	EventDepRemove    EventOp = "dep_remove"
	EventCommentWrite EventOp = "comment"
)

// The engine journals SEVEN ops; the public event vocabulary is SIX. The
// seventh, EventCommentWrite, is engine-only: a projector advances its source
// cursor across a comment row without minting a wire event, so a comment never
// reaches an external consumer as an event of its own (its effect is already
// visible in the next issue snapshot). That reconciliation is the frozen wire
// contract, not an implementation detail to be re-litigated per consumer.
//
// The split is stated here, once, so both halves are checkable from one place:
// adding an op without deciding which side it lands on breaks
// TestEventOpVocabularyIsFrozen.
var (
	// wireEventOps is the closed public vocabulary, in canonical order. These
	// are the only ops that may appear in an event delivered to a consumer.
	wireEventOps = []EventOp{EventCreate, EventUpdate, EventClose, EventDelete, EventDepAdd, EventDepRemove}
	// engineOnlyEventOps are journaled but never minted as wire events.
	engineOnlyEventOps = []EventOp{EventCommentWrite}
)

// WireEventOps returns the six-op public event vocabulary in canonical order.
func WireEventOps() []EventOp { return append([]EventOp(nil), wireEventOps...) }

// EngineOnlyEventOps returns the journaled ops that mint no wire event.
func EngineOnlyEventOps() []EventOp { return append([]EventOp(nil), engineOnlyEventOps...) }

// IsWireEventOp reports whether op is part of the public event vocabulary. A
// journaled op that is not one is engine-only and must be skipped — not
// dropped, not faulted on — by anything projecting the journal outward.
func IsWireEventOp(op EventOp) bool {
	for _, w := range wireEventOps {
		if op == w {
			return true
		}
	}
	return false
}

// EventDep is the edge payload recorded for dependency operations.
type EventDep struct {
	Kind     string `json:"kind"`
	Target   string `json:"target"`
	Metadata string `json:"metadata"`
}

// EventComment is the stable, replayable payload for a comment write. Source
// distinguishes a structured comment row from an audit-trail comment event.
type EventComment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	Source    string    `json:"source"`
}

// The closed set of EventComment.Source values. Consumers switch on these, so
// they are constants rather than literals at each emit site — a comment record
// carrying a source no emitter can produce would be a wire contract nobody
// implements, which is exactly what an untyped string at seven call sites
// invites.
const (
	// CommentSourceStructured is a row in the comments table: the comment a
	// user or agent wrote, replayable as itself.
	CommentSourceStructured = "structured"
	// CommentSourceAudit is a comment recorded as an audit-trail event rather
	// than a comment row.
	CommentSourceAudit = "audit"
)

// CommentSources returns the closed set of EventComment.Source values, in
// canonical order.
func CommentSources() []string {
	return []string{CommentSourceStructured, CommentSourceAudit}
}

// Journal activation is carried by the operation context. Store instances add
// this value when opening an explicitly enabled project, so opening one project
// cannot turn journaling on for any other project in the same process.
// A missing value is deliberately safe-off.
type journalContextKey struct{}

var journalTransactions sync.Map // map[DBTX]journalScope; entries live for one transaction

// journalScope is a transaction's journal activation. journalUnlocked marks a
// write transaction that holds no journal writer lock (the dolt NoJournal
// helpers): an emit there would allocate a seq on bd_events_seq outside the
// lock and abort overlapping journaled writers with 1213, so it fails instead
// of recording or skipping.
type journalScope uint8

const (
	journalOff journalScope = iota
	journalOn
	journalUnlocked
)

// ErrJournalUnlockedTx is returned by a journal emit on a transaction scoped
// with ScopeUnlockedJournalTransaction. The emit records nothing; returning it
// from the transaction body rolls the whole write back.
var ErrJournalUnlockedTx = errors.New("journal: emit in a transaction that holds no journal writer lock")

// WithEventsJournal returns ctx scoped to one storage operation with the
// durable events journal enabled or disabled.
//
// TEST-ONLY, AND ONLY OVER A REAL TRANSACTION. Production activation goes
// through storage.EventsJournalConfigurer / ScopeEventsJournalTransaction,
// which binds activation to a concrete tx. This context form exists because the
// domain/db suite drives repositories over a bare Runner. It is dangerous
// anywhere else: the seq allocation is an UPDATE followed by a SELECT that MUST
// observe that UPDATE, so pointing it at a *sql.DB (pooled, autocommit) or at
// one half of a split transaction would let the two statements land on
// different connections and silently mint duplicate or out-of-order seqs — the
// exact failure the counter exists to prevent. journalEnabled refuses the
// non-transactional case rather than trusting callers.
func WithEventsJournal(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, journalContextKey{}, enabled)
}

// ScopeEventsJournalTransaction associates activation with one concrete
// transaction and returns a cleanup function. Store implementations call it
// immediately after BeginTx. This is instance/project scoped even when many
// stores share a process; there is no process-wide activation switch.
//
// tx MUST be a real transaction (*sql.Tx), or a connection already inside one —
// never a *sql.DB. Seq allocation is an UPDATE whose effect a following SELECT
// must observe, and on a pooled autocommit handle those two statements can land
// on different connections. Every production caller passes the *sql.Tx it just
// began; TestEveryRawTxJournalScopeIsScopedOrExempt in the dolt package pins
// that they all do so.
func ScopeEventsJournalTransaction(tx DBTX, enabled bool) func() {
	if tx == nil {
		return func() {}
	}
	scope := journalOff
	if enabled {
		scope = journalOn
	}
	journalTransactions.Store(tx, scope)
	return func() { journalTransactions.Delete(tx) }
}

// ScopeUnlockedJournalTransaction marks tx as a write transaction that holds
// no journal writer lock and returns a cleanup function. Every journal emit on
// it returns ErrJournalUnlockedTx, whether or not the project's journal is on,
// so a body that was meant to record nothing fails loudly the first time it
// tries, rather than skipping silently (an unscoped tx) or writing outside the
// lock (a journal-scoped one).
func ScopeUnlockedJournalTransaction(tx DBTX) func() {
	if tx == nil {
		return func() {}
	}
	journalTransactions.Store(tx, journalUnlocked)
	return func() { journalTransactions.Delete(tx) }
}

// journalActive reports whether an emit on tx should record, and fails with
// ErrJournalUnlockedTx on an unlocked transaction. The unlocked check comes
// first so the test-only context form cannot mask it.
func journalActive(ctx context.Context, tx DBTX) (bool, error) {
	scope, _ := journalTransactions.Load(tx)
	if scope == journalUnlocked {
		return false, ErrJournalUnlockedTx
	}
	if enabled, ok := ctx.Value(journalContextKey{}).(bool); ok {
		return enabled, nil
	}
	return scope == journalOn, nil
}

// journalEnabled is journalActive for gates that cannot return the error. It
// is true on an unlocked transaction, so the gated path goes on to an emit,
// which fails with ErrJournalUnlockedTx.
func journalEnabled(ctx context.Context, tx DBTX) bool {
	on, err := journalActive(ctx, tx)
	return on || err != nil
}

type blockedJournalKey struct {
	table string
	id    string
}

type blockedJournalSnapshot map[blockedJournalKey]bool

// captureBlockedJournalSnapshot records the pre-maintenance readiness state
// for the exact affected set. It is a no-op unless this transaction's durable
// journal is enabled, so ordinary local operations pay no extra reads.
func captureBlockedJournalSnapshot(
	ctx context.Context,
	tx DBTX,
	issueIDs, wispIDs []string,
) (blockedJournalSnapshot, error) {
	if on, err := journalActive(ctx, tx); !on {
		return nil, err
	}

	snapshot := make(blockedJournalSnapshot, len(issueIDs)+len(wispIDs))
	for _, target := range []struct {
		table string
		ids   []string
	}{
		{table: "issues", ids: issueIDs},
		{table: "wisps", ids: wispIDs},
	} {
		for start := 0; start < len(target.ids); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(target.ids) {
				end = len(target.ids)
			}
			inClause, args := buildSQLInClause(target.ids[start:end])
			//nolint:gosec // table is one of the two hardcoded values above.
			rows, err := tx.QueryContext(ctx,
				fmt.Sprintf("SELECT id, is_blocked FROM %s WHERE id IN (%s)", target.table, inClause),
				args...)
			if err != nil {
				if optionalBlockedTable(target.table) && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("journal: snapshot derived is_blocked from %s: %w", target.table, err)
			}
			for rows.Next() {
				var id string
				var blocked int
				if err := rows.Scan(&id, &blocked); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("journal: scan derived is_blocked from %s: %w", target.table, err)
				}
				snapshot[blockedJournalKey{table: target.table, id: id}] = blocked != 0
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("journal: iterate derived is_blocked from %s: %w", target.table, err)
			}
			if err := rows.Close(); err != nil {
				return nil, fmt.Errorf("journal: close derived is_blocked from %s: %w", target.table, err)
			}
		}
	}
	return snapshot, nil
}

// recordBlockedJournalChanges compares the stable post-maintenance state with
// the captured state and journals only beads whose derived is_blocked value
// actually changed. The emitted update carries the complete post-mutation
// snapshot, allowing a cursor consumer to stay correct without graph queries.
func recordBlockedJournalChanges(
	ctx context.Context,
	tx DBTX,
	before blockedJournalSnapshot,
	issueIDs, wispIDs []string,
) error {
	if before == nil {
		return nil
	}
	after, err := captureBlockedJournalSnapshot(ctx, tx, issueIDs, wispIDs)
	if err != nil {
		return err
	}

	var changed []blockedJournalKey
	for key, afterBlocked := range after {
		if beforeBlocked, existed := before[key]; existed && beforeBlocked != afterBlocked {
			changed = append(changed, key)
		}
	}
	sort.Slice(changed, func(i, j int) bool {
		if changed[i].table != changed[j].table {
			return changed[i].table < changed[j].table
		}
		return changed[i].id < changed[j].id
	})
	for _, key := range changed {
		// Derived maintenance runs from dozens of mutation paths (recompute /
		// mark passes) that carry no actor of their own; attributing the
		// triggering mutation here would mean threading an actor through the
		// whole blocked-state layer, so these rows record no actor.
		if err := RecordEventInTx(ctx, tx, EventUpdate, key.id, ""); err != nil {
			return fmt.Errorf("journal: record derived is_blocked update for %s: %w", key.id, err)
		}
	}
	return nil
}

// RecordEventInTx records op for issueID, snapshotting the issue's
// post-mutation state as of tx (read-your-writes within the same transaction).
// Use it for every op except delete (which has no surviving row — use
// RecordDeleteInTx) and dependency ops (use RecordDepEventInTx). A no-op when
// journaling is disabled.
//
// actor is the acting identity that performed the mutation, as resolved for
// the audit-events table; "" when the mutation path genuinely has none —
// derived is_blocked maintenance, and the system surfaces with no request
// behind them (storage.DeleteIssue, gc and molecule cleanup, `bd repo
// remove`). A user-initiated delete is NOT one of those: its rows, cascade
// included, carry the requesting actor. It is an explicit parameter, not
// ambient context, so a new call site cannot compile without deciding
// attribution.
func RecordEventInTx(ctx context.Context, tx DBTX, op EventOp, issueID, actor string) error {
	_, err := recordIssueEventInTx(ctx, tx, op, issueID, actor)
	return err
}

// RecordCreateEventInTx is RecordEventInTx(EventCreate) for the create seams
// that write the stored row back onto the caller's issue (finishCreateIssueInTx
// and the unit-of-work repository's Insert): once the record is journaled it
// hands the row's arrival_seq back onto issue — the stamp this record just
// wrote, or the one an earlier record left on a row an import upsert rewrote —
// so the struct a caller keeps describes the row it created. The insert paths
// zero the field on the way in (PrepareIssueForInsert), so without this a
// caller that keeps its struct, as the public storage API's CreateIssue,
// CreateIssues and Transaction.CreateIssue callers do, reads every stamped
// wisp as unstamped. With the journal off nothing is recorded or stamped, and
// the field stays zero.
func RecordCreateEventInTx(ctx context.Context, tx DBTX, issue *types.Issue, actor string) error {
	recorded, err := recordIssueEventInTx(ctx, tx, EventCreate, issue.ID, actor)
	if err != nil {
		return err
	}
	if recorded != nil {
		issue.ArrivalSeq = recorded.ArrivalSeq
	}
	return nil
}

// recordIssueEventInTx is RecordEventInTx's body. It returns the snapshot it
// journaled — nil when the journal is off — whose ArrivalSeq is the row's
// stamp once the record is written.
func recordIssueEventInTx(ctx context.Context, tx DBTX, op EventOp, issueID, actor string) (*types.Issue, error) {
	if on, err := journalActive(ctx, tx); !on {
		return nil, err
	}
	issue, table, err := getJournalIssueInTx(ctx, tx, issueID)
	if err != nil {
		// The row should exist for a non-delete op; a missing row means the
		// mutation and the journal disagree, so fail the transaction rather than
		// record a hole.
		return nil, fmt.Errorf("journal: snapshot %s for %s: %w", op, issueID, err)
	}
	// A create of a wisp-plane row stamps the row with its create record's seq
	// (arrival_seq). Only a row that has no stamp yet takes one — the first
	// create record journaled for it — so a rename or an import upsert, which
	// journal a synthetic create for a row that already exists, never move a
	// stamp. issues rows are never stamped: that table replicates, and a
	// clone-local number has no meaning on another clone (migration 0073).
	stamp := op == EventCreate && table == "wisps" && issue.ArrivalSeq == 0
	if err := insertEventRow(ctx, tx, op, issueID, issue, nil, nil, actor, stamp); err != nil {
		return nil, err
	}
	return issue, nil
}

// RecordDeleteInTx records a delete for issueID with a null issue payload (the
// row no longer exists). A no-op when journaling is disabled. actor as on
// RecordEventInTx.
func RecordDeleteInTx(ctx context.Context, tx DBTX, issueID, actor string) error {
	if on, err := journalActive(ctx, tx); !on {
		return err
	}
	return insertEventRow(ctx, tx, EventDelete, issueID, nil, nil, nil, actor, false)
}

// journalableDeletesInTx narrows ids to the ones that actually exist in table,
// so a bulk delete records only rows it really removes. It is a no-op (nil,
// nil) when journaling is disabled, keeping the extra read off the ordinary
// local delete path. Callers MUST invoke it before issuing their DELETE.
func journalableDeletesInTx(ctx context.Context, tx DBTX, table string, ids []string) ([]string, error) {
	if on, err := journalActive(ctx, tx); !on || len(ids) == 0 {
		return nil, err
	}
	existing, err := ExistingIssueIDsInTableInTx(ctx, tx, table, ids)
	if err != nil {
		return nil, fmt.Errorf("journal: resolve deleted ids in %s: %w", table, err)
	}
	return existing, nil
}

// RecordDepEventInTx records a dependency add or remove for issueID, carrying
// the edge kind and target. The issue snapshot is the post-mutation state as of
// tx. A no-op when journaling is disabled. actor as on RecordEventInTx.
func RecordDepEventInTx(ctx context.Context, tx DBTX, op EventOp, issueID, kind, target, metadata, actor string) error {
	if on, err := journalActive(ctx, tx); !on {
		return err
	}
	issue, _, err := getJournalIssueInTx(ctx, tx, issueID)
	if err != nil {
		// The dependency source may itself have been deleted (cascade); record
		// the edge change with a null snapshot rather than failing.
		if errors.Is(err, storage.ErrNotFound) {
			return insertEventRow(ctx, tx, op, issueID, nil, &EventDep{Kind: kind, Target: target, Metadata: metadata}, nil, actor, false)
		}
		return fmt.Errorf("journal: snapshot %s for %s: %w", op, issueID, err)
	}
	return insertEventRow(ctx, tx, op, issueID, issue, &EventDep{Kind: kind, Target: target, Metadata: metadata}, nil, actor, false)
}

// RecordCommentEventInTx records a replayable structured or audit comment. The
// journal row's actor is the comment's Author — the identity that wrote it —
// so this takes no separate actor parameter.
func RecordCommentEventInTx(ctx context.Context, tx DBTX, issueID string, comment *EventComment) error {
	if on, err := journalActive(ctx, tx); !on {
		return err
	}
	issue, _, err := getJournalIssueInTx(ctx, tx, issueID)
	if err != nil {
		return fmt.Errorf("journal: snapshot comment for %s: %w", issueID, err)
	}
	return insertEventRow(ctx, tx, EventCommentWrite, issueID, issue, nil, comment, comment.Author, false)
}

// getJournalIssueInTx augments the normal issue snapshot with the persisted
// readiness projection. is_blocked is deliberately not part of ordinary issue
// hydration, but it is required in a journal snapshot because a dependency
// delta must be replayable without re-running graph maintenance. It also
// reports which table the row was read from, which decides whether a create
// record stamps the row's arrival_seq.
func getJournalIssueInTx(ctx context.Context, tx DBTX, issueID string) (*types.Issue, string, error) {
	for _, candidate := range []struct {
		issueTable string
		labelTable string
	}{
		{"issues", "labels"},
		{"wisps", "wisp_labels"},
	} {
		issue, err := getIssueFromTableInTx(ctx, tx, candidate.issueTable, candidate.labelTable, issueID)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			if optionalBlockedTable(candidate.issueTable) && isTableNotExistError(err) {
				continue
			}
			return nil, "", err
		}
		var blocked int
		//nolint:gosec // candidate.issueTable is one of the two hardcoded values above.
		if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT is_blocked FROM %s WHERE id = ?", candidate.issueTable), issueID).Scan(&blocked); err != nil {
			return nil, "", fmt.Errorf("journal: read is_blocked for %s: %w", issueID, err)
		}
		issue.IsBlocked = blocked != 0
		return issue, candidate.issueTable, nil
	}
	return nil, "", fmt.Errorf("%w: issue %s", storage.ErrNotFound, issueID)
}

// insertEventRow performs the actual INSERT. It is the ONE seam both write
// plumbings funnel through, so the seq mechanism cannot drift between them. A
// nil issue is stored as SQL NULL (deletes); a nil dep is stored as SQL NULL
// (non-dependency ops). ts is the insert time, stamped inside the committing
// transaction. actor is stored as-is — "" for the genuinely unattributable
// paths — in the NOT NULL DEFAULT ” actor column.
//
// stamp asks for the create record's seq to be written onto the wisps row as
// its arrival_seq (only recordIssueEventInTx, RecordEventInTx's body, passes
// true, and only for a create of an unstamped wisp-plane row). The stamp is
// written after the seq is drawn and before the snapshot is marshaled, so the
// record's post-image carries the value it stamped — record.seq ==
// row.arrival_seq == post-image.arrival_seq — and the counter is held for one
// extra statement, not for a second counter.
func insertEventRow(ctx context.Context, tx DBTX, op EventOp, issueID string, issue *types.Issue, dep *EventDep, comment *EventComment, actor string, stamp bool) error {
	var depJSON any
	if dep != nil {
		b, err := json.Marshal(dep)
		if err != nil {
			return fmt.Errorf("journal: marshal dep for %s: %w", issueID, err)
		}
		depJSON = string(b)
	}
	var commentJSON any
	if comment != nil {
		b, err := json.Marshal(comment)
		if err != nil {
			return fmt.Errorf("journal: marshal comment for %s: %w", issueID, err)
		}
		commentJSON = string(b)
	}
	// record writes the journal row at seq. prior is the stamp an earlier
	// attempt in this transaction already wrote (0 for none), which the retry
	// below moves rather than leaving behind.
	record := func(seq, prior int64) error {
		if stamp {
			if err := stampArrivalSeqInTx(ctx, tx, issueID, seq, prior); err != nil {
				return err
			}
			issue.ArrivalSeq = seq
		}
		var issueJSON any
		if issue != nil {
			b, err := json.Marshal(issue)
			if err != nil {
				return fmt.Errorf("journal: marshal issue %s: %w", issueID, err)
			}
			issueJSON = string(b)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO bd_events_journal (seq, ts, op, issue_id, actor, issue_json, dep_json, comment_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, seq, time.Now().UTC(), string(op), issueID, actor, issueJSON, depJSON, commentJSON)
		return err
	}

	seq, err := nextEventSeq(ctx, tx)
	if err != nil {
		return err
	}
	if err := record(seq, 0); err != nil {
		// A duplicate seq means the counter is BEHIND a value it already handed
		// out — a journal row's seq, or a wisps row's arrival_seq stamp (which
		// outlives its record once the journal is pruned). It was restored,
		// hand-edited, or copied from another workspace. Left alone that wedges
		// the instance permanently: every later mutation re-mints the same taken
		// seq and fails, and because the journal row shares the mutation's
		// transaction, the user's write fails with it. Raise the counter past
		// both high-water marks and retry exactly once; a second duplicate is a
		// real bug and must surface, not spin. The retry moves a stamp the
		// failed attempt already wrote (prior), since a stamp that landed before
		// its journal INSERT collided names a seq this record will not carry.
		if !isSeqCollision(err) {
			return fmt.Errorf("journal: record %s for %s: %w", op, issueID, err)
		}
		if healErr := healEventSeqCounter(ctx, tx); healErr != nil {
			return healErr
		}
		retry, err := nextEventSeq(ctx, tx)
		if err != nil {
			return err
		}
		if err := record(retry, seq); err != nil {
			return fmt.Errorf("journal: record %s for %s after seq counter heal: %w", op, issueID, err)
		}
	}
	return nil
}

// stampArrivalSeqInTx writes seq onto the wisps row issueID as its arrival_seq.
// It is the ONLY writer of arrival_seq (TestArrivalSeqIsWrittenOnlyByTheJournalSeam):
// the generic insert, upsert and move paths never bind the column, so no value
// a caller supplies is ever stored.
//
// The predicate admits the unstamped row, or the row carrying prior — the stamp
// an earlier attempt in this same transaction wrote before its journal INSERT
// collided — and nothing else, so a stamp is never overwritten once its record
// exists. It writes no other column: updated_at is self-assigned because the
// column carries ON UPDATE CURRENT_TIMESTAMP on Dolt, and row_lock,
// current_revision and content_hash are left alone, so the revision a caller
// reads right after its create still passes its compare-and-set.
//
// Exactly one row must match. Zero would mean the record about to be written
// claims a stamp the row does not carry, which fails the transaction rather
// than journal a false post-image.
func stampArrivalSeqInTx(ctx context.Context, tx DBTX, issueID string, seq, prior int64) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE wisps SET arrival_seq = ?, updated_at = updated_at
		WHERE id = ? AND (arrival_seq IS NULL OR arrival_seq = ?)
	`, seq, issueID, prior)
	if err != nil {
		return fmt.Errorf("journal: stamp arrival_seq %d on %s: %w", seq, issueID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("journal: stamp arrival_seq %d on %s: rows affected: %w", seq, issueID, err)
	}
	if n != 1 {
		return fmt.Errorf("journal: stamp arrival_seq %d on %s matched %d rows, want 1: the row is missing or already stamped", seq, issueID, n)
	}
	return nil
}

// isSeqCollision reports whether err is a uniqueness violation on a value the
// seq counter handed out: the journal's seq primary key, or the wisps
// arrival_seq unique index. dberrors.IsDuplicateKey covers MySQL/Dolt server
// (1062/1586) and the embedded engine's primary-key text. The embedded engine
// reports a UNIQUE-index violation with its own error kind, but Kind.Is does
// not unwrap, and the stamp's violation arrives wrapped (stampArrivalSeqInTx's
// %w), so the kind arms match only a bare error: the "duplicate unique key"
// text is what recognizes the wrapped one.
func isSeqCollision(err error) bool {
	return dberrors.IsDuplicateKey(err) || gmssql.ErrUniqueKeyViolation.Is(err) || gmssql.ErrPrimaryKeyViolation.Is(err) ||
		strings.Contains(strings.ToLower(err.Error()), "duplicate unique key")
}

// healEventSeqCounter seeds the counter row if it is missing and raises it to
// the high-water mark of every value it has ever handed out, so the next
// allocation cannot collide. That is the journal's MAX(seq) and the highest
// wisps.arrival_seq: a stamp IS a journal seq (see stampArrivalSeqInTx) and it
// outlives its record once the journal is pruned, so a counter healed to the
// journal alone could re-mint a value a row already carries. VALUES + GREATEST,
// not INSERT ... SELECT MAX(): in Dolt a literal+aggregate SELECT over an empty
// table yields zero rows, so an INSERT ... SELECT would seed nothing on a fresh
// journal. GREATEST also makes this safe to call on a counter that is already
// ahead — it never moves the counter backwards.
//
// A workspace whose wisps table, or its arrival_seq column, is missing (a
// drifted clone whose ignored series has not materialized them) has no stamps
// to clear, so the heal falls back to the journal alone: on MySQL and Dolt a
// failed statement leaves the transaction usable, so the second UPDATE runs in
// the same transaction as the first.
func healEventSeqCounter(ctx context.Context, tx DBTX) error {
	if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO bd_events_seq (id, next_seq) VALUES (0, 0)"); err != nil {
		return fmt.Errorf("journal: seed seq counter: %w", err)
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE bd_events_seq
		SET next_seq = GREATEST(next_seq,
			COALESCE((SELECT MAX(seq) FROM bd_events_journal), 0),
			COALESCE((SELECT MAX(arrival_seq) FROM wisps), 0))
		WHERE id = 0
	`)
	if err != nil && isMissingArrivalSeqSource(err) {
		_, err = tx.ExecContext(ctx, `
			UPDATE bd_events_seq
			SET next_seq = GREATEST(next_seq, COALESCE((SELECT MAX(seq) FROM bd_events_journal), 0))
			WHERE id = 0
		`)
	}
	if err != nil {
		return fmt.Errorf("journal: raise seq counter to high-water mark: %w", err)
	}
	return nil
}

// isMissingArrivalSeqSource reports whether err means the heal's wisps term
// cannot be evaluated because the wisps table or its arrival_seq column does not
// exist — the drifted-clone case healEventSeqCounter falls back on. Any other
// failure is returned by the heal rather than retried without the term.
func isMissingArrivalSeqSource(err error) bool {
	if dberrors.IsMissingTable(err, "wisps") {
		return true
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlUnknownColumn {
		return true
	}
	if gmssql.ErrColumnNotFound.Is(err) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "arrival_seq") &&
		(strings.Contains(s, "could not be found") || strings.Contains(s, "unknown column"))
}

// mysqlUnknownColumn is ER_BAD_FIELD_ERROR, MySQL's "Unknown column" code. Dolt's
// sql-server does not send it for a column the table lacks: 2.6 answers with
// error 1105 and the text `column "arrival_seq" could not be found in any table
// in scope`, so isMissingArrivalSeqSource's text match is what recognizes it
// there, and must stay.
const mysqlUnknownColumn = 1054

// nextEventSeq allocates the next journal sequence number from the single-row
// bd_events_seq counter, INSIDE the caller's transaction. Incrementing the
// shared counter row is what serializes seq assignment: two transactions that
// both allocate a seq contend on the one row, so only one commit order survives.
// The value becomes the journal row's seq, yielding gapless, commit-ordered seqs
// (a rolled-back transaction rolls back its increment, burning no seq). The
// counter persists across restart and prune never touches it, so seq never
// resets. The seed row is created by migration 0064 / ignored 0022; the
// self-heal below re-creates it at the journal's high-water mark if it is ever
// missing, so a re-seed can never collide with an existing seq. A counter that
// is PRESENT but stale cannot be detected here without a per-emit MAX(seq)
// read, so insertEventRow heals that case reactively off the duplicate-key
// failure instead — see there.
func nextEventSeq(ctx context.Context, tx DBTX) (int64, error) {
	advance := func() (int64, error) {
		res, err := tx.ExecContext(ctx, "UPDATE bd_events_seq SET next_seq = next_seq + 1 WHERE id = 0")
		if err != nil {
			return 0, fmt.Errorf("journal: advance seq counter: %w", err)
		}
		return res.RowsAffected()
	}
	n, err := advance()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		// The counter row is missing entirely: seed it at the journal's
		// high-water mark, so the re-seed can never collide with an existing seq.
		if err := healEventSeqCounter(ctx, tx); err != nil {
			return 0, err
		}
		if _, err := advance(); err != nil {
			return 0, err
		}
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, "SELECT next_seq FROM bd_events_seq WHERE id = 0").Scan(&seq); err != nil {
		return 0, fmt.Errorf("journal: read seq counter: %w", err)
	}
	return seq, nil
}

// compile-time assurance that *sql.Tx satisfies DBTX (the emit helpers accept
// both *sql.Tx and *sql.DB via DBTX).
var _ DBTX = (*sql.Tx)(nil)
