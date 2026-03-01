package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var closeCmd = &cobra.Command{
	Use:     "close [id...]",
	GroupID: "issues",
	Short:   "Close one or more issues",
	Long: `Close one or more issues.

If no issue ID is provided, closes the last touched issue (from most recent
create, update, show, or close operation).`,
	Args: cobra.MinimumNArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("close")

		// If no IDs provided, use last touched issue
		if len(args) == 0 {
			lastTouched := GetLastTouchedID()
			if lastTouched == "" {
				FatalErrorRespectJSON("no issue ID provided and no last touched issue")
			}
			args = []string{lastTouched}
		}
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			// Check --resolution alias (Jira CLI convention)
			reason, _ = cmd.Flags().GetString("resolution")
		}
		if reason == "" {
			// Check -m alias (git commit convention)
			reason, _ = cmd.Flags().GetString("message")
		}
		if reason == "" {
			// Check --comment alias (desire-path from hq-ftpg)
			reason, _ = cmd.Flags().GetString("comment")
		}
		if reason == "" {
			reason = "Closed"
		}
		force, _ := cmd.Flags().GetBool("force")
		continueFlag, _ := cmd.Flags().GetBool("continue")
		noAuto, _ := cmd.Flags().GetBool("no-auto")
		suggestNext, _ := cmd.Flags().GetBool("suggest-next")

		// Get session ID from flag or environment variable
		session, _ := cmd.Flags().GetString("session")
		if session == "" {
			session = os.Getenv("CLAUDE_SESSION_ID")
		}

		ctx := rootCtx

		// --continue only works with a single issue
		if continueFlag && len(args) > 1 {
			FatalErrorRespectJSON("--continue only works when closing a single issue")
		}

		// --suggest-next only works with a single issue
		if suggestNext && len(args) > 1 {
			FatalErrorRespectJSON("--suggest-next only works when closing a single issue")
		}

		// Resolve partial IDs first, handling cross-rig routing
		var resolvedIDs []string
		var routedArgs []string // IDs that need cross-repo routing (bypass daemon)
		// Direct mode - check routing for each ID
		for _, id := range args {
			if needsRouting(id) {
				routedArgs = append(routedArgs, id)
			} else {
				resolved, err := utils.ResolvePartialID(ctx, store, id)
				if err != nil {
					FatalErrorRespectJSON("resolving ID %s: %v", id, err)
				}
				resolvedIDs = append(resolvedIDs, resolved)
			}
		}

		// Direct mode
		closedIssues := []*types.Issue{}
		closedCount := 0

		// Handle local IDs
		for _, id := range resolvedIDs {
			// Get issue for checks (nil issue is handled by validateIssueClosable)
			issue, _ := store.GetIssue(ctx, id)

			if err := validateIssueClosable(id, issue, force); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			// Check gate satisfaction for machine-checkable gates (GH#1467)
			if !force {
				if err := checkGateSatisfaction(issue); err != nil {
					fmt.Fprintf(os.Stderr, "cannot close %s: %s\n", id, err)
					continue
				}
			}

			// Check if issue has open blockers (GH#962)
			if !force {
				blocked, blockers, err := store.IsBlocked(ctx, id)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error checking blockers for %s: %v\n", id, err)
					continue
				}
				if blocked && len(blockers) > 0 {
					fmt.Fprintf(os.Stderr, "cannot close %s: blocked by open issues %v (use --force to override)\n", id, blockers)
					continue
				}
			}

			if err := store.CloseIssue(ctx, id, reason, actor, session); err != nil {
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
				continue
			}

			closedCount++

			// Run close hook (best effort: hook runs only if re-fetch succeeds)
			closedIssue, _ := store.GetIssue(ctx, id)
			if closedIssue != nil && hookRunner != nil {
				hookRunner.Run(hooks.EventClose, closedIssue)
			}

			if jsonOutput {
				if closedIssue != nil {
					closedIssues = append(closedIssues, closedIssue)
				}
			} else {
				fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), formatFeedbackID(id, issueTitleOrEmpty(issue)), reason)
			}
		}

		// Handle routed IDs (cross-rig)
		for _, id := range routedArgs {
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}
			if result == nil || result.Issue == nil {
				if result != nil {
					result.Close()
				}
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				continue
			}

			if err := validateIssueClosable(result.ResolvedID, result.Issue, force); err != nil {
				result.Close()
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			// Check gate satisfaction for machine-checkable gates (GH#1467)
			if !force {
				if err := checkGateSatisfaction(result.Issue); err != nil {
					result.Close()
					fmt.Fprintf(os.Stderr, "cannot close %s: %s\n", id, err)
					continue
				}
			}

			// Check if issue has open blockers (GH#962)
			if !force {
				blocked, blockers, err := result.Store.IsBlocked(ctx, result.ResolvedID)
				if err != nil {
					result.Close()
					fmt.Fprintf(os.Stderr, "Error checking blockers for %s: %v\n", id, err)
					continue
				}
				if blocked && len(blockers) > 0 {
					result.Close()
					fmt.Fprintf(os.Stderr, "cannot close %s: blocked by open issues %v (use --force to override)\n", id, blockers)
					continue
				}
			}

			if err := result.Store.CloseIssue(ctx, result.ResolvedID, reason, actor, session); err != nil {
				result.Close()
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
				continue
			}

			closedCount++

			// Get updated issue for hook (best effort: hook runs only if re-fetch succeeds)
			closedIssue, _ := result.Store.GetIssue(ctx, result.ResolvedID)
			if closedIssue != nil && hookRunner != nil {
				hookRunner.Run(hooks.EventClose, closedIssue)
			}

			if jsonOutput {
				if closedIssue != nil {
					closedIssues = append(closedIssues, closedIssue)
				}
			} else {
				fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), formatFeedbackID(result.ResolvedID, result.Issue.Title), reason)
			}
			result.Close()
		}

		// Handle --suggest-next flag in direct mode
		if suggestNext && len(resolvedIDs) == 1 && closedCount > 0 {
			unblocked, err := store.GetNewlyUnblockedByClose(ctx, resolvedIDs[0])
			if err == nil && len(unblocked) > 0 {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"closed":    closedIssues,
						"unblocked": unblocked,
					})
					return
				}
				fmt.Printf("\nNewly unblocked:\n")
				for _, issue := range unblocked {
					fmt.Printf("  • %s (P%d)\n", formatFeedbackID(issue.ID, issue.Title), issue.Priority)
				}
			}
		}

		// Handle --continue flag
		if continueFlag && len(resolvedIDs) == 1 && closedCount > 0 {
			autoClaim := !noAuto
			result, err := AdvanceToNextStep(ctx, store, resolvedIDs[0], autoClaim, actor)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not advance to next step: %v\n", err)
			} else if result != nil {
				if jsonOutput {
					// Include continue result in JSON output
					outputJSON(map[string]interface{}{
						"closed":   closedIssues,
						"continue": result,
					})
					return
				}
				PrintContinueResult(result)
			}
		}

		if jsonOutput && len(closedIssues) > 0 {
			outputJSON(closedIssues)
		}

		// Exit non-zero if no issues were actually closed (close guard
		// and other soft failures should surface as non-zero exit codes for scripting)
		totalAttempted := len(resolvedIDs) + len(routedArgs)
		if totalAttempted > 0 && closedCount == 0 {
			os.Exit(1)
		}
	},
}

