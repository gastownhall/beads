package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// DriftItem represents a single drift check result.
type DriftItem struct {
	Check    string `json:"check"`
	Status   string `json:"status"` // "ok", "drift", "info", "skipped"
	Message  string `json:"message"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

const (
	driftStatusOK      = "ok"
	driftStatusDrift   = "drift"
	driftStatusInfo    = "info"
	driftStatusSkipped = "skipped"
)

var configDriftCmd = &cobra.Command{
	Use:   "drift",
	Short: "Detect config-vs-reality inconsistencies",
	Long: `Detect drift between declared configuration and actual system state.

This is a read-only diagnostic that answers "is my environment consistent
with my config?" — no mutations are performed.

Checks:
  - hooks     Git hooks installed and up-to-date
  - remote    Dolt remote matches federation.remote config
  - server    Server state matches dolt.shared-server config

Exit codes:
  0  No drift detected (all checks ok/info/skipped)
  1  Drift detected (at least one check has status "drift")

Examples:
  bd config drift
  bd config drift --json`,
	Run: func(_ *cobra.Command, _ []string) {
		items := runDriftChecks()

		if jsonOutput {
			outputJSON(items)
		} else {
			printDriftItems(items)
		}

		// Exit 1 if any drift detected
		for _, item := range items {
			if item.Status == driftStatusDrift {
				os.Exit(1)
			}
		}
	},
}

func init() {
	configCmd.AddCommand(configDriftCmd)
}

// runDriftChecks runs all drift checks and returns the results.
func runDriftChecks() []DriftItem {
	var items []DriftItem
	items = append(items, checkHooksDrift()...)
	items = append(items, checkRemoteDrift()...)
	items = append(items, checkServerDrift()...)
	return items
}

// checkHooksDrift checks whether git hooks are installed and current.
func checkHooksDrift() []DriftItem {
	// Verify we're in a git repo first
	_, err := git.GetGitHooksDir()
	if err != nil {
		return []DriftItem{{
			Check:   "hooks",
			Status:  driftStatusSkipped,
			Message: "Not a git repository",
		}}
	}

	statuses := CheckGitHooks()

	var items []DriftItem
	var missing, outdated []string

	for _, s := range statuses {
		if !s.Installed {
			missing = append(missing, s.Name)
		} else if s.Outdated {
			outdated = append(outdated, fmt.Sprintf("%s (have %s, want %s)", s.Name, s.Version, Version))
		}
	}

	if len(missing) > 0 {
		items = append(items, DriftItem{
			Check:    "hooks.missing",
			Status:   driftStatusDrift,
			Message:  fmt.Sprintf("Git hooks not installed: %s", strings.Join(missing, ", ")),
			Expected: "installed",
			Actual:   "missing",
		})
	}

	if len(outdated) > 0 {
		items = append(items, DriftItem{
			Check:    "hooks.outdated",
			Status:   driftStatusDrift,
			Message:  fmt.Sprintf("Git hooks outdated: %s", strings.Join(outdated, ", ")),
			Expected: "v" + Version,
			Actual:   "outdated",
		})
	}

	if len(missing) == 0 && len(outdated) == 0 {
		items = append(items, DriftItem{
			Check:   "hooks",
			Status:  driftStatusOK,
			Message: "Git hooks installed and current",
		})
	}

	return items
}

// checkRemoteDrift compares federation.remote config against actual Dolt remotes.
func checkRemoteDrift() []DriftItem {
	federationRemote := config.GetString("federation.remote")

	// Find the dolt data directory for CLI remote listing
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return []DriftItem{{
			Check:   "remote",
			Status:  driftStatusSkipped,
			Message: "No active beads workspace found",
		}}
	}

	doltDir := doltserver.ResolveDoltDir(beadsDir)

	cliRemotes, err := doltutil.ListCLIRemotes(doltDir)
	if err != nil {
		return []DriftItem{{
			Check:   "remote",
			Status:  driftStatusSkipped,
			Message: fmt.Sprintf("Cannot list remotes: %v", err),
		}}
	}

	// Find the "origin" remote (the conventional default)
	var originURL string
	for _, r := range cliRemotes {
		if r.Name == "origin" {
			originURL = r.URL
			break
		}
	}

	// Case 1: federation.remote set, no origin remote
	if federationRemote != "" && originURL == "" {
		return []DriftItem{{
			Check:    "remote",
			Status:   driftStatusDrift,
			Message:  "federation.remote is configured but no Dolt 'origin' remote exists",
			Expected: federationRemote,
			Actual:   "(no origin remote)",
		}}
	}

	// Case 2: federation.remote set, origin exists but doesn't match
	if federationRemote != "" && originURL != "" && originURL != federationRemote {
		return []DriftItem{{
			Check:    "remote",
			Status:   driftStatusDrift,
			Message:  "Dolt origin remote URL does not match federation.remote",
			Expected: federationRemote,
			Actual:   originURL,
		}}
	}

	// Case 3: federation.remote set and matches origin
	if federationRemote != "" && originURL == federationRemote {
		return []DriftItem{{
			Check:   "remote",
			Status:  driftStatusOK,
			Message: "Dolt origin remote matches federation.remote",
		}}
	}

	// Case 4: federation.remote not set but remotes exist
	if federationRemote == "" && len(cliRemotes) > 0 {
		names := make([]string, len(cliRemotes))
		for i, r := range cliRemotes {
			names[i] = r.Name
		}
		return []DriftItem{{
			Check:   "remote",
			Status:  driftStatusInfo,
			Message: fmt.Sprintf("Dolt remotes configured (%s) but federation.remote is not set", strings.Join(names, ", ")),
		}}
	}

	// Case 5: Neither configured
	return []DriftItem{{
		Check:   "remote",
		Status:  driftStatusInfo,
		Message: "No federation.remote configured and no Dolt remotes found",
	}}
}

// checkServerDrift compares dolt.shared-server config against running server state.
// Uses non-mutating PID file check instead of doltserver.IsRunning() which can
// delete stale state files.
func checkServerDrift() []DriftItem {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return []DriftItem{{
			Check:   "server",
			Status:  driftStatusSkipped,
			Message: "No active beads workspace found",
		}}
	}

	sharedServerEnabled := config.GetString("dolt.shared-server")
	wantServer := strings.EqualFold(sharedServerEnabled, "true")

	serverRunning := isServerProbablyRunning(beadsDir)

	if wantServer && !serverRunning {
		return []DriftItem{{
			Check:    "server",
			Status:   driftStatusDrift,
			Message:  "dolt.shared-server is enabled but no server appears to be running",
			Expected: "running",
			Actual:   "not running",
		}}
	}

	if !wantServer && serverRunning {
		return []DriftItem{{
			Check:   "server",
			Status:  driftStatusInfo,
			Message: "Dolt server is running but dolt.shared-server is not enabled in config",
		}}
	}

	if wantServer && serverRunning {
		return []DriftItem{{
			Check:   "server",
			Status:  driftStatusOK,
			Message: "Server running, consistent with dolt.shared-server config",
		}}
	}

	// Neither configured nor running
	return []DriftItem{{
		Check:   "server",
		Status:  driftStatusOK,
		Message: "No shared server configured or running",
	}}
}

// isServerProbablyRunning performs a non-mutating check for a running Dolt server.
// Reads the PID file and checks if the process exists, without deleting stale files.
func isServerProbablyRunning(beadsDir string) bool {
	pidFile := filepath.Join(beadsDir, doltserver.PIDFileName)
	data, err := os.ReadFile(pidFile) // #nosec G304 -- controlled path
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}

	// Check if the process exists (signal 0 = existence check)
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds; use Signal(0) to verify
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// printDriftItems renders drift results in human-readable format.
func printDriftItems(items []DriftItem) {
	if len(items) == 0 {
		fmt.Println("No drift checks available")
		return
	}

	statusIcon := map[string]string{
		driftStatusOK:      "✓",
		driftStatusDrift:   "✗",
		driftStatusInfo:    "ℹ",
		driftStatusSkipped: "–",
	}

	for _, item := range items {
		icon := statusIcon[item.Status]
		if icon == "" {
			icon = "?"
		}
		fmt.Fprintf(os.Stdout, "  %s %s: %s\n", icon, item.Check, item.Message)
		if item.Expected != "" || item.Actual != "" {
			fmt.Fprintf(os.Stdout, "      expected: %s\n", item.Expected)
			fmt.Fprintf(os.Stdout, "      actual:   %s\n", item.Actual)
		}
	}
}
