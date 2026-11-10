package versioncontrolops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Domain-aware auto-merge (federation ask #1, the flagship).
//
// Dolt merges disjoint writes cleanly, but beads stamps `issues.updated_at` on
// EVERY mutation, so any two edits to the same issue on two replicas between
// syncs collide on that cell — even when the semantic fields are disjoint
// (machine A adds a comment, machine B adds a label, and the issues row
// conflicts on nothing but the timestamp both bumped). The observed conflict
// rate is therefore far higher than the semantic-conflict rate, and the
// original row-level LWW resolver could only take the safe half of it: it
// declined whenever BOTH sides had moved updated_at past the merge base,
// because taking one side's whole row would silently drop the other side's
// field-level edits.
//
// This file replaces that with a FIELD-level three-way merge, which encodes
// beads' actual write semantics:
//
//   - a column only one side changed relative to the merge base keeps that
//     side's value — no edit is dropped, whatever the timestamps say;
//   - a column both sides changed to DIFFERENT values is the only genuine
//     conflict, and it is settled last-write-wins by `updated_at` (the ask's
//     rule for status/assignee/updated_at, applied uniformly);
//   - `updated_at` itself therefore merges to max(ours, theirs), since
//     whichever side is newer either wins the cell outright (both moved) or is
//     the only side that moved it.
//
// Two carve-outs keep per-cell independence from inventing states bd's own
// write paths cannot produce (see issuesCloseGroup and issuesNonScalarColumns
// below): the close columns move atomically, and a contested `notes` or
// `metadata` declines rather than letting LWW delete an append or a JSON key.
//
// A row is left for the operator when it is not modify/modify (add/add,
// delete/modify), when a genuinely conflicting cell cannot be settled because
// the two sides' `updated_at` values are equal or unparseable — the ambiguity
// LWW has no answer for — or when one of those carve-outs applies.
//
// The companion tables merge by the semantics the ask names:
//
//   - labels: SET-UNION. `labels` is all key columns (issue_id, label), so
//     two sides adding DIFFERENT labels are disjoint rows dolt already unions,
//     and a conflict can only mean the same (issue_id, label) on both sides —
//     identical data, resolvable by keeping it.
//   - comments/events: APPEND-ONLY UNION. Rows are insert-only and keyed by a
//     per-machine-unique id, so creation is disjoint and dolt unions it; a
//     same-id conflict whose columns agree is the same append on both sides
//     and is likewise resolvable by keeping it.
//
// For all three, a conflict where the row is missing on one side (a deletion
// racing an insert — compaction, or a label removal) or where the columns of a
// supposedly immutable row disagree is NOT unioned: it goes to the operator,
// because both "presence wins" and "deletion wins" would silently discard a
// real intent.

// unionConflictKeyColumns lists the primary-key columns of the tables merged by
// union semantics. The key columns are what identify a conflicted row for the
// dolt_conflicts_<table> delete that signals resolution.
var unionConflictKeyColumns = map[string][]string{
	"labels":   {"issue_id", "label"},
	"comments": {"id"},
	"events":   {"id"},
}

// issuesKeyColumn is the issues-table primary key, used both to identify a
// conflicted row and to exclude the key from the merge write-back.
const issuesKeyColumn = "id"

// issuesRowMerge is the field-level merge decision for one conflicted issues
// row: the columns whose merged value differs from OUR working-set value, and
// the raw values to write.
type issuesRowMerge struct {
	ourKey  any
	columns []string
	values  []any
	// lww names the cells both sides changed differently, which were settled
	// by timestamp rather than merged. They are the only cells where one
	// side's edit is superseded, so the resolver names them on stderr — the
	// same courtesy the config path pays for an otherwise-undiagnosable
	// supersession.
	lww []string
}

// loadConflictRows reads every live conflict row of table in raw scanned form.
func loadConflictRows(ctx context.Context, db DBConn, table string) ([]rawConflictRow, error) {
	if err := ValidateConflictTable(table); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT * FROM `dolt_conflicts_"+table+"`") //nolint:gosec // table validated as an identifier above
	if err != nil {
		return nil, fmt.Errorf("query conflicts for table %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("conflict columns for table %s: %w", table, err)
	}
	var out []rawConflictRow
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan conflict row for table %s: %w", table, err)
		}
		out = append(out, rawConflictRow{cols: cols, vals: vals})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conflicts for table %s: %w", table, err)
	}
	return out, nil
}

