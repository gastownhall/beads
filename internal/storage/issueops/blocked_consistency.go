package issueops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// blockedRecomputeGraphTables are the version-controlled tables a full
// is_blocked recompute reads and folds into its issues-only repair commit.
//
// The dolt-ignored wisp tables (wisps, wisp_dependencies) are deliberately
// excluded. They are never staged or committed, so dirty wisp state cannot leak
// into the repair commit; and because Dolt reports every ignored table as
// perpetually "modified" in dolt_status, guarding on them would refuse the
// recompute in any workspace that has ever created a wisp. is_blocked derived
// from wisp state is the same working-set-derived value every local write path
// already produces.
var blockedRecomputeGraphTables = []string{"issues", "dependencies"}

// ErrBlockedRecomputeDirtyGraph reports that a full is_blocked recompute was
// asked to run while its committable graph tables had uncommitted changes.
// Callers wrap it with the offending table names.
var ErrBlockedRecomputeDirtyGraph = errors.New("is_blocked recompute needs a clean working set")

// GuardBlockedRecomputeWorkingSet refuses a full is_blocked recompute when the
// committable graph tables (issues, dependencies) have uncommitted working-set
// changes (bd-6dnrw.37). The recompute derives is_blocked from the current graph
// and stages only `issues`, so running it dirty would either sweep unrelated
// issue edits into the repair commit or commit flags derived from uncommitted
// dependency edits that are not part of the same commit. Run it as the first
// statement inside the recompute's own transaction so it sees exactly the
// working set the recompute will read. Returns a wrapped
// ErrBlockedRecomputeDirtyGraph naming the dirty tables, or nil when clean.
func GuardBlockedRecomputeWorkingSet(ctx context.Context, tx DBTX) error {
	dirty, err := dirtyBlockedRecomputeGraphTables(ctx, tx)
	if err != nil {
		return fmt.Errorf("check working set before is_blocked recompute: %w", err)
	}
	if len(dirty) == 0 {
		return nil
	}
	return fmt.Errorf("%w: commit or discard pending changes to %s first",
		ErrBlockedRecomputeDirtyGraph, strings.Join(dirty, ", "))
}

// dirtyBlockedRecomputeGraphTables returns the sorted subset of the graph tables
// that have uncommitted working-set changes right now.
func dirtyBlockedRecomputeGraphTables(ctx context.Context, tx DBTX) ([]string, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(blockedRecomputeGraphTables)), ",")
	args := make([]any, len(blockedRecomputeGraphTables))
	for i, t := range blockedRecomputeGraphTables {
		args[i] = t
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT DISTINCT table_name FROM dolt_status WHERE table_name IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dirty []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		dirty = append(dirty, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(dirty)
	return dirty, nil
}

// DirtyGraphFingerprint fingerprints the uncommitted graph state that
// GuardBlockedRecomputeWorkingSet refuses to recompute over: the dirty table
// set, plus the identity of every changed row in each dirty table.
//
// It exists so a caller that retries the guard can tell a STUCK working set
// from a BUSY one (wy-wub2s). The guard's error says only "issues is dirty",
// which is equally true of a fleet committing thousands of rows a minute and of
// a table left permanently dirty by constraint violations no writer will ever
// commit. Those need opposite operator responses — wait, versus resolve by hand
// — and only evidence gathered ACROSS observations can separate them: a busy
// working set's fingerprint churns, a stuck one's is byte-identical forever.
//
// Contract, which the caller's escalation logic depends on:
//
//   - "" with a nil error means the graph tables are CLEAN. Never treat that as
//     a fingerprint value; comparing it across observations would read a
//     recovered working set as a stuck one.
//   - a non-empty fingerprint is comparable only against another fingerprint
//     from this same function, and only for equality. Its encoding is not
//     stable across versions and must not be persisted as anything but an
//     opaque token.
//   - an error means the evidence is UNAVAILABLE (an older engine without the
//     dolt_diff table function, a revoked read). A caller must then decline to
//     escalate rather than assume either answer: no evidence is not evidence of
//     being stuck.
//
// The row identities come from dolt_diff('HEAD','WORKING',<table>) rather than
// a whole-database hash (storage.StateHasher / DOLT_HASHOF_DB). The whole-DB
// hash is the wrong instrument here precisely on the topology that motivated
// this: on a shared sql-server every other agent's commits keep it moving, so a
// permanently-dirty table would look like progress forever.
func DirtyGraphFingerprint(ctx context.Context, tx DBTX) (string, error) {
	dirty, err := dirtyBlockedRecomputeGraphTables(ctx, tx)
	if err != nil {
		return "", fmt.Errorf("fingerprint dirty graph: %w", err)
	}
	if len(dirty) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(dirty))
	for _, table := range dirty {
		sig, err := dirtyGraphTableSignature(ctx, tx, table)
		if err != nil {
			return "", fmt.Errorf("fingerprint dirty graph: %s: %w", table, err)
		}
		parts = append(parts, table+":"+sig)
	}
	return strings.Join(parts, " "), nil
}

