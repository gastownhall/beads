package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// countClientLeases counts this process's client leases on serverDir.
func countClientLeases(t *testing.T, serverDir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(serverDir, "dolt-server.activity"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), fmt.Sprintf("client-%d-", os.Getpid())) {
			n++
		}
	}
	return n
}

// TestNewServerMode_HoldsClientLeaseFromBeforeAutoStart: an open that may
// auto-start holds a client lease on the server from before its auto-start,
// so the server's idle watcher cannot stop it under the open, and a failed
// open gives the lease back.
func TestNewServerMode_HoldsClientLeaseFromBeforeAutoStart(t *testing.T) {
	cfg := baseAutoStartCfg(t, "test_client_lease", 1, doltserver.PortSourcePortFile)
	cfg.BeadsDir = t.TempDir()
	if !serverOpenCanAutoStart(cfg) {
		t.Fatal("premise: this open must be allowed to auto-start")
	}
	serverDir := doltserver.ResolveServerDir(cfg.BeadsDir)

	leasesDuringStart := -1
	orig := ensureRunningDetailed
	t.Cleanup(func() { ensureRunningDetailed = orig })
	ensureRunningDetailed = func(string) (int, bool, error) {
		leasesDuringStart = countClientLeases(t, serverDir)
		return 0, false, errors.New("stub: auto-start refused")
	}

	if _, err := newServerMode(context.Background(), cfg); err == nil {
		t.Fatal("open succeeded with nothing listening")
	}
	if leasesDuringStart != 1 {
		t.Errorf("client leases during auto-start = %d, want 1", leasesDuringStart)
	}
	if n := countClientLeases(t, serverDir); n != 0 {
		t.Errorf("failed open left %d client lease(s)", n)
	}
}

// TestOpen_OwnedServerHoldsClientLeaseUntilClose pins GH#4282's store side
// for a successful open: a server-mode open that may auto-start the
// workspace's server holds one client lease, owned by this process, in the
// server directory's activity directory for the life of the store, and
// Close removes it. The idle watcher therefore sees the store even while
// its pool holds no SQL connection.
func TestOpen_OwnedServerHoldsClientLeaseUntilClose(t *testing.T) {
	portStr := testutil.StartIsolatedDoltContainer(t)
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse container port %q: %v", portStr, err)
	}
	// The server is reachable, so the open never auto-starts; a stray
	// auto-start must not launch a real server either.
	stubEnsureRunningDetailed(t, 0, false, errors.New("stub: no auto-start in this test"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	beadsDir := t.TempDir()
	cfg := &Config{
		Path:             filepath.Join(beadsDir, "dolt"),
		BeadsDir:         beadsDir,
		CommitterName:    "client-lease",
		CommitterEmail:   "client-lease@example.com",
		Database:         "client_lease_probe",
		ServerHost:       "127.0.0.1",
		ServerPort:       port,
		ServerPortSource: doltserver.PortSourcePortFile,
		AutoStart:        true,
		CreateIfMissing:  true,
	}
	if !serverOpenCanAutoStart(cfg) {
		t.Fatal("premise: this open must be allowed to auto-start")
	}
	serverDir := doltserver.ResolveServerDir(beadsDir)

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New against the local server: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = store.Close()
		}
	})

	if n := countClientLeases(t, serverDir); n != 1 {
		t.Fatalf("an open server-mode store holds %d client leases of this process, want 1", n)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	if n := countClientLeases(t, serverDir); n != 0 {
		t.Errorf("Close left %d client lease(s) behind", n)
	}
}
