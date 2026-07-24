package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// rebaseActor is the audit-trail actor recorded on the 'renamed' events that the
// renumber writes through the canonical issue-rename primitive.
const rebaseActor = "bd-dolt-rebase"

// txBeginner is satisfied by *sql.DB and *sql.Conn — the concrete types the
// rebase mechanic runs on. The renumber phase is pure DML (no Dolt stored
// procedures), so it runs inside a real transaction, which lets it reuse the
// tx-based canonical rename primitive and roll back cleanly on any error.
type txBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// This file implements `bd dolt rebase`: reconciling cross-clone hierarchical
// child-ID collisions that a plain pull cannot auto-merge (#4796, the
// data-level sibling of the #4259 schema-level PK fork).
//
// The hazard: child ids are assigned as "parent.N" where N comes from a purely
// LOCAL counter (issueops.GetNextChildIDTx). Two clones that each add a child
// under the same parent while offline both mint the same id (parent.71), so on
// sync the issues table has an add/add PK collision AND the child_counters row
// both-changed — neither of which the pull settle machinery will auto-resolve,
// so the pull aborts and the operator is left with a manual re-ID runbook.
//
// The fix here is pragmatic: renumber the losing side's colliding children to
// free ids so the collision disappears, then complete the merge, resolving the
// child_counters row to the true high-water mark. It is NOT the root-cause fix.
// The root cause is that the visible "parent.N" numbering is being used as the
// unique key; the durable identifier should be stable and unique while the
// visible hierarchical number is derived dynamically from parentage. That is a
// larger schema change tracked separately; this command unblocks sync today.
//
// content_hash is set NULL on every renumbered row. content_hash is used only
// for compaction/dedup, never for merge, so a NULL is benign and converges on
// the next compaction pass — the same choice the manual 2026-06-02 and
// 2026-07-14 reconciles made. (Recomputing it would need the full issue row;
// left as a follow-up.)

// rebaseCollision is one colliding child id and the parent it hangs under.
type rebaseCollision struct {
	id     string
	parent string
}

