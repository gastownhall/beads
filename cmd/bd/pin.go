package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// pinCmd attaches a mol to an agent's hook (work assignment)
//
// In the molecular chemistry metaphor:
//   - Hook: Agent's attachment point for work
//   - Pin: Action of attaching a mol to an agent's hook
var pinCmd = &cobra.Command{
	Use:     "pin <mol-id>",
	GroupID: "issues",
	Short:   "Attach a mol to an agent's hook (work assignment)",
	Long: `Pin a mol to an agent's hook - the action of assigning work.

This is the chemistry-inspired command for work assignment. Pinning a mol
to an agent's hook marks it as their current work focus.

What happens when you pin:
  1. The mol's pinned flag is set to true
  2. The mol's assignee is set to the target agent (with --for)
  3. The mol's status is set to in_progress (with --start)

Use cases:
  - Witness assigning work to polecat: bd pin bd-abc123 --for polecat-ace
  - Self-assigning work: bd pin bd-abc123
  - Reviewing what's on your hook: bd hook

Legacy behavior:
  - Multiple IDs can be pinned at once (original pin command)
  - Without --for, just sets the pinned flag

Examples:
  bd pin bd-abc123                    # Pin (set pinned flag)
  bd pin bd-abc123 --for polecat-ace  # Pin and assign to agent
  bd pin bd-abc123 --for me --start   # Pin, assign to self, start work`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("pin")

		ctx := rootCtx
		forAgent, _ := cmd.Flags().GetString("for")
		startWork, _ := cmd.Flags().GetBool("start")

		// Handle "me" as alias for current actor
		if forAgent == "me" {
			forAgent = actor
		}

		// Resolve partial IDs first
		var resolvedIDs []string
		if daemonClient != nil {
			for _, id := range args {
				resolveArgs := &rpc.ResolveIDArgs{ID: id}
				resp, err := daemonClient.ResolveID(resolveArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error resolving ID %s: %v\n", id, err)
					os.Exit(1)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
					os.Exit(1)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		} else {
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		pinnedIssues := []*types.Issue{}

		// If daemon is running, use RPC
		if daemonClient != nil {
			for _, id := range resolvedIDs {
				pinned := true
				updateArgs := &rpc.UpdateArgs{
					ID:     id,
					Pinned: &pinned,
				}

				// Set assignee if --for was provided
				if forAgent != "" {
					updateArgs.Assignee = &forAgent
				}

				// Set status to in_progress if --start was provided
				if startWork {
					status := string(types.StatusInProgress)
					updateArgs.Status = &status
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error pinning %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						pinnedIssues = append(pinnedIssues, &issue)
					}
				} else {
					msg := fmt.Sprintf("Pinned %s", id)
					if forAgent != "" {
						msg += fmt.Sprintf(" to %s's hook", forAgent)
					}
					fmt.Printf("%s %s\n", ui.RenderPass("📌"), msg)
				}
			}

			if jsonOutput && len(pinnedIssues) > 0 {
				outputJSON(pinnedIssues)
			}
			return
		}

		// Fall back to direct storage access
		if store == nil {
			fmt.Fprintln(os.Stderr, "Error: database not initialized")
			os.Exit(1)
		}

		for _, id := range args {
			fullID, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}

			updates := map[string]interface{}{
				"pinned": true,
			}

			// Set assignee if --for was provided
			if forAgent != "" {
				updates["assignee"] = forAgent
			}

			// Set status to in_progress if --start was provided
			if startWork {
				updates["status"] = string(types.StatusInProgress)
			}

			if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error pinning %s: %v\n", fullID, err)
				continue
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, fullID)
				if issue != nil {
					pinnedIssues = append(pinnedIssues, issue)
				}
			} else {
				msg := fmt.Sprintf("Pinned %s", fullID)
				if forAgent != "" {
					msg += fmt.Sprintf(" to %s's hook", forAgent)
				}
				fmt.Printf("%s %s\n", ui.RenderPass("📌"), msg)
			}
		}

		// Schedule auto-flush if any issues were pinned
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(pinnedIssues) > 0 {
			outputJSON(pinnedIssues)
		}
	},
}


func init() {
	// Pin command flags
	pinCmd.Flags().String("for", "", "Agent to pin work for (use 'me' for self)")
	pinCmd.Flags().Bool("start", false, "Also set status to in_progress")

	rootCmd.AddCommand(pinCmd)
}
