package schema

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/debug"
)

// Committed __temp__ rebuild residue (gastownhall/beads#7073, reported by
// @bee-ghosttrack). The `__temp__%` dolt_ignore pattern in doltIgnorePatterns
// prevents table-rebuild intermediates from being staged, but it has no effect
// on a name an OLDER binary already committed at HEAD: like git's .gitignore,
// an ignore entry cannot untrack a tracked path. Lineages carrying such a
// commit replicate the residue to every fresh clone, where the ignored series
// renames the HEAD-tracked intermediate onto the ignored final name — an ADD
// delta dolt can never stage, because every staging path anti-joins
// dolt_ignore. `bd dolt pull` then dies with
//
//	Error 1105: failed to update table; malformed database: chunk … required but not found
//
// on every clone of the lineage, forever. The pattern prevents; this file
// repairs.
//
// The repair is a CACHED untrack: `DOLT_RM('--cached', …)` removes the residue
// names from the staged root without touching the working set, so a rebuild
// intermediate that still holds rows (a fresh clone's pre-rename copy, 0062's
// `__temp__events_flip`, the #4356 untrack scratch) keeps them in the working
// set for its owner to consume under its existing contract. Committing the
// removal turns the table's presence back into an ADD delta everywhere else
// and unwedges the pull. It is deliberately not a numbered migration, for the
// same reason the #4356 heal is not: the affected databases are at-latest
// (their residue is the fruit of a COMPLETED pass), and a pull from a
// not-yet-repaired peer can re-introduce tracked residue afterwards.
const tempResidueUntrackCommitSubject = "schema: untrack committed __temp__ rebuild residue (gastownhall/beads#7073)"

// tempResiduePrefix is the reserved rebuild-intermediate prefix. It must stay
// in lockstep with the `__temp__%` pattern in doltIgnorePatterns; that comment
// is the contract a new __temp__ producer owes, and this is its janitor.
const tempResiduePrefix = "__temp__"

// tempResidueAdvisory reports a reconcile step that declined without changing
// anything. Overridable in tests for the same reason ignoredCursorAdvisory
// is: an advisory decline is invisible in production, which is exactly how a
// missing sqlmock expectation used to pass as a healthy run.
var tempResidueAdvisory = func(format string, args ...any) {
	debug.Logf(format, args...)
}

