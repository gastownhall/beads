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

var noteCmd = &cobra.Command{
	Use:     "note <id> [text...]",
	GroupID: "issues",
	Short:   "Append a note to an issue",
	Long: `Append a note to an issue's notes field.

Shorthand for 'bd update <id> --append-notes "text"'.

Examples:
  bd note gt-abc "Fixed the flaky test"
  bd note gt-abc Fixed the flaky test
  echo "note from pipe" | bd note gt-abc --stdin
  bd note gt-abc --file notes.txt`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("note")

		evt := metrics.NewCommandEvent("note")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		id := args[0]
		textArgs := args[1:]

		stdinFlag, _ := cmd.Flags().GetBool("stdin")
		fileFlag, _ := cmd.Flags().GetString("file")

		var noteText string
		switch {
		case stdinFlag:
			content, err := io.ReadAll(os.Stdin)
			if err != nil {
				return HandleErrorRespectJSON("reading from stdin: %v", err)
			}
			noteText = strings.TrimRight(string(content), "\n")
		case fileFlag != "":
			content, err := readBodyFile(fileFlag)
			if err != nil {
				return HandleErrorRespectJSON("reading file: %v", err)
			}
			noteText = content
		case len(textArgs) > 0:
			noteText = strings.Join(textArgs, " ")
		default:
			return HandleErrorRespectJSON("no note text provided (use positional args, --stdin, or --file)")
		}

		if noteText == "" {
			return HandleErrorRespectJSON("note text is empty")
		}

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

		issue := result.Issue
		issueStore := result.Store

		if err := validateIssueUpdatable(id, issue); err != nil {
			return HandleErrorRespectJSON("%s", err)
		}

		combined := issue.Notes
		if combined != "" {
			combined += "\n"
		}
		combined += noteText

		updates := map[string]interface{}{
			"notes": combined,
		}
		if err := issueStore.UpdateIssue(ctx, result.ResolvedID, updates, actor); err != nil {
			return HandleErrorRespectJSON("updating %s: %v", id, err)
		}
		if err := commitPendingIfEmbedded(ctx, issueStore, actor, doltAutoCommitParams{
			Command:  "note",
			IssueIDs: []string{result.ResolvedID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		SetLastTouchedID(result.ResolvedID)

		updatedIssue, _ := issueStore.GetIssue(ctx, result.ResolvedID)
		title := ""
		if updatedIssue != nil {
			title = updatedIssue.Title
		}
		if jsonOutput {
			if updatedIssue != nil {
				return outputJSON(updatedIssue)
			}
			return nil
		}
		fmt.Printf("%s Note added to %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, title))
		return nil
	},
}

func init() {
	noteCmd.Flags().Bool("stdin", false, "Read note text from stdin")
	noteCmd.Flags().String("file", "", "Read note text from file")
	noteCmd.MarkFlagsMutuallyExclusive("stdin", "file")
	noteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(noteCmd)
}
