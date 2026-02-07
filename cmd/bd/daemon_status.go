package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/ui"
)

// DaemonStatusReport is a single daemon status entry for JSON output
type DaemonStatusReport struct {
	Workspace       string  `json:"workspace"`
	PID             int     `json:"pid,omitempty"`
	Version         string  `json:"version,omitempty"`
	Status          string  `json:"status"`
	Issue           string  `json:"issue,omitempty"`
	Started         string  `json:"started,omitempty"`
	UptimeSeconds   float64 `json:"uptime_seconds,omitempty"`
	AutoCommit      bool    `json:"auto_commit,omitempty"`
	AutoPush        bool    `json:"auto_push,omitempty"`
	AutoPull        bool    `json:"auto_pull,omitempty"`
	LocalMode       bool    `json:"local_mode,omitempty"`
	SyncInterval    string  `json:"sync_interval,omitempty"`
	DaemonMode      string  `json:"daemon_mode,omitempty"`
	LogPath         string  `json:"log_path,omitempty"`
	VersionMismatch bool    `json:"version_mismatch,omitempty"`
	IsCurrent       bool    `json:"is_current,omitempty"`
}

// DaemonStatusAllResponse is returned for --all mode
type DaemonStatusAllResponse struct {
	Total        int                  `json:"total"`
	Healthy      int                  `json:"healthy"`
	Outdated     int                  `json:"outdated"`
	Stale        int                  `json:"stale"`
	Unresponsive int                  `json:"unresponsive"`
	Daemons      []DaemonStatusReport `json:"daemons"`
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	Long: `Show status of the current workspace's daemon, or all daemons with --all.

Examples:
  bd daemon status         # Current workspace daemon
  bd daemon status --all   # All running daemons`,
	Run: func(cmd *cobra.Command, args []string) {
		showAll, _ := cmd.Flags().GetBool("all")

		if showAll {
			showAllDaemonsStatus(cmd)
		} else {
			showCurrentDaemonStatus()
		}
	},
}

func init() {
	daemonStatusCmd.Flags().Bool("all", false, "Show status of all daemons")
	daemonStatusCmd.Flags().StringSlice("search", nil, "Directories to search for daemons (with --all)")
}

// shortenPath replaces home directory with ~ for display
func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// formatRelativeTime formats a time as relative (e.g., "2h ago")
func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	} else if d < time.Hour {
		mins := int(d.Minutes())
		if mins == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	} else if d < 24*time.Hour {
		hours := int(d.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1d ago"
	}
	return fmt.Sprintf("%dd ago", days)
}

// formatBoolIcon returns a styled checkmark or dash for boolean values
func formatBoolIcon(enabled bool) string {
	if enabled {
		return ui.RenderPass(ui.IconPass)
	}
	return ui.RenderMuted("-")
}

// renderDaemonStatusIcon renders status with semantic styling
func renderDaemonStatusIcon(status string) string {
	switch status {
	case "healthy", "running":
		return ui.RenderPass(ui.IconPass + " " + status)
	case "outdated", "version_mismatch":
		return ui.RenderWarn(ui.IconWarn + " outdated")
	case "stale":
		return ui.RenderWarn(ui.IconWarn + " stale")
	case "unresponsive":
		return ui.RenderFail(ui.IconFail + " unresponsive")
	case "not_running":
		return ui.RenderMuted("○ not running")
	default:
		return status
	}
}

