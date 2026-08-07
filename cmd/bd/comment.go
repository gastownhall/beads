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

// commentReservedIDWords are "comments" subcommand names that must never be
// silently accepted as the <id> positional of "bd comment" (singular). They
// exist so a typo'd plural form — "bd comment list <id>", meant to be
// "bd comments list" / "bd comments <id>" — fails loudly instead of treating
// "list" as the id and the real id as comment text.
//
// This is not a hypothetical: 15+ automated sessions in one deployment made
// exactly this typo over two days, and because "list" happened to be a
// leading-prefix abbreviation of an unrelated wisp's hash ("list3t0"), each
// one silently wrote a garbage comment onto that wisp instead of erroring.
// The word list mirrors the real "comments" subcommand (add) plus the other
// verbs a "comments <verb>" typo is likely to produce.
var commentReservedIDWords = map[string]bool{
	"list":   true,
	"add":    true,
	"rm":     true,
	"delete": true,
}

// checkCommentIDNotReservedWord rejects an id argument that is one of
// commentReservedIDWords, with a message pointing at the "bd comments"
// subcommand the caller most likely meant. Pure and side-effect free so it
// can run before either the direct or proxied-server RunE branch, and be
// unit tested without a store.
func checkCommentIDNotReservedWord(id string) error {
	if !commentReservedIDWords[id] {
		return nil
	}
	return HandleErrorRespectJSON(`%q is not a valid issue id — it looks like a misplaced "bd comments" subcommand.

To list comments:
  bd comments <issue-id>

To add a comment:
  bd comment <issue-id> "text"
  bd comments add <issue-id> "text"

See: bd comment --help`, id)
}

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

		if err := checkCommentIDNotReservedWord(args[0]); err != nil {
			return err
		}

		if usesProxiedServer() {
			return runCommentProxiedServer(cmd, rootCtx, args)
		}

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

		result, err := resolveAndGetIssueForMutationExact(ctx, store, id)
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

		comment, err := addCommentDirect(ctx, issueStore, result.ResolvedID, author, commentText)
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
