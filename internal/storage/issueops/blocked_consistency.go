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
//
// The rows this database cannot decide (undecidedRows) are held out of the
// fixpoint and settled after it, by markUndecidedAllBlockedSQL: see
// undecidedRows for why a mark the fixpoint made on the way would otherwise
// stick. Whether any dependency row dangles is asked once, up front, because
// nothing in the repair writes a dependency row or a wisp row.
func recomputeIsBlockedCounting(ctx context.Context, tx DBTX, issueIDs, wispIDs []string) (int64, error) {
	if len(issueIDs) == 0 && len(wispIDs) == 0 {
		return 0, nil
	}
	before, err := captureBlockedJournalSnapshot(ctx, tx, issueIDs, wispIDs)
	if err != nil {
		return 0, err
	}
	holders, err := danglingWispHoldersInTx(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("recompute is_blocked (dangling wisp edges): %w", err)
	}
	var total int64
	for {
		var changed int64
		// One exogeneity read per PASS, shared by every statement in it. It is
		// a property of the PARENT, so the same answer serves both tables.
		explained, err := subtreeExplainedParentsInTx(ctx, tx)
		if err != nil {
			return total, err
		}
		issueLeft := explained.undecidedRowsFor(holders.issues)
		wispLeft := explained.undecidedRowsFor(holders.wisps)
		if len(issueIDs) > 0 {
			n, err := recomputeAllIsBlockedPassInTx(ctx, tx, "issues", "i", "dependencies", explained, issueLeft)
			if err != nil {
				return total, err
			}
			changed += n
		}
		if len(wispIDs) > 0 {
			n, err := recomputeAllIsBlockedPassInTx(ctx, tx, "wisps", "w", "wisp_dependencies", explained, wispLeft)
			if err != nil {
				return total, err
			}
			changed += n
		}
		if changed == 0 {
			// Every decided row has converged, so the read above describes
			// the graph as it now stands: settle the undecided rows on it. A
			// mark there is a change the next pass must see, so it ends this
			// pass before the other table is settled on a stale read.
			for _, t := range []struct {
				table, alias, depTable string
				ids                    []string
				left                   undecidedRows
			}{
				{"issues", "i", "dependencies", issueIDs, issueLeft},
				{"wisps", "w", "wisp_dependencies", wispIDs, wispLeft},
			} {
				if len(t.ids) == 0 || t.left.empty() {
					continue
				}
				n, err := execRowsAffected(ctx, tx, "mark undecided",
					markUndecidedAllBlockedSQL(t.table, t.alias, t.depTable, explained, t.left),
					append(explained.args(), t.left.args()...)...)
				if err != nil {
					return total, err
				}
				if n > 0 {
					changed = n
					break
				}
			}
		}
		total += changed
		if changed == 0 {
			// The full recompute is repair (`bd recompute-blocked`, `bd doctor
			// --fix`): no request is behind the rows it corrects.
			if err := recordBlockedJournalChanges(ctx, tx, before, issueIDs, wispIDs, NoRequestActor); err != nil {
				return total, err
			}
			return total, nil
		}
	}
}

// recomputeAllIsBlockedPassInTx runs one full-table mark+unmark pass for one
// table using the unbatched semi-join statements, returning rows corrected.
// Error wrapping mirrors runMarkUnmarkBatchedInTx verbatim — downstream
// consumers (wh-bridge-sync among them) match on these strings. Neither
// statement touches a row in left except to clear a closed or pinned one.
func recomputeAllIsBlockedPassInTx(
	ctx context.Context, tx DBTX, table, alias, depTable string, explained subtreeExplainedParents, left undecidedRows,
) (int64, error) {
	args := append(explained.args(), left.args()...)
	changed, err := execRowsAffected(ctx, tx, "mark",
		markAllBlockedSQL(table, alias, depTable, explained, left), args...)
	if err != nil {
		return changed, err
	}
	n, err := execRowsAffected(ctx, tx, "unmark",
		unmarkAllBlockedSQL(table, alias, depTable, explained, left), args...)
	return changed + n, err
}

// execRowsAffected runs one is_blocked UPDATE of the full repair and returns
// the rows it changed. step names the statement ("mark", "unmark", "mark
// undecided") in the strings runMarkUnmarkBatchedInTx also produces:
// "recompute is_blocked (<step>): …" and, for a failed row count,
// "recompute is_blocked (<step> rows affected): …".
func execRowsAffected(ctx context.Context, tx DBTX, step, query string, args ...any) (int64, error) {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("recompute is_blocked (%s): %w", step, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recompute is_blocked (%s rows affected): %w", step, err)
	}
	return n, nil
}

// markAllBlockedSQL is the full-table analog of the batched mark template:
// same predicate, restructured so the engine can execute it as a semi-join.
// It marks no row in left (left.notInSQL): those are settled after the
// fixpoint, by markUndecidedAllBlockedSQL.
//
//nolint:gosec // G201: table, alias, and depTable are constants from the only two callers.
func markAllBlockedSQL(table, alias, depTable string, explained subtreeExplainedParents, left undecidedRows) string {
	return fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 1, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.is_blocked = 0
		  AND %[2]s.status <> 'closed' AND %[2]s.status <> 'pinned'
		  AND %[2]s.id IN (%[3]s)%[4]s
	`, table, alias, shouldBeBlockedIDsUnionPrecomputedSQL(depTable, explained),
		left.notInSQL(alias, depTable, ""))
}

// unmarkAllBlockedSQL is the full-table analog of the batched unmark
// template. NOT IN is null-hostile — one NULL in the subquery result makes the
// predicate UNKNOWN for every row and the unmark silently stops firing — which
// is why every leg of the union carries an explicit
// d.issue_id IS NOT NULL guard, and so does every leg of left's set.
//
// A row in left keeps its flag unless it is closed or pinned (undecidedRows).
//
//nolint:gosec // G201: table, alias, and depTable are constants from the only two callers.
func unmarkAllBlockedSQL(table, alias, depTable string, explained subtreeExplainedParents, left undecidedRows) string {
	return fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 0, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.is_blocked = 1
		  AND ( %[2]s.status = 'closed' OR %[2]s.status = 'pinned'
		        OR ( %[2]s.id NOT IN (%[3]s)%[4]s ) )
	`, table, alias, shouldBeBlockedIDsUnionPrecomputedSQL(depTable, explained),
		left.notInSQL(alias, depTable, ""))
}

// markUndecidedAllBlockedSQL is the full repair's closing mark: every open row
// in left (which must not be empty) that the converged graph gives a blocker it
// can see. It is markAllBlockedSQL with left's exclusion turned around.
//
//nolint:gosec // G201: table, alias, and depTable are constants from the only caller.
func markUndecidedAllBlockedSQL(table, alias, depTable string, explained subtreeExplainedParents, left undecidedRows) string {
	return withPresentSpawnerGate(fmt.Sprintf(`
		UPDATE %[1]s %[2]s SET %[2]s.is_blocked = 1, %[2]s.updated_at = %[2]s.updated_at
		WHERE %[2]s.is_blocked = 0
		  AND %[2]s.status <> 'closed' AND %[2]s.status <> 'pinned'
		  AND %[2]s.id IN (%[3]s)
		  AND %[2]s.id IN (%[4]s)
	`, table, alias, shouldBeBlockedIDsUnionPrecomputedSQL(depTable, explained),
		left.selectSQL(depTable, "")))
}

// presentSpawnerSQL confines the union's waits-for leg (the dependency row d)
// to a spawner this database has: an issue spawner, or a wisp spawner whose row
// is here, looked up by primary key.
const presentSpawnerSQL = `( d.depends_on_wisp_id IS NULL
		        OR EXISTS (SELECT 1 FROM wisps psp WHERE psp.id = d.depends_on_wisp_id) )`

// withPresentSpawnerGate rewrites every waits-for leg of the union spliced into
// stmt so it reads a gate only onto a present spawner (presentSpawnerSQL).
//
// A waits-for edge onto a wisp spawner that is not here is UNKNOWN even when an
// open durable child of the spawner is visible: the spawner's wisp children
// (their parent-child rows live in wisp_dependencies) and the spawner's own
// status are not here, and the any-children gate is non-monotone in its
// children, so the closed child that opens the gate may be one of the absent
// ones. The leg as written would read the gate from the visible children
// alone, which is a reason for no row whose edge dangles.
//
// Only the statements that can decide a held row read it: the closing marks,
// the doctor's counts while a row is held, and the mark-only pass an edge add
// runs over a chunk that holds a dangling edge (runMarkBatchedInTx), which
// marks held rows and decided rows alike. A decided row holds no such edge
// (it would be a holder), so the rewrite changes nothing for it, and a
// database without a dangling edge never runs a rewritten statement: its SQL is
// byte for byte what it was.
func withPresentSpawnerGate(stmt string) string {
	return strings.ReplaceAll(stmt, "WHERE ("+waitsForGateBlockedSQL+")",
		"WHERE "+presentSpawnerSQL+"\n\t\t  AND ("+waitsForGateBlockedSQL+")")
}

// danglingWispEdgeTypesSQL are the edge types the should-be-blocked union reads
// a wisp for: blocks and conditional-blocks (leg 2), parent-child (leg 4) and
// waits-for (leg 5, the spawner).
const danglingWispEdgeTypesSQL = "('blocks', 'conditional-blocks', 'parent-child', 'waits-for')"

