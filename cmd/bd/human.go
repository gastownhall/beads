package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var humanCmd = &cobra.Command{
	Use:     "human",
	GroupID: "setup",
	Short:   "Show essential commands for human users",
	Long: `Display a focused help menu showing only the most common commands.

bd has 70+ commands - many for AI agents, integrations, and advanced workflows.
This command shows the ~15 essential commands that human users need most often.

For the full command list, run: bd --help

SUBCOMMANDS:
  human list              List all human-needed beads (issues with 'human' label)
  human respond <id>      Respond to a human-needed bead (adds comment and closes)
  human dismiss <id>      Dismiss a human-needed bead permanently
  human stats             Show summary statistics for human-needed beads`,
	Run: func(cmd *cobra.Command, args []string) {
		evt := metrics.NewCommandEvent("human")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		fmt.Printf("\n%s\n", ui.RenderBold("bd - Essential Commands for Humans"))
		fmt.Printf("For all 70+ commands: bd --help\n\n")

		// Issues - Core workflow
		fmt.Printf("%s\n", ui.RenderAccent("Working With Issues:"))
		printCmd("create", "Create a new issue")
		printCmd("list", "List issues (filter with --status, --priority, --label)")
		printCmd("show <id>", "Show issue details")
		printCmd("update <id>", "Update an issue (--status, --priority, --assignee)")
		printCmd("close <id>", "Close one or more issues")
		printCmd("reopen <id>", "Reopen a closed issue")
		printCmd("note <id> <text>", "Add a note to an issue (or: comments add <id>)")
		fmt.Println()

		// Workflow
		fmt.Printf("%s\n", ui.RenderAccent("Finding Work:"))
		printCmd("ready", "Show issues ready to work on (no blockers)")
		printCmd("search <query>", "Search issues by text")
		printCmd("status", "Show project overview and counts")
		printCmd("stats", "Show detailed statistics")
		fmt.Println()

		// Dependencies
		fmt.Printf("%s\n", ui.RenderAccent("Dependencies:"))
		printCmd("dep add <a> <b>", "Add dependency (a depends on b)")
		printCmd("dep remove <a> <b>", "Remove a dependency")
		printCmd("dep tree <id>", "Show dependency tree")
		printCmd("graph", "Display visual dependency graph")
		printCmd("blocked", "Show all blocked issues")
		fmt.Println()

		// Setup & Maintenance
		fmt.Printf("%s\n", ui.RenderAccent("Setup & Maintenance:"))
		printCmd("init", "Initialize bd in current directory")
		printCmd("doctor", "Check installation health")
		fmt.Println()

		// Help
		fmt.Printf("%s\n", ui.RenderAccent("Getting Help:"))
		printCmd("quickstart", "Quick start guide with examples")
		printCmd("help <cmd>", "Help for any command")
		printCmd("--help", "Full command list (70+ commands)")
		fmt.Println()

		// Common examples
		fmt.Printf("%s\n", ui.RenderAccent("Quick Examples:"))
		fmt.Printf("  %s\n", ui.RenderMuted("# Create and track an issue"))
		fmt.Printf("  bd create \"Fix login bug\" --priority 1\n")
		fmt.Printf("  bd update bd-abc123 --claim\n")
		fmt.Printf("  bd close bd-abc123\n\n")

		fmt.Printf("  %s\n", ui.RenderMuted("# See what needs doing"))
		fmt.Printf("  bd ready                    # What can I work on?\n")
		fmt.Printf("  bd list --status open       # All open issues\n")
		fmt.Printf("  bd blocked                  # What's stuck?\n\n")
	},
}