// RebaseChildCollisions reconciles hierarchical child-ID collisions between the
// local branch (HEAD) and remoteRef, which must already be fetched and present
// locally. localDominates selects which side keeps its ids: false
// (remote-dominates, the default) renumbers the LOCAL colliding children; true
// renumbers the REMOTE's on a scratch branch. On success the merge is committed
// and a report of every renumber is returned. When there are no collisions the
// report's Renumbered is empty and no merge is performed (the caller should
// fall back to a plain pull).
//
// db must be a single session (a pinned *sql.Conn, or a *sql.DB whose pool
// holds one connection): the merge session flags and the branch checkouts must
// be visible across every statement.
func RebaseChildCollisions(ctx context.Context, db DBConn, remoteRef string, localDominates bool) (report *storage.RebaseReport, retErr error) {
	if err := issueops.ValidateRef(remoteRef); err != nil {
		return nil, fmt.Errorf("invalid remote ref: %w", err)
	}

	direction := "remote-dominates"
	if localDominates {
		direction = "local-dominates"
	}
	report = &storage.RebaseReport{Direction: direction, CountersSet: map[string]int{}}

	// Merge base: an id present on both sides but absent here is an independent
	// add/add — the collision signature. DOLT_MERGE_BASE returns a commit hash
	// (a valid ref for the subsequent AS OF).
	base, err := scalarString(ctx, db, fmt.Sprintf("SELECT DOLT_MERGE_BASE('HEAD', '%s')", remoteRef))
	if err != nil {
		return nil, fmt.Errorf("compute merge base with %s: %w", remoteRef, err)
	}
	if err := issueops.ValidateRef(base); err != nil {
		return nil, fmt.Errorf("merge base is not a usable ref: %w", err)
	}

	collisions, err := detectCollisions(ctx, db, remoteRef, base)
	if err != nil {
		return nil, err
	}
	if len(collisions) == 0 {
		return report, nil // nothing to rebase; caller falls back to pull
	}

	// Back up HEAD before mutating anything (idempotent: a re-run tolerates the
	// tag already existing).
	head8, err := scalarString(ctx, db, "SELECT LEFT(HASHOF('HEAD'), 8)")
	if err != nil {
		return nil, fmt.Errorf("read HEAD hash for backup tag: %w", err)
	}
	backupTag := "bd-pre-rebase-" + head8
	if _, err := db.ExecContext(ctx, "CALL DOLT_TAG(?, 'HEAD')", backupTag); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		return nil, fmt.Errorf("create backup tag %s: %w", backupTag, err)
	}
	report.BackupTag = backupTag

	// From here on the mechanic mutates the branch in several committed steps
	// (renumber, then merge, then — for local-dominates — a swap). A failure part
	// way through would otherwise strand those commits: e.g. a renumber that
	// commits but a merge that then aborts leaves the branch renumbered-but-
	// unmerged, with no bd command to undo it. Make the whole operation atomic by
	// hard-resetting the branch back to the pre-mutation backup tag on any error,
	// so it either fully reconciles or leaves the DB exactly as it was found.
	defer func() {
		if retErr == nil {
			return
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_RESET('--hard', ?)", backupTag); err != nil {
			retErr = fmt.Errorf("%w; the automatic restore to backup tag %s also failed (%v) — "+
				"recover by re-cloning from the remote (bd bootstrap) or restoring the tag with Dolt directly", retErr, backupTag, err)
		}
	}()

	// Both directions run entirely on the LOCAL working branch. A checked-out
	// remote ref cannot be used: the rename primitive touches the wisp_* tables,
	// which are session-local working-set tables never committed to history, so
	// they vanish the moment the working set is reset to a committed ref.
	//
	// So first renumber the local colliding children to free ids and merge the
	// remote in — the remote-dominates result: the remote's row sits at the
	// contested id, the local row at the new id.
	renum, counters, err := renumberCollisions(ctx, db, collisions, []string{remoteRef, base})
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)",
		fmt.Sprintf("rebase: renumber %d colliding child id(s) (%s) [#4796]", len(renum), direction)); err != nil {
		return nil, fmt.Errorf("commit renumber: %w", err)
	}
	report.Renumbered = renum
	for k, v := range counters {
		report.CountersSet[k] = v
	}

	if err := mergeAndSettleRebase(ctx, db, remoteRef, report); err != nil {
		return report, err
	}

	// For local-dominates, swap each contested pair so the LOCAL row reclaims the
	// contested id and the remote's row takes the new id — the inverse of what the
	// merge just left. The swap runs on the local branch, so the wisp_* tables the
	// rename primitive needs are present.
	if localDominates {
		if err := swapRenumberedSubtrees(ctx, db, renum); err != nil {
			return report, err
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)",
			fmt.Sprintf("rebase: keep %d local child id(s), renumber the remote's (local-dominates) [#4796]", len(renum))); err != nil {
			return report, fmt.Errorf("commit local-dominates swap: %w", err)
		}
	}
	return report, nil
}

