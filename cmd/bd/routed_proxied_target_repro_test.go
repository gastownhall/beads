package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestRoutedLookupOpensProxiedServerTargetRig reproduces #5861 item 2: a
// prefix route into a rig whose metadata.json says proxied-server must not be
// refused as "no proxied-server route" before any lookup is attempted.
//
// Adopted from the triage repro with one fixture correction its text implies:
// the sidecar names an external SQL server, because an external endpoint is
// what makes the routed read possible. A proxied rig with NO sidecar (or one
// with no external block) is a managed-local target, which the approved
// design keeps refusing with exactly the typed proxy.store.unrouted refusal
// this test rules out for the supported topology — see
// TestForeignProxiedExternalReadDispatch for that boundary. The sidecar here
// names a loopback port nothing listens on, so the expected outcome is an
// ordinary unreachable-endpoint error from actually attempting the target,
// never the unrouted refusal.
func TestRoutedLookupOpensProxiedServerTargetRig(t *testing.T) {
	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	rigCfg := configfile.DefaultConfig()
	rigCfg.DoltMode = configfile.DoltModeProxiedServer
	rigCfg.DoltDatabase = "gt"
	if err := rigCfg.Save(rigBeadsDir); err != nil {
		t.Fatalf("save rig metadata.json: %v", err)
	}
	if err := configfile.SaveProxiedServerClientInfo(rigBeadsDir, &configfile.ProxiedServerClientInfo{
		External: &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 1},
	}); err != nil {
		t.Fatalf("save rig sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = filepath.Join(townBeadsDir, "dolt")
	t.Cleanup(func() { dbPath = oldDBPath })

	_, err := resolveViaPrefixRouting(context.Background(), "gt-target1")
	var capErr *ProxyCapabilityError
	if errors.As(err, &capErr) && capErr.Code == "proxy.store.unrouted" {
		t.Fatalf("routed lookup refused the proxied-server target rig instead of reading it: %v", err)
	}
	if err == nil {
		t.Fatalf("routed lookup unexpectedly succeeded against a port nothing listens on")
	}
	t.Logf("routed lookup error (not the unrouted refusal): %v", err)
}