// human list command
var humanListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all human-needed beads",
	Long: `List all issues labeled with 'human' tag.

These are issues that require human intervention or input.

Examples:
  bd human list
  bd human list --status=open
  bd human list --json`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("human-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		status, _ := cmd.Flags().GetString("status")

		ctx := rootCtx

		filter := types.IssueFilter{
			Labels: []string{"human"},
		}

		if status != "" {
			s := types.Status(status)
			filter.Status = &s
		}

		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("listing human beads: %v", err)
		}

		var err error
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			return HandleErrorRespectJSON("listing human beads: %v", err)
		}

		if jsonOutput {
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
			for _, issue := range issues {
				issue.Labels = labelsMap[issue.ID]
			}

			data, err := json.MarshalIndent(issues, "", "  ")
			if err != nil {
				return HandleErrorRespectJSON("encoding JSON: %v", err)
			}
			fmt.Println(string(data))
			return nil
		}

		printHumanList(issues)
		return nil
	},
}

func printHumanList(issues []*types.Issue) {
	if len(issues) == 0 {
		fmt.Println("No human-needed beads found.")
		return
	}

	fmt.Printf("\n%s (%d found)\n\n", ui.RenderBold("Human-needed beads"), len(issues))
	for _, issue := range issues {
		fmt.Printf("  %s %s\n", ui.RenderCommand(issue.ID), issue.Title)
		if issue.Status != "open" {
			fmt.Printf("    Status: %s\n", issue.Status)
		}
		if issue.Priority != 0 {
			fmt.Printf("    Priority: P%d\n", issue.Priority)
		}
		fmt.Println()
	}
}

// human respond command
var humanRespondCmd = &cobra.Command{
	Use:   "respond <issue-id>",
	Short: "Respond to a human-needed bead",
	Long: `Respond to a human-needed bead by adding a comment and closing it.

The response is added as a comment and the issue is closed with reason "Responded".

Examples:
  bd human respond bd-123 --response "Use OAuth2 for authentication"
  bd human respond bd-123 -r "Approved, proceed with implementation"`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("human-respond")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		response, _ := cmd.Flags().GetString("response")

		if response == "" {
			return HandleErrorRespectJSON("--response is required")
		}

		CheckReadonly("human respond")

		ctx := rootCtx
		issueID := args[0]

		// Direct mode
		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("responding to human bead: %v", err)
		}

		// Resolve partial ID and get issue
		result, err := resolveAndGetIssueForMutation(ctx, store, issueID)
		if err != nil {
			return HandleErrorRespectJSON("resolving issue ID %s: %v", issueID, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue not found: %s", issueID)
		}
		defer result.Close()

		resolvedID := result.ResolvedID
		issue := result.Issue
		targetStore := result.Store

		if issue.Status == "closed" {
			return HandleErrorRespectJSON("issue %s is already closed", resolvedID)
		}

		labelsMap, _ := targetStore.GetLabelsForIssues(ctx, []string{resolvedID})
		hasHumanLabel := false
		for _, label := range labelsMap[resolvedID] {
			if label == "human" {
				hasHumanLabel = true
				break
			}
		}

		if !hasHumanLabel {
			fmt.Fprintf(os.Stderr, "Warning: Issue %s does not have 'human' label\n", resolvedID)
		}

		commentText := fmt.Sprintf("Response: %s", response)
		_, err = targetStore.AddIssueComment(ctx, resolvedID, actor, commentText)
		if err != nil {
			return HandleErrorRespectJSON("adding comment: %v", err)
		}

		if err := targetStore.CloseIssue(ctx, resolvedID, "Responded", actor, ""); err != nil {
			return HandleErrorRespectJSON("closing bead: %v", err)
		}

		fmt.Printf("%s Bead %s closed with response.\n", ui.RenderPass("✔"), resolvedID)
		return nil
	},
}

