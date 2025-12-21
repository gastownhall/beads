package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

var upgradeCmd = &cobra.Command{
	Use:     "upgrade",
	GroupID: "maint",
	Short:   "Check and manage bd version upgrades",
	Long: `Commands for checking bd version upgrades and reviewing changes.

The upgrade command helps you stay aware of bd version changes:
  - bd upgrade status: Check if bd version changed since last use
  - bd upgrade review: Show what's new since your last version
  - bd upgrade ack: Acknowledge the current version

Version tracking is automatic - bd updates metadata.json on every run.`,
}

var upgradeStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check if bd version has changed",
	Long: `Check if bd has been upgraded since you last used it.

This command uses the version tracking that happens automatically
at startup to detect if bd was upgraded.

Examples:
  bd upgrade status
  bd upgrade status --json`,
	Run: func(cmd *cobra.Command, args []string) {
		// Use in-memory state from trackBdVersion() which runs in PersistentPreRun
		if jsonOutput {
			result := map[string]interface{}{
				"upgraded":        versionUpgradeDetected,
				"current_version": Version,
			}
			if versionUpgradeDetected {
				result["previous_version"] = previousVersion
				result["changes_available"] = len(getVersionsSince(previousVersion)) > 0
			}
			outputJSON(result)
			return
		}

		// Human-readable output
		if versionUpgradeDetected {
			fmt.Printf("✨ bd upgraded from v%s to v%s\n", previousVersion, Version)
			newVersions := getVersionsSince(previousVersion)
			if len(newVersions) > 0 {
				fmt.Printf("   %d version%s with changes available\n",
					len(newVersions),
					pluralize(len(newVersions)))
				fmt.Println()
				fmt.Println("Run 'bd upgrade review' to see what changed")
			}
		} else if previousVersion == "" {
			fmt.Printf("bd version: v%s (first run or version tracking just enabled)\n", Version)
		} else {
			fmt.Printf("bd version: v%s (no upgrade detected)\n", Version)
		}
	},
}

var upgradeReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Review changes since last bd version",
	Long: `Show what's new in bd since the last version you used.

Unlike 'bd info --whats-new' which shows the last 3 versions,
this command shows ALL changes since your specific last version.

If you're upgrading from an old version, you'll see the complete
changelog of everything that changed since then.

Examples:
  bd upgrade review
  bd upgrade review --json`,
	Run: func(cmd *cobra.Command, args []string) {
		// Use in-memory state from trackBdVersion() which runs in PersistentPreRun
		lastVersion := previousVersion

		if lastVersion == "" {
			fmt.Println("No previous version recorded")
			fmt.Println("Run 'bd info --whats-new' to see recent changes")
			return
		}

		if !versionUpgradeDetected {
			fmt.Printf("You're already on v%s (no upgrade detected)\n", Version)
			fmt.Println("Run 'bd info --whats-new' to see recent changes")
			return
		}

		newVersions := getVersionsSince(lastVersion)

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"current_version":  Version,
				"previous_version": lastVersion,
				"new_versions":     newVersions,
			})
			return
		}

		// Human-readable output
		fmt.Printf("\n🔄 Upgraded from v%s to v%s\n", lastVersion, Version)
		fmt.Println(strings.Repeat("=", 60))
		fmt.Println()

		if len(newVersions) == 0 {
			fmt.Printf("v%s is newer than v%s but not in changelog\n", Version, lastVersion)
			fmt.Println("Run 'bd info --whats-new' to see recent documented changes")
			return
		}

		for _, vc := range newVersions {
			versionMarker := ""
			if vc.Version == Version {
				versionMarker = " ← current"
			}

			fmt.Printf("## v%s (%s)%s\n\n", vc.Version, vc.Date, versionMarker)

			for _, change := range vc.Changes {
				fmt.Printf("  • %s\n", change)
			}
			fmt.Println()
		}

		fmt.Println("💡 Run 'bd upgrade ack' to mark this version as seen")
		fmt.Println()
	},
}

var upgradeAckCmd = &cobra.Command{
	Use:   "ack",
	Short: "Acknowledge the current bd version",
	Long: `Mark the current bd version as acknowledged.

This updates metadata.json to record that you've seen the current
version. Mainly useful after reviewing upgrade changes to suppress
future upgrade notifications.

Note: Version tracking happens automatically, so you don't need to
run this command unless you want to explicitly mark acknowledgement.

Examples:
  bd upgrade ack
  bd upgrade ack --json`,
	Run: func(cmd *cobra.Command, args []string) {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			fmt.Println("Error: No .beads directory found")
			return
		}

		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			fmt.Printf("Error loading metadata.json: %v\n", err)
			return
		}
		if cfg == nil {
			cfg = configfile.DefaultConfig()
		}

		lastSeenVersion := cfg.LastBdVersion
		cfg.LastBdVersion = Version

		if err := cfg.Save(beadsDir); err != nil {
			fmt.Printf("Error saving metadata.json: %v\n", err)
			return
		}

		// Mark as acknowledged in current session
		upgradeAcknowledged = true
		versionUpgradeDetected = false

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"acknowledged":     true,
				"current_version":  Version,
				"previous_version": lastSeenVersion,
			})
			return
		}

		if lastSeenVersion == Version {
			fmt.Printf("✓ Already on v%s\n", Version)
		} else if lastSeenVersion == "" {
			fmt.Printf("✓ Acknowledged bd v%s\n", Version)
		} else {
			fmt.Printf("✓ Acknowledged upgrade from v%s to v%s\n", lastSeenVersion, Version)
		}
	},
}

func pluralize(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func init() {
	upgradeCmd.AddCommand(upgradeStatusCmd)
	upgradeCmd.AddCommand(upgradeReviewCmd)
	upgradeCmd.AddCommand(upgradeAckCmd)
	rootCmd.AddCommand(upgradeCmd)
}
