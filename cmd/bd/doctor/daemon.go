package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// CheckDaemonStatus checks the health of the daemon for a workspace.
// It checks for stale sockets, multiple daemons, and version mismatches.
func CheckDaemonStatus(path string, cliVersion string) DoctorCheck {
	// Normalize path for reliable comparison (handles symlinks)
	wsNorm, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Fallback to absolute path if EvalSymlinks fails
		wsNorm, _ = filepath.Abs(path)
	}

	// Use global daemon discovery (registry-based)
	daemons, err := daemon.DiscoverDaemons(nil)
	if err != nil {
		return DoctorCheck{
			Name:    "Daemon Health",
			Status:  StatusWarning,
			Message: "Unable to check daemon health",
			Detail:  err.Error(),
		}
	}

	// Filter to this workspace using normalized paths
	var workspaceDaemons []daemon.DaemonInfo
	for _, d := range daemons {
		dPath, err := filepath.EvalSymlinks(d.WorkspacePath)
		if err != nil {
			dPath, _ = filepath.Abs(d.WorkspacePath)
		}
		if dPath == wsNorm {
			workspaceDaemons = append(workspaceDaemons, d)
		}
	}

	// Check for stale socket directly (catches cases where RPC failed so WorkspacePath is empty)
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	socketPath := filepath.Join(beadsDir, "bd.sock")
	if _, err := os.Stat(socketPath); err == nil {
		// Socket exists - try to connect
		if len(workspaceDaemons) == 0 {
			// Socket exists but no daemon found in registry - likely stale
			return DoctorCheck{
				Name:    "Daemon Health",
				Status:  StatusWarning,
				Message: "Stale daemon socket detected",
				Detail:  fmt.Sprintf("Socket exists at %s but daemon is not responding", socketPath),
				Fix:     "Run 'bd daemons killall' to clean up stale sockets",
			}
		}
	}

	if len(workspaceDaemons) == 0 {
		return DoctorCheck{
			Name:    "Daemon Health",
			Status:  StatusOK,
			Message: "No daemon running (will auto-start on next command)",
		}
	}

	// Warn if multiple daemons for same workspace
	if len(workspaceDaemons) > 1 {
		return DoctorCheck{
			Name:    "Daemon Health",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Multiple daemons detected for this workspace (%d)", len(workspaceDaemons)),
			Fix:     "Run 'bd daemons killall' to clean up duplicate daemons",
		}
	}

	// Check for stale or version mismatched daemons
	for _, d := range workspaceDaemons {
		if !d.Alive {
			return DoctorCheck{
				Name:    "Daemon Health",
				Status:  StatusWarning,
				Message: "Stale daemon detected",
				Detail:  fmt.Sprintf("PID %d is not alive", d.PID),
				Fix:     "Run 'bd daemons killall' to clean up stale daemons",
			}
		}

		if d.Version != cliVersion {
			return DoctorCheck{
				Name:    "Daemon Health",
				Status:  StatusWarning,
				Message: fmt.Sprintf("Version mismatch (daemon: %s, CLI: %s)", d.Version, cliVersion),
				Fix:     "Run 'bd daemons killall' to restart daemons with current version",
			}
		}
	}

	return DoctorCheck{
		Name:    "Daemon Health",
		Status:  StatusOK,
		Message: fmt.Sprintf("Daemon running (PID %d, version %s)", workspaceDaemons[0].PID, workspaceDaemons[0].Version),
	}
}

// CheckVersionMismatch checks if the database version matches the CLI version.
// Returns a warning message if there's a mismatch, or empty string if versions match or can't be read.
func CheckVersionMismatch(db *sql.DB, cliVersion string) string {
	var dbVersion string
	err := db.QueryRow("SELECT value FROM metadata WHERE key = 'bd_version'").Scan(&dbVersion)
	if err != nil {
		return "" // Can't read version, skip
	}

	if dbVersion != "" && dbVersion != cliVersion {
		return fmt.Sprintf("Version mismatch (CLI: %s, database: %s)", cliVersion, dbVersion)
	}

	return ""
}

