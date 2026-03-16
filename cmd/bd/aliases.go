package main

// aliases.go — top-level command shortcuts (GH#2611)
//
// These add intuitive aliases for commonly attempted commands:
//   bd comment <id> "text"    → bd comments add <id> "text"
//   bd assign <id> <name>     → bd update <id> --assignee <name>
//   bd tag <id> <label>       → bd update <id> --add-label <label>
//   bd note <id> "text"       → bd update <id> --notes "text"
//   bd priority <id> <n>      → bd update <id> --priority <n>

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/utils"
)

var commentAliasCmd = &cobra.Command{
	Use:     "comment [issue-id] [text]",
	GroupID: "issues",
	Short:   "Add a comment to an issue (shortcut for 'comments add')",
	Args:    cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("comment")
		if len(args) < 2 {
			FatalErrorRespectJSON("usage: bd comment <id> \"text\"")
		}
		issueID := args[0]
		commentText := strings.Join(args[1:], " ")

		if strings.TrimSpace(commentText) == "" {
			FatalErrorRespectJSON("comment text cannot be empty")
		}

		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("adding comment: %v", err)
		}
		ctx := rootCtx

		fullID, err := utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		comment, err := store.AddIssueComment(ctx, fullID, getActorWithGit(), commentText)
		if err != nil {
			FatalErrorRespectJSON("adding comment: %v", err)
		}

		if jsonOutput {
			outputJSON(comment)
			return
		}
		fmt.Printf("Comment added to %s\n", fullID)
	},
}

var assignAliasCmd = &cobra.Command{
	Use:     "assign [issue-id] [assignee]",
	GroupID: "issues",
	Short:   "Assign an issue (shortcut for 'update --assignee')",
	Args:    cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("assign")
		issueID := args[0]
		assignee := args[1]

		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("assigning: %v", err)
		}
		ctx := rootCtx

		fullID, err := utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		if err := store.UpdateIssue(ctx, fullID, map[string]interface{}{
			"assignee": assignee,
		}, actor); err != nil {
			FatalErrorRespectJSON("assigning %s: %v", fullID, err)
		}

		if !jsonOutput {
			fmt.Printf("Assigned %s to %s\n", fullID, assignee)
		}
	},
}

var tagAliasCmd = &cobra.Command{
	Use:     "tag [issue-id] [label]",
	GroupID: "issues",
	Short:   "Add a label to an issue (shortcut for 'update --add-label')",
	Args:    cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("tag")
		issueID := args[0]
		label := args[1]

		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("tagging: %v", err)
		}
		ctx := rootCtx

		fullID, err := utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		if err := store.AddLabel(ctx, fullID, label, actor); err != nil {
			FatalErrorRespectJSON("adding label to %s: %v", fullID, err)
		}

		if !jsonOutput {
			fmt.Printf("Added label '%s' to %s\n", label, fullID)
		}
	},
}

var noteAliasCmd = &cobra.Command{
	Use:     "note [issue-id] [text]",
	GroupID: "issues",
	Short:   "Set notes on an issue (shortcut for 'update --notes')",
	Args:    cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("note")
		issueID := args[0]
		noteText := strings.Join(args[1:], " ")

		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("setting notes: %v", err)
		}
		ctx := rootCtx

		fullID, err := utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		if err := store.UpdateIssue(ctx, fullID, map[string]interface{}{
			"notes": noteText,
		}, actor); err != nil {
			FatalErrorRespectJSON("setting notes on %s: %v", fullID, err)
		}

		if !jsonOutput {
			fmt.Printf("Notes updated on %s\n", fullID)
		}
	},
}

var priorityAliasCmd = &cobra.Command{
	Use:     "priority [issue-id] [level]",
	GroupID: "issues",
	Short:   "Set priority on an issue (shortcut for 'update --priority')",
	Args:    cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("priority")
		issueID := args[0]
		priorityStr := args[1]

		priority, err := strconv.Atoi(priorityStr)
		if err != nil {
			FatalErrorRespectJSON("invalid priority %q: must be a number (0-4)", priorityStr)
		}

		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("setting priority: %v", err)
		}
		ctx := rootCtx

		fullID, err := utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		if err := store.UpdateIssue(ctx, fullID, map[string]interface{}{
			"priority": priority,
		}, actor); err != nil {
			FatalErrorRespectJSON("setting priority on %s: %v", fullID, err)
		}

		if !jsonOutput {
			fmt.Printf("Priority set to P%d on %s\n", priority, fullID)
		}
	},
}

func init() {
	commentAliasCmd.ValidArgsFunction = issueIDCompletion
	assignAliasCmd.ValidArgsFunction = issueIDCompletion
	tagAliasCmd.ValidArgsFunction = issueIDCompletion
	noteAliasCmd.ValidArgsFunction = issueIDCompletion
	priorityAliasCmd.ValidArgsFunction = issueIDCompletion

	rootCmd.AddCommand(commentAliasCmd)
	rootCmd.AddCommand(assignAliasCmd)
	rootCmd.AddCommand(tagAliasCmd)
	rootCmd.AddCommand(noteAliasCmd)
	rootCmd.AddCommand(priorityAliasCmd)
}
