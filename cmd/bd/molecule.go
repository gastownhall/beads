package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

var moleculeCmd = &cobra.Command{
	Use:   "molecule",
	Short: "Manage template molecules",
	Long: `Manage template molecules for issue instantiation.

Molecules are template issues that can be instantiated to create work items.
They are stored in molecules.jsonl and marked with is_template=true.

Examples:
  bd molecule list                    # List all available molecules
  bd molecule show mol-123            # Show details of a molecule
`,
}

var moleculeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available molecules",
	Long: `List all available template molecules.

Templates are read-only issues that can be instantiated to create work items.
Use --all to include closed molecules.

Examples:
  bd molecule list                    # List open molecules
  bd molecule list --all              # List all molecules including closed
`,
	Run: func(cmd *cobra.Command, args []string) {
		showAll, _ := cmd.Flags().GetBool("all")

		ctx := rootCtx
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// Build filter for template molecules
		isTemplate := true
		filter := types.IssueFilter{
			IsTemplate: &isTemplate,
		}

		if !showAll {
			// Default to non-closed
			status := types.StatusOpen
			filter.Status = &status
		}

		// Direct mode only for now
		if store == nil {
			fmt.Fprintf(os.Stderr, "Error: database not available\n")
			os.Exit(1)
		}

		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(issues) == 0 {
			fmt.Println("No molecules found")
			return
		}

		if jsonOutput {
			outputJSON(issues)
			return
		}

		// Print molecule list
		for _, issue := range issues {
			priorityStr := fmt.Sprintf("P%d", issue.Priority)
			fmt.Printf("%s [%s] %s\n", issue.ID, priorityStr, issue.Title)
		}
		fmt.Printf("\n%d molecule(s)\n", len(issues))
	},
}

var moleculeShowCmd = &cobra.Command{
	Use:   "show <molecule-id>",
	Short: "Show molecule details",
	Long: `Show detailed information about a template molecule.

Examples:
  bd molecule show mol-123
`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		moleculeID := args[0]

		ctx := rootCtx
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		if store == nil {
			fmt.Fprintf(os.Stderr, "Error: database not available\n")
			os.Exit(1)
		}

		issue, err := store.GetIssue(ctx, moleculeID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if issue == nil {
			fmt.Fprintf(os.Stderr, "Error: molecule %s not found\n", moleculeID)
			os.Exit(1)
		}

		if !issue.IsTemplate {
			fmt.Fprintf(os.Stderr, "Warning: %s is not a template molecule\n", moleculeID)
		}

		if jsonOutput {
			outputJSON(issue)
			return
		}

		// Print molecule details
		fmt.Printf("%s: %s\n", issue.ID, issue.Title)
		fmt.Printf("Type: %s\n", issue.IssueType)
		fmt.Printf("Priority: P%d\n", issue.Priority)
		fmt.Printf("Status: %s\n", issue.Status)
		fmt.Printf("Template: %v\n", issue.IsTemplate)

		if issue.Description != "" {
			fmt.Printf("\nDescription:\n%s\n", issue.Description)
		}

		if issue.Design != "" {
			fmt.Printf("\nDesign:\n%s\n", issue.Design)
		}

		if issue.AcceptanceCriteria != "" {
			fmt.Printf("\nAcceptance Criteria:\n%s\n", issue.AcceptanceCriteria)
		}
	},
}

