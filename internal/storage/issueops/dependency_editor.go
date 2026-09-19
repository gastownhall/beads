package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateAddDependenciesRequest applies the request rules every
// DependencyEditor implementation shares. It lives here rather than in each of
// them because a rule enforced on one backend and not the other is not a
// contract.
//
// The self-dependency refusal is here, ahead of the per-edge cycle probe and
// for EVERY edge type, for the reason the domain path states: a blocking
// self-edge otherwise trips the cycle check and reports the wrong refusal, and
// SkipPerEdgeCycleCheck would skip it entirely.
//
// The type check is that there IS a type — non-empty, within the column's
// length. It is deliberately not a membership test: the vocabulary is an open,
// workspace-configurable set (see the Dep* constants), so refusing an unlisted
// type would refuse a workspace's own.
func ValidateAddDependenciesRequest(request publicops.AddDependenciesRequest) error {
	if request.Actor == "" {
		return fmt.Errorf("%w: add dependencies requires an actor", storage.ErrValidation)
	}
	if len(request.Edges) == 0 {
		return fmt.Errorf("%w: add dependencies requires at least one edge", storage.ErrValidation)
	}
	for i, edge := range request.Edges {
		if edge.IssueID == "" || edge.DependsOnID == "" {
			return fmt.Errorf("%w: add dependencies edge %d requires both endpoints", storage.ErrValidation, i)
		}
		if !edge.Type.IsValid() {
			return fmt.Errorf("%w: add dependencies edge %d requires a dependency type (max %d chars)",
				storage.ErrValidation, i, types.MaxDependencyTypeLen)
		}
		if edge.IssueID == edge.DependsOnID {
			return fmt.Errorf("%w: %s cannot depend on itself", domain.ErrSelfDependency, edge.IssueID)
		}
	}
	return nil
}

// ValidateRemoveDependencyRequest applies the request rules every
// DependencyEditor implementation shares for a removal.
func ValidateRemoveDependencyRequest(request publicops.RemoveDependencyRequest) error {
	if request.Actor == "" {
		return fmt.Errorf("%w: remove dependency requires an actor", storage.ErrValidation)
	}
	if request.IssueID == "" || request.DependsOnID == "" {
		return fmt.Errorf("%w: remove dependency requires both endpoints", storage.ErrValidation)
	}
	return nil
}

// AddDependenciesCommitMessage is the history entry an edge assertion records.
//
// Unlike CloseBatchCommitMessage it is composed from the REQUEST rather than
// the result, and that difference is the contract's: the request is
// all-or-nothing, so what was asked for is exactly what landed or nothing did.
// A batch close has to name what landed because it can skip an id; this cannot.
//
// The two spellings are the two the CLI already wrote — one edge names both
// endpoints, several name their count — because the role is what both `bd dep
// add <a> <b>` and `bd dep add --file` now go through, and `bd dolt log` should
// keep reading the way it did. The count spelling is keyed on the EDGE COUNT
// and not on which flag was used, so a one-line bulk file now names its edge
// instead of reading "add 1 edges": one edge is one edge however it was
// spelled.
func AddDependenciesCommitMessage(request publicops.AddDependenciesRequest) string {
	if len(request.Edges) == 1 {
		return "bd: dep add " + request.Edges[0].IssueID + " " + request.Edges[0].DependsOnID
	}
	return fmt.Sprintf("dependency: add %d edges", len(request.Edges))
}

// RemoveDependencyCommitMessage is the history entry a removal records.
func RemoveDependencyCommitMessage(issueID, dependsOnID string) string {
	return "bd: dep remove " + issueID + " " + dependsOnID
}

