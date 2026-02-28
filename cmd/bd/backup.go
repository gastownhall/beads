package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
)

var backupForce bool

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Back up your beads database",
	Long: `Back up your beads database for off-machine recovery.

Without a subcommand, exports all tables to JSONL files in .beads/backup/.
Events are exported incrementally using a high-water mark.

For Dolt-native backups (preserves full commit history, faster for large databases):
  bd backup init <path>     Set up a backup destination (filesystem or DoltHub)
  bd backup sync            Push to configured backup destination

Other subcommands:
  bd backup status          Show backup status (JSONL + Dolt)
  bd backup restore [path]  Restore from JSONL backup files

DoltHub is recommended for cloud backup:
  bd backup init https://doltremoteapi.dolthub.com/<user>/<repo>
  Set DOLT_REMOTE_USER and DOLT_REMOTE_PASSWORD for authentication.

Note: Git-protocol remotes are NOT recommended for Dolt backups — push times
exceed 20 minutes, cache grows unboundedly, and force-push is needed after recovery.`,
	GroupID: "sync",
	RunE: func(cmd *cobra.Command, args []string) error {
		state, err := runBackupExport(rootCtx, backupForce)
		if err != nil {
			return err
		}

		if jsonOutput {
			data, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		fmt.Printf("Backup complete: %d issues, %d events, %d comments, %d deps, %d labels, %d config\n",
			state.Counts.Issues, state.Counts.Events, state.Counts.Comments,
			state.Counts.Dependencies, state.Counts.Labels, state.Counts.Config)

		// Optional git push
		if isBackupGitPushEnabled() {
			if err := gitBackup(rootCtx); err != nil {
				return err
			}
			fmt.Println("Backup committed and pushed to git.")
		}

		return nil
	},
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
				"jsonl": state,
				"dolt":  showDoltBackupStatusJSON(),
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

		hasJSONL := state.LastDoltCommit != ""
		hasDolt := false
		if cfg, _ := loadDoltBackupConfig(); cfg != nil {
			hasDolt = true
		}

		if !hasJSONL && !hasDolt {
			fmt.Println("No backup has been performed yet.")
			fmt.Println()
			fmt.Println("JSONL backup (portable, git-friendly):")
			fmt.Println("  bd backup                Run JSONL export now")
			fmt.Println("  Set backup.enabled: true in config.yaml for auto-backup")
			fmt.Println()
			fmt.Println("Dolt backup (preserves history, faster for large databases):")
			fmt.Println("  bd backup init <path>    Set up a backup destination")
			fmt.Println("  bd backup sync           Push to backup destination")
			showDBSize()
			return nil
		}

		if hasJSONL {
			fmt.Println("JSONL Backup:")
			fmt.Printf("  Last backup: %s (%s ago)\n",
				state.Timestamp.Format(time.RFC3339),
				time.Since(state.Timestamp).Round(time.Second))
			fmt.Printf("  Dolt commit: %s\n", state.LastDoltCommit)
			fmt.Printf("  Event high-water mark: %d\n", state.LastEventID)
			fmt.Printf("  Counts: %d issues, %d events, %d comments, %d deps, %d labels, %d config\n",
				state.Counts.Issues, state.Counts.Events, state.Counts.Comments,
				state.Counts.Dependencies, state.Counts.Labels, state.Counts.Config)
		}

		// Show config (effective values with source)
		enabled := isBackupAutoEnabled()
		interval := config.GetDuration("backup.interval")
		gitPush := isBackupGitPushEnabled()
		enabledSource := config.GetValueSource("backup.enabled")
		gitPushSource := config.GetValueSource("backup.git-push")
		enabledNote := ""
		if enabledSource == config.SourceDefault {
			if enabled {
				enabledNote = " (auto: git remote detected)"
			} else {
				enabledNote = " (auto: no git remote)"
			}
		}
		gitPushNote := ""
		if gitPushSource == config.SourceDefault {
			if gitPush {
				gitPushNote = " (auto)"
			}
		}
		fmt.Printf("\nConfig: enabled=%v%s interval=%s git-push=%v%s\n",
			enabled, enabledNote, interval, gitPush, gitPushNote)
		if gitRepo := config.GetString("backup.git-repo"); gitRepo != "" {
			fmt.Printf("Git repo: %s\n", gitRepo)
		}

		// Show Dolt backup info
		showDoltBackupStatus()

		// Show database size
		showDBSize()

		return nil
	},
}

func init() {
	backupCmd.Flags().BoolVar(&backupForce, "force", false, "Export even if nothing changed")
	backupCmd.AddCommand(backupStatusCmd)
	rootCmd.AddCommand(backupCmd)
}
