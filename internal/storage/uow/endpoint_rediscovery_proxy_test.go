package uow

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/steveyegge/beads/internal/testutil"
)

// rediscoveryFixture is a real workspace proxy (managed or in front of an
// external upstream) whose discovery is the production
// workspaceProxyDiscovery, counted so a test can tell which connections
// rediscovered.
type rediscoveryFixture struct {
	rootDir string
	opts    proxy.OpenOpts
	backend string // the external upstream's address; "" when managed
	real    func() (proxy.Endpoint, error)
	calls   atomic.Int64
}

func (f *rediscoveryFixture) discover() (proxy.Endpoint, error) {
	f.calls.Add(1)
	return f.real()
}

func newRediscoveryFixture(t *testing.T, opts proxy.OpenOpts, backend string) *rediscoveryFixture {
	t.Helper()
	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })
	t.Setenv("HOME", t.TempDir())

	rootDir := t.TempDir()
	shutdownOnInterrupt(t, rootDir)
	verifiedShutdownCleanup(t, rootDir)
	opts.LogFilePath = filepath.Join(t.TempDir(), "server.log")
	opts.IdleTimeout = defaultProxyIdleTimeout
	return &rediscoveryFixture{
		rootDir: rootDir,
		opts:    opts,
		backend: backend,
		real:    workspaceProxyDiscovery(rootDir, opts),
	}
}

func newManagedRediscoveryFixture(t *testing.T) *rediscoveryFixture {
	t.Helper()
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)
	absBin, err := filepath.Abs(bin)
	require.NoError(t, err)
	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	return newRediscoveryFixture(t, proxy.OpenOpts{
		Backend:        proxy.BackendLocalServer,
		ConfigFilePath: writeServerConfig(t, port),
		DoltBinPath:    absBin,
		Database:       "beads",
	}, "")
}

func newExternalRediscoveryFixture(t *testing.T) *rediscoveryFixture {
	t.Helper()
	upstreamPort, err := strconv.Atoi(testutil.StartIsolatedDoltContainer(t))
	require.NoError(t, err)
	external := configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: upstreamPort}
	return newRediscoveryFixture(t, proxy.OpenOpts{
		Backend:  proxy.BackendExternal,
		External: external,
	}, externalBackendAddress(external))
}

// open runs the provider open both constructors share, with a resolver over
// this fixture's counted discovery.
func (f *rediscoveryFixture) open(ctx context.Context, t *testing.T, ep proxy.Endpoint, database string) *doltSQLProvider {
	t.Helper()
	provider, err := openAndInitSchema(ctx, proxy.DataNetwork, ep, f.backend, database, "root", "", "", false, "",
		providerOptions{}, newProxyEndpointResolver(f.discover))
	require.NoError(t, err)
	p, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "openAndInitSchema returned %T, want *doltSQLProvider", provider)
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

var rediscoveryFixtures = []struct {
	name    string
	fixture func(*testing.T) *rediscoveryFixture
}{
	{"managed", newManagedRediscoveryFixture},
	{"external", newExternalRediscoveryFixture},
}

// TestProviderBootstrapKeepsFixedEndpoint pins that bootstrap never
// rediscovers: the fresh-database open (probe, no-database init, final pool)
// and the steady-state existing-database open run entirely on the endpoint the
// constructor resolved, and the steady-state open still hands out its one
// proven session. Only afterwards does a new physical connection rediscover; a
// reused pooled session does not.
func TestProviderBootstrapKeepsFixedEndpoint(t *testing.T) {
	for _, tc := range rediscoveryFixtures {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			ep, err := f.discover()
			require.NoError(t, err)

			for _, open := range []string{"fresh database", "existing database"} {
				f.calls.Store(0)
				p := f.open(ctx, t, ep, "bootstrap_fixed")
				assert.Zero(t, f.calls.Load(), "%s: bootstrap rediscovered", open)
				if open == "existing database" {
					assert.Equal(t, 1, p.db.Stats().OpenConnections,
						"%s: the single proven bootstrap session must be the pool handed out", open)
				}

				first, err := p.db.Conn(ctx)
				require.NoError(t, err)
				require.NoError(t, first.Close())
				held, err := p.db.Conn(ctx)
				require.NoError(t, err)
				assert.Zero(t, f.calls.Load(), "%s: a reused pooled session rediscovered", open)

				second, err := p.db.Conn(ctx)
				require.NoError(t, err)
				assert.Equal(t, int64(1), f.calls.Load(), "%s: a new physical connection must rediscover once", open)
				require.NoError(t, second.Close())
				require.NoError(t, held.Close())
			}
		})
	}
}

// requireSpawnLockHeld asserts that another actor holds the workspace's proxy
// spawn lock, which a live proxy keeps for its whole lifetime.
func requireSpawnLockHeld(t *testing.T, rootDir string) {
	t.Helper()
	lock, err := util.TryLock(filepath.Join(rootDir, proxy.LockFileName))
	if err == nil {
		lock.Unlock()
		t.Fatalf("%s is free; the live proxy should hold it", proxy.LockFileName)
	}
	require.True(t, lockfile.IsLocked(err), "probe %s: %v", proxy.LockFileName, err)
}

// TestProxyEndpointResolverAdoptsWithoutSpawnLock pins that rediscovering a
// healthy proxy adopts it without waiting on the spawn lock: while the lock is
// held (by the live proxy itself), a new physical connection rediscovers once
// and connects to that same proxy well inside discovery's 15-second open
// deadline, and the lock is still held afterwards.
func TestProxyEndpointResolverAdoptsWithoutSpawnLock(t *testing.T) {
	for _, tc := range rediscoveryFixtures {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			ep, err := f.discover()
			require.NoError(t, err)
			p := f.open(ctx, t, ep, "adopt_locked")
			live, err := pidfile.Read(f.rootDir, proxy.PIDFileName)
			require.NoError(t, err)
			require.NotNil(t, live)

			// Pin a session so the next request needs a new physical connection.
			held, err := p.db.Conn(ctx)
			require.NoError(t, err)
			defer func() { _ = held.Close() }()
			requireSpawnLockHeld(t, f.rootDir)
			f.calls.Store(0)

			connCtx, connCancel := context.WithTimeout(ctx, 5*time.Second)
			defer connCancel()
			conn, err := p.db.Conn(connCtx)
			require.NoError(t, err, "a new connection must adopt the live proxy without waiting on its spawn lock")
			defer func() { _ = conn.Close() }()
			var database string
			require.NoError(t, conn.QueryRowContext(connCtx, "SELECT DATABASE()").Scan(&database))
			assert.Equal(t, "adopt_locked", database)
			assert.Equal(t, int64(1), f.calls.Load(), "the new connection must rediscover")

			requireSpawnLockHeld(t, f.rootDir)
			after, err := pidfile.Read(f.rootDir, proxy.PIDFileName)
			require.NoError(t, err)
			require.NotNil(t, after)
			assert.Equal(t, live.Pid, after.Pid, "discovery must adopt the live proxy, not replace it")
			assert.Equal(t, live.Port, after.Port)
		})
	}
}
