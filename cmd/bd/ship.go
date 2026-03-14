package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/types"
	"github.com/gastownhall/beads/internal/ui"
)

var shipCmd = &cobra.Command{
	Use:   "ship <capability>",
	Short: "Publish a capability for cross-project dependencies",
	Long: `Ship a capability to satisfy cross-project dependencies.

This command:
  1. Finds issue with export:<capability> label
  2. Validates issue is closed (or --force to override)
  3. Adds provides:<capability> label

External projects can depend on this capability using:
  bd dep add <issue> external:<project>:<capability>

The capability is resolved when the external project has a closed issue
with the provides:<capability> label.

Examples:
  bd ship mol-run-assignee              # Ship the mol-run-assignee capability
  bd ship mol-run-assignee --force      # Ship even if issue is not closed
  bd ship mol-run-assignee --dry-run    # Preview without making changes`,
	Args: cobra.ExactArgs(1),
	Run:  runShip,
}

func runShip(cmd *cobra.Command, args []string) {
	CheckReadonly("ship")

	capability := args[0]
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	ctx := rootCtx

	// Find issue with export:<capability> label
	exportLabel := "export:" + capability
	providesLabel := "provides:" + capability

	var issues []*types.Issue
	var err error

	issues, err = store.GetIssuesByLabel(ctx, exportLabel)
	if err != nil {
		FatalError("listing issues: %v", err)
	}

	if len(issues) == 0 {
		FatalErrorWithHint(
			fmt.Sprintf("no issue found with label '%s'", exportLabel),
			fmt.Sprintf("add the label first: bd label add <issue-id> %s", exportLabel))
	}

	if len(issues) > 1 {
		fmt.Fprintf(os.Stderr, "Error: multiple issues found with label '%s':\n", exportLabel)
		for _, issue := range issues {
			fmt.Fprintf(os.Stderr, "  %s: %s (%s)\n", issue.ID, issue.Title, issue.Status)
		}
		FatalError("only one issue should have this label")
	}

	issue := issues[0]

	// Validate issue is closed (unless --force)
	if issue.Status != types.StatusClosed && !force {
		FatalErrorWithHint(
			fmt.Sprintf("issue %s is not closed (status: %s)", issue.ID, issue.Status),
			"close the issue first, or use --force to override")
	}

	// Check if already shipped (use direct store access)
	hasProvides := false
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		FatalError("getting labels: %v", err)
	}
	for _, l := range labels {
		if l == providesLabel {
			hasProvides = true
			break
		}
	}

	if hasProvides {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":     "already_shipped",
				"capability": capability,
				"issue_id":   issue.ID,
			})
		} else {
			fmt.Printf("%s Capability '%s' already shipped (%s)\n",
				ui.RenderPass("✓"), capability, issue.ID)
		}
		return
	}

	if dryRun {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":     "dry_run",
				"capability": capability,
				"issue_id":   issue.ID,
				"would_add":  providesLabel,
			})
		} else {
			fmt.Printf("%s Would ship '%s' on %s (dry run)\n",
				ui.RenderAccent("→"), capability, issue.ID)
		}
		return
	}

	// Add provides:<capability> label (use direct store access)
	if err := store.AddLabel(ctx, issue.ID, providesLabel, actor); err != nil {
		FatalError("adding label: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":     "shipped",
			"capability": capability,
			"issue_id":   issue.ID,
			"label":      providesLabel,
		})
	} else {
		fmt.Printf("%s Shipped %s (%s)\n",
			ui.RenderPass("✓"), capability, issue.ID)
		fmt.Printf("  Added label: %s\n", providesLabel)
		fmt.Printf("\nExternal projects can now depend on: external:%s:%s\n",
			"<this-project>", capability)
	}
}

func init() {
	shipCmd.Flags().Bool("force", false, "Ship even if issue is not closed")
	shipCmd.Flags().Bool("dry-run", false, "Preview without making changes")

	rootCmd.AddCommand(shipCmd)
}