// CheckGitSyncSetup checks if git repository and sync-branch are configured for daemon sync.
// This is informational - beads works fine without git sync, but users may want to enable it.
func CheckGitSyncSetup(path string) DoctorCheck {
	// Check if we're in a git repository
	_, err := git.GetGitDir()
	if err != nil {
		return DoctorCheck{
			Name:     "Git Sync Setup",
			Status:   StatusWarning,
			Message:  "No git repository (background sync unavailable)",
			Detail:   "The daemon requires a git repository for background sync. Without it, beads runs in direct mode.",
			Fix:      "Run 'git init' to enable background sync",
			Category: CategoryRuntime,
		}
	}

	// Git repo exists - check if sync-branch is configured
	if !syncbranch.IsConfigured() {
		return DoctorCheck{
			Name:     "Git Sync Setup",
			Status:   StatusOK,
			Message:  "Git repository detected (sync-branch not configured)",
			Detail:   "Beads commits directly to current branch. For team collaboration or to keep beads changes isolated, consider using a sync-branch.",
			Fix:      "Run 'bd config set sync.branch beads-sync' to use a dedicated branch for beads metadata",
			Category: CategoryRuntime,
		}
	}

	return DoctorCheck{
		Name:     "Git Sync Setup",
		Status:   StatusOK,
		Message:  "Git repository and sync-branch configured",
		Category: CategoryRuntime,
	}
}

// CheckDaemonAutoSync checks if daemon has auto-commit/auto-push enabled when
// sync-branch is configured. Missing auto-sync slows down agent workflows.
func CheckDaemonAutoSync(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Check if daemon is running
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Daemon Auto-Sync",
			Status:  StatusOK,
			Message: "Daemon not running (will use defaults on next start)",
		}
	}

	// Check if sync-branch is configured
	ctx := context.Background()
	store, err := factory.NewFromConfigWithOptions(ctx, beadsDir, factory.Options{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Daemon Auto-Sync",
			Status:  StatusOK,
			Message: "Could not check config (database unavailable)",
		}
	}
	defer func() { _ = store.Close() }()

	syncBranch, _ := store.GetConfig(ctx, "sync.branch")
	if syncBranch == "" {
		return DoctorCheck{
			Name:    "Daemon Auto-Sync",
			Status:  StatusOK,
			Message: "No sync-branch configured (auto-sync not applicable)",
		}
	}

	// Sync-branch is configured - check daemon's auto-commit/auto-push status
	client, err := rpc.TryConnect(socketPath)
	if err != nil || client == nil {
		return DoctorCheck{
			Name:    "Daemon Auto-Sync",
			Status:  StatusWarning,
			Message: "Could not connect to daemon to check auto-sync status",
		}
	}
	defer func() { _ = client.Close() }()

	status, err := client.Status()
	if err != nil {
		return DoctorCheck{
			Name:    "Daemon Auto-Sync",
			Status:  StatusWarning,
			Message: "Could not get daemon status",
			Detail:  err.Error(),
		}
	}

	if !status.AutoCommit || !status.AutoPush {
		var missing []string
		if !status.AutoCommit {
			missing = append(missing, "auto-commit")
		}
		if !status.AutoPush {
			missing = append(missing, "auto-push")
		}
		return DoctorCheck{
			Name:    "Daemon Auto-Sync",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Daemon running without %v (slows agent workflows)", missing),
			Detail:  "With sync-branch configured, auto-commit and auto-push should be enabled",
			Fix:     "Restart daemon: bd daemon stop . && bd daemon start",
		}
	}

	return DoctorCheck{
		Name:    "Daemon Auto-Sync",
		Status:  StatusOK,
		Message: "Auto-commit and auto-push enabled",
	}
}

