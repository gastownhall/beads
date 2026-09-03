//go:build cgo && integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
