package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/utils"
)

// JSON response types for daemons commands

// DaemonStopResponse is returned when a daemon is stopped
type DaemonStopResponse struct {
	Workspace string `json:"workspace"`
	PID       int    `json:"pid"`
	Stopped   bool   `json:"stopped"`
}

// DaemonRestartResponse is returned when a daemon is restarted
type DaemonRestartResponse struct {
	Workspace string `json:"workspace"`
	Action    string `json:"action"`
}

// DaemonLogsResponse is returned for daemon logs in JSON mode
type DaemonLogsResponse struct {
	Workspace string `json:"workspace"`
	LogPath   string `json:"log_path"`
	Content   string `json:"content"`
}

// DaemonKillallEmptyResponse is returned when no daemons are running
type DaemonKillallEmptyResponse struct {
	Stopped int `json:"stopped"`
	Failed  int `json:"failed"`
}

// DaemonHealthReport is a single daemon health report entry
type DaemonHealthReport struct {
	Workspace       string `json:"workspace"`
	SocketPath      string `json:"socket_path"`
	PID             int    `json:"pid,omitempty"`
	Version         string `json:"version,omitempty"`
	Status          string `json:"status"`
	Issue           string `json:"issue,omitempty"`
	VersionMismatch bool   `json:"version_mismatch,omitempty"`
}

// DaemonHealthResponse is returned for daemon health check
type DaemonHealthResponse struct {
	Total        int                  `json:"total"`
	Healthy      int                  `json:"healthy"`
	Stale        int                  `json:"stale"`
	Mismatched   int                  `json:"mismatched"`
	Unresponsive int                  `json:"unresponsive"`
	Daemons      []DaemonHealthReport `json:"daemons"`
}

// daemonsCmd is no longer needed — "daemons" is now a Cobra alias on daemonCmd.
// See daemon.go for the primary command definition.
var daemonsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all running bd daemons",
	Long: `List all running bd daemons with metadata including workspace path, PID, version,
uptime, last activity, and exclusive lock status.`,
	Run: func(cmd *cobra.Command, args []string) {
		searchRoots, _ := cmd.Flags().GetStringSlice("search")
		// Use global jsonOutput set by PersistentPreRun
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
		_, _ = fmt.Fprintln(w, "WORKSPACE\tPID\tVERSION\tUPTIME\tLAST ACTIVITY\tLOCK")
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
			_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n",
				workspace, d.PID, d.Version, uptime, lastActivity, lock)
		}
		_ = w.Flush()
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

