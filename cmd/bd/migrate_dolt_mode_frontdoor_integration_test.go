//go:build cgo && integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runBDExecSeparated(t *testing.T, bd, dir string, env []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir, cmd.Env = dir, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func createDependencyPair(t *testing.T, bd, dir string, env []string, sentinelID string) string {
	t.Helper()
	blockerOut, err := runBDExecWithBinary(t, bd, dir, env, "create", "migration blocker", "--json")
	require.NoError(t, err, "create blocker: %s", blockerOut)
	var blocker struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(blockerOut), &blocker))
	require.NotEmpty(t, blocker.ID)
	depOut, depErr := runBDExecWithBinary(t, bd, dir, env, "dep", "add", sentinelID, blocker.ID)
	require.NoError(t, depErr, "dep add: %s", depOut)
	return blocker.ID
}

func assertDependencyPair(t *testing.T, bd, dir string, env []string, sentinelID, blockerID string) {
	t.Helper()
	out, err := runBDExecWithBinary(t, bd, dir, env, "dep", "list", sentinelID, "--json")
	require.NoError(t, err, "dep list: %s", out)
	assert.Contains(t, out, blockerID, "dependency edge missing")
}

func migrationFrontDoorBinary(t *testing.T) string {
	if p := os.Getenv("BEADS_TEST_BD_BINARY"); p != "" {
		return p
	}
	out := filepath.Join(t.TempDir(), "bd")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = "."
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build cgo bd: %v\n%s", err, b)
	}
	return out
}

func migrationFrontDoorEnv(home string) []string {
	env := make([]string, 0)
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		// The cmd/bd package TestMain sets BEADS_TEST_MODE and related
		// controls for its in-process tests. Never inherit those into the
		// real front-door subprocess: BEADS_TEST_MODE disables auto-start
		// and makes a freshly initialized server resolve to port 1.
		if strings.HasPrefix(key, "BEADS_") || strings.HasPrefix(key, "BD_") || key == "HOME" || key == "USERPROFILE" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "USERPROFILE="+home)
}

// TestMigrateDoltModeFrontDoor exercises migration through the real bd
// executable. It is opt-in because it requires a local Dolt installation.
func TestMigrateDoltModeFrontDoor(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	dir := t.TempDir()
	home := t.TempDir()
	env := migrationFrontDoorEnv(home)
	out, err := runBDExecWithBinary(t, bd, dir, env, "init", "--backend", "dolt", "--server", "--prefix", "fd", "--quiet")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	createOut, err := runBDExecWithBinary(t, bd, dir, env, "create", "front-door sentinel", "--json")
	if err != nil {
		t.Fatalf("create sentinel: %v\n%s", err, createOut)
	}
	statusOut, _ := runBDExecWithBinary(t, bd, dir, env, "dolt", "status")
	if strings.Contains(statusOut, "Dolt server: running") {
		if out, err = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop"); err != nil {
			t.Fatalf("stop running server: %v\n%s", err, out)
		}
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil || created.ID == "" {
		t.Fatalf("invalid create JSON: %s", createOut)
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "migrate", "from-server-to-proxied-server"); err != nil {
		t.Fatalf("forward migration: %v\n%s", err, out)
	}
	metaProxy, _ := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	var proxyMetadata map[string]any
	if err := json.Unmarshal(metaProxy, &proxyMetadata); err != nil || proxyMetadata["dolt_mode"] != "proxied-server" {
		t.Fatalf("forward did not persist proxied mode: %s", metaProxy)
	}
	info, err := os.ReadFile(filepath.Join(dir, ".beads", "proxied_server_client_info.json"))
	if err != nil || len(info) == 0 {
		t.Fatalf("missing proxied sidecar: %v", err)
	}
	if !strings.Contains(out, "Switched to proxied-server mode") {
		t.Fatalf("unexpected forward output: %s", out)
	}
	list, err := runBDExecWithBinary(t, bd, dir, env, "list", "--json")
	if err != nil || !json.Valid([]byte(list)) || !strings.Contains(list, created.ID) {
		t.Fatalf("proxied list JSON: %v\n%s", err, list)
	}
	// A completed forward migration must refuse while its proxy process still
	// owns the target. This is exercised through the actual bd front door,
	// before provider startup can hide the ownership conflict.
	runningOut, runningErr := runBDExecWithBinary(t, bd, dir, env, "--json", "migrate", "from-server-to-proxied-server")
	if runningErr == nil {
		t.Fatalf("forward no-op unexpectedly succeeded while proxy was running: %s", runningOut)
	}
	var runningRefusal map[string]any
	if err := json.Unmarshal([]byte(runningOut), &runningRefusal); err != nil {
		t.Fatalf("running-proxy refusal JSON: %v\n%s", err, runningOut)
	}
	if data, ok := runningRefusal["data"].(map[string]any); ok {
		runningRefusal = data
	}
	if runningRefusal["code"] != "proxy.migrate.invalid_state" || runningRefusal["mutates"] != false {
		t.Fatalf("running-proxy refusal = %s", runningOut)
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "update", created.ID, "--title", "front-door sentinel updated"); err != nil {
		t.Fatalf("proxied update: %v\n%s", err, out)
	}
	statusOut, _ = runBDExecWithBinary(t, bd, dir, env, "dolt", "status")
	if strings.Contains(statusOut, "Dolt server: running") {
		if out, err = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop"); err != nil {
			t.Fatalf("stop proxied server: %v\n%s", err, out)
		}
	}
	// The proxy can supervise a Dolt child whose status text is stale; stop is
	// idempotent and its "not running" result is safe to ignore.
	if out, err = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop"); err != nil && !strings.Contains(strings.ToLower(out), "not running") {
		t.Fatalf("stop before reverse: %v\n%s", err, out)
	}
	sidecarBefore, err := os.ReadFile(filepath.Join(dir, ".beads", "proxied_server_client_info.json"))
	if err != nil {
		t.Fatal(err)
	}
	metaBefore, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "--json", "migrate", "from-proxied-server-to-server", "--dry-run"); err != nil {
		t.Fatalf("reverse dry-run: %v\n%s", err, out)
	}
	if _, err = os.Stat(filepath.Join(dir, ".beads", "proxied_server_client_info.json")); err != nil {
		t.Fatalf("dry-run removed sidecar: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".beads", "proxied_server_client_info.json")); string(got) != string(sidecarBefore) {
		t.Fatal("dry-run changed sidecar")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json")); string(got) != string(metaBefore) {
		t.Fatal("dry-run changed metadata")
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "migrate", "from-proxied-server-to-server"); err != nil {
		t.Fatalf("reverse migration: %v\n%s", err, out)
	}
	show, err := runBDExecWithBinary(t, bd, dir, env, "show", created.ID, "--json")
	if err != nil {
		t.Fatalf("show sentinel: %v\n%s", err, show)
	}
	if !json.Valid([]byte(show)) || !strings.Contains(show, "front-door sentinel updated") {
		t.Fatalf("sentinel missing or invalid JSON: %s", show)
	}
	meta, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	var serverMetadata map[string]any
	if err != nil || json.Unmarshal(meta, &serverMetadata) != nil || serverMetadata["dolt_mode"] != "server" {
		t.Fatalf("metadata mode not restored: %s", meta)
	}
	if _, err = os.Stat(filepath.Join(dir, ".beads", "proxied_server_client_info.json")); !os.IsNotExist(err) {
		t.Fatal("sidecar remains after reverse")
	}
	list, err = runBDExecWithBinary(t, bd, dir, env, "list", "--json")
	if err != nil || !json.Valid([]byte(list)) || !strings.Contains(list, "front-door sentinel updated") {
		t.Fatalf("direct list JSON: %v\n%s", err, list)
	}
}