// detectCollisions returns the child ids that exist on BOTH HEAD and remoteRef
// but not at the merge base (base) — independent add/add assignments of the
// same parent.N id. Only collision "roots" are returned: a colliding id whose
// ancestor also collides is left to the subtree rewrite of that ancestor.
func detectCollisions(ctx context.Context, db DBConn, remoteRef, base string) ([]rebaseCollision, error) {
	//nolint:gosec // G201: remoteRef and base are validated by ValidateRef; AS OF requires literals.
	q := fmt.Sprintf(`
		SELECT h.id FROM issues h
		WHERE h.id LIKE '%%.%%'
		  AND h.id IN (SELECT id FROM issues AS OF '%s')
		  AND h.id NOT IN (SELECT id FROM issues AS OF '%s')`, remoteRef, base)
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("detect child-id collisions: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan collision id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Keep only roots: drop any id that is a descendant of another colliding id.
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	var out []rebaseCollision
	for _, id := range ids {
		if hasCollidingAncestor(id, set) {
			continue
		}
		parent, _, ok := issueops.ParseHierarchicalID(id)
		if !ok {
			continue // not hierarchical; the LIKE guard should already exclude these
		}
		out = append(out, rebaseCollision{id: id, parent: parent})
	}
	// Deterministic order so multi-collision renumbering is reproducible.
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

// hasCollidingAncestor reports whether any strict ancestor of id is itself in
// the colliding set (so id will be carried by that ancestor's subtree rewrite).
func hasCollidingAncestor(id string, set map[string]bool) bool {
	for i := strings.LastIndex(id, "."); i > 0; i = strings.LastIndex(id[:i], ".") {
		if set[id[:i]] {
			return true
		}
	}
	return false
}

// renumberCollisions renumbers each collision's subtree on the currently
// checked-out branch to a free child number under its parent, chosen above the
// max child number seen on the current branch and every ref in otherRefs (so
// the new id collides with neither side). It returns the renumber records and
// the final high-water last_child per affected parent, and bumps child_counters
// accordingly.
func renumberCollisions(ctx context.Context, db DBConn, collisions []rebaseCollision, otherRefs []string) ([]storage.RebaseRenumber, map[string]int, error) {
	// Seed each affected parent's running counter with the current high-water
	// child number across the current branch and every otherRef.
	next := map[string]int{}
	for _, c := range collisions {
		if _, seen := next[c.parent]; seen {
			continue
		}
		m, err := maxChildNumber(ctx, db, "", c.parent)
		if err != nil {
			return nil, nil, err
		}
		for _, ref := range otherRefs {
			rm, err := maxChildNumber(ctx, db, ref, c.parent)
			if err != nil {
				return nil, nil, err
			}
			if rm > m {
				m = rm
			}
		}
		next[c.parent] = m
	}

	// The renumber is pure DML, so run it in one transaction that reuses the
	// canonical issue-rename primitive (issueops.UpdateIssueIDInTx) and rolls
	// back cleanly on any error. The primitive rewrites issues.id AND rekeys the
	// dependency edges, whose primary key is DERIVED from the issue id
	// (depid.New(issue_id, target), #4259) — the FK cascade alone would leave
	// that surrogate key stale and re-fork it on the next merge.
	tb, ok := db.(txBeginner)
	if !ok {
		return nil, nil, fmt.Errorf("rebase renumber needs a *sql.DB or *sql.Conn, got %T", db)
	}
	tx, err := tb.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin renumber transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	var records []storage.RebaseRenumber
	for _, c := range collisions {
		next[c.parent]++
		newRoot := fmt.Sprintf("%s.%d", c.parent, next[c.parent])
		if err := renameSubtreeInTx(ctx, tx, c.id, newRoot); err != nil {
			return nil, nil, err
		}
		records = append(records, storage.RebaseRenumber{OldID: c.id, NewID: newRoot, Parent: c.parent})
	}

	// Bump each affected parent's counter to its new high-water mark.
	counters := map[string]int{}
	for parent, n := range next {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE last_child = GREATEST(last_child, VALUES(last_child))`,
			parent, n); err != nil {
			return nil, nil, fmt.Errorf("bump child_counters for %s: %w", parent, err)
		}
		counters[parent] = n
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit renumber transaction: %w", err)
	}
	return records, counters, nil
}

// subtreeIDs returns the id of root and every descendant (id LIKE "root.%")
// across BOTH the durable issues table and the session-local wisps table, each
// of which the renumber renames individually. A durable (synced) root can have
// wisp children — ephemeral issues created under it in the current session —
// and those must move with the root: leaving a wisp child at oldRoot.* while its
// parent becomes newRoot orphans it and diverges identity from hierarchy (the
// wisp-under-durable shape maphew flagged on #4844). The wisps table is optional
// on older schemas, so a missing table degrades cleanly to "issues only".
func subtreeIDs(ctx context.Context, tx *sql.Tx, root string) ([]string, error) {
	ids, err := subtreeIDsFromTable(ctx, tx, "issues", root)
	if err != nil {
		return nil, err
	}
	wispIDs, err := subtreeIDsFromTable(ctx, tx, "wisps", root)
	if err != nil && !dberrors.IsTableNotExist(err) {
		return nil, err
	}
	return append(ids, wispIDs...), nil
}

// subtreeIDsFromTable enumerates root and its descendants within one table.
// table is a fixed literal ("issues" or "wisps"); only root is user data and it
// is bound as a parameter.
func subtreeIDsFromTable(ctx context.Context, tx *sql.Tx, table, root string) ([]string, error) {
	//nolint:gosec // G201: table is a caller-fixed literal; root is a bind param.
	q := fmt.Sprintf("SELECT id FROM %s WHERE id = ? OR id LIKE CONCAT(?, '.%%')", table)
	rows, err := tx.QueryContext(ctx, q, root, root)
	if err != nil {
		return nil, fmt.Errorf("enumerate %s subtree of %s: %w", table, root, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan subtree id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// renameSubtreeInTx renames oldRoot and every descendant (oldRoot.* -> newRoot.*)
// through the canonical issue-rename primitive, which rewrites issues.id AND
// rekeys the derived-PK dependency edges. The "parent.N" id string is not a
// foreign key, so descendants must be renamed explicitly (a leaf has none).
func renameSubtreeInTx(ctx context.Context, tx *sql.Tx, oldRoot, newRoot string) error {
	subtree, err := subtreeIDs(ctx, tx, oldRoot)
	if err != nil {
		return err
	}
	for _, oldID := range subtree {
		newID := newRoot + oldID[len(oldRoot):]
		// Load the current row from whichever table owns this id. A wisp child of
		// a durable root lives in wisps, not issues, so reading issues alone would
		// miss it (no rows) and abort the rename. UpdateIssueIDInTx re-checks
		// wisp-ness and routes the write to the matching table.
		table := "issues"
		if issueops.IsActiveWispInTx(ctx, tx, oldID) {
			table = "wisps"
		}
		var iss types.Issue
		//nolint:gosec // G201: table is chosen from two fixed literals by wisp routing; oldID is a bind param.
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COALESCE(title, ''), COALESCE(description, ''), COALESCE(design, ''),
			       COALESCE(acceptance_criteria, ''), COALESCE(notes, '')
			FROM %s WHERE id = ?`, table), oldID).Scan(
			&iss.Title, &iss.Description, &iss.Design, &iss.AcceptanceCriteria, &iss.Notes); err != nil {
			return fmt.Errorf("load %s %s for rename: %w", table, oldID, err)
		}
		if err := issueops.UpdateIssueIDInTx(ctx, tx, oldID, newID, &iss, rebaseActor); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", oldID, newID, err)
		}
	}
	return nil
}

