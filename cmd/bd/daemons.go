package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/daemon"
)

var daemonsCmd = &cobra.Command{
	Use:   "daemons",
	Short: "Manage multiple bd daemons",
	Long: `Manage bd daemon processes across all repositories and worktrees.

Subcommands:
  list    - Show all running daemons
  health  - Check health of all daemons
  killall - Stop all running daemons`,
}

var daemonsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all running bd daemons",
	Long: `List all running bd daemons with metadata including workspace path, PID, version,
uptime, last activity, and exclusive lock status.`,
	Run: func(cmd *cobra.Command, args []string) {
		searchRoots, _ := cmd.Flags().GetStringSlice("search")
		jsonOutput, _ := cmd.Flags().GetBool("json")

		// Discover daemons
		daemons, err := daemon.DiscoverDaemons(searchRoots)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
			os.Exit(1)
		}

		// Auto-cleanup stale sockets (unless --no-cleanup flag is set)
		noCleanup, _ := cmd.Flags().GetBool("no-cleanup")
		if !noCleanup {
			cleaned, err := daemon.CleanupStaleSockets(daemons)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to cleanup stale sockets: %v\n", err)
			} else if cleaned > 0 && !jsonOutput {
				fmt.Fprintf(os.Stderr, "Cleaned up %d stale socket(s)\n", cleaned)
			}
		}

		// Filter to only alive daemons
		var aliveDaemons []daemon.DaemonInfo
		for _, d := range daemons {
			if d.Alive {
				aliveDaemons = append(aliveDaemons, d)
			}
		}

		if jsonOutput {
			data, _ := json.MarshalIndent(aliveDaemons, "", "  ")
			fmt.Println(string(data))
			return
		}

		// Human-readable table output
		if len(aliveDaemons) == 0 {
			fmt.Println("No running daemons found")
			return
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "WORKSPACE\tPID\tVERSION\tUPTIME\tLAST ACTIVITY\tLOCK")

		for _, d := range aliveDaemons {
			workspace := d.WorkspacePath
			if workspace == "" {
				workspace = "(unknown)"
			}

			uptime := formatDaemonDuration(d.UptimeSeconds)
			
			lastActivity := "(unknown)"
			if d.LastActivityTime != "" {
				if t, err := time.Parse(time.RFC3339, d.LastActivityTime); err == nil {
					lastActivity = formatDaemonRelativeTime(t)
				}
			}

			lock := "-"
			if d.ExclusiveLockActive {
				lock = fmt.Sprintf("🔒 %s", d.ExclusiveLockHolder)
			}

			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n",
				workspace, d.PID, d.Version, uptime, lastActivity, lock)
		}

		w.Flush()
	},
}

func formatDaemonDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	} else if d < time.Hour {
		return fmt.Sprintf("%.0fm", d.Minutes())
	} else if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}

func formatDaemonRelativeTime(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	} else if d < time.Hour {
		return fmt.Sprintf("%.0fm ago", d.Minutes())
	} else if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh ago", d.Hours())
	}
	return fmt.Sprintf("%.1fd ago", d.Hours()/24)
}

var daemonsHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check health of all bd daemons",
	Long: `Check health of all running bd daemons and report any issues including
stale sockets, version mismatches, and unresponsive daemons.`,
	Run: func(cmd *cobra.Command, args []string) {
		searchRoots, _ := cmd.Flags().GetStringSlice("search")
		jsonOutput, _ := cmd.Flags().GetBool("json")

		// Discover daemons
		daemons, err := daemon.DiscoverDaemons(searchRoots)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
			os.Exit(1)
		}

		type healthReport struct {
			Workspace        string `json:"workspace"`
			SocketPath       string `json:"socket_path"`
			PID              int    `json:"pid,omitempty"`
			Version          string `json:"version,omitempty"`
			Status           string `json:"status"`
			Issue            string `json:"issue,omitempty"`
			VersionMismatch  bool   `json:"version_mismatch,omitempty"`
		}

		var reports []healthReport
		healthyCount := 0
		staleCount := 0
		mismatchCount := 0
		unresponsiveCount := 0

		currentVersion := Version

		for _, d := range daemons {
			report := healthReport{
				Workspace:  d.WorkspacePath,
				SocketPath: d.SocketPath,
				PID:        d.PID,
				Version:    d.Version,
			}

			if !d.Alive {
				report.Status = "stale"
				report.Issue = d.Error
				staleCount++
			} else if d.Version != currentVersion {
				report.Status = "version_mismatch"
				report.Issue = fmt.Sprintf("daemon version %s != client version %s", d.Version, currentVersion)
				report.VersionMismatch = true
				mismatchCount++
			} else {
				report.Status = "healthy"
				healthyCount++
			}

			reports = append(reports, report)
		}

		if jsonOutput {
			output := map[string]interface{}{
				"total":        len(reports),
				"healthy":      healthyCount,
				"stale":        staleCount,
				"mismatched":   mismatchCount,
				"unresponsive": unresponsiveCount,
				"daemons":      reports,
			}
			data, _ := json.MarshalIndent(output, "", "  ")
			fmt.Println(string(data))
			return
		}

		// Human-readable output
		if len(reports) == 0 {
			fmt.Println("No daemons found")
			return
		}

		fmt.Printf("Health Check Summary:\n")
		fmt.Printf("  Total:        %d\n", len(reports))
		fmt.Printf("  Healthy:      %d\n", healthyCount)
		fmt.Printf("  Stale:        %d\n", staleCount)
		fmt.Printf("  Mismatched:   %d\n", mismatchCount)
		fmt.Printf("  Unresponsive: %d\n\n", unresponsiveCount)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "WORKSPACE\tPID\tVERSION\tSTATUS\tISSUE")

		for _, r := range reports {
			workspace := r.Workspace
			if workspace == "" {
				workspace = "(unknown)"
			}

			pidStr := "-"
			if r.PID != 0 {
				pidStr = fmt.Sprintf("%d", r.PID)
			}

			version := r.Version
			if version == "" {
				version = "-"
			}

			status := r.Status
			issue := r.Issue
			if issue == "" {
				issue = "-"
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				workspace, pidStr, version, status, issue)
		}

		w.Flush()

		// Exit with error if there are any issues
		if staleCount > 0 || mismatchCount > 0 || unresponsiveCount > 0 {
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(daemonsCmd)
	
	// Add subcommands
	daemonsCmd.AddCommand(daemonsListCmd)
	daemonsCmd.AddCommand(daemonsHealthCmd)
	
	// Flags for list command
	daemonsListCmd.Flags().StringSlice("search", nil, "Directories to search for daemons (default: home, /tmp, cwd)")
	daemonsListCmd.Flags().Bool("json", false, "Output in JSON format")
	daemonsListCmd.Flags().Bool("no-cleanup", false, "Skip auto-cleanup of stale sockets")

	// Flags for health command
	daemonsHealthCmd.Flags().StringSlice("search", nil, "Directories to search for daemons (default: home, /tmp, cwd)")
	daemonsHealthCmd.Flags().Bool("json", false, "Output in JSON format")
}
