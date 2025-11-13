package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/rpc"
)

var (
	// Version is the current version of bd (overridden by ldflags at build time)
	Version = "0.23.1"
	// Build can be set via ldflags at compile time
	Build = "dev"
	// Commit is the git revision the binary was built from (optional ldflag)
	Commit = ""
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		checkDaemon, _ := cmd.Flags().GetBool("daemon")

		if checkDaemon {
			showDaemonVersion()
			return
		}

		commit := resolveCommitHash()

		if jsonOutput {
			result := map[string]string{
				"version": Version,
				"build":   Build,
			}
			if commit != "" {
				result["commit"] = commit
			}
			outputJSON(result)
		} else {
			if commit != "" {
				fmt.Printf("bd version %s (%s, commit %s)\n", Version, Build, shortCommit(commit))
			} else {
				fmt.Printf("bd version %s (%s)\n", Version, Build)
			}
		}
	},
}

func showDaemonVersion() {
	// Connect to daemon (PersistentPreRun skips version command)
	// We need to find the database path first to get the socket path
	if dbPath == "" {
		// Use public API to find database (same logic as PersistentPreRun)
		if foundDB := beads.FindDatabasePath(); foundDB != "" {
			dbPath = foundDB
		}
	}

	socketPath := getSocketPath()
	client, err := rpc.TryConnect(socketPath)
	if err != nil || client == nil {
		fmt.Fprintf(os.Stderr, "Error: daemon is not running\n")
		fmt.Fprintf(os.Stderr, "Hint: start daemon with 'bd daemon'\n")
		os.Exit(1)
	}
	defer func() { _ = client.Close() }()

	health, err := client.Health()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking daemon health: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"daemon_version": health.Version,
			"client_version": Version,
			"compatible":     health.Compatible,
			"daemon_uptime":  health.Uptime,
		})
	} else {
		fmt.Printf("Daemon version: %s\n", health.Version)
		fmt.Printf("Client version: %s\n", Version)
		if health.Compatible {
			fmt.Printf("Compatibility: ✓ compatible\n")
		} else {
			fmt.Printf("Compatibility: ✗ incompatible (restart daemon recommended)\n")
		}
		fmt.Printf("Daemon uptime: %.1f seconds\n", health.Uptime)
	}

	if !health.Compatible {
		os.Exit(1)
	}
}

func init() {
	versionCmd.Flags().Bool("daemon", false, "Check daemon version and compatibility")
	rootCmd.AddCommand(versionCmd)
}

func resolveCommitHash() string {
	if Commit != "" {
		return Commit
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}

	return ""
}

func shortCommit(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