func TestMigrateDoltModeFrontDoorRefusesMalformedSidecar(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	dir, home := t.TempDir(), t.TempDir()
	env := migrationFrontDoorEnv(home)
	if out, err := runBDExecWithBinary(t, bd, dir, env, "init", "--backend", "dolt", "--proxied-server", "--quiet"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	path := filepath.Join(dir, ".beads", "proxied_server_client_info.json")
	if err := os.WriteFile(path, []byte("{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	out, err := runBDExecWithBinary(t, bd, dir, env, "--json", "migrate", "from-proxied-server-to-server")
	if err == nil {
		t.Fatal("malformed sidecar unexpectedly succeeded")
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("expected JSON error output, got %s", out)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	if string(before) != string(after) {
		t.Fatal("refusal mutated metadata")
	}
}

func TestMigrateDoltModeFrontDoorRefusesExternalEndpoint(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "tcp", want: "externally hosted proxied Dolt endpoint"},
		{name: "unix", want: "externally hosted proxied Dolt endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			env := migrationFrontDoorEnv(home)
			beadsDir := filepath.Join(dir, ".beads")
			root := filepath.Join(beadsDir, "dolt")
			if err := os.MkdirAll(filepath.Join(root, ".dolt"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"myproj","backend":"dolt","dolt_mode":"proxied-server"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			metaBefore, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
			if err != nil {
				t.Fatal(err)
			}
			sidecarPath := filepath.Join(beadsDir, "proxied_server_client_info.json")
			// Replace the managed sidecar with an externally-owned endpoint.
			// This exercises migration refusal without attempting a network
			// connection to an unreachable host/socket during init.
			external := map[string]any{"root_path": filepath.Join(beadsDir, "dolt"), "external": map[string]any{}}
			if tc.name == "tcp" {
				external["external"] = map[string]any{"host": "db.example", "port": 3307}
			} else {
				external["external"] = map[string]any{"socket": "/tmp/beads-front-door.sock"}
			}
			sidecarData, _ := json.Marshal(external)
			if err := os.WriteFile(sidecarPath, sidecarData, 0o600); err != nil {
				t.Fatal(err)
			}
			sidecarBefore, err := os.ReadFile(sidecarPath)
			if err != nil {
				t.Fatal(err)
			}
			treeBefore := snapshotMigrationTree(t, beadsDir)
			out, err := runBDExecWithBinary(t, bd, dir, env, "--json", "migrate", "from-proxied-server-to-server")
			if err == nil {
				t.Fatal("external endpoint migration unexpectedly succeeded")
			}
			if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
				t.Fatalf("expected exit code 1, got %v", err)
			}
			var refusal map[string]any
			if err := json.Unmarshal([]byte(out), &refusal); err != nil {
				t.Fatalf("invalid refusal JSON: %v\n%s", err, out)
			}
			if data, ok := refusal["data"].(map[string]any); ok {
				refusal = data
			}
			if refusal["code"] != "proxy.migrate.external_endpoint" || refusal["mutates"] != false {
				t.Fatalf("expected typed non-mutating refusal, got: %s", out)
			}
			if !strings.Contains(strings.ToLower(out), strings.ToLower(tc.want)) {
				t.Fatalf("expected refusal text %q, got: %s", tc.want, out)
			}
			// Refusal must happen before any proxy/provider process starts.
			probe := exec.Command("pgrep", "-af", "[d]b-proxy-child --root "+root)
			if processOut, probeErr := probe.CombinedOutput(); probeErr == nil && len(strings.TrimSpace(string(processOut))) > 0 {
				t.Fatalf("external refusal started a proxy process: %s", processOut)
			}
			if got, _ := os.ReadFile(filepath.Join(beadsDir, "metadata.json")); string(got) != string(metaBefore) {
				t.Fatal("external refusal mutated metadata")
			}
			if got, _ := os.ReadFile(sidecarPath); string(got) != string(sidecarBefore) {
				t.Fatal("external refusal mutated sidecar")
			}
			if _, err := os.Stat(filepath.Join(beadsDir, "dolt-mode-migration.json")); !os.IsNotExist(err) {
				t.Fatal("external refusal created a migration journal")
			}
			if got := snapshotMigrationTree(t, beadsDir); !reflect.DeepEqual(treeBefore, got) {
				t.Fatal("external refusal mutated migration artifacts")
			}
		})
	}
}

func TestMigrateDoltModeFrontDoorExternalJournalMatrix(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	phases := []string{"prepared", "target_configured", "old_controls_retired", "verified", "committed"}
	for _, reverse := range []bool{false, true} {
		for _, phase := range phases {
			for _, unix := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%s", map[bool]string{false: "forward", true: "reverse"}[reverse], phase, map[bool]string{false: "tcp", true: "unix"}[unix]), func(t *testing.T) {
					dir, home := t.TempDir(), t.TempDir()
					env := migrationFrontDoorEnv(home)
					beadsDir, root := filepath.Join(dir, ".beads"), filepath.Join(dir, ".beads", "dolt")
					require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(root, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
					src, target := "server", "proxied-server"
					command := []string{"from-server-to-proxied-server"}
					if reverse {
						src, target, command = "proxied-server", "server", []string{"from-proxied-server-to-server"}
					}
					mode := src
					if phase != "prepared" {
						mode = target
					}
					require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(fmt.Sprintf(`{"database":"myproj","backend":"dolt","dolt_mode":%q}`, mode)), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte(Version), 0o600))
					require.NoError(t, os.Chmod(beadsDir, 0o700))
					ext := map[string]any{"host": "db.example", "port": 3307}
					if unix {
						ext = map[string]any{"socket": "/tmp/beads-front-door.sock"}
					}
					sidecar, _ := json.Marshal(map[string]any{"root_path": root, "external": ext})
					require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "proxied_server_client_info.json"), sidecar, 0o600))
					journal, _ := json.Marshal(map[string]any{"version": 1, "source_mode": src, "target_mode": target, "root_path": root, "external": ext, "sidecar": map[string]any{"root_path": root, "external": ext}, "ownership": "external", "attempt": 1, "phase": phase})
					require.NoError(t, os.WriteFile(filepath.Join(beadsDir, migrateJournalFileName), journal, 0o600))
					gatePath := filepath.Join(beadsDir, "dolt.gate.lock")
					require.NoError(t, os.WriteFile(gatePath, []byte("preseeded-gate-lock"), 0o600))
					gateBefore, gateErr := os.ReadFile(gatePath)
					gateExists := gateErr == nil
					tree := snapshotMigrationTree(t, beadsDir)
					out, err := runBDExecWithBinary(t, bd, dir, env, append([]string{"--json", "migrate"}, command...)...)
					require.Error(t, err)
					exitErr, ok := err.(*exec.ExitError)
					require.True(t, ok)
					require.Equal(t, 1, exitErr.ExitCode())
					var payload map[string]any
					require.NoError(t, json.Unmarshal([]byte(out), &payload), out)
					if data, ok := payload["data"].(map[string]any); ok {
						payload = data
					}
					assert.Equal(t, "proxy.migrate.external_endpoint", payload["code"])
					assert.Equal(t, false, payload["mutates"])
					assert.Contains(t, payload["error"], "externally hosted proxied Dolt endpoint")
					assert.Equal(t, tree, snapshotMigrationTree(t, beadsDir))
					for _, p := range append([]string{migrateLockFileName}, serverAssetNames()...) {
						_, statErr := os.Stat(filepath.Join(beadsDir, p))
						assert.True(t, os.IsNotExist(statErr), "unexpected artifact %s", p)
					}
					gateAfter, gateAfterErr := os.ReadFile(gatePath)
					assert.Equal(t, gateExists, gateAfterErr == nil, "dolt gate lock existence changed")
					if gateExists {
						assert.Equal(t, gateBefore, gateAfter, "dolt gate lock changed")
					}
					for _, p := range proxiedAssetNames() {
						_, statErr := os.Stat(filepath.Join(root, p))
						assert.True(t, os.IsNotExist(statErr), "unexpected proxy artifact %s", p)
					}
					probe := exec.Command("pgrep", "-af", "[d]b-proxy-child --root "+root)
					processOut, probeErr := probe.CombinedOutput()
					assert.True(t, probeErr != nil || len(strings.TrimSpace(string(processOut))) == 0, "proxy process started: %s", processOut)
				})
			}
		}
	}
}

