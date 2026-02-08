package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// mergeSlotCmd is the parent command for merge-slot operations
var mergeSlotCmd = &cobra.Command{
	Use:     "merge-slot",
	GroupID: "issues",
	Short:   "Manage merge-slot gates for serialized conflict resolution",
	Long: `Merge-slot gates serialize conflict resolution in the merge queue.

A merge slot is an exclusive access primitive: only one agent can hold it at a time.
This prevents "monkey knife fights" where multiple polecats race to resolve conflicts
and create cascading conflicts.

Each rig has one merge slot bead: <prefix>-merge-slot (labeled gt:slot).
The slot uses:
  - status=open: slot is available
  - status=in_progress: slot is held
  - holder field: who currently holds the slot
  - waiters field: priority-ordered queue of waiters

Examples:
  bd merge-slot create              # Create merge slot for current rig
  bd merge-slot check               # Check if slot is available
  bd merge-slot acquire             # Try to acquire the slot
  bd merge-slot release             # Release the slot
  bd merge-slot wait                # Wait for slot to become available`,
}

// mergeSlotCreateCmd creates a merge slot bead for the current rig
var mergeSlotCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a merge slot bead for the current rig",
	Long: `Create a merge slot bead for serialized conflict resolution.

The slot ID is automatically generated based on the beads prefix (e.g., gt-merge-slot).
The slot is created with status=open (available).`,
	Args: cobra.NoArgs,
	RunE: runMergeSlotCreate,
}

// mergeSlotCheckCmd checks the current merge slot status
var mergeSlotCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check merge slot availability",
	Long: `Check if the merge slot is available or held.

Returns:
  - available: slot can be acquired
  - held by <holder>: slot is currently held
  - not found: no merge slot exists for this rig`,
	Args: cobra.NoArgs,
	RunE: runMergeSlotCheck,
}

// mergeSlotAcquireCmd attempts to acquire the merge slot
var mergeSlotAcquireCmd = &cobra.Command{
	Use:   "acquire",
	Short: "Acquire the merge slot",
	Long: `Attempt to acquire the merge slot for exclusive access.

If the slot is available (status=open), it will be acquired:
  - status set to in_progress
  - holder set to the requester

If the slot is held (status=in_progress), the command fails and the
requester is optionally added to the waiters list (use --wait flag).

Use --holder to specify who is acquiring (default: BD_ACTOR env var).`,
	Args: cobra.NoArgs,
	RunE: runMergeSlotAcquire,
}

// mergeSlotReleaseCmd releases the merge slot
var mergeSlotReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release the merge slot",
	Long: `Release the merge slot after conflict resolution is complete.

Sets status back to open and clears the holder field.
If there are waiters, the highest-priority waiter should then acquire.`,
	Args: cobra.NoArgs,
	RunE: runMergeSlotRelease,
}

var (
	mergeSlotHolder    string
	mergeSlotAddWaiter bool
)

func init() {
	mergeSlotAcquireCmd.Flags().StringVar(&mergeSlotHolder, "holder", "", "Who is acquiring the slot (default: BD_ACTOR)")
	mergeSlotAcquireCmd.Flags().BoolVar(&mergeSlotAddWaiter, "wait", false, "Add to waiters list if slot is held")
	mergeSlotReleaseCmd.Flags().StringVar(&mergeSlotHolder, "holder", "", "Who is releasing the slot (for verification)")

	mergeSlotCmd.AddCommand(mergeSlotCreateCmd)
	mergeSlotCmd.AddCommand(mergeSlotCheckCmd)
	mergeSlotCmd.AddCommand(mergeSlotAcquireCmd)
	mergeSlotCmd.AddCommand(mergeSlotReleaseCmd)
	rootCmd.AddCommand(mergeSlotCmd)
}

