package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestRoutedMutationProxiedTargetKeepsRefusal pins AC-7 of #5861 item 2: the
// external read route is a READ route. A routed mutation into a proxied rig
// keeps the typed proxy.store.unrouted refusal whatever the sidecar says —
// even the externally readable topology the read route opens — and so does
// the managed-local target, which nothing routes into at all.
func TestRoutedMutationProxiedTargetKeepsRefusal(t *testing.T) {
	ctx := context.Background()
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
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = filepath.Join(townBeadsDir, "dolt")
	t.Cleanup(func() { dbPath = oldDBPath })

	assertUnrouted := func(t *testing.T, err error) {
		t.Helper()
		var capErr *ProxyCapabilityError
		if !errors.As(err, &capErr) || capErr.Code != "proxy.store.unrouted" {
			t.Fatalf("routed mutation into a proxied rig must keep the typed proxy.store.unrouted refusal, got: %v", err)
		}
	}

	t.Run("externally readable target", func(t *testing.T) {
		if err := configfile.SaveProxiedServerClientInfo(rigBeadsDir, &configfile.ProxiedServerClientInfo{
			External: &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 1},
		}); err != nil {
			t.Fatalf("save rig sidecar: %v", err)
		}
		_, err := resolveViaPrefixRoutingWithAccess(ctx, "gt-target1", true, false)
		assertUnrouted(t, err)
	})

	t.Run("managed-local target", func(t *testing.T) {
		if err := os.Remove(configfile.ProxiedServerClientInfoPath(rigBeadsDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove rig sidecar: %v", err)
		}
		_, err := resolveViaPrefixRoutingWithAccess(ctx, "gt-target2", true, false)
		assertUnrouted(t, err)
	})
}
