package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// ApplyResult represents the outcome of a single apply action.
type ApplyResult struct {
	Check   string `json:"check"`
	Action  string `json:"action"`
	Status  string `json:"status"` // "applied", "dry_run", "skipped", "error", "ok"
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

const (
	applyStatusApplied = "applied"
	applyStatusDryRun  = "dry_run"
	applyStatusSkipped = "skipped"
	applyStatusError   = "error"
	applyStatusOK      = "ok"
)

var configApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Reconcile system state to match configuration",
	Long: `Reconcile actual system state to match declared configuration.

Runs drift detection and then fixes any mismatches it finds:

  - hooks     Reinstall git hooks if missing or outdated
  - remote    Add/update Dolt origin remote to match federation.remote
  - server    Start Dolt server if dolt.shared-server is enabled

This command is idempotent — safe to run multiple times. Use --dry-run
to preview what would change without making modifications.

Examples:
  bd config apply
  bd config apply --dry-run
  bd config apply --json`,
	Run: func(cmd *cobra.Command, _ []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		results := runApply(dryRun)

		if jsonOutput {
			outputJSON(results)
		} else {
			printApplyResults(results)
		}

		for _, r := range results {
			if r.Status == applyStatusError {
				os.Exit(1)
			}
		}
	},
}

func init() {
	configApplyCmd.Flags().Bool("dry-run", false, "Show what would change without making modifications")
	configCmd.AddCommand(configApplyCmd)
}

// runApply detects drift and fixes each category.
func runApply(dryRun bool) []ApplyResult {
	driftItems := runDriftChecks()

	// Group drift items by check domain to avoid duplicate actions
	// (e.g., multiple hook items should trigger only one reinstall).
	hasDrift := map[string]bool{}
	for _, item := range driftItems {
		if item.Status == driftStatusDrift {
			hasDrift[item.Check] = true
		}
	}

	var results []ApplyResult
	results = append(results, applyHooks(hasDrift["hooks"], dryRun))
	results = append(results, applyRemote(hasDrift["remote"], dryRun))
	results = append(results, applyServer(hasDrift["server"], dryRun))
	return results
}

// applyHooks reinstalls git hooks if drift was detected.
func applyHooks(drifted bool, dryRun bool) ApplyResult {
	if !drifted {
		return ApplyResult{
			Check:   "hooks",
			Action:  "none",
			Status:  applyStatusOK,
			Message: "Git hooks are up to date",
		}
	}

	if dryRun {
		return ApplyResult{
			Check:   "hooks",
			Action:  "reinstall",
			Status:  applyStatusDryRun,
			Message: "Would reinstall git hooks",
		}
	}

	// Verify we're in a git repo
	if _, err := git.GetGitHooksDir(); err != nil {
		return ApplyResult{
			Check:   "hooks",
			Action:  "reinstall",
			Status:  applyStatusSkipped,
			Message: "Not a git repository",
		}
	}

	if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
		return ApplyResult{
			Check:   "hooks",
			Action:  "reinstall",
			Status:  applyStatusError,
			Message: "Failed to reinstall git hooks",
			Error:   err.Error(),
		}
	}

	return ApplyResult{
		Check:   "hooks",
		Action:  "reinstall",
		Status:  applyStatusApplied,
		Message: fmt.Sprintf("Reinstalled git hooks (%d hooks updated)", len(managedHookNames)),
	}
}

