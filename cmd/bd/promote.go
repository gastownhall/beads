package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// printPromoteResult renders the `bd promote` success output. updated is the
// best-effort post-promote re-read used only by --json (it may be nil, in
// which case the JSON path prints nothing, matching the classic behavior).
// Shared by the classic and proxied-server paths so the output shape cannot
// drift.
func printPromoteResult(fullID string, updated *types.Issue) error {
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	fmt.Printf("%s Promoted %s to permanent bead\n", ui.RenderPass("✓"), fullID)
	return nil
}

var promoteCmd = &cobra.Command{
	Use:     "promote <wisp-id>",
	GroupID: "issues",
	Short:   "Promote a wisp to a permanent bead",
	Long: `Promote a wisp (ephemeral issue) to a permanent bead.

This copies the issue from the wisps table (dolt_ignored) to the permanent
issues table (Dolt-versioned), preserving labels, dependencies, events, and
comments. The original ID is preserved so all links keep working.

A comment is added recording the promotion and optional reason.

Examples:
  bd promote bd-wisp-abc123
  bd promote bd-wisp-abc123 --reason "Worth tracking long-term"`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("promote")

		evt := metrics.NewCommandEvent("promote")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		id := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		if usesProxiedServer() {
			return runPromoteProxiedServer(rootCtx, id, reason)
		}

		ctx := rootCtx

		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}

		fullID, err := utils.ResolvePartialID(ctx, store, id)
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", id, err)
		}

		promoted, err := promoteDirect(ctx, store, fullID, reason)
		if err != nil {
			return promoteFailure(fullID, err)
		}
		return reportPromotion(fullID, promoted, func() *types.Issue {
			updated, _ := store.GetIssue(ctx, fullID)
			return updated
		})
	},
}

// promoteFailure maps a promotion that did not land to the command's error:
// the typed refusal for a row that is not a wisp, the classic sentence for a
// row that is gone, and the id for anything else. Shared by the direct and
// proxied-server routes, which both run issueops.Promote, so the error
// contract cannot drift.
func promoteFailure(fullID string, err error) error {
	var notWisp *issueops.NotAWispError
	switch {
	case errors.As(err, &notWisp):
		return HandleErrorRespectJSON("%v", err)
	case errors.Is(err, storage.ErrNotFound):
		return HandleErrorRespectJSON("issue %s not found", fullID)
	}
	return HandleErrorRespectJSON("promoting %s: %v", fullID, err)
}

// reportPromotion reports a promotion that landed, for both routes: a comment
// that could not be written is a warning (the move stands, as
// issueops.PromoteResult.CommentErr says), then the success output. reread is
// the best-effort --json re-read, called only under --json.
func reportPromotion(fullID string, promoted issueops.PromoteResult, reread func() *types.Issue) error {
	if promoted.CommentErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to add promotion comment to %s: %v\n", fullID, promoted.CommentErr)
	}

	commandDidWrite.Store(true)

	var updated *types.Issue
	if jsonOutput {
		updated = reread()
	}
	return printPromoteResult(fullID, updated)
}

func init() {
	promoteCmd.Flags().StringP("reason", "r", "", "Reason for promotion")
	promoteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(promoteCmd)
}

// promoteDirect runs `bd promote` on the direct route through
// issueops.Promote, the one library entry every route shares (the
// proxied-server route runs it over the provider's roles,
// runPromoteProxiedServer), over the store's own role accessors (so its
// decorator stack — hooks, telemetry — wraps the move and the comment). On a
// connected http workspace the same entry dials updateIssue with
// `patch.persistence`, and a server without the issues.update.persistence
// capability refuses before the dial, naming it.
//
// The history label is the classic "bd: promote <id>" where the store can
// carry a caller label, and the server's own where it cannot (S6d's rule,
// remote_backend.go).
func promoteDirect(ctx context.Context, st storage.DoltStorage, fullID, reason string) (issueops.PromoteResult, error) {
	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return issueops.PromoteResult{}, err
	}
	reader, err := st.IssueReader()
	if err != nil {
		return issueops.PromoteResult{}, err
	}
	lifecycle, err := writeOps(st)
	if err != nil {
		return issueops.PromoteResult{}, err
	}
	commenter, err := st.Commenter()
	if err != nil {
		return issueops.PromoteResult{}, err
	}
	provenance := ""
	if storeCarriesCallerAttribution(st) {
		provenance = "bd: promote " + fullID
	}
	return issueops.Promote(opsCtx, issueops.PromoteRoles{
		Reader: reader, Lifecycle: lifecycle, Commenter: commenter,
	}, issueops.PromoteRequest{
		Actor: currentActor(), IssueID: fullID, Reason: reason, Provenance: provenance,
	})
}
