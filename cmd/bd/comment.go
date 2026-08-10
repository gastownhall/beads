package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
)

// validateCommentArgs runs as cobra's Args validation for the singular
// "comment" shorthand, before RunE's usesProxiedServer() dispatch and (on
// the local/embedded path) before the id ever reaches ResolvePartialID's
// fuzzy/substring matching. "comment" has no subcommands of its own — its
// only job is "add a comment to <id>" — so when the id positional argument
// is exactly a word that IS a real subcommand on the plural sibling ("list",
// "add"), the near-certain explanation is that the caller confused the
// singular and plural forms, not that a bead is genuinely named "list" or
// "add" (real ids always carry a prefix+hyphen — see looksLikePrefixedID).
// Left unguarded, that word silently resolves via ResolvePartialID's
// substring/prefix fallback to whatever existing bead or wisp id happens to
// contain it, and the comment lands on the WRONG issue with no error.
func validateCommentArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.MinimumNArgs(1)(cmd, args); err != nil {
		return err
	}
	switch args[0] {
	case "list":
		return HandleErrorRespectJSON(`"bd comment list ..." is not valid — "comment" (singular) takes an issue id first and has no "list" subcommand.

To list comments on an issue:
  bd comments <issue-id>

To add a comment:
  bd comment <issue-id> "text"

See: bd comment --help`)
	case "add":
		return HandleErrorRespectJSON(`"bd comment add ..." is not valid — "comment" (singular) already means "add a comment" and takes an issue id first, not the word "add".

To add a comment:
  bd comment <issue-id> "text"

("add" is only a subcommand of the plural form: bd comments add <issue-id> "text".)

See: bd comment --help`)
	}
	return nil
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
  bd comment bd-123 --file notes.txt

Note: "comment" (singular) only adds a comment — it has no "list" subcommand.
To list comments on an issue, use the plural form: bd comments <id>`,
	Args:          validateCommentArgs,
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

		commentText, err := requireTextFromSources("comment text", "use positional args, --stdin, or --file",
			cmdTextSources(cmd, textArgs))
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		author := getActorWithGit()

		// Dispatched after the text is resolved so both backends read the
		// same sources and report the same conflicts.
		if usesProxiedServer() {
			return runCommentProxiedServer(rootCtx, id, author, commentText)
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
	registerTextSourceFlags(commentCmd, "comment text")
	commentCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(commentCmd)
}
