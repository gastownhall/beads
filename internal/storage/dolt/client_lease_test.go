package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
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
