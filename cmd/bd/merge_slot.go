package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
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
  - metadata.holder: who currently holds the slot
  - metadata.waiters: priority-ordered queue of waiters

Examples:
  bd merge-slot create              # Create merge slot for current rig
  bd merge-slot check               # Check if slot is available
  bd merge-slot acquire             # Try to acquire the slot
  bd merge-slot release             # Release the slot`,
}

var mergeSlotCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a merge slot bead for the current rig",
	Long: `Create a merge slot bead for serialized conflict resolution.

The slot ID is automatically generated based on the beads prefix (e.g., gt-merge-slot).
The slot is created with status=open (available).`,
	Args: cobra.NoArgs,
	RunE: runMergeSlotCreate,
}

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

var mergeSlotAcquireCmd = &cobra.Command{
	Use:   "acquire",
	Short: "Acquire the merge slot",
	Long: `Attempt to acquire the merge slot for exclusive access.

If the slot is available (status=open), it will be acquired:
  - status set to in_progress
  - holder set to the requester

If the slot is held (status=in_progress), the command fails unless
--wait is passed, which adds the requester to the waiters queue.

Use --holder to specify who is acquiring (default: BEADS_ACTOR env var).`,
	Args: cobra.NoArgs,
	RunE: runMergeSlotAcquire,
}

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
	mergeSlotAcquireCmd.Flags().StringVar(&mergeSlotHolder, "holder", "", "Who is acquiring the slot (default: BEADS_ACTOR)")
	mergeSlotAcquireCmd.Flags().BoolVar(&mergeSlotAddWaiter, "wait", false, "Add to waiters list if slot is held")
	mergeSlotReleaseCmd.Flags().StringVar(&mergeSlotHolder, "holder", "", "Who is releasing the slot (for verification)")

	mergeSlotCmd.AddCommand(mergeSlotCreateCmd)
	mergeSlotCmd.AddCommand(mergeSlotCheckCmd)
	mergeSlotCmd.AddCommand(mergeSlotAcquireCmd)
	mergeSlotCmd.AddCommand(mergeSlotReleaseCmd)
	rootCmd.AddCommand(mergeSlotCmd)
}

func runMergeSlotCreate(cmd *cobra.Command, args []string) error {
	CheckReadonly("merge-slot create")

	issue, err := store.MergeSlotCreate(rootCtx, actor)
	if err != nil {
		return err
	}

	commandDidWrite.Store(true)

	if jsonOutput {
		result := map[string]interface{}{
			"id":     issue.ID,
			"status": string(issue.Status),
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Created merge slot: %s\n", ui.RenderPass("✓"), issue.ID)
	return nil
}

func runMergeSlotCheck(cmd *cobra.Command, args []string) error {
	status, err := store.MergeSlotCheck(rootCtx)
	if err != nil {
		if isNotFoundErr(err) {
			slotID := storage.MergeSlotID(rootCtx, store)
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
		return err
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id":        status.SlotID,
			"available": status.Available,
			"holder":    nilIfEmpty(status.Holder),
			"waiters":   status.Waiters,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	if status.Available {
		fmt.Printf("%s Merge slot available: %s\n", ui.RenderPass("✓"), status.SlotID)
	} else {
		fmt.Printf("%s Merge slot held: %s\n", ui.RenderAccent("○"), status.SlotID)
		fmt.Printf("  Holder: %s\n", status.Holder)
		if len(status.Waiters) > 0 {
			fmt.Printf("  Waiters: %d\n", len(status.Waiters))
			for i, w := range status.Waiters {
				fmt.Printf("    %d. %s\n", i+1, w)
			}
		}
	}

	return nil
}

func runMergeSlotAcquire(cmd *cobra.Command, args []string) error {
	CheckReadonly("merge-slot acquire")

	holder := mergeSlotHolder
	if holder == "" {
		holder = actor
	}
	if holder == "" {
		return fmt.Errorf("no holder specified; use --holder or set BEADS_ACTOR env var")
	}

	result, err := store.MergeSlotAcquire(rootCtx, holder, actor, mergeSlotAddWaiter)
	if err != nil {
		return err
	}

	if !result.Acquired && !result.Waiting {
		// Slot was held, no --wait flag.
		if jsonOutput {
			out := map[string]interface{}{
				"id":       result.SlotID,
				"acquired": false,
				"holder":   result.Holder,
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(out)
		} else {
			fmt.Printf("%s Slot held by: %s\n", ui.RenderFail("✗"), result.Holder)
			fmt.Printf("Use --wait to add yourself to the waiters queue.\n")
		}
		os.Exit(1)
	}

	if result.Waiting {
		if jsonOutput {
			out := map[string]interface{}{
				"id":       result.SlotID,
				"acquired": false,
				"waiting":  true,
				"holder":   result.Holder,
				"position": result.Position,
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(out)
		} else {
			fmt.Printf("%s Slot held by %s, added to waiters queue (position %d)\n",
				ui.RenderAccent("○"), result.Holder, result.Position)
		}
		os.Exit(1)
	}

	// Successfully acquired.
	commandDidWrite.Store(true)

	if jsonOutput {
		out := map[string]interface{}{
			"id":       result.SlotID,
			"acquired": true,
			"holder":   holder,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(out)
	}

	fmt.Printf("%s Acquired merge slot: %s\n", ui.RenderPass("✓"), result.SlotID)
	fmt.Printf("  Holder: %s\n", holder)
	return nil
}

func runMergeSlotRelease(cmd *cobra.Command, args []string) error {
	CheckReadonly("merge-slot release")

	if err := store.MergeSlotRelease(rootCtx, mergeSlotHolder, actor); err != nil {
		return err
	}

	commandDidWrite.Store(true)

	if jsonOutput {
		slotID := storage.MergeSlotID(rootCtx, store)
		out := map[string]interface{}{
			"id":       slotID,
			"released": true,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(out)
	}

	slotID := storage.MergeSlotID(rootCtx, store)
	fmt.Printf("%s Released merge slot: %s\n", ui.RenderPass("✓"), slotID)
	return nil
}

// nilIfEmpty returns nil if s is empty, otherwise returns s.
// Used for JSON output where empty strings should be null.
func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
