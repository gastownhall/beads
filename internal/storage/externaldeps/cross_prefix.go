package externaldeps

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// THE CROSS-PREFIX ROUTER.
//
// A bare cross-prefix dependency target (GH#4647) is an issue id whose prefix
// names another repository: `bd dep add be-a gt-b` stores "gt-b" in
// depends_on_external beside "external:" refs, because IsExternalDepTarget
// calls a foreign-prefixed id external — it has no row in this database and
// the column carries no foreign key into issues. Unlike an external:
// capability ref the target IS an issue somewhere, and this workspace can
// often say which rig owns it: routes.jsonl. So the blocking policy resolves
// these targets through an IssueRouter the embedding CLI supplies, and treats
// what comes back by the same yardstick a local missing-target join uses.

// IssueRouteVerdict is the outcome of routing one bare cross-prefix issue id.
type IssueRouteVerdict int

const (
	// IssueRouteFound: the owning rig holds the issue, and Status is its
	// current status.
	IssueRouteFound IssueRouteVerdict = iota
	// IssueRouteAbsent: the owning store answered and holds no such issue.
	// The blocker is satisfied, exactly as a local missing-target join treats
	// a dangling row.
	IssueRouteAbsent
	// IssueRouteUnrouted: no route reaches the target's rig, or the mode is
	// one routing does not support (a proxied-server workspace a direct store
	// cannot open). Compatibility: not a blocker and not a warning either.
	IssueRouteUnrouted
	// IssueRouteUnavailable: a CONFIGURED route exists but could not answer —
	// its read failed, its admission refused, its workspace has no database,
	// its store would not open, or the opened store's read failed. Fail
	// closed: the sources stay excluded, with one warning per prefix.
	IssueRouteUnavailable
)

// RoutedIssue is one routed id's answer: the verdict, and the found issue's
// status when the verdict is IssueRouteFound.
type RoutedIssue struct {
	Verdict IssueRouteVerdict
	Status  types.Status
}

// IssueRouter resolves bare cross-prefix dependency targets for the blocking
// policy: for each id in ids, which rig owns it and — when one does — the
// issue's status. Ids arrive full (the stored depends_on_external values, no
// partial-id guessing), deduplicated, and never in an unbounded batch.
//
// An error fails the whole evaluation (the caller propagates it); a target
// the router could not answer for is an IssueRouteUnavailable verdict, not an
// error, because one unreachable rig must not mute the policy for targets
// whose rigs answered.
//
// The router may open foreign stores; it must open each destination once per
// evaluation, close what it opened, and honor ctx cancellation.
type IssueRouter interface {
	ResolveIssueStatuses(ctx context.Context, ids []string) (map[string]RoutedIssue, error)
}

// Option configures a policy Store at construction (New).
type Option func(*Store)

// WithIssueRouter gives the policy an IssueRouter for bare cross-prefix
// targets. A Store without one keeps its legacy behavior for those targets;
// cmd/bd passes one for its direct CLI policy instances only — never for the
// serve chains or the proxied unit-of-work provider (see wireExternalDependencyPolicy).
func WithIssueRouter(router IssueRouter) Option {
	return func(s *Store) { s.routeIssues = router }
}

// dependencyIsExternalCapabilityRef reports whether dep is one of the
// explicit external: capability references the policy has always guarded:
// blocking-typed, and external:. This is the single classification every
// collector in this package uses, so they cannot drift about what counts.
func dependencyIsExternalCapabilityRef(dep *types.Dependency) bool {
	return dep.Type.IsBlockingEdge() && isExternalReference(dep.DependsOnID)
}

// dependencyIsCrossPrefixRef reports whether dep is a bare cross-prefix issue
// target of a kind the policy guards: an id whose prefix names another
// repository (not an external: ref, whose colon prefix disqualifies it),
// attached with a blocks or conditional-blocks edge. The source's own prefix
// decides: same store, multi-prefix databases included — an id whose prefix
// equals the source's is a local id, wherever the row physically sits.
//
// The edge types deliberately mirror types.IsGateEdge at the query layer
// (GetExternalBlockingDependencyRecordsInTx selects blocks and
// conditional-blocks for the cross-prefix leg): the Go filter and the SQL leg
// must agree on which bare ids reach the policy at all. Other bare cross-prefix
// edge types — waits-for, relates-to — do not make their target a blocker
// (GH#4647 AC-6): only the two types whose LOCAL semantics are hard blockers.
func dependencyIsCrossPrefixRef(sourceID string, dep *types.Dependency) bool {
	if isExternalReference(dep.DependsOnID) || !types.IsGateEdge(dep.Type) {
		return false
	}
	sourcePrefix := types.ExtractPrefix(sourceID)
	targetPrefix := types.ExtractPrefix(dep.DependsOnID)
	return sourcePrefix != "" && targetPrefix != "" && sourcePrefix != targetPrefix
}

