//go:build cgo

package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestResolveDoltServerConnectionPropagatesPortSource guards the primary CLI
// store-open path (and, through the same function, the unit-of-work provider
// `bd serve` builds).
//
// resolveDoltServerConnection hand-builds a dolt.Config that is handed straight
// to dolt.New → applyConfigDefaults, which infers "the caller explicitly
// asserted this port" from "ServerPort nonzero, ServerPortSource unset". While
// this site copied doltserver.DefaultConfig(dir).Port alone, every port bd
// resolved for itself arrived here labeled authoritative — so the CLI path and
// the library open path (dolt.NewFromConfig) disagreed on precedence: the
// legacy BEADS_DOLT_PORT override was honored by one and silently dropped by
// the other.
func TestResolveDoltServerConnectionPropagatesPortSource(t *testing.T) {
	t.Run("port-file port is not a caller assertion (be-9tju)", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
		t.Setenv("BEADS_DOLT_SERVER_MODE", "")
		t.Setenv("HOME", t.TempDir())
		beadsDir := t.TempDir()
		if err := doltserver.EnsurePortFile(beadsDir, 14567); err != nil {
			t.Fatalf("EnsurePortFile: %v", err)
		}

		doltCfg := &dolt.Config{BeadsDir: beadsDir}
		if err := resolveDoltServerConnection(context.Background(), beadsDir, &configfile.Config{Backend: configfile.BackendDolt}, doltCfg); err != nil {
			t.Fatalf("resolveDoltServerConnection: %v", err)
		}

		if doltCfg.ServerPort != 14567 {
			t.Fatalf("ServerPort = %d, want 14567", doltCfg.ServerPort)
		}
		if doltCfg.ServerPortSource != doltserver.PortSourcePortFile {
			t.Fatalf("ServerPortSource = %q, want %q: the source must travel with the port, or applyConfigDefaults promotes bd's own port-file bookkeeping to caller_explicit", doltCfg.ServerPortSource, doltserver.PortSourcePortFile)
		}
		if doltCfg.ServerPortSource.IsAuthoritative() {
			t.Fatal("port-file port reported authoritative on the CLI path: BEADS_DOLT_PORT becomes a silent no-op here while still working on the library open path, and a stale port file turns auto-start's benign retarget into a hard error (GH#4052)")
		}
	})

	t.Run("shared-server default is not a caller assertion (GH#4052)", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		t.Setenv("BEADS_DOLT_PORT", "")
		t.Setenv("BEADS_DOLT_SERVER_MODE", "")
		t.Setenv("HOME", t.TempDir())
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		beadsDir := t.TempDir()

		doltCfg := &dolt.Config{BeadsDir: beadsDir}
		if err := resolveDoltServerConnection(context.Background(), beadsDir, &configfile.Config{Backend: configfile.BackendDolt}, doltCfg); err != nil {
			t.Fatalf("resolveDoltServerConnection: %v", err)
		}

		if doltCfg.ServerPort != doltserver.DefaultSharedServerPort {
			t.Fatalf("ServerPort = %d, want %d", doltCfg.ServerPort, doltserver.DefaultSharedServerPort)
		}
		if doltCfg.ServerPortSource.IsAuthoritative() {
			t.Fatalf("ServerPortSource = %q reported authoritative: bd chose the shared default 3308 itself, so it must not be defended as a user-configured port", doltCfg.ServerPortSource)
		}
	})
}
