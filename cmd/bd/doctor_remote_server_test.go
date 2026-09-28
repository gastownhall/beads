package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// TestDoctorRemoteServerSkipsFilesystemChecks verifies that when the Dolt
// server is remote (not localhost), filesystem-dependent checks return
// "skip" status with a message explaining why.
func TestDoctorRemoteServerSkipsFilesystemChecks(t *testing.T) {
	t.Parallel()

	// Create a temp project dir with .beads/ pointing to a remote host.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_server_host": "10.0.0.2",
		"dolt_server_port": 3307,
		"database":         "beads",
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// These checks access the local .dolt/ directory and should be skipped
	// for remote server mode.
	checksToSkip := []string{
		"Dolt Format",
		"Remote Consistency",
		"Btrfs NoCOW (dolt)",
	}

	for _, checkName := range checksToSkip {
		t.Run(checkName, func(t *testing.T) {
			var dc doctor.DoctorCheck
			switch checkName {
			case "Dolt Format":
				dc = doctor.CheckDoltFormat(tmpDir)
			case "Remote Consistency":
				dc = doctor.CheckRemoteConsistency(tmpDir)
			case "Btrfs NoCOW (dolt)":
				dc = doctor.CheckBtrfsNoCOW(tmpDir)
			}

			if dc.Status != "skip" {
				t.Errorf("%s: got status %q, want %q", checkName, dc.Status, "skip")
			}
			if dc.Message == "" || dc.Message != "skipped: remote server mode" {
				t.Errorf("%s: got message %q, want %q", checkName, dc.Message, "skipped: remote server mode")
			}
		})
	}
}

// TestDoctorRemoteServerRunsSQLChecks verifies that SQL-based checks still
// run normally when the Dolt server is remote.
func TestDoctorRemoteServerRunsSQLChecks(t *testing.T) {
	t.Parallel()

	// Create a temp project dir with .beads/ pointing to a remote host.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_server_host": "10.0.0.2",
		"dolt_server_port": 3307,
		"database":         "beads",
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Dolt Locks uses SQL (dolt_status) — should NOT be skipped.
	// It may fail to connect (no real server), but should not return "skip".
	dc := doctor.CheckDoltLocks(tmpDir)
	if dc.Status == "skip" {
		t.Errorf("Dolt Locks should not be skipped for remote server mode (it uses SQL)")
	}
}

// TestDoctorLocalServerDoesNotSkip verifies that local server configs
// do NOT trigger skip behavior.
func TestDoctorLocalServerDoesNotSkip(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3307,
		"database":         "beads",
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// With localhost config, CheckDoltFormat should NOT be skipped.
	dc := doctor.CheckDoltFormat(tmpDir)
	if dc.Status == "skip" {
		t.Errorf("Dolt Format should not be skipped for local server (host=127.0.0.1)")
	}
}