// getMergeSlotID returns the merge slot bead ID for the current rig
func getMergeSlotID() string {
	// Use the prefix from beads config (default "bd")
	prefix := "bd"

	// First try config.yaml (issue-prefix)
	if configPrefix := config.GetString("issue-prefix"); configPrefix != "" {
		prefix = strings.TrimSuffix(configPrefix, "-")
	} else if daemonClient != nil {
		// Daemon mode - use RPC to get config
		if configResp, err := daemonClient.GetConfig(&rpc.GetConfigArgs{Key: "issue_prefix"}); err == nil && configResp.Value != "" {
			prefix = strings.TrimSuffix(configResp.Value, "-")
		}
	} else if store != nil {
		// Direct mode - check database config
		if dbPrefix, err := store.GetConfig(rootCtx, "issue_prefix"); err == nil && dbPrefix != "" {
			prefix = strings.TrimSuffix(dbPrefix, "-")
		}
	}
	return prefix + "-merge-slot"
}

func runMergeSlotCreate(cmd *cobra.Command, args []string) error {
	CheckReadonly("merge-slot create")

	slotID := getMergeSlotID()
	ctx := rootCtx

	// Check if slot already exists
	var existing *types.Issue
	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: slotID})
		if err == nil && resp.Success {
			if uerr := json.Unmarshal(resp.Data, &existing); uerr == nil {
				fmt.Printf("Merge slot already exists: %s\n", slotID)
				return nil
			}
		}
	} else {
		existing, _ = store.GetIssue(ctx, slotID)
		if existing != nil {
			fmt.Printf("Merge slot already exists: %s\n", slotID)
			return nil
		}
	}

	// Create the merge slot bead
	title := "Merge Slot"
	description := "Exclusive access slot for serialized conflict resolution in the merge queue."

	if daemonClient != nil {
		createArgs := &rpc.CreateArgs{
			ID:          slotID,
			Title:       title,
			Description: description,
			IssueType:   string(types.TypeTask), // Use task type; gt:slot label marks it as slot
			Priority:    0,                      // P0 - system infrastructure
			Labels:      []string{"gt:slot"},    // Gas Town slot label
		}
		resp, err := daemonClient.Create(createArgs)
		if err != nil {
			return fmt.Errorf("failed to create merge slot: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("failed to create merge slot: %s", resp.Error)
		}
	} else {
		issue := &types.Issue{
			ID:          slotID,
			Title:       title,
			Description: description,
			IssueType:   types.TypeTask, // Use task type; gt:slot label marks it as slot
			Status:      types.StatusOpen,
			Priority:    0,
		}
		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			return fmt.Errorf("failed to create merge slot: %w", err)
		}
		// Add gt:slot label to mark as slot bead
		if err := store.AddLabel(ctx, slotID, "gt:slot", actor); err != nil {
			// Non-fatal: log warning but don't fail creation
			fmt.Fprintf(os.Stderr, "warning: failed to add gt:slot label: %v\n", err)
		}
		markDirtyAndScheduleFlush()
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id":     slotID,
			"status": "open",
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Created merge slot: %s\n", ui.RenderPass("✓"), slotID)
	return nil
}