// human dismiss command
var humanDismissCmd = &cobra.Command{
	Use:   "dismiss <issue-id>",
	Short: "Dismiss a human-needed bead",
	Long: `Dismiss a human-needed bead permanently without responding.

The issue is closed with a "Dismissed" reason and optional note.

Examples:
  bd human dismiss bd-123
  bd human dismiss bd-123 --reason "No longer applicable"`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("human-dismiss")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		reason, _ := cmd.Flags().GetString("reason")

		CheckReadonly("human dismiss")

		ctx := rootCtx
		issueID := args[0]

		// Direct mode
		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("dismissing human bead: %v", err)
		}

		// Resolve partial ID and get issue
		result, err := resolveAndGetIssueForMutation(ctx, store, issueID)
		if err != nil {
			return HandleErrorRespectJSON("resolving issue ID %s: %v", issueID, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue not found: %s", issueID)
		}
		defer result.Close()

		resolvedID := result.ResolvedID
		issue := result.Issue
		targetStore := result.Store

		if issue.Status == "closed" {
			return HandleErrorRespectJSON("issue %s is already closed", resolvedID)
		}

		labelsMap, _ := targetStore.GetLabelsForIssues(ctx, []string{resolvedID})
		hasHumanLabel := false
		for _, label := range labelsMap[resolvedID] {
			if label == "human" {
				hasHumanLabel = true
				break
			}
		}

		if !hasHumanLabel {
			fmt.Fprintf(os.Stderr, "Warning: Issue %s does not have 'human' label\n", resolvedID)
		}

		closeReason := "Dismissed"
		if reason != "" {
			closeReason = fmt.Sprintf("Dismissed: %s", reason)
		}

		if err := targetStore.CloseIssue(ctx, resolvedID, closeReason, actor, ""); err != nil {
			return HandleErrorRespectJSON("closing bead: %v", err)
		}

		fmt.Printf("%s Bead %s dismissed.\n", ui.RenderPass("✔"), resolvedID)
		return nil
	},
}

// human stats command
var humanStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show summary statistics for human-needed beads",
	Long: `Display summary statistics for human-needed beads.

Shows counts for total, pending (open), responded (closed without dismiss),
and dismissed beads.

Example:
  bd human stats`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("human-stats")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx

		filter := types.IssueFilter{
			Labels: []string{"human"},
		}

		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("getting human bead stats: %v", err)
		}

		var err error
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			return HandleErrorRespectJSON("getting human bead stats: %v", err)
		}

		printHumanStats(issues)
		return nil
	},
}

func printHumanStats(issues []*types.Issue) {
	total := len(issues)
	pending := 0
	closed := 0
	dismissed := 0

	for _, issue := range issues {
		switch issue.Status {
		case "closed":
			closed++
			if strings.Contains(strings.ToLower(issue.CloseReason), "dismiss") {
				dismissed++
			}
		default:
			// All non-closed statuses (open, in_progress, blocked, hooked, etc.) are pending
			pending++
		}
	}

	responded := closed - dismissed

	fmt.Printf("\n%s\n", ui.RenderBold("Human Beads Stats"))
	fmt.Println()
	fmt.Printf("  Total:      %d\n", total)
	fmt.Printf("  Pending:    %d\n", pending)
	fmt.Printf("  Responded:  %d\n", responded)
	fmt.Printf("  Dismissed:  %d\n", dismissed)
	fmt.Println()
}

// printCmd prints a command with consistent formatting
func printCmd(cmd, description string) {
	fmt.Printf("  %-20s %s\n", ui.RenderCommand(cmd), description)
}

func init() {
	// Add subcommands to humanCmd
	humanCmd.AddCommand(humanListCmd)
	humanCmd.AddCommand(humanRespondCmd)
	humanCmd.AddCommand(humanDismissCmd)
	humanCmd.AddCommand(humanStatsCmd)

	// Add flags for subcommands
	humanListCmd.Flags().StringP("status", "s", "", "Filter by status (open, closed, etc.)")
	humanRespondCmd.Flags().StringP("response", "r", "", "Response text (required)")
	_ = humanRespondCmd.MarkFlagRequired("response")
	humanDismissCmd.Flags().StringP("reason", "", "", "Reason for dismissal (optional)")

	// Register with root command
	rootCmd.AddCommand(humanCmd)
}