// policyBlockersFor classifies the records in depsByIssue by the two target
// kinds the policy guards — external: capability refs and bare cross-prefix
// issue ids — resolves BOTH kinds in one batch each (every named routing
// destination opens once per evaluation, however many issues name it), and
// returns, per source id, the targets that remain unsatisfied. A source whose
// every target is satisfied is absent from the result.
//
// Every policy collector shares this one classification — the workspace-wide
// blocking state (loadBlockingState), the per-issue IsBlocked, the batch
// IsBlockedWithBlockers and IsBlockedBatch views — so the four cannot drift
// about what a bare ref is or when one is satisfied.
func (s *Store) policyBlockersFor(ctx context.Context, depsByIssue map[string][]*types.Dependency) (map[string][]string, error) {
	refsByIssue := make(map[string][]string, len(depsByIssue))
	var capabilityRefs []reference
	var crossPrefixIDs []string
	seenCross := make(map[string]struct{})
	for sourceID, deps := range depsByIssue {
		for _, dep := range deps {
			if dep == nil {
				continue
			}
			switch {
			case dependencyIsExternalCapabilityRef(dep):
				capabilityRefs = append(capabilityRefs, parseReference(dep.DependsOnID))
				refsByIssue[sourceID] = appendUnique(refsByIssue[sourceID], dep.DependsOnID)
			case dependencyIsCrossPrefixRef(sourceID, dep):
				if _, seen := seenCross[dep.DependsOnID]; !seen {
					seenCross[dep.DependsOnID] = struct{}{}
					crossPrefixIDs = append(crossPrefixIDs, dep.DependsOnID)
				}
				refsByIssue[sourceID] = appendUnique(refsByIssue[sourceID], dep.DependsOnID)
			}
		}
	}
	if len(refsByIssue) == 0 {
		return refsByIssue, nil
	}

	// One resolution pass per KIND, batched across every source: the
	// capability refs open each foreign project once (resolveReferences), and
	// the cross-prefix ids route once per destination in one router call.
	satisfiedRefs, err := s.resolveReferences(ctx, capabilityRefs)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: resolve blockers: %w", err)
	}
	satisfiedCross, err := s.resolveCrossPrefixIssueIDs(ctx, crossPrefixIDs)
	if err != nil {
		return nil, err
	}

	// Unsatisfied targets are named blockers; a source with none is dropped.
	for sourceID, targets := range refsByIssue {
		unsatisfied := targets[:0]
		for _, target := range targets {
			satisfied := satisfiedRefs[target]
			if !isExternalReference(target) {
				satisfied = satisfiedCross[target]
			}
			if !satisfied {
				unsatisfied = append(unsatisfied, target)
			}
		}
		if len(unsatisfied) == 0 {
			delete(refsByIssue, sourceID)
			continue
		}
		refsByIssue[sourceID] = unsatisfied
	}
	return refsByIssue, nil
}

// resolveCrossPrefixIssueIDs answers id -> satisfied for the bare
// cross-prefix targets the policy collected. Resolution has two legs:
//
//   - The inner store first, by exact id in one batch: a multi-prefix
//     database (a shared server pinned to one database that several prefixes
//     commit to) holds rows for prefixes that are not its own, and those
//     answer without any route at all. A read failure here is an error — the
//     policy cannot verify its own workspace's rows.
//
//   - Whatever the inner store missed goes to the IssueRouter once. Its
//     verdicts decide: found-and-closed/pinned satisfies the blocker; any
//     other found status leaves it standing; and absent and unrouted results
//     are the compatibility non-blockers, while unavailable (a configured
//     route's failure) keeps the blocker standing (AC-8).
//
// A policy instance with no router (a serve chain, the proxied unit-of-work
// provider) treats every id it cannot resolve locally like an unrouted one —
// the compatibility verdict, non-blocking: such an instance can never
// distinguish "no route" from "a route I may not open", and compatibility
// with the pre-#4647 behavior of those chains demands the source not be
// stranded on an edge the instance cannot verify. The fail-closed case needs
// a configured route to have FAILED, which only a router can observe.
func (s *Store) resolveCrossPrefixIssueIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	satisfied := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return satisfied, nil
	}

	found, err := s.inner.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: read cross-prefix blockers from the local store: %w", err)
	}
	byID := make(map[string]types.Status, len(found))
	for _, issue := range found {
		if issue != nil {
			byID[issue.ID] = issue.Status
		}
	}
	remaining := make([]string, 0, len(ids))
	for _, id := range ids {
		if status, ok := byID[id]; ok {
			satisfied[id] = issueStatusSatisfiesBlocker(status)
		} else {
			remaining = append(remaining, id)
		}
	}
	if len(remaining) == 0 {
		return satisfied, nil
	}
	if s.routeIssues == nil {
		for _, id := range remaining {
			satisfied[id] = true
		}
		return satisfied, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	routed, err := s.routeIssues.ResolveIssueStatuses(ctx, remaining)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: route cross-prefix blockers: %w", err)
	}
	for _, id := range remaining {
		// A router answer that names no entry for an id is treated as
		// unrouted — the benign verdict — rather than as a silent blocker:
		// the unhappy paths all HAVE an explicit verdict, so a missing entry
		// is a router bug or an id the router never saw, neither of which
		// should strand work.
		satisfied[id] = routedIssueSatisfiesBlocker(routed[id])
	}
	return satisfied, nil
}

// routedIssueSatisfiesBlocker is the verdict ladder for one routed target.
// The compatibility verdicts — absent (the owning store answered and holds no
// such row, exactly a local missing-target join's dangling row) and unrouted
// (no route reaches the target, or the mode is one routing does not support,
// a proxied-server workspace a direct store cannot open) — satisfy the
// blocker: they neither exclude the source nor warn. Only IssueRouteFound
// with an open status, and IssueRouteUnavailable (a CONFIGURED route's
// failure, AC-8), leave the blocker standing.
func routedIssueSatisfiesBlocker(routed RoutedIssue) bool {
	switch routed.Verdict {
	case IssueRouteFound:
		return issueStatusSatisfiesBlocker(routed.Status)
	case IssueRouteAbsent, IssueRouteUnrouted:
		return true
	case IssueRouteUnavailable:
		return false
	default:
		return true
	}
}

// issueStatusSatisfiesBlocker reports whether an issue's status satisfies a
// blocking edge onto it: closed and pinned do — the same two statuses the
// local is_blocked recompute (blocked_consistency.go) treats as unable to
// block — and any other status leaves the blocker standing.
func issueStatusSatisfiesBlocker(status types.Status) bool {
	return status == types.StatusClosed || status == types.StatusPinned
}
