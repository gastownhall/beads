//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestPrimeCfgNilHonorsSharedServer is a regression test for GH#6551.
//
// bd prime resolves its store through ensureStoreActiveForPrime ->
// ensureStoreActiveWithContext -> newDoltStoreFromConfig, a different path
// from every other command's resolution in cmd/bd/main.go. That path did
// not compensate for the gap configfile.IsDoltServerMode() leaves on
// purpose (it does not read dolt.shared-server from config.yaml, to avoid
// a circular import with the doltserver package): a linked git worktree
// commonly has config.yaml tracked but metadata.json gitignored as
// machine-local, so configfile.Load(beadsDir) returns (nil, nil) there and
// prime silently fell through to embeddeddolt.Open, creating a phantom
// embedded database named "beads" instead of reaching the real shared
// server. Same shape as the GH#3817 fix already applied to main.go's own
// resolution (see TestSharedServerCfgNilHonorsSharedServer) and the same
// centralizing fix (effectiveServerMode in store_factory.go).
//
// Hermetic: no container required. Auto-start is disabled and the server
// port points nowhere, so the honored path fails fast instead of silently
// creating the phantom store — the observable signal this test checks for.
func TestPrimeCfgNilHonorsSharedServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	bdBinary := buildSharedServerTestBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Config-less beads dir (no metadata.json, no config.yaml) — the
	// configfile.Load -> (nil, nil) case that left the compensation gap.
	beadsDir := filepath.Join(t.TempDir(), ".beads", "shared-server-prime")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
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
		"BD_DISABLE_EVENT_FLUSH=1",
		"BEADS_TEST_MODE=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
	}

	neutralCwd := t.TempDir()
	if _, err := ssExec(ctx, bdBinary, neutralCwd, env, "prime", "--memories-only"); err != nil {
		// A connection error from the honored shared-server attempt is
		// expected and fine — formatMemoriesForPrime degrades gracefully to
		// a "memory unavailable" banner rather than failing the command; it
		// must not silently create the phantom store either way.
		_ = err
	}

	phantom := filepath.Join(beadsDir, "embeddeddolt")
	if info, statErr := os.Stat(phantom); statErr == nil && info.IsDir() {
		t.Fatalf("bd prime created a phantom embedded database at %s under "+
			"BEADS_DOLT_SHARED_SERVER with no project config (GH#6551 regression)", phantom)
	}
}
