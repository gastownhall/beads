package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
)

var closeCmd = &cobra.Command{
	Use:     "close [id...]",
	Aliases: []string{"done"},
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

		// Desire-path: "bd done <id> <message>" treats last positional arg as reason
		// when no reason flag was explicitly provided (hq-pe8ce)
		if reason == "" && cmd.CalledAs() == "done" && len(args) >= 2 {
			reason = args[len(args)-1]
			args = args[:len(args)-1]
		}

		if reason == "" {
			reason = "Closed"
		}

		// Validate close reason if configured
		closeValidation := config.GetString("validation.on-close")
		if closeValidation == "error" || closeValidation == "warn" {
			if err := validation.ValidateCloseReason(reason); err != nil {
				if closeValidation == "error" {
					FatalErrorRespectJSON("%v", err)
				}
				// warn mode: print warning but proceed
				fmt.Fprintf(os.Stderr, "%s %v\n", ui.RenderWarn("⚠"), err)
			}
		}

		force, _ := cmd.Flags().GetBool("force")
		continueFlag, _ := cmd.Flags().GetBool("continue")
		noAuto, _ := cmd.Flags().GetBool("no-auto")
		suggestNext, _ := cmd.Flags().GetBool("suggest-next")

		claimNext, _ := cmd.Flags().GetBool("claim-next")

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

		// Resolve partial IDs
		var resolvedIDs []string
		for _, id := range args {
			resolved, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				FatalErrorRespectJSON("resolving ID %s: %v", id, err)
			}
			resolvedIDs = append(resolvedIDs, resolved)
		}

		// Direct mode
		closedIssues := []*types.Issue{}
		closedCount := 0

		for _, id := range resolvedIDs {
			// Get issue for checks (nil issue is handled by validateIssueClosable)
			issue, _ := store.GetIssue(ctx, id)

			if err := validateIssueClosable(id, issue, force); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			// Epic close guard: prevent closing epics with open children (mw-local-4so.5.2)
			if !force && issue != nil && issue.IssueType == types.TypeEpic {
				openChildren := countEpicOpenChildren(ctx, id)
				if openChildren > 0 {
					fmt.Fprintf(os.Stderr, "cannot close epic %s: %d open child issue(s); close children first or use --force to override\n", id, openChildren)
					continue
				}
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

			// Audit log the close (survives Dolt GC flatten)
			oldStatus := "open"
			if issue != nil {
				oldStatus = string(issue.Status)
			}
			audit.LogFieldChange(id, "status", oldStatus, "closed", actor, reason)

			closedCount++

			// Auto-close parent molecule if all steps are now complete
			autoCloseCompletedMolecule(ctx, store, id, actor, session)

			// Run hooks (best effort: hooks run only if re-fetch succeeds)
			closedIssue, _ := store.GetIssue(ctx, id)
			if closedIssue != nil && hookRunner != nil {
				// Fire on_update only if status actually changed (GH#2630)
				if issue == nil || issue.Status != types.StatusClosed {
					hookRunner.Run(hooks.EventUpdate, closedIssue)
				}
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

		// Handle --claim-next flag
		var claimedNextIssue *types.Issue
		if claimNext && closedCount > 0 && !continueFlag {
			readyIssues, err := store.GetReadyWork(ctx, types.WorkFilter{
				Status:     "open",
				Limit:      1,
				SortPolicy: types.SortPolicy("priority"),
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not get ready issues: %v\n", err)
			} else if len(readyIssues) > 0 {
				nextIssue := readyIssues[0]
				err := store.ClaimIssue(ctx, nextIssue.ID, actor)
				if err == nil {
					claimedNextIssue = nextIssue
					if jsonOutput {
						// JSON handled below
					} else {
						fmt.Printf("%s Auto-claimed next ready issue: %s (P%d)\n", ui.RenderPass("✓"), formatFeedbackID(nextIssue.ID, nextIssue.Title), nextIssue.Priority)
					}
					SetLastTouchedID(nextIssue.ID)
				} else {
					fmt.Fprintf(os.Stderr, "Warning: could not claim next issue %s: %v\n", nextIssue.ID, err)
				}
			} else if !jsonOutput {
				fmt.Printf("\n%s No ready issues available to claim.\n", ui.RenderWarn("✨"))
			}
		}

		if jsonOutput && len(closedIssues) > 0 {
			if claimedNextIssue != nil {
				outputJSON(map[string]interface{}{
					"closed":  closedIssues,
					"claimed": claimedNextIssue,
				})
			} else {
				outputJSON(closedIssues)
			}
		}

		// Embedded mode: flush Dolt commit. DoltStore commits
		// inline during CloseIssue so this is only needed for EmbeddedDoltStore.
		if isEmbeddedMode() && closedCount > 0 && store != nil {
			if _, err := store.CommitPending(ctx, actor); err != nil {
				FatalErrorRespectJSON("failed to commit: %v", err)
			}
		}

		// Exit non-zero if no issues were actually closed (close guard
		// and other soft failures should surface as non-zero exit codes for scripting)
		totalAttempted := len(resolvedIDs)
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
	closeCmd.Flags().Bool("claim-next", false, "Automatically claim the next highest priority available issue")
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

// autoCloseCompletedMolecule checks if closing a step completed a parent molecule,
// and if so, auto-closes the molecule root. This prevents stale wisps that are
// complete but never explicitly closed (e.g., deacon patrol wisps).
func autoCloseCompletedMolecule(ctx context.Context, s storage.DoltStorage, closedStepID, actorName, session string) {
	moleculeID := findParentMolecule(ctx, s, closedStepID)
	if moleculeID == "" {
		return // Not part of a molecule
	}

	// Check if molecule root is already closed
	root, err := s.GetIssue(ctx, moleculeID)
	if err != nil || root == nil || root.Status == types.StatusClosed {
		return
	}

	// Load progress to check completion
	progress, err := getMoleculeProgress(ctx, s, moleculeID)
	if err != nil {
		return // Best effort — don't fail the close
	}

	if progress.Completed < progress.Total {
		return // Not all steps complete yet
	}

	// All steps complete — auto-close the molecule root
	if err := s.CloseIssue(ctx, moleculeID, "all steps complete", actorName, session); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not auto-close completed molecule %s: %v\n", moleculeID, err)
		return
	}

	if !jsonOutput {
		fmt.Printf("%s Auto-closed completed molecule %s\n", ui.RenderPass("✓"), formatFeedbackID(moleculeID, root.Title))
	}
}

// countEpicOpenChildren returns the number of open (non-closed) children for an epic.
// Uses GetDependentsWithMetadata to find parent-child relationships.
func countEpicOpenChildren(ctx context.Context, epicID string) int {
	dependents, err := store.GetDependentsWithMetadata(ctx, epicID)
	if err != nil {
		return 0
	}
	count := 0
	for _, dep := range dependents {
		if dep.DependencyType == types.DepParentChild && dep.Issue.Status != types.StatusClosed {
			count++
		}
	}
	return count
}