func init() {
	closeCmd.Flags().StringP("reason", "r", "", "Reason for closing")
	closeCmd.Flags().String("resolution", "", "Alias for --reason (Jira CLI convention)")
	_ = closeCmd.Flags().MarkHidden("resolution") // Hidden alias for agent/CLI ergonomics
	closeCmd.Flags().StringP("message", "m", "", "Alias for --reason (git commit convention)")
	_ = closeCmd.Flags().MarkHidden("message") // Hidden alias for agent/CLI ergonomics
	closeCmd.Flags().String("comment", "", "Alias for --reason")
	_ = closeCmd.Flags().MarkHidden("comment") // Hidden alias for agent/CLI ergonomics
	closeCmd.Flags().BoolP("force", "f", false, "Force close pinned issues or unsatisfied gates")
	closeCmd.Flags().Bool("continue", false, "Auto-advance to next step in molecule")
	closeCmd.Flags().Bool("no-auto", false, "With --continue, show next step but don't claim it")
	closeCmd.Flags().Bool("suggest-next", false, "Show newly unblocked issues after closing")
	closeCmd.Flags().String("session", "", "Claude Code session ID (or set CLAUDE_SESSION_ID env var)")
	closeCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(closeCmd)
}

// isMachineCheckableGate returns true if the issue is a gate with a machine-checkable await type.
func isMachineCheckableGate(issue *types.Issue) bool {
	if issue == nil || issue.IssueType != "gate" {
		return false
	}
	switch {
	case strings.HasPrefix(issue.AwaitType, "gh:pr"):
		return true
	case strings.HasPrefix(issue.AwaitType, "gh:run"):
		return true
	case issue.AwaitType == "timer":
		return true
	case issue.AwaitType == "bead":
		return true
	default:
		return false
	}
}

// checkGateSatisfaction checks whether a gate issue's condition is satisfied.
// Returns nil if the gate is satisfied (or not a machine-checkable gate), or an error describing why it cannot be closed.
func checkGateSatisfaction(issue *types.Issue) error {
	if !isMachineCheckableGate(issue) {
		return nil
	}

	var resolved bool
	var escalated bool
	var reason string
	var err error

	switch {
	case strings.HasPrefix(issue.AwaitType, "gh:run"):
		resolved, escalated, reason, err = checkGHRun(issue)
	case strings.HasPrefix(issue.AwaitType, "gh:pr"):
		resolved, escalated, reason, err = checkGHPR(issue)
	case issue.AwaitType == "timer":
		resolved, escalated, reason, err = checkTimer(issue, time.Now())
	case issue.AwaitType == "bead":
		resolved, reason = checkBeadGate(rootCtx, issue.AwaitID)
		if resolved {
			return nil
		}
		return fmt.Errorf("gate condition not satisfied: %s (use --force to override)", reason)
	}

	if err != nil {
		// If we can't check the condition, allow close with a warning
		fmt.Fprintf(os.Stderr, "Warning: could not evaluate gate condition: %v\n", err)
		return nil
	}

	if resolved {
		return nil
	}

	if escalated {
		return fmt.Errorf("gate condition not satisfied: %s (use --force to override)", reason)
	}

	return fmt.Errorf("gate condition not satisfied: %s (use --force to override)", reason)
}
