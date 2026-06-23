package versioncontrolops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
)

// memoryConfigKeyPrefix is the config-table key prefix under which `bd remember`
// stores persistent memories. It is sourced from the shared kvkeys package so it
// can never drift from the prefix cmd/bd actually writes (kvkeys.Prefix "kv." +
// kvkeys.MemoryPrefix "memory."), which cmd/bd also reserves against generic
// `bd kv set` keys. Config rows with this prefix are the only config class safe
// to auto-resolve on merge; any other key (issue_prefix above all) is left for
// the operator.
const memoryConfigKeyPrefix = kvkeys.MemoryConfigKeyPrefix

// This file holds the merge-settlement machinery shared by server-mode
// DoltStore (which drives it inside an explicit *sql.Tx) and the embedded
// store's pull path (which drives it on a pinned autocommit connection via
// MergeAndSettle): auto-resolving the conflict classes that are safe without
// operator input (GH#2466 metadata, #4259 audit-only dependency edges,
// bd-6dnrw.29 schema_migrations vintage rows, GH#2474 convergent kv.memory.*
// config rows) and repairing FK cascade
// violations (bd-6dnrw.4). All functions take a DBConn, which *sql.Tx,
// *sql.Conn, and *sql.DB all satisfy.

// MergeAndSettle merges ref into the current branch and settles the result:
// safe conflict classes are auto-resolved, FK cascade violations are
// repaired, and anything needing the operator aborts the merge. It is the
// autocommit-mode equivalent of server-mode pullWithAutoResolve and is used
// by the embedded pull path (bd-6dnrw.40), where Dolt stored procedures
// cannot run inside an explicit SQL transaction.
//
// db must be a single session (a pinned *sql.Conn, or a *sql.DB used
// sequentially whose pool holds one connection): the session flags set here
// must be visible to the DOLT_MERGE and to every settle statement. The flags
// let a conflicted or FK-violating merge land in the working set instead of
// rolling back, so the settle pass can repair it; SettleMerge's gates ensure
// nothing unrepaired survives without an error.
func MergeAndSettle(ctx context.Context, db DBConn, ref string) error {
	// Capture pre-merge cleanliness before anything runs: abortMerge's
	// hard-reset fallback is only safe when nothing uncommitted predates
	// the merge (bd-578h9.2).
	preMergeClean := workingSetClean(ctx, db)

	if _, err := db.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		return fmt.Errorf("set dolt_allow_commit_conflicts: %w", err)
	}
	if _, err := db.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 1"); err != nil {
		return fmt.Errorf("set dolt_force_transaction_commit: %w", err)
	}

	_, mergeErr := db.ExecContext(ctx, "CALL DOLT_MERGE(?)", ref)
	if mergeErr != nil && strings.Contains(mergeErr.Error(), "up to date") {
		// DOLT_PULL swallows "Already up to date." internally; we do the same.
		mergeErr = nil
	}
	return SettleMerge(ctx, db, mergeErr, preMergeClean)
}

// MergeConflictsError reports the conflicts a settle pass refused to
// auto-resolve. By the time the caller sees it the merge has been aborted (or
// the transaction rolled back) and the working set restored, so the conflicts
// are no longer queryable from dolt_conflicts — they were captured before the
// abort precisely so callers with a conflict-reporting contract (PullFrom) can
// still surface them (bd-578h9.15). Unwrap returns the merge statement's own
// error, when there was one.
type MergeConflictsError struct {
	Conflicts []storage.Conflict
	// MergeErr is the merge/pull statement's own error; nil on Dolt versions
	// that leave conflicts in the working set without erroring.
	MergeErr error
}

func (e *MergeConflictsError) Error() string {
	tables := make([]string, len(e.Conflicts))
	for i, c := range e.Conflicts {
		tables[i] = c.Field
	}
	return fmt.Sprintf("merge conflicts in %s require operator resolution; merge aborted and working set restored",
		strings.Join(tables, ", "))
}

func (e *MergeConflictsError) Unwrap() error { return e.MergeErr }

