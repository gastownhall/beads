package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// createDepEdges describes the dependency edges attached to a new issue at
// create time: --parent, --deps, and --waits-for.
type createDepEdges struct {
	parentID string
	specs    []domain.DependencySpec
	waitsFor *domain.WaitsForSpec
}

func (e createDepEdges) empty() bool {
	return e.parentID == "" && len(e.specs) == 0 && e.waitsFor == nil
}

// createIssueWithDeps persists a new issue together with its create-time
// dependency edges in one transaction. The previous shape — commit the issue
// first, then add each edge in its own transaction with warn-only error
// handling — meant a failed dep-add still exited 0 and left a dep-less,
// permanently-ready bead behind for orchestrators to dispatch prematurely.
// Here any edge failure rolls back the create and is returned as a fatal
// error naming the failing edge.
//
// With no edges requested it delegates to store.CreateIssue so a bare create
// keeps its store-specific routing and commit behavior.
func createIssueWithDeps(ctx context.Context, st storage.DoltStorage, issue *types.Issue, actor string, edges createDepEdges) error {
	if edges.empty() {
		return st.CreateIssue(ctx, issue, actor)
	}

	// Store-level CreateIssue routes configured infra types to the wisps
	// tables; transaction-level CreateIssue routes on the Ephemeral/NoHistory
	// flags only, so resolve the routing up front (mirrors DoltStore and
	// sqlkit CreateIssue).
	if !issue.Ephemeral && !issue.NoHistory && st.IsInfraTypeCtx(ctx, issue.IssueType) {
		issue.Ephemeral = true
	}

	// Auto-minted IDs are only known after tx.CreateIssue runs, so the Dolt
	// commit message can name the issue only when the ID is already reserved
	// (explicit --id or a parent-child child ID).
	commitMsg := "bd: create"
	if issue.ID != "" {
		commitMsg = "bd: create " + issue.ID
	}

	return transactHonoringAutoCommit(ctx, st, commitMsg, func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, issue, actor); err != nil {
			return err
		}
		// issue.ID is only reserved after tx.CreateIssue for auto-minted IDs, so
		// the edge helpers run after the create.
		if err := addParentEdge(ctx, tx, issue.ID, edges.parentID, actor); err != nil {
			return err
		}
		if err := addDepSpecEdges(ctx, tx, issue.ID, edges.specs, actor); err != nil {
			return err
		}
		return addWaitsForEdge(ctx, tx, issue.ID, edges.waitsFor, actor)
	})
}

// addParentEdge adds the --parent parent-child edge, if requested.
func addParentEdge(ctx context.Context, tx storage.Transaction, issueID, parentID, actor string) error {
	if parentID == "" {
		return nil
	}
	dep := &types.Dependency{
		IssueID:     issueID,
		DependsOnID: parentID,
		Type:        types.DepParentChild,
	}
	if err := tx.AddDependency(ctx, dep, actor); err != nil {
		return fmt.Errorf("adding parent-child dependency %s -> %s: %w", issueID, parentID, err)
	}
	return nil
}

// addDepSpecEdges adds the --deps edges, honoring each spec's SwapDirection.
func addDepSpecEdges(ctx context.Context, tx storage.Transaction, issueID string, specs []domain.DependencySpec, actor string) error {
	for _, spec := range specs {
		dep := &types.Dependency{
			IssueID:     issueID,
			DependsOnID: spec.TargetID,
			Type:        spec.Type,
			Metadata:    spec.Metadata,
		}
		if spec.SwapDirection {
			dep.IssueID, dep.DependsOnID = dep.DependsOnID, dep.IssueID
		}
		if err := tx.AddDependency(ctx, dep, actor); err != nil {
			return fmt.Errorf("adding dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
		}
	}
	return nil
}

// addWaitsForEdge adds the --waits-for spawner-gate edge, if requested.
func addWaitsForEdge(ctx context.Context, tx storage.Transaction, issueID string, waitsFor *domain.WaitsForSpec, actor string) error {
	if waitsFor == nil {
		return nil
	}
	metaJSON, err := json.Marshal(types.WaitsForMeta{Gate: waitsFor.Gate})
	if err != nil {
		return fmt.Errorf("serializing waits-for metadata: %w", err)
	}
	dep := &types.Dependency{
		IssueID:     issueID,
		DependsOnID: waitsFor.SpawnerID,
		Type:        types.DepWaitsFor,
		Metadata:    string(metaJSON),
	}
	if err := tx.AddDependency(ctx, dep, actor); err != nil {
		return fmt.Errorf("adding waits-for dependency %s -> %s: %w", issueID, waitsFor.SpawnerID, err)
	}
	return nil
}
