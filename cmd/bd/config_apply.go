package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/dolt"
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		evt := metrics.NewCommandEvent("config-apply")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		results := runApply(dryRun)

		if jsonOutput {
			if err := outputJSON(results); err != nil {
				return err
			}
		} else {
			printApplyResults(results)
		}

		for _, r := range results {
			if r.Status == applyStatusError {
				return SilentExit()
			}
		}
		return nil
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
	hasDrift := driftDomains(driftItems)

	var results []ApplyResult
	results = append(results, applyHooks(hasDrift["hooks"], dryRun))
	serverResult := applyServer(hasDrift["server"], dryRun)
	results = append(results, serverResult)
	results = append(results, remoteApplyResult(driftItems, serverResult, dryRun, checkRemoteDrift))
	return results
}

func shouldRecheckRemoteAfterServerStart(remoteSkipped bool, serverResult ApplyResult) bool {
	return remoteSkipped && serverResult.Check == "server" && serverResult.Status == applyStatusApplied
}

func remoteApplyResult(driftItems []DriftItem, serverResult ApplyResult, dryRun bool, recheckRemote func() []DriftItem) ApplyResult {
	remoteItems := driftItems
	if shouldRecheckRemoteAfterServerStart(skippedDriftDomain(remoteItems, "remote"), serverResult) {
		remoteItems = recheckRemote()
	}
	if skipped := skippedDriftItem(remoteItems, "remote"); skipped != nil {
		return skippedRemoteApplyResult(*skipped)
	}
	return applyRemote(driftDomains(remoteItems)["remote"], dryRun)
}

func driftDomains(items []DriftItem) map[string]bool {
	hasDrift := map[string]bool{}
	for _, item := range items {
		if item.Status == driftStatusDrift {
			hasDrift[driftDomain(item.Check)] = true
		}
	}
	return hasDrift
}

func skippedDriftDomain(items []DriftItem, domain string) bool {
	return skippedDriftItem(items, domain) != nil
}

func skippedDriftItem(items []DriftItem, domain string) *DriftItem {
	for _, item := range items {
		if item.Status == driftStatusSkipped && driftDomain(item.Check) == domain {
			skipped := item
			return &skipped
		}
	}
	return nil
}

func driftDomain(check string) string {
	if before, _, ok := strings.Cut(check, "."); ok {
		return before
	}
	return check
}

func skippedRemoteApplyResult(item DriftItem) ApplyResult {
	return ApplyResult{
		Check:   "remote",
		Action:  "none",
		Status:  applyStatusSkipped,
		Message: item.Message,
	}
}

func remoteApplyStoreConfig(dryRun bool) *dolt.Config {
	return &dolt.Config{
		ReadOnly:         dryRun,
		DisableAutoStart: true,
	}
}

func remoteURLMatchesConfig(currentURL, configuredURL string) bool {
	return doltutil.RemoteURLsMatch(currentURL, configuredURL)
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
			Message: "No active beads workspace found",
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

	ctx := context.Background()
	st, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, remoteApplyStoreConfig(dryRun))
	if err != nil {
		return ApplyResult{
			Check:   "remote",
			Action:  "configure",
			Status:  applyStatusError,
			Message: "Failed to open Dolt store",
			Error:   err.Error(),
		}
	}
	defer func() { _ = st.Close() }()

	remotes, err := st.ListRemotes(ctx)
	if err != nil {
		return ApplyResult{
			Check:   "remote",
			Action:  "configure",
			Status:  applyStatusError,
			Message: "Failed to list Dolt remotes",
			Error:   err.Error(),
		}
	}
	var currentURL string
	for _, r := range remotes {
		if r.Name == "origin" {
			currentURL = r.URL
			break
		}
	}

	if dryRun {
		if currentURL == "" {
			return ApplyResult{
				Check:   "remote",
				Action:  "add_remote",
				Status:  applyStatusDryRun,
				Message: fmt.Sprintf("Would add Dolt origin remote: %s", federationRemote),
			}
		}
		if remoteURLMatchesConfig(currentURL, federationRemote) {
			return ApplyResult{
				Check:   "remote",
				Action:  "none",
				Status:  applyStatusOK,
				Message: "Dolt remote configuration is consistent",
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
		if err := st.AddRemote(ctx, "origin", federationRemote); err != nil {
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

	if remoteURLMatchesConfig(currentURL, federationRemote) {
		return ApplyResult{
			Check:   "remote",
			Action:  "none",
			Status:  applyStatusOK,
			Message: "Dolt remote configuration is consistent",
		}
	}

	oldURL := currentURL
	if err := st.RemoveRemote(ctx, "origin"); err != nil {
		return ApplyResult{
			Check:   "remote",
			Action:  "update_remote",
			Status:  applyStatusError,
			Message: "Failed to remove old Dolt origin remote",
			Error:   err.Error(),
		}
	}

	if err := st.AddRemote(ctx, "origin", federationRemote); err != nil {
		_ = st.AddRemote(ctx, "origin", oldURL)
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
			Message: "No active beads workspace found",
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
