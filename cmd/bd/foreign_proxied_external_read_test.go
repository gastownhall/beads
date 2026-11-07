package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// writeProxiedTargetMetadata writes a proxied-server workspace's metadata and
// returns its .beads dir. database may be "" to test the no-dolt_database
// target.
func writeProxiedTargetMetadata(t *testing.T, beadsDir, database string) {
	t.Helper()
	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeProxiedServer
	cfg.DoltDatabase = database
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
}

// writeExternalSidecar writes a sidecar naming an external server at a
// loopback port nothing listens on (port 1): attempts must fail past the
// dispatch, at the endpoint, never before it.
func writeExternalSidecar(t *testing.T, beadsDir string) {
	t.Helper()
	if err := configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		External: &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 1},
	}); err != nil {
		t.Fatalf("save sidecar: %v", err)
	}
}

func asProxyUnrouted(err error) bool {
	var capErr *ProxyCapabilityError
	return errors.As(err, &capErr) && capErr.Code == "proxy.store.unrouted"
}

// TestForeignProxiedExternalReadDispatch pins the dispatch boundary of the
// #5861 external read: which foreign proxied targets open through it, which
// keep the typed unrouted refusal, and which are left to the factory
// dispatch entirely.
func TestForeignProxiedExternalReadDispatch(t *testing.T) {
	ctx := context.Background()

	t.Run("absent sidecar keeps the typed refusal", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeProxiedTargetMetadata(t, beadsDir, "gt")
		store, handled, err := openForeignProxiedExternalRead(ctx, beadsDir)
		if !handled || err == nil || store != nil {
			t.Fatalf("want handled typed refusal, got store=%v handled=%v err=%v", store, handled, err)
		}
		if !asProxyUnrouted(err) {
			t.Fatalf("absent sidecar must keep the typed proxy.store.unrouted refusal, got: %v", err)
		}
	})

	t.Run("managed-local sidecar keeps the typed refusal", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeProxiedTargetMetadata(t, beadsDir, "gt")
		if err := configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{}); err != nil {
			t.Fatalf("save sidecar: %v", err)
		}
		store, handled, err := openForeignProxiedExternalRead(ctx, beadsDir)
		if !handled || err == nil || store != nil {
			t.Fatalf("want handled typed refusal, got store=%v handled=%v err=%v", store, handled, err)
		}
		if !asProxyUnrouted(err) {
			t.Fatalf("managed-local sidecar must keep the typed proxy.store.unrouted refusal, got: %v", err)
		}
	})

	t.Run("malformed sidecar names its path", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeProxiedTargetMetadata(t, beadsDir, "gt")
		sidecar := configfile.ProxiedServerClientInfoPath(beadsDir)
		if err := os.WriteFile(sidecar, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write sidecar: %v", err)
		}
		store, handled, err := openForeignProxiedExternalRead(ctx, beadsDir)
		if !handled || err == nil || store != nil {
			t.Fatalf("want handled error, got store=%v handled=%v err=%v", store, handled, err)
		}
		if asProxyUnrouted(err) {
			t.Fatalf("an unreadable sidecar is unconfirmable, not the typed unrouted refusal: %v", err)
		}
		if !strings.Contains(err.Error(), sidecar) {
			t.Fatalf("error must name the sidecar path %s, got: %v", sidecar, err)
		}
	})

	t.Run("invalid external config is refused before dialing", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeProxiedTargetMetadata(t, beadsDir, "gt")
		if err := configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
			External: &configfile.ExternalDoltConfig{Host: "127.0.0.1"}, // Host without Port
		}); err != nil {
			t.Fatalf("save sidecar: %v", err)
		}
		store, handled, err := openForeignProxiedExternalRead(ctx, beadsDir)
		if !handled || err == nil || store != nil {
			t.Fatalf("want handled error, got store=%v handled=%v err=%v", store, handled, err)
		}
		if asProxyUnrouted(err) {
			t.Fatalf("an invalid external config is a configuration error, not the typed unrouted refusal: %v", err)
		}
	})

	t.Run("target without dolt_database is refused", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeProxiedTargetMetadata(t, beadsDir, "")
		writeExternalSidecar(t, beadsDir)
		store, handled, err := openForeignProxiedExternalRead(ctx, beadsDir)
		if !handled || err == nil || store != nil {
			t.Fatalf("want handled error, got store=%v handled=%v err=%v", store, handled, err)
		}
		if asProxyUnrouted(err) {
			t.Fatalf("a target naming no database is a configuration error, not the typed unrouted refusal: %v", err)
		}
		if !strings.Contains(err.Error(), "dolt_database") {
			t.Fatalf("error must name the missing dolt_database, got: %v", err)
		}
	})

	t.Run("external target attempts the endpoint, not the refusal", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeProxiedTargetMetadata(t, beadsDir, "gt")
		writeExternalSidecar(t, beadsDir)
		store, handled, err := openForeignProxiedExternalRead(ctx, beadsDir)
		if !handled || err == nil || store != nil {
			t.Fatalf("want handled dial error, got store=%v handled=%v err=%v", store, handled, err)
		}
		if asProxyUnrouted(err) {
			t.Fatalf("an external target must be attempted, not refused as unrouted: %v", err)
		}
		if !strings.Contains(err.Error(), "unreachable") {
			t.Fatalf("error should report the unreachable endpoint, got: %v", err)
		}
	})

	t.Run("non-proxied workspace is left to the factory", func(t *testing.T) {
		beadsDir := t.TempDir()
		cfg := configfile.DefaultConfig()
		cfg.DoltMode = configfile.DoltModeServer
		cfg.DoltDatabase = "gt"
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("save metadata.json: %v", err)
		}
		store, handled, err := openForeignProxiedExternalRead(ctx, beadsDir)
		if handled || err != nil || store != nil {
			t.Fatalf("want unhandled, got store=%v handled=%v err=%v", store, handled, err)
		}
	})

	t.Run("no metadata at all is left to the factory", func(t *testing.T) {
		beadsDir := t.TempDir()
		store, handled, err := openForeignProxiedExternalRead(ctx, beadsDir)
		if handled || err != nil || store != nil {
			t.Fatalf("want unhandled, got store=%v handled=%v err=%v", store, handled, err)
		}
	})

	t.Run("corrupt metadata fails the read without falling back", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(configfile.MetadataFilePath(beadsDir), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write metadata.json: %v", err)
		}
		// The load refusal is generic — the factory's open fails with the
		// same text — so it is asserted on the foreign read itself, not on
		// the helper's handled flag: whatever dispatch runs, the read must
		// fail naming the metadata and never fall back to the embedded store.
		_, err := openForeignStore(ctx, beadsDir, foreignRead)
		if err == nil || !strings.Contains(err.Error(), "refusing to fall back") {
			t.Fatalf("corrupt metadata must refuse to fall back to the embedded store, got: %v", err)
		}
	})

	t.Run("preview and write keep the generic refusal", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeProxiedTargetMetadata(t, beadsDir, "gt")
		writeExternalSidecar(t, beadsDir)
		for _, tt := range []struct {
			name   string
			access foreignAccess
		}{
			{"preview", foreignPreview},
			{"write", foreignWrite},
			{"create", foreignCreate},
		} {
			t.Run(tt.name, func(t *testing.T) {
				_, err := openForeignStore(ctx, beadsDir, tt.access)
				if err == nil || !asProxyUnrouted(err) {
					t.Fatalf("%s of an external proxied target must keep the typed unrouted refusal, got: %v", tt.name, err)
				}
			})
		}
	})
}
