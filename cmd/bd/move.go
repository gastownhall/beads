package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var moveCmd = &cobra.Command{
	Use:     "move <issue-id> --to <rig|prefix>",
	GroupID: "issues",
	Short:   "Move an issue to a different rig with dependency remapping",
	Long: `Move an issue from one rig to another, updating all dependencies.

This command:
1. Creates a new issue in the target rig with the same content
2. Remaps all dependencies that reference the old ID
3. Closes the source issue with a redirect note

The target rig can be specified as:
  - A rig name: beads, gastown
  - A prefix: bd-, gt-
  - A prefix without hyphen: bd, gt

Dependencies are remapped in both directions:
  - Issues that depend ON the moved issue are updated
  - Issues that the moved issue DEPENDS ON are updated

Examples:
  bd move hq-c21fj --to beads     # Move to beads by rig name
  bd move hq-q3tki --to gt-       # Move to gastown by prefix
  bd move hq-1h2to --to gt        # Move to gastown (prefix without hyphen)`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("move")

		sourceID := args[0]
		targetRig, _ := cmd.Flags().GetString("to")
		if targetRig == "" {
			FatalError("--to flag is required. Specify target rig (e.g., --to beads, --to gt-)")
		}

		keepOpen, _ := cmd.Flags().GetBool("keep-open")
		skipDeps, _ := cmd.Flags().GetBool("skip-deps")

		ctx := rootCtx

		// Step 1: Get the source issue (via routing if needed)
		result, err := resolveAndGetIssueWithRouting(ctx, store, sourceID)
		if err != nil {
			FatalError("failed to find source issue: %v", err)
		}
		if result == nil || result.Issue == nil {
			FatalError("source issue %s not found", sourceID)
		}
		defer result.Close()

		sourceIssue := result.Issue
		resolvedSourceID := result.ResolvedID
		sourceStore := result.Store

		// Warn if source issue is already closed
		if sourceIssue.Status == types.StatusClosed {
			fmt.Fprintf(os.Stderr, "%s Source issue %s is already closed\n", ui.RenderWarn("⚠"), resolvedSourceID)
		}

		// Warn if ephemeral
		if sourceIssue.Ephemeral {
			fmt.Fprintf(os.Stderr, "%s Source issue %s is ephemeral (wisp). Moving ephemeral issues may not be appropriate.\n", ui.RenderWarn("⚠"), resolvedSourceID)
		}

		// Step 2: Find the town-level beads directory
		townBeadsDir, err := findTownBeadsDir()
		if err != nil {
			FatalError("cannot move: %v", err)
		}

		// Step 3: Resolve the target rig's beads directory
		targetBeadsDir, targetPrefix, err := routing.ResolveBeadsDirForRig(targetRig, townBeadsDir)
		if err != nil {
			FatalError("%v", err)
		}

		// Check we're not moving to the same rig
		sourcePrefix := routing.ExtractPrefix(resolvedSourceID)
		if sourcePrefix == targetPrefix {
			FatalError("source issue %s is already in rig %q", resolvedSourceID, targetRig)
		}

		// Step 4: Open storage for the target rig
		targetDBPath := filepath.Join(targetBeadsDir, "beads.db")
		targetStore, err := sqlite.New(ctx, targetDBPath)
		if err != nil {
			FatalError("failed to open target rig database: %v", err)
		}
		defer func() {
			if err := targetStore.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to close target rig database: %v\n", err)
			}
		}()

		// Step 5: Create the new issue in target rig (copy all fields)
		newIssue := &types.Issue{
			// Don't copy ID - let target rig generate new one
			Title:              sourceIssue.Title,
			Description:        sourceIssue.Description,
			Design:             sourceIssue.Design,
			AcceptanceCriteria: sourceIssue.AcceptanceCriteria,
			Notes:              sourceIssue.Notes,
			Status:             types.StatusOpen, // Always start as open
			Priority:           sourceIssue.Priority,
			IssueType:          sourceIssue.IssueType,
			Assignee:           sourceIssue.Assignee,
			ExternalRef:        sourceIssue.ExternalRef,
			EstimatedMinutes:   sourceIssue.EstimatedMinutes,
			SourceRepo:         sourceIssue.SourceRepo,
			Ephemeral:          sourceIssue.Ephemeral,
			MolType:            sourceIssue.MolType,
			RoleType:           sourceIssue.RoleType,
			Rig:                sourceIssue.Rig,
			DueAt:              sourceIssue.DueAt,
			DeferUntil:         sourceIssue.DeferUntil,
			CreatedBy:          actor,
		}

		// Append moved note to description
		if newIssue.Description != "" {
			newIssue.Description += "\n\n"
		}
		newIssue.Description += fmt.Sprintf("(Moved from %s)", resolvedSourceID)

		if err := targetStore.CreateIssue(ctx, newIssue, actor); err != nil {
			FatalError("failed to create issue in target rig: %v", err)
		}

		newID := newIssue.ID

		// Step 6: Copy labels if any
		labels, err := sourceStore.GetLabels(ctx, resolvedSourceID)
		if err == nil && len(labels) > 0 {
			for _, label := range labels {
				if err := targetStore.AddLabel(ctx, newID, label, actor); err != nil {
					WarnError("failed to copy label %s: %v", label, err)
				}
			}
		}

		// Step 7: Remap dependencies in the source store
		// targetRig is used to create external references for cross-rig moves
		var depsRemapped int
		if !skipDeps {
			depsRemapped, err = remapDependencies(ctx, sourceStore, resolvedSourceID, newID, targetRig, actor)
			if err != nil {
				WarnError("failed to remap some dependencies: %v", err)
			}
		}

		// Step 8: Close the source issue (unless --keep-open)
		if !keepOpen {
			closeReason := fmt.Sprintf("Moved to %s", newID)
			if err := sourceStore.CloseIssue(ctx, resolvedSourceID, closeReason, actor, ""); err != nil {
				WarnError("failed to close source issue: %v", err)
			}
			// Schedule auto-flush if source was local store
			if !result.Routed {
				markDirtyAndScheduleFlush()
			}
		}

		// Output
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"source":        resolvedSourceID,
				"target":        newID,
				"closed":        !keepOpen,
				"deps_remapped": depsRemapped,
			})
		} else {
			fmt.Printf("%s Moved %s → %s\n", ui.RenderPass("✓"), resolvedSourceID, newID)
			if depsRemapped > 0 {
				fmt.Printf("  Remapped %d dependencies\n", depsRemapped)
			}
			if !keepOpen {
				fmt.Printf("  Source issue closed\n")
			}
		}
	},
}

