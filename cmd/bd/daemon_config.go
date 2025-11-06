package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/steveyegge/beads/internal/beads"
)

// getGlobalBeadsDir returns the global beads directory (~/.beads)
func getGlobalBeadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot get home directory: %w", err)
	}

	beadsDir := filepath.Join(home, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		return "", fmt.Errorf("cannot create global beads directory: %w", err)
	}

	return beadsDir, nil
}

// ensureBeadsDir ensures the local beads directory exists (.beads in the current workspace)
func ensureBeadsDir() (string, error) {
	var beadsDir string
	if dbPath != "" {
		beadsDir = filepath.Dir(dbPath)
	} else {
		// Use public API to find database (same logic as other commands)
		if foundDB := beads.FindDatabasePath(); foundDB != "" {
			dbPath = foundDB // Store it for later use
			beadsDir = filepath.Dir(foundDB)
		} else {
			// No database found - error out instead of falling back to ~/.beads
			return "", fmt.Errorf("no database path configured (run 'bd init' or set BEADS_DB)")
		}
	}

	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		return "", fmt.Errorf("cannot create beads directory: %w", err)
	}

	return beadsDir, nil
}

// boolToFlag returns the flag string if condition is true, otherwise returns empty string
func boolToFlag(condition bool, flag string) string {
	if condition {
		return flag
	}
	return ""
}

// getEnvInt reads an integer from environment variable with a default value
func getEnvInt(key string, defaultValue int) int {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return defaultValue
}

// getEnvBool reads a boolean from environment variable with a default value
func getEnvBool(key string, defaultValue bool) bool {
	if val := os.Getenv(key); val != "" {
		return val == "true" || val == "1"
	}
	return defaultValue
}

// getSocketPathForPID determines the socket path for a given PID file
func getSocketPathForPID(pidFile string, global bool) string {
	if global {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".beads", "bd.sock")
	}
	// Local daemon: socket is in same directory as PID file
	return filepath.Join(filepath.Dir(pidFile), "bd.sock")
}

// getPIDFilePath returns the path to the daemon PID file
func getPIDFilePath(global bool) (string, error) {
	var beadsDir string
	var err error

	if global {
		beadsDir, err = getGlobalBeadsDir()
	} else {
		beadsDir, err = ensureBeadsDir()
	}

	if err != nil {
		return "", err
	}
	return filepath.Join(beadsDir, "daemon.pid"), nil
}

// getLogFilePath returns the path to the daemon log file
func getLogFilePath(userPath string, global bool) (string, error) {
	if userPath != "" {
		return userPath, nil
	}

	var beadsDir string
	var err error

	if global {
		beadsDir, err = getGlobalBeadsDir()
	} else {
		beadsDir, err = ensureBeadsDir()
	}

	if err != nil {
		return "", err
	}
	return filepath.Join(beadsDir, "daemon.log"), nil
}
