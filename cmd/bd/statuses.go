package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// builtInStatuses describes the built-in statuses with their categories and descriptions.
var builtInStatuses = []struct {
	Status      types.Status
	Category    types.StatusCategory
	Description string
}{
	{types.StatusOpen, types.CategoryActive, "Available to work (default)"},
	{types.StatusInProgress, types.CategoryWIP, "Actively being worked on"},
	{types.StatusBlocked, types.CategoryWIP, "Blocked by a dependency"},
	{types.StatusDeferred, types.CategoryFrozen, "Deliberately put on ice for later"},
	{types.StatusClosed, types.CategoryDone, "Completed"},
	{types.StatusPinned, types.CategoryFrozen, "Persistent, stays open indefinitely"},
	{types.StatusHooked, types.CategoryWIP, "Attached to an agent's hook"},
}

var statusesCmd = &cobra.Command{
	Use:     "statuses",
	GroupID: "views",
	Short:   "List valid issue statuses",
	Long: `List all valid issue statuses and their categories.

Built-in statuses (open, in_progress, blocked, etc.) are always valid.
Additional statuses can be configured via status.custom:

  bd config set status.custom "in_review:active,qa_testing:wip,on_hold:frozen"

Categories control behavior:
  active  — appears in 'bd ready' and default 'bd list'
  wip     — excluded from 'bd ready', visible in default 'bd list'
  done    — excluded from 'bd ready' and default 'bd list'
  frozen  — excluded from 'bd ready' and default 'bd list'

Statuses without a category (legacy format) are valid but excluded from 'bd ready'.

Examples:
  bd statuses            # List all statuses with icons and categories
  bd statuses --json     # Output as JSON
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("statuses")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if err := ensureDirectMode("statuses command requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		var customStatuses []types.CustomStatus
		ctx := context.Background()
		if store != nil {
			if cs, err := store.GetCustomStatusesDetailed(ctx); err == nil {
				customStatuses = cs
			}
		}

		if jsonOutput {
			result := struct {
				BuiltInStatuses []statusInfo         `json:"built_in_statuses"`
				CustomStatuses  []types.CustomStatus `json:"custom_statuses,omitempty"`
			}{}

			for _, s := range builtInStatuses {
				result.BuiltInStatuses = append(result.BuiltInStatuses, statusInfo{
					Name:        string(s.Status),
					Category:    string(s.Category),
					Icon:        ui.GetStatusIcon(string(s.Status)),
					Description: s.Description,
				})
			}
			result.CustomStatuses = customStatuses
			return outputJSON(result)
		}

		fmt.Println("Built-in statuses:")
		for _, s := range builtInStatuses {
			icon := ui.RenderStatusIcon(string(s.Status))
			fmt.Printf("  %s %-14s [%-6s]  %s\n", icon, s.Status, s.Category, s.Description)
		}

		if len(customStatuses) > 0 {
			fmt.Println("\nCustom statuses:")
			for _, cs := range customStatuses {
				icon := ui.RenderStatusIconWithCategory(cs.Name, cs.Category)
				fmt.Printf("  %s %-14s [%-6s]\n", icon, cs.Name, cs.Category)
			}
		} else {
			fmt.Println("\nNo custom statuses configured.")
			fmt.Println("Configure with: bd config set status.custom \"name:category,...\"")
			fmt.Println("Categories: active, wip, done, frozen")
		}
		return nil
	},
}

type statusInfo struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

func init() {
	rootCmd.AddCommand(statusesCmd)
}
