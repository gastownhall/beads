package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
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
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("comment")

		id := args[0]
		textArgs := args[1:]

		// Determine comment text from args, stdin, or file
		stdinFlag, _ := cmd.Flags().GetBool("stdin")
		fileFlag, _ := cmd.Flags().GetString("file")

		var commentText string
		switch {
		case stdinFlag:
			content, err := io.ReadAll(os.Stdin)
			if err != nil {
				FatalErrorRespectJSON("reading from stdin: %v", err)
			}
			commentText = strings.TrimRight(string(content), "\n")
		case fileFlag != "":
			content, err := readBodyFile(fileFlag)
			if err != nil {
				FatalErrorRespectJSON("reading file: %v", err)
			}
			commentText = content
		case len(textArgs) > 0:
			commentText = strings.Join(textArgs, " ")
		default:
			FatalErrorRespectJSON("no comment text provided (use positional args, --stdin, or --file)")
		}

		if strings.TrimSpace(commentText) == "" {
			FatalErrorRespectJSON("comment text cannot be empty")
		}

		author := getActorWithGit()

		ctx := rootCtx

		result, err := resolveAndGetIssueWithRouting(ctx, store, id)
		if err != nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("resolving %s: %v", id, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			FatalErrorRespectJSON("issue %s not found", id)
		}
		defer result.Close()

		issueStore := result.Store

		if err := validateIssueUpdatable(id, result.Issue); err != nil {
			FatalErrorRespectJSON("%s", err)
		}

		comment, err := issueStore.AddIssueComment(ctx, result.ResolvedID, author, commentText)
		if err != nil {
			FatalErrorRespectJSON("adding comment: %v", err)
		}

		// Flush Dolt commit for embedded mode
		if isEmbeddedDolt && store != nil {
			if _, err := store.CommitPending(ctx, actor); err != nil {
				FatalErrorRespectJSON("failed to commit: %v", err)
			}
		}

		SetLastTouchedID(result.ResolvedID)

		if jsonOutput {
			outputJSON(comment)
		} else {
			fmt.Printf("%s Comment added to %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, result.Issue.Title))
		}
	},
}

func init() {
	commentCmd.Flags().Bool("stdin", false, "Read comment text from stdin")
	commentCmd.Flags().String("file", "", "Read comment text from file")
	commentCmd.MarkFlagsMutuallyExclusive("stdin", "file")
	commentCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(commentCmd)
}