// swapRenumberedSubtrees inverts a completed remote-dominates renumber so the
// LOCAL row reclaims each contested id and the remote's row takes the new id.
// After the merge, id r.OldID holds the remote's row and r.NewID holds the local
// row; each pair is swapped via a free temporary id (three subtree renames). Runs
// on the local branch so the session-local wisp_* tables are present.
func swapRenumberedSubtrees(ctx context.Context, db DBConn, records []storage.RebaseRenumber) error {
	tb, ok := db.(txBeginner)
	if !ok {
		return fmt.Errorf("rebase swap needs a *sql.DB or *sql.Conn, got %T", db)
	}
	tx, err := tb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin swap transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	for i, r := range records {
		// A temp id far above any real child number, unique per pair, so no rename
		// in the three-step swap collides with a live id.
		temp := fmt.Sprintf("%s.%d", r.Parent, 1_000_000+i)
		if err := renameSubtreeInTx(ctx, tx, r.OldID, temp); err != nil { // remote -> temp
			return err
		}
		if err := renameSubtreeInTx(ctx, tx, r.NewID, r.OldID); err != nil { // local -> contested id
			return err
		}
		if err := renameSubtreeInTx(ctx, tx, temp, r.NewID); err != nil { // remote -> new id
			return err
		}
	}
	return tx.Commit()
}

