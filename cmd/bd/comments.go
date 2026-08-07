package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/uimd"
	"github.com/steveyegge/beads/issueops"
)

// validateCommentsArgs runs as cobra's Args validation for the parent
// "comments" command, which executes before PersistentPreRunE opens the
// store (or runs migrations) and before RunE's usesProxiedServer() dispatch.
// The parent command only lists comments for a single issue, so any trailing
// positional arg is a mistake — most commonly the swapped-order add,
// `bd comments <id> add <text>`, which otherwise falls through, reads only
// args[0], and silently drops the rest with exit 0 (GH#4642). Validating
// here (rather than inside RunE, local-store-only) rejects the malformed
// invocation identically for the direct and proxied-server paths, before
// either one can open a store, run a migration, or hit the remote-migrate
// gate for an invocation that is guaranteed to fail.
func validateCommentsArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.MinimumNArgs(1)(cmd, args); err != nil {
		return err
	}
	if len(args) <= 1 {
		return nil
	}

	issueID := args[0]
	if args[1] == "add" {
		return HandleErrorRespectJSON(`"bd comments <issue-id> add ..." is not valid — the subcommand must come first.

To add a comment, run:
  bd comments add <issue-id> <text>

Example:
  bd comments add %s "your comment"

See: bd comments --help`, issueID)
	}
	return HandleErrorRespectJSON(`"bd comments" takes a single issue id and lists its comments.

To list comments:
  bd comments <issue-id>

To add a comment:
  bd comments add <issue-id> <text>

See: bd comments --help`)
}

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
	Args:          validateCommentsArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("comments")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runCommentsProxiedServer(cmd, rootCtx, args)
		}

		localTime, _ := cmd.Flags().GetBool("local-time")
		issueID := args[0]

		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("getting comments: %v", err)
		}
		ctx := rootCtx

		result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
		if err != nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue %s not found", issueID)
		}
		defer result.Close()
		issueID = result.ResolvedID

		comments, err := result.Store.GetIssueComments(ctx, issueID)
		if err != nil {
			return HandleErrorRespectJSON("getting comments: %v", err)
		}

		if comments == nil {
			comments = make([]*types.Comment, 0)
		}

		if jsonOutput {
			return outputJSON(comments)
		}

		if len(comments) == 0 {
			fmt.Printf("No comments on %s\n", issueID)
			return nil
		}

		fmt.Printf("\nComments on %s:\n\n", issueID)
		for _, comment := range comments {
			ts := comment.CreatedAt
			if localTime {
				ts = ts.Local()
			}
			fmt.Printf("[%s] at %s\n", comment.Author, ts.Format("2006-01-02 15:04"))
			rendered := uimd.RenderMarkdown(comment.Text)
			for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
				fmt.Printf("  %s\n", line)
			}
			fmt.Println()
		}
		return nil
	},
}

var commentsMisplacedListCmd = &cobra.Command{
	Use:           "list",
	Short:         "Invalid — use bd comments <issue-id> to list comments",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return HandleErrorRespectJSON(`"bd comments list" is not valid.

To list comments on an issue, run:
  bd comments <issue-id>

Example:
  bd comments bd-123

See: bd comments --help`)
	},
}

var commentsAddCmd = &cobra.Command{
	Use:   "add [issue-id] [text...]",
	Short: "Add a comment to an issue",
	Long: `Add a comment to an issue.

Examples:
  # Add a comment
  bd comments add bd-123 "Working on this now"

  # Add a comment from a file
  bd comments add bd-123 -f notes.txt`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("comment add")

		evt := metrics.NewCommandEvent("comments-add")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runCommentsAddProxiedServer(cmd, rootCtx, args)
		}

		issueID := args[0]

		commentText, _ := cmd.Flags().GetString("file")
		if commentText != "" {
			data, err := os.ReadFile(commentText) // #nosec G304 - user-provided file path is intentional
			if err != nil {
				return HandleErrorRespectJSON("reading file: %v", err)
			}
			commentText = string(data)
		} else if len(args) < 2 {
			return HandleErrorRespectJSON("comment text required (use -f to read from file)")
		} else {
			commentText = strings.Join(args[1:], " ")
		}

		if strings.TrimSpace(commentText) == "" {
			return HandleErrorRespectJSON("comment text cannot be empty")
		}

		author, _ := cmd.Flags().GetString("author")
		if author == "" {
			author = getActorWithGit()
		}

		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("adding comment: %v", err)
		}
		ctx := rootCtx

		result, err := resolveAndGetIssueForMutation(ctx, store, issueID)
		if err != nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue %s not found", issueID)
		}
		defer result.Close()
		issueID = result.ResolvedID

		comment, err := addCommentDirect(ctx, result.Store, issueID, author, commentText)
		if err != nil {
			return HandleErrorRespectJSON("adding comment: %v", err)
		}
		if err := commitPendingIfEmbedded(ctx, result.Store, actor, doltAutoCommitParams{
			Command:  "comments add",
			IssueIDs: []string{issueID},
		}); err != nil {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		if jsonOutput {
			return outputJSON(comment)
		}

		fmt.Printf("Comment added to %s\n", issueID)
		return nil
	},
}

// addCommentDirect appends one comment through the Commenter role and returns
// the stored comment. It is the direct-route sibling of addCommentProxied and
// builds the SAME request, so the two front doors ask the storage layer one
// question.
//
// The role is reached through the STORE's own accessor, never a constructor:
// that accessor is where the store's decorator stack — hooks, telemetry — adds
// its layer, so a comment written here fires the same hook a comment written
// through the provider does. The store is the caller's rather than the global
// one, because a prefix-routed write has to reach the role through the store
// that actually holds the issue.
//
// The RESOLVE stays with the caller. On this route it is fuzzy, prefix-routed
// and cross-repo, none of which belongs on a contract an unattended client also
// calls, and the caller has already produced the canonical id. The role runs
// the issue-then-wisp fallback itself, so the comment lands on the plane the id
// actually names.
//
// The auto-commit policy is applied HERE rather than by each caller. The role
// creates its Dolt version commit inside the storage layer, so
// `--dolt-auto-commit batch` can only defer it by saying so on the context —
// the reason issueOpsContext exists — and a caller that forgets writes exactly
// the per-write commit batch mode is there to avoid. Both comment commands come
// through this function, so the policy is stated once.
func addCommentDirect(ctx context.Context, st storage.DoltStorage, issueID, author, text string) (*types.Comment, error) {
	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return nil, err
	}
	commenter, err := st.Commenter()
	if err != nil {
		return nil, err
	}
	result, err := commenter.AddComment(opsCtx, issueops.AddCommentRequest{
		Author:  author,
		IssueID: issueID,
		Text:    text,
	})
	if err != nil {
		return nil, err
	}
	return result.Comment, nil
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
