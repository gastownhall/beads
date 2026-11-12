package externaldeps

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/types"
)

// ForeignIssueResolver resolves bare cross-prefix issue IDs (GH#5784) into the
// row that owns them in the rig that owns the prefix. The storage-layer
// decorator cannot reach another rig's database itself: it holds one open
// store, and prefix routing (routes.jsonl, longest-prefix match, a
// per-open-database target) is command-layer work. cmd/bd injects the resolver
// through NewWithForeignResolver; a Store built without one treats every bare
// cross-prefix target as unresolved, and its blocking edges fail closed.
//
// ResolveForeignIssues is called once per policy evaluation with every bare
// target that evaluation saw, so the implementation can open each foreign
// database at most once per pass, however many sources name it.
type ForeignIssueResolver interface {
	ResolveForeignIssues(ctx context.Context, ids []string) (map[string]*ForeignIssueResult, error)
}

// ForeignIssueResult is one target's answer from a ForeignIssueResolver.
//
// The error return of ResolveForeignIssues is reserved for the operation
// itself failing (cancellation above all); a target this rig cannot answer —
// no route, unreadable database, no such row — travels in-band as a nil Issue
// with the reason in Reason, so one unreachable rig does not take the whole
// ready read down with it.
type ForeignIssueResult struct {
	// Issue is the foreign row, nil when the target could not be answered.
	Issue *types.Issue
	// Reason is non-empty when Issue is nil: why the target was unresolved.
	Reason string
}

// defaultCrossRigWarning is the default warning for an unresolved bare target:
// like a foreign project that cannot be opened (defaultProjectWarning), the
// edge fails closed, never silently. The second line names the remedy the
// design requires alongside the diagnosis: fix the route, or remove the edge
// (GH#5784 §200) — the target stays blocking until one of the two happens.
func defaultCrossRigWarning(target, reason string) {
	fmt.Fprintf(os.Stderr, "Warning: cross-rig dependency target %q is unresolved (%s); it remains blocking\n  Fix its route in .beads/routes.jsonl, or remove the edge with `bd dep remove <issue-id> %s`\n", target, reason, target)
}

// bareCrossRigBlockingEdge reports whether dep is a bare cross-prefix edge
// that gates readiness (GH#5784): a target stored in depends_on_external
// because it names an ID whose prefix belongs to another rig. Only blocks and
// conditional-blocks gate; a waits-for edge to a foreign issue does not gate
// readiness, exactly as a local one does not (the ready predicate reads the
// is_blocked flag, which only ready-affecting types set).
func bareCrossRigBlockingEdge(dep *types.Dependency) bool {
	return dep != nil &&
		dep.DependsOnID != "" &&
		!isExternalReference(dep.DependsOnID) &&
		types.ExtractPrefix(dep.DependsOnID) != types.ExtractPrefix(dep.IssueID) &&
		(dep.Type == types.DepBlocks || dep.Type == types.DepConditionalBlocks)
}

// blockingStateOpts carries the reads blockingStateFromRecords needs beyond
// the dependency records it is handed.
type blockingStateOpts struct {
	// localRead fetches local issue rows by id. The decorator passes its
	// inner store's GetIssuesByIDs; the remote roles their BatchGetter-backed
	// read; the UOW wrapper its chunked GetMany. A nil localRead answers no
	// target locally.
	localRead func(context.Context, []string) ([]*types.Issue, error)
}

// bareTargetSatisfaction answers, for every bare cross-prefix blocking target,
// whether a settled row for it is known: closed or pinned locally, or closed
// or pinned in its own rig through the resolver. Every target starts
// unsatisfied; an unresolved target — no resolver wired, no route, unreadable
// foreign database, no such row — stays unsatisfied and fails closed, warned
// once per evaluation (bareTargets holds each target once). The pinned state
// satisfies exactly as it does for a local dependency target (blocked.go: a
// withheld target gates nothing its rig is not offering anyway).
//
// A resolver error is the operation failing, not a per-target answer: context
// cancellation propagates, anything else is treated like a foreign project
// that cannot be opened (resolveReferences) — warn and fail closed.
func (s *Store) bareTargetSatisfaction(ctx context.Context, targets []string, localRead func(context.Context, []string) ([]*types.Issue, error)) (map[string]bool, error) {
	satisfied := make(map[string]bool, len(targets))
	for _, target := range targets {
		satisfied[target] = false
	}
	if len(targets) == 0 {
		return satisfied, nil
	}

	// Local rows first: this mirrors the raw ready predicate's satisfaction
	// probe (sqlbuild), so a target that lives in this store is settled the
	// same way on both paths, without a foreign read.
	if localRead != nil {
		issues, err := localRead(ctx, targets)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			if issue == nil {
				continue
			}
			if issue.Status == types.StatusClosed || issue.Status == types.StatusPinned {
				satisfied[issue.ID] = true
			}
		}
	}

	remaining := make([]string, 0, len(targets))
	for _, target := range targets {
		if !satisfied[target] {
			remaining = append(remaining, target)
		}
	}
	if len(remaining) == 0 {
		return satisfied, nil
	}

	if s.resolveForeign == nil {
		for _, target := range remaining {
			s.warnCrossRigTarget(target, "no cross-rig resolver is configured")
		}
		return satisfied, nil
	}
	resolved, err := s.resolveForeign.ResolveForeignIssues(ctx, remaining)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		for _, target := range remaining {
			s.warnCrossRigTarget(target, fmt.Sprintf("resolver failed: %v", err))
		}
		return satisfied, nil
	}
	for _, target := range remaining {
		result := resolved[target]
		if result == nil {
			result = &ForeignIssueResult{Reason: "the owning rig did not answer for it"}
		}
		if result.Issue == nil {
			s.warnCrossRigTarget(target, crossRigReason(result))
			continue
		}
		if result.Issue.Status == types.StatusClosed || result.Issue.Status == types.StatusPinned {
			satisfied[target] = true
		}
	}
	return satisfied, nil
}

// crossRigReason renders a resolver's in-band refusal for a warning.
func crossRigReason(result *ForeignIssueResult) string {
	if result.Reason == "" {
		return "unresolved in its rig"
	}
	return result.Reason
}