// maxChildNumber returns the highest DIRECT child number under parent as seen
// at ref (empty ref = the current working set). Mirrors the child-scan in
// issueops.GetNextChildIDTx: direct children match "parent.%" but not
// "parent.%.%".
func maxChildNumber(ctx context.Context, db DBConn, ref, parent string) (int, error) {
	query := `
		SELECT COALESCE(MAX(CAST(SUBSTRING_INDEX(id, '.', -1) AS UNSIGNED)), 0)
		FROM issues%s
		WHERE id LIKE CONCAT(?, '.%%') AND id NOT LIKE CONCAT(?, '.%%.%%')`
	asOf := ""
	if ref != "" {
		if err := issueops.ValidateRef(ref); err != nil {
			return 0, fmt.Errorf("invalid ref for max-child scan: %w", err)
		}
		//nolint:gosec // G201: ref validated above; AS OF requires a literal.
		asOf = fmt.Sprintf(" AS OF '%s'", ref)
	}
	//nolint:gosec // G201: only the validated AS OF clause is interpolated; parent is a bind param.
	q := fmt.Sprintf(query, asOf)
	var n int
	if err := db.QueryRowContext(ctx, q, parent, parent).Scan(&n); err != nil {
		return 0, fmt.Errorf("max child number for %s%s: %w", parent, asOf, err)
	}
	return n, nil
}

