package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/daemon"
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
	beadsDir := filepath.Join(path, ".beads")
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
