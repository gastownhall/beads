package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
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
		if daemonClient != nil {
			for _, id := range args {
				// Check if this ID needs routing to a different beads directory
				if needsRouting(id) {
					routedArgs = append(routedArgs, id)
					continue
				}
				resolveArgs := &rpc.ResolveIDArgs{ID: id}
				resp, err := daemonClient.ResolveID(resolveArgs)
				if err != nil {
					FatalErrorRespectJSON("resolving ID %s: %v", id, err)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					FatalErrorRespectJSON("unmarshaling resolved ID: %v", err)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		} else {
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
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			closedIssues := []*types.Issue{}
			for _, id := range resolvedIDs {
				// Get issue for template and pinned checks
				showArgs := &rpc.ShowArgs{ID: id}
				showResp, showErr := daemonClient.Show(showArgs)
				if showErr == nil {
					var issue types.Issue
					if json.Unmarshal(showResp.Data, &issue) == nil {
						if err := validateIssueClosable(id, &issue, force); err != nil {
							fmt.Fprintf(os.Stderr, "%s\n", err)
							continue
						}
					}
				}

				closeArgs := &rpc.CloseArgs{
					ID:          id,
					Reason:      reason,
					Session:     session,
					SuggestNext: suggestNext,
				}
				resp, err := daemonClient.CloseIssue(closeArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
					continue
				}

				// Handle response based on whether SuggestNext was requested
				if suggestNext {
					var result rpc.CloseResult
					if err := json.Unmarshal(resp.Data, &result); err == nil {
						if result.Closed != nil {
							// Run close hook
							if hookRunner != nil {
								hookRunner.Run(hooks.EventClose, result.Closed)
							}
							if jsonOutput {
								closedIssues = append(closedIssues, result.Closed)
							}
						}
						if !jsonOutput {
							fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), id, reason)
							// Display newly unblocked issues
							if len(result.Unblocked) > 0 {
								fmt.Printf("\nNewly unblocked:\n")
								for _, issue := range result.Unblocked {
									fmt.Printf("  • %s %q (P%d)\n", issue.ID, issue.Title, issue.Priority)
								}
							}
						}
					}
				} else {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						// Run close hook
						if hookRunner != nil {
							hookRunner.Run(hooks.EventClose, &issue)
						}
						if jsonOutput {
							closedIssues = append(closedIssues, &issue)
						}
					}
					if !jsonOutput {
						fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), id, reason)
					}
				}
			}

			// Handle routed IDs via direct mode (cross-rig)
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

				if err := result.Store.CloseIssue(ctx, result.ResolvedID, reason, actor, session); err != nil {
					result.Close()
					fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
					continue
				}

				// Get updated issue for hook
				closedIssue, _ := result.Store.GetIssue(ctx, result.ResolvedID)
				if closedIssue != nil && hookRunner != nil {
					hookRunner.Run(hooks.EventClose, closedIssue)
				}

				if jsonOutput {
					if closedIssue != nil {
						closedIssues = append(closedIssues, closedIssue)
					}
				} else {
					fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), result.ResolvedID, reason)
				}
				result.Close()
			}

			// Handle --continue flag in daemon mode
			// Note: --continue requires direct database access to walk parent-child chain
			if continueFlag && len(closedIssues) > 0 {
				fmt.Fprintf(os.Stderr, "\nNote: --continue requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon close %s --continue\n", resolvedIDs[0])
			}

			if jsonOutput && len(closedIssues) > 0 {
				outputJSON(closedIssues)
			}
			return
		}

		// Direct mode
		closedIssues := []*types.Issue{}
		closedCount := 0

		// Handle local IDs
		for _, id := range resolvedIDs {
			// Get issue for checks
			issue, _ := store.GetIssue(ctx, id)

			if err := validateIssueClosable(id, issue, force); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			if err := store.CloseIssue(ctx, id, reason, actor, session); err != nil {
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
				continue
			}

			closedCount++

			// Run close hook
			closedIssue, _ := store.GetIssue(ctx, id)
			if closedIssue != nil && hookRunner != nil {
				hookRunner.Run(hooks.EventClose, closedIssue)
			}

			if jsonOutput {
				if closedIssue != nil {
					closedIssues = append(closedIssues, closedIssue)
				}
			} else {
				fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), id, reason)
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

			if err := result.Store.CloseIssue(ctx, result.ResolvedID, reason, actor, session); err != nil {
				result.Close()
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
				continue
			}

			closedCount++

			// Get updated issue for hook
			closedIssue, _ := result.Store.GetIssue(ctx, result.ResolvedID)
			if closedIssue != nil && hookRunner != nil {
				hookRunner.Run(hooks.EventClose, closedIssue)
			}

			if jsonOutput {
				if closedIssue != nil {
					closedIssues = append(closedIssues, closedIssue)
				}
			} else {
				fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), result.ResolvedID, reason)
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
					fmt.Printf("  • %s %q (P%d)\n", issue.ID, issue.Title, issue.Priority)
				}
			}
		}

		// Schedule auto-flush if any issues were closed
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
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
	},
}

func init() {
	closeCmd.Flags().StringP("reason", "r", "", "Reason for closing")
	closeCmd.Flags().String("resolution", "", "Alias for --reason (Jira CLI convention)")
	_ = closeCmd.Flags().MarkHidden("resolution") // Hidden alias for agent/CLI ergonomics
	closeCmd.Flags().BoolP("force", "f", false, "Force close pinned issues")
	closeCmd.Flags().Bool("continue", false, "Auto-advance to next step in molecule")
	closeCmd.Flags().Bool("no-auto", false, "With --continue, show next step but don't claim it")
	closeCmd.Flags().Bool("suggest-next", false, "Show newly unblocked issues after closing")
	closeCmd.Flags().String("session", "", "Claude Code session ID (or set CLAUDE_SESSION_ID env var)")
	rootCmd.AddCommand(closeCmd)
}