func TestMigrateDoltModeFrontDoorMalformedStateRefuses(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	for _, tc := range []struct {
		name string
		file string
		body string
	}{
		{"metadata", "metadata.json", "{malformed"},
		{"journal", migrateJournalFileName, "{malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			env := migrationFrontDoorEnv(home)
			beadsDir := filepath.Join(dir, ".beads")
			require.NoError(t, os.MkdirAll(beadsDir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte(Version), 0o600))
			if tc.name == "journal" {
				require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"myproj","backend":"dolt","dolt_mode":"proxied-server"}`), 0o600))
			}
			require.NoError(t, os.WriteFile(filepath.Join(beadsDir, tc.file), []byte(tc.body), 0o600))
			before := snapshotMigrationTree(t, beadsDir)
			out, _, err := runBDExecSeparated(t, bd, dir, env, "--json", "migrate", "from-proxied-server-to-server")
			require.Error(t, err)
			exitErr, ok := err.(*exec.ExitError)
			require.True(t, ok)
			require.Equal(t, 1, exitErr.ExitCode())
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &payload), out)
			if data, ok := payload["data"].(map[string]any); ok {
				payload = data
			}
			assert.Equal(t, "proxy.migrate.invalid_state", payload["code"])
			assert.Equal(t, false, payload["mutates"])
			assert.Equal(t, before, snapshotMigrationTree(t, beadsDir))
		})
	}
}

// TestMigrateDoltModeForwardNoopRefusalsFrontDoor keeps the completed-target
// path fail-closed at the real CLI boundary. Both output modes must preserve
// the workspace byte-for-byte while refusing ambiguous ownership/artifacts.
func TestMigrateDoltModeForwardNoopRefusalsFrontDoor(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	cases := []struct {
		name   string
		shared bool
		info   map[string]any
		asset  string
		want   string
	}{
		{name: "direct external sidecar", info: map[string]any{"external": map[string]any{"host": "db.example", "port": 3307}}, want: "externally hosted"},
		{name: "direct mismatched sidecar", info: map[string]any{"root_path": "/tmp/other"}, want: "canonical"},
		{name: "direct stale control", asset: "proxy.log", want: "stale proxy control"},
		{name: "shared mismatched sidecar", shared: true, info: map[string]any{"root_path": "/tmp/other"}, want: "canonical"},
		{name: "shared external sidecar", shared: true, info: map[string]any{"external": map[string]any{"host": "db.example", "port": 3307}}, want: "externally hosted"},
		{name: "shared stale control", shared: true, asset: "proxy.log", want: "stale proxy control"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			env := migrationFrontDoorEnv(home)
			beadsDir := filepath.Join(dir, ".beads")
			root := filepath.Join(beadsDir, "dolt")
			if tc.shared {
				shared := filepath.Join(home, "shared-server")
				env = append(env, "BEADS_SHARED_SERVER_DIR="+shared, "BEADS_DOLT_SHARED_SERVER=1")
				root = filepath.Join(shared, "dolt")
			}
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
			require.NoError(t, os.MkdirAll(beadsDir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"myproj","backend":"dolt","dolt_mode":"proxied-server"}`), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte(Version), 0o600))
			if tc.shared {
				require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: false\n"), 0o600))
			}
			if tc.info == nil {
				tc.info = map[string]any{"root_path": root}
			}
			if _, ok := tc.info["root_path"]; !ok {
				tc.info["root_path"] = root
			}
			b, err := json.Marshal(tc.info)
			require.NoError(t, err)
			sidecar := filepath.Join(beadsDir, "proxied_server_client_info.json")
			require.NoError(t, os.WriteFile(sidecar, b, 0o600))
			if tc.asset != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, tc.asset), []byte("stale"), 0o600))
			}
			before := snapshotMigrationTree(t, beadsDir, root)
			for _, jsonMode := range []bool{false, true} {
				args := []string{"migrate", "from-server-to-proxied-server"}
				if tc.shared {
					args[1] = "from-shared-server-to-proxied-server"
				}
				if jsonMode {
					args = append([]string{"--json"}, args...)
				}
				stdout, stderr, runErr := runBDExecSeparated(t, bd, dir, env, args...)
				require.Error(t, runErr)
				exitErr, ok := runErr.(*exec.ExitError)
				require.True(t, ok)
				require.Equal(t, 1, exitErr.ExitCode())
				if jsonMode {
					var payload map[string]any
					require.NoError(t, json.Unmarshal([]byte(stdout), &payload), stdout)
					if data, ok := payload["data"].(map[string]any); ok {
						payload = data
					}
					assert.Equal(t, "proxy.migrate.invalid_state", payload["code"])
					assert.Equal(t, false, payload["mutates"])
				} else {
					assert.Contains(t, strings.ToLower(stderr), strings.ToLower(tc.want))
				}
				assert.Equal(t, before, snapshotMigrationTree(t, beadsDir, root))
			}
		})
	}
}

