package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// Reading and pruning the durable events journal (bd_events_journal). Both
// query bodies live here, at the issueops seam, for the same reason emission
// does: every store plumbing — embedded, server-mode, and the unit-of-work
// repositories — hands its own transaction to these functions, so the read
// contract and the retention rules cannot drift between plumbings.
//
// Pruning deletes rows below a sequence number, but never below a configurable
// retention floor. Two independent floors compose: keep every row younger than
// events-journal-retain-days, and always keep the newest
// events-journal-retain-rows rows regardless of age. The floors are pure "keep"
// constraints, so they can only ever reduce what a prune removes — a consumer
// that has not advanced its watermark stays protected even against
// `prune --before <huge>`.
//
// Both prunes resolve through the same floors: the one an operator asks for
// (`bd events prune`, this file) and the automatic bounding that keeps an
// enabled journal inside its floors (journal_autoprune.go, whose whole target
// is "the bound the floors leave when nothing else constrains it").
//
// EVERY prune is a strict PREFIX delete: --before and both floors are resolved
// into ONE exclusive seq bound and the delete is `seq < bound`. Nothing here
// filters rows individually.
//
// That is a deliberate departure from the reference implementation this was
// ported from, which expressed the age floor as a per-row `AND ts < cutoff`.
// ts is CLIENT-stamped at insert time and is therefore NOT monotone in seq: two
// writers against one SQL server, or a single writer whose clock is stepped
// back by NTP, can commit seq N with a later ts than seq N+1. A per-row age
// predicate then deletes N+1 while keeping N, leaving a hole in the MIDDLE of
// the retained window — and the truncation check can only see a missing PREFIX,
// so that hole is silent record loss, exactly what this feature exists to
// prevent. Resolving the age floor to a seq bound (the oldest seq that is still
// young enough) costs one extra scalar read and makes an interleaved timestamp
// keep more rather than split a pair. See ComputeEventsTruncation for the
// defense-in-depth half.
//
// Both helpers here are engine state only — the journal table is dolt_ignored —
// so pruning never touches versioned issue data.

// EventsPruneRowsCeilQuery returns the query that finds the highest seq a
// retain-rows floor is allowed to delete: the seq of the (retainRows+1)-th
// newest row. Bind retainRows as the OFFSET. It returns no row when the journal
// holds retainRows or fewer rows, meaning the whole journal is inside the
// retained window and nothing may be pruned by the rows floor.
func EventsPruneRowsCeilQuery() string {
	return `SELECT seq FROM bd_events_journal ORDER BY seq DESC LIMIT 1 OFFSET ?`
}

// EventsPruneDaysFloorQuery returns the query that resolves the retain-days
// floor to a seq: the LOWEST seq whose ts is at or after the cutoff. Bind the
// cutoff timestamp. Everything from that seq up is protected by the age floor,
// so a prune may not cross it.
//
// MIN(seq) rather than a per-row age test is the whole fix: it protects an old
// row that sits ABOVE a young one instead of deleting it out from under the
// rows around it. MIN over no matching rows yields a single NULL row (no
// ErrNoRows), which reads as "every row is older than the cutoff" — the age
// floor then constrains nothing.
func EventsPruneDaysFloorQuery() string {
	return `SELECT MIN(seq) FROM bd_events_journal WHERE ts >= ?`
}

// BuildEventsPruneWhere renders a resolved prune bound as the WHERE predicate
// (without the "WHERE" keyword) and its bind args. One clause, always: the
// bound already carries --before and both floors.
func BuildEventsPruneWhere(bound int64) (string, []any) {
	return "seq < ?", []any{bound}
}

// EventsPruneCutoff is the timestamp a retain-days floor protects from.
func EventsPruneCutoff(retainDays int, now time.Time) time.Time {
	return now.AddDate(0, 0, -retainDays).UTC()
}

