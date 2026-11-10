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

// openBatchApplier hands back the guarded apply-many role for whichever route
// this invocation is on — the same two-arm shape as openIssueLifecycle in
// cmd/bd/label.go, because `bd set-state` composes rather than patches: its
// event, parent edge and label swap are one plan, and the role is what makes
// them one transaction (GH#5637). Neither branch opens a unit of work or
// composes a transaction — the role does — and there is no fallback to
// separate writes if a route cannot offer the surface: a front door that
// cannot ask for atomicity must refuse rather than strand half a transition.
func openBatchApplier() (issueops.BatchApplier, error) {
	if usesProxiedServer() {
		return proxiedBatchApplier()
	}
	return store.BatchApplier()
}

// setStateFailurePrefix maps a batch refusal onto the two messages this
// command has always printed: the event item and its parent edge read as
// "creating event:", and the label patch reads as "setting <dimension>:".
// Every other failure — including an HTTP capability refusal raised before
// the request was sent — reads as the label arm, which is where the
// command's own input is spelled.
func setStateFailurePrefix(err error, dimension string) string {
	var itemErr *issueops.ItemError
	if errors.As(err, &itemErr) && (itemErr.Index == 0 || itemErr.Index == 1) {
		return "creating event:"
	}
	return fmt.Sprintf("setting %s:", dimension)
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

	applier, err := openBatchApplier()
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

	// ONE ALL-OR-NOTHING REQUEST. The event, its parent-child edge and the
	// label swap are three items of one ApplyBatch, so a refused patch rolls
	// the event back with it (GH#5637): this command used to make two
	// lifecycle calls that each committed their own Dolt version, and a
	// label that failed validation left a committed event recording a
	// transition that never happened, with nothing to clean it up.
	//
	// THE EVENT IS CREATED IN THE PLANE ITS SUBJECT LIVES IN, and the copied
	// retention mode is what keeps that a fact rather than a choice — the
	// same statement both retired routes made by branching on it here. The
	// parent edge is an ITEM rather than a CreateRequest.ParentID, so the
	// event is minted an ordinary id instead of consuming one of the
	// subject's child numbers, and the guarded create's parent-plane check
	// is not part of this shape: a dep_add routes by its own source plane
	// under the batch's ordinary edge policy, which accepts an edge to a
	// subject that moved planes after the pre-read (the label item still
	// resolves the CURRENT row — see the compatibility note in
	// docs/core-concepts/labels.md).
	//
	// ONE PATCH SWAPS THE DIMENSION, exactly as before: a LabelPatch applies
	// Add and Remove in one guarded update, so the dimension has one value
	// or the command failed. `bd set-state` has always edited templates;
	// the waiver is the role's own (issueops.UpdateItem.AllowTemplate),
	// spelled only when the pre-read saw a template, so a subject that
	// became a template after the read refuses atomically rather than
	// retrying with broader permission.
	result, err := applier.ApplyBatch(ctx, issueops.ApplyBatchRequest{
		Actor:      currentActor(),
		Provenance: fmt.Sprintf("bd: set-state %s", fullID),
		Items: []issueops.ApplyItem{
			{
				Kind: issueops.ItemCreate,
				Create: &issueops.CreateItem{
					Key: "event",
					Issue: &types.Issue{
						Title:       eventTitle,
						Description: eventDesc,
						Status:      types.StatusClosed,
						Priority:    4,
						IssueType:   types.TypeEvent,
						Ephemeral:   details.Ephemeral,
						NoHistory:   details.NoHistory,
					},
				},
			},
			{
				Kind: issueops.ItemDepAdd,
				DepAdd: &issueops.DepAddItem{
					Source: issueops.Ref{Key: "event"},
					Target: issueops.Ref{ID: fullID},
					Type:   types.DepParentChild,
				},
			},
			{
				Kind: issueops.ItemUpdate,
				Update: &issueops.UpdateItem{
					Target: issueops.Ref{ID: fullID},
					Patch: issueops.IssuePatch{Labels: issueops.LabelPatch{
						Add:    []string{newLabel},
						Remove: removeStateLabel(oldLabel),
					}},
					AllowTemplate: details.IsTemplate,
				},
			},
		},
	})
	if err != nil {
		return HandleErrorRespectJSON("%s %v", setStateFailurePrefix(err, dimension), err)
	}
	// Marked only on success: a refused batch left nothing behind, so there
	// is no partial write for the deferred commit to know about.
	commandDidWrite.Store(true)
	eventID := result.Keys["event"]

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
