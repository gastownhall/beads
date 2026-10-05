//go:build cgo

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil"
)

// A workspace can declare server mode without ever writing metadata.json:
// `dolt.mode: server` in .beads/config.yaml, or BEADS_DOLT_SERVER_MODE=1 in the
// environment. Both are resolved by configfile.IsDoltServerMode, and gc
// provisions the config.yaml shape on every `gc-beads-bd start`.
//
// Before the fix, the metadata-less branch in the root PersistentPreRunE gated
// its config substitution on HostImpliesServerMode alone. That predicate
// answers a narrower question — "does a configured HOST imply server mode?" —
// and deliberately returns false as soon as config.yaml names a dolt.mode; it
// never consults BEADS_DOLT_SERVER_MODE at all. So cfg stayed nil, ServerMode
// stayed false, and bd created and then read a brand-new
// .beads/embeddeddolt/beads database: every query answered out of an empty
// relic, exit 0, "No issues found." A reader cannot tell that from real
// emptiness, which is why these tests assert on the phantom directory and on
// the false-empty separately.
//
// The invariant under test is one-directional and does not depend on a server
// being reachable: a workspace that selects server mode must reach the server
// or fail loudly. It must never quietly become an embedded workspace.

const phantomEmbeddedDir = "embeddeddolt"