// TestMigrateDoltModeDirectForwardFaultsFrontDoor runs every checkpoint fault
// through a real managed-local bd process, then retries twice. The issue row
// proves the physical Dolt data survived the journal repair and idempotent
// second invocation.
func TestMigrateDoltModeDirectForwardFaultsFrontDoor(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	for _, phase := range []string{"prepared", "target_configured", "old_controls_retired", "verified", "committed"} {
		t.Run(phase, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			env := migrationFrontDoorEnv(home)
			out, err := runBDExecWithBinary(t, bd, dir, env, "init", "--backend", "dolt", "--server", "--prefix", "fault", "--quiet")
			require.NoError(t, err, "init: %s", out)
			created, err := runBDExecWithBinary(t, bd, dir, env, "create", "migration sentinel", "--json")
			require.NoError(t, err, "create: %s", created)
			var row struct {
				ID string `json:"id"`
			}
			require.NoError(t, json.Unmarshal([]byte(created), &row))
			blockerID := createDependencyPair(t, bd, dir, env, row.ID)
			_, _ = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop")
			beadsDir := filepath.Join(dir, ".beads")
			root := filepath.Join(beadsDir, "dolt")
			touchFile(t, filepath.Join(root, "migration-sentinel"))
			sentinelBefore := snapshotMigrationTree(t, beadsDir, root)
			faultEnv := append(append([]string(nil), env...), "BEADS_MIGRATION_FAIL_PHASE="+phase)
			out, err = runBDExecWithBinary(t, bd, dir, faultEnv, "migrate", "from-server-to-proxied-server")
			require.Error(t, err, "fault phase unexpectedly succeeded: %s", out)
			exitErr, ok := err.(*exec.ExitError)
			require.True(t, ok)
			require.Equal(t, 1, exitErr.ExitCode())
			j, jErr := os.Stat(filepath.Join(beadsDir, migrateJournalFileName))
			require.NoError(t, jErr)
			require.False(t, j.IsDir())
			afterFailure := snapshotMigrationTree(t, beadsDir, root)
			require.Equal(t, sentinelBefore[filepath.Join(root, "migration-sentinel")], afterFailure[filepath.Join(root, "migration-sentinel")])
			retryEnv := append(append([]string(nil), env...), "BEADS_MIGRATION_FAIL_PHASE=")
			require.NoError(t, func() error {
				_, e := runBDExecWithBinary(t, bd, dir, retryEnv, "migrate", "from-server-to-proxied-server")
				return e
			}())
			after := snapshotMigrationTree(t, beadsDir, root)
			require.NoError(t, func() error {
				_, e := runBDExecWithBinary(t, bd, dir, retryEnv, "migrate", "from-server-to-proxied-server")
				return e
			}())
			assert.Equal(t, after, snapshotMigrationTree(t, beadsDir, root))
			assertDirectMigrationFinal(t, beadsDir, root, false, filepath.Join(root, "migration-sentinel"))
			assertDependencyPair(t, bd, dir, retryEnv, row.ID, blockerID)
			show, showErr := runBDExecWithBinary(t, bd, dir, retryEnv, "show", row.ID, "--json")
			require.NoError(t, showErr, "show sentinel: %s", show)
			assert.Contains(t, show, "migration sentinel")
			_, _ = runBDExecWithBinary(t, bd, dir, retryEnv, "dolt", "stop")
		})
	}
}

