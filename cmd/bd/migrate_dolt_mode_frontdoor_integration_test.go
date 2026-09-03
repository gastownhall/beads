//go:build cgo && integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateDoltModeFrontDoor exercises migration through the real bd
// executable. It is opt-in because it requires a local Dolt installation.
func TestMigrateDoltModeFrontDoor(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "BEADS_DOLT_SHARED_SERVER=", "BEADS_SHARED_SERVER_DIR=")
	out, err := runBDExecWithBinary(t, bd, dir, env, "init", "--backend", "dolt", "--server", "--prefix", "fd", "--quiet")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	createOut, err := runBDExecWithBinary(t, bd, dir, env, "create", "front-door sentinel", "--json")
	if err != nil {
		t.Fatalf("create sentinel: %v\n%s", err, createOut)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil || created.ID == "" {
		t.Fatalf("invalid create JSON: %s", createOut)
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop"); err != nil {
		t.Fatalf("stop server: %v\n%s", err, out)
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "migrate", "from-server-to-proxied-server"); err != nil {
		t.Fatalf("forward migration: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Switched to proxied-server mode") {
		t.Fatalf("unexpected forward output: %s", out)
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
	if !json.Valid([]byte(show)) || !strings.Contains(show, "front-door sentinel") {
		t.Fatalf("sentinel missing or invalid JSON: %s", show)
	}
	meta, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	if err != nil || !strings.Contains(string(meta), `"dolt_mode":"server"`) {
		t.Fatalf("metadata mode not restored: %s", meta)
	}
	if _, err = os.Stat(filepath.Join(dir, ".beads", "proxied_server_client_info.json")); !os.IsNotExist(err) {
		t.Fatal("sidecar remains after reverse")
	}
}

func TestMigrateDoltModeFrontDoorRefusesMalformedSidecar(t *testing.T) {
	if os.Getenv("BEADS_TEST_MIGRATION_FRONTDOOR") != "1" {
		t.Skip("set BEADS_TEST_MIGRATION_FRONTDOOR=1")
	}
	bd := buildBDForInitTests(t)
	dir, home := t.TempDir(), t.TempDir()
	env := append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "BEADS_DOLT_SHARED_SERVER=", "BEADS_SHARED_SERVER_DIR=")
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