var daemonsStopCmd = &cobra.Command{
	Use:   "stop <workspace-path|pid>",
	Short: "Stop a specific bd daemon",
	Long: `Stop a specific bd daemon gracefully by workspace path or PID.
Sends shutdown command via RPC, with SIGTERM fallback if RPC fails.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		target := args[0]
		// Use global jsonOutput set by PersistentPreRun
		// Discover all daemons
		daemons, err := daemon.DiscoverDaemons(nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
			os.Exit(1)
		}
		// Find matching daemon by workspace path or PID
		// Use PathsEqual for case-insensitive comparison on macOS/Windows (GH#869)
		var targetDaemon *daemon.DaemonInfo
		for _, d := range daemons {
			if utils.PathsEqual(d.WorkspacePath, target) || fmt.Sprintf("%d", d.PID) == target {
				targetDaemon = &d
				break
			}
		}
		if targetDaemon == nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": "daemon not found"})
			} else {
				fmt.Fprintf(os.Stderr, "Error: daemon not found for %s\n", target)
			}
			os.Exit(1)
		}
		// Stop the daemon
		if err := daemon.StopDaemon(*targetDaemon); err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": err.Error()})
			} else {
				fmt.Fprintf(os.Stderr, "Error stopping daemon: %v\n", err)
			}
			os.Exit(1)
		}
		if jsonOutput {
			outputJSON(DaemonStopResponse{
				Workspace: targetDaemon.WorkspacePath,
				PID:       targetDaemon.PID,
				Stopped:   true,
			})
		} else {
			fmt.Printf("Stopped daemon for %s (PID %d)\n", targetDaemon.WorkspacePath, targetDaemon.PID)
		}
	},
}
var daemonsRestartCmd = &cobra.Command{
	Use:   "restart <workspace-path|pid>",
	Short: "Restart a specific bd daemon",
	Long: `Restart a specific bd daemon by workspace path or PID.
Stops the daemon gracefully, then starts a new one.`,
	Args:    cobra.ExactArgs(1),
	PreRunE: guardDaemonStartForDolt,
	Run: func(cmd *cobra.Command, args []string) {
		target := args[0]
		searchRoots, _ := cmd.Flags().GetStringSlice("search")
		// Use global jsonOutput set by PersistentPreRun
		// Discover daemons
		daemons, err := daemon.DiscoverDaemons(searchRoots)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
			os.Exit(1)
		}
		// Find the target daemon
		// Use PathsEqual for case-insensitive comparison on macOS/Windows (GH#869)
		var targetDaemon *daemon.DaemonInfo
		for _, d := range daemons {
			if utils.PathsEqual(d.WorkspacePath, target) || fmt.Sprintf("%d", d.PID) == target {
				targetDaemon = &d
				break
			}
		}
		if targetDaemon == nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": "daemon not found"})
			} else {
				fmt.Fprintf(os.Stderr, "Error: daemon not found for %s\n", target)
			}
			os.Exit(1)
		}
		workspace := targetDaemon.WorkspacePath

		// Guardrail: don't (re)start daemons for single-process backends (e.g., embedded Dolt).
		// This command may be run from a different workspace, so check the target workspace.
		// Note: Dolt server mode supports multi-process, so GetCapabilities() is used.
		targetBeadsDir := beads.FollowRedirect(filepath.Join(workspace, ".beads"))
		if cfg, err := configfile.Load(targetBeadsDir); err == nil && cfg != nil {
			if cfg.GetCapabilities().SingleProcessOnly {
				if jsonOutput {
					outputJSON(map[string]string{"error": fmt.Sprintf("daemon mode is not supported for backend %q (single-process only)", cfg.GetBackend())})
				} else {
					fmt.Fprintf(os.Stderr, "Error: cannot restart daemon for workspace %s: backend %q is single-process-only\n", workspace, cfg.GetBackend())
					fmt.Fprintf(os.Stderr, "Hint: initialize the workspace with sqlite backend for daemon mode (e.g. `bd init --backend sqlite`)\n")
				}
				os.Exit(1)
			}
		}

		// Stop the daemon
		if !jsonOutput {
			fmt.Printf("Stopping daemon for workspace: %s (PID %d)\n", workspace, targetDaemon.PID)
		}
		if err := daemon.StopDaemon(*targetDaemon); err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": err.Error()})
			} else {
				fmt.Fprintf(os.Stderr, "Error stopping daemon: %v\n", err)
			}
			os.Exit(1)
		}
		// Wait a moment for cleanup
		time.Sleep(500 * time.Millisecond)
		// Start a new daemon by executing 'bd daemon' in the workspace directory
		if !jsonOutput {
			fmt.Printf("Starting new daemon for workspace: %s\n", workspace)
		}
		exe, err := os.Executable()
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": fmt.Sprintf("cannot resolve executable: %v", err)})
			} else {
				fmt.Fprintf(os.Stderr, "Error: cannot resolve executable: %v\n", err)
			}
			os.Exit(1)
		}
		// Check if workspace-local bd binary exists (preferred)
		localBd := filepath.Join(workspace, "bd")
		_, localErr := os.Stat(localBd)
		bdPath := exe
		if localErr == nil {
			// Use local bd binary if it exists
			bdPath = localBd
		}
		// Use bd daemon command with proper working directory
		// The daemon will fork itself into the background
		daemonCmd := &exec.Cmd{
			Path: bdPath,
			Args: []string{bdPath, "daemon"},
			Dir:  workspace,
			Env:  os.Environ(),
		}
		if err := daemonCmd.Start(); err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": fmt.Sprintf("failed to start daemon: %v", err)})
			} else {
				fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
			}
			os.Exit(1)
		}
		// Don't wait for daemon to exit (it will fork and continue in background)
		// Use timeout to prevent goroutine leak if daemon never completes
		go func() {
			done := make(chan struct{})
			go func() {
				_ = daemonCmd.Wait()
				close(done)
			}()

			select {
			case <-done:
				// Daemon exited normally (forked successfully)
			case <-time.After(10 * time.Second):
				// Timeout - daemon should have forked by now
				if daemonCmd.Process != nil {
					_ = daemonCmd.Process.Kill()
				}
			}
		}()
		if jsonOutput {
			outputJSON(DaemonRestartResponse{
				Workspace: workspace,
				Action:    "restarted",
			})
		} else {
			fmt.Printf("Successfully restarted daemon for workspace: %s\n", workspace)
		}
	},
}
var daemonsLogsCmd = &cobra.Command{
	Use:   "logs <workspace-path|pid>",
	Short: "View logs for a specific bd daemon",
	Long: `View logs for a specific bd daemon by workspace path or PID.
Supports tail mode (last N lines) and follow mode (like tail -f).`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		target := args[0]
		// Use global jsonOutput set by PersistentPreRun
		follow, _ := cmd.Flags().GetBool("follow")
		lines, _ := cmd.Flags().GetInt("lines")
		// Discover all daemons
		daemons, err := daemon.DiscoverDaemons(nil)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": err.Error()})
			} else {
				fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
			}
			os.Exit(1)
		}
		// Find matching daemon by workspace path or PID
		// Use PathsEqual for case-insensitive comparison on macOS/Windows (GH#869)
		var targetDaemon *daemon.DaemonInfo
		for _, d := range daemons {
			if utils.PathsEqual(d.WorkspacePath, target) || fmt.Sprintf("%d", d.PID) == target {
				targetDaemon = &d
				break
			}
		}
		if targetDaemon == nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": "daemon not found"})
			} else {
				fmt.Fprintf(os.Stderr, "Error: daemon not found for %s\n", target)
			}
			os.Exit(1)
		}
		// Determine log file path
		logPath := filepath.Join(filepath.Dir(targetDaemon.SocketPath), "daemon.log")
		// Check if log file exists
		if _, err := os.Stat(logPath); err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": "log file not found"})
			} else {
				fmt.Fprintf(os.Stderr, "Error: log file not found: %s\n", logPath)
			}
			os.Exit(1)
		}
		if jsonOutput {
			// JSON mode: read entire file
			// #nosec G304 - controlled path from daemon discovery
			content, err := os.ReadFile(logPath)
			if err != nil {
				outputJSON(map[string]string{"error": err.Error()})
				os.Exit(1)
			}
			outputJSON(DaemonLogsResponse{
				Workspace: targetDaemon.WorkspacePath,
				LogPath:   logPath,
				Content:   string(content),
			})
			return
		}
		// Human-readable mode
		if follow {
			tailFollow(logPath)
		} else {
			if err := tailLines(logPath, lines); err != nil {
				fmt.Fprintf(os.Stderr, "Error reading log file: %v\n", err)
				os.Exit(1)
			}
		}
	},
}

func tailLines(filePath string, n int) error {
	// #nosec G304 - controlled path from daemon discovery
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	// Read all lines
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Print last N lines
	start := 0
	if len(lines) > n {
		start = len(lines) - n
	}
	for i := start; i < len(lines); i++ {
		fmt.Println(lines[i])
	}
	return nil
}
func tailFollow(filePath string) {
	// #nosec G304 - controlled path from daemon discovery
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()
	// Seek to end
	_, _ = file.Seek(0, io.SeekEnd)
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// Wait for more content
				time.Sleep(100 * time.Millisecond)
				continue
			}
			fmt.Fprintf(os.Stderr, "Error reading log file: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(strings.TrimRight(line, "\n\r") + "\n")
	}
}

var daemonsKillallCmd = &cobra.Command{
	Use:   "killall",
	Short: "Stop all running bd daemons",
	Long: `Stop all running bd daemons gracefully via RPC, falling back to SIGTERM/SIGKILL.
Uses escalating shutdown strategy: RPC (2s) → SIGTERM (3s) → SIGKILL (1s).`,
	Run: func(cmd *cobra.Command, args []string) {
		searchRoots, _ := cmd.Flags().GetStringSlice("search")
		// Use global jsonOutput set by PersistentPreRun
		force, _ := cmd.Flags().GetBool("force")
		// Discover all daemons
		daemons, err := daemon.DiscoverDaemons(searchRoots)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": err.Error()})
			} else {
				fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
			}
			os.Exit(1)
		}
		// Filter to alive daemons only
		var aliveDaemons []daemon.DaemonInfo
		for _, d := range daemons {
			if d.Alive {
				aliveDaemons = append(aliveDaemons, d)
			}
		}
		if len(aliveDaemons) == 0 {
			if jsonOutput {
				outputJSON(DaemonKillallEmptyResponse{
					Stopped: 0,
					Failed:  0,
				})
			} else {
				fmt.Println("No running daemons found")
			}
			return
		}
		// Kill all daemons
		results := daemon.KillAllDaemons(aliveDaemons, force)
		if jsonOutput {
			outputJSON(results)
		} else {
			fmt.Printf("Stopped: %d\n", results.Stopped)
			fmt.Printf("Failed:  %d\n", results.Failed)
			if len(results.Failures) > 0 {
				fmt.Println("\nFailures:")
				for _, f := range results.Failures {
					fmt.Printf("  %s (PID %d): %s\n", f.Workspace, f.PID, f.Error)
				}
			}
		}
		if results.Failed > 0 {
			os.Exit(1)
		}
	},
}
var daemonsHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check health of all bd daemons",
	Long: `Check health of all running bd daemons and report any issues including
stale sockets, version mismatches, and unresponsive daemons.`,
	Run: func(cmd *cobra.Command, args []string) {
		searchRoots, _ := cmd.Flags().GetStringSlice("search")
		// Use global jsonOutput set by PersistentPreRun
		// Discover daemons
		daemons, err := daemon.DiscoverDaemons(searchRoots)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
			os.Exit(1)
		}
		var reports []DaemonHealthReport
		healthyCount := 0
		staleCount := 0
		mismatchCount := 0
		unresponsiveCount := 0
		currentVersion := Version
		for _, d := range daemons {
			report := DaemonHealthReport{
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
			outputJSON(DaemonHealthResponse{
				Total:        len(reports),
				Healthy:      healthyCount,
				Stale:        staleCount,
				Mismatched:   mismatchCount,
				Unresponsive: unresponsiveCount,
				Daemons:      reports,
			})
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
		_, _ = fmt.Fprintln(w, "WORKSPACE\tPID\tVERSION\tSTATUS\tISSUE")
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
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				workspace, pidStr, version, status, issue)
		}
		_ = w.Flush()
		// Exit with error if there are any issues
		if staleCount > 0 || mismatchCount > 0 || unresponsiveCount > 0 {
			os.Exit(1)
		}
	},
}

func init() {
	// Add multi-daemon subcommands to daemonCmd (primary location)
	daemonCmd.AddCommand(daemonsListCmd)
	daemonCmd.AddCommand(daemonsHealthCmd)
	daemonCmd.AddCommand(daemonsStopCmd)
	daemonCmd.AddCommand(daemonsLogsCmd)
	daemonCmd.AddCommand(daemonsKillallCmd)
	daemonCmd.AddCommand(daemonsRestartCmd)

	// "daemons" backwards compatibility is handled by Aliases on daemonCmd
	// (see daemon.go). No separate daemonsCmd needed.

	// Flags for list command
	daemonsListCmd.Flags().StringSlice("search", nil, "Directories to search for daemons (default: home, /tmp, cwd)")
	daemonsListCmd.Flags().Bool("no-cleanup", false, "Skip auto-cleanup of stale sockets")
	// Flags for health command
	daemonsHealthCmd.Flags().StringSlice("search", nil, "Directories to search for daemons (default: home, /tmp, cwd)")
	// Flags for stop command
	// Flags for logs command
	daemonsLogsCmd.Flags().BoolP("follow", "f", false, "Follow log output (like tail -f)")
	daemonsLogsCmd.Flags().IntP("lines", "n", 50, "Number of lines to show from end of log")
	// Flags for killall command
	daemonsKillallCmd.Flags().StringSlice("search", nil, "Directories to search for daemons (default: home, /tmp, cwd)")
	daemonsKillallCmd.Flags().Bool("force", false, "Use SIGKILL immediately if graceful shutdown fails")
	// Flags for restart command
	daemonsRestartCmd.Flags().StringSlice("search", nil, "Directories to search for daemons (default: home, /tmp, cwd)")
}