// TestMigrateDoltModeDirectReverseFaultsFrontDoor covers the reverse
// managed-local transition through the executable for every checkpoint.
func TestMigrateDoltModeDirectReverseFaultsFrontDoor(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	for _, phase := range []string{"prepared", "target_configured", "old_controls_retired", "verified", "committed"} {
		t.Run(phase, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			env := migrationFrontDoorEnv(home)
			out, err := runBDExecWithBinary(t, bd, dir, env, "init", "--backend", "dolt", "--server", "--prefix", "reverse", "--quiet")
			require.NoError(t, err, "init: %s", out)
			created, err := runBDExecWithBinary(t, bd, dir, env, "create", "reverse migration sentinel", "--json")
			require.NoError(t, err, "create: %s", created)
			var row struct {
				ID string `json:"id"`
			}
			require.NoError(t, json.Unmarshal([]byte(created), &row))
			blockerID := createDependencyPair(t, bd, dir, env, row.ID)
			_, _ = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop")
			_, err = runBDExecWithBinary(t, bd, dir, env, "migrate", "from-server-to-proxied-server")
			require.NoError(t, err)
			_, _ = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop")
			beadsDir := filepath.Join(dir, ".beads")
			root := filepath.Join(beadsDir, "dolt")
			touchFile(t, filepath.Join(root, "migration-sentinel"))
			before := snapshotMigrationTree(t, beadsDir, root)
			faultEnv := append(append([]string(nil), env...), "BEADS_MIGRATION_FAIL_PHASE="+phase)
			out, err = runBDExecWithBinary(t, bd, dir, faultEnv, "migrate", "from-proxied-server-to-server")
			require.Error(t, err, "fault phase unexpectedly succeeded: %s", out)
			exitErr, ok := err.(*exec.ExitError)
			require.True(t, ok)
			require.Equal(t, 1, exitErr.ExitCode())
			require.Equal(t, before[filepath.Join(root, "migration-sentinel")], snapshotMigrationTree(t, beadsDir, root)[filepath.Join(root, "migration-sentinel")])
			retryEnv := append(append([]string(nil), env...), "BEADS_MIGRATION_FAIL_PHASE=")
			out, err = runBDExecWithBinary(t, bd, dir, retryEnv, "migrate", "from-proxied-server-to-server")
			require.NoError(t, err, "retry: %s", out)
			after := snapshotMigrationTree(t, beadsDir, root)
			_, err = runBDExecWithBinary(t, bd, dir, retryEnv, "migrate", "from-proxied-server-to-server")
			require.NoError(t, err)
			assert.Equal(t, after, snapshotMigrationTree(t, beadsDir, root))
			assertDirectMigrationFinal(t, beadsDir, root, true, filepath.Join(root, "migration-sentinel"))
			assertDependencyPair(t, bd, dir, retryEnv, row.ID, blockerID)
			show, err := runBDExecWithBinary(t, bd, dir, retryEnv, "show", row.ID, "--json")
			require.NoError(t, err, "show: %s", show)
			assert.Contains(t, show, "reverse migration sentinel")
			_, _ = runBDExecWithBinary(t, bd, dir, retryEnv, "dolt", "stop")
		})
	}
}

