//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// Routed reads into a proxied-server rig whose sidecar names an external Dolt
// server (gastownhall/beads#5861), against the shard's shared dolt
// sql-server. newSharedProxiedProject gives each rig its own database and a
// scoped user that can see only it, so a read that reached B's bead through
// A's user or database would fail here. Unit cases that need no server are in
// foreign_proxied_external_read_test.go.

// routeExternalRig writes a routes.jsonl into from's .beads sending prefix
// to the rig directory to, relative to from (the town root routes resolve
// against is the directory holding the launcher's .beads).
func routeExternalRig(t *testing.T, from, to, prefix string) {
	t.Helper()
	rel, err := filepath.Rel(from, to)
	if err != nil {
		t.Fatalf("relative route %s -> %s: %v", from, to, err)
	}
	line := `{"prefix":"` + prefix + `-","path":"` + filepath.ToSlash(rel) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(from, ".beads", "routes.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
}

// externalOnlyCopy is a rig directory holding only rig's metadata.json and
// proxied-server sidecar, with dolt_database replaced when database is
// non-empty: a clone of rig's workspace that has never run a proxy, so a test
// can watch every file a foreign read could touch.
func externalOnlyCopy(t *testing.T, rig proxiedProject, database string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "rig")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := configfile.LoadForDiscovery(rig.beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load %s metadata: %v", rig.beadsDir, err)
	}
	if database != "" {
		cfg.DoltDatabase = database
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save copy metadata: %v", err)
	}
	info, err := configfile.LoadProxiedServerClientInfo(rig.beadsDir)
	if err != nil || info == nil || info.External == nil {
		t.Fatalf("rig %s has no external sidecar: %v (info=%+v)", rig.beadsDir, err, info)
	}
	if err := configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{External: info.External}); err != nil {
		t.Fatalf("save copy sidecar: %v", err)
	}
	return dir
}

// TestProxiedServerRoutedExternalTargetInProcess runs the routed read in this
// process, as a direct launcher does, so it can drive routing's globals
// (dbPath) and the launcher's environment. It is not parallel for that
// reason.
func TestProxiedServerRoutedExternalTargetInProcess(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	a := newSharedProxiedProject(t, bd, "rla")
	b := newSharedProxiedProject(t, bd, "rlb")
	target := bdProxiedCreate(t, bd, b.dir, "Routed external target")
	// The read goes to a copy of B's configuration that never ran a proxy, so
	// every file under it is one the read alone could have written.
	bCopy := externalOnlyCopy(t, b, "")

	// A's own connection settings, which must not reach B's server.
	isolateBeadsDirForTest(t)
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", a.database)
	t.Setenv("BEADS_DOLT_SERVER_USER", a.database)
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_PASSWORD", "launcher-secret")
	t.Setenv(configfile.ExternalDoltPasswordEnvVar, "")
	oldDBPath := dbPath
	dbPath = filepath.Join(a.beadsDir, "dolt")
	t.Cleanup(func() { dbPath = oldDBPath })
	ctx := context.Background()

	// AC-1: the routed read returns B's bead from B's database as B's user.
	t.Run("routed_read_returns_target_bead", func(t *testing.T) {
		routeExternalRig(t, a.dir, bCopy, "rlb")
		result, err := resolveViaPrefixRouting(ctx, target.ID)
		if err != nil {
			t.Fatalf("routed read of %s: %v", target.ID, err)
		}
		defer result.Close()
		if result.Issue == nil || result.Issue.ID != target.ID || result.Issue.Title != "Routed external target" {
			t.Fatalf("routed read returned %+v, want B's bead %s", result.Issue, target.ID)
		}
		if !result.Routed {
			t.Error("result not marked routed")
		}
	})

	// AC-2 (direct launcher) and AC-5 (absent bead): the routed gate lookup
	// reports the target's actual state, and an absent bead as unconfirmed.
	t.Run("gate_lookup_reports_target_state", func(t *testing.T) {
		routeExternalRig(t, a.dir, bCopy, "rlb")
		issue, err := prefixRoutedBeadGateTarget(ctx, target.ID)
		if err != nil || issue == nil || issue.Status != types.StatusOpen {
			t.Fatalf("gate lookup of open %s = %+v, %v; want it open", target.ID, issue, err)
		}
		gateTarget := bdProxiedCreate(t, bd, b.dir, "Closed external target")
		bdProxiedClose(t, bd, b.dir, gateTarget.ID)
		issue, err = prefixRoutedBeadGateTarget(ctx, gateTarget.ID)
		if err != nil || issue == nil || issue.Status != types.StatusClosed {
			t.Fatalf("gate lookup of closed %s = %+v, %v; want it closed", gateTarget.ID, issue, err)
		}
		issue, err = prefixRoutedBeadGateTarget(ctx, "rlb-nosuchbead")
		if issue != nil || !errors.Is(err, errBeadGateTargetUnconfirmed) || !strings.Contains(err.Error(), `no issue found matching "rlb-nosuchbead"`) {
			t.Fatalf("gate lookup of an absent bead = %+v, %v; want it unconfirmed", issue, err)
		}
	})
}

