package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// runSupersedeWithAck supersedes oldID with newIssue and, in the same
// transaction, creates an acknowledgement issue that blocks every open issue
// depending on oldID. The blocked set is defined only by existing edges to
// oldID: the command never decides which work a change affects.
func runSupersedeWithAck(ctx context.Context, store storage.DoltStorage, actor, oldID string, newIssue *types.Issue) error {
	oldIssue, err := store.GetIssue(ctx, oldID)
	if err != nil || oldIssue == nil {
		return fmt.Errorf("superseded issue not found: %s", oldID)
	}

	dependents, err := store.GetDependentsWithMetadata(ctx, oldID)
	if err != nil {
		return fmt.Errorf("failed to read dependents of %s: %w", oldID, err)
	}
	blocked := openSupersedeDependents(dependents, newIssue.ID)

	var ackID string
	commitMsg := fmt.Sprintf("bd: supersede %s with %s (ack)", oldID, newIssue.ID)
	err = transact(ctx, store, commitMsg, func(tx storage.Transaction) error {
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID:     oldID,
			DependsOnID: newIssue.ID,
			Type:        types.DepSupersedes,
		}, actor); err != nil {
			return fmt.Errorf("failed to add supersede link: %w", err)
		}
		if err := tx.CloseIssue(ctx, oldID, "", actor, ""); err != nil {
			return fmt.Errorf("failed to close superseded issue: %w", err)
		}
		if len(blocked) == 0 {
			return nil
		}

		ack := &types.Issue{
			Title:       fmt.Sprintf("Acknowledge: %s superseded by %s", oldID, newIssue.ID),
			Description: supersedeAckDescription(oldIssue, newIssue, blocked),
			Status:      types.StatusOpen,
			Priority:    oldIssue.Priority,
			IssueType:   types.TypeTask,
		}
		if err := tx.CreateIssue(ctx, ack, actor); err != nil {
			return fmt.Errorf("failed to create acknowledgement issue: %w", err)
		}
		ackID = ack.ID
		for _, id := range blocked {
			if err := tx.AddDependency(ctx, &types.Dependency{
				IssueID:     id,
				DependsOnID: ackID,
				Type:        types.DepBlocks,
			}, actor); err != nil {
				return fmt.Errorf("failed to block %s on %s: %w", id, ackID, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	commandDidWrite.Store(true)

	if isJSONOutput() {
		out := map[string]interface{}{
			"superseded":  oldID,
			"replacement": newIssue.ID,
			"status":      "closed",
			"blocked":     blocked,
		}
		if ackID != "" {
			out["acknowledgement"] = ackID
		}
		return outputJSON(out)
	}

	fmt.Printf("%s Marked %s as superseded by %s (closed)\n", ui.RenderPass("✓"), oldID, newIssue.ID)
	if ackID == "" {
		fmt.Printf("No open issues depend on %s; no acknowledgement issue created\n", oldID)
		return nil
	}
	fmt.Printf("%s Created %s; it blocks %d open dependent(s) until closed: %s\n",
		ui.RenderPass("✓"), ackID, len(blocked), strings.Join(blocked, ", "))
	return nil
}

// openSupersedeDependents returns the IDs of dependents that are not closed or pinned,
// excluding the replacement issue, without duplicates and in input order.
func openSupersedeDependents(dependents []*types.IssueWithDependencyMetadata, replacementID string) []string {
	seen := make(map[string]bool, len(dependents))
	ids := make([]string, 0, len(dependents))
	for _, d := range dependents {
		if d == nil {
			continue
		}
		id := d.ID
		if id == replacementID || seen[id] || d.Status == types.StatusClosed || d.Status == types.StatusPinned {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func supersedeAckDescription(oldIssue, newIssue *types.Issue, blocked []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s was superseded by %s. Read the replacement, then close this issue to unblock: %s.\n\n",
		oldIssue.ID, newIssue.ID, strings.Join(blocked, ", "))
	fmt.Fprintf(&b, "## Before: %s %s\n\n%s\n\n", oldIssue.ID, oldIssue.Title, oldIssue.Description)
	fmt.Fprintf(&b, "## After: %s %s\n\n%s\n", newIssue.ID, newIssue.Title, newIssue.Description)
	return b.String()
}
