package main

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// gateDepLoader reads the dependency records of a page of ids.
type gateDepLoader func(ctx context.Context, ids []string) (map[string][]*types.Dependency, error)

// gateHydrator resolves the gate CANDIDATES a page's edges name into issues.
type gateHydrator func(ctx context.Context, ids []string) ([]*types.Issue, error)

// gatesByIssueID returns, per listed id, the OPEN gates holding it back — the
// ids themselves, so a row can name the gate rather than only admit one exists.
// An issue with no live gate is absent from the map.
//
// BATCHED BY ID SET, never per row: the dependency rows for the whole page come
// back in one call (or from the map a tree view has already loaded), and the
// gate candidates they name are hydrated in one more. The COST BOUND is per
// page, not per row — two calls whatever the page length — though each of those
// calls is itself a few round trips (the id-set readers partition wisps from
// issues and then batch their INs), so "two queries" understates the wire
// traffic and "per page" is the promise that matters.
//
// The verdict is types.GateIsHolding plus types.SubjectCanBeGated — the one
// predicate `bd show`, the detail view and the readiness query's is_blocked
// column all share — so a glyph here and a GATED header there cannot say
// different things about the same bead.
//
// Best effort: a read that fails yields no decoration rather than failing the
// listing, matching how `bd list` already treats its blocking annotation.
func gatesByIssueID(
	ctx context.Context,
	issues []*types.Issue,
	preloadedDeps map[string][]*types.Dependency,
	loadDeps gateDepLoader,
	hydrate gateHydrator,
) map[string][]string {
	if len(issues) == 0 || hydrate == nil {
		return nil
	}

	// The SUBJECT clause, asked once here so no formatter has to re-ask it: a
	// closed or pinned bead is out of `bd ready` for its own reasons and is
	// never decorated, so it does not even join the dependency read.
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if !types.SubjectCanBeGated(issue) {
			continue
		}
		ids = append(ids, issue.ID)
	}
	if len(ids) == 0 {
		return nil
	}

	depsByIssue := preloadedDeps
	if depsByIssue == nil {
		if loadDeps == nil {
			return nil
		}
		var err error
		depsByIssue, err = loadDeps(ctx, ids)
		if err != nil {
			return nil
		}
	}

	// Candidate targets: the gate-rule edges of the ids on this page. A gate
	// reached over any other edge (waits-for, parent-child, relates-to) does
	// not hold anything back by the status rule, and the predicate would
	// reject it anyway; skipping it here keeps the hydration batch small.
	var targetIDs []string
	seen := make(map[string]bool)
	for _, id := range ids {
		for _, dep := range depsByIssue[id] {
			if dep == nil || !types.IsGateEdge(dep.Type) || seen[dep.DependsOnID] {
				continue
			}
			seen[dep.DependsOnID] = true
			targetIDs = append(targetIDs, dep.DependsOnID)
		}
	}
	if len(targetIDs) == 0 {
		return nil
	}

	targets, err := hydrate(ctx, targetIDs)
	if err != nil {
		return nil
	}
	targetByID := make(map[string]*types.Issue, len(targets))
	for _, target := range targets {
		if target != nil {
			targetByID[target.ID] = target
		}
	}

	gated := make(map[string][]string)
	for _, id := range ids {
		for _, dep := range depsByIssue[id] {
			if dep == nil {
				continue
			}
			if types.GateIsHolding(dep.Type, targetByID[dep.DependsOnID]) {
				gated[id] = append(gated[id], dep.DependsOnID)
			}
		}
	}
	if len(gated) == 0 {
		return nil
	}
	return gated
}

// gatedIssueIDs is gatesByIssueID on the direct (embedded / non-proxied) route.
func gatedIssueIDs(
	ctx context.Context,
	st storage.DoltStorage,
	issues []*types.Issue,
	preloadedDeps map[string][]*types.Dependency,
) map[string][]string {
	if st == nil {
		return nil
	}
	return gatesByIssueID(ctx, issues, preloadedDeps,
		func(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
			return st.GetDependencyRecordsForIssues(ctx, ids)
		},
		func(ctx context.Context, ids []string) ([]*types.Issue, error) {
			return st.GetIssuesByIDs(ctx, ids)
		})
}

// proxiedGatedIssueIDs is gatesByIssueID on the proxied-server route, reading
// through a unit of work the caller already holds. The proxied --json routes
// get gated_by from workapi.BuildIssueDetails; this is what keeps the proxied
// TEXT routes from rendering a plain OPEN row for the same bead.
func proxiedGatedIssueIDs(
	ctx context.Context,
	uw uow.UnitOfWork,
	issues []*types.Issue,
	preloadedDeps map[string][]*types.Dependency,
) map[string][]string {
	if uw == nil {
		return nil
	}
	return gatesByIssueID(ctx, issues, preloadedDeps,
		func(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
			return uw.DependencyUseCase().GetForIssueIDs(ctx, ids)
		},
		func(ctx context.Context, ids []string) ([]*types.Issue, error) {
			return uw.IssueUseCase().GetIssuesByIDs(ctx, ids)
		})
}

// proxiedGatedIssueIDsOwnUOW serves the proxied compact and agent listings,
// which reach their blocking annotation through a role rather than a unit of
// work and so hold none to lend. It opens one, reads, and closes it; a failure
// to open yields no decoration, because a listing that renders is worth more
// than one that refuses over a glyph.
func proxiedGatedIssueIDsOwnUOW(ctx context.Context, issues []*types.Issue) map[string][]string {
	if len(issues) == 0 {
		return nil
	}
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return nil
	}
	defer uw.Close(ctx)
	return proxiedGatedIssueIDs(ctx, uw, issues, nil)
}
