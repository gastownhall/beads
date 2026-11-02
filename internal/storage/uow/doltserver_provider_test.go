package uow

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shutdownOnInterrupt(t *testing.T, rootDir string) {
	t.Helper()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			_ = proxy.Shutdown(rootDir)
			os.Exit(1)
		case <-done:
		}
	}()
	t.Cleanup(func() {
		signal.Stop(ch)
		close(done)
	})
}

func TestNewDoltServerUOWProvider_ValidationErrors(t *testing.T) {
	cases := []struct {
		name     string
		database string
		rootUser string
		doltBin  string
		backend  proxy.Backend
		want     string
	}{
		{"empty database", "", "root", "/usr/bin/true", proxy.BackendLocalServer, "database name must not be empty"},
		{"invalid backend", "beads", "root", "/usr/bin/true", proxy.Backend("nope"), "unknown backend"},
		{"empty rootUser", "beads", "", "/usr/bin/true", proxy.BackendLocalServer, "rootUser must not be empty"},
		{"empty doltBin", "beads", "root", "", proxy.BackendLocalServer, "doltBinExec must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewDoltServerUOWProvider(
				context.Background(),
				t.TempDir(),
				tc.database,
				"", "", tc.backend,
				tc.rootUser, "", tc.doltBin,
				0,
				0,
				false,
				"",
			)
			assert.Nil(t, p)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestNewDoltServerUOWProvider_HappyPath(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	provider, err := NewDoltServerUOWProvider(
		context.Background(),
		storeRootDir,
		"beads",
		logPath,
		cfgPath,
		proxy.BackendLocalServer,
		"root",
		"",
		bin,
		0,
		0,
		false,
		"",
	)

	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
}

// TestNewDoltServerUOWProvider_CarriesProxiedServerMode pins the link of the
// proxied-server marker that the data-behind gate tests cannot see: they build
// the provider with the field already set, so they stay green if the open
// stops copying the option into the providers it builds, the one the gate
// runs on included. Both polarities, because the open `bd serve` makes for a
// server-mode workspace passes no option and must keep the runnable-here
// remedy. The two opens share one server.
func TestNewDoltServerUOWProvider_CarriesProxiedServerMode(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	for _, tt := range []struct {
		name string
		opts []ProviderOption
		want bool
	}{
		{name: "server-mode open", want: false},
		{name: "proxied-server open", opts: []ProviderOption{WithProxiedServerMode()}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewDoltServerUOWProvider(
				context.Background(),
				storeRootDir,
				"beads",
				logPath,
				cfgPath,
				proxy.BackendLocalServer,
				"root",
				"",
				bin,
				0,
				0,
				false,
				"",
				tt.opts...,
			)
			require.NoError(t, err)
			require.NotNil(t, provider)
			t.Cleanup(func() { _ = provider.Close(context.Background()) })

			sqlProvider, ok := provider.(*doltSQLProvider)
			require.True(t, ok, "NewDoltServerUOWProvider returned %T, want *doltSQLProvider", provider)
			assert.Equal(t, tt.want, sqlProvider.proxiedServerMode, "provider.proxiedServerMode")
		})
	}
}

func TestNewDoltServerUOWProvider_ConcurrentInstantiation(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	const concurrency = 10
	type result struct {
		provider UnitOfWorkProvider
		err      error
	}
	results := make([]result, concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()
			p, err := NewDoltServerUOWProvider(
				context.Background(),
				storeRootDir,
				"beads",
				logPath,
				cfgPath,
				proxy.BackendLocalServer,
				"root",
				"",
				bin,
				0,
				0,
				false,
				"",
			)
			results[i] = result{provider: p, err: err}
		}()
	}
	wg.Wait()

	t.Cleanup(func() {
		for _, r := range results {
			if r.provider != nil {
				_ = r.provider.Close(context.Background())
			}
		}
	})

	for i, r := range results {
		assert.NoErrorf(t, r.err, "provider %d", i)
		assert.NotNilf(t, r.provider, "provider %d", i)
	}
}

var (
	bdBinaryOnce sync.Once
	bdBinary     string
	bdBinaryErr  error
)

