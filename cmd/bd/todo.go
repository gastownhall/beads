package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var todoCmd = &cobra.Command{
	Use:     "todo",
	GroupID: "issues",
	Short:   "Manage TODO items (convenience wrapper for task issues)",
	Long: `Manage TODO items as lightweight task issues.

TODOs are regular task-type issues with convenient shortcuts:
  bd todo add "Title"    -> bd create "Title" -t task -p 2
  bd todo                -> bd list --type task --status open
  bd todo done <id>      -> bd close <id>

TODOs can be promoted to full issues by changing type or priority:
  bd update todo-123 --type bug --priority 0`,
	Run: func(cmd *cobra.Command, args []string) {
		// Default action: list todos
		listTodosCmd.Run(cmd, args)
	},
}

var addTodoCmd = &cobra.Command{
	Use:   "add <title>",
	Short: "Add a new TODO item",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("todo add")
		title := strings.Join(args, " ")

		// Get priority flag, default to 2
		priority, _ := cmd.Flags().GetInt("priority")

		// Get description flag
		description, _ := cmd.Flags().GetString("description")

		// Create the issue as a task type
		ctx := rootCtx

		issueType := types.TypeTask
		issue := &types.Issue{
			Title:       title,
			Description: description,
			Priority:    priority,
			IssueType:   issueType,
			Status:      types.StatusOpen,
			Assignee:    getActorWithGit(),
			Owner:       getOwner(),
			CreatedBy:   getActorWithGit(),
		}

		// Generate ID
		if err := store.CreateIssue(ctx, issue, getActorWithGit()); err != nil {
			FatalError("failed to create TODO: %v", err)
		}

		if jsonOutput {
			data, err := json.MarshalIndent(issue, "", "  ")
			if err != nil {
				FatalError("failed to marshal JSON: %v", err)
			}
			fmt.Println(string(data))
		} else {
			fmt.Printf("Created %s: %s\n", ui.RenderID(issue.ID), issue.Title)
		}
	},
}

var listTodosCmd = &cobra.Command{
	Use:   "list",
	Short: "List TODO items",
	Run: func(cmd *cobra.Command, args []string) {
		// Get show-all flag
		showAll, _ := cmd.Flags().GetBool("all")

		ctx := rootCtx

		// Build filter for task-type issues
		taskType := types.TypeTask
		filter := types.IssueFilter{
			IssueType: &taskType,
		}
		if !showAll {
			openStatus := types.StatusOpen
			filter.Status = &openStatus
		}

		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			FatalError("failed to list TODOs: %v", err)
		}

		if jsonOutput {
			data, err := json.MarshalIndent(issues, "", "  ")
			if err != nil {
				FatalError("failed to marshal JSON: %v", err)
			}
			fmt.Println(string(data))
		} else {
			if len(issues) == 0 {
				fmt.Println("No TODOs found")
				return
			}

			// Sort by priority then ID
			todoSortIssues(issues)

			// Pretty print
			for _, issue := range issues {
				statusIcon := ui.RenderStatusIcon(string(issue.Status))
				priority := ui.RenderPriority(issue.Priority)
				fmt.Printf("  %s %s  %-40s  %s  %s\n",
					statusIcon,
					ui.RenderID(issue.ID),
					todoTruncate(issue.Title, 40),
					priority,
					issue.Status)
			}
			fmt.Printf("\nTotal: %d TODOs\n", len(issues))
		}
	},
}

var doneTodoCmd = &cobra.Command{
	Use:   "done <id> [<id>...]",
	Short: "Mark TODO(s) as done",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("todo done")
		ctx := rootCtx

		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			reason = "Completed"
		}

		var closedIDs []string
		for _, issueID := range args {
			// Verify it exists
			issue, err := store.GetIssue(ctx, issueID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to get issue %s: %v\n", issueID, err)
				continue
			}
			if issue == nil {
				fmt.Fprintf(os.Stderr, "Error: issue %s not found\n", issueID)
				continue
			}

			// Close the issue (session is empty string for CLI operations)
			if err := store.CloseIssue(ctx, issueID, reason, getActorWithGit(), ""); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to close %s: %v\n", issueID, err)
				continue
			}
			closedIDs = append(closedIDs, issueID)
		}

		if jsonOutput {
			data, err := json.MarshalIndent(map[string]interface{}{
				"closed": closedIDs,
				"reason": reason,
			}, "", "  ")
			if err != nil {
				FatalError("failed to marshal JSON: %v", err)
			}
			fmt.Println(string(data))
		} else {
			for _, id := range closedIDs {
				fmt.Printf("Closed %s\n", ui.RenderID(id))
			}
		}
	},
}

func init() {
	// Add subcommands
	todoCmd.AddCommand(addTodoCmd)
	todoCmd.AddCommand(listTodosCmd)
	todoCmd.AddCommand(doneTodoCmd)

	// Add flags
	addTodoCmd.Flags().IntP("priority", "p", 2, "Priority (0-4, default 2)")
	addTodoCmd.Flags().StringP("description", "d", "", "Description")

	listTodosCmd.Flags().Bool("all", false, "Show all TODOs including completed")

	doneTodoCmd.Flags().String("reason", "", "Reason for closing (default: Completed)")

	// Register with root
	rootCmd.AddCommand(todoCmd)
}

// todoTruncate truncates a string to the specified length with ellipsis
func todoTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// todoSortIssues sorts issues by priority (ascending) then ID
func todoSortIssues(issues []*types.Issue) {
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(a.ID, b.ID)
	})
}
