package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
)

// Sync mode info for display
var syncModeInfo = []struct {
	Mode        string
	Description string
}{
	{SyncModeGitPortable, "JSONL exported on push, imported on pull (default)"},
	{SyncModeRealtime, "JSONL exported on every change (more git noise)"},
	{SyncModeDoltNative, "Dolt remotes for sync, JSONL export-only backup (requires Dolt backend)"},
	{SyncModeBeltAndSuspenders, "Both Dolt remotes and JSONL (maximum redundancy)"},
}

var syncModeCmd = &cobra.Command{
	Use:   "mode",
	Short: "Manage sync mode configuration",
	Long: `Manage sync mode configuration.

Sync mode controls how beads synchronizes data with git:

  git-portable (default)
    JSONL exported on push, imported on pull.
    Works with standard git workflows.

  realtime
    JSONL exported on every database change.
    Provides immediate persistence but more git noise.

  dolt-native
    Dolt remotes for sync, JSONL export-only (backup).
    Requires Dolt backend and configured Dolt remote.

  belt-and-suspenders
    Uses both Dolt remotes AND JSONL.
    Maximum redundancy - Dolt for versioning, JSONL for git portability.

Commands:
  bd sync mode list      List available sync modes
  bd sync mode current   Show current sync mode
  bd sync mode set       Set sync mode`,
}

var syncModeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available sync modes",
	Long: `List all available sync modes and their descriptions.

Sync modes control how beads synchronizes with git and Dolt remotes.`,
	Run: func(cmd *cobra.Command, args []string) {
		if jsonOutput {
			modes := make([]map[string]string, len(syncModeInfo))
			for i, m := range syncModeInfo {
				modes[i] = map[string]string{
					"mode":        m.Mode,
					"description": m.Description,
				}
			}
			outputJSON(map[string]interface{}{
				"modes": modes,
			})
			return
		}

		fmt.Println("Available sync modes:")
		for _, m := range syncModeInfo {
			fmt.Printf("  %-22s %s\n", m.Mode, m.Description)
		}
		fmt.Println("\nSet sync mode: bd sync mode set <mode>")
	},
}

var syncModeCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show current sync mode",
	Long:  `Show the currently configured sync mode.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Try to get from database if available
		var currentMode string
		if store != nil {
			currentMode = GetSyncMode(rootCtx, store)
		} else {
			// Fall back to config.yaml
			currentMode = string(config.GetSyncMode())
		}

		if jsonOutput {
			result := map[string]interface{}{
				"mode": currentMode,
			}
			// Add description
			for _, m := range syncModeInfo {
				if m.Mode == currentMode {
					result["description"] = m.Description
					break
				}
			}
			outputJSON(result)
			return
		}

		fmt.Printf("Current sync mode: %s\n", currentMode)
		// Show description
		for _, m := range syncModeInfo {
			if m.Mode == currentMode {
				fmt.Printf("  %s\n", m.Description)
				break
			}
		}
	},
}

var syncModeSetCmd = &cobra.Command{
	Use:   "set <mode>",
	Short: "Set sync mode",
	Long: `Set the sync mode.

Valid modes:
  git-portable          JSONL exported on push, imported on pull (default)
  realtime              JSONL exported on every change
  dolt-native           Dolt remotes for sync, JSONL export-only backup (requires Dolt backend)
  belt-and-suspenders   Both Dolt remotes and JSONL

Example:
  bd sync mode set realtime
  bd sync mode set dolt-native`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("sync mode set")

		mode := strings.TrimSpace(args[0])

		// Validate mode
		if !config.IsValidSyncMode(mode) {
			fmt.Fprintf(os.Stderr, "Error: invalid sync mode: %s\n", mode)
			fmt.Fprintf(os.Stderr, "Valid modes: %s\n", strings.Join(config.ValidSyncModes(), ", "))
			os.Exit(1)
		}

		// Require direct mode for database writes
		if err := ensureDirectMode("sync mode set requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Set the mode
		if err := SetSyncMode(rootCtx, store, mode); err != nil {
			fmt.Fprintf(os.Stderr, "Error setting sync mode: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"mode": mode,
			}
			// Add description
			for _, m := range syncModeInfo {
				if m.Mode == mode {
					result["description"] = m.Description
					break
				}
			}
			outputJSON(result)
			return
		}

		fmt.Printf("Sync mode set to: %s\n", mode)
		// Show description
		for _, m := range syncModeInfo {
			if m.Mode == mode {
				fmt.Printf("  %s\n", m.Description)
				break
			}
		}
	},
}

func init() {
	syncModeCmd.AddCommand(syncModeListCmd)
	syncModeCmd.AddCommand(syncModeCurrentCmd)
	syncModeCmd.AddCommand(syncModeSetCmd)
	syncCmd.AddCommand(syncModeCmd)
}
