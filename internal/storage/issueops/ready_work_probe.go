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
	// (children of future-deferred parents, descendants of filter.ParentID),
	// shared by both families.
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

	cols := []string{
		"EXISTS (SELECT 1 FROM wisps)",
		// Only the table REFERENCE matters here: the statement fails with a
		// missing-table error when wisp_dependencies is absent, and an empty
		// table still exists (optionalTableExistsInTx's contract). The value
		// is not read.
		"EXISTS (SELECT 1 FROM wisp_dependencies)",
	}
	if excludeDeferred {
		cols = append(cols,
			"EXISTS (SELECT 1 FROM issues WHERE "+futureDeferredPredicate+")",
			"EXISTS (SELECT 1 FROM wisps WHERE "+futureDeferredPredicate+")")
	}
	if wantCollision {
		cols = append(cols, "EXISTS (SELECT 1 FROM issues JOIN wisps ON wisps.id = issues.id)")
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
		p.wispsPresent = flags[0]
		// The statement ran, so every table it names exists.
		p.wispDepsExist = true
		next := 2
		if excludeDeferred {
			hasDeferredParent = flags[2] || flags[3]
			next = 4
		}
		// Without a wisp row there is nothing to collide with.
		p.idCollision = wantCollision && p.wispsPresent && flags[next]
		if hasDeferredParent {
			// Every table the child scan reads is known to exist, so its
			// four legs can go out as one statement.
			children, dcErr := getDeferredChildrenAllTablesInTx(ctx, tx)
			if dcErr != nil {
				return nil, fmt.Errorf("compute deferred parent children: %w", dcErr)
			}
			p.inputs.DeferredChildIDs = children
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
	return nil
}

// readsWisps reports whether the wisp family takes part in the ready set:
// GetReadyWorkWithCountsInTx has always read it only when the wisps table has
// rows and wisp_dependencies exists.
func (p *readyWorkProbe) readsWisps() bool {
	return p.wispsPresent && p.wispDepsExist
}
