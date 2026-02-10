package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/factory"
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

	// Ship requires direct store access for label operations
	// Use factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
	if daemonClient != nil && store == nil {
		beadsDir := filepath.Dir(dbPath)
		store, err = factory.NewFromConfig(ctx, beadsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = store.Close() }()
	}

	if daemonClient != nil {
		// Use RPC to list issues with the export label
		listArgs := &rpc.ListArgs{
			LabelsAny: []string{exportLabel},
		}
		resp, err := daemonClient.List(listArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing issues: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(resp.Data, &issues); err != nil {
			fmt.Fprintf(os.Stderr, "Error unmarshaling issues: %v\n", err)
			os.Exit(1)
		}
	} else {
		issues, err = store.GetIssuesByLabel(ctx, exportLabel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing issues: %v\n", err)
			os.Exit(1)
		}
	}

	if len(issues) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no issue found with label '%s'\n", exportLabel)
		fmt.Fprintf(os.Stderr, "Hint: add the label first: bd label add <issue-id> %s\n", exportLabel)
		os.Exit(1)
	}

	if len(issues) > 1 {
		fmt.Fprintf(os.Stderr, "Error: multiple issues found with label '%s':\n", exportLabel)
		for _, issue := range issues {
			fmt.Fprintf(os.Stderr, "  %s: %s (%s)\n", issue.ID, issue.Title, issue.Status)
		}
		fmt.Fprintf(os.Stderr, "Hint: only one issue should have this label\n")
		os.Exit(1)
	}

	issue := issues[0]

	// Validate issue is closed (unless --force)
	if issue.Status != types.StatusClosed && !force {
		fmt.Fprintf(os.Stderr, "Error: issue %s is not closed (status: %s)\n", issue.ID, issue.Status)
		fmt.Fprintf(os.Stderr, "Hint: close the issue first, or use --force to override\n")
		os.Exit(1)
	}

	// Check if already shipped (use direct store access)
	hasProvides := false
	labels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting labels: %v\n", err)
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Error adding label: %v\n", err)
		os.Exit(1)
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
