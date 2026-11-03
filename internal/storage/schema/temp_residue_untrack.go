package schema

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// A table-rebuild intermediate ("__temp__X", see doltIgnorePatterns) that an
// older binary committed at HEAD — a @@dolt_transaction_commit=1 auto-commit,
// or a blanket add from before "__temp__%" was seeded — wedges every FRESH
// CLONE of that lineage, not just the store that produced it
// (gastownhall/beads#7073). The ignored-lane cursor is clone-local, so a clone
// replays the ignored series from zero on its first open, and the rebuild
// renames the HEAD-tracked __temp__X onto its ignored final name. Nothing can
// stage that rename: "__temp__%" filters the old name out of every staging
// candidate set, and even a forced DOLT_ADD of the old name stages nothing,
// because the working set no longer has it. The next pull then fails, and not
// with the "cannot merge with uncommitted changes" a plain delete earns but
// with "malformed database: chunk 0000… required but not found", which reads
// as corruption.
//
// The repair removes the intermediate from the STAGED root only
// (DOLT_RM('--cached')) and commits that, scoped. Unlike a drop-and-commit
// sweep it never touches the working set, which matters twice over:
//
//   - a __temp__ table's COMMITTED lineage is never read back, but its WORKING
//     rows may be: main 0062 restores events from __temp__events_flip, and the
//     #4356 cursor heal restores its cursor from its scratch table. Cached
//     removal leaves both owners exactly the rows they expect;
//   - a store already in the renamed state (the intermediate is gone from the
//     working set, its final name holds the rows) converges by the same
//     single operation, with no rename-back or row copy: once HEAD stops
//     carrying the old name, the final table is an ignored ADD delta, the
//     shape a never-wedged clone has.
//
// Like the cursor heal, this is a re-probe at every writable open rather than
// a numbered migration: the affected stores are at-latest, and a pull from a
// not-yet-repaired peer can bring the tracked intermediate back.
const (
	tempIntermediatePrefix = "__temp__"

	tempResidueUntrackCommitMessage = "schema: untrack committed __temp__ table-rebuild intermediates so dolt_ignore can apply (gastownhall/beads#7073)"
)

// readTempResidue is the reconcile's read-only gate, shared by MigrateUp and
// alreadyConverged for the reason ignoredCursorState gives: a term only one of
// them evaluates is unreachable in server mode. It returns, sorted, the
// __temp__ tables committed at HEAD that dolt_ignore genuinely exempts.
//
// A healthy database pays one HEAD listing that returns no rows. The listing
// matches LIKE '__temp__%', where every underscore is a single-character
// wildcard, so the literal prefix is checked again here. Eligibility is
// tableActivelyIgnored's, veto and all: an operator who versions an
// intermediate on purpose keeps it tracked, whether the ignored=0 row names it
// exactly or is a wildcard Dolt may apply to it ('__temp__keep*'), which
// declines rather than guesses.
//
// qualifier is as for readIgnoredCursorState.
func readTempResidue(ctx context.Context, db DBConn, qualifier string) ([]string, error) {
	tracked, err := listTempTables(ctx, db, qualifier, "HEAD")
	if err != nil {
		return nil, err
	}
	var eligible []string
	for _, table := range tracked {
		ignored, err := tableActivelyIgnored(ctx, db, qualifier, table)
		if err != nil {
			return nil, fmt.Errorf("reading dolt_ignore for %s: %w", table, err)
		}
		if ignored {
			eligible = append(eligible, table)
		}
	}
	return eligible, nil
}

// listTempTables lists the __temp__ tables of one root ('HEAD' or 'STAGED'),
// sorted. Its result set is fully read and closed before it returns, so the
// caller's next query on a pinned connection never interleaves with it.
//
// Like tableTrackedAtHead it LISTS rather than selects: the listing succeeds
// on every database, and a failing probe would pin the pooled Dolt session to
// its pre-statement catalog (be-bv7x).
func listTempTables(ctx context.Context, db DBConn, qualifier, revision string) ([]string, error) {
	from := ""
	if qualifier != "" {
		// The AS OF clause must trail the FROM.
		from = " FROM " + qualifier
	}
	//nolint:gosec // G201: the qualifier is identifier-quoted, the revision and prefix are package constants.
	query := "SHOW TABLES" + from + " AS OF '" + revision + "' LIKE '" + tempIntermediatePrefix + "%'"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing __temp__ tables at %s: %w", revision, err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("listing __temp__ tables at %s: %w", revision, err)
		}
		if strings.HasPrefix(strings.ToLower(name), tempIntermediatePrefix) {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("listing __temp__ tables at %s: %w", revision, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("listing __temp__ tables at %s: %w", revision, err)
	}
	sort.Strings(tables)
	return tables, nil
}

