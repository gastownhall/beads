package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molBurnCmd = &cobra.Command{
	Use:   "burn <molecule-id> [molecule-id...]",
	Short: "Delete a molecule without creating a digest",
	Long: `Burn a molecule, deleting it without creating a digest.

Unlike squash (which creates a permanent digest before deletion), burn
completely removes the molecule with no trace. Use this for:
  - Abandoned patrol cycles
  - Crashed or failed workflows
  - Test/debug molecules you don't want to preserve

The burn operation differs based on molecule phase:
  - Wisp (ephemeral): Direct delete
  - Mol (persistent): Cascade delete (syncs to remotes)

CAUTION: This is a destructive operation. The molecule's data will be
permanently lost. If you want to preserve a summary, use 'bd mol squash'.

Example:
  bd mol burn bd-abc123              # Delete molecule with no trace
  bd mol burn bd-abc123 --dry-run    # Preview what would be deleted
  bd mol burn bd-abc123 --force      # Skip confirmation
  bd mol burn bd-a1 bd-b2 bd-c3      # Batch delete multiple wisps`,
	Args: cobra.MinimumNArgs(1),
	Run:  runMolBurn,
}

// BurnResult holds the result of a burn operation
type BurnResult struct {
	MoleculeID   string   `json:"molecule_id"`
	DeletedIDs   []string `json:"deleted_ids"`
	DeletedCount int      `json:"deleted_count"`
}

// BatchBurnResult holds aggregated results when burning multiple molecules
type BatchBurnResult struct {
	Results      []BurnResult `json:"results"`
	TotalDeleted int          `json:"total_deleted"`
	FailedCount  int          `json:"failed_count"`
}

