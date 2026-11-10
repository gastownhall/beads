// Package main implements the bd CLI state management commands.
// These commands provide convenient access to the labels-as-state pattern
// documented in docs/core-concepts/labels.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// resolveStateTarget reads one issue's detail view for the state commands, on
// whichever route this invocation is on.
//
// STATE IS LABELS, so this is the same two moves `bd label list` makes after
// ga-26w10: resolve the positional argument to the exact id the roles take,
// then ask issueops.Reader for the detail view, whose Labels are already
// hydrated and already sorted. There is no label-shaped read here and no plane
// to choose — Reader.Get's issue-then-wisp lookup finds an ephemeral row on its
// own, which is how the DIRECT route acquires wisp support it never had: it
// used to call store.GetLabels, which only ever reads the durable table.
//
// Resolution stays a front-door job, and stays shared with `bd label`:
// GetRequest.ID is exact by contract, so the partial-id affordance the direct
// route offers cannot live below this line.
func resolveStateTarget(ctx context.Context, issueID string) (*issueops.IssueDetails, error) {
	fullID, err := resolveLabelTarget(ctx, issueID)
	if err != nil {
		return nil, err
	}
	reader, err := openIssueReader()
	if err != nil {
		return nil, err
	}
	details, err := reader.Get(ctx, issueops.GetRequest{ID: fullID})
	if errors.Is(err, storage.ErrNotFound) {
		return nil, errors.New("not found")
	}
	if err != nil {
		return nil, err
	}
	return details, nil
}

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

		return runState(rootCtx, args[0], args[1])
	},
}

func runState(ctx context.Context, issueID, dimension string) error {
	details, err := resolveStateTarget(ctx, issueID)
	if err != nil {
		return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
	}

	value, _ := findStateLabel(details.Labels, dimension)

	if jsonOutput {
		result := map[string]interface{}{
			"issue_id":  details.ID,
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
		evt := metrics.NewCommandEvent("set-state")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		issueID := args[0]
		stateSpec := args[1]

		parts := strings.SplitN(stateSpec, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return HandleErrorRespectJSON("invalid state format %q, expected <dimension>=<value>", stateSpec)
		}
		dimension := parts[0]
		newValue := parts[1]

		reason, _ := cmd.Flags().GetString("reason")

		CheckReadonly("set-state")

		return runSetState(rootCtx, issueID, dimension, newValue, reason)
	},
}

func runSetState(ctx context.Context, issueID, dimension, newValue, reason string) error {
	details, err := resolveStateTarget(ctx, issueID)
	if err != nil {
		return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
	}
	fullID := details.ID

	oldValue, found := findStateLabel(details.Labels, dimension)
	oldLabel := ""
	if found {
		oldLabel = dimension + ":" + oldValue
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
		// The same marker types.GateDescription writes, from the one constant
		// that owns its spelling — types.GateReason reads any description
		// carrying it, so the two writers must not drift apart by accident.
		eventDesc += types.ReasonMarker + reason
	}

	applier, err := openIssueBatchApplier()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	// The role commits its own Dolt version inside the storage layer, so
	// `--dolt-auto-commit batch` is deferred on the CONTEXT rather than by
	// blanking a commit message — see applyLabelEdit in cmd/bd/label.go.
	ctx, err = issueOpsContext(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	// ONE REQUEST, ONE TRANSACTION (GH#5637). The event, its parent edge and
	// the label swap are the three items of a single BatchApplier plan, so a
	// refusal anywhere rolls all three back and a state change is recorded
	// exactly when it happened. This used to be two separately committed
	// lifecycle calls, and a refused label patch left a committed event for a
	// transition that never took place. There is no fallback to separate
	// writes: a route that cannot apply the plan fails the command.
	//
	// THE EVENT IS CREATED IN THE PLANE ITS SUBJECT LIVES IN: it carries the
	// subject's own Ephemeral and NoHistory, and the parent edge is written to
	// the event's plane, so all three writes land in one plane. The event takes
	// an ordinary minted id; the edge, not a dotted id, is what makes it the
	// subject's child.
	//
	// ONE PATCH SWAPS THE DIMENSION: a LabelPatch applies Add and Remove in
	// one guarded update, so the dimension has one value or the command
	// failed. bd set-state has always edited templates, and AllowTemplate
	// stands the template guard down for that item only.
	result, err := applier.ApplyBatch(ctx, issueops.ApplyBatchRequest{
		Actor:      currentActor(),
		Provenance: "bd: set-state " + fullID,
		Items: []issueops.ApplyItem{
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
				Key: stateEventKey,
				Issue: &types.Issue{
					Title:       eventTitle,
					Description: eventDesc,
					Status:      types.StatusClosed,
					Priority:    4,
					IssueType:   types.TypeEvent,
					Ephemeral:   details.Ephemeral,
					NoHistory:   details.NoHistory,
				},
			}},
			{Kind: issueops.ItemDepAdd, DepAdd: &issueops.DepAddItem{
				Source: issueops.Ref{Key: stateEventKey},
				Target: issueops.Ref{ID: fullID},
				Type:   types.DepParentChild,
			}},
			{Kind: issueops.ItemUpdate, Update: &issueops.UpdateItem{
				Target: issueops.Ref{ID: fullID},
				Patch: issueops.IssuePatch{Labels: issueops.LabelPatch{
					Add:    []string{newLabel},
					Remove: removeStateLabel(oldLabel),
				}},
				AllowTemplate: details.IsTemplate,
			}},
		},
	})
	if err != nil {
		return HandleErrorRespectJSON("%s", setStateRefusal(dimension, err))
	}
	commandDidWrite.Store(true)
	eventID := result.Keys[stateEventKey]

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
}

