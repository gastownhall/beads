package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
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
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runShip,
}

func runShip(cmd *cobra.Command, args []string) error {
	CheckReadonly("ship")

	evt := metrics.NewCommandEvent("ship")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	capability := args[0]
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	ctx := rootCtx

	exportLabel := "export:" + capability
	providesLabel := "provides:" + capability

	var issues []*types.Issue
	var err error

	issues, err = store.GetIssuesByLabel(ctx, exportLabel)
	if err != nil {
		return HandleErrorRespectJSON("listing issues: %v", err)
	}

	if len(issues) == 0 {
		return HandleErrorWithHintRespectJSON(
			fmt.Sprintf("no issue found with label '%s'", exportLabel),
			fmt.Sprintf("add the label first: bd label add <issue-id> %s", exportLabel))
	}

	if len(issues) > 1 {
		fmt.Fprintf(os.Stderr, "Error: multiple issues found with label '%s':\n", exportLabel)
		for _, issue := range issues {
			fmt.Fprintf(os.Stderr, "  %s: %s (%s)\n", issue.ID, issue.Title, issue.Status)
		}
		return HandleErrorRespectJSON("only one issue should have this label")
	}

	issue := issues[0]

	if issue.Status != types.StatusClosed && !force {
		return HandleErrorWithHintRespectJSON(
			fmt.Sprintf("issue %s is not closed (status: %s)", issue.ID, issue.Status),
			"close the issue first, or use --force to override")
	}

	hasProvides := false
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		return HandleErrorRespectJSON("getting labels: %v", err)
	}
	for _, l := range labels {
		if l == providesLabel {
			hasProvides = true
			break
		}
	}

	if hasProvides {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":     "already_shipped",
				"capability": capability,
				"issue_id":   issue.ID,
			})
		}
		fmt.Printf("%s Capability '%s' already shipped (%s)\n",
			ui.RenderPass("✓"), capability, issue.ID)
		return nil
	}

	if dryRun {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":     "dry_run",
				"capability": capability,
				"issue_id":   issue.ID,
				"would_add":  providesLabel,
			})
		}
		fmt.Printf("%s Would ship '%s' on %s (dry run)\n",
			ui.RenderAccent("→"), capability, issue.ID)
		return nil
	}

	if err := store.AddLabel(ctx, issue.ID, providesLabel, actor); err != nil {
		return HandleErrorRespectJSON("adding label: %v", err)
	}

	commandDidWrite.Store(true)

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"status":     "shipped",
			"capability": capability,
			"issue_id":   issue.ID,
			"label":      providesLabel,
		})
	}
	fmt.Printf("%s Shipped %s (%s)\n",
		ui.RenderPass("✓"), capability, issue.ID)
	fmt.Printf("  Added label: %s\n", providesLabel)
	fmt.Printf("\nExternal projects can now depend on: external:%s:%s\n",
		"<this-project>", capability)
	return nil
}

func init() {
	shipCmd.Flags().Bool("force", false, "Ship even if issue is not closed")
	shipCmd.Flags().Bool("dry-run", false, "Preview without making changes")

	rootCmd.AddCommand(shipCmd)
}