// untrackTempResidue removes every eligible HEAD-tracked __temp__ table from
// HEAD while keeping its working rows, reporting whether it may have changed
// HEAD. It runs after healTrackedIgnoredCursorTable, which owns its own
// scratch table's restore and cleanup, and before the migration pass, so a
// fresh clone is repaired before the ignored series can rename anything.
//
// Failure policy follows the cursor heal's #5816 rule that a repair must
// never mint an un-openable database. Everything before DOLT_RM is a read or
// a staging-area reset and declines advisorially, and so does a DOLT_RM
// refused for access: the CALL is authorized, and DOLT_RM checks its branch
// permission, before any root changes. DOLT_RM and the commit are the only
// HEAD-moving steps; once DOLT_RM has run, or failed any other way, the change
// is counted even when a later step fails, because a failed commit does not
// prove HEAD stayed put (DOLT_RM commits its own SQL transaction, which on a
// @@dolt_transaction_commit=1 server is already a Dolt commit), and a false
// "nothing changed" leaves the caller's pool pinned to a stale root. Those
// failures warn rather than fail the open: the working rows are untouched,
// and the next writable open retries. Only context cancellation propagates.
func untrackTempResidue(ctx context.Context, db DBConn) (bool, error) {
	eligible, err := readTempResidue(ctx, db, "")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		ignoredCursorAdvisory("schema: cannot read the committed __temp__ intermediate gate, leaving it alone: %v\n", err)
		return false, nil
	}
	if len(eligible) == 0 {
		return false, nil
	}

	// Scope the commit below to the removals alone: unstage whatever else is
	// staged (the edits stay in the working set), and only then publish the
	// pinned transaction, so the staging procedures on the transaction-pinned
	// server path see this session's working set — and a staged change that
	// is not ours is never published as staged.
	if err := unstageBeforeIgnoredCursorUntrack(ctx, db); err != nil {
		return declineTempResidueUntrack(ctx, "cannot clear the staging area", eligible, err)
	}
	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		return declineTempResidueUntrack(ctx, "cannot publish the working set", eligible, err)
	}
	staged, err := listTempTables(ctx, db, "", "STAGED")
	if err != nil {
		return declineTempResidueUntrack(ctx, "cannot read the staging area", eligible, err)
	}
	targets := intersectSorted(eligible, staged)
	if len(targets) == 0 {
		// Tracked at HEAD yet already gone from the staging area after the
		// unstage: something outside this reconcile owns that delta. An
		// empty DOLT_RM would fail, and claiming success would be a lie.
		ignoredCursorAdvisory("schema: committed __temp__ intermediates %s are not in the staging area; leaving them alone\n",
			strings.Join(eligible, ", "))
		return false, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(targets)), ", ")
	args := make([]any, len(targets))
	for i, table := range targets {
		args[i] = table
	}
	if err := DrainCall(ctx, db, "CALL DOLT_RM('--cached', "+placeholders+")", args...); err != nil {
		if dberrors.IsAccessDenied(err) {
			// Refused before any root changed, so unlike every failure
			// below this counts nothing, and the cursor heal's rule holds: a
			// client that may not repair is never told, at every open, that
			// its database is being repaired.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return false, ctxErr
			}
			ignoredCursorAdvisory("schema: not permitted to untrack committed %s; continuing without it: %v\n",
				strings.Join(targets, ", "), err)
			return false, nil
		}
		return tempResidueUntrackFailed(ctx, "untracking", targets, err)
	}

	// Announced only now that DOLT_RM has run: a refused one claims nothing.
	log.Printf("schema: %s committed at HEAD on this database, which wedges `bd dolt pull` on every fresh clone; untracking and keeping the rows (gastownhall/beads#7073)",
		strings.Join(targets, ", "))

	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('--skip-empty', '-m', ?)", tempResidueUntrackCommitMessage); err != nil {
		return tempResidueUntrackFailed(ctx, "committing the untrack of", targets, err)
	}
	return true, nil
}

// declineTempResidueUntrack is the advisory exit for a step that ran before
// anything could move HEAD.
func declineTempResidueUntrack(ctx context.Context, what string, tables []string, err error) (bool, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	ignoredCursorAdvisory("schema: %s before untracking committed %s, leaving them alone: %v\n",
		what, strings.Join(tables, ", "), err)
	return false, nil
}

// tempResidueUntrackFailed is the exit for a failure once DOLT_RM was issued
// and not refused: the change is counted (see untrackTempResidue), and the
// open goes on.
func tempResidueUntrackFailed(ctx context.Context, step string, tables []string, err error) (bool, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return true, ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true, err
	}
	log.Printf("schema: warning: %s committed %s failed, rows kept, the next writable open retries: %v",
		step, strings.Join(tables, ", "), err)
	return true, nil
}

// intersectSorted returns the names present in both sorted lists, compared
// case-insensitively as Dolt compares table names, in a's spelling.
func intersectSorted(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, name := range b {
		in[strings.ToLower(name)] = true
	}
	var out []string
	for _, name := range a {
		if in[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	return out
}