// SettleMerge finishes a merge that ran on db with the session flags
// MergeAndSettle sets: it auto-resolves the safe conflict classes, repairs FK
// cascade violations (bd-6dnrw.4), and leaves the settled working set in
// place — or aborts the merge when anything needs the operator. mergeErr is
// the merge statement's own error; it is surfaced whenever nothing was
// resolved or repaired. preMergeClean reports whether the working set was
// clean before the merge ran; it gates abortMerge's hard-reset fallback.
// The decision logic mirrors server-mode settleMergeInTx exactly; the abort
// stands in for that path's transaction rollback, restoring the pre-merge
// working set so a retry is possible.
func SettleMerge(ctx context.Context, db DBConn, mergeErr error, preMergeClean bool) error {
	// Check for merge conflicts regardless of whether the merge errored.
	// Some Dolt versions error on conflicts, others leave them in the working set.
	resolved, resolveErr := TryAutoResolveMergeConflicts(ctx, db)
	if resolveErr != nil {
		abortMerge(ctx, db, preMergeClean)
		if mergeErr != nil {
			return mergeErr
		}
		return resolveErr
	}

	// bd-578h9.15: conflicts the resolver declined are the operator's. Capture
	// them BEFORE the abort wipes merge state — a post-abort GetConflicts sees
	// an empty set, which made PullFrom's conflict-reporting contract dead
	// code. The resolver pre-screens every table before resolving any, so a
	// declined resolve leaves dolt_conflicts fully intact here.
	if !resolved {
		if conflicts, err := GetConflicts(ctx, db); err == nil && len(conflicts) > 0 {
			abortMerge(ctx, db, preMergeClean)
			return &MergeConflictsError{Conflicts: conflicts, MergeErr: mergeErr}
		}
	}

	// bd-6dnrw.4: repair FK cascade violations the merge produced (child rows
	// whose parent issue was deleted on the other clone). Unrepaired
	// violations MUST NOT survive: with the force flag on, every statement
	// autocommits, so the abort below is what keeps them out of the database.
	repairedViol, hadViol, violErr := TryRepairFKCascadeViolations(ctx, db)
	if violErr != nil {
		abortMerge(ctx, db, preMergeClean)
		if mergeErr != nil {
			return mergeErr
		}
		return violErr
	}
	if hadViol && !repairedViol {
		abortMerge(ctx, db, preMergeClean)
		if mergeErr != nil {
			return mergeErr
		}
		return fmt.Errorf("pull merge left constraint violations bd cannot auto-repair; inspect dolt_constraint_violations and resolve before retrying")
	}

	if mergeErr != nil && !resolved && !repairedViol {
		// Merge failed for a non-conflict reason, or conflicts include non-metadata tables.
		abortMerge(ctx, db, preMergeClean)
		return mergeErr
	}

	// Conclude the merge for resolved conflicts only now, after the FK repair:
	// DOLT_COMMIT refuses a violated working set, so a merge carrying both
	// classes could never settle when the resolver committed first (bd-578h9.14).
	if resolved {
		if err := CommitResolvedConflicts(ctx, db); err != nil {
			abortMerge(ctx, db, preMergeClean)
			if mergeErr != nil {
				return mergeErr
			}
			return err
		}
	}

	return nil
}

// abortMerge restores the pre-merge state after a settle pass refused the
// merge — the autocommit-mode stand-in for server mode's tx.Rollback().
// DOLT_MERGE('--abort') is the precise tool but only works while merge state
// is active; a force-committed violation-only merge may have closed it, so
// fall back to a hard reset — but only when the working set was clean before
// the merge ran. The most common reason --abort fails is a merge that
// REFUSED TO START on a dirty working set; hard-resetting there would
// destroy uncommitted data the merge never touched (bd-578h9.2).
// Best-effort: the caller's error is what matters.
func abortMerge(ctx context.Context, db DBConn, preMergeClean bool) {
	if _, err := db.ExecContext(ctx, "CALL DOLT_MERGE('--abort')"); err != nil && preMergeClean {
		_, _ = db.ExecContext(ctx, "CALL DOLT_RESET('--hard')")
	}
}

// workingSetClean reports whether dolt_status is empty. Errors count as
// dirty so the hard-reset fallback stays conservative.
func workingSetClean(ctx context.Context, db DBConn) bool {
	rows, err := db.QueryContext(ctx, "SELECT 1 FROM dolt_status LIMIT 1")
	if err != nil {
		return false
	}
	defer rows.Close()
	return !rows.Next() && rows.Err() == nil
}

