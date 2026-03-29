// Package main implements the bd CLI state management commands.
// These commands provide convenient access to the labels-as-state pattern
// documented in docs/LABELS.md.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
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
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		issueID := args[0]
		dimension := args[1]

		// Resolve partial ID
		var fullID string
		var err error
		fullID, err = utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		// Get labels for the issue
		var labels []string
		labels, err = store.GetLabels(ctx, fullID)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		// Find label matching dimension:*
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
			outputJSON(result)
			return
		}

		if value == "" {
			fmt.Printf("(no %s state set)\n", dimension)
		} else {
			fmt.Println(value)
		}
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
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("set-state")
		ctx := rootCtx
		issueID := args[0]
		stateSpec := args[1]

		// Parse dimension=value
		parts := strings.SplitN(stateSpec, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			FatalErrorRespectJSON("invalid state format %q, expected <dimension>=<value>", stateSpec)
		}
		dimension := parts[0]
		newValue := parts[1]

		reason, _ := cmd.Flags().GetString("reason")

		// Resolve partial ID
		var fullID string
		var err error
		fullID, err = utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		// Get current labels to find existing dimension value
		var labels []string
		labels, err = store.GetLabels(ctx, fullID)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		// Find existing label for this dimension
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

		// Skip if no change
		if oldLabel == newLabel {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"issue_id":  fullID,
					"dimension": dimension,
					"value":     newValue,
					"changed":   false,
				})
			} else {
				fmt.Printf("(no change: %s already set to %s)\n", dimension, newValue)
			}
			return
		}

		// 1. Create event bead recording the state change
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
		// Get next child ID for the event
		childID, err := store.GetNextChildID(ctx, fullID)
		if err != nil {
			FatalErrorRespectJSON("generating child ID: %v", err)
		}

		event := &types.Issue{
			ID:          childID,
			Title:       eventTitle,
			Description: eventDesc,
			Status:      types.StatusClosed, // Events are immediately closed
			Priority:    4,
			IssueType:   types.TypeEvent,
			CreatedBy:   getActorWithGit(),
		}
		if err := store.CreateIssue(ctx, event, actor); err != nil {
			FatalErrorRespectJSON("creating event: %v", err)
		}

		// Add parent-child dependency
		dep := &types.Dependency{
			IssueID:     childID,
			DependsOnID: fullID,
			Type:        types.DepParentChild,
		}
		if err := store.AddDependency(ctx, dep, actor); err != nil {
			WarnError("failed to add parent-child dependency: %v", err)
		}

		eventID = childID

		// 2. Remove old label if exists
		if oldLabel != "" {
			if err := store.RemoveLabel(ctx, fullID, oldLabel, actor); err != nil {
				WarnError("failed to remove old label %s: %v", oldLabel, err)
			}
		}

		// 3. Add new label
		if err := store.AddLabel(ctx, fullID, newLabel, actor); err != nil {
			FatalErrorRespectJSON("adding label: %v", err)
		}

		// Embedded mode: flush Dolt commit.
		if isEmbeddedMode() && store != nil {
			if _, err := store.CommitPending(ctx, actor); err != nil {
				FatalErrorRespectJSON("failed to commit: %v", err)
			}
		}

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
			outputJSON(result)
			return
		}

		fmt.Printf("%s Set %s = %s on %s\n", ui.RenderPass("✓"), dimension, newValue, fullID)
		if oldValue != "" {
			fmt.Printf("  Previous: %s\n", oldValue)
		}
		fmt.Printf("  Event: %s\n", eventID)
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
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		issueID := args[0]

		// Resolve partial ID
		var fullID string
		var err error
		fullID, err = utils.ResolvePartialID(ctx, store, issueID)
		if err != nil {
			FatalErrorRespectJSON("resolving %s: %v", issueID, err)
		}

		// Get labels for the issue
		var labels []string
		labels, err = store.GetLabels(ctx, fullID)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		// Extract state labels (those with colon)
		states := make(map[string]string)
		for _, label := range labels {
			if idx := strings.Index(label, ":"); idx > 0 {
				dimension := label[:idx]
				value := label[idx+1:]
				states[dimension] = value
			}
		}

		if jsonOutput {
			result := map[string]interface{}{
				"issue_id": fullID,
				"states":   states,
			}
			outputJSON(result)
			return
		}

		if len(states) == 0 {
			fmt.Printf("\n%s has no state labels\n", fullID)
			return
		}

		fmt.Printf("\n%s State for %s:\n", ui.RenderAccent("📊"), fullID)
		for dimension, value := range states {
			fmt.Printf("  %s: %s\n", dimension, value)
		}
		fmt.Println()
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