var moleculeInstantiateCmd = &cobra.Command{
	Use:   "instantiate <molecule-id>",
	Short: "Create a work item from a template molecule",
	Long: `Create a new work item based on a template molecule.

The new issue will inherit the template's title, description, design,
acceptance criteria, priority, and issue type. The new issue will have
is_template=false and will be linked to the template via a discovered-from
dependency.

Examples:
  bd molecule instantiate mol-123              # Create work item from template
  bd molecule instantiate mol-123 --title "Custom title"  # Override title
`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("instantiate")
		moleculeID := args[0]

		// Get flag overrides
		titleOverride, _ := cmd.Flags().GetString("title")
		assignee, _ := cmd.Flags().GetString("assignee")

		ctx := rootCtx
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		if store == nil && daemonClient == nil {
			fmt.Fprintf(os.Stderr, "Error: database not available\n")
			os.Exit(1)
		}

		// Get the template molecule
		var template *types.Issue
		var err error

		if daemonClient != nil {
			showArgs := &rpc.ShowArgs{ID: moleculeID}
			resp, showErr := daemonClient.Show(showArgs)
			if showErr != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", showErr)
				os.Exit(1)
			}
			template = &types.Issue{}
			if jsonErr := json.Unmarshal(resp.Data, template); jsonErr != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", jsonErr)
				os.Exit(1)
			}
		} else {
			template, err = store.GetIssue(ctx, moleculeID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		if template == nil {
			fmt.Fprintf(os.Stderr, "Error: molecule %s not found\n", moleculeID)
			os.Exit(1)
		}

		if !template.IsTemplate {
			fmt.Fprintf(os.Stderr, "Warning: %s is not a template molecule (is_template=false)\n", moleculeID)
		}

		// Build title (use override or template title)
		title := template.Title
		if titleOverride != "" {
			title = titleOverride
		}

		// Create the new work item
		if daemonClient != nil {
			// Daemon mode
			createArgs := &rpc.CreateArgs{
				Title:              title,
				Description:        template.Description,
				IssueType:          string(template.IssueType),
				Priority:           template.Priority,
				Design:             template.Design,
				AcceptanceCriteria: template.AcceptanceCriteria,
				Assignee:           assignee,
				Dependencies:       []string{"discovered-from:" + moleculeID},
			}

			resp, createErr := daemonClient.Create(createArgs)
			if createErr != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", createErr)
				os.Exit(1)
			}

			var issue types.Issue
			if jsonErr := json.Unmarshal(resp.Data, &issue); jsonErr != nil {
				fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", jsonErr)
				os.Exit(1)
			}

			// Run create hook
			if hookRunner != nil {
				hookRunner.Run(hooks.EventCreate, &issue)
			}

			if jsonOutput {
				outputJSON(&issue)
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Created work item: %s (from template %s)\n", green("✓"), issue.ID, moleculeID)
				fmt.Printf("  Title: %s\n", issue.Title)
				fmt.Printf("  Priority: P%d\n", issue.Priority)
				fmt.Printf("  Status: %s\n", issue.Status)
			}
			return
		}

		// Direct mode
		now := time.Now()
		issue := &types.Issue{
			Title:              title,
			Description:        template.Description,
			Design:             template.Design,
			AcceptanceCriteria: template.AcceptanceCriteria,
			Status:             types.StatusOpen,
			Priority:           template.Priority,
			IssueType:          template.IssueType,
			Assignee:           assignee,
			IsTemplate:         false, // Work items are not templates
			CreatedAt:          now,
			UpdatedAt:          now,
		}

		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating issue: %v\n", err)
			os.Exit(1)
		}

		// Get the created issue (ID is set by CreateIssue)
		createdIssue, err := store.GetIssue(ctx, issue.ID)
		if err != nil || createdIssue == nil {
			fmt.Fprintf(os.Stderr, "Error getting created issue: %v\n", err)
			os.Exit(1)
		}

		// Add discovered-from dependency to template
		dep := &types.Dependency{
			IssueID:     createdIssue.ID,
			DependsOnID: moleculeID,
			Type:        types.DepDiscoveredFrom,
			CreatedAt:   now,
			CreatedBy:   actor,
		}
		if err := store.AddDependency(ctx, dep, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add dependency to template: %v\n", err)
		}

		// Run create hook
		if hookRunner != nil {
			hookRunner.Run(hooks.EventCreate, createdIssue)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(createdIssue)
		} else {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("%s Created work item: %s (from template %s)\n", green("✓"), createdIssue.ID, moleculeID)
			fmt.Printf("  Title: %s\n", createdIssue.Title)
			fmt.Printf("  Priority: P%d\n", createdIssue.Priority)
			fmt.Printf("  Status: %s\n", createdIssue.Status)
		}
	},
}

func init() {
	// Add subcommands
	moleculeCmd.AddCommand(moleculeListCmd)
	moleculeCmd.AddCommand(moleculeShowCmd)
	moleculeCmd.AddCommand(moleculeInstantiateCmd)

	// Flags for list command
	moleculeListCmd.Flags().Bool("all", false, "Include closed molecules")

	// Flags for instantiate command
	moleculeInstantiateCmd.Flags().String("title", "", "Override the template title")
	moleculeInstantiateCmd.Flags().StringP("assignee", "a", "", "Assign the new work item")

	// Add molecule command to root
	rootCmd.AddCommand(moleculeCmd)
}