// TryAutoResolveMergeConflicts auto-resolves merge conflicts that are safe to
// resolve without operator input, and returns (true, nil) only if ALL conflicts
// were resolved. It handles four classes:
//
//   - metadata: machine-local rows (e.g. dolt_auto_push_*) that routinely diverge
//     across clones (GH#2466). Resolved with "theirs".
//   - dependencies: with deterministic ids (#4259) the same logical edge has the
//     same primary key on every clone, so a same-PK conflict is the SAME edge.
//     When the two sides differ only in audit columns (created_at, created_by,
//     metadata, thread_id) — same edge, same type, present on both sides — the
//     conflict is resolved with "theirs" (the remote's values win, which is
//     convergent across clones pulling from the same remote). A conflict where the
//     dependency type differs, or one side deleted the edge, is a real semantic
//     conflict and is left for the operator.
//   - schema_migrations: pre-#4270 binaries record (version, NULL content_hash)
//     while post-#4270 binaries record (version, sha256), so two clones applying
//     the SAME migration with mixed binary vintages conflict on the cursor row
//     (bd-6dnrw.29). When one side's hash is NULL/empty and the other has one
//     (or both are equal), the row is resolved keeping the hash — recorded
//     provenance beats its absence, and the result converges across clones.
//     Two DIFFERENT non-empty hashes are the #4259 schema fork itself and are
//     left for the operator (bd doctor reports them as Migration Content Skew).
//   - config: persistent memories live in config as kv.memory.* rows (the
//     pre-pull auto-commit now commits config so they sync). Like metadata,
//     same-key memory edits across clones are machine-convergent: resolved with
//     "theirs", so all clones pulling from one remote converge on the remote's
//     value. A conflict touching ANY non-memory config key (issue_prefix above
//     all) is a real semantic conflict and is left for the operator.
//
// Any conflict on another table, or an unresolvable dependencies,
// schema_migrations, or config conflict, returns (false, nil) so the caller
// fails the pull and the operator resolves it.
//
// The resolved tables are staged but NOT committed: the caller must run
// CommitResolvedConflicts after the FK cascade repair, because DOLT_COMMIT
// refuses a working set with outstanding constraint violations (bd-578h9.14).
func TryAutoResolveMergeConflicts(ctx context.Context, db DBConn) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT `table`, num_conflicts FROM dolt_conflicts")
	if err != nil {
		return false, fmt.Errorf("failed to query conflicts: %w", err)
	}

	type conflict struct {
		table string
		count int
	}
	var conflicts []conflict
	for rows.Next() {
		var c conflict
		if err := rows.Scan(&c.table, &c.count); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("failed to scan conflict: %w", err)
		}
		conflicts = append(conflicts, c)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	if len(conflicts) == 0 {
		return false, nil // No conflicts to resolve — error was something else
	}

	// Decide which conflicted tables are safe to auto-resolve. If any conflict is
	// not safely resolvable, resolve nothing and let the pull fail.
	var resolvable []string
	for _, c := range conflicts {
		switch c.table {
		case "metadata":
			resolvable = append(resolvable, "metadata")
		case "dependencies":
			auditOnly, err := dependencyConflictsAreAuditOnly(ctx, db)
			if err != nil {
				return false, err
			}
			if !auditOnly {
				return false, nil
			}
			resolvable = append(resolvable, "dependencies")
		case "schema_migrations":
			vintageOnly, err := schemaMigrationsConflictsAreVintageOnly(ctx, db)
			if err != nil {
				return false, err
			}
			if !vintageOnly {
				return false, nil
			}
			resolvable = append(resolvable, "schema_migrations")
		case "config":
			memoryOnly, err := configConflictsAreMemoryConvergent(ctx, db)
			if err != nil {
				return false, err
			}
			if !memoryOnly {
				return false, nil
			}
			resolvable = append(resolvable, "config")
		default:
			return false, nil
		}
	}

	// Resolve each safe table and stage only that table (GH#2455).
	// table is from the fixed allowlist above, never user input.
	for _, table := range resolvable {
		switch table {
		case "schema_migrations":
			// Row-wise: keep whichever side recorded a content hash, so the
			// table-level --ours/--theirs choice can never drop one.
			if err := resolveSchemaMigrationsVintageConflicts(ctx, db); err != nil {
				return false, err
			}
		case "config":
			// --theirs makes this clone's local kv.memory.* edit lose to the
			// remote value (the same convergent trade-off metadata makes). That
			// supersession is otherwise undiagnosable, so name the resolved keys
			// first. Best-effort: a diagnostics query failure must not abort an
			// otherwise-correct resolution.
			if keys, kerr := resolvedConfigConflictKeys(ctx, db); kerr == nil && len(keys) > 0 {
				fmt.Fprintf(os.Stderr,
					"Notice: auto-resolved %d memory config conflict(s) with the remote value (--theirs); "+
						"local edits to %s were superseded\n",
					len(keys), strings.Join(keys, ", "))
			}
			if _, err := db.ExecContext(ctx, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'config')"); err != nil {
				return false, fmt.Errorf("failed to resolve config conflicts: %w", err)
			}
		default:
			//nolint:gosec // G201: table is one of the hardcoded constants above.
			if _, err := db.ExecContext(ctx, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', '"+table+"')"); err != nil {
				return false, fmt.Errorf("failed to resolve %s conflicts: %w", table, err)
			}
		}
		//nolint:gosec // G201: table is one of the hardcoded constants above.
		if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('"+table+"')"); err != nil {
			return false, fmt.Errorf("failed to stage %s: %w", table, err)
		}
	}

	return true, nil
}