// remapDependencies updates all dependencies in the store that reference oldID to use newID.
// For cross-rig moves, dependencies TO the old ID are converted to external references.
// Dependencies FROM the old ID (where oldID is the dependent) are removed with a warning.
// Returns the number of dependencies remapped.
func remapDependencies(ctx context.Context, s storage.Storage, oldID, newID, targetRig, actor string) (int, error) {
	count := 0

	// Determine if this is a cross-rig move by comparing prefixes
	oldPrefix := routing.ExtractPrefix(oldID)
	newPrefix := routing.ExtractPrefix(newID)
	isCrossRig := oldPrefix != newPrefix

	// Get dependencies where oldID is the issue (oldID depends on something)
	depsFrom, err := s.GetDependencyRecords(ctx, oldID)
	if err != nil {
		return count, fmt.Errorf("getting dependencies from %s: %w", oldID, err)
	}

	// Handle deps FROM the old ID (where oldID is the dependent)
	for _, dep := range depsFrom {
		// Remove old dependency
		if err := s.RemoveDependency(ctx, oldID, dep.DependsOnID, actor); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: failed to remove dep %s->%s: %v\n", oldID, dep.DependsOnID, err)
			continue
		}

		if isCrossRig {
			// For cross-rig moves, these deps can't be recreated in source store
			// User needs to recreate them in target rig
			continue
		}

		// Same-rig move: recreate with newID as dependent
		newDep := &types.Dependency{
			IssueID:     newID,
			DependsOnID: dep.DependsOnID,
			Type:        dep.Type,
			CreatedBy:   actor,
			Metadata:    dep.Metadata,
			ThreadID:    dep.ThreadID,
		}
		if err := s.AddDependency(ctx, newDep, actor); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: failed to add dep %s->%s: %v\n", newID, dep.DependsOnID, err)
			continue
		}
		count++
	}

	if len(depsFrom) > 0 && isCrossRig {
		fmt.Fprintf(os.Stderr, "  note: %d dependencies FROM %s were removed (recreate in target rig if needed)\n", len(depsFrom), oldID)
	}

	// Get dependents (issues that depend on oldID)
	dependents, err := s.GetDependents(ctx, oldID)
	if err != nil {
		return count, fmt.Errorf("getting dependents of %s: %w", oldID, err)
	}

	// For each issue that depends on oldID, update to depend on newID
	for _, dependent := range dependents {
		// Get the dependency record to preserve type/metadata
		depRecords, err := s.GetDependencyRecords(ctx, dependent.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: failed to get deps for %s: %v\n", dependent.ID, err)
			continue
		}

		for _, dep := range depRecords {
			if dep.DependsOnID != oldID {
				continue
			}

			// Remove old dependency
			if err := s.RemoveDependency(ctx, dependent.ID, oldID, actor); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: failed to remove dep %s->%s: %v\n", dependent.ID, oldID, err)
				continue
			}

			// Determine what to point to:
			// - Same rig: point directly to newID
			// - Cross-rig: point to external reference
			var targetID string
			if isCrossRig {
				targetID = fmt.Sprintf("external:%s:%s", targetRig, newID)
			} else {
				targetID = newID
			}

			// Add new dependency
			newDep := &types.Dependency{
				IssueID:     dependent.ID,
				DependsOnID: targetID,
				Type:        dep.Type,
				CreatedBy:   actor,
				Metadata:    dep.Metadata,
				ThreadID:    dep.ThreadID,
			}
			if err := s.AddDependency(ctx, newDep, actor); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: failed to add dep %s->%s: %v\n", dependent.ID, targetID, err)
				continue
			}
			count++
		}
	}

	return count, nil
}

func init() {
	moveCmd.Flags().String("to", "", "Target rig or prefix (required)")
	moveCmd.Flags().Bool("keep-open", false, "Keep the source issue open (don't close it)")
	moveCmd.Flags().Bool("skip-deps", false, "Skip dependency remapping")
	rootCmd.AddCommand(moveCmd)
}
