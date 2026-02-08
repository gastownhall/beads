package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

func singleProcessBackendHelp(backend string) string {
	b := strings.TrimSpace(backend)
	if b == "" {
		b = "unknown"
	}
	// Keep this short; Cobra will prefix with "Error:".
	return fmt.Sprintf("daemon mode is not supported with the current backend (%s). Use a multi-process compatible backend (e.g., `bd init --backend sqlite`) or run commands in direct mode without the daemon", b)
}

// guardDaemonStartForDolt blocks daemon start/restart commands when the current
// workspace backend is single-process-only, unless --federation is specified.
//
// Rationale: some backends may be single-writer at the OS-process level. The
// daemon architecture relies on multiple processes (CLI + daemon + helper spawns),
// which can trigger lock contention.
//
// Exception: --federation flag enables multi-writer mode.
//
// Note: This guard should only be attached to commands that START a daemon process
// (start, restart). Read-only commands (status, stop, logs, health, list) are allowed
// even with Dolt backend.
//
// We still allow help output so users can discover the command surface.
func guardDaemonStartForDolt(cmd *cobra.Command, _ []string) error {
	// Allow `--help` for any daemon subcommand.
	if helpFlag := cmd.Flags().Lookup("help"); helpFlag != nil {
		if help, _ := cmd.Flags().GetBool("help"); help {
			return nil
		}
	}

	// Allow `--federation` flag which enables dolt sql-server (multi-writer) mode.
	if fedFlag := cmd.Flags().Lookup("federation"); fedFlag != nil {
		if federation, _ := cmd.Flags().GetBool("federation"); federation {
			return nil
		}
	}

	// Best-effort determine the active workspace backend. If we can't determine it,
	// don't block (the command will likely fail later anyway).
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		// Fall back to configured dbPath if set; daemon commands often run from workspace root,
		// but tests may set BEADS_DB explicitly.
		if dbPath != "" {
			beadsDir = filepath.Dir(dbPath)
		} else if found := beads.FindDatabasePath(); found != "" {
			beadsDir = filepath.Dir(found)
		}
	}
	if beadsDir == "" {
		return nil
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return nil
	}

	// Use GetCapabilities() to properly handle Dolt server mode
	if cfg.GetCapabilities().SingleProcessOnly {
		return fmt.Errorf("%s", singleProcessBackendHelp(cfg.GetBackend()))
	}

	return nil
}