// danglingWispEdgeSQL is the row-level spelling of "this dependency row names
// a wisp this database has no row for", for a dependency row aliased rowAlias
// read beside `LEFT JOIN wisps <wispAlias> ON <wispAlias>.id =
// <rowAlias>.depends_on_wisp_id`, over the edge types the union reads a wisp
// for (danglingWispEdgeTypesSQL).
//
// WHY SUCH A ROW EXISTS. dependencies is a versioned table and
// depends_on_wisp_id replicates with it, but wisps and wisp_% are dolt_ignored
// and stay on the database that minted them. A clone or a pull therefore
// carries the edge and not the wisp, and not the wisp's own wisp_dependencies
// rows either (its parent-child row among them). On the minting database the
// edge does not outlive the wisp (deleting a wisp deletes its edges in the
// same transaction, DeleteWispFromDependenciesInTx), so there a dangling edge
// means a delete that went around that path.
//
// WHAT IT MEANS. An inner join to wisps drops the row, which reads "this
// blocker is satisfied". That is a guess: the wisp may be open on the database
// that has it. issues.is_blocked is versioned, so a recompute that writes the
// guess replicates it, the minting database writes the true flag back, and the
// two trade the same rows through every merge. The row is UNKNOWN here: its
// holder is an undecided row (undecidedRows), and as a parent's row it makes
// the parent undecided for its children (parentsExplainedBySubtreeCoreSQL).
//
// WHY A LEFT JOIN. Spelled NOT EXISTS (SELECT 1 FROM wisps …), the engine
// planned it as an anti-join over a full scan of wisps, once per statement:
// with 20000 wisps and 600 wisp edges in a 200-id batch, a scoped recompute
// ran about 20x slower on the database that mints the wisps (the review of
// gastownhall/beads#8597). Every caller writes it beside a LEFT JOIN on the
// wisps primary key instead, hinted to a lookup where the caller drives from a
// batch (TestGraphWalkPlansHonorJoinHints pins the plans).
//
//nolint:gosec // G201: rowAlias and wispAlias are constants from the callers.
func danglingWispEdgeSQL(rowAlias, wispAlias string) string {
	return danglingWispEdgeOfTypesSQL(rowAlias, wispAlias, danglingWispEdgeTypesSQL)
}

// danglingWispEdgeOfTypesSQL is danglingWispEdgeSQL over a subset of its edge
// types, types being a rendered IN-list of them.
//
//nolint:gosec // G201: rowAlias, wispAlias and types are constants from the callers.
func danglingWispEdgeOfTypesSQL(rowAlias, wispAlias, types string) string {
	return fmt.Sprintf(`( %[1]s.depends_on_wisp_id IS NOT NULL AND %[2]s.id IS NULL
		      AND %[1]s.type IN %[3]s )`, rowAlias, wispAlias, types)
}

// danglingWispHoldersSQL selects every depTable issue_id that holds a
// dangling wisp edge of the given types (danglingWispEdgeOfTypesSQL), or that
// matches or, a rendered "OR (…)" alternative on the same row ("" for none).
// scope is the same per-leg predicate the union takes: "" for the whole
// table, a rendered batch IN-list for a batch. It carries the union legs'
// d.issue_id IS NOT NULL guard because it is read through the same NOT IN,
// and it drives from the dependency rows and looks each wisp up by primary
// key, as the union's wisp legs do.
//
//nolint:gosec // G201: depTable, scope, types and or are constants and ? placeholders from the callers.
func danglingWispHoldersSQL(depTable, scope, types, or string) string {
	return fmt.Sprintf(`
		SELECT /*+ JOIN_ORDER(d, dw) LOOKUP_JOIN(d, dw) */ d.issue_id FROM %[1]s d
		LEFT JOIN wisps dw ON dw.id = d.depends_on_wisp_id
		WHERE d.issue_id IS NOT NULL %[2]s
		  AND ( %[3]s%[4]s )`, depTable, scope, danglingWispEdgeOfTypesSQL("d", "dw", types), or)
}

// undecidedRows' select is always exactly two members of a UNION, one per pair
// of edge types, and not for the rows' sake. A statement that tests
// `id IN (set)` or `id NOT IN (set)` as a conjunct of its own lets the engine
// unnest a set that is one SELECT into a semi- or anti-join, and the join's
// exists iterator runs that SELECT again for every outer row: with the set
// written as one SELECT, the full repair and doctor counts over the wisp-edge
// fixture of TestGraphWalkPlansHonorJoinHints took 224 s on the race build,
// and 19 s as two members. A set operation is not unnested; it stays a cached
// subquery, read once per statement, which is how the should-be-blocked union
// already runs.
const (
	danglingBlockerTypesSQL  = "('blocks', 'conditional-blocks')"
	danglingAncestorTypesSQL = "('parent-child', 'waits-for')"
)

// undecidedRows names, for one dependency table, the rows whose is_blocked this
// database cannot derive:
//
//   - every row holding a dangling wisp edge, whose own reason may be the
//     absent wisp. holders says whether the table holds one at all: with none,
//     and no undecided parent, the set is empty, and every statement leaves it
//     out and reads as it did before undecided rows existed;
//   - every parent-child child of an UNDECIDED parent (issueParents,
//     wispParents; see subtreeExplainedParents), whose inherited flag
//     depends on whether that parent is blocked from outside its own subtree,
//     which the absent wisp decides.
//
// The set stops there. An undecided row with no reason row of its own, such as
// the reasonless child of an undecided parent, is never an undecided parent:
// its flag is inherited, and every database passes it down unchanged. Its
// children are decided from the flag it holds as found, which is the value the
// database that has the wisp gave it, and from which that database derives the
// same value for them. Holding them too would make every scoped write climb
// from each reasonless blocked parent of its batch to learn whether an
// undecided parent sits above it, on a database with no dangling edge as well
// (TestDanglingWispEdge_ReasonlessHeldChildPassesItsFlagDown pins the stop).
//
// A recompute never derives such a row's flag from the graph it cannot see.
// It holds the row out of the mark/unmark fixpoint, so the flag stays as found
// while the decided rows converge, and then marks the row if, and only if, the
// converged graph gives it a blocker this database can see: an open blocker,
// a blocking gate, a parent blocked from outside its subtree. A closed or
// pinned row is still cleared. So a merged 1 is never cleared on a guess and
// a merged 0 is set only on evidence.
//
// WHY AFTER, AND NOT IN THE FIXPOINT. The fixpoint reads stored flags, and a
// stored flag can be wrong until the fixpoint corrects it. A parent with a
// stale 1 marks its child on the first pass and is cleared on the same pass,
// and the next pass clears the child again, unless the child is undecided: an
// undecided row cannot be cleared on what this database sees, so a mark made
// on the way would stick, and so would the guess it encodes (the review of
// gastownhall/beads#8597). Marked only from the converged graph, the row gets
// no mark the repair itself would have taken back.
//
// "As found" is the flag the row had when the recompute began, whoever set it:
// the last merge, or a local blocker that has since closed. The flag carries
// no record of which, so a row whose local blocker closed keeps the 1 that
// blocker set, until a merge brings the minting database's value or the
// dangling edge is removed.
type undecidedRows struct {
	holders      bool
	issueParents []string
	wispParents  []string
}

// empty reports a set with no rows by construction, which every statement
// takes as "leave the set out".
func (u undecidedRows) empty() bool {
	return !u.holders && len(u.issueParents) == 0 && len(u.wispParents) == 0
}

// args returns the ids the unscoped set binds, in the order selectSQL reads
// them.
func (u undecidedRows) args() []any {
	return u.selectArgs(nil)
}

// selectArgs returns what selectSQL binds when its scope carries scopeArgs:
// the scope's ids once per member, and the undecided parents after the
// second member's.
func (u undecidedRows) selectArgs(scopeArgs []any) []any {
	out := append(append([]any(nil), scopeArgs...), scopeArgs...)
	for _, id := range u.issueParents {
		out = append(out, id)
	}
	for _, id := range u.wispParents {
		out = append(out, id)
	}
	return out
}

// selectSQL renders the set as an uncorrelated select of depTable issue_ids,
// confined by scope as the union is. It must not be called on an empty set.
// It is two members whatever the set holds (danglingBlockerTypesSQL): the
// holders of a dangling blocks edge, and the holders of a dangling parent-child
// or waits-for edge together with the children of the undecided parents. A
// table without holders (u.holders false) reads no row from its dangling
// tests, which cost it a lookup per wisp edge.
//
//nolint:gosec // G201: depTable and scope are constants; the ids are ? placeholders.
func (u undecidedRows) selectSQL(depTable, scope string) string {
	var children strings.Builder
	for _, parent := range []struct {
		col string
		ids []string
	}{
		{"depends_on_issue_id", u.issueParents},
		{"depends_on_wisp_id", u.wispParents},
	} {
		if len(parent.ids) == 0 {
			continue
		}
		fmt.Fprintf(&children, `
		       OR ( d.type = 'parent-child' AND d.%s IN (%s) )`, parent.col,
			strings.TrimSuffix(strings.Repeat("?,", len(parent.ids)), ","))
	}
	return danglingWispHoldersSQL(depTable, scope, danglingBlockerTypesSQL, "") + "\n\t\tUNION" +
		danglingWispHoldersSQL(depTable, scope, danglingAncestorTypesSQL, children.String())
}

// notInSQL is the conjunct that keeps alias's row out of the set: "" for an
// empty set, which keeps the statement exactly what it was before undecided
// rows existed.
func (u undecidedRows) notInSQL(alias, depTable, scope string) string {
	if u.empty() {
		return ""
	}
	return fmt.Sprintf("\n\t\t  AND %s.id NOT IN (%s)", alias, u.selectSQL(depTable, scope))
}

// danglingHolders says which dependency tables hold at least one dangling wisp
// edge (danglingWispEdgeSQL).
type danglingHolders struct {
	issues, wisps bool
}