func buildBDBinary(t *testing.T) string {
	t.Helper()
	bdBinaryOnce.Do(func() {
		prebuilt, err := bazeltest.PrebuiltBD()
		if err != nil {
			bdBinaryErr = err
			return
		}
		if prebuilt != "" {
			bdBinary = prebuilt
			return
		}
		tmpDir, err := os.MkdirTemp("", "bd-uow-test-*")
		if err != nil {
			bdBinaryErr = fmt.Errorf("temp dir: %w", err)
			return
		}
		name := "bd"
		if runtime.GOOS == "windows" {
			name = "bd.exe"
		}
		bdBinary = filepath.Join(tmpDir, name)
		cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", bdBinary, "github.com/steveyegge/beads/cmd/bd")
		if out, err := cmd.CombinedOutput(); err != nil {
			bdBinaryErr = fmt.Errorf("go build bd: %v\n%s", err, out)
		}
	})
	if bdBinaryErr != nil {
		t.Fatalf("build bd: %v", bdBinaryErr)
	}
	return bdBinary
}

func writeServerConfig(t *testing.T, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("log_level: debug\nlistener:\n  host: 127.0.0.1\n  port: %d\n", port)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// TestNewDoltServerUOWProvider_RecoversAfterProxyIdleExit reproduces #7295: a
// long-lived managed provider must still open units of work after its proxy
// retires on idle, instead of dialing the retired proxy's port forever.
//
// The row proves the rediscovered connection reaches the same database and
// data; the accept-counting listener on the retired proxy's port proves the
// provider never dialed the stale address (a stale dial would not just fail:
// this listener accepts TCP, so without rediscovery the handshake hangs on it
// until the dial deadline, and the listener would count the accept).
func TestNewDoltServerUOWProvider_RecoversAfterProxyIdleExit(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx := context.Background()
	provider, err := NewDoltServerUOWProvider(
		ctx, storeRootDir, "beads", logPath, cfgPath,
		proxy.BackendLocalServer, "root", "", bin,
		0,
		2*time.Second, // short proxy idle timeout
		false, "",
		WithProxiedServerMode(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close(ctx) })

	// Retain no idle connections, like bd serve after pool_idle_time: every
	// unit of work dials a new physical connection.
	sqlProvider, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "NewDoltServerUOWProvider returned %T, want *doltSQLProvider", provider)
	sqlProvider.SetPoolLimits(PoolLimits{MaxIdleConns: -1})

	tx, err := sqlProvider.BeginTx(ctx)
	require.NoError(t, err, "first unit of work")
	_, err = tx.Runner().ExecContext(ctx,
		"CREATE TABLE t_proxy_recovery (id INT PRIMARY KEY, v VARCHAR(64))")
	require.NoError(t, err, "create the recovery probe table")
	_, err = tx.Runner().ExecContext(ctx,
		"INSERT INTO t_proxy_recovery (id, v) VALUES (1, 'survivor')")
	require.NoError(t, err, "seed the recovery probe row")
	require.NoError(t, tx.Commit(ctx, "seed proxy recovery probe"))

	pf, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	oldPort := pf.Port
	oldAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(oldPort))

	// Wait for the proxy to retire on idle: its data port refuses connections.
	deadline := time.Now().Add(60 * time.Second)
	for {
		c, dialErr := net.DialTimeout("tcp", oldAddr, time.Second)
		if dialErr != nil {
			break
		}
		_ = c.Close()
		require.True(t, time.Now().Before(deadline), "proxy at %s never retired on idle", oldAddr)
		time.Sleep(250 * time.Millisecond)
	}

	// Own the retired port so a provider that dials the stale address is
	// caught in the act instead of failing with a bare refusal.
	stale, err := net.Listen("tcp", oldAddr)
	require.NoError(t, err, "retired proxy port %s was not released", oldAddr)
	var staleAccepts atomic.Int32
	go func() {
		for {
			c, err := stale.Accept()
			if err != nil {
				return
			}
			staleAccepts.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = stale.Close() })

	tx, err = sqlProvider.BeginTx(ctx)
	require.NoError(t, err, "unit of work after the proxy retired on idle must rediscover the proxy")
	var v string
	require.NoError(t, tx.Runner().QueryRowContext(ctx,
		"SELECT v FROM t_proxy_recovery WHERE id = 1").Scan(&v),
		"the rediscovered connection must read the row committed before retirement")
	require.Equal(t, "survivor", v)
	require.NoError(t, tx.Rollback(ctx))

	require.Zero(t, staleAccepts.Load(),
		"the provider dialed the retired proxy's port %d", oldPort)

	// The unit of work must have run through a rediscovered proxy, whose
	// published port is not the retired one this test holds.
	pf, err = pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotEqual(t, oldPort, pf.Port,
		"the provider must publish a replacement proxy endpoint, not the retired one")
}
