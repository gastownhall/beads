package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
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

If the slot is available (status=open), it will be acquired atomically:
  - status set to in_progress
  - metadata.holder set to the requester

If the slot is held (status=in_progress), the command fails and the
requester is optionally added to the waiters list (use --wait flag).

Use --holder to specify who is acquiring (default: BEADS_ACTOR env var).`,
	Args: cobra.NoArgs,
	RunE: runMergeSlotAcquire,
}

// mergeSlotReleaseCmd releases the merge slot
var mergeSlotReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release the merge slot",
	Long: `Release the merge slot after conflict resolution is complete.

Sets status back to open and clears the metadata.holder field.
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

// slotMetadata holds the merge slot state stored in the issue metadata field.
type slotMetadata struct {
	Holder  string   `json:"holder,omitempty"`
	Waiters []string `json:"waiters,omitempty"`
}

// getMergeSlotID returns the merge slot bead ID for the current rig.
func getMergeSlotID() string {
	prefix := "bd"
	if configPrefix := config.GetString("issue-prefix"); configPrefix != "" {
		prefix = strings.TrimSuffix(configPrefix, "-")
	} else if store != nil {
		if dbPrefix, err := store.GetConfig(rootCtx, "issue_prefix"); err == nil && dbPrefix != "" {
			prefix = strings.TrimSuffix(dbPrefix, "-")
		}
	}
	return prefix + "-merge-slot"
}

// parseSlotMetadata extracts holder and waiters from an issue's Metadata field.
func parseSlotMetadata(issue *types.Issue) slotMetadata {
	var meta slotMetadata
	if len(issue.Metadata) > 0 {
		_ = json.Unmarshal(issue.Metadata, &meta)
	}
	return meta
}