// duplicateConflictKey reports the first our-side key held by more than one
// live conflict row. Both resolvers settle a row by deleting its conflict BY
// KEY, so two rows sharing one key would both be cleared by the first delete
// and make the second iteration abort on "no conflict row deleted" — a message
// about the wrong thing entirely, after a row was resolved without ever being
// merged. loadConflictRow refuses the same shape on the operator's single-row
// path (conflicts.go); the auto-merge pre-screens instead DECLINE on it, which
// is this file's idiom and what lets the caller still build the
// MergeConflictsError that tells an operator which tables need them.
//
// Rows whose our-side key is absent are skipped: a delete/modify conflict NULLs
// our whole side, such a row is never the target of a keyed delete (the safety
// checks decline it first), and treating several of them as one repeated key
// would decline merges that are perfectly settleable.
//
// Keys are compared by their rendered bytes, which is stricter than the
// collation the DELETE itself matches under: a case-insensitive collation could
// still let two rows the guard reads as distinct be deleted together. Dolt's
// default is a binary collation and the beads schema pins no other, so the two
// agree today.
func duplicateConflictKey(keyCols []string, rows []rawConflictRow) (string, bool) {
	if len(keyCols) == 0 {
		return "", false
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		parts := make([]string, 0, len(keyCols))
		for _, k := range keyCols {
			v, has := row.value("our", k)
			if !has {
				break
			}
			s := formatConflictValue(v)
			if s == nil {
				break
			}
			parts = append(parts, *s)
		}
		if len(parts) != len(keyCols) {
			continue
		}
		key := strings.Join(parts, "\x00")
		if seen[key] {
			return strings.Join(parts, "/"), true
		}
		seen[key] = true
	}
	return "", false
}

// declineDuplicateConflictRows reports (and explains) a duplicate-key decline.
// The reason is otherwise undiagnosable: the caller only learns that the table
// was not auto-merged, the same courtesy the resolver pays a superseded cell.
func declineDuplicateConflictRows(table string, keyCols []string, rows []rawConflictRow) bool {
	dup, ok := duplicateConflictKey(keyCols, rows)
	if !ok {
		return false
	}
	fmt.Fprintf(os.Stderr,
		"Notice: not auto-merging %s; several live conflict rows share the key %s, which must be resolved by hand\n",
		table, dup)
	return true
}

// dataColumns returns the row's data column names (conflict metadata and the
// named excluded columns dropped), in conflict-table order and de-duplicated.
// A column is only reported when the row actually carries a value for it on
// every side that matters; callers read the sides they need with value().
func (r rawConflictRow) dataColumns(exclude ...string) []string {
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[e] = true
	}
	seen := make(map[string]bool, len(r.cols))
	var out []string
	for _, c := range r.cols {
		side, field, ok := splitConflictColumn(c)
		if !ok || side != "our" || conflictMetaSuffixes[field] || skip[field] || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

// sidesPresent reports whether the row exists on the base, our, and their
// sides, judged by the key columns (a dolt conflict row NULLs out every column
// of a side that has no row).
func (r rawConflictRow) sidesPresent(keyCols []string) (base, ours, theirs bool) {
	present := func(side string) bool {
		for _, k := range keyCols {
			v, ok := r.value(side, k)
			if !ok || v == nil {
				return false
			}
		}
		return true
	}
	return present("base"), present("our"), present("their")
}

// conflictCellsEqual compares two raw conflict cell values through the same
// normalization the presentation layer uses, so a driver returning []byte for
// one side and string for the other does not read as a difference. SQL NULL is
// distinct from the empty string.
func conflictCellsEqual(a, b any) bool {
	x, y := formatConflictValue(a), formatConflictValue(b)
	if x == nil || y == nil {
		return x == nil && y == nil
	}
	return *x == *y
}

// conflictTimestampLayouts are the shapes an `updated_at` cell can arrive in:
// RFC3339 (what formatConflictValue renders a driver-parsed time.Time as) and
// the two MySQL DATETIME text forms (drivers configured without parseTime).
var conflictTimestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05",
}