// CommitResolvedConflicts creates the dolt commit that concludes a merge whose
// conflicts TryAutoResolveMergeConflicts settled. Callers that saw
// resolved=true MUST call this, and only AFTER TryRepairFKCascadeViolations
// has run: DOLT_COMMIT refuses a working set with outstanding constraint
// violations, so a merge carrying both an auto-resolvable conflict and an FK
// cascade violation could never settle while the resolver committed first
// (bd-578h9.14).
func CommitResolvedConflicts(ctx context.Context, db DBConn) error {
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'auto-resolve merge conflicts (GH#2466, #4259, GH#2474)')"); err != nil {
		return fmt.Errorf("failed to commit resolved conflicts: %w", err)
	}
	return nil
}

// dependencyConflictsAreAuditOnly reports whether every conflicted row in the
// dependencies table is the SAME logical edge on both sides that differs only in
// audit columns (created_at/created_by/metadata/thread_id) — the only class safe to
// auto-resolve with --theirs.
//
// It does NOT trust the primary key as proof of a shared edge. With deterministic
// ids the same edge has the same id on every clone, but an issue rename can leave a
// row's surrogate id stale (depid.New(oldID, target)) while issue_id/target have
// already moved (#4259 finding 2), so two genuinely different edges could collide on
// one id. We therefore verify the natural identity — issue_id and the resolved
// target — matches on both sides, and that the type matches, before declaring the
// conflict audit-only. It returns false if any conflicted row differs in identity or
// type, or was deleted on one side (an add/delete conflict).
func dependencyConflictsAreAuditOnly(ctx context.Context, db DBConn) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT our_id, their_id,
		       our_issue_id, their_issue_id,
		       our_depends_on_issue_id, their_depends_on_issue_id,
		       our_depends_on_wisp_id, their_depends_on_wisp_id,
		       our_depends_on_external, their_depends_on_external,
		       our_type, their_type
		FROM dolt_conflicts_dependencies`)
	if err != nil {
		return false, fmt.Errorf("query dependency conflicts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			ourID, theirID             sql.NullString
			ourIssue, theirIssue       sql.NullString
			ourDepIssue, theirDepIssue sql.NullString
			ourDepWisp, theirDepWisp   sql.NullString
			ourDepExt, theirDepExt     sql.NullString
			ourType, theirType         sql.NullString
		)
		if err := rows.Scan(&ourID, &theirID, &ourIssue, &theirIssue,
			&ourDepIssue, &theirDepIssue, &ourDepWisp, &theirDepWisp,
			&ourDepExt, &theirDepExt, &ourType, &theirType); err != nil {
			return false, fmt.Errorf("scan dependency conflict: %w", err)
		}
		// One side deleted the edge (add/delete conflict): leave for the operator.
		if !ourID.Valid || !theirID.Valid {
			return false, nil
		}
		// Same edge requires the same source issue. A differing issue_id means the
		// shared id is stale on one side (e.g. a rename), not a shared edge.
		if ourIssue.Valid != theirIssue.Valid || ourIssue.String != theirIssue.String {
			return false, nil
		}
		// ...and the same resolved target.
		ourTarget, ourOK := resolveConflictDepTarget(ourDepIssue, ourDepWisp, ourDepExt)
		theirTarget, theirOK := resolveConflictDepTarget(theirDepIssue, theirDepWisp, theirDepExt)
		if ourOK != theirOK || ourTarget != theirTarget {
			return false, nil
		}
		// A differing type is the only remaining way this is a real semantic conflict.
		if ourType.Valid != theirType.Valid || ourType.String != theirType.String {
			return false, nil
		}
	}
	return true, rows.Err()
}

// configConflictsAreMemoryConvergent reports whether every conflicted config
// row is a persistent-memory row (key prefixed memoryConfigKeyPrefix). Memories
// are the only config class safe to auto-resolve with --theirs: like metadata,
// all clones pulling from the same remote converge on the remote's value (a
// local edit to the same memory key loses, the same convergent trade-off
// metadata makes). Any other config key in conflict — issue_prefix above all,
// whose stale-value sweep GH#2455 specifically guards against — is a real
// semantic conflict, so the whole config table is left for the operator.
//
// The key column is config's primary key, so a same-key conflict carries the
// identical key on both sides; an add/delete conflict leaves one side NULL. A
// row is convergent only if every key it presents is a memory key.
func configConflictsAreMemoryConvergent(ctx context.Context, db DBConn) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT our_key, their_key FROM dolt_conflicts_config`)
	if err != nil {
		return false, fmt.Errorf("query config conflicts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ourKey, theirKey sql.NullString
		if err := rows.Scan(&ourKey, &theirKey); err != nil {
			return false, fmt.Errorf("scan config conflict: %w", err)
		}
		for _, k := range []sql.NullString{ourKey, theirKey} {
			if k.Valid && !strings.HasPrefix(k.String, memoryConfigKeyPrefix) {
				return false, nil
			}
		}
	}
	return true, rows.Err()
}

