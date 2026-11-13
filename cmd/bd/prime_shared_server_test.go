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
// ensureReadOnlyStoreActiveWithContext -> newReadOnlyStoreFromConfig, a different path
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
// precedence chain), and newReadOnlyStoreFromConfig runs normalizeLoadedConfig
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
	// BEADS_DIR and returns "", and ensureReadOnlyStoreActiveWithContext refuses with
	// ErrNoBeadsDatabase before newReadOnlyStoreFromConfig is ever reached. This
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

// TestPrimeConfigOnlyDirectoryDoesNotCreateLedger covers the explicit
// BEADS_DIR scenario reported in GH#7920 through the real command and open.
func TestPrimeConfigOnlyDirectoryDoesNotCreateLedger(t *testing.T) {
	bd := buildSharedServerTestBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ancestor, _ := primeInitializedWorkspace(t, ctx, bd)
	for _, cwd := range []struct{ name, path string }{
		{"outside_repo", t.TempDir()},
		{"inside_initialized_repo", ancestor},
	} {
		for _, fixture := range []struct{ name, content string }{
			{"config.yaml", "# Beads configuration\n"},
			{"metadata.json", `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"prime"}`},
		} {
			t.Run(cwd.name+"/"+fixture.name, func(t *testing.T) {
				beadsDir := t.TempDir()
				configPath := filepath.Join(beadsDir, fixture.name)
				if err := os.WriteFile(configPath, []byte(fixture.content), 0o600); err != nil {
					t.Fatal(err)
				}
				env := primeEmbeddedTestEnv(t, beadsDir)
				for _, mode := range []string{"--memories-only", "--full", "--mcp"} {
					out, err := ssExec(ctx, bd, cwd.path, env, "prime", mode)
					if err != nil {
						t.Fatalf("prime %s: %v\n%s", mode, err, out)
					}
					if !strings.Contains(out, "no embedded database") {
						t.Fatalf("prime %s did not report the missing ledger: %s", mode, out)
					}
					if _, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); !os.IsNotExist(err) {
						t.Fatalf("prime %s created embedded storage (stat error: %v)", mode, err)
					}
				}
				data, err := os.ReadFile(configPath)
				if err != nil || string(data) != fixture.content {
					t.Fatalf("prime changed %s: %q, %v", fixture.name, data, err)
				}
			})
		}
	}
}

func TestPrimeReadsInitializedWorkspace(t *testing.T) {
	bd := buildSharedServerTestBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root, env := primeInitializedWorkspace(t, ctx, bd)
	const memory = "prime must read this initialized ledger"
	if out, err := ssExec(ctx, bd, root, env, "remember", memory, "--key", "prime-existing"); err != nil {
		t.Fatalf("remember: %v\n%s", err, out)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{"explicit", "discovery"} {
		t.Run(selection, func(t *testing.T) {
			selectedEnv := append([]string(nil), env...)
			if selection == "discovery" {
				for i, entry := range selectedEnv {
					if strings.HasPrefix(entry, "BEADS_DIR=") {
						selectedEnv[i] = "BEADS_DIR="
					}
				}
			}
			out, err := ssExec(ctx, bd, child, selectedEnv, "prime", "--memories-only")
			if err != nil || !strings.Contains(out, memory) || strings.Contains(out, "unavailable") {
				t.Fatalf("prime did not read the initialized ledger: %v\n%s", err, out)
			}
		})
	}
}

func TestPrimeEmptyOrMissingExplicitDirectoryStaysUninitialized(t *testing.T) {
	bd := buildSharedServerTestBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root, _ := primeInitializedWorkspace(t, ctx, bd)
	for _, kind := range []string{"empty", "missing"} {
		t.Run(kind, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), "explicit")
			if kind == "empty" {
				if err := os.Mkdir(beadsDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			out, err := ssExec(ctx, bd, root, primeEmbeddedTestEnv(t, beadsDir), "prime")
			if err != nil || strings.TrimSpace(out) != "" {
				t.Fatalf("prime should stay silent without a workspace: %v\n%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); !os.IsNotExist(err) {
				t.Fatalf("prime initialized %s explicit directory: %v", kind, err)
			}
		})
	}
}

func primeEmbeddedTestEnv(t *testing.T, beadsDir string) []string {
	t.Helper()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"BEADS_DIR=" + beadsDir,
		"BEADS_DOLT_SHARED_SERVER=0",
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_TEST_MODE=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BD_DISABLE_METRICS=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
}

func primeInitializedWorkspace(t *testing.T, ctx context.Context, bd string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	env := primeEmbeddedTestEnv(t, filepath.Join(root, ".beads"))
	if out, err := ssExec(ctx, "git", root, env, "-c", "core.hooksPath=.git/hooks", "init", "--quiet"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := ssExec(ctx, "git", root, env, "config", "core.hooksPath", ".git/hooks"); err != nil {
		t.Fatalf("isolate git hooks: %v\n%s", err, out)
	}
	for _, entry := range []struct{ key, value string }{{"user.name", "Prime Test"}, {"user.email", "prime@example.test"}} {
		if out, err := ssExec(ctx, "git", root, env, "config", entry.key, entry.value); err != nil {
			t.Fatalf("git config %s: %v\n%s", entry.key, err, out)
		}
	}
	if out, err := ssExec(ctx, bd, root, env, "init", "--quiet", "--prefix", "prime", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}
	return root, env
}