func runMolBurn(cmd *cobra.Command, args []string) {
	CheckReadonly("mol burn")

	ctx := rootCtx

	// mol burn requires direct store access (daemon auto-bypassed for wisp ops)
	if store == nil {
		fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		fmt.Fprintf(os.Stderr, "Hint: run 'bd init' or 'bd import' to initialize the database\n")
		os.Exit(1)
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")

	// Single ID: use original logic for backward compatibility
	if len(args) == 1 {
		burnSingleMolecule(ctx, args[0], dryRun, force)
		return
	}

	// Multiple IDs: batch mode for efficiency
	burnMultipleMolecules(ctx, args, dryRun, force)
}

// burnSingleMolecule handles the single molecule case (original behavior)
func burnSingleMolecule(ctx context.Context, moleculeID string, dryRun, force bool) {
	// Resolve molecule ID in main store
	resolvedID, err := utils.ResolvePartialID(ctx, store, moleculeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving molecule ID %s: %v\n", moleculeID, err)
		os.Exit(1)
	}

	// Load the molecule
	rootIssue, err := store.GetIssue(ctx, resolvedID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
		os.Exit(1)
	}

	// Branch based on molecule phase
	if rootIssue.Ephemeral {
		// Wisp: direct delete
		burnWispMolecule(ctx, resolvedID, dryRun, force)
	} else {
		// Mol: cascade delete
		burnPersistentMolecule(ctx, resolvedID, dryRun, force)
	}
}

// burnMultipleMolecules handles batch deletion of multiple molecules efficiently
func burnMultipleMolecules(ctx context.Context, moleculeIDs []string, dryRun, force bool) {
	var wispIDs []string
	var persistentIDs []string
	var failedResolve []string

	// First pass: resolve and categorize all IDs
	for _, moleculeID := range moleculeIDs {
		resolvedID, err := utils.ResolvePartialID(ctx, store, moleculeID)
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: failed to resolve %s: %v\n", moleculeID, err)
			}
			failedResolve = append(failedResolve, moleculeID)
			continue
		}

		issue, err := store.GetIssue(ctx, resolvedID)
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: failed to load %s: %v\n", resolvedID, err)
			}
			failedResolve = append(failedResolve, moleculeID)
			continue
		}

		if issue.Ephemeral {
			wispIDs = append(wispIDs, resolvedID)
		} else {
			persistentIDs = append(persistentIDs, resolvedID)
		}
	}

	if len(wispIDs) == 0 && len(persistentIDs) == 0 {
		if jsonOutput {
			outputJSON(BatchBurnResult{FailedCount: len(failedResolve)})
		} else {
			fmt.Println("No valid molecules to burn")
		}
		return
	}

	if dryRun {
		if !jsonOutput {
			fmt.Printf("\nDry run: would burn %d wisp(s) and %d persistent molecule(s)\n", len(wispIDs), len(persistentIDs))
			if len(wispIDs) > 0 {
				fmt.Printf("\nWisps to delete:\n")
				for _, id := range wispIDs {
					fmt.Printf("  - %s\n", id)
				}
			}
			if len(persistentIDs) > 0 {
				fmt.Printf("\nPersistent molecules to delete:\n")
				for _, id := range persistentIDs {
					fmt.Printf("  - %s\n", id)
				}
			}
			if len(failedResolve) > 0 {
				fmt.Printf("\nFailed to resolve (%d):\n", len(failedResolve))
				for _, id := range failedResolve {
					fmt.Printf("  - %s\n", id)
				}
			}
		}
		return
	}

	// Confirm unless --force
	if !force && !jsonOutput {
		fmt.Printf("About to burn %d wisp(s) and %d persistent molecule(s)\n", len(wispIDs), len(persistentIDs))
		fmt.Printf("This will permanently delete all molecule data with no digest.\n")
		fmt.Printf("\nContinue? [y/N] ")

		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Canceled.")
			return
		}
	}

	batchResult := BatchBurnResult{
		Results:     make([]BurnResult, 0),
		FailedCount: len(failedResolve),
	}

	// Batch delete all wisps in one call
	if len(wispIDs) > 0 {
		result, err := burnWisps(ctx, store, wispIDs)
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Error burning wisps: %v\n", err)
			}
		} else {
			batchResult.TotalDeleted += result.DeletedCount
			batchResult.Results = append(batchResult.Results, *result)
		}
	}

	// Handle persistent molecules individually (they need subgraph loading)
	for _, id := range persistentIDs {
		subgraph, err := loadTemplateSubgraph(ctx, store, id)
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: failed to load subgraph for %s: %v\n", id, err)
			}
			batchResult.FailedCount++
			continue
		}

		var issueIDs []string
		for _, issue := range subgraph.Issues {
			issueIDs = append(issueIDs, issue.ID)
		}

		// Use deleteBatch for persistent molecules
		deleteBatch(nil, issueIDs, true, false, false, false, false, "mol burn")
		batchResult.TotalDeleted += len(issueIDs)
		batchResult.Results = append(batchResult.Results, BurnResult{
			MoleculeID:   id,
			DeletedIDs:   issueIDs,
			DeletedCount: len(issueIDs),
		})
	}

	if jsonOutput {
		outputJSON(batchResult)
		return
	}

	fmt.Printf("%s Burned %d molecule(s): %d issues deleted\n", ui.RenderPass("✓"), len(wispIDs)+len(persistentIDs), batchResult.TotalDeleted)
	if batchResult.FailedCount > 0 {
		fmt.Printf("  %d failed\n", batchResult.FailedCount)
	}
}

// burnWispMolecule handles wisp deletion (ephemeral-only)
func burnWispMolecule(ctx context.Context, resolvedID string, dryRun, force bool) {
	// Load the molecule subgraph
	subgraph, err := loadTemplateSubgraph(ctx, store, resolvedID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading wisp molecule: %v\n", err)
		os.Exit(1)
	}

	// Collect wisp issue IDs to delete (only delete wisps, not regular children)
	var wispIDs []string
	for _, issue := range subgraph.Issues {
		if issue.Ephemeral {
			wispIDs = append(wispIDs, issue.ID)
		}
	}

	if len(wispIDs) == 0 {
		if jsonOutput {
			outputJSON(BurnResult{
				MoleculeID:   resolvedID,
				DeletedCount: 0,
			})
		} else {
			fmt.Printf("No wisp issues found for molecule %s\n", resolvedID)
		}
		return
	}

	if dryRun {
		fmt.Printf("\nDry run: would burn wisp %s\n\n", resolvedID)
		fmt.Printf("Root: %s\n", subgraph.Root.Title)
		fmt.Printf("\nWisp issues to delete (%d total):\n", len(wispIDs))
		for _, issue := range subgraph.Issues {
			if !issue.Ephemeral {
				continue
			}
			status := string(issue.Status)
			if issue.ID == subgraph.Root.ID {
				fmt.Printf("  - [%s] %s (%s) [ROOT]\n", status, issue.Title, issue.ID)
			} else {
				fmt.Printf("  - [%s] %s (%s)\n", status, issue.Title, issue.ID)
			}
		}
		fmt.Printf("\nNo digest will be created (use 'bd mol squash' to create one).\n")
		return
	}

	// Confirm unless --force
	if !force && !jsonOutput {
		fmt.Printf("About to burn wisp %s (%d issues)\n", resolvedID, len(wispIDs))
		fmt.Printf("This will permanently delete all wisp data with no digest.\n")
		fmt.Printf("Use 'bd mol squash' instead if you want to preserve a summary.\n")
		fmt.Printf("\nContinue? [y/N] ")

		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Canceled.")
			return
		}
	}

	// Perform the burn
	result, err := burnWisps(ctx, store, wispIDs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error burning wisp: %v\n", err)
		os.Exit(1)
	}
	result.MoleculeID = resolvedID

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Burned wisp: %d issues deleted\n", ui.RenderPass("✓"), result.DeletedCount)
	fmt.Printf("  Ephemeral: %s\n", resolvedID)
	fmt.Printf("  No digest created.\n")
}

