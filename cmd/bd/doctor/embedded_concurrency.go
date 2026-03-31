package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// CheckEmbeddedModeConcurrency detects when a project is using Dolt embedded
// mode but shows signs of concurrent access issues (lock files, multiple
// processes). Recommends switching to server mode for multi-agent workflows.
// Addresses GH#2086.
func CheckEmbeddedModeConcurrency(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return DoctorCheck{
			Name:     "Embedded Mode Concurrency",
			Status:   StatusOK,
			Message:  "N/A (no config)",
			Category: CategoryRuntime,
		}
	}

	if isServerBackedRuntime(beadsDir, cfg) {
		return DoctorCheck{
			Name:     "Embedded Mode Concurrency",
			Status:   StatusOK,
			Message:  "Using server mode (multi-writer supported)",
			Category: CategoryRuntime,
		}
	}

	// In embedded mode — check for signs of concurrent access problems
	var issues []string

	// Check for dolt-access.lock (advisory lock from embedded mode)
	accessLockPath := filepath.Join(beadsDir, "dolt-access.lock")
	if _, err := os.Stat(accessLockPath); err == nil {
		issues = append(issues, "dolt-access.lock present (embedded mode advisory lock)")
	}

	// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
	// directory — including noms/LOCK files. These are Dolt-internal files.
	// Removing them WILL cause unrecoverable data corruption and data loss.
	// Dolt manages these files itself; external interference is never safe.

	if len(issues) == 0 {
		return DoctorCheck{
			Name:     "Embedded Mode Concurrency",
			Status:   StatusOK,
			Message:  "No concurrent access issues detected",
			Category: CategoryRuntime,
		}
	}

	return DoctorCheck{
		Name:    "Embedded Mode Concurrency",
		Status:  StatusWarning,
		Message: fmt.Sprintf("Embedded mode with %d lock indicator(s) — concurrent access may cause failures", len(issues)),
		Detail: fmt.Sprintf("Detected: %s. Embedded mode is single-writer only. "+
			"If you run multiple bd processes (e.g., multiple Claude Code windows), "+
			"switch to server mode for reliable concurrent access.", joinIssues(issues)),
		Fix:      "Start the Dolt server: bd dolt start",
		Category: CategoryRuntime,
	}
}

// isServerBackedRuntime reports whether this repo is using Beads' server-backed
// Dolt runtime, even when older metadata.json files omit dolt_mode.
func isServerBackedRuntime(beadsDir string, cfg *configfile.Config) bool {
	if cfg == nil {
		return false
	}
	if cfg.IsDoltServerMode() || doltserver.IsSharedServerMode() {
		return true
	}
	if cfg.DoltServerPort > 0 {
		return true
	}
	if os.Getenv("BEADS_DOLT_SERVER_PORT") != "" || os.Getenv("BEADS_DOLT_PORT") != "" {
		return true
	}

	serverDir := doltserver.ResolveServerDir(beadsDir)
	state, err := doltserver.IsRunning(serverDir)
	return err == nil && state != nil && state.Running
}

// joinIssues joins issue strings with semicolons.
func joinIssues(issues []string) string {
	result := ""
	for i, s := range issues {
		if i > 0 {
			result += "; "
		}
		result += s
	}
	return result
}