func runMergeSlotCheck(cmd *cobra.Command, args []string) error {
	slotID := getMergeSlotID()
	ctx := rootCtx

	// Get the slot bead
	var slot *types.Issue
	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: slotID})
		if err != nil {
			if jsonOutput {
				result := map[string]interface{}{
					"id":        slotID,
					"available": false,
					"error":     "not found",
				}
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			fmt.Printf("Merge slot not found: %s\n", slotID)
			fmt.Printf("Run 'bd merge-slot create' to create one.\n")
			return nil
		}
		var details types.IssueDetails
		if uerr := json.Unmarshal(resp.Data, &details); uerr != nil {
			return fmt.Errorf("parsing response: %w", uerr)
		}
		slot = &details.Issue
	} else {
		var err error
		slot, err = store.GetIssue(ctx, slotID)
		if err != nil || slot == nil {
			if jsonOutput {
				result := map[string]interface{}{
					"id":        slotID,
					"available": false,
					"error":     "not found",
				}
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			fmt.Printf("Merge slot not found: %s\n", slotID)
			fmt.Printf("Run 'bd merge-slot create' to create one.\n")
			return nil
		}
	}

	available := slot.Status == types.StatusOpen
	holder := slot.Holder
	waiters := slot.Waiters

	if jsonOutput {
		result := map[string]interface{}{
			"id":        slotID,
			"available": available,
			"status":    string(slot.Status),
			"holder":    emptyToNil(holder),
			"waiters":   waiters,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	if available {
		fmt.Printf("%s Merge slot available: %s\n", ui.RenderPass("✓"), slotID)
	} else {
		fmt.Printf("%s Merge slot held: %s\n", ui.RenderAccent("○"), slotID)
		fmt.Printf("  Holder: %s\n", holder)
		if len(waiters) > 0 {
			fmt.Printf("  Waiters: %d\n", len(waiters))
			for i, w := range waiters {
				fmt.Printf("    %d. %s\n", i+1, w)
			}
		}
	}

	return nil
}

func runMergeSlotAcquire(cmd *cobra.Command, args []string) error {
	CheckReadonly("merge-slot acquire")

	slotID := getMergeSlotID()
	ctx := rootCtx

	// Determine holder
	holder := mergeSlotHolder
	if holder == "" {
		holder = actor
	}
	if holder == "" {
		return fmt.Errorf("no holder specified; use --holder or set BD_ACTOR env var")
	}

	// Get the slot bead
	var slot *types.Issue
	if daemonClient != nil {
		// Try to resolve the slot ID first
		resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: slotID})
		if err != nil {
			return fmt.Errorf("merge slot not found: %s (run 'bd merge-slot create' first)", slotID)
		}
		var resolvedID string
		if uerr := json.Unmarshal(resp.Data, &resolvedID); uerr != nil {
			return fmt.Errorf("parsing response: %w", uerr)
		}

		showResp, showErr := daemonClient.Show(&rpc.ShowArgs{ID: resolvedID})
		if showErr != nil {
			return fmt.Errorf("merge slot not found: %s", slotID)
		}
		var details types.IssueDetails
		if uerr := json.Unmarshal(showResp.Data, &details); uerr != nil {
			return fmt.Errorf("parsing response: %w", uerr)
		}
		slot = &details.Issue
	} else {
		var err error
		resolvedID, err := utils.ResolvePartialID(ctx, store, slotID)
		if err != nil {
			return fmt.Errorf("merge slot not found: %s (run 'bd merge-slot create' first)", slotID)
		}
		slot, err = store.GetIssue(ctx, resolvedID)
		if err != nil || slot == nil {
			return fmt.Errorf("merge slot not found: %s", slotID)
		}
	}

	// Check slot availability
	if slot.Status != types.StatusOpen {
		// Slot is held
		if mergeSlotAddWaiter {
			// Add to waiters list
			alreadyWaiting := false
			for _, w := range slot.Waiters {
				if w == holder {
					alreadyWaiting = true
					break
				}
			}

			if !alreadyWaiting {
				newWaiters := append(slot.Waiters, holder)
				if daemonClient != nil {
					updateArgs := &rpc.UpdateArgs{
						ID:      slot.ID,
						Waiters: newWaiters,
					}
					_, err := daemonClient.Update(updateArgs)
					if err != nil {
						return fmt.Errorf("failed to add waiter: %w", err)
					}
				} else {
					updates := map[string]interface{}{
						"waiters": newWaiters,
					}
					if err := store.UpdateIssue(ctx, slot.ID, updates, actor); err != nil {
						return fmt.Errorf("failed to add waiter: %w", err)
					}
					markDirtyAndScheduleFlush()
				}
			}

			if jsonOutput {
				result := map[string]interface{}{
					"id":       slot.ID,
					"acquired": false,
					"waiting":  true,
					"holder":   slot.Holder,
					"position": len(slot.Waiters) + 1,
				}
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}

			fmt.Printf("%s Slot held by %s, added to waiters queue (position %d)\n",
				ui.RenderAccent("○"), slot.Holder, len(slot.Waiters)+1)
			os.Exit(1) // Exit with error to indicate slot not acquired
		}

		if jsonOutput {
			result := map[string]interface{}{
				"id":       slot.ID,
				"acquired": false,
				"holder":   slot.Holder,
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}

		fmt.Printf("%s Slot held by: %s\n", ui.RenderFail("✗"), slot.Holder)
		fmt.Printf("Use --wait to add yourself to the waiters queue.\n")
		os.Exit(1) // Exit with error to indicate slot not acquired
	}

	// Slot is available - acquire it
	inProgressStatus := string(types.StatusInProgress)
	if daemonClient != nil {
		updateArgs := &rpc.UpdateArgs{
			ID:     slot.ID,
			Status: &inProgressStatus,
			Holder: &holder,
		}
		_, err := daemonClient.Update(updateArgs)
		if err != nil {
			return fmt.Errorf("failed to acquire slot: %w", err)
		}
	} else {
		updates := map[string]interface{}{
			"status": types.StatusInProgress,
			"holder": holder,
		}
		if err := store.UpdateIssue(ctx, slot.ID, updates, actor); err != nil {
			return fmt.Errorf("failed to acquire slot: %w", err)
		}
		markDirtyAndScheduleFlush()
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id":       slot.ID,
			"acquired": true,
			"holder":   holder,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Acquired merge slot: %s\n", ui.RenderPass("✓"), slot.ID)
	fmt.Printf("  Holder: %s\n", holder)
	return nil
}

func runMergeSlotRelease(cmd *cobra.Command, args []string) error {
	CheckReadonly("merge-slot release")

	slotID := getMergeSlotID()
	ctx := rootCtx

	// Get the slot bead
	var slot *types.Issue
	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: slotID})
		if err != nil {
			return fmt.Errorf("merge slot not found: %s", slotID)
		}
		var details types.IssueDetails
		if uerr := json.Unmarshal(resp.Data, &details); uerr != nil {
			return fmt.Errorf("parsing response: %w", uerr)
		}
		slot = &details.Issue
	} else {
		var err error
		slot, err = store.GetIssue(ctx, slotID)
		if err != nil || slot == nil {
			return fmt.Errorf("merge slot not found: %s", slotID)
		}
	}

	// Verify holder if specified
	if mergeSlotHolder != "" && slot.Holder != mergeSlotHolder {
		return fmt.Errorf("slot held by %s, not %s", slot.Holder, mergeSlotHolder)
	}

	// Check if slot is actually held
	if slot.Status == types.StatusOpen {
		if jsonOutput {
			result := map[string]interface{}{
				"id":       slot.ID,
				"released": false,
				"error":    "slot not held",
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		fmt.Printf("Slot is not held: %s\n", slot.ID)
		return nil
	}

	previousHolder := slot.Holder
	waiters := slot.Waiters

	// Release the slot
	openStatus := string(types.StatusOpen)
	emptyHolder := ""
	if daemonClient != nil {
		updateArgs := &rpc.UpdateArgs{
			ID:     slot.ID,
			Status: &openStatus,
			Holder: &emptyHolder,
		}
		_, err := daemonClient.Update(updateArgs)
		if err != nil {
			return fmt.Errorf("failed to release slot: %w", err)
		}
	} else {
		updates := map[string]interface{}{
			"status": types.StatusOpen,
			"holder": "",
		}
		if err := store.UpdateIssue(ctx, slot.ID, updates, actor); err != nil {
			return fmt.Errorf("failed to release slot: %w", err)
		}
		markDirtyAndScheduleFlush()
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id":              slot.ID,
			"released":        true,
			"previous_holder": previousHolder,
			"waiters":         len(waiters),
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Released merge slot: %s\n", ui.RenderPass("✓"), slot.ID)
	fmt.Printf("  Previous holder: %s\n", previousHolder)
	if len(waiters) > 0 {
		fmt.Printf("  Waiters pending: %d\n", len(waiters))
		fmt.Printf("  Next in queue: %s\n", waiters[0])
	}

	return nil
}
