package dolt

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// stubIdleWatchRunning makes newServerMode see an idle watcher on the
// server, or none, without one running.
func stubIdleWatchRunning(t *testing.T, running bool) {
	t.Helper()
	orig := idleWatchRunning
	t.Cleanup(func() { idleWatchRunning = orig })
	idleWatchRunning = func(string) bool { return running }
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

// TestNewServerMode_ClientLeaseDoesNotDependOnAutoStart pins which opens hold
// a client lease from before their first dial. Permission to start a server
// does not decide it: an open that may not start one holds a lease on a
// server that has an idle watcher, or the watcher would stop that server
// under it. A strict read-only open writes no lease. Taking a lease prunes
// the lease of a process that died with its store open, watcher or not.
func TestNewServerMode_ClientLeaseDoesNotDependOnAutoStart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		set       func(*Config)
		watched   bool
		wantLease bool
	}{
		{name: "may auto-start, no watcher yet", set: func(*Config) {}, wantLease: true},
		{name: "auto-start off, watched server", set: func(c *Config) { c.AutoStart = false }, watched: true, wantLease: true},
		{name: "auto-start off, unwatched server", set: func(c *Config) { c.AutoStart = false }},
		{name: "strict read-only, watched server", set: func(c *Config) { c.ReadOnly, c.DisableAutoStart = true, true }, watched: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseAutoStartCfg(t, "test_client_lease_policy", 1, doltserver.PortSourcePortFile)
			cfg.BeadsDir = t.TempDir()
			tc.set(cfg)
			serverDir := doltserver.ResolveServerDir(cfg.BeadsDir)
			stubIdleWatchRunning(t, tc.watched)
			stubEnsureRunningDetailed(t, 0, false, errors.New("stub: auto-start refused"))

			// A lease left by a bd process that died with its store open.
			// The PID is above Linux's largest pid_max (1<<22).
			activity := filepath.Join(serverDir, "dolt-server.activity")
			if err := os.MkdirAll(activity, 0o700); err != nil {
				t.Fatal(err)
			}
			deadLease := filepath.Join(activity, fmt.Sprintf("client-%d-0a0b0c", 1<<22+4242))
			if err := os.WriteFile(deadLease, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			leasesAtDial := -1
			origDial := dialServer
			t.Cleanup(func() { dialServer = origDial })
			dialServer = func(string, string, time.Duration) (net.Conn, error) {
				if leasesAtDial < 0 {
					leasesAtDial = countClientLeases(t, serverDir)
				}
				return nil, errors.New("stub: nothing listening")
			}

			if _, err := newServerMode(context.Background(), cfg); err == nil {
				t.Fatal("open succeeded with nothing listening")
			}
			want := 0
			if tc.wantLease {
				want = 1
			}
			if leasesAtDial != want {
				t.Errorf("client leases at the first dial = %d, want %d", leasesAtDial, want)
			}
			if n := countClientLeases(t, serverDir); n != 0 {
				t.Errorf("failed open left %d client lease(s)", n)
			}
			_, statErr := os.Stat(deadLease)
			if pruned := os.IsNotExist(statErr); pruned != tc.wantLease {
				t.Errorf("dead process's lease pruned = %v, want %v (an open prunes when it takes a lease)", pruned, tc.wantLease)
			}
		})
	}
}

// TestOpen_OwnedServerHoldsClientLeaseUntilClose pins GH#4282's store side
// for successful opens of the workspace's server: an open that may
// auto-start it, or one that may not on a server with an idle watcher, holds
// one client lease, owned by this process, in the server directory's
// activity directory for the life of the store. A strict read-only open of a
// watched server writes no lease and holds a server connection instead, and
// one that may not start a server, on a server with no watcher, holds
// neither. Close gives back what the open took. The idle watcher therefore
// sees each store even while its pool holds no SQL connection.
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
	newCfg := func() *Config {
		return &Config{
			Path:             filepath.Join(beadsDir, "dolt"),
			BeadsDir:         beadsDir,
			CommitterName:    "client-lease",
			CommitterEmail:   "client-lease@example.com",
			Database:         "client_lease_probe",
			ServerHost:       "127.0.0.1",
			ServerPort:       port,
			ServerPortSource: doltserver.PortSourcePortFile,
			CreateIfMissing:  true,
		}
	}
	strictReadOnly := func(c *Config) { c.ReadOnly, c.DisableAutoStart, c.CreateIfMissing = true, true, false }
	serverDir := doltserver.ResolveServerDir(beadsDir)

	// In order: the first open creates the database the strict read-only
	// opens cannot.
	for _, tc := range []struct {
		name         string
		set          func(*Config)
		mayAutoStart bool
		watched      bool
		wantLease    int
		wantHold     bool
	}{
		{name: "may auto-start", set: func(c *Config) { c.AutoStart = true }, mayAutoStart: true, wantLease: 1},
		{name: "auto-start off, watched server", set: func(c *Config) { c.AutoStart = false }, watched: true, wantLease: 1},
		{name: "auto-start off, unwatched server", set: func(c *Config) { c.AutoStart = false }},
		{name: "strict read-only, watched server", set: strictReadOnly, watched: true, wantHold: true},
		{name: "strict read-only, unwatched server", set: strictReadOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubIdleWatchRunning(t, tc.watched)
			cfg := newCfg()
			tc.set(cfg)
			if got := serverOpenCanAutoStart(cfg); got != tc.mayAutoStart {
				t.Fatalf("premise: serverOpenCanAutoStart = %v, want %v", got, tc.mayAutoStart)
			}

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

			if n := countClientLeases(t, serverDir); n != tc.wantLease {
				t.Errorf("the open store holds %d client leases of this process, want %d", n, tc.wantLease)
			}
			if hold := store.releaseServerHold != nil; hold != tc.wantHold {
				t.Errorf("the open store holds a server connection = %v, want %v", hold, tc.wantHold)
			}

			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			closed = true
			if n := countClientLeases(t, serverDir); n != 0 {
				t.Errorf("Close left %d client lease(s) behind", n)
			}
			if store.releaseServerHold != nil {
				t.Error("Close kept the store's server connection")
			}
		})
	}
}
