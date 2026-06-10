//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestProxiedServerExternalStoreCommands locks in the routed-store wiring that
// makes the legacy store-based commands (list/ready/update/close/stats) work in
// proxied-server mode. Before that wiring these commands dereferenced a nil
// store and panicked; only `bd create` (uow-based) worked. Gated on
// BEADS_TEST_PROXIED_SERVER=1 + a dolt binary/container.
func TestProxiedServerExternalStoreCommands(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	port := testutil.StartIsolatedDoltContainer(t)

	p := bdProxiedInit(t, bd, "pse",
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", port,
	)

	// Sanity: metadata records proxied-server external mode.
	info, err := configfile.LoadProxiedServerClientInfo(p.beadsDir)
	if err != nil || info == nil || info.External == nil {
		t.Fatalf("expected external client info, err=%v info=%+v", err, info)
	}

	a := bdProxiedCreate(t, bd, p.dir, "first")
	b := bdProxiedCreate(t, bd, p.dir, "second")

	// list — exercises the routed store's GetReadyWork/GetStatistics path.
	out, err := bdProxiedRun(t, bd, p.dir, "list")
	if err != nil {
		t.Fatalf("bd list failed (routed store): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), a.ID) || !strings.Contains(string(out), b.ID) {
		t.Fatalf("list missing created issues:\n%s", out)
	}

	// ready — both open issues with no blockers should be ready.
	if out, err := bdProxiedRun(t, bd, p.dir, "ready"); err != nil {
		t.Fatalf("bd ready failed (routed store): %v\n%s", err, out)
	}

	// update + close — write path through the routed store (DOLT_COMMIT via proxy).
	if out, err := bdProxiedRun(t, bd, p.dir, "update", a.ID, "--status", "in_progress"); err != nil {
		t.Fatalf("bd update failed: %v\n%s", err, out)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "close", b.ID, "--reason", "done"); err != nil {
		t.Fatalf("bd close failed: %v\n%s", err, out)
	}

	// Confirm the writes are visible (b closed, a in progress) via a fresh
	// invocation — proves the routed store reads committed state back.
	got := bdProxiedShow(t, bd, p.dir, b.ID)
	if got.Status != "closed" {
		t.Fatalf("expected %s closed, got %q", b.ID, got.Status)
	}
}