// stateEventKey names set-state's event create inside its one-request plan,
// so the parent edge can reference the id the create mints.
const stateEventKey = "event"

// setStateRefusal says which part of a refused state change refused: the
// event create and its parent edge (items 0 and 1) as `creating event`, and
// the label swap or anything else as `setting <dimension>`. Nothing landed
// either way. An item refusal is reported as the item's own error, without the
// plan position the user did not write.
func setStateRefusal(dimension string, err error) string {
	var itemErr *issueops.ItemError
	if errors.As(err, &itemErr) {
		if itemErr.Index < 2 {
			return fmt.Sprintf("creating event: %v", itemErr.Err)
		}
		return fmt.Sprintf("setting %s: %v", dimension, itemErr.Err)
	}
	return fmt.Sprintf("setting %s: %v", dimension, err)
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

		return runStateList(rootCtx, args[0])
	},
}

func runStateList(ctx context.Context, issueID string) error {
	details, err := resolveStateTarget(ctx, issueID)
	if err != nil {
		return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
	}
	fullID := details.ID

	states := collectStateLabels(details.Labels)

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

func findStateLabel(labels []string, dimension string) (string, bool) {
	prefix := dimension + ":"
	for _, label := range labels {
		if strings.HasPrefix(label, prefix) {
			return strings.TrimPrefix(label, prefix), true
		}
	}
	return "", false
}

// removeStateLabel is the Remove member of the dimension swap: the old label
// when there was one, and nothing when the dimension is being set for the
// first time. A nil slice rather than a one-entry slice holding "" matters —
// LabelPatch drops an empty entry, so both spell "remove nothing", but only
// one of them says so.
func removeStateLabel(oldLabel string) []string {
	if oldLabel == "" {
		return nil
	}
	return []string{oldLabel}
}

func collectStateLabels(labels []string) map[string]string {
	states := make(map[string]string)
	for _, label := range labels {
		if index := strings.Index(label, ":"); index > 0 {
			states[label[:index]] = label[index+1:]
		}
	}
	return states
}