// ReadEventsInTx returns journal rows with seq greater than since, ordered by
// seq ascending. limit > 0 caps the result. It runs inside the caller's
// transaction so it works on every store plumbing (embedded, server, proxied).
//
// It returns *storage.EventsJournalTruncatedError when since sits below the
// retained window — see ComputeEventsTruncation for why that is a hard failure
// rather than a silent skip.
func ReadEventsInTx(ctx context.Context, tx DBTX, since int64, limit int) ([]storage.EventsJournalRow, error) {
	out, err := readEventsRowsInTx(ctx, tx, since, limit)
	if err != nil {
		return nil, err
	}
	if err := ComputeEventsTruncation(since, out, func() (int64, error) {
		return readEventsHeadInTx(ctx, tx)
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadEventsPageInTx is ReadEventsInTx plus the journal HEAD, for a consumer
// that has to know how far behind it is rather than merely what came next.
// `bd events tail` does not: it prints what it is given and polls again. A
// polling HTTP consumer does, because the answer to "keep asking, or wait?"
// is not in the rows.
//
// The head read is UNCONDITIONAL here, where ReadEventsInTx pays for one only
// in the ambiguous cases. That is the deliberate cost of the extra member:
// tail --follow runs this decision every second and must stay free, so the two
// entry points differ in exactly this and share everything else — the row
// query, the normalization, and ComputeEventsTruncation's verdict.
//
// The head is read AFTER the rows and inside the SAME transaction, which is
// what keeps the pair coherent. Reading it first would let a mutation commit in
// between and hand back a head BELOW the last row served — a consumer would
// read that as "you are past the end" and stall. This order can only report a
// head that is equal to or ahead of the last row, which is the truth a poller
// acts on correctly either way.
func ReadEventsPageInTx(ctx context.Context, tx DBTX, since int64, limit int) (storage.EventsJournalPage, error) {
	rows, err := readEventsRowsInTx(ctx, tx, since, limit)
	if err != nil {
		return storage.EventsJournalPage{}, err
	}
	head, err := readEventsHeadInTx(ctx, tx)
	if err != nil {
		return storage.EventsJournalPage{}, err
	}
	// The SAME verdict the CLI read reaches, from the head already in hand: a
	// truncation this path reported differently would be a second retention
	// contract.
	if err := ComputeEventsTruncation(since, rows, func() (int64, error) {
		return head, nil
	}); err != nil {
		return storage.EventsJournalPage{}, err
	}
	return storage.EventsJournalPage{Rows: rows, Head: head}, nil
}

// ComputeEventsTruncation decides whether the rows a read returned are the
// caller's contiguous continuation from since, or the remains of a window whose
// prefix a prune already deleted. It returns *storage.EventsJournalTruncatedError
// in the latter case and nil otherwise.
//
// This is the ONE place the decision lives, so every read plumbing that serves
// a `--since` — the DBTX path and anything projecting the journal outward —
// answers a pruned-past checkpoint identically. readHead is only invoked in the
// ambiguous cases below.
//
// Seqs are gapless by construction (see nextEventSeq) and prune only ever
// deletes a prefix (`seq < bound`), so the hole a reader normally observes is a
// missing prefix. That makes the common case free: when the first row returned
// is exactly since+1 AND the batch is internally contiguous, the read is a
// clean continuation and no extra query runs — which matters because
// `bd events tail --follow` calls this every second.
//
// Three shapes cost a counter read, and they are checked in this order:
//
//  1. no rows at all: either genuinely caught up (since >= head) or the whole
//     journal was pruned out from under the caller (since < head), which is
//     indistinguishable from "nothing new" at the SQL level and is exactly the
//     silent-loss case this exists to catch.
//  2. rows start above since+1: the prefix was pruned. Floor is the first row
//     and Since is the caller's own checkpoint — the nearest hole is the one a
//     consumer must decide about first.
//  3. the batch itself skips a seq: a hole in the MIDDLE of the retained
//     window. Nothing in bd can produce one — prune is prefix-only by
//     construction (see ComputeEventsPruneWhere) — but the rows are already in
//     memory, so checking costs nothing and covers what the prefix argument
//     cannot: a restored, hand-edited, or partially-copied journal table, and
//     any future prune path that forgets the prefix rule. A silent interior
//     hole is the one failure this feature must never ship, so it is checked
//     rather than argued.
//
// The prefix check precedes the interior sweep deliberately. A batch can have
// BOTH shapes at once, and only one window can be reported; reporting the
// interior hole would name a Floor beyond a gap the caller has not been told
// about yet, skipping it silently. Reporting the nearest hole first means a
// consumer that resumes from Floor-1 meets the next hole on its next read and
// decides about that one too — every gap is surfaced, one resume at a time, and
// none is stepped over.
func ComputeEventsTruncation(since int64, rows []storage.EventsJournalRow, readHead func() (int64, error)) error {
	if len(rows) == 0 {
		head, err := readHead()
		if err != nil {
			return err
		}
		if since >= head {
			return nil
		}
		// Empty journal: the floor is one past the head — nothing is retained,
		// and the caller's checkpoint is provably behind it.
		return &storage.EventsJournalTruncatedError{Since: since, Floor: head + 1, Head: head}
	}
	if rows[0].Seq != since+1 {
		head, err := readHead()
		if err != nil {
			return err
		}
		return &storage.EventsJournalTruncatedError{Since: since, Floor: rows[0].Seq, Head: head}
	}
	if gapAt := firstEventsSeqGap(rows); gapAt > 0 {
		head, err := readHead()
		if err != nil {
			return err
		}
		// The prefix is intact, so the caller's checkpoint is not the boundary:
		// Since is the last seq servable contiguously from it, and Floor is where
		// the next intact island starts. See the note on
		// EventsJournalTruncatedError.Since.
		return &storage.EventsJournalTruncatedError{
			Since: rows[gapAt-1].Seq,
			Floor: rows[gapAt].Seq,
			Head:  head,
		}
	}
	return nil
}

// firstEventsSeqGap returns the index of the first row that does not continue
// its predecessor, or 0 when the batch is internally contiguous. Rows arrive in
// ascending seq order, so one linear pass over memory settles it.
func firstEventsSeqGap(rows []storage.EventsJournalRow) int {
	for i := 1; i < len(rows); i++ {
		if rows[i].Seq != rows[i-1].Seq+1 {
			return i
		}
	}
	return 0
}

// readEventsHeadInTx returns the highest seq the counter has ever assigned.
// Prune never touches the counter, so this is the head of the journal's history
// even when every row has been deleted. A missing counter row means no mutation
// has ever been journaled here, which is a head of 0.
func readEventsHeadInTx(ctx context.Context, tx DBTX) (int64, error) {
	var head int64
	err := tx.QueryRowContext(ctx, "SELECT next_seq FROM bd_events_seq WHERE id = 0").Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("journal: read seq counter: %w", err)
	}
	return head, nil
}

func readEventsRowsInTx(ctx context.Context, tx DBTX, since int64, limit int) ([]storage.EventsJournalRow, error) {
	// CAST(ts AS CHAR) normalizes the DATETIME to a stable string across drivers.
	q := `SELECT seq, CAST(ts AS CHAR), op, issue_id, actor, issue_json, dep_json, comment_json
	      FROM bd_events_journal WHERE seq > ? ORDER BY seq ASC`
	if limit > 0 {
		q += " LIMIT " + strconv.Itoa(limit)
	}
	rows, err := tx.QueryContext(ctx, q, since)
	if err != nil {
		return nil, fmt.Errorf("journal: read since %d: %w", since, err)
	}
	defer rows.Close()

	var out []storage.EventsJournalRow
	for rows.Next() {
		var (
			r         storage.EventsJournalRow
			issueJS   sql.NullString
			depJS     sql.NullString
			commentJS sql.NullString
		)
		if err := rows.Scan(&r.Seq, &r.TS, &r.Op, &r.IssueID, &r.Actor, &issueJS, &depJS, &commentJS); err != nil {
			return nil, fmt.Errorf("journal: scan row: %w", err)
		}
		r.TS = normalizeEventsTimestamp(r.TS)
		r.IssueJSON = issueJS.String
		r.DepJSON = depJS.String
		r.CommentJSON = commentJS.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// normalizeEventsTimestamp emits the journal boundary's stable RFC3339Nano UTC
// contract. Dolt/MySQL stringify DATETIME with a space rather than a `T`, and a
// driver may or may not append an offset; a consumer parsing the record needs
// one parsable UTC timestamp regardless of which shape the backend produced.
func normalizeEventsTimestamp(raw string) string {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UTC().Format(time.RFC3339Nano)
		}
	}
	// Preserve an unexpected driver rendering rather than silently changing the
	// event payload. Normal supported backends are covered by the layouts above.
	return raw
}

// ComputeEventsPruneWhere resolves --before and both retention floors into ONE
// exclusive seq bound and returns it as a DELETE predicate (without the "WHERE"
// keyword) plus bind args, or skip=true when nothing may be pruned.
//
// readRowsCeil reports (ceil, found, err) — the highest seq the retain-rows
// floor permits deleting; found is false when the journal holds retainRows or
// fewer rows, which protects the whole journal. readDaysFloor reports
// (floorSeq, found, err) — the lowest seq still inside the age window; found is
// false when every row is older than the cutoff, which constrains nothing.
// Each is invoked only when its floor is enabled (> 0).
//
// This is the ONE place the retain-floor orchestration lives, so the DBTX path
// below and any other plumbing that grows a prune cannot drift on which rows a
// floor protects. Only the substrate-specific scalar reads and the DELETE
// execution differ.
//
// Every floor narrows the bound and none widens it, which is what makes a prune
// unable to remove a row a floor protects — and, because the result is a single
// `seq < bound`, unable to leave a hole above one either.
func ComputeEventsPruneWhere(
	before int64,
	retainDays, retainRows int,
	now time.Time,
	readRowsCeil func() (ceil int64, found bool, err error),
	readDaysFloor func(cutoff time.Time) (floorSeq int64, found bool, err error),
) (where string, args []any, skip bool, err error) {
	bound := before
	if retainRows > 0 {
		ceil, found, cerr := readRowsCeil()
		if cerr != nil {
			return "", nil, false, cerr
		}
		if !found {
			// Fewer rows than the floor retains: the whole journal is inside the
			// retained window.
			return "", nil, true, nil
		}
		// ceil is the highest seq the floor allows deleting, so the exclusive
		// bound is one past it.
		if ceil+1 < bound {
			bound = ceil + 1
		}
	}
	if retainDays > 0 {
		floorSeq, found, derr := readDaysFloor(EventsPruneCutoff(retainDays, now))
		if derr != nil {
			return "", nil, false, derr
		}
		// found == false means no row is young enough, so the age floor protects
		// nothing and the bound is unchanged.
		if found && floorSeq < bound {
			bound = floorSeq
		}
	}
	if bound <= 1 {
		// seq starts at 1, so nothing sits below the bound.
		return "", nil, true, nil
	}
	where, args = BuildEventsPruneWhere(bound)
	return where, args, false, nil
}

// eventsPruneFloorReadersInTx returns the two substrate reads
// ComputeEventsPruneWhere resolves its floors with, bound to one transaction.
// Both prune entry points — the explicit `bd events prune` below and the
// automatic bounding in journal_autoprune.go — take their readers from here, so
// a floor cannot mean one thing when an operator asks for it and another when
// maintenance applies it.
func eventsPruneFloorReadersInTx(ctx context.Context, tx DBTX, retainRows int) (
	func() (int64, bool, error),
	func(time.Time) (int64, bool, error),
) {
	readRowsCeil := func() (int64, bool, error) {
		var ceil int64
		scanErr := tx.QueryRowContext(ctx, EventsPruneRowsCeilQuery(), retainRows).Scan(&ceil)
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, false, nil
		}
		if scanErr != nil {
			return 0, false, fmt.Errorf("journal: compute retain-rows floor: %w", scanErr)
		}
		return ceil, true, nil
	}
	readDaysFloor := func(cutoff time.Time) (int64, bool, error) {
		// MIN over no matching rows yields one NULL row, not ErrNoRows.
		var floorSeq sql.NullInt64
		scanErr := tx.QueryRowContext(ctx, EventsPruneDaysFloorQuery(), cutoff).Scan(&floorSeq)
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, false, nil
		}
		if scanErr != nil {
			return 0, false, fmt.Errorf("journal: compute retain-days floor: %w", scanErr)
		}
		if !floorSeq.Valid {
			return 0, false, nil
		}
		return floorSeq.Int64, true, nil
	}
	return readRowsCeil, readDaysFloor
}

// PruneEventsInTx deletes journal rows with seq below before, honoring the
// retain-days and retain-rows floors (0 disables a floor), and returns the
// number of rows deleted. It runs inside the caller's transaction.
func PruneEventsInTx(ctx context.Context, tx DBTX, before int64, retainDays, retainRows int, now time.Time) (int64, error) {
	readRowsCeil, readDaysFloor := eventsPruneFloorReadersInTx(ctx, tx, retainRows)
	where, args, skip, err := ComputeEventsPruneWhere(before, retainDays, retainRows, now, readRowsCeil, readDaysFloor)
	if err != nil {
		return 0, err
	}
	if skip {
		return 0, nil
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM bd_events_journal WHERE "+where, args...)
	if err != nil {
		return 0, fmt.Errorf("journal: prune below %d: %w", before, err)
	}
	return res.RowsAffected()
}
