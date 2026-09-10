package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// proxiedRequireIssue mirrors the direct route's pre-flight for the verbs that
// refuse a ghost endpoint with the classic "issue not found" wording: it reads
// the durable issue row through the unit of work and normalizes both not-found
// shapes (a sentinel error or a nil row) into one nil-issue answer the caller
// turns into the classic message. Exact canonical ids only — proxied mode does
// no partial-id resolution, matching every other proxied verb.
func proxiedRequireIssue(ctx context.Context, uw uow.UnitOfWork, id string) (*types.Issue, error) {
	issue, err := uw.IssueUseCase().GetIssue(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get issue %s: %w", id, err)
	}
	return issue, nil
}

// runRelateProxiedServer is the proxied-server twin of runRelate: both
// directed relates-to edges land in ONE unit of work under one commit message,
// where the direct route's two AddDependencyWithOptions calls ride the shared
// store transaction. The bidirectional pair cannot trip the cycle gates —
// relates-to is not a scheduling type, so both the per-edge probe and the
// whole-graph gate skip it by design.
func runRelateProxiedServer(ctx context.Context, args []string) error {
	id1 := args[0]
	id2 := args[1]

	if id1 == id2 {
		return HandleErrorRespectJSON("cannot relate an issue to itself")
	}
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		// The direct route refuses a missing endpoint before writing anything;
		// keep its exact wording rather than the bulk path's anonymous
		// ghost-source refusal (bd-yby99.9).
		for _, id := range []string{id1, id2} {
			issue, err := proxiedRequireIssue(ctx, uw, id)
			if err != nil {
				return "", err
			}
			if issue == nil {
				return "", fmt.Errorf("issue not found: %s", id)
			}
		}
		deps := []*types.Dependency{
			{IssueID: id1, DependsOnID: id2, Type: types.DepRelatesTo},
			{IssueID: id2, DependsOnID: id1, Type: types.DepRelatesTo},
		}
		// bd relate is an explicit dependency verb, so the bulk path's
		// EmitEvent trail matches the direct route's history behavior.
		if _, err := uw.DependencyUseCase().AddDependencies(ctx, deps, actor, domain.BulkAddDepsOpts{}); err != nil {
			return "", err
		}
		return fmt.Sprintf("bd: relate %s %s", id1, id2), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"id1":     id1,
			"id2":     id2,
			"related": true,
		})
	}

	fmt.Printf("%s Linked %s ↔ %s\n", ui.RenderPass("✓"), id1, id2)
	return nil
}

// runUnrelateProxiedServer is the proxied-server twin of runUnrelate: both
// directed removals ride ONE unit of work. Removal is idempotent on each
// direction (a missing edge is a success, like the direct route), and a pair
// that removed nothing returns an empty commit message — the deliberate
// no-commit no-op, the same answer the DependencyEditor role gives a removal
// that found no edge.
func runUnrelateProxiedServer(ctx context.Context, args []string) error {
	id1 := args[0]
	id2 := args[1]

	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		for _, id := range []string{id1, id2} {
			issue, err := proxiedRequireIssue(ctx, uw, id)
			if err != nil {
				return "", err
			}
			if issue == nil {
				return "", fmt.Errorf("issue not found: %s", id)
			}
		}
		removed1, err := uw.DependencyUseCase().RemoveDependencyBySource(ctx, id1, id2, actor)
		if err != nil {
			return "", fmt.Errorf("failed to remove relates-to %s -> %s: %w", id1, id2, err)
		}
		removed2, err := uw.DependencyUseCase().RemoveDependencyBySource(ctx, id2, id1, actor)
		if err != nil {
			return "", fmt.Errorf("failed to remove relates-to %s -> %s: %w", id2, id1, err)
		}
		if !removed1 && !removed2 {
			return "", nil
		}
		return fmt.Sprintf("bd: unrelate %s %s", id1, id2), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"id1":       id1,
			"id2":       id2,
			"unrelated": true,
		})
	}

	fmt.Printf("%s Unlinked %s ↔ %s\n", ui.RenderPass("✓"), id1, id2)
	return nil
}
