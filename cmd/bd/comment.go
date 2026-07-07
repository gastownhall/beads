package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

var commentCmd = &cobra.Command{
	Use:     "comment <id> [text...]",
	GroupID: "issues",
	Short:   "Add a comment to an issue",
	Long: `Add a comment to an issue.

Shorthand for 'bd comments add <id> "text"'.

Examples:
  bd comment bd-123 "Working on this now"
  bd comment bd-123 Working on this now
  echo "comment from pipe" | bd comment bd-123 --stdin
  bd comment bd-123 --file notes.txt`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("comment")

		evt := metrics.NewCommandEvent("comment")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		id := args[0]
		textArgs := args[1:]

		stdinFlag, _ := cmd.Flags().GetBool("stdin")
		fileFlag, _ := cmd.Flags().GetString("file")

		var commentText string
		switch {
		case stdinFlag:
			content, err := io.ReadAll(os.Stdin)
			if err != nil {
				return HandleErrorRespectJSON("reading from stdin: %v", err)
			}
			commentText = strings.TrimRight(string(content), "\n")
		case fileFlag != "":
			content, err := readBodyFile(fileFlag)
			if err != nil {
				return HandleErrorRespectJSON("reading file: %v", err)
			}
			commentText = content
		case len(textArgs) > 0:
			commentText = strings.Join(textArgs, " ")
		default:
			return HandleErrorRespectJSON("no comment text provided (use positional args, --stdin, or --file)")
		}

		if strings.TrimSpace(commentText) == "" {
			return HandleErrorRespectJSON("comment text cannot be empty")
		}

		author := getActorWithGit()

		ctx := rootCtx

		result, err := resolveAndGetIssueForMutation(ctx, store, id)
		if err != nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("resolving %s: %v", id, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue %s not found", id)
		}
		defer result.Close()

		issueStore := result.Store

		if err := validateIssueUpdatable(id, result.Issue); err != nil {
			return HandleErrorRespectJSON("%s", err)
		}

		comment, err := issueStore.AddIssueComment(ctx, result.ResolvedID, author, commentText)
		if err != nil {
			return HandleErrorRespectJSON("adding comment: %v", err)
		}
		if err := commitPendingIfEmbedded(ctx, issueStore, actor, doltAutoCommitParams{
			Command:  "comment",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(result.ResolvedID)

		if jsonOutput {
			return outputJSON(comment)
		}
		fmt.Printf("%s Comment added to %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, result.Issue.Title))
		return nil
	},
}

func init() {
	commentCmd.Flags().Bool("stdin", false, "Read comment text from stdin")
	commentCmd.Flags().String("file", "", "Read comment text from file")
	commentCmd.MarkFlagsMutuallyExclusive("stdin", "file")
	commentCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(commentCmd)
}