// resolvedConfigConflictKeys returns the keys of the config rows currently in
// conflict, used only to name the kv.memory.* keys whose local value the
// --theirs auto-resolution is about to supersede. It must be called BEFORE
// DOLT_CONFLICTS_RESOLVE clears dolt_conflicts_config. config's primary key is
// `key`, so a same-key conflict carries the identical key on both sides; an
// add/delete conflict leaves one side NULL, so COALESCE picks whichever side has
// it.
func resolvedConfigConflictKeys(ctx context.Context, db DBConn) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT COALESCE(our_key, their_key) FROM dolt_conflicts_config")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key sql.NullString
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		if key.Valid {
			keys = append(keys, key.String)
		}
	}
	return keys, rows.Err()
}

// schemaMigrationsConflictsAreVintageOnly reports whether every conflicted
// schema_migrations row is the same migration version present on BOTH sides
// whose content hashes are compatible: equal, or NULL/empty on exactly one side
// (a pre-#4270 binary recorded the version without a hash, bd-6dnrw.29). Two
// different non-empty hashes mean the clones applied different content for the
// same version — the #4259 schema fork — and are never auto-resolved. A row
// deleted on one side is not a vintage artifact either.
func schemaMigrationsConflictsAreVintageOnly(ctx context.Context, db DBConn) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT our_version, their_version, our_content_hash, their_content_hash
		FROM dolt_conflicts_schema_migrations`)
	if err != nil {
		return false, fmt.Errorf("query schema_migrations conflicts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ourVersion, theirVersion sql.NullInt64
		var ourHash, theirHash sql.NullString
		if err := rows.Scan(&ourVersion, &theirVersion, &ourHash, &theirHash); err != nil {
			return false, fmt.Errorf("scan schema_migrations conflict: %w", err)
		}
		if !ourVersion.Valid || !theirVersion.Valid || ourVersion.Int64 != theirVersion.Int64 {
			return false, nil
		}
		ours, theirs := ourHash.String, theirHash.String
		if ours != "" && theirs != "" && ours != theirs {
			return false, nil // real content skew (#4259) — operator decides
		}
	}
	return true, rows.Err()
}

// resolveSchemaMigrationsVintageConflicts resolves vintage-only cursor-row
// conflicts (validated by schemaMigrationsConflictsAreVintageOnly) keeping
// whichever side recorded a content hash: when theirs has the hash and ours is
// NULL, the working-set row is updated to theirs before the table-level
// resolve, so '--ours' never discards recorded provenance.
func resolveSchemaMigrationsVintageConflicts(ctx context.Context, db DBConn) error {
	rows, err := db.QueryContext(ctx, `
		SELECT our_version, our_content_hash, their_content_hash
		FROM dolt_conflicts_schema_migrations`)
	if err != nil {
		return fmt.Errorf("query schema_migrations conflicts: %w", err)
	}
	type hashFix struct {
		version int64
		hash    string
	}
	var fixes []hashFix
	for rows.Next() {
		var version sql.NullInt64
		var ourHash, theirHash sql.NullString
		if err := rows.Scan(&version, &ourHash, &theirHash); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan schema_migrations conflict: %w", err)
		}
		if ourHash.String == "" && theirHash.String != "" {
			fixes = append(fixes, hashFix{version: version.Int64, hash: theirHash.String})
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}

	for _, f := range fixes {
		if _, err := db.ExecContext(ctx,
			"UPDATE schema_migrations SET content_hash = ? WHERE version = ?", f.hash, f.version); err != nil {
			return fmt.Errorf("backfill content_hash for migration %d: %w", f.version, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CONFLICTS_RESOLVE('--ours', 'schema_migrations')"); err != nil {
		return fmt.Errorf("failed to resolve schema_migrations conflicts: %w", err)
	}
	return nil
}

// resolveConflictDepTarget returns the single non-null dependency target from a
// conflict row's three typed target columns, following the same precedence as
// COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external).
func resolveConflictDepTarget(issueTarget, wispTarget, external sql.NullString) (string, bool) {
	switch {
	case issueTarget.Valid:
		return issueTarget.String, true
	case wispTarget.Valid:
		return wispTarget.String, true
	case external.Valid:
		return external.String, true
	default:
		return "", false
	}
}

// fkCascadeRepairDeletes maps each synced child table holding a FOREIGN KEY to
// issues(id) (migrations 0041/0042 added ON DELETE/UPDATE CASCADE; ignored
// migration 0002 covers child_counters) to the DELETE that applies the FK's
// cascade semantics by hand after a merge (bd-6dnrw.4).
//
// Dolt merges each table row-wise and never re-executes cascades, so "clone A
// deletes issue X" merged with "clone B inserts a child row referencing X"
// produces a child row whose parent is gone — a foreign-key constraint
// violation that makes the merge transaction roll back, and retrying can never
// converge. Deleting the dangling rows is the convergent repair: it is exactly
// what the cascade did on the deleting clone, and what the FK would have
// forced had the two writes been sequenced on one database.
var fkCascadeRepairDeletes = map[string]string{
	"dependencies": `DELETE FROM dependencies
		WHERE issue_id NOT IN (SELECT id FROM issues)
		   OR (depends_on_issue_id IS NOT NULL AND depends_on_issue_id NOT IN (SELECT id FROM issues))`,
	"labels":               `DELETE FROM labels WHERE issue_id NOT IN (SELECT id FROM issues)`,
	"comments":             `DELETE FROM comments WHERE issue_id NOT IN (SELECT id FROM issues)`,
	"events":               `DELETE FROM events WHERE issue_id NOT IN (SELECT id FROM issues)`,
	"issue_snapshots":      `DELETE FROM issue_snapshots WHERE issue_id NOT IN (SELECT id FROM issues)`,
	"compaction_snapshots": `DELETE FROM compaction_snapshots WHERE issue_id NOT IN (SELECT id FROM issues)`,
	"child_counters":       `DELETE FROM child_counters WHERE parent_id NOT IN (SELECT id FROM issues)`,
}

// TryRepairFKCascadeViolations repairs the post-merge foreign-key constraint
// violations produced by the delete-vs-insert cascade hazard (bd-6dnrw.4): for
// every violating table it deletes the rows whose issue reference dangles,
// clears that table's dolt_constraint_violations entries, and stages the
// table. The caller's session must run with
// @@dolt_force_transaction_commit=1 for the merge to survive long enough to be
// repaired, and must NOT keep the merge when (repaired=false, had=true) —
// unrepaired violations are the operator's.
//
// Returns (repaired, had):
//   - (false, false): no violations — nothing to do.
//   - (true, true): every violation was an issues-FK violation on a known
//     synced child table, and all were repaired and cleared.
//   - (false, true): violations of another shape (different constraint type,
//     unknown table, FK to a different parent) — nothing was touched.
func TryRepairFKCascadeViolations(ctx context.Context, db DBConn) (repaired, had bool, err error) {
	tables, err := constraintViolationTables(ctx, db)
	if err != nil {
		return false, false, err
	}
	if len(tables) == 0 {
		return false, false, nil
	}

	// Validate every violating table before touching any of them.
	for _, t := range tables {
		if _, ok := fkCascadeRepairDeletes[t]; !ok {
			return false, true, nil
		}
		issueFKOnly, err := violationsAreIssueFKOnly(ctx, db, t)
		if err != nil {
			return false, true, err
		}
		if !issueFKOnly {
			return false, true, nil
		}
	}

	for _, t := range tables {
		res, err := db.ExecContext(ctx, fkCascadeRepairDeletes[t])
		if err != nil {
			return false, true, fmt.Errorf("cascade-repair %s: %w", t, err)
		}
		n, _ := res.RowsAffected()
		// t is from the fixed fkCascadeRepairDeletes allowlist, never user input.
		//nolint:gosec // G201/G202: hardcoded table name.
		if _, err := db.ExecContext(ctx, "DELETE FROM dolt_constraint_violations_"+t); err != nil {
			return false, true, fmt.Errorf("clear %s constraint violations: %w", t, err)
		}
		//nolint:gosec // G202: hardcoded table name.
		if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('"+t+"')"); err != nil {
			return false, true, fmt.Errorf("stage repaired %s: %w", t, err)
		}
		fmt.Fprintf(os.Stderr,
			"Notice: pull merged %s row(s) referencing issue(s) deleted on another clone; applied the foreign key's cascade delete (%d row(s) removed)\n",
			t, n)
	}

	// The repair must leave nothing behind: a residual violation here means the
	// deletes above did not cover the constraint that fired, and committing
	// would persist a violated working set.
	remaining, err := constraintViolationTables(ctx, db)
	if err != nil {
		return false, true, err
	}
	if len(remaining) > 0 {
		return false, true, nil
	}
	return true, true, nil
}

// constraintViolationTables lists the tables with outstanding constraint
// violations in the working set.
func constraintViolationTables(ctx context.Context, db DBConn) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")
	if err != nil {
		return nil, fmt.Errorf("query constraint violations: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan constraint violation: %w", err)
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

// violationsAreIssueFKOnly reports whether every constraint violation recorded
// for table is a foreign-key violation referencing issues — the only class the
// cascade repair understands. violation_info is Dolt's JSON descriptor; its
// ReferencedTable names the FK's parent.
func violationsAreIssueFKOnly(ctx context.Context, db DBConn, table string) (bool, error) {
	// table is from the fixed fkCascadeRepairDeletes allowlist, never user input.
	//nolint:gosec // G202: hardcoded table name.
	rows, err := db.QueryContext(ctx,
		"SELECT violation_type, violation_info FROM dolt_constraint_violations_"+table)
	if err != nil {
		return false, fmt.Errorf("query %s constraint violations: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var vtype string
		var vinfo any
		if err := rows.Scan(&vtype, &vinfo); err != nil {
			return false, fmt.Errorf("scan %s constraint violation: %w", table, err)
		}
		if vtype != "foreign key" {
			return false, nil
		}
		// Server mode returns violation_info as JSON text; the embedded engine
		// hands back the driver's native value (e.g. merge.FkCVMeta), which
		// marshals to the same JSON.
		var infoJSON []byte
		switch v := vinfo.(type) {
		case []byte:
			infoJSON = v
		case string:
			infoJSON = []byte(v)
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return false, nil // unknown descriptor shape — operator decides
			}
			infoJSON = b
		}
		var info struct {
			ReferencedTable string `json:"ReferencedTable"`
		}
		if err := json.Unmarshal(infoJSON, &info); err != nil {
			return false, nil // unknown descriptor shape — operator decides
		}
		if info.ReferencedTable != "issues" {
			return false, nil
		}
	}
	return true, rows.Err()
}