// applyRemote ensures the Dolt origin remote matches federation.remote config.
func applyRemote(drifted bool, dryRun bool) ApplyResult {
	if !drifted {
		return ApplyResult{
			Check:   "remote",
			Action:  "none",
			Status:  applyStatusOK,
			Message: "Dolt remote configuration is consistent",
		}
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return ApplyResult{
			Check:   "remote",
			Action:  "configure",
			Status:  applyStatusSkipped,
			Message: "No .beads directory found",
		}
	}

	federationRemote := config.GetString("federation.remote")
	if federationRemote == "" {
		return ApplyResult{
			Check:   "remote",
			Action:  "configure",
			Status:  applyStatusSkipped,
			Message: "federation.remote is not set in config",
		}
	}

	doltDir := doltserver.ResolveDoltDir(beadsDir)
	currentURL := doltutil.FindCLIRemote(doltDir, "origin")

	if dryRun {
		if currentURL == "" {
			return ApplyResult{
				Check:   "remote",
				Action:  "add_remote",
				Status:  applyStatusDryRun,
				Message: fmt.Sprintf("Would add Dolt origin remote: %s", federationRemote),
			}
		}
		return ApplyResult{
			Check:   "remote",
			Action:  "update_remote",
			Status:  applyStatusDryRun,
			Message: fmt.Sprintf("Would update Dolt origin remote from %s to %s", currentURL, federationRemote),
		}
	}

	if currentURL == "" {
		// No origin exists — add it
		if err := doltutil.AddCLIRemote(doltDir, "origin", federationRemote); err != nil {
			return ApplyResult{
				Check:   "remote",
				Action:  "add_remote",
				Status:  applyStatusError,
				Message: "Failed to add Dolt origin remote",
				Error:   err.Error(),
			}
		}
		return ApplyResult{
			Check:   "remote",
			Action:  "add_remote",
			Status:  applyStatusApplied,
			Message: fmt.Sprintf("Added Dolt origin remote: %s", federationRemote),
		}
	}

	// Origin exists but wrong URL — remove then re-add
	// Save old URL in case we need to report it
	oldURL := currentURL
	if err := doltutil.RemoveCLIRemote(doltDir, "origin"); err != nil {
		return ApplyResult{
			Check:   "remote",
			Action:  "update_remote",
			Status:  applyStatusError,
			Message: "Failed to remove old Dolt origin remote",
			Error:   err.Error(),
		}
	}

	if err := doltutil.AddCLIRemote(doltDir, "origin", federationRemote); err != nil {
		// Try to restore the old remote on failure
		_ = doltutil.AddCLIRemote(doltDir, "origin", oldURL)
		return ApplyResult{
			Check:   "remote",
			Action:  "update_remote",
			Status:  applyStatusError,
			Message: "Failed to add new Dolt origin remote (old remote restored)",
			Error:   err.Error(),
		}
	}

	return ApplyResult{
		Check:   "remote",
		Action:  "update_remote",
		Status:  applyStatusApplied,
		Message: fmt.Sprintf("Updated Dolt origin remote from %s to %s", oldURL, federationRemote),
	}
}

// applyServer starts the Dolt server if config says it should be running but it isn't.
// Does NOT stop a running server if config says it shouldn't be — that's too destructive.
func applyServer(drifted bool, dryRun bool) ApplyResult {
	if !drifted {
		return ApplyResult{
			Check:   "server",
			Action:  "none",
			Status:  applyStatusOK,
			Message: "Server state is consistent with config",
		}
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return ApplyResult{
			Check:   "server",
			Action:  "start",
			Status:  applyStatusSkipped,
			Message: "No .beads directory found",
		}
	}

	// Only start if config says server should be running
	wantServer := config.GetString("dolt.shared-server") == "true"
	if !wantServer {
		// Server is running but config doesn't want it — report but don't stop
		return ApplyResult{
			Check:   "server",
			Action:  "none",
			Status:  applyStatusSkipped,
			Message: "Server is running but dolt.shared-server is not enabled; not stopping (use 'bd dolt stop' manually)",
		}
	}

	serverDir := doltserver.ResolveServerDir(beadsDir)

	if dryRun {
		return ApplyResult{
			Check:   "server",
			Action:  "start",
			Status:  applyStatusDryRun,
			Message: "Would start Dolt shared server",
		}
	}

	state, err := doltserver.Start(serverDir)
	if err != nil {
		return ApplyResult{
			Check:   "server",
			Action:  "start",
			Status:  applyStatusError,
			Message: "Failed to start Dolt server",
			Error:   err.Error(),
		}
	}

	return ApplyResult{
		Check:   "server",
		Action:  "start",
		Status:  applyStatusApplied,
		Message: fmt.Sprintf("Started Dolt server (PID %d, port %d)", state.PID, state.Port),
	}
}

// printApplyResults renders apply results in human-readable format.
func printApplyResults(results []ApplyResult) {
	if len(results) == 0 {
		fmt.Println("No actions to apply")
		return
	}

	statusIcon := map[string]string{
		applyStatusApplied: "✓",
		applyStatusDryRun:  "~",
		applyStatusSkipped: "–",
		applyStatusError:   "✗",
		applyStatusOK:      "✓",
	}

	for _, r := range results {
		icon := statusIcon[r.Status]
		if icon == "" {
			icon = "?"
		}
		fmt.Fprintf(os.Stdout, "  %s %s: %s\n", icon, r.Check, r.Message)
		if r.Error != "" {
			fmt.Fprintf(os.Stdout, "      error: %s\n", r.Error)
		}
	}
}
