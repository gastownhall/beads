// Package main implements the bd CLI state management commands.
// These commands provide convenient access to the labels-as-state pattern
// documented in docs/LABELS.md.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var stateCmd = &cobra.Command{
	Use:     "state <issue-id> <dimension>",
	GroupID: "issues",
	Short:   "Query the current value of a state dimension",
	Long: `Query the current value of a state dimension from an issue's labels.

State labels follow the convention <dimension>:<value>, for example:
  patrol:active
  mode:degraded
  health:healthy

This command extracts the value for a given dimension.

Examples:
  bd state witness-abc patrol     # Output: active
  bd state witness-abc mode       # Output: normal
  bd state witness-abc health     # Output: healthy`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("state")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx
		issueID := args[0]
		dimension := args[1]

		var fullID string
		var err error
		fullID, err = utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		var labels []string
		labels, err = store.GetLabels(ctx, fullID)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		prefix := dimension + ":"
		var value string
		for _, label := range labels {
			if strings.HasPrefix(label, prefix) {
				value = strings.TrimPrefix(label, prefix)
				break
			}
		}

		if jsonOutput {
			result := map[string]interface{}{
				"issue_id":  fullID,
				"dimension": dimension,
				"value":     value,
			}
			if value == "" {
				result["value"] = nil
			}
			return outputJSON(result)
		}

		if value == "" {
			fmt.Printf("(no %s state set)\n", dimension)
		} else {
			fmt.Println(value)
		}
		return nil
	},
}

var setStateCmd = &cobra.Command{
	Use:     "set-state <issue-id> <dimension>=<value>",
	GroupID: "issues",
	Short:   "Set operational state (creates event + updates label)",
	Long: `Atomically set operational state on an issue.

This command:
1. Creates an event bead recording the state change (source of truth)
2. Removes any existing label for the dimension
3. Adds the new dimension:value label (fast lookup cache)

State labels follow the convention <dimension>:<value>, for example:
  patrol:active, patrol:muted
  mode:normal, mode:degraded
  health:healthy, health:failing

Examples:
  bd set-state agent-abc patrol=muted --reason "Investigating stuck worker"
  bd set-state agent-abc mode=degraded --reason "High error rate detected"
  bd set-state agent-abc health=healthy

The --reason flag provides context for the event bead (recommended).`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("set-state")

		evt := metrics.NewCommandEvent("set-state")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx
		issueID := args[0]
		stateSpec := args[1]

		parts := strings.SplitN(stateSpec, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return HandleErrorRespectJSON("invalid state format %q, expected <dimension>=<value>", stateSpec)
		}
		dimension := parts[0]
		newValue := parts[1]

		reason, _ := cmd.Flags().GetString("reason")

		var fullID string
		var err error
		fullID, err = utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		var labels []string
		labels, err = store.GetLabels(ctx, fullID)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		prefix := dimension + ":"
		var oldLabel string
		var oldValue string
		for _, label := range labels {
			if strings.HasPrefix(label, prefix) {
				oldLabel = label
				oldValue = strings.TrimPrefix(label, prefix)
				break
			}
		}

		newLabel := dimension + ":" + newValue

		if oldLabel == newLabel {
			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"issue_id":  fullID,
					"dimension": dimension,
					"value":     newValue,
					"changed":   false,
				})
			}
			fmt.Printf("(no change: %s already set to %s)\n", dimension, newValue)
			return nil
		}

		eventTitle := fmt.Sprintf("State change: %s → %s", dimension, newValue)
		eventDesc := ""
		if oldValue != "" {
			eventDesc = fmt.Sprintf("Changed %s from %s to %s", dimension, oldValue, newValue)
		} else {
			eventDesc = fmt.Sprintf("Set %s to %s", dimension, newValue)
		}
		if reason != "" {
			eventDesc += "\n\nReason: " + reason
		}

		var eventID string
		childID, err := store.GetNextChildID(ctx, fullID)
		if err != nil {
			return HandleErrorRespectJSON("generating child ID: %v", err)
		}

		event := &types.Issue{
			ID:          childID,
			Title:       eventTitle,
			Description: eventDesc,
			Status:      types.StatusClosed,
			Priority:    4,
			IssueType:   types.TypeEvent,
			CreatedBy:   getActorWithGit(),
		}
		if err := store.CreateIssue(ctx, event, actor); err != nil {
			return HandleErrorRespectJSON("creating event: %v", err)
		}

		dep := &types.Dependency{
			IssueID:     childID,
			DependsOnID: fullID,
			Type:        types.DepParentChild,
		}
		if err := store.AddDependency(ctx, dep, actor); err != nil {
			WarnError("failed to add parent-child dependency: %v", err)
		}

		eventID = childID

		if oldLabel != "" {
			if err := store.RemoveLabel(ctx, fullID, oldLabel, actor); err != nil {
				WarnError("failed to remove old label %s: %v", oldLabel, err)
			}
		}

		if err := store.AddLabel(ctx, fullID, newLabel, actor); err != nil {
			return HandleErrorRespectJSON("adding label: %v", err)
		}

		commandDidWrite.Store(true)

		if jsonOutput {
			result := map[string]interface{}{
				"issue_id":  fullID,
				"dimension": dimension,
				"old_value": oldValue,
				"new_value": newValue,
				"event_id":  eventID,
				"changed":   true,
			}
			if oldValue == "" {
				result["old_value"] = nil
			}
			return outputJSON(result)
		}

		fmt.Printf("%s Set %s = %s on %s\n", ui.RenderPass("✓"), dimension, newValue, fullID)
		if oldValue != "" {
			fmt.Printf("  Previous: %s\n", oldValue)
		}
		fmt.Printf("  Event: %s\n", eventID)
		return nil
	},
}

// stateListCmd lists all state dimensions on an issue
var stateListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List all state dimensions on an issue",
	Long: `List all state labels (dimension:value format) on an issue.

This filters labels to only show those following the state convention.

Example:
  bd state list witness-abc
  # Output:
  #   patrol: active
  #   mode: normal
  #   health: healthy`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("state-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx
		issueID := args[0]

		var fullID string
		var err error
		fullID, err = utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		var labels []string
		labels, err = store.GetLabels(ctx, fullID)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		states := make(map[string]string)
		for _, label := range labels {
			if idx := strings.Index(label, ":"); idx > 0 {
				dimension := label[:idx]
				value := label[idx+1:]
				states[dimension] = value
			}
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"issue_id": fullID,
				"states":   states,
			})
		}

		if len(states) == 0 {
			fmt.Printf("\n%s has no state labels\n", fullID)
			return nil
		}

		fmt.Printf("\n%s State for %s:\n", ui.RenderAccent("📊"), fullID)
		for dimension, value := range states {
			fmt.Printf("  %s: %s\n", dimension, value)
		}
		fmt.Println()
		return nil
	},
}

func init() {
	// set-state flags
	setStateCmd.Flags().String("reason", "", "Reason for the state change (recorded in event)")

	// Add subcommands
	stateCmd.AddCommand(stateListCmd)

	rootCmd.AddCommand(stateCmd)
	rootCmd.AddCommand(setStateCmd)
}

// Ensure ctx is available
var _ context.Context = rootCtx