// burnPersistentMolecule handles mol deletion (cascade delete)
func burnPersistentMolecule(ctx context.Context, resolvedID string, dryRun, force bool) {
	// Load the molecule subgraph to show what will be deleted
	subgraph, err := loadTemplateSubgraph(ctx, store, resolvedID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
		os.Exit(1)
	}

	// Collect all issue IDs in the molecule
	var issueIDs []string
	for _, issue := range subgraph.Issues {
		issueIDs = append(issueIDs, issue.ID)
	}

	if len(issueIDs) == 0 {
		if jsonOutput {
			outputJSON(BurnResult{
				MoleculeID:   resolvedID,
				DeletedCount: 0,
			})
		} else {
			fmt.Printf("No issues found for molecule %s\n", resolvedID)
		}
		return
	}

	if dryRun {
		fmt.Printf("\nDry run: would burn mol %s\n\n", resolvedID)
		fmt.Printf("Root: %s\n", subgraph.Root.Title)
		fmt.Printf("\nIssues to delete (%d total):\n", len(issueIDs))
		for _, issue := range subgraph.Issues {
			status := string(issue.Status)
			if issue.ID == subgraph.Root.ID {
				fmt.Printf("  - [%s] %s (%s) [ROOT]\n", status, issue.Title, issue.ID)
			} else {
				fmt.Printf("  - [%s] %s (%s)\n", status, issue.Title, issue.ID)
			}
		}
		fmt.Printf("\nNote: Persistent mol - deletions sync to remotes.\n")
		fmt.Printf("No digest will be created (use 'bd mol squash' to create one).\n")
		return
	}

	// Confirm unless --force
	if !force && !jsonOutput {
		fmt.Printf("About to burn mol %s (%d issues)\n", resolvedID, len(issueIDs))
		fmt.Printf("This will permanently delete all molecule data with no digest.\n")
		fmt.Printf("Note: Persistent mol - deletions sync to remotes.\n")
		fmt.Printf("Use 'bd mol squash' instead if you want to preserve a summary.\n")
		fmt.Printf("\nContinue? [y/N] ")

		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Canceled.")
			return
		}
	}

	// Use deleteBatch with cascade=false (we already have all IDs from subgraph)
	// force=true, hardDelete=false (Dolt handles sync)
	deleteBatch(nil, issueIDs, true, false, false, jsonOutput, false, "mol burn")
}

// burnWisps deletes all wisp issues without creating a digest
//
//nolint:unparam // error return kept for future use and consistent API
func burnWisps(ctx context.Context, s *dolt.DoltStore, ids []string) (*BurnResult, error) {
	result := &BurnResult{
		DeletedIDs: make([]string, 0, len(ids)),
	}

	for _, id := range ids {
		if err := s.DeleteIssue(ctx, id); err != nil {
			// Log but continue - try to delete as many as possible
			fmt.Fprintf(os.Stderr, "Warning: failed to delete %s: %v\n", id, err)
			continue
		}
		result.DeletedIDs = append(result.DeletedIDs, id)
		result.DeletedCount++
	}

	return result, nil
}

func init() {
	molBurnCmd.Flags().Bool("dry-run", false, "Preview what would be deleted")
	molBurnCmd.Flags().Bool("force", false, "Skip confirmation prompt")

	molCmd.AddCommand(molBurnCmd)
}
