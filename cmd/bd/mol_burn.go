package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molBurnCmd = &cobra.Command{
	Use:   "burn <molecule-id>",
	Short: "Delete a molecule without creating a digest",
	Long: `Burn a molecule, deleting it without creating a digest.

Unlike squash (which creates a permanent digest before deletion), burn
completely removes the molecule with no trace. Use this for:
  - Abandoned patrol cycles
  - Crashed or failed workflows
  - Test/debug molecules you don't want to preserve

The burn operation differs based on molecule phase:
  - Wisp (ephemeral): Direct delete, no tombstones
  - Mol (persistent): Cascade delete with tombstones (syncs to remotes)

CAUTION: This is a destructive operation. The molecule's data will be
permanently lost. If you want to preserve a summary, use 'bd mol squash'.

Example:
  bd mol burn bd-abc123              # Delete molecule with no trace
  bd mol burn bd-abc123 --dry-run    # Preview what would be deleted
  bd mol burn bd-abc123 --force      # Skip confirmation`,
	Args: cobra.ExactArgs(1),
	Run:  runMolBurn,
}

// BurnResult holds the result of a burn operation
type BurnResult struct {
	MoleculeID   string   `json:"molecule_id"`
	DeletedIDs   []string `json:"deleted_ids"`
	DeletedCount int      `json:"deleted_count"`
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

	moleculeID := args[0]

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
		// Wisp: direct delete without tombstones
		burnWispMolecule(ctx, resolvedID, dryRun, force)
	} else {
		// Mol: cascade delete with tombstones
		burnPersistentMolecule(ctx, resolvedID, dryRun, force)
	}
}

// burnWispMolecule handles wisp deletion (no tombstones, ephemeral-only)
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

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Burned wisp: %d issues deleted\n", ui.RenderPass("✓"), result.DeletedCount)
	fmt.Printf("  Ephemeral: %s\n", resolvedID)
	fmt.Printf("  No digest created.\n")
}

// burnPersistentMolecule handles mol deletion (with tombstones, cascade delete)
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
		fmt.Printf("\nNote: Persistent mol - will create tombstones (syncs to remotes).\n")
		fmt.Printf("No digest will be created (use 'bd mol squash' to create one).\n")
		return
	}

	// Confirm unless --force
	if !force && !jsonOutput {
		fmt.Printf("About to burn mol %s (%d issues)\n", resolvedID, len(issueIDs))
		fmt.Printf("This will permanently delete all molecule data with no digest.\n")
		fmt.Printf("Note: Persistent mol - tombstones will sync to remotes.\n")
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
	// force=true, hardDelete=false (keep tombstones for sync)
	deleteBatch(nil, issueIDs, true, false, false, jsonOutput, false, "mol burn")
}

// burnWisps deletes all wisp issues without creating a digest
func burnWisps(ctx context.Context, s interface{}, ids []string) (*BurnResult, error) {
	// Type assert to SQLite storage for delete access
	sqliteStore, ok := s.(*sqlite.SQLiteStorage)
	if !ok {
		return nil, fmt.Errorf("burn requires SQLite storage backend")
	}

	result := &BurnResult{
		DeletedIDs: make([]string, 0, len(ids)),
	}

	for _, id := range ids {
		if err := sqliteStore.DeleteIssue(ctx, id); err != nil {
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
