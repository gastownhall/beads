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
	return fmt.Sprintf("daemon mode is not supported with the %q backend (single-process only). To use daemon mode, initialize with %q (e.g. `bd init --backend sqlite`). Otherwise run commands in direct mode (default for dolt)", b, configfile.BackendSQLite)
}

// guardDaemonStartForDolt blocks daemon start/restart commands when the current
// workspace backend is Dolt, unless --federation is specified.
//
// Rationale: embedded Dolt is effectively single-writer at the OS-process level. The
// daemon architecture relies on multiple processes (CLI + daemon + helper spawns),
// which can trigger lock contention and transient "read-only" failures.
//
// Exception: --federation flag enables dolt sql-server mode which is multi-writer.
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

	backend := cfg.GetBackend()
	if configfile.CapabilitiesForBackend(backend).SingleProcessOnly {
		return fmt.Errorf("%s", singleProcessBackendHelp(backend))
	}

	return nil
}