// TestProxiedServerGateRoutedExternalTarget drives bd gate check and bd close
// in proxied-server rig A on gates awaiting beads in external proxied-server
// rig B, reached through A's routes.jsonl.
func TestProxiedServerGateRoutedExternalTarget(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// AC-2: gate check leaves the gate pending on B's open bead, for that
	// reason, and resolves it once the bead closes.
	t.Run("gate_check_follows_target_state", func(t *testing.T) {
		t.Parallel()
		a := newSharedProxiedProject(t, bd, "rga")
		b := newSharedProxiedProject(t, bd, "rgb")
		routeExternalRig(t, a.dir, b.dir, "rgb")
		target := bdProxiedCreate(t, bd, b.dir, "Awaited external bead")
		gate := bdProxiedCreate(t, bd, a.dir, "Cross-rig bead gate", "--type", "gate")
		db := openProxiedDB(t, a)
		seedGateAwait(t, db, gate.ID, "bead", target.ID, time.Now().UTC(), 0)

		out, stderr, err := bdProxiedRunBuffers(t, bd, a.dir, "gate", "check", "--type", "bead")
		if err != nil {
			t.Fatalf("gate check (target open) failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got == types.StatusClosed {
			t.Fatal("gate resolved while its external target is open")
		}
		if combined := out + stderr; strings.Contains(combined, "cannot confirm") || strings.Contains(combined, "proxied-server route") {
			t.Fatalf("gate pending because its target could not be read, not because it is open:\n%s", combined)
		}

		bdProxiedClose(t, bd, b.dir, target.ID)
		out, stderr, err = bdProxiedRunBuffers(t, bd, a.dir, "gate", "check", "--type", "bead")
		if err != nil {
			t.Fatalf("gate check (target closed) failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got != types.StatusClosed {
			t.Fatalf("gate should resolve once its external target closes, got %q\nstdout:\n%s\nstderr:\n%s", got, out, stderr)
		}
		if !strings.Contains(out, "1 resolved") {
			t.Errorf("expected 1 resolved, got:\n%s", out)
		}
	})

	// AC-3: bd close's gate pre-check refuses while B's bead is open and
	// permits the close once it is closed.
	t.Run("close_precheck_follows_target_state", func(t *testing.T) {
		t.Parallel()
		a := newSharedProxiedProject(t, bd, "rca")
		b := newSharedProxiedProject(t, bd, "rcb")
		routeExternalRig(t, a.dir, b.dir, "rcb")
		target := bdProxiedCreate(t, bd, b.dir, "Awaited external bead")
		gate := bdProxiedCreate(t, bd, a.dir, "Cross-rig bead gate", "--type", "gate")
		db := openProxiedDB(t, a)
		seedGateAwait(t, db, gate.ID, "bead", target.ID, time.Now().UTC(), 0)

		out, stderr, err := bdProxiedRunBuffers(t, bd, a.dir, "close", gate.ID)
		if err == nil {
			t.Fatalf("bd close of a gate awaiting an open external bead succeeded:\n%s", out)
		}
		combined := out + stderr
		if !strings.Contains(combined, "gate condition not satisfied") || !strings.Contains(combined, target.ID+" is open") {
			t.Fatalf("bd close refused for another reason than the open target:\n%s", combined)
		}
		if got := readStatus(t, db, gate.ID); got == types.StatusClosed {
			t.Fatal("refused close closed the gate")
		}

		bdProxiedClose(t, bd, b.dir, target.ID)
		bdProxiedClose(t, bd, a.dir, gate.ID)
		if got := readStatus(t, db, gate.ID); got != types.StatusClosed {
			t.Errorf("gate should close once its external target is closed, got %q", got)
		}
	})
}