// tempResidueTables is the read-only gate both callers evaluate — MigrateUp's
// reconcile to decide what to do, alreadyConverged to decide whether the
// locked path has anything left. It lists HEAD for the `__temp__%` LIKE shape,
// keeps only names with the literal __temp__ prefix (SQL LIKE's underscores
// are single-character wildcards, so the listing is a superset), and retains
// only names dolt_ignore genuinely covers under tableActivelyIgnored — the
// operator's exact-name veto and its conservative refusal on conflicting
// negative wildcard patterns included. A vetoed intermediate is versioned on
// purpose and must stay exactly where it is.
//
// On a healthy database this is one empty listing and nothing else. Every
// probe always succeeds against a live database (the be-bv7x catalog-snapshot
// rule), so the gate is safe on the every-open and lock-free fast paths.
//
// qualifier is the identifier-quoted database name selectTargetDatabase
// returned, or empty when the session is already on the target database.
func tempResidueTables(ctx context.Context, db DBConn, qualifier string) ([]string, error) {
	from := ""
	if qualifier != "" {
		// The AS OF clause must trail the FROM; `SHOW TABLES AS OF … FROM x`
		// is a syntax error.
		from = " FROM " + qualifier
	}
	//nolint:gosec // G201: the qualifier is the identifier-quoted database name and the pattern is a constant.
	rows, err := db.QueryContext(ctx, "SHOW TABLES"+from+" AS OF 'HEAD' LIKE '__temp__%'")
	if err != nil {
		return nil, fmt.Errorf("listing __temp__ residue at HEAD: %w", err)
	}
	var candidates []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("listing __temp__ residue at HEAD: %w", err)
		}
		if strings.HasPrefix(strings.ToLower(name), tempResiduePrefix) {
			candidates = append(candidates, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("listing __temp__ residue at HEAD: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("listing __temp__ residue at HEAD: %w", err)
	}
	sort.Strings(candidates)

	eligible := make([]string, 0, len(candidates))
	for _, name := range candidates {
		ignored, err := tableActivelyIgnored(ctx, db, qualifier, name)
		if err != nil {
			return nil, err
		}
		if ignored {
			eligible = append(eligible, name)
		}
	}
	return eligible, nil
}

// reconcileTempResidue cached-untracks committed __temp__ rebuild residue and
// commits the removal, reporting whether it changed anything. It runs on every
// writable open and costs one empty HEAD listing on a healthy database.
//
// Failure policy, inherited from the #5816 lesson via the #4356 heal: every
// failure before the first mutating statement (the DOLT_RM) declines
// advisorially, leaving the database exactly as it was — a fenced client, an
// unreadable dolt_ignore, an unstaged staging area that will not clear — and
// the open carries on with the residue in place, no worse than before this
// function existed. Once the RM is issued the answer changes: an RM or commit
// failure does not prove HEAD stayed unchanged (the server's automatic
// transaction commits may already have landed the removal), so the repair is
// reported as a possible change even when it ends in an error, the caller
// rebuilds its pool instead of serving stale roots (schema.go:955-963), and
// the residue is left for the next ordinary open. Only context cancellation
// is returned; everything else stays a WARN so a repair mishap can never
// become a fatal open (embeddeddolt/store.go:758 makes generic MigrateUp
// errors fatal).
//
// The interrupted-RM crash window (staged root missing a name HEAD still has)
// needs no resume state machine: dolt_status reports the staged removal, the
// unstage sweep's DOLT_RESET re-arms the staged root, and the next open
// re-runs this reconcile from the top.
func reconcileTempResidue(ctx context.Context, db DBConn) (bool, error) {
	residue, err := tempResidueTables(ctx, db, "")
	if err != nil {
		tempResidueAdvisory("schema: cannot list __temp__ residue at HEAD, leaving it in place: %v\n", err)
		return false, nil
	}
	if len(residue) == 0 {
		return false, nil
	}

	// Advisory zone. Nothing here mutates history: the unstage sweep retains
	// every working-set edit, and a bare COMMIT only publishes this session's
	// own pinned transaction (the sql-server path) — the sweep runs first so
	// unrelated staged changes are cleared, not published, if it has to
	// decline.
	if err := unstageBeforeIgnoredCursorUntrack(ctx, db); err != nil {
		tempResidueAdvisory("schema: cannot clear the staging area before untracking __temp__ residue, leaving it in place: %v\n", err)
		return false, nil
	}
	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		tempResidueAdvisory("schema: cannot publish the session transaction before untracking __temp__ residue, leaving it in place: %v\n", err)
		return false, nil
	}

	// The pinned DOLT_RM removes names from the STAGED root and errors
	// ErrTableNotFound on any name the staged root does not carry, so the
	// repair intersects before issuing it. The unstage sweep above normally
	// re-arms the staged root to HEAD, making the intersection the residue
	// itself; an empty one means an engine state this repair does not
	// understand, and a refused RM (FK constraint, an older server without
	// cached-remove semantics) would surface the same way. Decline with a
	// diagnostic rather than mint a false success or an RM that must fail.
	removable, err := stagedResidueNames(ctx, db, residue)
	if err != nil {
		tempResidueAdvisory("schema: cannot read the staged root before untracking __temp__ residue, leaving it in place: %v\n", err)
		return false, nil
	}
	if len(removable) == 0 {
		tempResidueAdvisory("schema: %s tracked at HEAD but absent from the staged root; leaving them in place for the next open\n",
			strings.Join(residue, ", "))
		return false, nil
	}

	log.Printf("schema: untracking committed __temp__ rebuild residue from HEAD (gastownhall/beads#7073): %s",
		strings.Join(removable, ", "))
	if err := removeTempResidueFromHead(ctx, db, removable); err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		// The RM may have partially or fully landed (an automatic
		// transaction commit, or the staged removal committing before the
		// failure), so this is a possible change, not a clean decline: report
		// it and let the next ordinary open re-run the gate.
		tempResidueAdvisory("schema: untracking __temp__ residue %s may have partially applied; reporting a possible change and leaving the rest to the next open: %v\n",
			strings.Join(removable, ", "), err)
		return true, nil
	}
	return true, nil
}

// stagedResidueNames narrows residue to the names the staged root carries,
// which is exactly what DOLT_RM('--cached') can remove.
func stagedResidueNames(ctx context.Context, db DBConn, residue []string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW TABLES AS OF 'STAGED'")
	if err != nil {
		return nil, fmt.Errorf("listing the staged root: %w", err)
	}
	staged := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("listing the staged root: %w", err)
		}
		staged[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("listing the staged root: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("listing the staged root: %w", err)
	}

	removable := make([]string, 0, len(residue))
	for _, name := range residue {
		if _, ok := staged[name]; ok {
			removable = append(removable, name)
		}
	}
	return removable, nil
}

// removeTempResidueFromHead issues the cached removal and commits it. With
// --cached the working root is only an FK check whose result is discarded, so
// working rows survive verbatim; the commit message names the tables so a
// fleet operator reading dolt_log sees exactly what was untracked and why.
// --skip-empty covers the server whose automatic transaction commits already
// landed the removal before DOLT_COMMIT ran.
func removeTempResidueFromHead(ctx context.Context, db DBConn, names []string) error {
	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		placeholders[i] = "?"
		args[i] = name
	}
	//nolint:gosec // G201: the statement text is constant; only bound table names vary.
	rmErr := DrainCall(ctx, db, "CALL DOLT_RM('--cached', "+strings.Join(placeholders, ", ")+")", args...)
	if rmErr != nil {
		return fmt.Errorf("cached-removing %s: %w", strings.Join(names, ", "), rmErr)
	}
	message := tempResidueUntrackCommitSubject + ": " + strings.Join(names, ", ")
	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('--skip-empty', '-m', ?)", message); err != nil {
		return fmt.Errorf("committing the removal of %s: %w", strings.Join(names, ", "), err)
	}
	return nil
}