// encodeSlotMetadata serializes slot metadata to a JSON string for storage.
func encodeSlotMetadata(meta slotMetadata) (string, error) {
	b, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func runMergeSlotCreate(cmd *cobra.Command, args []string) error {
	CheckReadonly("merge-slot create")

	slotID := getMergeSlotID()
	ctx := rootCtx

	// Check if slot already exists
	existing, _ := store.GetIssue(ctx, slotID)
	if existing != nil {
		fmt.Printf("Merge slot already exists: %s\n", slotID)
		return nil
	}

	issue := &types.Issue{
		ID:          slotID,
		Title:       "Merge Slot",
		Description: "Exclusive access slot for serialized conflict resolution in the merge queue.",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    0,
	}
	if err := store.CreateIssue(ctx, issue, actor); err != nil {
		return fmt.Errorf("failed to create merge slot: %w", err)
	}
	if err := store.AddLabel(ctx, slotID, "gt:slot", actor); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to add gt:slot label: %v\n", err)
	}

	if isEmbeddedDolt && store != nil {
		if _, err := store.CommitPending(ctx, actor); err != nil {
			return fmt.Errorf("failed to commit: %w", err)
		}
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

	slot, err := store.GetIssue(ctx, slotID)
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

	meta := parseSlotMetadata(slot)
	available := slot.Status == types.StatusOpen

	if jsonOutput {
		result := map[string]interface{}{
			"id":        slotID,
			"available": available,
			"status":    string(slot.Status),
			"holder":    nilIfEmpty(meta.Holder),
			"waiters":   meta.Waiters,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	if available {
		fmt.Printf("%s Merge slot available: %s\n", ui.RenderPass("✓"), slotID)
	} else {
		fmt.Printf("%s Merge slot held: %s\n", ui.RenderAccent("○"), slotID)
		fmt.Printf("  Holder: %s\n", meta.Holder)
		if len(meta.Waiters) > 0 {
			fmt.Printf("  Waiters: %d\n", len(meta.Waiters))
			for i, w := range meta.Waiters {
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

	holder := mergeSlotHolder
	if holder == "" {
		holder = actor
	}
	if holder == "" {
		return fmt.Errorf("no holder specified; use --holder or set BEADS_ACTOR env var")
	}

	type acquireResult struct {
		acquired bool
		waiting  bool
		holder   string
		position int
		slotID   string
	}

	var result acquireResult

	err := transact(ctx, store, fmt.Sprintf("bd: acquire merge slot %s for %s", slotID, holder), func(tx storage.Transaction) error {
		slot, err := tx.GetIssue(ctx, slotID)
		if err != nil || slot == nil {
			return fmt.Errorf("merge slot not found: %s (run 'bd merge-slot create' first)", slotID)
		}

		meta := parseSlotMetadata(slot)
		result.slotID = slot.ID
		result.holder = meta.Holder

		if slot.Status != types.StatusOpen {
			// Slot is held
			if mergeSlotAddWaiter {
				alreadyWaiting := false
				for _, w := range meta.Waiters {
					if w == holder {
						alreadyWaiting = true
						break
					}
				}
				if !alreadyWaiting {
					meta.Waiters = append(meta.Waiters, holder)
				}
				metaStr, err := encodeSlotMetadata(meta)
				if err != nil {
					return fmt.Errorf("failed to encode metadata: %w", err)
				}
				if err := tx.UpdateIssue(ctx, slot.ID, map[string]interface{}{"metadata": metaStr}, actor); err != nil {
					return fmt.Errorf("failed to add waiter: %w", err)
				}
				result.waiting = true
				result.position = len(meta.Waiters)
			}
			return nil
		}

		// Slot is available — acquire it
		newMeta := slotMetadata{
			Holder:  holder,
			Waiters: meta.Waiters,
		}
		metaStr, err := encodeSlotMetadata(newMeta)
		if err != nil {
			return fmt.Errorf("failed to encode metadata: %w", err)
		}
		updates := map[string]interface{}{
			"status":   types.StatusInProgress,
			"metadata": metaStr,
		}
		if err := tx.UpdateIssue(ctx, slot.ID, updates, actor); err != nil {
			return fmt.Errorf("failed to acquire slot: %w", err)
		}
		result.acquired = true
		result.holder = holder
		return nil
	})
	if err != nil {
		return err
	}

	if !result.acquired && !result.waiting {
		// Slot was held, no --wait flag
		if jsonOutput {
			out := map[string]interface{}{
				"id":       result.slotID,
				"acquired": false,
				"holder":   result.holder,
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(out)
		} else {
			fmt.Printf("%s Slot held by: %s\n", ui.RenderFail("✗"), result.holder)
			fmt.Printf("Use --wait to add yourself to the waiters queue.\n")
		}
		os.Exit(1)
	}

	if result.waiting {
		if jsonOutput {
			out := map[string]interface{}{
				"id":       result.slotID,
				"acquired": false,
				"waiting":  true,
				"holder":   result.holder,
				"position": result.position,
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(out)
		} else {
			fmt.Printf("%s Slot held by %s, added to waiters queue (position %d)\n",
				ui.RenderAccent("○"), result.holder, result.position)
		}
		os.Exit(1)
	}

	// Successfully acquired
	if jsonOutput {
		out := map[string]interface{}{
			"id":       result.slotID,
			"acquired": true,
			"holder":   holder,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(out)
	}

	fmt.Printf("%s Acquired merge slot: %s\n", ui.RenderPass("✓"), result.slotID)
	fmt.Printf("  Holder: %s\n", holder)
	return nil
}

func runMergeSlotRelease(cmd *cobra.Command, args []string) error {
	CheckReadonly("merge-slot release")

	slotID := getMergeSlotID()
	ctx := rootCtx

	type releaseResult struct {
		released       bool
		notHeld        bool
		previousHolder string
		numWaiters     int
		nextWaiter     string
		slotID         string
	}

	var result releaseResult

	err := transact(ctx, store, fmt.Sprintf("bd: release merge slot %s", slotID), func(tx storage.Transaction) error {
		slot, err := tx.GetIssue(ctx, slotID)
		if err != nil || slot == nil {
			return fmt.Errorf("merge slot not found: %s", slotID)
		}

		meta := parseSlotMetadata(slot)
		result.slotID = slot.ID

		if mergeSlotHolder != "" && meta.Holder != mergeSlotHolder {
			return fmt.Errorf("slot held by %s, not %s", meta.Holder, mergeSlotHolder)
		}

		if slot.Status == types.StatusOpen {
			result.notHeld = true
			return nil
		}

		result.previousHolder = meta.Holder
		result.numWaiters = len(meta.Waiters)
		if len(meta.Waiters) > 0 {
			result.nextWaiter = meta.Waiters[0]
		}

		newMeta := slotMetadata{
			Waiters: meta.Waiters,
		}
		metaStr, err := encodeSlotMetadata(newMeta)
		if err != nil {
			return fmt.Errorf("failed to encode metadata: %w", err)
		}
		updates := map[string]interface{}{
			"status":   types.StatusOpen,
			"metadata": metaStr,
		}
		if err := tx.UpdateIssue(ctx, slot.ID, updates, actor); err != nil {
			return fmt.Errorf("failed to release slot: %w", err)
		}
		result.released = true
		return nil
	})
	if err != nil {
		return err
	}

	if result.notHeld {
		if jsonOutput {
			out := map[string]interface{}{
				"id":       result.slotID,
				"released": false,
				"error":    "slot not held",
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(out)
		}
		fmt.Printf("Slot is not held: %s\n", result.slotID)
		return nil
	}

	if jsonOutput {
		out := map[string]interface{}{
			"id":              result.slotID,
			"released":        true,
			"previous_holder": result.previousHolder,
			"waiters":         result.numWaiters,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(out)
	}

	fmt.Printf("%s Released merge slot: %s\n", ui.RenderPass("✓"), result.slotID)
	fmt.Printf("  Previous holder: %s\n", result.previousHolder)
	if result.numWaiters > 0 {
		fmt.Printf("  Waiters pending: %d\n", result.numWaiters)
		fmt.Printf("  Next in queue: %s\n", result.nextWaiter)
	}

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