// dirtyGraphTableSignature hashes the identity of every row the working set
// changes in table, order-independently, so two observations of the same
// pending edits agree.
//
// The commit-metadata columns are excluded: dolt_diff reports the WORKING side
// with a null to_commit and the HEAD side with whatever commit HEAD is, so
// including them would make the fingerprint change every time anyone else
// commits anything — the same false-progress signal the whole-DB hash has.
func dirtyGraphTableSignature(ctx context.Context, tx DBTX, table string) (string, error) {
	if !isBlockedRecomputeGraphTable(table) {
		// Unreachable via dirtyBlockedRecomputeGraphTables (it filters on the
		// same allowlist), and the check is what makes the literal below safe.
		return "", fmt.Errorf("refusing to fingerprint unexpected table %q", table)
	}
	// dolt_diff requires a literal table argument, so it cannot be a bind
	// parameter; table is a member of blockedRecomputeGraphTables, asserted
	// above, never caller- or row-supplied text.
	//nolint:gosec // table is from a fixed allowlist, checked immediately above.
	rows, err := tx.QueryContext(ctx, "SELECT * FROM dolt_diff('HEAD', 'WORKING', '"+table+"')")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}

	var rowSignatures []string
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return "", err
		}
		var b strings.Builder
		for i, column := range columns {
			if isDiffCommitMetadataColumn(column) {
				continue
			}
			b.WriteString(column)
			b.WriteByte('=')
			writeDiffSignatureValue(&b, values[i])
			b.WriteByte(0)
		}
		rowSignatures = append(rowSignatures, b.String())
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sort.Strings(rowSignatures)

	h := sha256.New()
	for _, row := range rowSignatures {
		_, _ = h.Write([]byte(row))
		_, _ = h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isBlockedRecomputeGraphTable(table string) bool {
	for _, t := range blockedRecomputeGraphTables {
		if t == table {
			return true
		}
	}
	return false
}

// IsBlockedRecomputeGraphTable reports whether table is one of the graph
// tables (issues, dependencies) the is_blocked recompute guard protects.
// Exported so a caller correlating another diagnostic (e.g. constraint
// violations) against dirty-guard state uses the same table set rather than
// a second hand-maintained list (wy-mhouc).
func IsBlockedRecomputeGraphTable(table string) bool {
	return isBlockedRecomputeGraphTable(table)
}

func isDiffCommitMetadataColumn(column string) bool {
	switch strings.ToLower(column) {
	case "from_commit", "to_commit", "from_commit_date", "to_commit_date":
		return true
	default:
		return false
	}
}

func writeDiffSignatureValue(b *strings.Builder, v any) {
	switch typed := v.(type) {
	case nil:
		b.WriteString("<nil>")
	case []byte:
		b.Write(typed)
	case string:
		b.WriteString(typed)
	default:
		fmt.Fprintf(b, "%v", typed)
	}
}

// RecomputeAllIsBlockedInTx recomputes the denormalized is_blocked column for
// every issue and wisp in one batched mark/unmark fixpoint and returns the
// number of rows it corrected.
//
// Unlike RecomputeIsBlockedAfterMergeInTx, which is scoped to a pull's diff and
// is skipped when a re-pull merges nothing (head == fromCommit), this is the
// always-available full repair: it does not depend on a merge advancing HEAD,
// so it can recover an is_blocked column left stale by a post-merge recompute
// that failed after its merge committed, or by a conflicted pull the operator
// resolved by hand (bd-6dnrw.37). It is idempotent — on a consistent database
// it changes nothing and returns 0.
func RecomputeAllIsBlockedInTx(ctx context.Context, tx DBTX) (int64, error) {
	issueIDs, err := allIDs(ctx, tx, "issues")
	if err != nil {
		return 0, fmt.Errorf("recompute all is_blocked: list issues: %w", err)
	}
	wispIDs, err := allIDs(ctx, tx, "wisps")
	if err != nil {
		if isTableNotExistError(err) {
			wispIDs = nil
		} else {
			return 0, fmt.Errorf("recompute all is_blocked: list wisps: %w", err)
		}
	}
	return recomputeIsBlockedCounting(ctx, tx, issueIDs, wispIDs)
}

// recomputeIsBlockedCounting is RecomputeIsBlockedInTx with a corrected-row
// count: it sums the rows flipped across every fixpoint pass. A parent flip in
// one pass can cascade to its children in the next, and each correction counts.
//
// Unlike the scoped passes, the full repair runs UNBATCHED semi-join statements
// (markAllBlockedSQL/unmarkAllBlockedSQL): the id lists here cover every row by
// construction, so an IN-batch adds nothing but statement count. This was the
// first home of the decorrelated union — the batched templates' original
// OR-of-correlated-EXISTS shape ran per outer row and made the full repair
// cost ~80+ statement-seconds on fast hardware and >600s on small cloud CPUs
// (bd-t9ypt); the batched templates now use the same union scoped to their
// batch (gastownhall/beads#6288). The id lists are still captured for the
// journal snapshot, and len(wispIDs) doubles as the "wisps table exists and
// has rows" signal.
func recomputeIsBlockedCounting(ctx context.Context, tx DBTX, issueIDs, wispIDs []string) (int64, error) {
	if len(issueIDs) == 0 && len(wispIDs) == 0 {
		return 0, nil
	}
	before, err := captureBlockedJournalSnapshot(ctx, tx, issueIDs, wispIDs)
	if err != nil {
		return 0, err
	}
	var total int64
	for {
		var changed int64
		if len(issueIDs) > 0 {
			n, err := recomputeAllIsBlockedPassInTx(ctx, tx, "issues", "i", "dependencies")
			if err != nil {
				return total, err
			}
			changed += n
		}
		if len(wispIDs) > 0 {
			n, err := recomputeAllIsBlockedPassInTx(ctx, tx, "wisps", "w", "wisp_dependencies")
			if err != nil {
				return total, err
			}
			changed += n
		}
		total += changed
		if changed == 0 {
			if err := recordBlockedJournalChanges(ctx, tx, before, issueIDs, wispIDs); err != nil {
				return total, err
			}
			return total, nil
		}
	}
}

// recomputeAllIsBlockedPassInTx runs one full-table mark+unmark pass for one
// table using the unbatched semi-join statements, returning rows corrected.
// Error wrapping mirrors runMarkUnmarkBatchedInTx verbatim — downstream
// consumers (wh-bridge-sync among them) match on these strings.
func recomputeAllIsBlockedPassInTx(ctx context.Context, tx DBTX, table, alias, depTable string) (int64, error) {
	var changed int64
	res, err := tx.ExecContext(ctx, markAllBlockedSQL(table, alias, depTable))
	if err != nil {
		return changed, fmt.Errorf("recompute is_blocked (mark): %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return changed, fmt.Errorf("recompute is_blocked (mark rows affected): %w", err)
	}
	changed += n

	res, err = tx.ExecContext(ctx, unmarkAllBlockedSQL(table, alias, depTable))
	if err != nil {
		return changed, fmt.Errorf("recompute is_blocked (unmark): %w", err)
	}
	n, err = res.RowsAffected()
	if err != nil {
		return changed, fmt.Errorf("recompute is_blocked (unmark rows affected): %w", err)
	}
	changed += n
	return changed, nil
}

// markAllBlockedSQL is the full-table analog of the batched mark template:
// same predicate, restructured so the engine can execute it as a semi-join.
//
//nolint:gosec // G201: table, alias, and depTable are constants from the only two callers.
func markAllBlockedSQL(table, alias, depTable string) string {
	return fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 1, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.is_blocked = 0
		  AND %[2]s.status <> 'closed' AND %[2]s.status <> 'pinned'
		  AND %[2]s.id IN (%[3]s)
	`, table, alias, shouldBeBlockedIDsUnionSQL(depTable))
}

// unmarkAllBlockedSQL is the full-table analog of the batched unmark
// template. NOT IN is null-hostile — one NULL in the subquery result makes the
// predicate UNKNOWN for every row and the unmark silently stops firing — which
// is why every leg of shouldBeBlockedIDsUnionSQL carries an explicit
// d.issue_id IS NOT NULL guard.
//
//nolint:gosec // G201: table, alias, and depTable are constants from the only two callers.
func unmarkAllBlockedSQL(table, alias, depTable string) string {
	return fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 0, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.is_blocked = 1
		  AND ( %[2]s.status = 'closed' OR %[2]s.status = 'pinned'
		        OR %[2]s.id NOT IN (%[3]s) )
	`, table, alias, shouldBeBlockedIDsUnionSQL(depTable))
}

// shouldBeBlockedIDsUnionSQL selects, UNCORRELATED with any outer row, every
// depTable issue_id that currently has a reason to be blocked: one UNION leg
// per reason — an open blocks/conditional-blocks target (issue or wisp), a
// blocked parent-child parent (issue or wisp), a blocking waits-for gate.
// Semantically, `<alias>.id IN (this select)` ≡ the disjunction of those
// reasons correlated on d.issue_id = <alias>.id, which is what the batched
// mark/unmark templates in blocked_state.go once spelled out as five
// correlated EXISTS — but the engine evaluates this form once as hash
// lookups instead of per outer row (bd-t9ypt). Full repair, doctor count and
// the batched templates (scoped, see below) now share this one definition.
//
//nolint:gosec // G201: depTable is constant; waitsForGateBlockedSQL is a constant template.
func shouldBeBlockedIDsUnionSQL(depTable string) string {
	return shouldBeBlockedIDsUnionScopedSQL(depTable, "")
}

// shouldBeBlockedIDsUnionScopedSQL is shouldBeBlockedIDsUnionSQL with an
// extra predicate on the dependency row appended to every leg's WHERE — "" for
// the whole table (full repair, doctor count), batchScopeSQL for the batched
// mark/unmark templates in blocked_state.go, which confine each leg to
// d.issue_id IN (batch) so the union stays index-sized per statement. scope is
// spliced verbatim: a %s inside it survives this Sprintf for the batch runner.
//
// The waits-for leg wraps its dependency rows in a DISTINCT derived table
// BEFORE the gate predicate (waitsForGateBlockedSQL, six correlated EXISTS
// over the parent-child children and the spawner) is applied, so the engine
// narrows to the leg's few waits-for rows first and evaluates the gate only
// per surviving row. Written flat, dolt 2.1.8's planner pushed the gate's
// correlated subqueries ahead of the type filter and re-ran them against the
// whole overlay for every candidate row — over a 19k-row uncommitted working
// set the flat leg took 436 s while returning zero rows; behind the derived
// table, 0.01 s (gastownhall/beads#6288, wy-pyrus4). The DISTINCT is
// semantically free (the leg feeds a UNION, and the gate depends only on the
// projected columns) and is what makes the barrier structural: a plain
// single-table derived table is the mergeable shape MySQL-dialect planners
// fold back into the outer query; a DISTINCT one is materialized.
//
//nolint:gosec // G201: depTable and scope are constants; waitsForGateBlockedSQL is a constant template.
func shouldBeBlockedIDsUnionScopedSQL(depTable, scope string) string {
	return fmt.Sprintf(`
		SELECT d.issue_id FROM %[1]s d
		JOIN issues t ON t.id = d.depends_on_issue_id
		WHERE d.issue_id IS NOT NULL %[3]s
		  AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		  AND t.status <> 'closed' AND t.status <> 'pinned'
		UNION
		SELECT d.issue_id FROM %[1]s d
		JOIN wisps t ON t.id = d.depends_on_wisp_id
		WHERE d.issue_id IS NOT NULL %[3]s
		  AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		  AND t.status <> 'closed' AND t.status <> 'pinned'
		UNION
		SELECT d.issue_id FROM %[1]s d
		JOIN issues p ON p.id = d.depends_on_issue_id
		WHERE d.issue_id IS NOT NULL %[3]s
		  AND d.type = 'parent-child'
		  AND p.is_blocked = 1
		UNION
		SELECT d.issue_id FROM %[1]s d
		JOIN wisps p ON p.id = d.depends_on_wisp_id
		WHERE d.issue_id IS NOT NULL %[3]s
		  AND d.type = 'parent-child'
		  AND p.is_blocked = 1
		UNION
		SELECT d.issue_id FROM (
		  SELECT DISTINCT d.issue_id, d.depends_on_issue_id, d.depends_on_wisp_id, d.metadata
		  FROM %[1]s d
		  WHERE d.issue_id IS NOT NULL %[3]s
		    AND d.type = 'waits-for'
		) d
		WHERE (%[2]s)
	`, depTable, waitsForGateBlockedSQL, scope)
}

// CountIsBlockedInconsistenciesInTx reports how many issue and wisp rows carry a
// stale is_blocked flag — rows a full recompute would flip. It is the read-only
// detection behind the bd doctor "Blocked State" check (bd-6dnrw.37); the
// repair is RecomputeAllIsBlockedInTx.
//
// The two share no SQL but are pinned together by the blocked-consistency
// lockstep test: a converged database counts 0, and any row this counts is one
// a recompute pass changes. The count is a single-pass lower bound — a
// corrupted parent's children are only counted on the pass after the parent is
// fixed — which is exactly what a "needs repair?" check wants: nonzero means run
// --fix, zero means consistent.
func CountIsBlockedInconsistenciesInTx(ctx context.Context, tx DBTX) (int64, error) {
	var total int64

	n, err := countRows(ctx, tx, countStaleIsBlockedSQL("issues", "i", "dependencies"))
	if err != nil {
		return 0, fmt.Errorf("count stale is_blocked issues: %w", err)
	}
	total += n

	n, err = countRows(ctx, tx, countStaleIsBlockedSQL("wisps", "w", "wisp_dependencies"))
	if err != nil {
		if isTableNotExistError(err) {
			return total, nil
		}
		return 0, fmt.Errorf("count stale is_blocked wisps: %w", err)
	}
	total += n

	return total, nil
}

// countStaleIsBlockedSQL builds a COUNT(*) of rows whose stored is_blocked
// disagrees with the dependency graph for one table (issues or wisps). The two
// OR branches mirror the mark and unmark UPDATE shapes exactly:
//
//   - mark-eligible:   is_blocked = 0 on an open row that should be blocked.
//   - unmark-eligible: is_blocked = 1 on a row that should not be blocked
//     (closed/pinned, or its id absent from the should-be-blocked union — the
//     set-membership form of De Morgan over the shouldBeBlocked disjunction).
//
// is_blocked is 0 or 1, so the branches are mutually exclusive and the count is
// their sum. Built on the uncorrelated shouldBeBlockedIDsUnionSQL for the same
// reason as the full repair (bd-t9ypt): the correlated disjunction form ran per
// outer row and cost whole-table scans per branch. table/alias/depTable are
// hardcoded constants supplied by the only two callers.
//
//nolint:gosec // G201: table, alias, and depTable are constant; only the constant gate SQL is interpolated.
func countStaleIsBlockedSQL(table, alias, depTable string) string {
	union := shouldBeBlockedIDsUnionSQL(depTable)
	return fmt.Sprintf(`
		SELECT COUNT(*) FROM %[1]s %[2]s
		WHERE
		  ( %[2]s.is_blocked = 0
		    AND %[2]s.status <> 'closed' AND %[2]s.status <> 'pinned'
		    AND %[2]s.id IN (%[3]s) )
		  OR
		  ( %[2]s.is_blocked = 1
		    AND ( %[2]s.status = 'closed' OR %[2]s.status = 'pinned'
		          OR %[2]s.id NOT IN (%[3]s) ) )
	`, table, alias, union)
}

// countRows runs a single COUNT(*) query and returns the scalar.
func countRows(ctx context.Context, tx DBTX, query string) (int64, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}