// TestMetadatalessServerModeWorkspaceNeverOpensPhantomEmbeddedDatabase covers
// the two ways a workspace with no metadata.json selects server mode. Both
// shapes are admitted by the legacy-upgrade guard as-is, so this test exercises
// the store-selection gate directly on any branch.
func TestMetadatalessServerModeWorkspaceNeverOpensPhantomEmbeddedDatabase(t *testing.T) {
	bd := buildBDUnderTest(t)

	cases := []struct {
		name  string
		setup func(t *testing.T, beadsDir string, port int) []string
	}{
		{
			name: "config.yaml dolt.mode server",
			setup: func(t *testing.T, beadsDir string, port int) []string {
				t.Helper()
				writeFile(t, filepath.Join(beadsDir, "config.yaml"),
					[]byte(fmt.Sprintf("dolt:\n  mode: server\n  port: %d\n", port)))
				return nil
			},
		},
		{
			name: "BEADS_DOLT_SERVER_MODE env var",
			setup: func(t *testing.T, beadsDir string, port int) []string {
				t.Helper()
				return []string{
					"BEADS_DOLT_SERVER_MODE=1",
					fmt.Sprintf("BEADS_DOLT_SERVER_PORT=%d", port),
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := t.TempDir()
			initGitRepo(t, repoDir)
			beadsDir := filepath.Join(repoDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			port := freeLoopbackPort(t)
			env := tc.setup(t, beadsDir, port)
			writeFile(t, filepath.Join(beadsDir, localVersionFile), []byte("1.3.0\n"))
			if err := os.Chmod(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}

			// --db names the local Dolt path bd itself computes for this
			// workspace. It is the shortest route to store selection for a
			// metadata-less workspace that has no local database directory
			// yet, which is exactly the state a server-mode workspace is
			// supposed to stay in.
			run := runBDInWorkspace(t, bd, repoDir,
				[]string{"list", "--json", "--limit", "0", "--all"}, env,
				filepath.Join(beadsDir, "dolt"))

			assertNoPhantomEmbeddedDatabase(t, beadsDir)
			assertNotFalseEmpty(t, run)
			if !strings.Contains(run.output, fmt.Sprintf("127.0.0.1:%d", port)) {
				t.Fatalf("bd did not report the configured server endpoint 127.0.0.1:%d:\n%s", port, run.output)
			}
		})
	}
}

// TestGCProvisionedServerWorkspaceNeverOpensPhantomEmbeddedDatabase pins the
// byte shape gc creates: config.yaml declaring server mode, a .beads/dolt data
// root owned by the sql-server, the gitignored port file, and a current-era
// version witness — and still no metadata.json.
//
// Two different mechanisms keep this shape safe depending on what else has
// landed, and the assertions deliberately cover only what both guarantee. On a
// branch without gastownhall/beads#6119 the legacy-upgrade guard refuses the
// workspace outright (over-broadly — that is what #6119 fixes). Once #6119
// removes that refusal, the store-selection gate fixed here is the only thing
// standing between this shape and a phantom embedded database, so the test
// stays meaningful in exactly the configuration that ships.
func TestGCProvisionedServerWorkspaceNeverOpensPhantomEmbeddedDatabase(t *testing.T) {
	bd := buildBDUnderTest(t)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	port := freeLoopbackPort(t)
	writeFile(t, filepath.Join(beadsDir, "config.yaml"),
		[]byte(fmt.Sprintf("dolt:\n  mode: server\n  port: %d\n", port)))
	writeFile(t, filepath.Join(beadsDir, "dolt-server.port"), []byte(fmt.Sprintf("%d\n", port)))
	writeFile(t, filepath.Join(beadsDir, localVersionFile), []byte("1.3.0\n"))
	if err := os.Chmod(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"list", "--json", "--limit", "0", "--all"},
		{"ready", "--json"},
	} {
		t.Run(args[0], func(t *testing.T) {
			run := runBDInWorkspace(t, bd, repoDir, args, nil, "")
			assertNoPhantomEmbeddedDatabase(t, beadsDir)
			assertNotFalseEmpty(t, run)
		})
	}
}

type bdRun struct {
	args     []string
	output   string
	stdout   string
	exitCode int
	err      error
}

// runBDInWorkspace runs bd as a subprocess with auto-start disabled, so a
// server-mode workspace cannot mask a misrouted open by starting a server of
// its own. dbPath, when non-empty, is passed as --db.
//
// Every ambient BEADS_DOLT_* and BEADS_DIR is stripped first. This package's
// TestMain starts a shared Dolt server and exports its endpoint, and env beats
// config.yaml in every layer of the precedence chain being tested here — a
// leaked BEADS_DOLT_SERVER_PORT would retarget the workspace at that server
// and quietly change what the assertions mean.
func runBDInWorkspace(t *testing.T, bd, repoDir string, args, extraEnv []string, dbPath string) bdRun {
	t.Helper()
	if dbPath != "" {
		args = append([]string{"--db", dbPath}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bd, args...)
	cmd.Dir = repoDir
	cmd.Env = append(envWithoutBeadsStorageSettings(),
		"BD_DISABLE_METRICS=1",
		"BEADS_DOLT_AUTO_START=0",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		t.Fatalf("bd %s did not finish before the deadline: %v\n%s%s",
			strings.Join(args, " "), ctxErr, stdout.String(), stderr.String())
	}
	run := bdRun{
		args:   args,
		output: stdout.String() + stderr.String(),
		stdout: stdout.String(),
		err:    err,
	}
	run.exitCode = cmd.ProcessState.ExitCode()
	return run
}

// assertNoPhantomEmbeddedDatabase is the load-bearing assertion: bd must not
// have created .beads/embeddeddolt for a workspace that selects server mode.
// Once that directory exists it is self-perpetuating — discovery prefers it
// over .beads/dolt on every later run — so the damage outlives the invocation
// that caused it.
func assertNoPhantomEmbeddedDatabase(t *testing.T, beadsDir string) {
	t.Helper()
	path := filepath.Join(beadsDir, phantomEmbeddedDir)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(path)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("bd created a phantom embedded database at %s (contains %v) for a server-mode workspace", path, names)
	}
}

// assertNotFalseEmpty rejects the failure this bug produced: a successful,
// empty answer sourced from somewhere other than the configured server. An
// error is a fine outcome here — a reader can act on it.
func assertNotFalseEmpty(t *testing.T, run bdRun) {
	t.Helper()
	if run.err == nil {
		t.Fatalf("bd %s exited 0 for an unreachable server-mode workspace; a silent empty result is indistinguishable from real emptiness:\n%s",
			strings.Join(run.args, " "), run.output)
	}
	if strings.Contains(run.output, "no beads configuration found") {
		t.Fatalf("bd %s fell through to the embedded default instead of the configured server:\n%s",
			strings.Join(run.args, " "), run.output)
	}
	for _, empty := range []string{"No issues found.", "No ready work"} {
		if strings.Contains(run.stdout, empty) {
			t.Fatalf("bd %s reported %q instead of reaching the configured server:\n%s",
				strings.Join(run.args, " "), empty, run.output)
		}
	}
	if strings.TrimSpace(run.stdout) == "[]" {
		t.Fatalf("bd %s printed an empty JSON result instead of reaching the configured server:\n%s",
			strings.Join(run.args, " "), run.output)
	}
}

func envWithoutBeadsStorageSettings() []string {
	kept := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "BEADS_DOLT_") || name == "BEADS_DIR" {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	return port
}
