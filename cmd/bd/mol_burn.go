package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molBurnCmd = &cobra.Command{
	Use:   "burn <molecule-id>",
	Short: "Delete a wisp molecule without creating a digest",
	Long: `Burn a wisp molecule, deleting it without creating a digest.

Unlike squash (which creates a permanent digest before deletion), burn
completely removes the wisp with no trace. Use this for:
  - Abandoned patrol cycles
  - Crashed or failed workflows
  - Test/debug wisps you don't want to preserve

The burn operation:
  1. Verifies the molecule is in wisp storage (.beads-wisp/)
  2. Deletes the molecule and all its children
  3. No digest is created (use 'bd mol squash' if you want a digest)

CAUTION: This is a destructive operation. The wisp's data will be
permanently lost. If you want to preserve a summary, use 'bd mol squash'.

Example:
  bd mol burn bd-abc123              # Delete wisp with no trace
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
	WispDir      string   `json:"wisp_dir"`
}

func runMolBurn(cmd *cobra.Command, args []string) {
	CheckReadonly("mol burn")

	ctx := rootCtx

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")

	moleculeID := args[0]

	// Find wisp storage
	wispDir := beads.FindWispDir()
	if wispDir == "" {
		if jsonOutput {
			outputJSON(BurnResult{
				MoleculeID:   moleculeID,
				DeletedCount: 0,
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: no .beads directory found\n")
		}
		os.Exit(1)
	}

	// Check if wisp directory exists
	if _, err := os.Stat(wispDir); os.IsNotExist(err) {
		if jsonOutput {
			outputJSON(BurnResult{
				MoleculeID:   moleculeID,
				DeletedCount: 0,
				WispDir:      wispDir,
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: no wisp storage found at %s\n", wispDir)
		}
		os.Exit(1)
	}

	// Open wisp storage
	wispStore, err := beads.NewWispStorage(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening wisp storage: %v\n", err)
		os.Exit(1)
	}
	defer wispStore.Close()

	// Resolve molecule ID in wisp storage
	resolvedID, err := utils.ResolvePartialID(ctx, wispStore, moleculeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: molecule %s not found in wisp storage\n", moleculeID)
		fmt.Fprintf(os.Stderr, "Hint: mol burn only works with wisps in .beads-wisp/\n")
		fmt.Fprintf(os.Stderr, "      Use 'bd wisp list' to see available wisps\n")
		os.Exit(1)
	}

	// Load the molecule subgraph
	subgraph, err := loadTemplateSubgraph(ctx, wispStore, resolvedID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading wisp molecule: %v\n", err)
		os.Exit(1)
	}

	// Collect all issue IDs to delete
	var allIDs []string
	for _, issue := range subgraph.Issues {
		allIDs = append(allIDs, issue.ID)
	}

	if dryRun {
		fmt.Printf("\nDry run: would burn wisp %s\n\n", resolvedID)
		fmt.Printf("Root: %s\n", subgraph.Root.Title)
		fmt.Printf("Storage: .beads-wisp/\n")
		fmt.Printf("\nIssues to delete (%d total):\n", len(allIDs))
		for _, issue := range subgraph.Issues {
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
		fmt.Printf("About to burn wisp %s (%d issues)\n", resolvedID, len(allIDs))
		fmt.Printf("This will permanently delete all data with no digest.\n")
		fmt.Printf("Use 'bd mol squash' instead if you want to preserve a summary.\n")
		fmt.Printf("\nContinue? [y/N] ")

		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Cancelled.")
			return
		}
	}

	// Perform the burn
	result, err := burnWisp(ctx, wispStore, allIDs, wispDir)
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
	fmt.Printf("  Wisp: %s\n", resolvedID)
	fmt.Printf("  No digest created.\n")
}

// burnWisp deletes all wisp issues without creating a digest
func burnWisp(ctx context.Context, wispStore beads.Storage, ids []string, wispDir string) (*BurnResult, error) {
	// Type assert to SQLite storage for delete access
	sqliteStore, ok := wispStore.(*sqlite.SQLiteStorage)
	if !ok {
		return nil, fmt.Errorf("burn requires SQLite storage backend")
	}

	result := &BurnResult{
		DeletedIDs: make([]string, 0, len(ids)),
		WispDir:    wispDir,
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
