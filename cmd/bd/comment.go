package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
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
	// "list" and "add" are genuinely misplaced "bd comments" subcommands, but
	// "rm" and "delete" are not — there is no "bd comments rm"/"bd comments
	// delete" (they read as bd's own delete command, or "dep rm"'s pattern,
	// used in the wrong place). The message below must hold for all four, so
	// it says "reserved word", never "misplaced bd comments subcommand".
	return HandleErrorRespectJSON(`%q is not a valid issue id — bd reserves it as a command/subcommand word (a real id never collides with one), so it is refused as an id instead of silently resolved as one.

To comment on an issue:
  bd comment <issue-id> "text"
  bd comments add <issue-id> "text"

To list comments:
  bd comments <issue-id>

See: bd comment --help`, id)
}

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
	// The two cases above carry hand-written messages for the two typos that
	// were actually reported, both real "bd comments" subcommands. The
	// remaining reserved words ("rm", "delete") are not "bd comments"
	// subcommands — they collide with words bd uses elsewhere ("bd delete",
	// "dep rm") — so they get checkCommentIDNotReservedWord's word-agnostic
	// generic message instead of a claim that would be false for them.
	// Keeping the whole set in commentReservedIDWords also keeps the check
	// unit-testable on its own.
	return checkCommentIDNotReservedWord(args[0])
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

		result, err := resolveAndGetIssueForMutationExact(ctx, store, id)
		if err != nil {
			if result != nil {
				result.Close()
			}
			if errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
				// The issue does exist — id just isn't its full form — so
				// "resolving %s: %v"'s generic wording (and the "no issue
				// found matching" text underneath a plain not-found) would be
				// false here. Say what actually happened instead.
				return HandleErrorRespectJSON("id abbreviations are not accepted on comment writes; use the full id from `bd show %s`", id)
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