// ExecuteAddDependencies asserts every requested edge in tx and reports the
// durable tables changed. It is the store-backed body behind the
// DependencyEditor accessor; the unit-of-work provider has its own, for the
// reason Lifecycle does.
//
// ALL-OR-NOTHING is enforced by doing nothing special: the first refusal
// returns an error, and the caller's transaction rolls every earlier edge back
// with it. There is no per-edge outcome to report because there is no outcome
// but the request's.
//
// Edges are applied PARENT-CHILD FIRST regardless of request order, so the
// complete planned hierarchy is visible before any blocking edge is validated
// against it — an ordering the direct bulk path already relies on. The
// whole-graph gate at the end runs even when the per-edge probe was skipped:
// per-edge checks cannot see a path that only exists once several of this
// request's own edges are in place.
//
// Both orderings hold ACROSS THE TWO PLANES, not within each: a request may
// mix wisp-sourced and issue-sourced edges, and the hierarchy a blocking edge
// is validated against, like the graph the final gate walks, spans both
// tables. That is why the phase loop and the gate are over the whole request
// and the plane is decided per edge, one level down.
func ExecuteAddDependencies(ctx context.Context, tx *sql.Tx, request publicops.AddDependenciesRequest) (publicops.AddDependenciesResult, ChangedTables, error) {
	// One scoped query for the whole request instead of a probe per edge: a
	// bulk `bd dep add --file` against a remote Dolt pays WAN latency per
	// round-trip (GH#3414). Nothing an edge write does moves a source between
	// planes, so a set read once up front stays true for the transaction.
	sources := make([]string, 0, len(request.Edges))
	for _, edge := range request.Edges {
		sources = append(sources, edge.IssueID)
	}
	wispSources, err := WispIDSetInTx(ctx, tx, sources)
	if err != nil {
		return publicops.AddDependenciesResult{}, nil, err
	}

	tables := ChangedTables{}
	for phase := 0; phase < 2; phase++ {
		parentPhase := phase == 0
		for _, edge := range request.Edges {
			if (edge.Type == types.DepParentChild) != parentPhase {
				continue
			}
			_, sourceIsWisp := wispSources[edge.IssueID]
			edgeTables, err := addDependencyEdgeInTx(ctx, tx, request, edge, sourceIsWisp)
			if err != nil {
				return publicops.AddDependenciesResult{}, nil, err
			}
			tables.Merge(edgeTables)
		}
	}
	if err := checkAddedEdgesForCycles(ctx, tx, request.Edges); err != nil {
		return publicops.AddDependenciesResult{}, nil, err
	}
	added := make([]publicops.DependencyEdge, len(request.Edges))
	copy(added, request.Edges)
	return publicops.AddDependenciesResult{Added: added}, tables, nil
}

// addDependencyEdgeInTx writes one edge and reports the durable tables it
// touched.
//
// The edge FOLLOWS ITS SOURCE, the same rule the removal reads and the same
// one the stores' own dependency verb applied before this role existed: a wisp
// has no row in the issues plane, so pinning the source there refused every
// `bd dep add <wisp-id> <target>` with "issue not found". Source class decides
// the dependency table and, with it, the event table. The target's routing
// stays independently detected: either class may legitimately depend on the
// other.
//
// Refusals pass through UNWRAPPED. Every one of them —
// *DependencyTypeConflictError, *DependencyHierarchyConflictError,
// ErrDependencyCycle, a missing endpoint — is already a complete sentence, and
// the store's own dependency verb has always surfaced them verbatim.
func addDependencyEdgeInTx(ctx context.Context, tx *sql.Tx, request publicops.AddDependenciesRequest, edge publicops.DependencyEdge, sourceIsWisp bool) (ChangedTables, error) {
	sourceTable, _, eventTable, depTable := WispTableRouting(sourceIsWisp)

	dep := &types.Dependency{IssueID: edge.IssueID, DependsOnID: edge.DependsOnID, Type: edge.Type}
	eventWritten, err := AddDependencyInTx(ctx, tx, dep, request.Actor, AddDependencyOpts{
		SourceTable:    sourceTable,
		WriteTable:     depTable,
		IsCrossPrefix:  types.ExtractPrefix(edge.IssueID) != types.ExtractPrefix(edge.DependsOnID),
		SkipCycleCheck: request.SkipPerEdgeCycleCheck,
		EmitEvent:      true,
	})
	if err != nil {
		return nil, err
	}
	// Stage the source's dependency table always and its events table only
	// when a row was recorded — the same selective staging the stores' own
	// AddDependencyWithOptions does, so an idempotent re-add cannot sweep
	// unrelated pending event rows into this commit (GH#2455). Naming the
	// source's own pair rather than the durable pair is what keeps that true
	// across the planes: ChangedTables drops the wisp tables itself, so a
	// wisp-sourced edge stages nothing and a request made entirely of them
	// commits nothing, which is right — the wisp plane is dolt-ignored and has
	// no version history to write.
	//
	// This is NOT every table the edge wrote. A blocking edge also flips the
	// source's is_blocked (markDirectBlockingDependencySourceInTx, then
	// MarkIsBlockedInTx over the affected ids), and neither `issues` nor any
	// other issue-plane table is ever staged here. On an issue-sourced
	// blocking add the flip therefore stays in the working set and is swept up
	// by whatever command auto-commits next — including one that was REFUSED,
	// which then shows up in history having changed nothing of its own. That
	// predates this role: origin/main's dolt.AddDependencyWithOptions staged
	// the same {dependencies, events} pair. Tracked as bd-2y9ke; fixing it
	// changes what every dep-add commit contains and belongs in its own change
	// with its own tests, so do not widen the set here without reading that
	// bead first.
	tables := ChangedTables{}
	tables.Add(depTable)
	if eventWritten {
		tables.Add(eventTable)
	}
	return tables, nil
}