func assertDirectMigrationFinal(t *testing.T, beadsDir, root string, reverse bool, sentinel string) {
	t.Helper()
	meta, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(meta, &got))
	if reverse {
		assert.Equal(t, "server", got["dolt_mode"])
		_, err = os.Stat(filepath.Join(beadsDir, configfile.ProxiedServerClientInfoFileName))
		assert.True(t, os.IsNotExist(err), "reverse sidecar remains")
		if value, ok := config.WorkspaceYamlValue(beadsDir, "dolt.shared-server"); ok {
			assert.Equal(t, "false", strings.ToLower(value), "direct reverse must not enable shared topology")
		}
		for _, path := range proxy.ControlFilePaths(root) {
			_, statErr := os.Stat(path)
			assert.True(t, os.IsNotExist(statErr), "stale proxy control %s", path)
		}
	} else {
		assert.Equal(t, "proxied-server", got["dolt_mode"])
		info, infoErr := configfile.LoadProxiedServerClientInfo(beadsDir)
		require.NoError(t, infoErr)
		require.NotNil(t, info)
		assert.Equal(t, filepath.Clean(root), filepath.Clean(info.ResolvedRootPath(beadsDir)))
		for _, path := range serverAssetNames() {
			_, statErr := os.Stat(filepath.Join(beadsDir, path))
			assert.True(t, os.IsNotExist(statErr), "stale server control %s", path)
		}
	}
	for _, path := range []string{filepath.Join(beadsDir, migrateJournalFileName), filepath.Join(beadsDir, migrateLockFileName)} {
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr), "stale migration artifact %s", path)
	}
	sentinelBytes, sentinelErr := os.ReadFile(sentinel)
	require.NoError(t, sentinelErr)
	assert.Equal(t, []byte("x"), sentinelBytes, "migration changed Dolt root sentinel")
	doltState, _ := doltserver.IsRunning(beadsDir)
	assert.False(t, doltState != nil && doltState.Running, "direct Dolt process still running")
	proxyRunning, _ := proxy.IsRunning(root)
	assert.False(t, proxyRunning, "direct proxy process still running")
}

// sharedMigrationFrontDoorSnapshot records both durable migration artifacts
// and the live ownership probes. The latter are deliberately queried rather
// than represented by pid/status files, which can be stale after a crash.
type sharedMigrationFrontDoorSnapshot struct {
	Tree         map[string][]byte
	Controls     map[string]string
	DoltRunning  bool
	DoltPID      int
	DoltPort     int
	ProxyRunning bool
}

func snapshotSharedMigrationFrontDoor(t *testing.T, beadsDir, sharedDir, root string) sharedMigrationFrontDoorSnapshot {
	t.Helper()
	doltState, _ := doltserver.IsRunning(sharedDir)
	doltRunning := doltState != nil && doltState.Running
	doltPID, doltPort := 0, 0
	if doltState != nil {
		doltPID, doltPort = doltState.PID, doltState.Port
	}
	proxyRunning, _ := proxy.IsRunning(root)
	controls := map[string]string{}
	paths := append([]string{
		filepath.Join(beadsDir, "metadata.json"),
		filepath.Join(beadsDir, "config.yaml"),
		filepath.Join(beadsDir, configfile.ProxiedServerClientInfoFileName),
		filepath.Join(beadsDir, migrateJournalFileName),
		filepath.Join(beadsDir, migrateLockFileName),
		filepath.Join(beadsDir, "dolt.gate.lock"),
	}, doltserver.StateFilePaths(sharedDir)...)
	paths = append(paths, proxy.ControlFilePaths(root)...)
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			controls[path] = "missing"
			continue
		}
		controls[path] = string(contents)
	}
	return sharedMigrationFrontDoorSnapshot{
		Tree:         snapshotMigrationTree(t, beadsDir, sharedDir),
		Controls:     controls,
		DoltRunning:  doltRunning,
		DoltPID:      doltPID,
		DoltPort:     doltPort,
		ProxyRunning: proxyRunning,
	}
}