// CheckLegacyDaemonConfig checks for deprecated daemon config options and
// encourages migration to the unified daemon.auto-sync setting.
func CheckLegacyDaemonConfig(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	ctx := context.Background()
	store, err := factory.NewFromConfigWithOptions(ctx, beadsDir, factory.Options{ReadOnly: true})
	if err != nil {
		return DoctorCheck{
			Name:    "Daemon Config",
			Status:  StatusOK,
			Message: "Could not check config (database unavailable)",
		}
	}
	defer func() { _ = store.Close() }()

	// Check for deprecated individual settings
	var legacySettings []string

	if val, _ := store.GetConfig(ctx, "daemon.auto_commit"); val != "" {
		legacySettings = append(legacySettings, "daemon.auto_commit")
	}
	if val, _ := store.GetConfig(ctx, "daemon.auto_push"); val != "" {
		legacySettings = append(legacySettings, "daemon.auto_push")
	}

	if len(legacySettings) > 0 {
		return DoctorCheck{
			Name:    "Daemon Config",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Deprecated config options found: %v", legacySettings),
			Detail:  "These options still work but are deprecated. Use daemon.auto-sync for read/write mode or daemon.auto-pull for read-only mode.",
			Fix:     "Run: bd config delete daemon.auto_commit && bd config delete daemon.auto_push && bd config set daemon.auto-sync true",
		}
	}

	return DoctorCheck{
		Name:    "Daemon Config",
		Status:  StatusOK,
		Message: "Using current config format",
	}
}

// CheckHydratedRepoDaemons checks if daemons are running for all repos
// configured in repos.additional. Without running daemons, JSONL files won't
// be kept updated, causing multi-repo hydration to become stale (bd-fix-routing).
func CheckHydratedRepoDaemons(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	ctx := context.Background()
	// Use factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
	store, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Hydrated Repo Daemons",
			Status:  StatusOK,
			Message: "Could not check config (database unavailable)",
		}
	}
	defer func() { _ = store.Close() }()

	// Get repos.additional from config
	additionalReposStr, _ := store.GetConfig(ctx, "repos.additional")
	if additionalReposStr == "" {
		return DoctorCheck{
			Name:    "Hydrated Repo Daemons",
			Status:  StatusOK,
			Message: "No additional repos configured (N/A)",
		}
	}

	// Parse additional repos (stored as JSON array string)
	var additionalRepos []string
	if err := unmarshalConfigValue(additionalReposStr, &additionalRepos); err != nil {
		return DoctorCheck{
			Name:    "Hydrated Repo Daemons",
			Status:  StatusWarning,
			Message: "Could not parse repos.additional config",
			Detail:  err.Error(),
		}
	}

	if len(additionalRepos) == 0 {
		return DoctorCheck{
			Name:    "Hydrated Repo Daemons",
			Status:  StatusOK,
			Message: "No additional repos configured (N/A)",
		}
	}

	// Check each additional repo for running daemon
	var missingDaemons []string
	for _, repoPath := range additionalRepos {
		// Expand ~ to home directory
		expandedPath := expandPath(repoPath)

		// Construct socket path
		socketPath := filepath.Join(expandedPath, ".beads", "bd.sock")

		// Try to connect to daemon
		client, err := rpc.TryConnect(socketPath)
		if err == nil && client != nil {
			_ = client.Close()
			// Daemon is running, all good
		} else {
			// No daemon running
			missingDaemons = append(missingDaemons, repoPath)
		}
	}

	if len(missingDaemons) > 0 {
		return DoctorCheck{
			Name:    "Hydrated Repo Daemons",
			Status:  StatusWarning,
			Message: fmt.Sprintf("Daemons not running in %d hydrated repo(s)", len(missingDaemons)),
			Detail:  fmt.Sprintf("Missing daemons in: %v", missingDaemons),
			Fix:     "For each repo, run: cd <repo> && bd daemon start --local",
		}
	}

	return DoctorCheck{
		Name:    "Hydrated Repo Daemons",
		Status:  StatusOK,
		Message: fmt.Sprintf("All %d hydrated repo(s) have running daemons", len(additionalRepos)),
	}
}

// unmarshalConfigValue unmarshals a JSON config value
func unmarshalConfigValue(value string, target interface{}) error {
	// Config values are stored as JSON
	if value == "" {
		return nil
	}

	// Unmarshal JSON into target
	return json.Unmarshal([]byte(value), target)
}
