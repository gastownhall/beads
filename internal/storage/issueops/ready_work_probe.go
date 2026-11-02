package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// readyWorkProbe holds the per-transaction facts every leg of a ready read
// consults: which optional wisp-plane tables exist and hold rows, and the ID
// sets the ready predicate is rendered from. A ready read renders the
// predicate for two table families (issues, wisps) and — for a capped page —
// sizes the whole set as well; each of those used to re-derive these facts
// with its own statements. Against a remote SQL server every statement is a
// sequential round trip, so the facts are resolved ONCE per transaction here
// and shared by every leg.
type readyWorkProbe struct {
	// wispsPresent: the wisps table exists and holds at least one row. False
	// means the ready set is issues-only (wispsTableEmptyOrMissingInTx).
	wispsPresent bool
	// wispDepsExist: the wisp_dependencies table exists
	// (optionalTableExistsInTx). The counts mega-query joins it, so the wisp
	// family is only read when it does.
	wispDepsExist bool
	// idCollision: some ID exists in both issues and wisps. Only then can the
	// two ready sets overlap, so only then does sizing the merged set need the
	// overlap count. Probed only when the caller sizes the set; conservatively
	// true whenever it was not probed exactly.
	idCollision bool
	// inputs are the ID sets the ready WHERE clause is rendered from
	// (children of future-deferred parents, descendants of filter.ParentID,
	// each family's stale-flagged rows), shared by both families. A family's
	// stale rows are read only when the probe statement saw one, so the common
	// case, no stale flag, adds no statement of its own. Its EXISTS still
	// visits every flagged row to prove that (StaleBlockedIDsSQL).
	inputs sqlbuild.ReadyWorkWhereInputs
}

const futureDeferredPredicate = "defer_until IS NOT NULL AND defer_until > UTC_TIMESTAMP()"

// probeReadyWorkInTx resolves a readyWorkProbe for filter.
//
// The existence facts are answered by ONE statement of EXISTS subqueries,
// which is portable across every SQL dialect the shared issueops code runs on
// (no window functions, no FROM-less dialect quirks beyond a SELECT of
// scalar subqueries). EXISTS rather than COUNT so each leg stops at its first
// row.
//
// A pre-migration database may legitimately lack the wisp plane, and a single
// statement cannot tolerate one missing table while reading the rest, so a
// missing-table failure falls back to the per-table probes — the exact
// statements, tolerances and error classification these facts were always
// resolved with.
//
// wantCollision asks for the issues/wisps ID-collision fact, which only a
// caller sizing the merged ready set needs.
func probeReadyWorkInTx(ctx context.Context, tx DBTX, filter types.WorkFilter, wantCollision bool) (*readyWorkProbe, error) {
	p := &readyWorkProbe{}
	excludeDeferred := !filter.IncludeDeferred

	// Which columns this statement carries depends on the filter, so each fact
	// records the index it landed on as it is appended rather than the reader
	// below counting appends back. Two of these columns are conditional, so a
	// hardcoded index is a fact read from the wrong column the first time a
	// new one is inserted above it — and the fact being misread would be the
	// ready predicate.
	cols := make([]string, 0, 7) // the four unconditional facts plus the three conditional ones
	appendFact := func(sql string) int {
		cols = append(cols, sql)
		return len(cols) - 1
	}

	iWispsPresent := appendFact("EXISTS (SELECT 1 FROM wisps)")
	// Only the table REFERENCE matters here: the statement fails with a
	// missing-table error when wisp_dependencies is absent, and an empty
	// table still exists (optionalTableExistsInTx's contract). The value
	// is not read, so this fact keeps no index.
	appendFact("EXISTS (SELECT 1 FROM wisp_dependencies)")
	iIssuesStale := appendFact("EXISTS (" + sqlbuild.StaleBlockedIDsSQL(IssuesFilterTables) + ")")
	iWispsStale := appendFact("EXISTS (" + sqlbuild.StaleBlockedIDsSQL(WispsFilterTables) + ")")

	iIssuesDeferred, iWispsDeferred := -1, -1
	if excludeDeferred {
		iIssuesDeferred = appendFact("EXISTS (SELECT 1 FROM issues WHERE " + futureDeferredPredicate + ")")
		iWispsDeferred = appendFact("EXISTS (SELECT 1 FROM wisps WHERE " + futureDeferredPredicate + ")")
	}
	iCollision := -1
	if wantCollision {
		iCollision = appendFact("EXISTS (SELECT 1 FROM issues JOIN wisps ON wisps.id = issues.id)")
	}
	flags := make([]bool, len(cols))
	dest := make([]any, len(cols))
	for i := range flags {
		dest[i] = &flags[i]
	}

	hasDeferredParent := false
	err := tx.QueryRowContext(ctx, "SELECT "+strings.Join(cols, ", ")).Scan(dest...)
	switch {
	case err == nil:
		p.wispsPresent = flags[iWispsPresent]
		// The statement ran, so every table it names exists.
		p.wispDepsExist = true
		if excludeDeferred {
			hasDeferredParent = flags[iIssuesDeferred] || flags[iWispsDeferred]
		}
		if iCollision >= 0 {
			// Without a wisp row there is nothing to collide with.
			p.idCollision = p.wispsPresent && flags[iCollision]
		}
		if hasDeferredParent {
			// Every table the child scan reads is known to exist, so its
			// four legs can go out as one statement.
			children, dcErr := getDeferredChildrenAllTablesInTx(ctx, tx)
			if dcErr != nil {
				return nil, fmt.Errorf("compute deferred parent children: %w", dcErr)
			}
			p.inputs.DeferredChildIDs = children
		}
		if err := p.readStaleBlockedInTx(ctx, tx, flags[iIssuesStale], flags[iWispsStale]); err != nil {
			return nil, err
		}
	case isTableNotExistError(err):
		if err := p.probeTableByTableInTx(ctx, tx, excludeDeferred); err != nil {
			return nil, err
		}
		// Not probed exactly; assume the worst so a total is never
		// under-corrected.
		p.idCollision = wantCollision && p.wispsPresent
	default:
		return nil, fmt.Errorf("ready probe: %w", err)
	}

	// Parent filtering: all transitive descendants of parentID (GH#3396).
	if filter.ParentID != nil {
		descendantIDs, descErr := GetDescendantIDsInTx(ctx, tx, *filter.ParentID, 0)
		if descErr != nil {
			return nil, fmt.Errorf("get parent descendants: %w", descErr)
		}
		p.inputs.ParentDescendantIDs = descendantIDs
	}
	return p, nil
}

