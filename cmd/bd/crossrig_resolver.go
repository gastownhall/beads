package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
)

// prefixRouteResolver implements externaldeps.ForeignIssueResolver from the
// source workspace's own prefix routes (routes.jsonl in its beads directory,
// GH#5784): a bare cross-prefix target — "hq-xyz" named by a "gt-" issue —
// resolves through the route whose prefix it carries, into the target rig's
// own database, opened read-only with that database selected per open
// (openRoutedTargetStore's read arm, never the environment swap). The routes
// are read from the DEPENDENT's workspace, not the directory the process was
// launched from (§208): a town-root launch must resolve a rig's dependencies
// through that rig's routes.
//
// Per-target availability is in-band (ForeignIssueResult.Reason): a missing
// route, a route admission refuses, an unreadable target and a target the
// owning rig does not answer for all leave that target blocking — one target
// never poisons another that shares its prefix (§200). Only context
// cancellation is an operation error, which the policy layer propagates:
// everything else is a blocking result, not a failed query.
type prefixRouteResolver struct {
	// beadsDir is the source workspace whose routes.jsonl governs. Empty
	// means no workspace was resolved for the store; every target then has
	// no route and fails closed.
	beadsDir string
}

// newPrefixRouteResolver builds the bare-target resolver for the workspace
// that owns the store it is injected into.
func newPrefixRouteResolver(beadsDir string) externaldeps.ForeignIssueResolver {
	return &prefixRouteResolver{beadsDir: beadsDir}
}

// ResolveForeignIssues answers each id with the issue its prefix route's rig
// holds, or with the reason it cannot be answered. It issues one exact-ID
// batch read per distinct route, so a page of bare targets against one rig
// costs one foreign open, and every target the route's rig does not answer
// keeps its per-id reason without affecting the others.
func (r *prefixRouteResolver) ResolveForeignIssues(ctx context.Context, ids []string) (map[string]*externaldeps.ForeignIssueResult, error) {
	out := make(map[string]*externaldeps.ForeignIssueResult, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	routes, err := loadPrefixRoutes(r.beadsDir)
	if err != nil {
		// No routes.jsonl is a workspace with no route to follow: every
		// target has no route, in-band (§200). A routes file that cannot be
		// read for any other reason is the same per-target unavailability —
		// the resolver answers readiness queries, and one unreadable file
		// must not fail the query the targets would have blocked for anyway.
		if !errors.Is(err, fs.ErrNotExist) {
			for _, id := range ids {
				out[id] = &externaldeps.ForeignIssueResult{Reason: fmt.Sprintf("loading routes: %v", err)}
			}
			return out, nil
		}
		return r.unrouted(out, ids, "no routes available"), nil
	}
	if len(routes) == 0 {
		return r.unrouted(out, ids, "no routes available"), nil
	}

	// Group the targets by the route each one matches, preserving the order
	// the ids arrived in within each group. A target with no matching route —
	// or one whose route points back at this workspace's own database, which
	// the local read already had its chance at — fails closed on its own.
	byRoute := make(map[*prefixRoute][]string)
	for _, id := range ids {
		route := matchPrefixRoute(routes, id)
		if route == nil {
			out[id] = &externaldeps.ForeignIssueResult{Reason: fmt.Sprintf("no route for prefix %q", types.ExtractPrefix(id))}
			continue
		}
		if route.Path == "." {
			out[id] = &externaldeps.ForeignIssueResult{Reason: "route points back to this workspace's own database"}
			continue
		}
		byRoute[route] = append(byRoute[route], id)
	}

	for route, targets := range byRoute {
		if err := r.resolveRouteTargets(ctx, out, route, targets); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// unrouted marks every id unavailable with reason, the answer for a workspace
// with no routes at all.
func (r *prefixRouteResolver) unrouted(out map[string]*externaldeps.ForeignIssueResult, ids []string, reason string) map[string]*externaldeps.ForeignIssueResult {
	for _, id := range ids {
		out[id] = &externaldeps.ForeignIssueResult{Reason: reason}
	}
	return out
}

// resolveRouteTargets answers one route's targets from that route's rig. Every
// failure short of cancellation is written as the route's per-target reason:
// the targets stay blocking, and the rest of the answer stands.
func (r *prefixRouteResolver) resolveRouteTargets(ctx context.Context, out map[string]*externaldeps.ForeignIssueResult, route *prefixRoute, targets []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unavailable := func(format string, args ...any) {
		reason := fmt.Sprintf(format, args...)
		for _, id := range targets {
			out[id] = &externaldeps.ForeignIssueResult{Reason: reason}
		}
	}

	targetBeadsDir := prefixRouteTargetBeadsDir(r.beadsDir, route)

	// Refuse what admission refuses from the target's own contents first, as
	// an auto-route does (openAutoRoutedStore): the dolt_database lookup below
	// would otherwise report a historical SQLite or legacy Dolt rig as a
	// missing dolt_database field.
	if _, err := admitForeignWorkspace(targetBeadsDir); err != nil {
		unavailable("route %q to %s: %v", route.Prefix, route.Path, err)
		return nil
	}

	// The target's own dolt_database must be known before the open: with the
	// per-open read path there is no environment swap to fall back on, and an
	// open without a database would resolve to the LAUNCHER's database.
	targetDB, err := loadDoltDatabase(targetBeadsDir)
	if err != nil || targetDB == "" {
		if err == nil {
			err = fmt.Errorf("%s has no dolt_database field", filepath.Join(targetBeadsDir, "metadata.json"))
		}
		unavailable("route %q to %s: target rig has no dolt_database configured: %v", route.Prefix, route.Path, err)
		return nil
	}

	foreign, err := openRoutedTargetStore(ctx, targetBeadsDir, targetDB, false)
	if err != nil || foreign == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		unavailable("opening routed store for %s failed: %v", route.Path, err)
		return nil
	}

	issues, err := foreign.GetIssuesByIDs(ctx, targets)
	_ = foreign.Close()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		unavailable("reading routed store for %s failed: %v", route.Path, err)
		return nil
	}
	for _, issue := range issues {
		if issue != nil {
			out[issue.ID] = &externaldeps.ForeignIssueResult{Issue: issue}
		}
	}
	// Targets the rig did not answer stay absent: the policy layer reports
	// them as unresolved ("the owning rig did not answer for it") without
	// this resolver inventing a row or a reason for them.
	return nil
}
