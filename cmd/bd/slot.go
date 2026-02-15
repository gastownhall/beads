package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// Valid slot names for agent beads
var validSlots = map[string]bool{
	"hook": true, // hook_bead field - current work (0..1)
	"role": true, // role_bead field - role definition (required)
}

var slotCmd = &cobra.Command{
	Use:   "slot",
	Short: "Manage agent bead slots",
	Long: `Manage slots on agent beads.

Agent beads have named slots that reference other beads:
  hook  - Current work attached to agent's hook (0..1 cardinality)
  role  - Role definition bead (required for agents)

Slots enforce cardinality constraints - the hook slot can only hold one bead.

Examples:
  bd slot show gt-mayor           # Show all slots for mayor agent
  bd slot set gt-emma hook bd-xyz # Attach work bd-xyz to emma's hook
  bd slot clear gt-emma hook      # Clear emma's hook (detach work)`,
}

var slotSetCmd = &cobra.Command{
	Use:   "set <agent> <slot> <bead>",
	Short: "Set a slot on an agent bead",
	Long: `Set a slot on an agent bead.

The slot command enforces cardinality: if the hook slot is already occupied,
the command will error. Use 'bd slot clear' first to detach existing work.

Examples:
  bd slot set gt-emma hook bd-xyz   # Attach bd-xyz to emma's hook
  bd slot set gt-mayor role gt-role # Set mayor's role bead`,
	Args: cobra.ExactArgs(3),
	RunE: runSlotSet,
}

var slotClearCmd = &cobra.Command{
	Use:   "clear <agent> <slot>",
	Short: "Clear a slot on an agent bead",
	Long: `Clear a slot on an agent bead.

This detaches whatever bead is currently in the slot.

Examples:
  bd slot clear gt-emma hook   # Detach work from emma's hook
  bd slot clear gt-mayor role  # Clear mayor's role (not recommended)`,
	Args: cobra.ExactArgs(2),
	RunE: runSlotClear,
}

var slotShowCmd = &cobra.Command{
	Use:   "show <agent>",
	Short: "Show all slots on an agent bead",
	Long: `Show all slots on an agent bead.

Displays the current values of all slot fields.

Examples:
  bd slot show gt-emma   # Show emma's slots
  bd slot show gt-mayor  # Show mayor's slots`,
	Args: cobra.ExactArgs(1),
	RunE: runSlotShow,
}

func init() {
	slotCmd.AddCommand(slotSetCmd)
	slotCmd.AddCommand(slotClearCmd)
	slotCmd.AddCommand(slotShowCmd)
	rootCmd.AddCommand(slotCmd)
}