// danglingWispHoldersInTx asks each dependency table whether it holds a
// dangling wisp edge, one probe each that stops at the first hit. A database
// that holds every wisp its edges name, which is every database that mints its
// own, then runs the full repair and the doctor counts as it did before
// undecided rows existed.
func danglingWispHoldersInTx(ctx context.Context, tx DBTX) (danglingHolders, error) {
	var out danglingHolders
	var err error
	if out.issues, err = hasDanglingWispEdgeInTx(ctx, tx, "dependencies"); err != nil {
		return out, err
	}
	if out.wisps, err = hasDanglingWispEdgeInTx(ctx, tx, "wisp_dependencies"); err != nil && !isTableNotExistError(err) {
		return out, err
	}
	return out, nil
}

// subtreeExplainedParents is one read of parentsExplainedBySubtreeSQL, pruned
// by pruneExplainedByAncestryInTx: the ids of the blocked parents that may not
// darken their children, split by the parent's kind (gastownhall/beads#6506).
// They come in two sorts, and the parent-child legs subtract both:
//
//   - EXPLAINED (issueParents, wispParents): blockedness solely explained by
//     the parent's own subtree, with nothing blocked above it that is not. Its
//     children are bright, which this database can derive.
//   - UNDECIDED (undecidedIssueParents, undecidedWispParents): no reason this
//     database can see places the parent's blockedness outside its own
//     subtree, but one it cannot see might. The parent holds a dependency on a
//     wisp that is not here (danglingWispEdgeSQL), or a parent above it does
//     and nothing above is plainly exogenous. Whether its children are dark
//     is the minting database's answer, so they are undecided rows
//     (undecidedRows) and keep the flag they have.
//
// Both sorts carry a reason row of their own: a blocking reason, or a
// dependency on a wisp that is not here. A blocked parent with neither is never
// in the set, so it passes its stored flag down, even while it is an undecided
// row itself (parentsExplainedBySubtreeSQL, undecidedRows).
//
// WHY THE IDS AND NOT THE SQL. A spliced `p.id NOT IN (deriving query)` is
// evaluated once per leg per statement, and a full repair pass is four
// statements over two parent kinds. Measured at 1000 issues / 2870 deps:
// 46 ms on origin/main, 420 ms with the set spliced into all four, 80 ms read
// once per pass and bound as ids. The read itself is the same query either
// way; what changes is how many times a pass pays for it. The BATCHED path
// makes the same trade against its own scope rather than the whole store
// (scopedExplainedParentsInTx) — spliced there, it measured 6.9x.
//
// The set is small by construction — blocked parents that carry a blocking
// reason of their own, all of whose reasons point inside their own subtree or
// at a wisp that is not here — so binding it as an IN-list is bounded by the
// blocked-parent count, not by the store.
type subtreeExplainedParents struct {
	issueParents []string
	wispParents  []string

	undecidedIssueParents []string
	undecidedWispParents  []string
}

// issueLeg and wispLeg are the ids one parent-child leg subtracts: the
// explained parents of that kind, then the undecided ones.
func (e subtreeExplainedParents) issueLeg() []string {
	return append(append([]string(nil), e.issueParents...), e.undecidedIssueParents...)
}

func (e subtreeExplainedParents) wispLeg() []string {
	return append(append([]string(nil), e.wispParents...), e.undecidedWispParents...)
}

// args returns the bound ids in the order the union's legs consume them: the
// issue-parent leg (3) before the wisp-parent leg (4). A caller that splices
// the union more than once (countStaleIsBlockedSQL) repeats this list once per
// splice, in the same order.
func (e subtreeExplainedParents) args() []any {
	issues, wisps := e.issueLeg(), e.wispLeg()
	out := make([]any, 0, len(issues)+len(wisps))
	for _, id := range issues {
		out = append(out, id)
	}
	for _, id := range wisps {
		out = append(out, id)
	}
	return out
}

// empty reports a read that returned no parent of either sort.
func (e subtreeExplainedParents) empty() bool {
	return len(e.issueParents) == 0 && len(e.wispParents) == 0 &&
		len(e.undecidedIssueParents) == 0 && len(e.undecidedWispParents) == 0
}

// undecidedRowsFor is the undecided-row set (undecidedRows) of one dependency
// table under this read: its dangling-edge holders when holders says it has
// any, and the children of this read's undecided parents.
func (e subtreeExplainedParents) undecidedRowsFor(holders bool) undecidedRows {
	return undecidedRows{
		holders:      holders,
		issueParents: e.undecidedIssueParents,
		wispParents:  e.undecidedWispParents,
	}
}

// notInPlaceholders renders the leg's exclusion for n bound ids — and nothing
// at all for none, because `NOT IN ()` is a syntax error and "exclude nothing"
// is the right reading of an empty set.
func notInPlaceholders(n int) string {
	if n == 0 {
		return ""
	}
	return "AND p.id NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", n), ",") + ")"
}

// subtreeExplainedParentsInTx reads both kinds' sets, once, for every
// unbatched statement that needs them.
//
// It is ONE read per kind and not one per dependency table because exogeneity
// is a property of the PARENT — of its own blocking reasons and its own
// subtree — and says nothing about which table the children asking about it
// live in. The candidate restriction inside the query therefore spans both
// tables (unbatchedCandSources): a parent that is a parent anywhere is
// computed once and excluded everywhere. Halving the reads is the difference
// between the doctor count landing at 1.7x origin/main and at 1.4x.
func subtreeExplainedParentsInTx(ctx context.Context, tx DBTX) (subtreeExplainedParents, error) {
	var read subtreeExplainedParents
	var err error

	read.issueParents, read.undecidedIssueParents, err = queryExplainedParents(ctx, tx,
		parentsExplainedBySubtreeSQL("issues", "dependencies", "depends_on_issue_id", unbatchedCandSources()))
	if err != nil {
		return read, fmt.Errorf("read issue parents explained by their own subtree: %w", err)
	}
	read.wispParents, read.undecidedWispParents, err = queryExplainedParents(ctx, tx,
		parentsExplainedBySubtreeSQL("wisps", "wisp_dependencies", "depends_on_wisp_id", unbatchedCandSources()))
	if err != nil && !isTableNotExistError(err) {
		return read, fmt.Errorf("read wisp parents explained by their own subtree: %w", err)
	}
	// Every parent in the store was a candidate, so a blocked parent this read
	// did not return is one its own reasons do not explain.
	return pruneExplainedByAncestryInTx(ctx, tx, read, true)
}

// scopedExplainedParentsInTx is subtreeExplainedParentsInTx confined to ONE
// BATCH: the same two reads, but the candidate parents are read only from the
// batch's own dependency rows, so the set is derived for the handful of
// parents above the batch instead of for every parent in the store.
//
// WHY A READ AND NOT A SPLICE. The batched mark/unmark templates used to
// splice the deriving query into both of their parent-child legs,
// batch-scoped, which reads as the cheap option: the scope confines cand to
// the batch, so the subquery is small. It is not cheap. A spliced
// `p.id NOT IN (subquery)` is paid once per leg per STATEMENT, and a batched
// pass is a mark and an unmark, so one 200-id recompute derived the set four
// times. Measured at 1000 issues / 2870 deps on embedded dolt, one pass over
// 200 ids: 53 ms on origin/main, 422 ms with the query spliced into both legs
// of both statements (6.9x, against a 1.5x bar), 61 ms read once per batch and
// bound as ids. It is the same trade the unbatched statements make one level
// up (see subtreeExplainedParents): the read is the same query either way,
// and what changes is how many times a pass pays for it.
//
// scope is the batch predicate already rendered to placeholders
// ("AND d.issue_id IN (?,?,…)") and args its ids. Both parent kinds are read
// with the same scope, because exogeneity is a property of the PARENT and the
// batch is only what bounds which parents can be asked about.
//
// holders reports whether a row of the batch holds a dangling wisp edge, which
// the guard read answers in the same statement (batchParentKindsInTx).
func scopedExplainedParentsInTx(
	ctx context.Context, tx DBTX, depTable, scope string, args []any,
) (read subtreeExplainedParents, holders bool, err error) {
	hasIssueParent, hasWispParent, holders, err := batchParentKindsInTx(ctx, tx, depTable, scope, args)
	if err != nil {
		if isTableNotExistError(err) {
			return read, false, nil
		}
		return read, false, err
	}
	cands := []candSource{{depTable: depTable, scope: scope}}

	if hasIssueParent {
		read.issueParents, read.undecidedIssueParents, err = queryExplainedParents(ctx, tx,
			parentsExplainedBySubtreeSQL("issues", "dependencies", "depends_on_issue_id", cands), args...)
		if err != nil {
			return read, holders, fmt.Errorf("read batch issue parents explained by their own subtree: %w", err)
		}
	}
	if hasWispParent {
		read.wispParents, read.undecidedWispParents, err = queryExplainedParents(ctx, tx,
			parentsExplainedBySubtreeSQL("wisps", "wisp_dependencies", "depends_on_wisp_id", cands), args...)
		if err != nil && !isTableNotExistError(err) {
			return read, holders, fmt.Errorf("read batch wisp parents explained by their own subtree: %w", err)
		}
	}
	// Only the batch's own parents were candidates, so the parents above them
	// are read by id as the walk climbs.
	read, err = pruneExplainedByAncestryInTx(ctx, tx, read, false)
	return read, holders, err
}

// parentRef is one parent in pruneExplainedByAncestryInTx's walk: its id and
// its kind, which picks the tables its rows live in. The two id spaces are kept
// apart rather than trusted not to collide.
type parentRef struct {
	id   string
	wisp bool
}

// parentEdge is one parent-child edge from child up to a parent that is
// blocked.
type parentEdge struct {
	child, parent parentRef
}

