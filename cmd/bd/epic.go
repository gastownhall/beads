package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
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
	Use:           "status",
	Short:         "Show epic completion status",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("epic-status")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		eligibleOnly, _ := cmd.Flags().GetBool("eligible-only")

		if usesProxiedServer() {
			return runEpicStatusProxiedServer(rootCtx, eligibleOnly)
		}

		epics, err := store.GetEpicsEligibleForClosure(rootCtx)
		if err != nil {
			return HandleErrorRespectJSON("getting epic status: %v", err)
		}
		return renderEpicStatus(epics, eligibleOnly)
	},
}

func renderEpicStatus(epics []*types.EpicStatus, eligibleOnly bool) error {
	if eligibleOnly {
		epics = filterEligibleEpics(epics)
	}
	if jsonOutput {
		if epics == nil {
			epics = []*types.EpicStatus{}
		}
		return outputJSON(epics)
	}
	if len(epics) == 0 {
		fmt.Println("No open epics found")
		return nil
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
	return nil
}

func filterEligibleEpics(epics []*types.EpicStatus) []*types.EpicStatus {
	filtered := []*types.EpicStatus{}
	for _, epic := range epics {
		if epic.EligibleForClose {
			filtered = append(filtered, epic)
		}
	}
	return filtered
}

var closeEligibleEpicsCmd = &cobra.Command{
	Use:           "close-eligible",
	Short:         "Close epics where all children are complete",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("epic-close-eligible")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		dryRun, _ := cmd.Flags().GetBool("dry-run")

		if usesProxiedServer() {
			return runCloseEligibleEpicsProxiedServer(rootCtx, dryRun)
		}

		if !dryRun {
			CheckReadonly("epic close-eligible")
		}
		epics, err := store.GetEpicsEligibleForClosure(rootCtx)
		if err != nil {
			return HandleErrorRespectJSON("getting eligible epics: %v", err)
		}
		eligibleEpics := filterEligibleEpics(epics)
		if len(eligibleEpics) == 0 {
			return outputNoEligibleEpics()
		}
		if dryRun {
			return outputCloseEligibleDryRun(eligibleEpics)
		}
		closedIDs := []string{}
		for _, epicStatus := range eligibleEpics {
			err := store.CloseIssue(rootCtx, epicStatus.Epic.ID, "All children completed", "system", "")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", epicStatus.Epic.ID, err)
				continue
			}
			closedIDs = append(closedIDs, epicStatus.Epic.ID)
		}
		if len(closedIDs) > 0 {
			commandDidWrite.Store(true)
		}
		return outputCloseEligibleResult(closedIDs)
	},
}

func outputNoEligibleEpics() error {
	if jsonOutput {
		return outputJSON([]*types.EpicStatus{})
	}
	fmt.Println("No epics eligible for closure")
	return nil
}

func outputCloseEligibleDryRun(eligibleEpics []*types.EpicStatus) error {
	if jsonOutput {
		return outputJSON(eligibleEpics)
	}
	fmt.Printf("Would close %d epic(s):\n", len(eligibleEpics))
	for _, epicStatus := range eligibleEpics {
		fmt.Printf("  - %s: %s\n", epicStatus.Epic.ID, epicStatus.Epic.Title)
	}
	return nil
}

func outputCloseEligibleResult(closedIDs []string) error {
	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"closed": closedIDs,
			"count":  len(closedIDs),
		})
	}
	fmt.Printf("✓ Closed %d epic(s)\n", len(closedIDs))
	for _, id := range closedIDs {
		fmt.Printf("  - %s\n", id)
	}
	return nil
}

func init() {
	epicCmd.AddCommand(epicStatusCmd)
	epicCmd.AddCommand(closeEligibleEpicsCmd)
	epicStatusCmd.Flags().Bool("eligible-only", false, "Show only epics eligible for closure")
	closeEligibleEpicsCmd.Flags().Bool("dry-run", false, "Preview what would be closed without making changes")
	rootCmd.AddCommand(epicCmd)
}