func runSlotSet(cmd *cobra.Command, args []string) error {
	CheckReadonly("slot set")

	agentArg := args[0]
	slotName := strings.ToLower(args[1])
	beadArg := args[2]

	// Validate slot name
	if !validSlots[slotName] {
		return fmt.Errorf("invalid slot name %q; valid slots: hook, role", slotName)
	}

	ctx := rootCtx

	// Resolve agent ID with routing support (agent beads may live in a different database)
	routedResult, err := resolveAndGetIssueWithRouting(ctx, store, agentArg)
	if err != nil {
		if routedResult != nil {
			routedResult.Close()
		}
		return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
	}
	if routedResult == nil || routedResult.Issue == nil {
		if routedResult != nil {
			routedResult.Close()
		}
		return fmt.Errorf("agent bead not found: %s", agentArg)
	}
	defer routedResult.Close()

	agentID := routedResult.ResolvedID
	agent := routedResult.Issue

	// Determine which store to use (routed or local)
	activeStore := store
	if routedResult.Routed {
		activeStore = routedResult.Store
	}

	// Resolve bead ID - use routing for cross-beads references (e.g., hq-* from rig beads)
	var beadID string
	if needsRouting(beadArg) {
		// Cross-beads reference - resolve via routing
		result, routeErr := resolveAndGetIssueWithRouting(ctx, store, beadArg)
		if result != nil {
			defer result.Close()
		}
		if routeErr != nil {
			return fmt.Errorf("failed to resolve bead %s: %w", beadArg, routeErr)
		}
		if result == nil || result.Issue == nil {
			return fmt.Errorf("failed to resolve bead %s: no issue found matching %q", beadArg, beadArg)
		}
		beadID = result.ResolvedID
	} else {
		beadID, err = utils.ResolvePartialID(ctx, store, beadArg)
		if err != nil {
			return fmt.Errorf("failed to resolve bead %s: %w", beadArg, err)
		}
	}

	// Verify agent bead is actually an agent (check for gt:agent label)
	labels, _ := activeStore.GetLabels(ctx, agentID)
	if !isAgentBead(labels) {
		return fmt.Errorf("%s is not an agent bead (missing gt:agent label)", agentID)
	}

	// Check cardinality - error if slot is already occupied (for hook)
	if slotName == "hook" && agent.HookBead != "" {
		return fmt.Errorf("hook slot already occupied by %s; use 'bd slot clear %s hook' first", agent.HookBead, agentID)
	}

	// Update the slot
	updates := map[string]interface{}{}
	switch slotName {
	case "hook":
		updates["hook_bead"] = beadID
	case "role":
		updates["role_bead"] = beadID
	}
	if err := activeStore.UpdateIssue(ctx, agentID, updates, actor); err != nil {
		return fmt.Errorf("failed to set slot: %w", err)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"agent": agentID,
			"slot":  slotName,
			"bead":  beadID,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Set %s.%s = %s\n", ui.RenderPass("✓"), agentID, slotName, beadID)
	return nil
}

func runSlotClear(cmd *cobra.Command, args []string) error {
	CheckReadonly("slot clear")

	agentArg := args[0]
	slotName := strings.ToLower(args[1])

	// Validate slot name
	if !validSlots[slotName] {
		return fmt.Errorf("invalid slot name %q; valid slots: hook, role", slotName)
	}

	ctx := rootCtx

	// Resolve agent ID with routing support (agent beads may live in a different database)
	routedResult, err := resolveAndGetIssueWithRouting(ctx, store, agentArg)
	if err != nil {
		if routedResult != nil {
			routedResult.Close()
		}
		return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
	}
	if routedResult == nil || routedResult.Issue == nil {
		if routedResult != nil {
			routedResult.Close()
		}
		return fmt.Errorf("agent bead not found: %s", agentArg)
	}
	defer routedResult.Close()

	agentID := routedResult.ResolvedID

	// Determine which store to use (routed or local)
	activeStore := store
	if routedResult.Routed {
		activeStore = routedResult.Store
	}

	// Verify agent bead is actually an agent (check for gt:agent label)
	labels, _ := activeStore.GetLabels(ctx, agentID)
	if !isAgentBead(labels) {
		return fmt.Errorf("%s is not an agent bead (missing gt:agent label)", agentID)
	}

	// Clear the slot (set to empty string)
	updates := map[string]interface{}{}
	switch slotName {
	case "hook":
		updates["hook_bead"] = ""
	case "role":
		updates["role_bead"] = ""
	}
	if err := activeStore.UpdateIssue(ctx, agentID, updates, actor); err != nil {
		return fmt.Errorf("failed to clear slot: %w", err)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"agent": agentID,
			"slot":  slotName,
			"bead":  nil,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Cleared %s.%s\n", ui.RenderPass("✓"), agentID, slotName)
	return nil
}

func runSlotShow(cmd *cobra.Command, args []string) error {
	agentArg := args[0]

	ctx := rootCtx

	// Resolve agent ID with routing support (agent beads may live in a different database)
	routedResult, err := resolveAndGetIssueWithRouting(ctx, store, agentArg)
	if err != nil {
		if routedResult != nil {
			routedResult.Close()
		}
		return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
	}
	if routedResult == nil || routedResult.Issue == nil {
		if routedResult != nil {
			routedResult.Close()
		}
		return fmt.Errorf("agent bead not found: %s", agentArg)
	}
	defer routedResult.Close()

	agentID := routedResult.ResolvedID
	agent := routedResult.Issue

	// Determine which store to use (routed or local)
	activeStore := store
	if routedResult.Routed {
		activeStore = routedResult.Store
	}

	// Verify agent bead is actually an agent (check for gt:agent label)
	labels, _ := activeStore.GetLabels(ctx, agentID)
	if !isAgentBead(labels) {
		return fmt.Errorf("%s is not an agent bead (missing gt:agent label)", agentID)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"agent": agentID,
			"slots": map[string]interface{}{
				"hook": emptyToNil(agent.HookBead),
				"role": emptyToNil(agent.RoleBead),
			},
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	// Human-readable output
	fmt.Printf("Agent: %s\n", agentID)
	fmt.Println("Slots:")
	if agent.HookBead != "" {
		fmt.Printf("  hook: %s\n", agent.HookBead)
	} else {
		fmt.Println("  hook: (empty)")
	}
	if agent.RoleBead != "" {
		fmt.Printf("  role: %s\n", agent.RoleBead)
	} else {
		fmt.Println("  role: (empty)")
	}

	return nil
}

// emptyToNil converts empty string to nil for JSON output
func emptyToNil(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
