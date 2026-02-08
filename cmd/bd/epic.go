package main

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"os"
)

var epicCmd = &cobra.Command{
	Use:     "epic",
	GroupID: "deps",
	Short:   "Epic management commands",
}
var epicStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show epic completion status",
	Run: func(cmd *cobra.Command, args []string) {
		eligibleOnly, _ := cmd.Flags().GetBool("eligible-only")
		// Use global jsonOutput set by PersistentPreRun
		var epics []*types.EpicStatus
		var err error
		if daemonClient != nil {
			resp, err := daemonClient.EpicStatus(&rpc.EpicStatusArgs{
				EligibleOnly: eligibleOnly,
			})
			if err != nil {
				FatalErrorRespectJSON("communicating with daemon: %v", err)
			}
			if !resp.Success {
				FatalErrorRespectJSON("getting epic status: %s", resp.Error)
			}
			if err := json.Unmarshal(resp.Data, &epics); err != nil {
				FatalErrorRespectJSON("parsing response: %v", err)
			}
		} else {
			ctx := rootCtx
			epics, err = store.GetEpicsEligibleForClosure(ctx)
			if err != nil {
				FatalErrorRespectJSON("getting epic status: %v", err)
			}
			if eligibleOnly {
				filtered := []*types.EpicStatus{}
				for _, epic := range epics {
					if epic.EligibleForClose {
						filtered = append(filtered, epic)
					}
				}
				epics = filtered
			}
		}
		if jsonOutput {
			if epics == nil {
				epics = []*types.EpicStatus{}
			}
			outputJSON(epics)
			return
		}
		// Human-readable output
		if len(epics) == 0 {
			fmt.Println("No open epics found")
			return
		}
		for _, epicStatus := range epics {
			epic := epicStatus.Epic
			percentage := 0
			if epicStatus.TotalChildren > 0 {
				percentage = (epicStatus.ClosedChildren * 100) / epicStatus.TotalChildren
			}
			statusIcon := ""
			if epicStatus.EligibleForClose {
				statusIcon = ui.RenderPass("✓")
			} else if percentage > 0 {
				statusIcon = ui.RenderWarn("○")
			} else {
				statusIcon = "○"
			}
			fmt.Printf("%s %s %s\n", statusIcon, ui.RenderAccent(epic.ID), ui.RenderBold(epic.Title))
			fmt.Printf("   Progress: %d/%d children closed (%d%%)\n",
				epicStatus.ClosedChildren, epicStatus.TotalChildren, percentage)
			if epicStatus.EligibleForClose {
				fmt.Printf("   %s\n", ui.RenderPass("Eligible for closure"))
			}
			fmt.Println()
		}
	},
}
var closeEligibleEpicsCmd = &cobra.Command{
	Use:   "close-eligible",
	Short: "Close epics where all children are complete",
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		// Block writes in readonly mode (closing modifies data)
		if !dryRun {
			CheckReadonly("epic close-eligible")
		}
		// Use global jsonOutput set by PersistentPreRun
		var eligibleEpics []*types.EpicStatus
		if daemonClient != nil {
			resp, err := daemonClient.EpicStatus(&rpc.EpicStatusArgs{
				EligibleOnly: true,
			})
			if err != nil {
				FatalErrorRespectJSON("communicating with daemon: %v", err)
			}
			if !resp.Success {
				FatalErrorRespectJSON("getting eligible epics: %s", resp.Error)
			}
			if err := json.Unmarshal(resp.Data, &eligibleEpics); err != nil {
				FatalErrorRespectJSON("parsing response: %v", err)
			}
		} else {
			ctx := rootCtx
			epics, err := store.GetEpicsEligibleForClosure(ctx)
			if err != nil {
				FatalErrorRespectJSON("getting eligible epics: %v", err)
			}
			for _, epic := range epics {
				if epic.EligibleForClose {
					eligibleEpics = append(eligibleEpics, epic)
				}
			}
		}
		if len(eligibleEpics) == 0 {
			if jsonOutput {
				outputJSON([]*types.EpicStatus{})
			} else {
				fmt.Println("No epics eligible for closure")
			}
			return
		}
		if dryRun {
			if jsonOutput {
				outputJSON(eligibleEpics)
			} else {
				fmt.Printf("Would close %d epic(s):\n", len(eligibleEpics))
				for _, epicStatus := range eligibleEpics {
					fmt.Printf("  - %s: %s\n", epicStatus.Epic.ID, epicStatus.Epic.Title)
				}
			}
			return
		}
		// Actually close the epics
		closedIDs := []string{}
		for _, epicStatus := range eligibleEpics {
			if daemonClient != nil {
				resp, err := daemonClient.CloseIssue(&rpc.CloseArgs{
					ID:     epicStatus.Epic.ID,
					Reason: "All children completed",
				})
				if err != nil || !resp.Success {
					errMsg := ""
					if err != nil {
						errMsg = err.Error()
					} else if !resp.Success {
						errMsg = resp.Error
					}
					fmt.Fprintf(os.Stderr, "Error closing %s: %s\n", epicStatus.Epic.ID, errMsg)
					continue
				}
			} else {
				ctx := rootCtx
				err := store.CloseIssue(ctx, epicStatus.Epic.ID, "All children completed", "system", "")
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", epicStatus.Epic.ID, err)
					continue
				}
			}
			closedIDs = append(closedIDs, epicStatus.Epic.ID)
		}
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"closed": closedIDs,
				"count":  len(closedIDs),
			})
		} else {
			fmt.Printf("✓ Closed %d epic(s)\n", len(closedIDs))
			for _, id := range closedIDs {
				fmt.Printf("  - %s\n", id)
			}
		}
	},
}

func init() {
	epicCmd.AddCommand(epicStatusCmd)
	epicCmd.AddCommand(closeEligibleEpicsCmd)
	epicStatusCmd.Flags().Bool("eligible-only", false, "Show only epics eligible for closure")
	closeEligibleEpicsCmd.Flags().Bool("dry-run", false, "Preview what would be closed without making changes")
	rootCmd.AddCommand(epicCmd)
}
