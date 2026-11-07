package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestExternalDependencyPolicyProxiedExternalTarget pins the external-deps
// policy seam against #5861 item 2's read route: openExternalDependencyProject
// — the opener both plumbings share (wireExternalDependencyPolicy and
// wireExternalDependencyUOWProvider) — reads a proxied-server project whose
// sidecar names an external SQL server through that connection instead of
// refusing it as unrouted, because the policy's blocking-state reads are
// exactly the non-mutating foreign read the route serves. The endpoint here is
// deliberately unreachable, so a failed read that names it is the proof the
// attempt happened. A managed-local project (no external sidecar) keeps the
// typed refusal: nothing can route into it.
func TestExternalDependencyPolicyProxiedExternalTarget(t *testing.T) {
	ctx := context.Background()
	rigRoot := t.TempDir()
	rigBeadsDir := filepath.Join(rigRoot, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatalf("create rig .beads: %v", err)
	}
	writeRig := func(t *testing.T, sidecar *configfile.ProxiedServerClientInfo) {
		t.Helper()
		cfg := configfile.DefaultConfig()
		cfg.DoltMode = configfile.DoltModeProxiedServer
		cfg.DoltDatabase = "gt"
		if err := cfg.Save(rigBeadsDir); err != nil {
			t.Fatalf("save rig metadata.json: %v", err)
		}
		if err := configfile.SaveProxiedServerClientInfo(rigBeadsDir, sidecar); err != nil {
			t.Fatalf("save rig sidecar: %v", err)
		}
	}

	t.Run("externally served project is read, not refused", func(t *testing.T) {
		writeRig(t, &configfile.ProxiedServerClientInfo{
			External: &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 1},
		})
		store, err := openExternalDependencyProject(ctx, rigRoot)
		if store != nil {
			store.Close()
		}
		if err == nil {
			t.Fatal("an unreachable external endpoint must fail the read")
		}
		var capErr *ProxyCapabilityError
		if errors.As(err, &capErr) && capErr.Code == "proxy.store.unrouted" {
			t.Fatalf("the policy must attempt the external read instead of refusing it as unrouted: %v", err)
		}
		if !strings.Contains(err.Error(), "unreachable") {
			t.Errorf("error should name the attempted endpoint as unreachable, got: %v", err)
		}
	})

	t.Run("managed-local project keeps the refusal", func(t *testing.T) {
		writeRig(t, &configfile.ProxiedServerClientInfo{})
		_, err := openExternalDependencyProject(ctx, rigRoot)
		var capErr *ProxyCapabilityError
		if !errors.As(err, &capErr) || capErr.Code != "proxy.store.unrouted" {
			t.Fatalf("a managed-local proxied project keeps the typed refusal, got: %v", err)
		}
	})
}