// parentState is what pruneExplainedByAncestryInTx knows about one blocked
// parent it has met, ordered so that the larger value wins along a chain: a
// parent is as dark as the darkest parent above it.
type parentState uint8

const (
	// parentExplained: explained by its own subtree, or a no-reason parent the
	// walk climbs through, with nothing darker met above it yet.
	parentExplained parentState = iota
	// parentUndecided: blocked, with no reason this database can see placing
	// that outside its own subtree, and a dependency on a wisp it does not
	// have, its own or a parent's above it (subtreeExplainedParents).
	parentUndecided
	// parentExogenous: blocked from outside its own subtree, by a reason this
	// database can see. Final.
	parentExogenous
)

// pruneExplainedByAncestryInTx settles a read of parentsExplainedBySubtreeSQL
// against what is above each parent: a parent blocked FROM ABOVE (one with a
// blocked parent that is exogenous, at any depth) is dropped, and an explained
// parent with an undecided one above it and nothing exogenous is undecided
// too. It is the contract's recursive clause ("…or P's own parent-child parent
// is exogenously blocked") for parents that carry reasons of their own.
//
// WHY THE FIXPOINT DOES NOT CARRY IT. The read decides from a parent's OWN
// reason rows. The legs' p.is_blocked = 1 conjunct, read only on a parent
// that is neither closed nor pinned (a closed or pinned row is never blocked,
// whatever its stored bit says), carries inheritance only for a parent with
// no reason row: such a parent is dark exactly while something above it
// cascades into it. A parent with a subtree reason is dark either way, so its
// bit says nothing about what is above it. Q blocks on
// outside X, pc P -> Q, P blocks on its own child C: the read explains P, so C
// comes bright while the epic above it is still gated by X. A one-level check
// ("no blocked parent outside the set") is not enough either. Put a second
// close gate between them (R blocks on X, pc Q -> R, Q blocks on P, pc P -> Q,
// P blocks on C): P's blocked parent Q is in the set, so P stays explained and
// R's gate never reaches C. Whether a parent is explained depends on the whole
// chain above it, and no is_blocked bit records that chain.
//
// So this resolves it here, over the few parents the read returned:
//
//   - the edges: each returned parent's blocked parents
//     (blockedParentEdgesInTx);
//   - the state of every blocked parent the walk meets. A complete read
//     already decides it for every parent with a reason row of its own: every
//     such parent in the store was a candidate, so one it did not return is
//     exogenous. A batch-scoped read decided only the batch's parents, so
//     the walk reads the rest by id (explainedAmongInTx). A blocked parent
//     with no reason row of its own is decided by neither read — its bit is
//     inherited — so the walk climbs through it (withoutReasonAmongInTx) and
//     lets the chain decide it;
//   - the states themselves (settleParentStates): every parent ends as dark
//     as the darkest blocked parent above it, starting from the state the
//     walk entered it with and raising states along the edges until nothing
//     rises. This is the GREATEST explained set the contract allows, because
//     "exogenously blocked" needs a finite chain up to a real outside reason,
//     so a malformed hierarchy cycle of close gates with nothing exogenous
//     above it stays explained, and the least undecided one, for the same
//     reason. The result is unique, so the order of the raises cannot change
//     it. Only the read's parents are returned: the others only decide them.
//
// COST. Nothing when the read is empty, which covers every store with no close
// gate and every batch with none at or above it. Otherwise it is one indexed
// edge read per level the walk climbs, plus, at a level that meets blocked
// parents it has not met before, an explained read of them by id on the
// scoped path (explainedAmongInTx) and, on both paths, a reason-row read by id
// of those still unknown (withoutReasonAmongInTx), over parents that are few
// by construction.
func pruneExplainedByAncestryInTx(
	ctx context.Context, tx DBTX, read subtreeExplainedParents, complete bool,
) (subtreeExplainedParents, error) {
	if read.empty() {
		return read, nil
	}
	refs, state := read.refStates()
	above, err := climbBlockedParentsInTx(ctx, tx, refs, state, complete)
	if err != nil {
		return read, err
	}
	settleParentStates(state, above)

	var out subtreeExplainedParents
	for _, p := range refs {
		switch {
		case state[p] == parentExplained && p.wisp:
			out.wispParents = append(out.wispParents, p.id)
		case state[p] == parentExplained:
			out.issueParents = append(out.issueParents, p.id)
		case state[p] == parentUndecided && p.wisp:
			out.undecidedWispParents = append(out.undecidedWispParents, p.id)
		case state[p] == parentUndecided:
			out.undecidedIssueParents = append(out.undecidedIssueParents, p.id)
		}
	}
	return out, nil
}

// refStates lists the read's parents with their kinds, issue parents first,
// and the state each enters the walk with.
func (e subtreeExplainedParents) refStates() ([]parentRef, map[parentRef]parentState) {
	var refs []parentRef
	state := make(map[parentRef]parentState)
	for _, kind := range []struct {
		ids []string
		ps  parentState
		w   bool
	}{
		{e.issueParents, parentExplained, false},
		{e.undecidedIssueParents, parentUndecided, false},
		{e.wispParents, parentExplained, true},
		{e.undecidedWispParents, parentUndecided, true},
	} {
		for _, id := range kind.ids {
			p := parentRef{id: id, wisp: kind.w}
			refs = append(refs, p)
			state[p] = kind.ps
		}
	}
	return refs, state
}

// climbBlockedParentsInTx is pruneExplainedByAncestryInTx's walk: up from the
// read's parents through their blocked parents, returning the edges (child to
// its blocked parents) and entering every blocked parent it meets in state.
//
// A blocked parent the walk meets is one of four things. Explained by its
// own subtree: a complete read already returned it (so it is met), a scoped
// read decides it by id (explainedAmongInTx), and the walk climbs on from it.
// Undecided by its own rows: returned or read the same way, and the walk
// climbs on from it too, because a parent exogenous above it still darkens
// everything below. Carrying a reason row of its own and neither: final, it
// is exogenously blocked, and nothing above it can change that. Carrying NO
// reason row of its own: its bit is inherited, so it says nothing by itself
// (gastownhall/beads#7386). Such a parent is transparent — blocked from above
// exactly when one of its own blocked parents is — so the walk enters it as
// explained and climbs through it, and settleParentStates decides it with
// the rest. Treating it as final would let a stale bit on it hold itself up:
// in a malformed hierarchy cycle it darkens the explained parent below it,
// which darkens it back, and full repair converges on the stale state.
func climbBlockedParentsInTx(
	ctx context.Context, tx DBTX, frontier []parentRef, state map[parentRef]parentState, complete bool,
) (map[parentRef][]parentRef, error) {
	above := make(map[parentRef][]parentRef)
	for len(frontier) > 0 {
		edges, err := blockedParentEdgesInTx(ctx, tx, frontier)
		if err != nil {
			return nil, err
		}
		var unknown []parentRef
		for _, e := range edges {
			above[e.child] = append(above[e.child], e.parent)
			if _, met := state[e.parent]; !met {
				state[e.parent] = parentExogenous
				unknown = append(unknown, e.parent)
			}
		}
		if len(unknown) == 0 {
			break
		}
		// A fresh slice: the first frontier is the caller's refs, read again
		// after the walk to build the result.
		var next []parentRef
		if !complete {
			explained, undecided, err := explainedAmongInTx(ctx, tx, unknown)
			if err != nil {
				return nil, err
			}
			for _, p := range explained {
				state[p] = parentExplained
				next = append(next, p)
			}
			for _, p := range undecided {
				state[p] = parentUndecided
				next = append(next, p)
			}
		}
		var unread []parentRef
		for _, p := range unknown {
			if state[p] == parentExogenous {
				unread = append(unread, p)
			}
		}
		transparent, err := withoutReasonAmongInTx(ctx, tx, unread)
		if err != nil {
			return nil, err
		}
		for _, p := range transparent {
			state[p] = parentExplained
			next = append(next, p)
		}
		frontier = next
	}
	return above, nil
}

// settleParentStates raises every met parent to the darkest state among its
// blocked parents, until nothing rises: the greatest explained set the
// contract allows, and within the rest, exogenous wherever a chain reaches a
// reason this database can see outside its subtree.
func settleParentStates(state map[parentRef]parentState, above map[parentRef][]parentRef) {
	for raised := true; raised; {
		raised = false
		for p, s := range state {
			for _, q := range above[p] {
				if state[q] > s {
					s = state[q]
				}
			}
			if s != state[p] {
				state[p] = s
				raised = true
			}
		}
	}
}

// blockedParentEdgesInTx reads the parent-child edges from children up to a
// parent that is blocked. An edge lives in the child's dependency table and
// names the parent in the column for the parent's kind, so this is one lookup
// per child kind and parent kind, each keyed on the child's issue_id. The hints
// pin that drive, as on the batched legs. A parent is blocked here on the same
// terms as there: its bit is 1 and it is neither closed nor pinned, because
// the merge clause admits a stale 1 on such a row (gastownhall/beads#7626).
//
//nolint:gosec // G201: table and column names are constants; the ids are ? placeholders.
func blockedParentEdgesInTx(ctx context.Context, tx DBTX, children []parentRef) ([]parentEdge, error) {
	var issueIDs, wispIDs []string
	for _, c := range children {
		if c.wisp {
			wispIDs = append(wispIDs, c.id)
		} else {
			issueIDs = append(issueIDs, c.id)
		}
	}
	var out []parentEdge
	for _, side := range []struct {
		depTable string
		ids      []string
		wisp     bool
	}{
		{"dependencies", issueIDs, false},
		{"wisp_dependencies", wispIDs, true},
	} {
		for start := 0; start < len(side.ids); start += queryBatchSize {
			end := min(start+queryBatchSize, len(side.ids))
			placeholders, args := buildSQLInClause(side.ids[start:end])
			for _, parent := range []struct {
				table, col string
				wisp       bool
			}{
				{"issues", "depends_on_issue_id", false},
				{"wisps", "depends_on_wisp_id", true},
			} {
				edges, err := queryParentEdges(ctx, tx, fmt.Sprintf(`
					SELECT /*+ JOIN_ORDER(d, p) LOOKUP_JOIN(d, p) */ d.issue_id, p.id
					FROM %[1]s d
					JOIN %[2]s p ON p.id = d.%[3]s
					WHERE d.issue_id IN (%[4]s)
					  AND d.type = 'parent-child'
					  AND p.is_blocked = 1
					  AND p.status <> 'closed' AND p.status <> 'pinned'
				`, side.depTable, parent.table, parent.col, placeholders), args, side.wisp, parent.wisp)
				if err != nil {
					return nil, fmt.Errorf("read blocked parents of explained parents: %w", err)
				}
				out = append(out, edges...)
			}
		}
	}
	return out, nil
}

