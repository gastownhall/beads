package dolt

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestOpen_OwnedServerHoldsClientLeaseUntilClose pins GH#4282's store side:
// a server-mode open against a local bd-managed server takes an idle-watch
// lease in the workspace's activity directory for the life of the store, and
// Close releases it — so an idle watcher sees the store even when its pool
// holds no open SQL connection at the moment.
func TestOpen_OwnedServerHoldsClientLeaseUntilClose(t *testing.T) {
	portStr := testutil.StartIsolatedDoltContainer(t)
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse container port %q: %v", portStr, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	beadsDir := t.TempDir()
	cfg := &Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		CommitterName:   "idle-lease",
		CommitterEmail:  "idle-lease@example.com",
		Database:        "idle_lease_probe",
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		CreateIfMissing: true,
	}
	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New against the local server: %v", err)
	}

	leases := func() []string {
		t.Helper()
		// The literal dolt-server.activity (matching
		// doltserver.ActivityDirName) keeps this test compiling against a
		// bd that predates the idle watch, so it fails there behaviorally
		// rather than at compile time: read it from disk, not from the
		// store, so the test sees what the watcher sees.
		entries, err := os.ReadDir(filepath.Join(doltserver.ResolveServerDir(beadsDir), "dolt-server.activity"))
		if err != nil {
			return nil
		}
		var found []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "client-") {
				found = append(found, entry.Name())
			}
		}
		return found
	}

	// The open holds exactly one lease, owned by this process.
	held := leases()
	if len(held) != 1 {
		t.Fatalf("an open server-mode store holds %d client leases, want 1", len(held))
	}
	pidText, _, _ := strings.Cut(strings.TrimPrefix(held[0], "client-"), "-")
	if pid, err := strconv.Atoi(pidText); err != nil || pid != os.Getpid() {
		t.Errorf("lease %q names PID %s (err %v), want this process's %d", held[0], pidText, err, os.Getpid())
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if left := leases(); len(left) != 0 {
		t.Errorf("Close left the lease behind: %v", left)
	}
}