func setupSharedMigrationFrontDoor(t *testing.T, bd string) (dir, home, sharedDir, sharedRoot, issueID, blockerID string, env []string) {
	t.Helper()
	dir, home = t.TempDir(), t.TempDir()
	sharedDir = filepath.Join(home, "shared-server")
	sharedRoot = filepath.Join(sharedDir, "dolt")
	env = migrationFrontDoorEnv(home)
	portProbe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := portProbe.Addr().(*net.TCPAddr).Port
	require.NoError(t, portProbe.Close())
	env = append(env, "BEADS_SHARED_SERVER_DIR="+sharedDir, "BEADS_DOLT_SHARED_SERVER=1", "BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port))
	out, err := runBDExecWithBinary(t, bd, dir, env, "init", "--backend", "dolt", "--shared-server", "--prefix", "shared", "--quiet")
	require.NoError(t, err, "shared init: %s", out)
	created, err := runBDExecWithBinary(t, bd, dir, env, "create", "shared migration sentinel", "--json")
	require.NoError(t, err, "shared create: %s", created)
	var row struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(created), &row))
	blockerID = createDependencyPair(t, bd, dir, env, row.ID)
	require.NotEmpty(t, row.ID)
	// Migrations require exclusive ownership. Stop the real shared Dolt
	// process before taking the baseline snapshot.
	status, _ := runBDExecWithBinary(t, bd, dir, env, "dolt", "status")
	if strings.Contains(status, "Dolt server: running") {
		stop, stopErr := runBDExecWithBinary(t, bd, dir, env, "dolt", "stop")
		require.NoError(t, stopErr, "stop shared server: %s", stop)
	}
	return dir, home, sharedDir, sharedRoot, row.ID, blockerID, env
}

func assertSharedMigrationResult(t *testing.T, bd, dir string, env []string, beadsDir, sharedDir, sharedRoot, issueID string, wantShared bool) {
	t.Helper()
	meta, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	require.NoError(t, err)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(meta, &metadata))
	wantMode := "proxied-server"
	if wantShared {
		wantMode = "server"
	}
	assert.Equal(t, wantMode, metadata["dolt_mode"])
	yamlValue, yamlSet := readWorkspaceYamlValueForFrontDoor(t, beadsDir, "dolt.shared-server")
	assert.True(t, yamlSet)
	assert.Equal(t, fmt.Sprintf("%t", wantShared), strings.ToLower(yamlValue))
	sidecarPath := filepath.Join(beadsDir, "proxied_server_client_info.json")
	if wantShared {
		_, statErr := os.Stat(sidecarPath)
		assert.True(t, os.IsNotExist(statErr), "reverse migration must remove sidecar")
	} else {
		sidecar, sidecarErr := os.ReadFile(sidecarPath)
		require.NoError(t, sidecarErr)
		assert.Contains(t, string(sidecar), sharedRoot)
	}
	_, journalErr := os.Stat(filepath.Join(beadsDir, migrateJournalFileName))
	assert.True(t, os.IsNotExist(journalErr), "migration journal must be finalized")
	// Probe ownership before `show`, because opening a shared workspace can
	// legitimately auto-start its managed server.
	doltState, _ := doltserver.IsRunning(sharedDir)
	assert.False(t, doltState != nil && doltState.Running, "shared Dolt process still running before show")
	proxyRunning, _ := proxy.IsRunning(sharedRoot)
	assert.False(t, proxyRunning, "proxy process still running before show")
	if wantShared {
		// Shared-server mode is an externally addressed managed server and does
		// not auto-start on read paths. Start it explicitly to prove the real
		// issue row remains readable after reverse migration.
		start, startErr := runBDExecWithBinary(t, bd, dir, env, "dolt", "start")
		require.NoError(t, startErr, "start shared server for verification: %s", start)
	}
	show, showErr := runBDExecWithBinary(t, bd, dir, env, "show", issueID, "--json")
	require.NoError(t, showErr, "show migrated issue: %s", show)
	assert.Contains(t, show, "shared migration sentinel")
	// `show` may auto-start the managed shared server. Stop it before probing
	// final ownership so the idempotence assertion covers the migration itself.
	_, _ = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop")
	// Neither migration direction may leave a managed child alive.
	doltState, _ = doltserver.IsRunning(sharedDir)
	assert.False(t, doltState != nil && doltState.Running, "shared Dolt process still running")
	proxyRunning, _ = proxy.IsRunning(sharedRoot)
	assert.False(t, proxyRunning, "proxy process still running")
}

func readWorkspaceYamlValueForFrontDoor(t *testing.T, beadsDir, key string) (string, bool) {
	t.Helper()
	value, ok := config.WorkspaceYamlValue(beadsDir, key)
	return value, ok
}