// parseConflictTimestamp parses a raw conflict cell as a timestamp. ok is
// false for NULL or any unrecognized shape — an unparseable timestamp must
// make LWW decline, never guess.
func parseConflictTimestamp(v any) (time.Time, bool) {
	if t, isTime := v.(time.Time); isTime {
		return t.UTC(), true
	}
	s := formatConflictValue(v)
	if s == nil || *s == "" {
		return time.Time{}, false
	}
	for _, layout := range conflictTimestampLayouts {
		if t, err := time.Parse(layout, *s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// issuesCloseGroup are the columns beads always writes together: `bd close`
// sets status/closed_at/close_reason/closed_by_session in one statement and
// `bd reopen` clears them in one statement (issueops/close.go, reopen.go), and
// types.Issue.Validate enforces the biconditional "closed iff closed_at". Cell
// independence is wrong for them: our close and their status change would
// otherwise merge into `status='in_progress' AND closed_at=<t>`, a row no
// write path can produce and validation rejects. When any of them is
// contested the whole group is settled from the LWW winner, atomically.
var issuesCloseGroup = []string{"status", "closed_at", "close_reason", "closed_by_session"}

// issuesNonScalarColumns are columns whose contents are structurally merged by
// bd's own write paths, so per-cell LWW would silently destroy one side's
// work: `notes` is append-only (`bd note` = --append-notes) and `metadata` is
// a JSON object mutated key-wise. Comments and events get append-only union
// treatment for exactly this reason; these two live inside the issues row
// where cell-level merge cannot express a union, so a genuinely contested one
// DECLINES to the operator rather than dropping an append.
var issuesNonScalarColumns = map[string]bool{
	"notes":    true,
	"metadata": true,
}

// cellVerdict classifies one cell against the merge base.
type cellVerdict int

const (
	cellAgree      cellVerdict = iota // both sides hold the same value
	cellOursOnly                      // only we changed it
	cellTheirsOnly                    // only they changed it
	cellContested                     // both sides changed it, differently
)

// classifyCell compares one column's three sides. ok is false when the
// conflict row cannot be classified at all (a side's column is missing).
func classifyCell(row rawConflictRow, col string) (cellVerdict, any, bool) {
	ourVal, ourHas := row.value("our", col)
	theirVal, theirHas := row.value("their", col)
	if !ourHas || !theirHas {
		return 0, nil, false
	}
	if conflictCellsEqual(ourVal, theirVal) {
		return cellAgree, theirVal, true
	}
	baseVal, baseHas := row.value("base", col)
	if !baseHas {
		return 0, nil, false
	}
	switch {
	case conflictCellsEqual(theirVal, baseVal):
		return cellOursOnly, theirVal, true
	case conflictCellsEqual(ourVal, baseVal):
		return cellTheirsOnly, theirVal, true
	default:
		return cellContested, theirVal, true
	}
}

// mergeIssuesConflictRow computes the field-level three-way merge of one
// conflicted issues row. ok is false when the row must be left for the
// operator: not modify/modify, a contested cell whose LWW tiebreak is
// ambiguous (equal or unparseable updated_at), or a contested cell whose
// contents LWW cannot merge without loss (issuesNonScalarColumns).
//
// It is pure, so every merge rule is unit-testable without a database.
func mergeIssuesConflictRow(row rawConflictRow) (issuesRowMerge, bool) {
	baseOK, ourOK, theirOK := row.sidesPresent([]string{issuesKeyColumn})
	if !baseOK || !ourOK || !theirOK {
		// add/add (no base row) or delete/modify (one side removed it):
		// neither has a field-level answer.
		return issuesRowMerge{}, false
	}
	ourKey, _ := row.value("our", issuesKeyColumn)

	// row_lock is the opaque optimistic-concurrency token every
	// status/ownership write remints (freshRowLock in issueops/lease.go), so
	// whenever both sides edited the row it is contested by construction — and
	// it must never be settled by LWW like a data cell: keeping either side's
	// pre-merge token would let a stale ExpectedVersion (types.Issue.RowVersion)
	// win a CAS against a row whose content the merge just changed. It is
	// excluded from classification here and handled after the merge plan is
	// built: a settled row that differs from ours gets a token fresh relative
	// to BOTH parents (see below). Excluding it also keeps a token-only
	// divergence — two replicas independently settling the same merge mint
	// different random tokens — from declining the next sync's conflict, since
	// LWW could never break that tie (both sides carry the same updated_at).
	ourLock := mustValue(row, "our", "row_lock")
	theirLock := mustValue(row, "their", "row_lock")

	ourUpdatedRaw := mustValue(row, "our", "updated_at")
	theirUpdatedRaw := mustValue(row, "their", "updated_at")
	ourUpdated, ourTimeOK := parseConflictTimestamp(ourUpdatedRaw)
	theirUpdated, theirTimeOK := parseConflictTimestamp(theirUpdatedRaw)
	// theirsWin reports the LWW winner, and whether the tiebreak can be made
	// at all. It is only consulted for a genuinely contested cell.
	lwwUsable := ourTimeOK && theirTimeOK && !ourUpdated.Equal(theirUpdated)
	theirsWinLWW := lwwUsable && theirUpdated.After(ourUpdated)

	// Classify every data column once; the group rules below read the map.
	// row_lock is deliberately not a data column here (see above).
	cols := row.dataColumns(issuesKeyColumn, "row_lock")
	verdicts := make(map[string]cellVerdict, len(cols))
	theirVals := make(map[string]any, len(cols))
	for _, col := range cols {
		v, theirVal, ok := classifyCell(row, col)
		if !ok {
			// A column dolt reports for our side but not for base/theirs. The
			// enumeration is our_*-driven, so this catches a their_*/base_*
			// column going missing, not the reverse; dolt builds the conflict
			// table from the merged schema, so all three sides are expected.
			return issuesRowMerge{}, false
		}
		verdicts[col] = v
		theirVals[col] = theirVal
	}

	// A contested column bd merges structurally has no cell-level answer.
	for col, v := range verdicts {
		if v == cellContested && issuesNonScalarColumns[col] {
			return issuesRowMerge{}, false
		}
	}

	// The close group moves atomically: if ANY member is contested, the LWW
	// winner supplies all of them, overriding the per-cell rule that would
	// otherwise keep our closed_at beside their status.
	closeGroupContested := false
	inCloseGroup := make(map[string]bool, len(issuesCloseGroup))
	for _, col := range issuesCloseGroup {
		inCloseGroup[col] = true
		if verdicts[col] == cellContested {
			closeGroupContested = true
		}
	}

	merge := issuesRowMerge{ourKey: ourKey}
	takeTheirs := func(col string) {
		merge.columns = append(merge.columns, col)
		merge.values = append(merge.values, theirVals[col])
	}
	for _, col := range cols {
		v := verdicts[col]
		if v == cellAgree {
			continue
		}
		if closeGroupContested && inCloseGroup[col] {
			// Atomic group: one LWW decision for all of its members.
			if !lwwUsable {
				return issuesRowMerge{}, false
			}
			merge.lww = append(merge.lww, col)
			if theirsWinLWW {
				// Their side wins the group: take their value for every
				// member, including the ones only WE changed (that is what
				// makes the group atomic).
				takeTheirs(col)
			}
			// Ours wins the group: our values already stand, including the
			// members only THEY changed — they are part of their losing close
			// state, not an independent edit.
			continue
		}
		switch v {
		case cellOursOnly:
			// Our working-set value already stands.
			continue
		case cellTheirsOnly:
			// Only they changed it: take their edit. This is the case
			// row-level LWW used to lose.
			takeTheirs(col)
		case cellContested:
			// Both sides changed the same cell to different values — the only
			// genuine conflict. Settle it last-write-wins by updated_at.
			if !lwwUsable {
				return issuesRowMerge{}, false
			}
			merge.lww = append(merge.lww, col)
			if theirsWinLWW {
				takeTheirs(col)
			}
		}
	}

	// updated_at MUST be written explicitly whenever anything else is, because
	// issues.updated_at is DATETIME ... ON UPDATE CURRENT_TIMESTAMP: an UPDATE
	// that omits it silently restamps the row with this clone's wall clock,
	// which (a) breaks the max(ours, theirs) contract, (b) makes the same
	// merge produce different bytes on each replica so the next sync
	// re-conflicts, and (c) can make issueops' `VALUES(updated_at) >
	// issues.updated_at` import stale-guard reject a genuinely newer edit.
	// The codebase's standing rule for this column (issueops/blocked_state.go)
	// is the same: assign it explicitly to suppress the ON UPDATE clause.
	// When nothing else is written no UPDATE runs at all, and our row already
	// holds the max (we could only have won every contest by being newer).
	if len(merge.columns) > 0 {
		merged := ourUpdatedRaw
		if theirTimeOK && (!ourTimeOK || theirUpdated.After(ourUpdated)) {
			merged = theirUpdatedRaw
		}
		merge.setColumn("updated_at", merged)

		// The settled row differs from BOTH parents (their absorbed edits make
		// it differ from ours; our surviving cells make it differ from theirs),
		// so it must carry a row_lock distinct from both parents' tokens — an
		// ExpectedVersion CAS read against either pre-merge row must lose
		// against the merged row (gastownhall/beads#4682's hazard: an
		// LWW-settled merge that keeps one side's old token lets a stale CAS
		// win). When nothing is written the settled row IS our row, byte for
		// byte, so our token still vouches for exactly the content a CAS
		// holder read and it is correct — and convergence-critical (see the
		// exclusion comment above) — to leave it alone.
		//
		// This reminting on ANY settled write (not just a status/assignee/
		// started_at change) is wider than RowVersion's documented contract
		// (storage.go's CloseIssueOptions.ExpectedVersion doc, ~319-325):
		// RowVersion is meant to track lifecycle/ownership writes only, and a
		// merge that only touched e.g. title or description is not one of
		// those. The widening is accepted as fail-safe rather than tightened
		// to the lifecycle columns: the failure mode is a spurious
		// ExpectedVersion mismatch on a non-lifecycle-only merge, and a caller
		// that hits it simply re-reads and retries (types.Issue.RowVersion),
		// never a missed conflict. Narrowing this to match the documented
		// contract exactly is a separate, deliberate change, not implied by
		// this fix.
		//
		// Gated on the column actually existing in the conflict table: a
		// pre-0054 schema (before row_lock was added) has no
		// our_row_lock/their_row_lock column, and unconditionally naming
		// "row_lock" in the write-back plan there would turn a clean
		// auto-merge into a hard "unknown column" pull failure. Degrade to
		// the pre-row_lock behavior (no row_lock write) when the column is
		// absent.
		if _, hasRowLock := row.value("our", "row_lock"); hasRowLock {
			merge.setColumn("row_lock", freshRowLockDistinctFrom(ourLock, theirLock))
		}
	}
	return merge, true
}

// freshRowLockDistinctFrom mints a new row_lock token that differs from both
// parents' tokens. freshRowLock is crypto-random over int64, so a collision is
// already a ~2⁻⁶³ event; rerolling makes the "the settled row's token matches
// neither pre-merge row" guarantee exact rather than probabilistic. The raw
// conflict-table values are compared through the same normalization the merge
// rules use, so a driver handing back []byte("123") for one side and int64 for
// the candidate still compares as equal.
func freshRowLockDistinctFrom(ourLock, theirLock any) int64 {
	for {
		candidate := issueops.FreshRowLock()
		if !conflictCellsEqual(candidate, ourLock) && !conflictCellsEqual(candidate, theirLock) {
			return candidate
		}
	}
}

// setColumn adds or overwrites a column in the write-back plan.
func (m *issuesRowMerge) setColumn(col string, val any) {
	for i, c := range m.columns {
		if c == col {
			m.values[i] = val
			return
		}
	}
	m.columns = append(m.columns, col)
	m.values = append(m.values, val)
}

// mustValue reads a side's column, returning nil when the conflict table has
// no such column (the caller's rules then treat it as absent/unparseable).
func mustValue(row rawConflictRow, side, col string) any {
	v, _ := row.value(side, col)
	return v
}

// issuesConflictsAreFieldMergeable reports whether every live issues conflict
// can be settled by mergeIssuesConflictRow, and returns the merge plan so the
// resolution pass does not recompute it.
func issuesConflictsAreFieldMergeable(ctx context.Context, db DBConn) ([]issuesRowMerge, bool, error) {
	rows, err := loadConflictRows(ctx, db, "issues")
	if err != nil {
		return nil, false, err
	}
	if declineDuplicateConflictRows("issues", []string{issuesKeyColumn}, rows) {
		return nil, false, nil
	}
	plan := make([]issuesRowMerge, 0, len(rows))
	for _, row := range rows {
		merged, ok := mergeIssuesConflictRow(row)
		if !ok {
			return nil, false, nil
		}
		plan = append(plan, merged)
	}
	return plan, true, nil
}

// resolveIssuesFieldMerge applies a plan from issuesConflictsAreFieldMergeable.
//
// DOLT_CONFLICTS_RESOLVE is table-level (--ours/--theirs), which cannot express
// a per-cell merge, so this uses dolt's manual-resolution path: write the
// merged values over our working-set row, then DELETE the conflict row — the
// delete is what tells dolt the row is settled, so it must come last. A row
// whose merge equals our side needs no write at all.
func resolveIssuesFieldMerge(ctx context.Context, db DBConn, plan []issuesRowMerge) error {
	for _, m := range plan {
		if len(m.lww) > 0 {
			// Both sides edited these cells since the merge base, so one
			// side's value was superseded by timestamp. That supersession is
			// otherwise undiagnosable once the conflict row is gone — the same
			// reason the config path names its resolved keys.
			fmt.Fprintf(os.Stderr,
				"Notice: auto-merged issue %v; %s settled last-write-wins (the older side's edit was superseded)\n",
				m.ourKey, strings.Join(m.lww, ", "))
		}
		if m.ourKey == nil {
			return fmt.Errorf("unexpected conflict row with no issue id (safety check bypassed)")
		}
		if len(m.columns) > 0 {
			sets := make([]string, len(m.columns))
			args := make([]any, 0, len(m.columns)+1)
			for i, col := range m.columns {
				// MySQL cannot bind an identifier and a peer's schema merge can
				// extend the conflict table's columns, so gate every name the
				// same way the table name is gated.
				if err := ValidateConflictTable(col); err != nil {
					return fmt.Errorf("refusing to write unexpected column %q of issues: %w", col, err)
				}
				sets[i] = fmt.Sprintf("`%s` = ?", col)
				args = append(args, m.values[i])
			}
			args = append(args, m.ourKey)
			stmt := fmt.Sprintf("UPDATE `issues` SET %s WHERE `%s` = ?", strings.Join(sets, ", "), issuesKeyColumn) //nolint:gosec // identifiers validated above
			res, err := db.ExecContext(ctx, stmt, args...)
			if err != nil {
				return fmt.Errorf("apply merged values for issue %v: %w", m.ourKey, err)
			}
			// Zero rows would mean the row we planned against is gone —
			// another session deleted it between the read and the write, and
			// clearing the conflict now would discard their side undetectably.
			// But RowsAffected is rows CHANGED, not rows MATCHED: the DSN does
			// not set clientFoundRows (doltutil/dsn.go), so a write the backend
			// normalizes to the bytes already stored also reports zero. Only a
			// follow-up existence check can tell "vanished" from "no-op".
			if n, err := res.RowsAffected(); err != nil || n == 0 {
				present, err := conflictTargetStillPresent(ctx, db, "issues", issuesKeyColumn, m.ourKey)
				if err != nil {
					return fmt.Errorf("confirm issue %v still exists after writing merged values: %w", m.ourKey, err)
				}
				if !present {
					return fmt.Errorf("merged values for issue %v matched no row (was it deleted concurrently?); conflict left unresolved", m.ourKey)
				}
			}
		}
		res, err := db.ExecContext(ctx,
			"DELETE FROM dolt_conflicts_issues WHERE our_"+issuesKeyColumn+" = ?", m.ourKey)
		if err != nil {
			return fmt.Errorf("clear conflict for issue %v: %w", m.ourKey, err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("conflict for issue %v was not cleared (no conflict row deleted)", m.ourKey)
		}
	}
	return nil
}

// unionConflictsAreSafe reports whether every live conflict of a union-merged
// table (labels, comments, events) is the same row on both sides with matching
// columns — the only class where "union" has an unambiguous answer. A row
// missing on one side (a deletion racing an insert) or diverging columns in a
// supposedly immutable row goes to the operator.
func unionConflictsAreSafe(ctx context.Context, db DBConn, table string) ([]unionRowKey, bool, error) {
	keyCols, ok := unionConflictKeyColumns[table]
	if !ok {
		return nil, false, fmt.Errorf("table %s is not union-mergeable", table)
	}
	rows, err := loadConflictRows(ctx, db, table)
	if err != nil {
		return nil, false, err
	}
	if declineDuplicateConflictRows(table, keyCols, rows) {
		return nil, false, nil
	}
	plan := make([]unionRowKey, 0, len(rows))
	for _, row := range rows {
		key, ok := unionRowIsSafe(row, keyCols)
		if !ok {
			return nil, false, nil
		}
		plan = append(plan, key)
	}
	return plan, true, nil
}

// unionRowIsSafe decides one union-table conflict row and returns its key.
// Pure, so the safety property is unit-testable without a database: the row
// must exist on BOTH sides and every column must agree, which is what makes
// "union" unambiguous. A row missing on one side (a deletion racing an insert)
// or a supposedly immutable row whose columns diverge is refused.
func unionRowIsSafe(row rawConflictRow, keyCols []string) (unionRowKey, bool) {
	_, ourOK, theirOK := row.sidesPresent(keyCols)
	if !ourOK || !theirOK {
		return unionRowKey{}, false
	}
	for _, col := range row.dataColumns() {
		ourVal, ourHas := row.value("our", col)
		theirVal, theirHas := row.value("their", col)
		if !ourHas || !theirHas || !conflictCellsEqual(ourVal, theirVal) {
			return unionRowKey{}, false
		}
	}
	key := unionRowKey{columns: keyCols}
	for _, k := range keyCols {
		v, _ := row.value("our", k)
		key.values = append(key.values, v)
	}
	return key, true
}

// unionRowKey is one validated conflict row's primary key, carried from the
// check pass to the resolution pass so the delete is bound to the rows that
// were actually validated rather than to whatever a second query returns.
type unionRowKey struct {
	columns []string
	values  []any
}

// resolveUnionConflicts settles the conflicts unionConflictsAreSafe validated.
// Both sides hold the same row, so our working set already carries the union:
// deleting the conflict row is the whole resolution.
func resolveUnionConflicts(ctx context.Context, db DBConn, table string, plan []unionRowKey) error {
	if _, ok := unionConflictKeyColumns[table]; !ok {
		return fmt.Errorf("table %s is not union-mergeable", table)
	}
	for _, row := range plan {
		preds := make([]string, 0, len(row.columns))
		args := make([]any, 0, len(row.columns))
		for i, k := range row.columns {
			v := row.values[i]
			if v == nil {
				return fmt.Errorf("unexpected %s conflict row with no our_%s (safety check bypassed)", table, k)
			}
			preds = append(preds, "`our_"+k+"` = ?")
			args = append(args, v)
		}
		//nolint:gosec // table and key columns come from the unionConflictKeyColumns allowlist.
		stmt := "DELETE FROM `dolt_conflicts_" + table + "` WHERE " + strings.Join(preds, " AND ")
		res, err := db.ExecContext(ctx, stmt, args...)
		if err != nil {
			return fmt.Errorf("clear %s conflict: %w", table, err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("a %s conflict was not cleared (no conflict row deleted)", table)
		}
	}
	return nil
}