// queryParentEdges runs one of blockedParentEdgesInTx's lookups and tags each
// (child, parent) row with the kinds the lookup was for. A table that does not
// exist has no edges.
func queryParentEdges(
	ctx context.Context, tx DBTX, query string, args []any, childWisp, parentWisp bool,
) ([]parentEdge, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		if isTableNotExistError(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []parentEdge
	for rows.Next() {
		var childID, parentID string
		if err := rows.Scan(&childID, &parentID); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, parentEdge{
			child:  parentRef{id: childID, wisp: childWisp},
			parent: parentRef{id: parentID, wisp: parentWisp},
		})
	}
	return out, rows.Err()
}

// explainedAmongInTx is parentsExplainedBySubtreeSQL for the named parents, by
// id: those explained by their own subtree, and those undecided by their own
// rows. pruneExplainedByAncestryInTx uses it for the blocked parents above a
// batch that the batch-scoped read was not asked about.
//
// Unlike the wisp reads above, it does not tolerate a missing table, because it
// cannot meet one: the prune runs only on a non-empty read, and that read came
// from the same core query, which reads both item tables and both dependency
// tables whichever kind it asks about.
func explainedAmongInTx(ctx context.Context, tx DBTX, parents []parentRef) (explained, undecided []parentRef, err error) {
	for _, kind := range []struct {
		table, depTable string
		wisp            bool
	}{
		{"issues", "dependencies", false},
		{"wisps", "wisp_dependencies", true},
	} {
		var ids []string
		for _, p := range parents {
			if p.wisp == kind.wisp {
				ids = append(ids, p.id)
			}
		}
		for start := 0; start < len(ids); start += queryBatchSize {
			end := min(start+queryBatchSize, len(ids))
			placeholders, args := buildSQLInClause(ids[start:end])
			found, open, err := queryExplainedParents(ctx, tx,
				parentsExplainedBySubtreeCoreSQL(kind.table, kind.depTable, candByIDSQL(kind.table, placeholders)),
				args...)
			if err != nil {
				return nil, nil, fmt.Errorf("read explained parents above the batch: %w", err)
			}
			for _, id := range found {
				explained = append(explained, parentRef{id: id, wisp: kind.wisp})
			}
			for _, id := range open {
				undecided = append(undecided, parentRef{id: id, wisp: kind.wisp})
			}
		}
	}
	return explained, undecided, nil
}