// checkAddedEdgesForCycles runs the whole-graph gate over the scheduling edges
// this request added. Per-edge probes cannot see a path that only closes once
// several of the request's own edges exist, and SkipPerEdgeCycleCheck turns
// them off entirely, so this is the check that actually holds the invariant.
//
// The message is built through domain.NewCycleError so it errors.Is-matches
// ErrDependencyCycle while rendering byte-for-byte what the direct bulk path
// already prints.
func checkAddedEdgesForCycles(ctx context.Context, tx *sql.Tx, edges []publicops.DependencyEdge) error {
	var pairs [][2]string
	for _, edge := range edges {
		if !types.IsSchedulingEdge(edge.Type) {
			continue
		}
		pairs = append(pairs, [2]string{edge.IssueID, edge.DependsOnID})
	}
	if len(pairs) == 0 {
		return nil
	}
	graph := make(map[string][]string)
	if err := AppendSchedulingGraphInTx(ctx, tx, cycleDetectionTables(), graph); err != nil {
		return fmt.Errorf("final cycle check failed (no edges added): %w", err)
	}
	if cyclePath := CycleThroughEdgesInGraph(graph, pairs); cyclePath != "" {
		return domain.NewCycleError("dependency cycle would be created: %s (no edges added; run 'bd dep cycles' for analysis)", cyclePath)
	}
	return nil
}

// ExecuteRemoveDependency removes one edge in tx and reports the durable
// tables changed.
//
// A missing edge reports Removed false and NO changed tables, which is how the
// callers spell "commit nothing": removing an edge that was never there leaves
// the graph it already had, and a history entry for it would be a commit with
// nothing in it.
func ExecuteRemoveDependency(ctx context.Context, tx *sql.Tx, request publicops.RemoveDependencyRequest) (publicops.RemoveDependencyResult, ChangedTables, error) {
	// Routing is READ here rather than pinned, unlike the add. A removal
	// cannot put an edge anywhere, so pinning it would only mean failing to
	// remove an edge the caller named — and the staging has to name the tables
	// the delete actually touched or the commit sweeps rows it never wrote
	// (GH#2455). ChangedTables drops the wisp tables itself.
	sourceIsWisp := IsActiveWispInTx(ctx, tx, request.IssueID)
	_, _, eventTable, depTable := WispTableRouting(sourceIsWisp)

	// RemoveDependencyInTx reports whether it recorded an event, which on the
	// explicit-verb path is exactly "an edge was there and is gone" — it
	// returns early without emitting when no row matched.
	eventWritten, err := RemoveDependencyInTx(ctx, tx, request.IssueID, request.DependsOnID, request.Actor, true)
	if err != nil {
		return publicops.RemoveDependencyResult{}, nil, err
	}
	if !eventWritten {
		return publicops.RemoveDependencyResult{Removed: false}, nil, nil
	}
	tables := ChangedTables{}
	tables.Add(depTable, eventTable)
	return publicops.RemoveDependencyResult{Removed: true}, tables, nil
}
