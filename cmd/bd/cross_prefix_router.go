package main

import (
	"context"
	"errors"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
)

// cliIssueRouter is the IssueRouter the direct CLI hands the external
// dependency policy for bare cross-prefix blocking targets (GH#4647): it
// routes through the SAME read-only sequence a routed command's read uses —
// lookupPrefixRoute (routes.jsonl longest-match) → admitForeignWorkspace →
// loadDoltDatabase → openRoutedTargetStore(false) — never a write, never a
// partial-id guess: the ids arrive as the store stored them.
//
// It groups ids by their matched DESTINATION (the resolved .beads directory,
// not the prefix: longest-match routes can send different prefixes to one
// rig) and opens each destination once per evaluation, honoring ctx and
// closing what it opened.
//
// It is per-process CLI machinery: openRoutedTargetStore swaps the
// process-global BEADS_DOLT_SERVER_DATABASE around its open (routed.go), so
// this router must never run from a server handling concurrent requests —
// directPolicyRouterFor keeps it off bd serve, and the proxied provider's
// policy instance is constructed without it.
type cliIssueRouter struct{}

// routedDestination is one prefix-route target with the ids its route
// matched: the route (for messages), the resolved .beads directory, and the
// ids to read there.
type routedDestination struct {
	route    *prefixRoute
	beadsDir string
	ids      []string
}

// directPolicyRouterFor returns the cross-prefix issue router for a policy
// instance the direct CLI composes, or nil where the design bars it. bd serve
// serves its roles from the same decorated store chain this is injected
// into, so giving that chain a router would route from a server answering
// concurrent requests — the one place the env-var-swapping open is unsafe.
// The proxied CLI never asks: its policy rides the unit-of-work provider,
// which is wired without a router.
func directPolicyRouterFor(cmd *cobra.Command) externaldeps.IssueRouter {
	for c := cmd; c != nil; c = c.Parent() {
		if c == serveCmd {
			return nil
		}
	}
	return cliIssueRouter{}
}

// ResolveIssueStatuses implements externaldeps.IssueRouter.
func (cliIssueRouter) ResolveIssueStatuses(ctx context.Context, ids []string) (map[string]externaldeps.RoutedIssue, error) {
	result := make(map[string]externaldeps.RoutedIssue, len(ids))

	// Group the ids a route matched by their actual destination. One prefix
	// identifies one longest-match route, but two prefixes can share a
	// destination, and one destination must be opened once.
	destinations := make(map[string]*routedDestination)
	var order []string // deterministic open order
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		route, beadsDir, err := lookupPrefixRoute(id)
		switch {
		case err == nil:
			d, ok := destinations[beadsDir]
			if !ok {
				d = &routedDestination{route: route, beadsDir: beadsDir}
				destinations[beadsDir] = d
				order = append(order, beadsDir)
			}
			d.ids = append(d.ids, id)
		case errors.Is(err, errRoutingUnavailable):
			// No route, or one pointing back at this database: the target was
			// never this workspace's business to verify (AC-7's
			// compatibility verdict — non-blocking, and the decorator never
			// warns for it).
			result[id] = externaldeps.RoutedIssue{Verdict: externaldeps.IssueRouteUnrouted}
		default:
			// A routes.jsonl that cannot be read, or another operational
			// lookup failure: fail closed, with the decorator's
			// one-per-prefix warning.
			result[id] = externaldeps.RoutedIssue{Verdict: externaldeps.IssueRouteUnavailable}
		}
	}

	for _, key := range order {
		d := destinations[key]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		verdict, statuses, err := routedDestinationStatuses(ctx, d)
		if err != nil {
			return nil, err
		}
		for _, id := range d.ids {
			if status, found := statuses[id]; found {
				result[id] = externaldeps.RoutedIssue{Verdict: externaldeps.IssueRouteFound, Status: status}
			} else {
				// Every id of this destination that the read did not find
				// gets the destination's verdict: absent when the owning
				// store answered, unavailable when the route could not.
				result[id] = externaldeps.RoutedIssue{Verdict: verdict}
			}
		}
	}
	return result, nil
}

// routedDestinationStatuses opens ONE routed destination read-only and reads
// its exact ids in one batch. The verdict it returns is for the ids the batch
// read did not find: IssueRouteAbsent when the read succeeded (no such row),
// or IssueRouteUnavailable when the route could not answer at all — the
// admission, configuration, open and read failures. A proxied-server
// workspace no direct store can open is NOT a failure:
// proxy.store.unrouted becomes IssueRouteUnrouted, an unsupported mode
// (AC-7), and never warns.
func routedDestinationStatuses(ctx context.Context, d *routedDestination) (externaldeps.IssueRouteVerdict, map[string]types.Status, error) {
	const unavailable = externaldeps.IssueRouteUnavailable

	// Refuse what admission refuses from the target's own contents first, as
	// an auto-route does (openAutoRoutedStore): report it as the route being
	// unavailable, never dig into the rig.
	if _, err := admitForeignWorkspace(d.beadsDir); err != nil {
		return unavailable, nil, nil
	}
	targetDB, err := loadDoltDatabase(d.beadsDir)
	if err != nil || targetDB == "" {
		// An unreadable or absent dolt_database is a configured route that
		// cannot answer: fail closed.
		return unavailable, nil, nil
	}
	// The read-only open: a status probe must never write migrations or
	// history into the target rig (bd-6dnrw.32, GH#3231).
	targetStore, err := openRoutedTargetStore(ctx, d.beadsDir, targetDB, false)
	if err != nil {
		if isProxiedStoreUnroutedErr(err) {
			return externaldeps.IssueRouteUnrouted, nil, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return unavailable, nil, ctxErr
		}
		return unavailable, nil, nil
	}
	issues, err := targetStore.GetIssuesByIDs(ctx, d.ids)
	_ = targetStore.Close()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return unavailable, nil, ctxErr
		}
		return unavailable, nil, nil
	}
	statuses := make(map[string]types.Status, len(issues))
	for _, issue := range issues {
		if issue != nil {
			statuses[issue.ID] = issue.Status
		}
	}
	return externaldeps.IssueRouteAbsent, statuses, nil
}

// isProxiedStoreUnroutedErr reports whether err is the typed
// proxy.store.unrouted refusal — a routing gap on a proxied-server
// workspace, not a route that failed.
func isProxiedStoreUnroutedErr(err error) bool {
	var capErr *ProxyCapabilityError
	return errors.As(err, &capErr) && capErr.Code == "proxy.store.unrouted"
}
