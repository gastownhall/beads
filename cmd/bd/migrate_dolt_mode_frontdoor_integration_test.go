//go:build cgo && integration

package main

import (
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
	if out, err = runBDExecWithBinary(t, bd, dir, env, "dolt", "stop"); err != nil {
		t.Fatalf("stop server: %v\n%s", err, out)
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "migrate", "from-server-to-proxied-server"); err != nil {
		t.Fatalf("forward migration: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Switched to proxied-server mode") {
		t.Fatalf("unexpected forward output: %s", out)
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "--json", "migrate", "from-proxied-server-to-server", "--dry-run"); err != nil {
		t.Fatalf("reverse dry-run: %v\n%s", err, out)
	}
	if _, err = os.Stat(filepath.Join(dir, ".beads", "proxied_server_client_info.json")); err != nil {
		t.Fatalf("dry-run removed sidecar: %v", err)
	}
	if out, err = runBDExecWithBinary(t, bd, dir, env, "migrate", "from-proxied-server-to-server"); err != nil {
		t.Fatalf("reverse migration: %v\n%s", err, out)
	}
}
