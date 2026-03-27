package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Back up your beads database",
	Long: `Back up your beads database for off-machine recovery.

Commands:
  bd backup add <path>     Add a backup destination (filesystem or DoltHub)
  bd backup sync           Push to configured backup destination
  bd backup restore [path] Restore from a backup directory
  bd backup status         Show backup status

DoltHub is recommended for cloud backup:
  bd backup add https://doltremoteapi.dolthub.com/<user>/<repo>
  Set DOLT_REMOTE_USER and DOLT_REMOTE_PASSWORD for authentication.`,
	GroupID: "sync",
}

var backupStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show last backup status",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := backupDir()
		if err != nil {
			return err
		}

		state, err := loadBackupState(dir)
		if err != nil {
			return err
		}

		if jsonOutput {
			result := map[string]interface{}{
				"backup": state,
				"dolt":   showDoltBackupStatusJSON(),
			}
			if dbSize := showDBSizeJSON(); dbSize != nil {
				result["database_size"] = dbSize
			}
			data, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		hasBackup := state.LastDoltCommit != ""
		hasDolt := false
		if cfg, _ := loadDoltBackupConfig(); cfg != nil {
			hasDolt = true
		}

		if !hasBackup && !hasDolt {
			fmt.Println("No backup has been performed yet.")
			fmt.Println()
			fmt.Println("Setup:")
			fmt.Println("  bd backup init <path>    Set up a backup destination")
			fmt.Println("  bd backup sync           Push to backup destination")
			showDBSize()
			return nil
		}

		if hasBackup {
			fmt.Println("Backup:")
			fmt.Printf("  Last backup: %s (%s ago)\n",
				state.Timestamp.Format(time.RFC3339),
				time.Since(state.Timestamp).Round(time.Second))
			fmt.Printf("  Dolt commit: %s\n", state.LastDoltCommit)
		}

		// Show config (effective values with source)
		enabled := isBackupAutoEnabled()
		interval := config.GetDuration("backup.interval")
		enabledSource := config.GetValueSource("backup.enabled")
		enabledNote := ""
		if enabledSource == config.SourceDefault {
			if enabled {
				enabledNote = " (auto: git remote detected)"
			} else {
				enabledNote = " (auto: no git remote)"
			}
		}
		fmt.Printf("\nConfig: enabled=%v%s interval=%s\n",
			enabled, enabledNote, interval)

		// Show Dolt backup info
		showDoltBackupStatus()

		// Show database size
		showDBSize()

		return nil
	},
}

func init() {
	backupCmd.AddCommand(backupStatusCmd)
	rootCmd.AddCommand(backupCmd)
}