// TestMigrateDoltModeSharedForwardFaultsFrontDoor exercises all five
// checkpoints through the real cgo bd subprocess using a disposable managed
// shared-server root. Each injected fault is repaired by a retry and a second
// successful invocation proves idempotence without changing durable state.
func TestMigrateDoltModeSharedForwardFaultsFrontDoor(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	for _, phase := range []string{"prepared", "target_configured", "old_controls_retired", "verified", "committed"} {
		t.Run(phase, func(t *testing.T) {
			dir, _, sharedDir, sharedRoot, issueID, blockerID, env := setupSharedMigrationFrontDoor(t, bd)
			beadsDir := filepath.Join(dir, ".beads")
			touchFile(t, filepath.Join(sharedRoot, "migration-sentinel"))
			before := snapshotSharedMigrationFrontDoor(t, beadsDir, sharedDir, sharedRoot)
			faultEnv := append(append([]string(nil), env...), "BEADS_MIGRATION_FAIL_PHASE="+phase)
			out, err := runBDExecWithBinary(t, bd, dir, faultEnv, "migrate", "from-shared-server-to-proxied-server")
			require.Error(t, err, "fault phase unexpectedly succeeded: %s", out)
			require.FileExists(t, filepath.Join(beadsDir, migrateJournalFileName))
			failed := snapshotSharedMigrationFrontDoor(t, beadsDir, sharedDir, sharedRoot)
			assert.Equal(t, before.Tree[filepath.Join(sharedRoot, "migration-sentinel")], failed.Tree[filepath.Join(sharedRoot, "migration-sentinel")])
			retryEnv := append(append([]string(nil), env...), "BEADS_MIGRATION_FAIL_PHASE=")
			out, err = runBDExecWithBinary(t, bd, dir, retryEnv, "migrate", "from-shared-server-to-proxied-server")
			require.NoError(t, err, "retry: %s", out)
			after := snapshotSharedMigrationFrontDoor(t, beadsDir, sharedDir, sharedRoot)
			out, err = runBDExecWithBinary(t, bd, dir, retryEnv, "migrate", "from-shared-server-to-proxied-server")
			require.NoError(t, err, "second success: %s", out)
			assert.Equal(t, after, snapshotSharedMigrationFrontDoor(t, beadsDir, sharedDir, sharedRoot))
			assertSharedMigrationResult(t, bd, dir, retryEnv, beadsDir, sharedDir, sharedRoot, issueID, false)
			assertDependencyPair(t, bd, dir, retryEnv, issueID, blockerID)
		})
	}
}

// TestMigrateDoltModeSharedReverseFaultsFrontDoor is the reverse-direction
// counterpart of TestMigrateDoltModeSharedForwardFaultsFrontDoor.
func TestMigrateDoltModeSharedReverseFaultsFrontDoor(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := migrationFrontDoorBinary(t)
	for _, phase := range []string{"prepared", "target_configured", "old_controls_retired", "verified", "committed"} {
		t.Run(phase, func(t *testing.T) {
			dir, _, sharedDir, sharedRoot, issueID, blockerID, env := setupSharedMigrationFrontDoor(t, bd)
			beadsDir := filepath.Join(dir, ".beads")
			_, err := runBDExecWithBinary(t, bd, dir, env, "migrate", "from-shared-server-to-proxied-server")
			require.NoError(t, err)
			// Forward migration leaves the proxy stopped; keep this explicit so
			// the reverse matrix never races a child process from auto-start.
			_, _ = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop")
			touchFile(t, filepath.Join(sharedRoot, "migration-sentinel"))
			before := snapshotSharedMigrationFrontDoor(t, beadsDir, sharedDir, sharedRoot)
			faultEnv := append(append([]string(nil), env...), "BEADS_MIGRATION_FAIL_PHASE="+phase)
			out, err := runBDExecWithBinary(t, bd, dir, faultEnv, "migrate", "from-proxied-server-to-shared-server")
			require.Error(t, err, "fault phase unexpectedly succeeded: %s", out)
			require.FileExists(t, filepath.Join(beadsDir, migrateJournalFileName))
			failed := snapshotSharedMigrationFrontDoor(t, beadsDir, sharedDir, sharedRoot)
			assert.Equal(t, before.Tree[filepath.Join(sharedRoot, "migration-sentinel")], failed.Tree[filepath.Join(sharedRoot, "migration-sentinel")])
			retryEnv := append(append([]string(nil), env...), "BEADS_MIGRATION_FAIL_PHASE=")
			out, err = runBDExecWithBinary(t, bd, dir, retryEnv, "migrate", "from-proxied-server-to-shared-server")
			require.NoError(t, err, "retry: %s", out)
			after := snapshotSharedMigrationFrontDoor(t, beadsDir, sharedDir, sharedRoot)
			out, err = runBDExecWithBinary(t, bd, dir, retryEnv, "migrate", "from-proxied-server-to-shared-server")
			require.NoError(t, err, "second success: %s", out)
			assert.Equal(t, after, snapshotSharedMigrationFrontDoor(t, beadsDir, sharedDir, sharedRoot))
			assertSharedMigrationResult(t, bd, dir, retryEnv, beadsDir, sharedDir, sharedRoot, issueID, true)
			assertDependencyPair(t, bd, dir, retryEnv, issueID, blockerID)
		})
	}
}