// showCurrentDaemonStatus shows detailed status for current workspace daemon
func showCurrentDaemonStatus() {
	pidFile, err := getPIDFilePath()
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}

	beadsDir := filepath.Dir(pidFile)
	workspacePath := filepath.Dir(beadsDir)
	socketPath := rpc.ShortSocketPath(workspacePath)

	// Check if daemon is running
	isRunning, pid := isDaemonRunning(pidFile)
	if !isRunning {
		if jsonOutput {
			outputJSON(DaemonStatusReport{
				Workspace: workspacePath,
				Status:    "not_running",
			})
		} else {
			fmt.Printf("%s\n\n", renderDaemonStatusIcon("not_running"))
			fmt.Printf("  Workspace:  %s\n", shortenPath(workspacePath))
			fmt.Printf("\n  To start:   bd daemon start\n")
		}
		return
	}

	// Get detailed status via RPC
	var rpcStatus *rpc.StatusResponse
	if client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second); err == nil && client != nil {
		if status, err := client.Status(); err == nil {
			rpcStatus = status
		}
		_ = client.Close()
	}

	// Get started time from PID file
	var startedTime time.Time
	if info, err := os.Stat(pidFile); err == nil {
		startedTime = info.ModTime()
	}

	// Determine daemon version and check for mismatch
	daemonVersion := ""
	versionMismatch := false
	if rpcStatus != nil {
		daemonVersion = rpcStatus.Version
		if daemonVersion != "" && daemonVersion != Version {
			versionMismatch = true
		}
	}

	// Determine status
	status := "running"
	issue := ""
	if versionMismatch {
		status = "outdated"
		issue = fmt.Sprintf("daemon %s != cli %s", daemonVersion, Version)
	}

	// Get log path
	logPath := filepath.Join(beadsDir, "daemon.log")
	if _, err := os.Stat(logPath); err != nil {
		logPath = ""
	}

	if jsonOutput {
		report := DaemonStatusReport{
			Workspace:       workspacePath,
			PID:             pid,
			Version:         daemonVersion,
			Status:          status,
			Issue:           issue,
			LogPath:         logPath,
			VersionMismatch: versionMismatch,
			IsCurrent:       true,
		}
		if !startedTime.IsZero() {
			report.Started = startedTime.Format(time.RFC3339)
		}
		if rpcStatus != nil {
			report.UptimeSeconds = rpcStatus.UptimeSeconds
			report.AutoCommit = rpcStatus.AutoCommit
			report.AutoPush = rpcStatus.AutoPush
			report.AutoPull = rpcStatus.AutoPull
			report.LocalMode = rpcStatus.LocalMode
			report.SyncInterval = rpcStatus.SyncInterval
			report.DaemonMode = rpcStatus.DaemonMode
		}
		outputJSON(report)
		return
	}

	// Human-readable output with semantic styling
	// Status line
	versionStr := ""
	if daemonVersion != "" {
		versionStr = fmt.Sprintf(", v%s", daemonVersion)
	}
	if versionMismatch {
		fmt.Printf("%s (PID %d%s)\n", renderDaemonStatusIcon("outdated"), pid, versionStr)
		fmt.Printf("  %s\n\n", ui.RenderWarn(fmt.Sprintf("CLI version: %s", Version)))
	} else {
		fmt.Printf("%s (PID %d%s)\n\n", renderDaemonStatusIcon("running"), pid, versionStr)
	}

	// Details
	fmt.Printf("  Workspace:  %s\n", shortenPath(workspacePath))
	if !startedTime.IsZero() {
		fmt.Printf("  Started:    %s (%s)\n", startedTime.Format("2006-01-02 15:04:05"), formatRelativeTime(startedTime))
	}

	if rpcStatus != nil {
		fmt.Printf("  Mode:       %s\n", rpcStatus.DaemonMode)
		fmt.Printf("  Interval:   %s\n", rpcStatus.SyncInterval)

		// Compact sync flags display
		syncFlags := []string{}
		if rpcStatus.AutoCommit {
			syncFlags = append(syncFlags, ui.RenderPass(ui.IconPass)+" commit")
		}
		if rpcStatus.AutoPush {
			syncFlags = append(syncFlags, ui.RenderPass(ui.IconPass)+" push")
		}
		if rpcStatus.AutoPull {
			syncFlags = append(syncFlags, ui.RenderPass(ui.IconPass)+" pull")
		}
		if len(syncFlags) > 0 {
			fmt.Printf("  Sync:       %s\n", strings.Join(syncFlags, "  "))
		} else {
			fmt.Printf("  Sync:       %s\n", ui.RenderMuted("none"))
		}

		if rpcStatus.LocalMode {
			fmt.Printf("  Local:      %s\n", ui.RenderWarn("yes (no git sync)"))
		}
	}

	if logPath != "" {
		// Show relative path for log
		relLog := ".beads/daemon.log"
		fmt.Printf("  Log:        %s\n", relLog)
	}

	// Show hint about other daemons
	daemons, err := daemon.DiscoverDaemons(nil)
	if err == nil {
		aliveCount := 0
		for _, d := range daemons {
			if d.Alive {
				aliveCount++
			}
		}
		if aliveCount > 1 {
			fmt.Printf("\n  %s\n", ui.RenderMuted(fmt.Sprintf("%d other daemon(s) running (bd daemon status --all)", aliveCount-1)))
		}
	}
}

