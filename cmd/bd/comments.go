package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var commentsCmd = &cobra.Command{
	Use:     "comments [issue-id]",
	GroupID: "issues",
	Short:   "View or manage comments on an issue",
	Long: `View or manage comments on an issue.

Examples:
  # List all comments on an issue (issue id is required — there is no "comments list")
  bd comments bd-123

  # List comments in JSON format
  bd comments bd-123 --json

  # Add a comment
  bd comments add bd-123 "This is a comment"

  # Add a comment from a file
  bd comments add bd-123 -f notes.txt`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		localTime, _ := cmd.Flags().GetBool("local-time")
		issueID := args[0]

		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("getting comments: %v", err)
		}
		ctx := rootCtx

		result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
		if err != nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("issue %s not found", issueID)
		}
		defer result.Close()
		issueID = result.ResolvedID

		comments, err := result.Store.GetIssueComments(ctx, issueID)
		if err != nil {
			FatalErrorRespectJSON("getting comments: %v", err)
		}

		// Normalize nil to empty slice for consistent JSON output
		if comments == nil {
			comments = make([]*types.Comment, 0)
		}

		if jsonOutput {
			outputJSON(comments)
			return
		}

		// Human-readable output
		if len(comments) == 0 {
			fmt.Printf("No comments on %s\n", issueID)
			return
		}

		fmt.Printf("\nComments on %s:\n\n", issueID)
		for _, comment := range comments {
			ts := comment.CreatedAt
			if localTime {
				ts = ts.Local()
			}
			fmt.Printf("[%s] at %s\n", comment.Author, ts.Format("2006-01-02 15:04"))
			rendered := ui.RenderMarkdown(comment.Text)
			// TrimRight removes trailing newlines that Glamour adds, preventing extra blank lines
			for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
				fmt.Printf("  %s\n", line)
			}
			fmt.Println()
		}
	},
}

// commentsMisplacedListCmd catches the reflexive "bd comments list" invocation (GH#3542).
// Listing comments always requires an issue id: bd comments <issue-id>.
var commentsMisplacedListCmd = &cobra.Command{
	Use:   "list",
	Short: "Invalid — use bd comments <issue-id> to list comments",
	Run: func(cmd *cobra.Command, args []string) {
		FatalErrorRespectJSON(`"bd comments list" is not valid.

To list comments on an issue, run:
  bd comments <issue-id>

Example:
  bd comments bd-123

See: bd comments --help`)
	},
}

var commentsAddCmd = &cobra.Command{
	Use:   "add [issue-id] [text]",
	Short: "Add a comment to an issue",
	Long: `Add a comment to an issue.

Examples:
  # Add a comment
  bd comments add bd-123 "Working on this now"

  # Add a comment from a file
  bd comments add bd-123 -f notes.txt`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("comment add")
		issueID := args[0]

		// Get comment text from flag or argument
		commentText, _ := cmd.Flags().GetString("file")
		if commentText != "" {
			// Read from file
			data, err := os.ReadFile(commentText) // #nosec G304 - user-provided file path is intentional
			if err != nil {
				FatalErrorRespectJSON("reading file: %v", err)
			}
			commentText = string(data)
		} else if len(args) < 2 {
			FatalErrorRespectJSON("comment text required (use -f to read from file)")
		} else {
			commentText = args[1]
		}

		if strings.TrimSpace(commentText) == "" {
			FatalErrorRespectJSON("comment text cannot be empty")
		}

		// Get author from author flag, or use git-aware default
		author, _ := cmd.Flags().GetString("author")
		if author == "" {
			author = getActorWithGit()
		}

		if err := ensureStoreActive(); err != nil {
			FatalErrorRespectJSON("adding comment: %v", err)
		}
		ctx := rootCtx

		result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
		if err != nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("issue %s not found", issueID)
		}
		defer result.Close()
		issueID = result.ResolvedID

		comment, err := result.Store.AddIssueComment(ctx, issueID, author, commentText)
		if err != nil {
			FatalErrorRespectJSON("adding comment: %v", err)
		}

		commandDidWrite.Store(true)

		if jsonOutput {
			outputJSON(comment)
			return
		}

		fmt.Printf("Comment added to %s\n", issueID)
	},
}

func init() {
	commentsCmd.AddCommand(commentsMisplacedListCmd)
	commentsCmd.AddCommand(commentsAddCmd)
	commentsCmd.Flags().Bool("local-time", false, "Show timestamps in local time instead of UTC")
	commentsAddCmd.Flags().StringP("file", "f", "", "Read comment text from file")
	commentsAddCmd.Flags().StringP("author", "a", "", "Add author to comment")

	// Issue ID completions
	commentsCmd.ValidArgsFunction = issueIDCompletion
	commentsAddCmd.ValidArgsFunction = issueIDCompletion

	rootCmd.AddCommand(commentsCmd)
}

func isUnknownOperationError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "unknown operation")
}
