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

// sharedServerPrimePort is pinned so the env var the run is given and the
// assertion on the captured output cannot drift apart. BEADS_DOLT_SERVER_PORT is
// the highest-priority port source (internal/doltserver), so it wins over
// DefaultSharedServerPort, and nothing listens here.
//
// Below 32768, i.e. outside the kernel's ephemeral range (32768-60999 here and
// on the GitHub runners), so a process that bound :0 cannot be handed this port
// and answer the dial the assertion below expects to fail.
const sharedServerPrimePort = "19999"

// TestPrimeHonorsSharedServerConfigYaml is a regression test for GH#6551.
//
// bd prime resolves its store through ensureStoreActiveForPrime ->
// ensureStoreActiveWithContext -> newDoltStoreFromConfig, a different path
// from every other command's resolution in cmd/bd/main.go. That path did not
// compensate for the gap configfile.IsDoltServerMode() leaves on purpose: it
// does not read dolt.shared-server from config.yaml, to avoid a circular
// import with the doltserver package. A linked git worktree commonly has
// config.yaml tracked but metadata.json gitignored as machine-local, so the
// workspace's only statement of shared-server mode lives in the one file this
// resolution ignored, and prime silently fell through to embeddeddolt.Open,
// creating a phantom embedded database named "beads" instead of reaching the
// real shared server. Same shape as the GH#3817 fix already applied to
// main.go's own resolution (see TestSharedServerCfgNilHonorsSharedServer) and
// the same centralizing fix (effectiveServerMode).
//
// The config.yaml layer is the ONLY thing this test may rely on, which is why
// BEADS_DOLT_SHARED_SERVER is deliberately absent from the env below.
// configfile.IsDoltServerMode honors that env var itself (step 2 of its
// precedence chain), and newDoltStoreFromConfig runs normalizeLoadedConfig
// before the gate, so an absent metadata.json arrives as a non-nil
// DefaultConfig() rather than nil: exporting the env var made the OLD gate
// select server mode too, and the A/B went green on unfixed code. This test
// was named for a `cfg == nil` short-circuit that does not happen at this call
// site.
//
// Hermetic: no container required. Auto-start is disabled and the server
// port points nowhere, so the honored path fails fast instead of silently
// creating the phantom store — the observable signal this test checks for.
func TestPrimeHonorsSharedServerConfigYaml(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	bdBinary := buildSharedServerTestBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// metadata.json-less beads dir — the configfile.Load -> (nil, nil) case
	// that left the compensation gap.
	beadsDir := filepath.Join(t.TempDir(), ".beads", "shared-server-prime")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir metadata-less beads dir: %v", err)
	}
	// The dir needs a config.yaml, and not only for realism. It is
	// simultaneously the reported GH#6551 shape — config.yaml tracked,
	// metadata.json gitignored as machine-local, so configfile.Load still
	// returns (nil, nil) because it reads only metadata.json — and what makes
	// the workspace DISCOVERABLE. With an entirely empty dir,
	// internal/beads.hasBeadsProjectFiles is false, so FindBeadsDir() ignores
	// BEADS_DIR and returns "", and ensureStoreActiveWithContext refuses with
	// ErrNoBeadsDatabase before newDoltStoreFromConfig is ever reached. This
	// test then passed identically with and without the fix: no phantom store
	// can be created on either, so the assertion below proved nothing.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt:\n  shared-server: true\n  auto-start: false\n"), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"BEADS_DIR=" + beadsDir,
		// BEADS_DOLT_SHARED_SERVER is intentionally NOT set here; see the note
		// on the test. The workspace's config.yaml is the signal under test.
		"BEADS_DOLT_SHARED_SERVER=",
		// Disable auto-start and point at a port nothing listens on so the
		// shared-server path fails fast instead of spinning up a server.
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_DOLT_SERVER_PORT=" + sharedServerPrimePort,
		"BD_DISABLE_EVENT_FLUSH=1",
		"BEADS_TEST_MODE=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
	}

	neutralCwd := t.TempDir()
	// A connection error from the honored shared-server attempt is expected and
	// fine — formatMemoriesForPrime degrades gracefully to a "memory
	// unavailable" banner rather than failing the command — so the exit status
	// carries no signal. The OUTPUT does, and it is checked below.
	out, _ := ssExec(ctx, bdBinary, neutralCwd, env, "prime", "--memories-only")

	// Positive half: prime must have REACHED shared-server resolution. The
	// phantom-directory check below is one-sided on its own — any earlier
	// failure (a rename of `prime --memories-only`, a bad env pin, any refusal
	// before store resolution) also leaves no embeddeddolt directory, so the
	// test would go green without ever exercising the honored path. Asserting
	// the pinned endpoint appears is what makes it able to fail for the reason
	// it was written.
	if !strings.Contains(out, sharedServerPrimePort) {
		t.Fatalf("bd prime never reached shared-server resolution: output does not name the pinned endpoint %s.\n"+
			"Output:\n%s", sharedServerPrimePort, out)
	}

	phantom := filepath.Join(beadsDir, "embeddeddolt")
	if info, statErr := os.Stat(phantom); statErr == nil && info.IsDir() {
		t.Fatalf("bd prime created a phantom embedded database at %s for a workspace with "+
			"dolt.shared-server: true in its config.yaml (GH#6551 regression)", phantom)
	}
}