// showAllDaemonsStatus shows status of all daemons
func showAllDaemonsStatus(cmd *cobra.Command) {
	searchRoots, _ := cmd.Flags().GetStringSlice("search")

	// Discover daemons
	daemons, err := daemon.DiscoverDaemons(searchRoots)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
		}
		os.Exit(1)
	}

	// Auto-cleanup stale sockets
	if cleaned, err := daemon.CleanupStaleSockets(daemons); err == nil && cleaned > 0 && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Cleaned up %d stale socket(s)\n", cleaned)
	}

	// Get current workspace to mark it
	currentWorkspace := ""
	if pidFile, err := getPIDFilePath(); err == nil {
		beadsDir := filepath.Dir(pidFile)
		currentWorkspace = filepath.Dir(beadsDir)
	}

	currentVersion := Version
	var reports []DaemonStatusReport
	healthyCount := 0
	outdatedCount := 0
	staleCount := 0
	unresponsiveCount := 0

	for _, d := range daemons {
		report := DaemonStatusReport{
			Workspace: d.WorkspacePath,
			PID:       d.PID,
			Version:   d.Version,
			IsCurrent: d.WorkspacePath == currentWorkspace,
		}

		if !d.Alive {
			report.Status = "stale"
			report.Issue = d.Error
			staleCount++
		} else if d.Version != "" && d.Version != currentVersion {
			report.Status = "outdated"
			report.Issue = fmt.Sprintf("daemon %s != cli %s", d.Version, currentVersion)
			report.VersionMismatch = true
			outdatedCount++
		} else {
			report.Status = "healthy"
			healthyCount++
		}

		reports = append(reports, report)
	}

	if jsonOutput {
		outputJSON(DaemonStatusAllResponse{
			Total:        len(reports),
			Healthy:      healthyCount,
			Outdated:     outdatedCount,
			Stale:        staleCount,
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

	// Summary line
	fmt.Printf("Daemons: %d total", len(reports))
	if healthyCount > 0 {
		fmt.Printf(", %s", ui.RenderPass(fmt.Sprintf("%d healthy", healthyCount)))
	}
	if outdatedCount > 0 {
		fmt.Printf(", %s", ui.RenderWarn(fmt.Sprintf("%d outdated", outdatedCount)))
	}
	if staleCount > 0 {
		fmt.Printf(", %s", ui.RenderWarn(fmt.Sprintf("%d stale", staleCount)))
	}
	if unresponsiveCount > 0 {
		fmt.Printf(", %s", ui.RenderFail(fmt.Sprintf("%d unresponsive", unresponsiveCount)))
	}
	fmt.Println()
	fmt.Println()

	// Table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "  WORKSPACE\tPID\tVERSION\tSTATUS")

	for _, r := range reports {
		workspace := shortenPath(r.Workspace)
		if workspace == "" {
			workspace = "(unknown)"
		}

		// Add arrow for current workspace
		prefix := "  "
		if r.IsCurrent {
			prefix = ui.RenderAccent("→ ")
		}

		pidStr := "-"
		if r.PID != 0 {
			pidStr = fmt.Sprintf("%d", r.PID)
		}

		version := r.Version
		if version == "" {
			version = "-"
		}

		// Render status with icon and color
		var statusDisplay string
		switch r.Status {
		case "healthy":
			statusDisplay = ui.RenderPass(ui.IconPass + " healthy")
		case "outdated":
			statusDisplay = ui.RenderWarn(ui.IconWarn + " outdated")
			// Add version hint
			statusDisplay += ui.RenderMuted(fmt.Sprintf(" (cli: %s)", currentVersion))
		case "stale":
			statusDisplay = ui.RenderWarn(ui.IconWarn + " stale")
		case "unresponsive":
			statusDisplay = ui.RenderFail(ui.IconFail + " unresponsive")
		default:
			statusDisplay = r.Status
		}

		_, _ = fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\n",
			prefix, workspace, pidStr, version, statusDisplay)
	}
	_ = w.Flush()

	// Exit with error if there are issues
	if outdatedCount > 0 || staleCount > 0 || unresponsiveCount > 0 {
		os.Exit(1)
	}
}