// withoutReasonAmongInTx reports which of parents carry NO blocking reason row
// of their own: no open blocks/conditional-blocks target, no blocking
// waits-for gate, and no dependency on a wisp this database does not have.
// Such a parent's is_blocked bit is inherited from above, so neither
// explained-parent read can decide it (it has no row to aggregate), and
// climbBlockedParentsInTx climbs through it instead. The row predicate is the
// one the explained-parent reads aggregate (blockingReasonSQL, or a dangling
// wisp edge), so the two readings of "reason row" cannot drift.
//
//nolint:gosec // G201: depTable is a constant; placeholders carries only ? placeholders.
func withoutReasonAmongInTx(ctx context.Context, tx DBTX, parents []parentRef) ([]parentRef, error) {
	if len(parents) == 0 {
		return nil, nil
	}
	carrying := make(map[parentRef]bool, len(parents))
	for _, kind := range []struct {
		depTable string
		wisp     bool
	}{
		{"dependencies", false},
		{"wisp_dependencies", true},
	} {
		var ids []string
		for _, p := range parents {
			if p.wisp == kind.wisp {
				ids = append(ids, p.id)
			}
		}
		for start := 0; start < len(ids); start += queryBatchSize {
			end := min(start+queryBatchSize, len(ids))
			placeholders, args := buildSQLInClause(ids[start:end])
			found, err := queryIDs(ctx, tx, fmt.Sprintf(`
		  SELECT DISTINCT pr.issue_id FROM %s pr
		  LEFT JOIN wisps rw ON rw.id = pr.depends_on_wisp_id
		  WHERE pr.issue_id IN (%s)
		    AND ( (%s) OR %s )`, kind.depTable, placeholders,
				blockingReasonSQL("pr"), danglingWispEdgeSQL("pr", "rw")), args...)
			if err != nil {
				return nil, fmt.Errorf("read blocked parents carrying a reason of their own: %w", err)
			}
			for _, id := range found {
				carrying[parentRef{id: id, wisp: kind.wisp}] = true
			}
		}
	}
	var out []parentRef
	for _, p := range parents {
		if !carrying[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// batchParentKindsInTx reports which parent KINDS the batch can possibly ask
// about: whether any parent-child row in the batch names an issue parent, and
// whether any names a wisp parent. In the same statement it reports whether
// any row in the batch holds a dangling wisp edge (danglingWispEdgeSQL), which
// decides whether the batch has undecided rows of its own.
//
// It is the cheap guard in front of the two reads above, and it is what keeps
// the SMALL-batch case honest. A kind with no parent-child row in the batch
// has an empty candidate set, hence an empty exogeneity set, so its read is
// pure overhead — and that overhead is not small: the read is a ten-join walk
// under two CTEs, which dolt spends ~8 ms planning whether it returns rows or
// not. Most batches name no wisp parent at all, and a batch of roots (or of
// rows with no hierarchy) names neither. Measured at 1000 issues / 2870 deps,
// per pass: a 2-id batch of unparented rows 37 ms with both reads run blind
// against 20 ms with this guard, which is origin/main's own number.
//
// One indexed aggregate over the batch's own dependency rows, every column at
// once, because COUNT ignores NULLs and a parent-child row carries exactly one
// of the two parent columns. The wisp lookup is pinned to the primary key, as
// the union's wisp legs are, so the dangling count costs one probe per wisp
// edge in the batch and never a scan of wisps.
//
//nolint:gosec // G201: depTable is a constant from the caller; scope carries only ? placeholders.
func batchParentKindsInTx(
	ctx context.Context, tx DBTX, depTable, scope string, args []any,
) (issueParent, wispParent, holders bool, err error) {
	query := fmt.Sprintf(`
		SELECT /*+ JOIN_ORDER(d, dw) LOOKUP_JOIN(d, dw) */
		       COUNT(CASE WHEN d.type = 'parent-child' THEN d.depends_on_issue_id END),
		       COUNT(CASE WHEN d.type = 'parent-child' THEN d.depends_on_wisp_id END),
		       COUNT(CASE WHEN %[3]s THEN 1 END)
		FROM %[1]s d
		LEFT JOIN wisps dw ON dw.id = d.depends_on_wisp_id
		WHERE d.issue_id IS NOT NULL %[2]s
	`, depTable, scope, danglingWispEdgeSQL("d", "dw"))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return false, false, false, err
	}
	defer func() { _ = rows.Close() }()
	var issueParents, wispParents, dangling int64
	if rows.Next() {
		if err := rows.Scan(&issueParents, &wispParents, &dangling); err != nil {
			return false, false, false, fmt.Errorf("scan batch parent kinds: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return false, false, false, err
	}
	return issueParents > 0, wispParents > 0, dangling > 0, nil
}

// candSource names one dependency table the candidate-parent set is read from,
// with the batch scope that confines it there (empty for the unbatched reads).
type candSource struct {
	depTable string
	scope    string
}

// unbatchedCandSources is every table a parent-child edge can live in. The
// batched templates pass their own single scoped source instead.
func unbatchedCandSources() []candSource {
	return []candSource{{depTable: "dependencies"}, {depTable: "wisp_dependencies"}}
}

// queryExplainedParents runs one explained-parent read
// (parentsExplainedBySubtreeCoreSQL), whose rows are a parent id and whether
// the parent holds a dependency on a wisp this database does not have, and
// splits the ids into the explained and the undecided.
func queryExplainedParents(ctx context.Context, tx DBTX, query string, args ...any) (explained, undecided []string, err error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var dangling int64
		if err := rows.Scan(&id, &dangling); err != nil {
			return nil, nil, err
		}
		if dangling != 0 {
			undecided = append(undecided, id)
		} else {
			explained = append(explained, id)
		}
	}
	return explained, undecided, rows.Err()
}

func queryIDs(ctx context.Context, tx DBTX, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// shouldBeBlockedIDsUnionPrecomputedSQL is the union for the UNBATCHED
// statements: no batch scope, and the two parent-child legs subtract an
// already-read id list instead of re-deriving it (see
// subtreeExplainedParents). The caller binds explained.args() once per splice.
//
//nolint:gosec // G201: depTable is constant; the exclusions are ? placeholders.
func shouldBeBlockedIDsUnionPrecomputedSQL(depTable string, explained subtreeExplainedParents) string {
	return shouldBeBlockedIDsUnionCoreSQL(depTable, "",
		notInPlaceholders(len(explained.issueLeg())),
		notInPlaceholders(len(explained.wispLeg())))
}

// shouldBeBlockedIDsUnionScopedPrecomputedSQL selects, UNCORRELATED with any
// outer row, every depTable issue_id that currently has a reason to be
// blocked: one UNION leg per reason — an open blocks/conditional-blocks target
// (issue or wisp), an EXOGENOUSLY blocked parent-child parent (issue or wisp;
// see parentsExplainedBySubtreeSQL), a blocking waits-for gate.
// Semantically, `<alias>.id IN (this select)` ≡ the disjunction of those
// reasons correlated on d.issue_id = <alias>.id, which is what the batched
// mark/unmark templates in blocked_state.go once spelled out as five
// correlated EXISTS — but the engine evaluates this form once as hash
// lookups instead of per outer row (bd-t9ypt). Full repair, doctor count and
// the batched templates share the one core below.
//
// It carries an
// extra predicate on the dependency row appended to every leg's WHERE — "" for
// the whole table (full repair, doctor count), batchScopeSQL for the batched
// mark/unmark templates in blocked_state.go, which confine each leg to
// d.issue_id IN (batch) so the union stays index-sized per statement. scope is
// spliced verbatim: a %s inside it survives this Sprintf for the batch runner.
//
// Like the unbatched form above, and unlike the shape this round replaced, the
// two parent-child legs SUBTRACT AN ALREADY-READ id list rather than the query
// that derives it: the batch runner reads the set once per batch
// (scopedExplainedParentsInTx) and binds it into both statements. The caller
// binds explained.args() once per splice, interleaved with the batch ids in
// text order — expandBatchTemplate does that bookkeeping.
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
// A scoped union pins each joined leg to drive from the batch's own
// dependency rows and look the target or parent up by primary key
// (JOIN_ORDER/LOOKUP_JOIN hints; on MySQL, JOIN_ORDER is honored — the order
// it forces is the one wanted — and the unknown LOOKUP_JOIN hint is ignored
// with a warning). Left to its cost model, the Dolt sql-server planner
// intermittently — depending on when its background statistics refresh
// lands — turned the issues legs around: scanning every open issue through
// the (is_blocked, status) index and probing dependencies per row. Over an
// import's ~2000-5000 uncommitted issues that is ~3 s per leg and 6-10 s per
// mark/unmark statement, where the lookup plan takes ~0.1 s; one plan flip
// early in an import of 5000 cost most of its 28 minutes and brushed the
// 10 s read timeout. The unscoped form (full repair, doctor count) walks the
// whole table and keeps the planner's choice. TestGraphWalkPlansHonorJoinHints
// (embedded) pins the hinted plans.
//
//nolint:gosec // G201: depTable and scope are constants; waitsForGateBlockedSQL is a constant template.
func shouldBeBlockedIDsUnionScopedPrecomputedSQL(
	depTable, scope string, explained subtreeExplainedParents,
) string {
	return shouldBeBlockedIDsUnionCoreSQL(depTable, scope,
		notInPlaceholders(len(explained.issueLeg())),
		notInPlaceholders(len(explained.wispLeg())))
}

// shouldBeBlockedIDsUnionCoreSQL is the five legs themselves. A parent-child
// leg reads the parent's stored bit only while the parent is neither closed
// nor pinned, the same status test legs 1-2 apply to their targets: such a row
// is never blocked, but BlockedStateInvariant's merge clause admits a stale 1
// on it, and its children must not inherit that (gastownhall/beads#7626).
//
// The two parent-child legs take their exogeneity condition as text: both
// callers pass an already-read IN-list rendered to placeholders
// (notInPlaceholders), saying "unless this parent is explained by its own
// subtree, or undecided" (subtreeExplainedParents), and the empty string says
// "no parent is", which is what an empty set means.
//
//nolint:gosec // G201: depTable and scope are constants; the conditions come from the two callers above.
func shouldBeBlockedIDsUnionCoreSQL(depTable, scope, issueParentCond, wispParentCond string) string {
	targetHint, parentHint := "", ""
	if scope != "" {
		targetHint = "/*+ JOIN_ORDER(d, t) LOOKUP_JOIN(d, t) */ "
		parentHint = "/*+ JOIN_ORDER(d, p) LOOKUP_JOIN(d, p) */ "
	}
	return fmt.Sprintf(`
		SELECT %[6]sd.issue_id FROM %[1]s d
		JOIN issues t ON t.id = d.depends_on_issue_id
		WHERE d.issue_id IS NOT NULL %[3]s
		  AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		  AND t.status <> 'closed' AND t.status <> 'pinned'
		UNION
		SELECT %[6]sd.issue_id FROM %[1]s d
		JOIN wisps t ON t.id = d.depends_on_wisp_id
		WHERE d.issue_id IS NOT NULL %[3]s
		  AND (d.type = 'blocks' OR d.type = 'conditional-blocks')
		  AND t.status <> 'closed' AND t.status <> 'pinned'
		UNION
		SELECT %[7]sd.issue_id FROM %[1]s d
		JOIN issues p ON p.id = d.depends_on_issue_id
		WHERE d.issue_id IS NOT NULL %[3]s
		  AND d.type = 'parent-child'
		  AND p.is_blocked = 1
		  AND p.status <> 'closed' AND p.status <> 'pinned'
		  %[4]s
		UNION
		SELECT %[7]sd.issue_id FROM %[1]s d
		JOIN wisps p ON p.id = d.depends_on_wisp_id
		WHERE d.issue_id IS NOT NULL %[3]s
		  AND d.type = 'parent-child'
		  AND p.is_blocked = 1
		  AND p.status <> 'closed' AND p.status <> 'pinned'
		  %[5]s
		UNION
		SELECT d.issue_id FROM (
		  SELECT DISTINCT d.issue_id, d.depends_on_issue_id, d.depends_on_wisp_id, d.metadata
		  FROM %[1]s d
		  WHERE d.issue_id IS NOT NULL %[3]s
		    AND d.type = 'waits-for'
		) d
		WHERE (%[2]s)
	`, depTable, waitsForGateBlockedSQL, scope, issueParentCond, wispParentCond, targetHint, parentHint)
}

// subtreeWalkDepth is how far parentsExplainedBySubtreeSQL walks up from a
// blocking reason's target looking for the parent that holds the reason — the
// depth to which "inside my own subtree" is decided.
//
// The walk is spelled as this many pairs of LEFT JOINs rather than as WITH
// RECURSIVE, and that is a measured choice, not a portability one (dolt runs
// recursive CTEs elsewhere in this package). At 1000 issues / 2870 deps the
// seeded recursive walk cost 45 ms per evaluation against 6 ms for four levels
// of joins: a recursive CTE pays a whole query's overhead per ITERATION, so
// its cost tracks hierarchy depth even when it returns a hundred rows. A fixed
// join chain pays once, and each level is an index probe.
//
// The price is a horizon. A reason target more than this many parent-child
// edges below its blocker reads as exogenous, which is the pre-#6506 behavior
// for that one pair: it propagates MORE, never less, so nothing is hidden that
// the old code showed, and the permanent lock this fix removes needs the
// blocker and the target in the SAME subtree within the horizon. Four covers
// epic -> sub-epic -> leg -> task, which is deeper than any dotted-id
// hierarchy this store has carried, and it is the knob to turn first if a
// real store is ever found below it.
//
// BOTH SIDES OF THE HORIZON ARE PINNED, by a pair of dolt tests that differ by
// one edge: TestParentChildCascade_ReasonAtTheWalkHorizon (a reason exactly
// this many levels down; the subtree comes bright) and
// _ReasonPastTheWalkHorizon (one level further; the subtree stays dark, which
// is origin/main's own answer, so that test passes unchanged on origin/main).
// Raising this constant makes the second one fail and name the rows that
// changed. Measured cost of raising it, at 1000 issues / 2870 deps: see the
// commit that introduced the pin.
const subtreeWalkDepth = 4

// parentsExplainedBySubtreeSQL is the parent-child legs' exogeneity test
// (gastownhall/beads#6506): the set of blocked parents whose blockedness is
// SOLELY explained by their own subtree, which the legs subtract with
// `p.id NOT IN (...)`. It decides once per statement, uncorrelated with any
// outer row, which blocked parents may not darken their children.
//
// The bug it closes: the legs used to cascade on p.is_blocked = 1 alone,
// WHATEVER set that bit. A parent that is blocked only by its own children —
// the "close gate on the epic" idiom, where P carries blocks edges onto C1 and
// C2 so P cannot close before them — therefore darkened C1 and C2, the very
// children it was waiting for. Neither child could ever appear in bd ready, so
// neither could be worked, so neither could close: a permanent lock. P's OWN
// is_blocked stays 1 (it really does depend on open children); only the
// propagation to its children changes.
//
// The contract: a parent-child edge propagates only EXOGENOUS blockedness. P
// counts as blocked FOR CHILD C iff P has a blocking reason — an open
// blocks/conditional-blocks target, or a blocking waits-for gate — whose
// target is OUTSIDE P'S OWN SUBTREE, or P's own parent-child parent is
// (recursively) exogenously blocked.
//
// SUBTREE, not direct children. A depth-1 reading of "own child" leaves the
// permanent lock alive one level down: P blocks on grandchild G, pc C -> P,
// pc G -> C. G is not P's direct child, so P reads exogenous, so P darkens C,
// so C darkens G — and G is the only row whose close can free P. The walk up
// the ancestor chain (subtreeWalkDepth) is what makes this a descendant test.
//
// SHAPE. The caller's condition is a subtraction:
//
//	AND p.id NOT IN (this select, pruned by pruneExplainedByAncestryInTx)
//
// A parent cascades unless it appears here. A parent with NO blocking reason
// row of its own is absent from this set, because the set is built from reason
// rows, so it cascades. For such a parent that is the contract's recursive
// clause, carried by the EXISTING fixpoint and not by a second pass of SQL
// recursion: it is dark because ITS parent is, so its children inherit.
// Because the legs keep the p.is_blocked = 1 conjunct, a chain converges the
// way it always did, one level per fixpoint pass, and the single-pass doctor
// COUNT stays the documented lower bound. The conjunct counts only on a parent
// that is neither closed nor pinned: such a row is never blocked, and a stale
// 1 the merge clause leaves on it must not cascade (gastownhall/beads#7626).
// A parent that DOES carry a reason is dark whatever is above it, so the
// fixpoint cannot carry the clause for it: pruneExplainedByAncestryInTx drops
// it from the set when a blocked parent above it is not itself explained.
//
// Every reason row is either inside the parent's subtree or outside it, so
// "no reason row is exogenous" (MAX(...) = 0 below) is the whole membership
// test; the two bits the shape started with collapse to one. A dependency row
// on a wisp this database does not have (danglingWispEdgeSQL) is a third kind
// of row: neither, because the wisp and its own parent-child row are on the
// database that minted it. It is read beside the reason rows and never counts
// as exogenous, and a parent whose rows are otherwise all inside its subtree,
// or who has no other, comes back UNDECIDED (the read's second column) rather
// than explained: the legs subtract it either way, and its children are
// undecided rows (undecidedRows), so this database neither darkens them on the
// guess that the wisp is outside nor brightens them on the guess that it is
// inside (the review of gastownhall/beads#8597). That holds for every edge
// type that names it, parent-child included: a parent that is a child of a
// wisp that is not here cannot tell whether that wisp is blocked from outside.
//
// COST (gastownhall/beads#6288, wy-pyrus4, and B2 of the #6506 review). Three
// earlier cuts were built and measured at 1000 issues / 2870 deps, and all
// three are in the rejected pile:
//
//   - a per-row correlated EXISTS whose body held two more correlated EXISTS,
//     the exact shape #6288 removed from the waits-for leg: 3.3-3.7x on the
//     full repair, 5.2x on the doctor count (the review's own numbers);
//   - the set as a derived table LEFT JOINed on p.id: ~30x. A join operand is
//     a join operand — dolt planned it as the inner side of a nested loop and
//     rebuilt it per outer row, the correlated shape again in a derived
//     table's clothes;
//   - the same set built with WITH RECURSIVE: it blew the driver read timeout
//     on the full repair (see subtreeWalkDepth).
//
// What is left is uncorrelated by construction. Nothing below references the
// outer row — that is what lets the engine build the set once per statement
// and probe it, the form this file already relies on for
// `<alias>.id IN (the should-be-blocked union)` (bd-t9ypt) — and it is the
// property to preserve in any future edit here. Measured against a leg with
// no exogeneity test at all: 3 ms -> 18 ms per evaluation, flat in the number
// of parent-child rows the leg returns.
//
// The set is also confined to the statement's own work:
//
//   - cand is the set of parents the caller can possibly ask about. A batched
//     mark/unmark passes ONE source — its own dependency table, carrying its
//     own batch scope (see scopedExplainedParentsInTx) — so it computes the
//     handful of parents above the batch. The unbatched read passes both
//     tables unscoped, which is every row that is a parent at all and already
//     prunes most of the table. The alias must be d: batchScopeSQL is written
//     against d.issue_id and is spliced verbatim. It binds to this subquery's
//     own FROM, not an enclosing leg's d — the set is not lateral and
//     correlates with nothing outside it.
//   - rsn is those parents' blocking reason rows, and is where the reason
//     predicate is paid: once per candidate parent row, not once per
//     parent-child edge in the leg.
//
// blockingReasonSQL mirrors union legs 1, 2 and 5 row by row; keep them in
// step or the "solely explained by its own subtree" reading silently drifts.
//
// parentTable/parentDepTable/parentCol describe the PARENT's kind (issues +
// dependencies, or wisps + wisp_dependencies); cands name the tables the
// parent-child edges are read from, which are the CHILDREN's. A parent-child
// edge lives in the child's table and names the parent in parentCol.
//
//nolint:gosec // G201: every argument is a constant from the two call sites; scope is batchScopeSQL.
func parentsExplainedBySubtreeSQL(parentTable, parentDepTable, parentCol string, cands []candSource) string {
	var cand strings.Builder
	for i, c := range cands {
		if i > 0 {
			cand.WriteString("\n\t\t    UNION")
		}
		fmt.Fprintf(&cand, `
		    SELECT DISTINCT d.%[1]s FROM %[2]s d
		    WHERE d.issue_id IS NOT NULL %[3]s
		      AND d.type = 'parent-child'
		      AND d.%[1]s IS NOT NULL`, parentCol, c.depTable, c.scope)
	}
	return parentsExplainedBySubtreeCoreSQL(parentTable, parentDepTable, cand.String())
}

// candByIDSQL is a cand body naming parents by id: placeholders is a rendered
// IN-list, for explainedAmongInTx.
//
//nolint:gosec // G201: parentTable is a constant; placeholders carries only ? placeholders.
func candByIDSQL(parentTable, placeholders string) string {
	return fmt.Sprintf(`
		    SELECT c.id FROM %s c WHERE c.id IN (%s)`, parentTable, placeholders)
}

// parentsExplainedBySubtreeCoreSQL is parentsExplainedBySubtreeSQL over a cand
// body the caller has already built: the parent-child rows of a batch or of the
// whole store (parentsExplainedBySubtreeSQL), or parents named by id
// (candByIDSQL). Each row is a parent id and 1 when the parent is undecided (it
// holds a dangling wisp edge), 0 when it is explained; queryExplainedParents
// splits them. A parent that is closed or pinned is neither: it is never
// blocked, so it darkens no child whatever it holds.
//
//nolint:gosec // G201: parentTable and parentDepTable are constants; cand is built from constants and ? placeholders.
func parentsExplainedBySubtreeCoreSQL(parentTable, parentDepTable, cand string) string {
	// The ancestor walk: one pair of indexed LEFT JOINs per level, on the
	// dependency tables directly. Materializing the parent-child edges into a
	// CTE first was measured at 6.6x on the batched write path — the whole
	// edge set of the store, built per statement, to answer a question about a
	// handful of rows. Joined straight onto idx_dependencies_issue it is a
	// probe per level per reason row instead.
	var joins, match strings.Builder
	child := "r.tgt"
	for i := 1; i <= subtreeWalkDepth; i++ {
		ia, wa := fmt.Sprintf("i%d", i), fmt.Sprintf("w%d", i)
		fmt.Fprintf(&joins, `
		    LEFT JOIN dependencies %[1]s ON %[1]s.issue_id = %[3]s AND %[1]s.type = 'parent-child'
		    LEFT JOIN wisp_dependencies %[2]s ON %[2]s.issue_id = %[3]s AND %[2]s.type = 'parent-child'`,
			ia, wa, child)
		// The level's parent, whichever table and column carries it. A child
		// has one parent-child edge, so at most one of these four is set.
		parent := fmt.Sprintf(
			"COALESCE(%[1]s.depends_on_issue_id, %[1]s.depends_on_wisp_id, %[2]s.depends_on_issue_id, %[2]s.depends_on_wisp_id)",
			ia, wa)
		if i > 1 {
			match.WriteString(" OR ")
		}
		fmt.Fprintf(&match, "%s = r.pid", parent)
		child = parent
	}

	return fmt.Sprintf(`
		  WITH
		  cand(pid) AS (%[3]s
		  ),
		  rsn(pid, tgt, dangling) AS (
		    SELECT pr.issue_id, COALESCE(pr.depends_on_issue_id, pr.depends_on_wisp_id),
		           CASE WHEN %[7]s THEN 1 ELSE 0 END
		    FROM %[2]s pr
		    JOIN %[1]s pp ON pp.id = pr.issue_id
		    JOIN cand ON cand.pid = pr.issue_id
		    LEFT JOIN wisps rw ON rw.id = pr.depends_on_wisp_id
		    WHERE pp.is_blocked = 1
		      AND pp.status <> 'closed' AND pp.status <> 'pinned'
		      AND pr.issue_id IS NOT NULL
		      AND ( (%[4]s) OR %[7]s )
		  ),
		  bits(pid, exogenous, dangling) AS (
		    SELECT r.pid,
		           MAX(CASE WHEN r.dangling = 1 THEN 0 WHEN %[6]s THEN 0 ELSE 1 END),
		           MAX(r.dangling)
		    FROM rsn r%[5]s
		    GROUP BY r.pid
		  )
		  SELECT b.pid, b.dangling FROM bits b WHERE b.exogenous = 0
	`, parentTable, parentDepTable, cand,
		blockingReasonSQL("pr"), joins.String(), match.String(), danglingWispEdgeSQL("pr", "rw"))
}

// blockingReasonSQL is the row-level spelling of "this dependency row is,
// right now, a reason for its own issue to be blocked" — union legs 1 and 2
// (an open blocks/conditional-blocks target, issue or wisp) and leg 5 (a
// blocking waits-for gate), for a dependency row aliased rowAlias.
//
// It reads only what this database can see. A row naming a wisp that is not
// here is not a reason by it (its EXISTS finds no wisp), and both callers read
// it beside this predicate, as danglingWispEdgeSQL: the explained-parent read
// makes its parent undecided, and withoutReasonAmongInTx counts it as a row of
// the parent's own, so such a parent is never taken for transparent.
//
// It is a per-ROW predicate, but both callers evaluate it in a set, over a
// bounded list of parents' rows: parentsExplainedBySubtreeSQL's aggregate,
// over the reason rows of the parents that statement can be asked about, once,
// and withoutReasonAmongInTx, over the rows of the parents the walk could not
// otherwise decide, by id. It is never applied per row of an enclosing leg —
// that is the correlated shape #6288 removed and B2 measured.
//
//nolint:gosec // G201: rowAlias is a constant; waitsForGateBlockedSQLFor rewrites a constant template.
func blockingReasonSQL(rowAlias string) string {
	return fmt.Sprintf(`
		        ( ( (%[1]s.type = 'blocks' OR %[1]s.type = 'conditional-blocks')
		            AND ( EXISTS (SELECT 1 FROM issues bt
		                          WHERE bt.id = %[1]s.depends_on_issue_id
		                            AND bt.status <> 'closed' AND bt.status <> 'pinned')
		               OR EXISTS (SELECT 1 FROM wisps bt
		                          WHERE bt.id = %[1]s.depends_on_wisp_id
		                            AND bt.status <> 'closed' AND bt.status <> 'pinned') ) )
		          OR ( %[1]s.type = 'waits-for' AND (%[2]s) ) )
	`, rowAlias, waitsForGateBlockedSQLFor(rowAlias))
}

// CountIsBlockedInconsistenciesInTx reports how many issue and wisp rows carry a
// stale is_blocked flag — rows a full recompute would flip. It is the read-only
// detection behind the bd doctor "Blocked State" check (bd-6dnrw.37); the
// repair is RecomputeAllIsBlockedInTx.
//
// Both derive their membership test from the one union
// builder above, and they are pinned together by the blocked-consistency
// lockstep test: a converged database counts 0, and any row this counts is one
// a recompute pass changes. The count is a single-pass lower bound — a
// corrupted parent's children are only counted on the pass after the parent is
// fixed — which is exactly what a "needs repair?" check wants: nonzero means run
// --fix, zero means consistent.
func CountIsBlockedInconsistenciesInTx(ctx context.Context, tx DBTX) (int64, error) {
	holders, err := danglingWispHoldersInTx(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("count stale is_blocked issues: %w", err)
	}
	// One exogeneity read for both statements; each splices the union twice,
	// so the ids are bound twice per statement, then the undecided rows' own.
	explained, err := subtreeExplainedParentsInTx(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("count stale is_blocked issues: %w", err)
	}
	var total int64
	for _, t := range []struct {
		table, alias, depTable, what string
		left                         undecidedRows
	}{
		{"issues", "i", "dependencies", "issues", explained.undecidedRowsFor(holders.issues)},
		{"wisps", "w", "wisp_dependencies", "wisps", explained.undecidedRowsFor(holders.wisps)},
	} {
		args := append(append(explained.args(), explained.args()...), t.left.args()...)
		n, err := countRows(ctx, tx, countStaleIsBlockedSQL(t.table, t.alias, t.depTable, explained, t.left), args...)
		if err != nil {
			if t.table == "wisps" && isTableNotExistError(err) {
				return total, nil
			}
			return 0, fmt.Errorf("count stale is_blocked %s: %w", t.what, err)
		}
		total += n
	}
	return total, nil
}

// countStaleIsBlockedSQL builds a COUNT(*) of rows whose stored is_blocked
// disagrees with the dependency graph for one table (issues or wisps). The two
// OR branches mirror the full repair's statements exactly:
//
//   - mark-eligible:   is_blocked = 0 on an open row that should be blocked,
//     which the fixpoint marks or, for an undecided row, the closing mark
//     does (markUndecidedAllBlockedSQL).
//   - unmark-eligible: is_blocked = 1 on a row that should not be blocked
//     (closed/pinned, or its id absent from the should-be-blocked union — the
//     set-membership form of De Morgan over the shouldBeBlocked disjunction),
//     unless it is an undecided row (left), which the repair leaves as found.
//
// is_blocked is 0 or 1, so the branches are mutually exclusive and the count is
// their sum. Built on the uncorrelated union for the same
// reason as the full repair (bd-t9ypt): the correlated disjunction form ran per
// outer row and cost whole-table scans per branch. table/alias/depTable are
// hardcoded constants supplied by the only two callers.
//
// Like every single pass it reads stored flags as it finds them, so a parent
// with a stale 1 makes its children read mark-eligible. A decided child is
// then one the repair's first pass really does mark (and a later pass clears
// again). An undecided child is not: the repair marks it only from the
// converged graph, so it never touches it. The count can then exceed what the
// repair corrects. It is never below what the first pass corrects, and it is
// 0 after any repair.
//
//nolint:gosec // G201: table, alias, and depTable are constant; only the constant gate SQL is interpolated.
func countStaleIsBlockedSQL(table, alias, depTable string, explained subtreeExplainedParents, left undecidedRows) string {
	union := shouldBeBlockedIDsUnionPrecomputedSQL(depTable, explained)
	if !left.empty() {
		// The mark branch reads held rows as the closing mark does.
		union = withPresentSpawnerGate(union)
	}
	return fmt.Sprintf(`
		SELECT COUNT(*) FROM %[1]s %[2]s
		WHERE
		  ( %[2]s.is_blocked = 0
		    AND %[2]s.status <> 'closed' AND %[2]s.status <> 'pinned'
		    AND %[2]s.id IN (%[3]s) )
		  OR
		  ( %[2]s.is_blocked = 1
		    AND ( %[2]s.status = 'closed' OR %[2]s.status = 'pinned'
		          OR ( %[2]s.id NOT IN (%[3]s)%[4]s ) ) )
	`, table, alias, union, left.notInSQL(alias, depTable, ""))
}

// CountIsBlockedHeldInTx reports how many open issue and wisp rows are
// undecided (undecidedRows) and have no blocker this database can see: the
// rows whose is_blocked the recompute leaves as found, because neither value
// can be derived here. They hold a dependency on a wisp this database has no
// row for (danglingWispEdgeSQL), or they are the children of a parent whose
// blockedness such a dependency may decide. It is the read behind the second
// half of the bd doctor "Blocked State" line.
//
// Zero on a database that holds every wisp its edges name. Nonzero is the
// ordinary state of a clone of a database that blocks issues on wisps, and on
// the database that mints the wisps it names edges left behind by a delete
// that bypassed the wisp-delete cascade.
//
// It asks first whether any dangling edge exists at all, one probe per
// dependency table that stops at the first hit, so a database that holds
// every wisp its edges name, which is every database that mints its own, pays
// two probes and not the exogeneity read.
func CountIsBlockedHeldInTx(ctx context.Context, tx DBTX) (int64, error) {
	holders, err := danglingWispHoldersInTx(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("count held is_blocked rows: %w", err)
	}
	if !holders.issues && !holders.wisps {
		// No row holds a dangling edge, so no parent is undecided either: the
		// read below would return none.
		return 0, nil
	}
	explained, err := subtreeExplainedParentsInTx(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("count held is_blocked rows: %w", err)
	}
	var total int64
	for _, t := range []struct {
		table, alias, depTable string
		left                   undecidedRows
	}{
		{"issues", "i", "dependencies", explained.undecidedRowsFor(holders.issues)},
		{"wisps", "w", "wisp_dependencies", explained.undecidedRowsFor(holders.wisps)},
	} {
		if t.left.empty() {
			continue
		}
		n, err := countRows(ctx, tx, countHeldIsBlockedSQL(t.table, t.alias, t.depTable, explained, t.left),
			append(t.left.args(), explained.args()...)...)
		if err != nil {
			if t.table == "wisps" && isTableNotExistError(err) {
				return total, nil
			}
			return 0, fmt.Errorf("count held is_blocked %s: %w", t.table, err)
		}
		total += n
	}
	return total, nil
}

// countHeldIsBlockedSQL counts one table's rows for
// CountIsBlockedHeldInTx; left must not be empty. The undecided set comes
// first, so its ids bind before the union's, in text order.
//
//nolint:gosec // G201: table, alias, and depTable are constants from the only two callers.
func countHeldIsBlockedSQL(table, alias, depTable string, explained subtreeExplainedParents, left undecidedRows) string {
	// The union reads held rows as the closing mark does: a waits-for gate
	// only onto a present spawner (withPresentSpawnerGate).
	return fmt.Sprintf(`
		SELECT COUNT(*) FROM %[1]s %[2]s
		WHERE %[2]s.status <> 'closed' AND %[2]s.status <> 'pinned'
		  AND %[2]s.id IN (%[3]s)
		  AND %[2]s.id NOT IN (%[4]s)
	`, table, alias, left.selectSQL(depTable, ""),
		withPresentSpawnerGate(shouldBeBlockedIDsUnionPrecomputedSQL(depTable, explained)))
}

// hasDanglingWispEdgeInTx reports whether depTable holds at least one dangling
// wisp edge (danglingWispEdgeSQL) on a row with a holder. It is the select
// danglingWispHoldersSQL spells, cut off at the first row, so it costs one
// primary-key probe of wisps per wisp edge it reads before the first hit.
//
//nolint:gosec // G201: depTable is a constant from the one caller.
func hasDanglingWispEdgeInTx(ctx context.Context, tx DBTX, depTable string) (bool, error) {
	rows, err := tx.QueryContext(ctx, danglingWispHoldersSQL(depTable, "", danglingWispEdgeTypesSQL, "")+"\n\t\tLIMIT 1")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := rows.Next()
	return found, rows.Err()
}

// countRows runs a single COUNT(*) query and returns the scalar.
func countRows(ctx context.Context, tx DBTX, query string, args ...any) (int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
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