// probeTableByTableInTx is the per-table fallback for a database missing part
// of the wisp plane.
func (p *readyWorkProbe) probeTableByTableInTx(ctx context.Context, tx DBTX, excludeDeferred bool) error {
	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return fmt.Errorf("wisp dependency probe: %w", err)
	}
	p.wispDepsExist = wispDepsExist
	empty, err := wispsTableEmptyOrMissingInTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("wisp probe: %w", err)
	}
	p.wispsPresent = !empty
	if excludeDeferred {
		children, err := getChildrenOfDeferredParentsInTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("compute deferred parent children: %w", err)
		}
		p.inputs.DeferredChildIDs = children
	}
	// No probe answered whether a flag is stale, so read both sets; the wisp
	// one only where the wisp family is read at all.
	return p.readStaleBlockedInTx(ctx, tx, true, p.readsWisps())
}

// readStaleBlockedInTx reads the stale-flagged rows (sqlbuild.StaleBlockedIDsSQL)
// of each family it is told holds one, which the ready predicate reads around
// (GH#3268). The wisp set is read only for a ready set that reads wisps.
func (p *readyWorkProbe) readStaleBlockedInTx(ctx context.Context, tx DBTX, issues, wisps bool) error {
	read := func(tables FilterTables) error {
		ids, err := queryIDs(ctx, tx, sqlbuild.StaleBlockedIDsSQL(tables))
		if err != nil {
			return fmt.Errorf("stale blocked flags in %s: %w", tables.Main, err)
		}
		if p.inputs.StaleBlockedIDs == nil {
			p.inputs.StaleBlockedIDs = make(map[string][]string, 2)
		}
		p.inputs.StaleBlockedIDs[tables.Main] = ids
		return nil
	}
	if issues {
		if err := read(IssuesFilterTables); err != nil {
			return err
		}
	}
	if wisps && p.readsWisps() {
		return read(WispsFilterTables)
	}
	return nil
}

// readsWisps reports whether the wisp family takes part in the ready set:
// GetReadyWorkWithCountsInTx has always read it only when the wisps table has
// rows and wisp_dependencies exists.
func (p *readyWorkProbe) readsWisps() bool {
	return p.wispsPresent && p.wispDepsExist
}
