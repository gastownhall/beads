package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/utils"
)

var slotCmd = &cobra.Command{
	Use:     "slot",
	GroupID: "issues",
	Short:   "Manage named slots on issues",
	Long: `Manage named key/value slots stored in an issue's metadata.

Slots are arbitrary string values associated with a named key on any issue.
They are stored in the issue's metadata JSON field.

Examples:
  bd slot set be-abc hook_bead be-xyz   # Set hook_bead slot on be-abc
  bd slot get be-abc hook_bead          # Get hook_bead slot from be-abc
  bd slot clear be-abc hook_bead        # Clear hook_bead slot on be-abc`,
}

var slotSetCmd = &cobra.Command{
	Use:   "set <issue> <key> <value>",
	Short: "Set a named slot on an issue",
	Args:  cobra.ExactArgs(3),
	RunE:  runSlotSet,
}

var slotGetCmd = &cobra.Command{
	Use:   "get <issue> <key>",
	Short: "Get the value of a named slot on an issue",
	Args:  cobra.ExactArgs(2),
	RunE:  runSlotGet,
}

var slotClearCmd = &cobra.Command{
	Use:   "clear <issue> <key>",
	Short: "Clear a named slot on an issue",
	Args:  cobra.ExactArgs(2),
	RunE:  runSlotClear,
}

func init() {
	slotCmd.AddCommand(slotSetCmd)
	slotCmd.AddCommand(slotGetCmd)
	slotCmd.AddCommand(slotClearCmd)
	rootCmd.AddCommand(slotCmd)
}

func runSlotSet(cmd *cobra.Command, args []string) error {
	CheckReadonly("slot set")

	ctx := rootCtx
	issueArg, key, value := args[0], args[1], args[2]

	issueID, err := utils.ResolvePartialID(ctx, store, issueArg)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", issueArg, err)
	}

	if err := store.SlotSet(ctx, issueID, key, value, actor); err != nil {
		return fmt.Errorf("slot set: %w", err)
	}

	if jsonOutput {
		outputJSON(map[string]string{"issue_id": issueID, "key": key, "value": value})
	} else {
		fmt.Printf("Set slot %q on %s\n", key, issueID)
	}
	return nil
}

func runSlotGet(cmd *cobra.Command, args []string) error {
	ctx := rootCtx
	issueArg, key := args[0], args[1]

	issueID, err := utils.ResolvePartialID(ctx, store, issueArg)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", issueArg, err)
	}

	value, err := store.SlotGet(ctx, issueID, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			if jsonOutput {
				outputJSON(map[string]interface{}{"issue_id": issueID, "key": key, "value": nil})
			} else {
				fmt.Printf("(not set)\n")
			}
			return nil
		}
		return fmt.Errorf("slot get: %w", err)
	}

	if jsonOutput {
		outputJSON(map[string]string{"issue_id": issueID, "key": key, "value": value})
	} else {
		fmt.Println(value)
	}
	return nil
}

func runSlotClear(cmd *cobra.Command, args []string) error {
	CheckReadonly("slot clear")

	ctx := rootCtx
	issueArg, key := args[0], args[1]

	issueID, err := utils.ResolvePartialID(ctx, store, issueArg)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", issueArg, err)
	}

	if err := store.SlotClear(ctx, issueID, key, actor); err != nil {
		return fmt.Errorf("slot clear: %w", err)
	}

	if jsonOutput {
		outputJSON(map[string]string{"issue_id": issueID, "key": key, "status": "cleared"})
	} else {
		fmt.Printf("Cleared slot %q on %s\n", key, issueID)
	}
	return nil
}