// mergeAndSettleRebase merges winRef into HEAD after the losing side has been
// renumbered, then settles: it resolves the child_counters both-changed row to
// the true high-water mark (the half the standard pull settle refuses), lets
// TryAutoResolveMergeConflicts clear the remaining safe classes, repairs FK
// cascade violations, and commits. Any unresolved conflict aborts and restores.
func mergeAndSettleRebase(ctx context.Context, db DBConn, winRef string, report *storage.RebaseReport) error {
	if err := issueops.ValidateRef(winRef); err != nil {
		return fmt.Errorf("invalid merge ref: %w", err)
	}
	preMergeClean := workingSetClean(ctx, db)
	if _, err := db.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		return fmt.Errorf("set dolt_allow_commit_conflicts: %w", err)
	}
	if _, err := db.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 1"); err != nil {
		return fmt.Errorf("set dolt_force_transaction_commit: %w", err)
	}

	_, mergeErr := db.ExecContext(ctx, "CALL DOLT_MERGE(?)", winRef)
	if mergeErr != nil && strings.Contains(mergeErr.Error(), "up to date") {
		mergeErr = nil
	}
	if mergeErr != nil {
		abortMerge(ctx, db, preMergeClean)
		return fmt.Errorf("merge %s after renumber: %w", winRef, mergeErr)
	}

	// Resolve the child_counters both-changed conflict to the real high-water
	// mark per affected parent, then keep "ours" (which now holds that value).
	if err := resolveChildCountersToHighWater(ctx, db, report); err != nil {
		abortMerge(ctx, db, preMergeClean)
		return err
	}

	// Clear the remaining safe conflict classes (metadata / audit-only deps /
	// issues-LWW). A refusal here means an unrelated real conflict — abort.
	if _, err := TryAutoResolveMergeConflicts(ctx, db); err != nil {
		abortMerge(ctx, db, preMergeClean)
		return err
	}
	if conflicts, err := GetConflicts(ctx, db); err != nil {
		abortMerge(ctx, db, preMergeClean)
		return err
	} else if len(conflicts) > 0 {
		tables := make([]string, len(conflicts))
		for i, c := range conflicts {
			tables[i] = c.Field
		}
		abortMerge(ctx, db, preMergeClean)
		return fmt.Errorf("rebase merge left conflicts in %s that require operator resolution; merge aborted",
			strings.Join(tables, ", "))
	}

	if repairedViol, hadViol, err := TryRepairFKCascadeViolations(ctx, db); err != nil {
		abortMerge(ctx, db, preMergeClean)
		return err
	} else if hadViol && !repairedViol {
		// An unrepaired FK cascade must never be committed. The commit gate below
		// cannot be relied on to catch it: dolt_force_transaction_commit=1 (set
		// above) disables DOLT_COMMIT's refusal of a violated working set, so a
		// forced -Am commit would persist the violation. Guard it explicitly here,
		// exactly as the sibling pull settle does (mergesettle.go).
		abortMerge(ctx, db, preMergeClean)
		return fmt.Errorf("rebase merge left constraint violations bd cannot auto-repair; inspect dolt_constraint_violations and resolve before retrying")
	}

	// -Am (stage all + commit): the incoming non-conflicted merge rows (e.g. the
	// winning side's new children) land in the working set unstaged, unlike the
	// standard settle where the conflict resolution stages the issues table. The
	// conflict-free state was asserted above and any FK cascade violation was
	// repaired-or-aborted by the explicit guard above (the forced-commit flag
	// means we cannot lean on DOLT_COMMIT to refuse one), so staging all is safe.
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)",
		fmt.Sprintf("rebase: merge after collision renumber (%s) [#4796]", report.Direction)); err != nil {
		if strings.Contains(err.Error(), "nothing to commit") {
			return nil
		}
		abortMerge(ctx, db, preMergeClean)
		return fmt.Errorf("commit rebase merge: %w", err)
	}
	return nil
}

// resolveChildCountersToHighWater rewrites each conflicted child_counters row
// to the true maximum direct-child number present in the merged working set,
// then resolves the table with --ours (which now holds that value). This is the
// second half of the mechanic: the merge clears the issues collision, but the
// counter row is a both-changed conflict the standard settle never touches.
func resolveChildCountersToHighWater(ctx context.Context, db DBConn, report *storage.RebaseReport) error {
	rows, err := db.QueryContext(ctx,
		"SELECT COALESCE(our_parent_id, their_parent_id, base_parent_id) FROM dolt_conflicts_child_counters")
	if err != nil {
		// No conflict on this table (or Dolt version without the system table):
		// nothing to resolve here.
		if strings.Contains(err.Error(), "dolt_conflicts_child_counters") {
			return nil
		}
		return fmt.Errorf("query child_counters conflicts: %w", err)
	}
	var parents []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan child_counters conflict: %w", err)
		}
		parents = append(parents, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(parents) == 0 {
		return nil
	}

	for _, p := range parents {
		trueMax, err := maxChildNumber(ctx, db, "", p)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx,
			"UPDATE child_counters SET last_child = ? WHERE parent_id = ?", trueMax, p); err != nil {
			return fmt.Errorf("set child_counters high-water for %s: %w", p, err)
		}
		report.CountersSet[p] = trueMax
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CONFLICTS_RESOLVE('--ours', 'child_counters')"); err != nil {
		return fmt.Errorf("resolve child_counters conflicts: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('child_counters')"); err != nil {
		return fmt.Errorf("stage resolved child_counters: %w", err)
	}
	return nil
}

// scalarString runs a query expected to return a single string column.
func scalarString(ctx context.Context, db DBConn, query string, args ...any) (string, error) {
	var s string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&s); err != nil {
		return "", err
	}
	return s, nil
}
