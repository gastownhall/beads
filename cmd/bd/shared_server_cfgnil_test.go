//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSharedServerCfgNilHonorsSharedServer is a regression test for GH#3817.
//
// When the resolved beadsDir has no metadata.json/config.yaml, configfile.Load
// returns (nil, nil). The shared-server override in cmd/bd/main.go used to live
// only inside the `cfg != nil` branch, so an exported BEADS_DOLT_SHARED_SERVER
// was silently ignored: doltCfg.ServerMode stayed false and newDoltStore fell
// through to embeddeddolt.Open. With a leftover embedded data directory present
// (e.g. a clone or recovery dir that has no metadata.json yet) bd would read
// that phantom embedded DB and silently report success, fragmenting data away
// from the shared server instead of connecting to it.
//
// The fix honors shared-server mode regardless of whether project config was
// found. This test reproduces the config-less case with no reachable server and
// asserts bd attempts the server (clear error) rather than silently falling
// back to the embedded database.
//
// Hermetic: no container required. Auto-start is disabled and the server port
// points nowhere, so the "honored" path fails fast with a connection error.
func TestSharedServerCfgNilHonorsSharedServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	bdBinary := buildSharedServerTestBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Config-less beads dir (no metadata.json, no config.yaml) with a leftover
	// embedded data directory — the configfile.Load -> (nil, nil) case where an
	// embedded read could otherwise silently succeed.
	beadsDir := filepath.Join(t.TempDir(), ".beads", "shared-server")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("mkdir config-less beads dir: %v", err)
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"BEADS_DIR=" + beadsDir,
		"BEADS_DOLT_SHARED_SERVER=1",
		// Disable auto-start and point at a port nothing listens on so the
		// shared-server path fails fast instead of spinning up a server.
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_DOLT_SERVER_PORT=59999",
		"BEADS_TEST_MODE=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
	}

	neutralCwd := t.TempDir()
	out, err := ssExec(ctx, bdBinary, neutralCwd, env, "list")

	// Before the fix: bd silently ignored shared-server mode, read the leftover
	// embedded DB, and exited 0 with "No issues found". After: shared-server is
	// honored, so bd tries the (unreachable) server and fails clearly.
	if err == nil {
		t.Fatalf("bd list silently succeeded under BEADS_DOLT_SHARED_SERVER with no "+
			"reachable server and no project config; expected a server-connection "+
			"error — bd fell through to the embedded backend (GH#3817 regression)\n"+
			"output:\n%s", out)
	}
	if strings.Contains(out, "No issues found") {
		t.Fatalf("bd list returned embedded \"No issues found\" instead of honoring "+
			"shared-server mode (GH#3817 regression)\noutput:\n%s", out)
	}
	if !strings.Contains(out, "Dolt server") {
		t.Fatalf("expected a Dolt server connection error (shared-server mode honored), got:\n%s", out)
	}
}
