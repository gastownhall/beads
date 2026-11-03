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
	"sync"
	"syscall"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/procid"
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

// openManagedProvider opens a provider on a fresh managed root, with the
// proxy's idle timeout idle, and returns it with its root.
func openManagedProvider(ctx context.Context, t *testing.T, database string, idle time.Duration) (*doltSQLProvider, string) {
	t.Helper()
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)
	bdBin := buildBDBinary(t)
	previous := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = previous })
	t.Setenv("HOME", t.TempDir())
	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)
	provider, err := NewDoltServerUOWProvider(ctx, storeRootDir, database,
		filepath.Join(storeRootDir, "server.log"), writeServerConfig(t, port),
		proxy.BackendLocalServer, "root", "", bin, 0, idle, false, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	return provider.(*doltSQLProvider), storeRootDir
}

// readProbe reads the row seedProbe wrote, on a transaction's pinned session
// — the path every bd serve request takes (BeginTx). DATABASE() and the value
// tell a reconnect to the real database from one to a fresh empty one.
func readProbe(ctx context.Context, p *doltSQLProvider, database string) error {
	tx, err := p.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var gotDatabase, value string
	err = tx.(*doltServerTx).conn.QueryRowContext(ctx, "SELECT DATABASE(), value FROM reconnect_probe WHERE id = 1").Scan(&gotDatabase, &value)
	if err == nil && (gotDatabase != database || value != "persisted before idle") {
		err = fmt.Errorf("reconnected to database %q with value %q", gotDatabase, value)
	}
	return err
}

func seedProbe(ctx context.Context, t *testing.T, p *doltSQLProvider) {
	t.Helper()
	_, err := p.db.ExecContext(ctx, "CREATE TABLE reconnect_probe (id INT PRIMARY KEY, value VARCHAR(40))")
	require.NoError(t, err)
	write, err := p.BeginTx(ctx)
	require.NoError(t, err)
	if _, err := write.Runner().ExecContext(ctx, "INSERT INTO reconnect_probe VALUES (1, 'persisted before idle')"); err != nil {
		_ = write.Rollback(ctx)
		t.Fatal(err)
	}
	require.NoError(t, write.Commit(ctx, ""))
}

// waitProxyExit waits for the proxy recorded in old to leave the process
// table.
func waitProxyExit(t *testing.T, storeRootDir string, old *pidfile.PidFile, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		alive, err := procid.Verify(old.Pid, procid.Token(old.Birth))
		require.NoError(t, err)
		if !alive {
			return
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(storeRootDir, proxy.LogFileName))
			t.Fatalf("proxy did not exit: %s", log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestNewDoltServerUOWProvider_RediscoversProxyAfterIdleExit is GH#7295 at the
// provider: once the pool has let its last connection go, a proxy with a
// finite idle timeout exits, and the provider must find the workspace's proxy
// again rather than dial the dead port until it is rebuilt. Adapted from the
// fixture in #7998 by Vishnu Jayavel.
//
// Recovery is bounded, not instant: a dial gives up at its own budget (the
// DSN's 5s timeout) while discovery carries on, so a read may fail with
// ErrEndpointUnavailable before one succeeds. What the test pins is that reads
// recover within 60s of the first one after the exit, that every failed read
// returns within the dial budget, and that the provider keeps its own idle
// policy, database and bootstrap authority.
func TestNewDoltServerUOWProvider_RediscoversProxyAfterIdleExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const database = "managed_reconnect"
	p, storeRootDir := openManagedProvider(ctx, t, database, 2*time.Second)
	// Only this fixture drops idle sessions at once; the proxy's own finite
	// idle timeout is what retires it.
	p.db.SetMaxIdleConns(0)
	p.db.SetMaxOpenConns(4)
	seedProbe(ctx, t, p)
	require.NoError(t, readProbe(ctx, p, database))

	old, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, old)
	bootstrapEndpoint := p.serverEndpoint
	waitProxyExit(t, storeRootDir, old, 30*time.Second)

	// A dial that stayed on the old port would reach this unrelated listener,
	// which never answers the BDPROXY1 hello: the dial must not hand it to
	// the driver, and must give up within its budget.
	unrelated, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", old.Port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = unrelated.Close() })

	const dialBudget = 5 * time.Second // util.DoltServerDSN's dial timeout
	firstRead := time.Now()
	var lastErr error
	for {
		start := time.Now()
		request, done := context.WithTimeout(ctx, 20*time.Second)
		lastErr = readProbe(request, p, database)
		done()
		if lastErr == nil {
			break
		}
		took := time.Since(start)
		t.Logf("read at +%v failed after %v: %v", start.Sub(firstRead).Round(time.Millisecond), took.Round(time.Millisecond), lastErr)
		require.Truef(t, proxy.IsEndpointUnavailable(lastErr), "read failed with %v, want an endpoint-unavailable failure while recovering", lastErr)
		require.LessOrEqualf(t, took, dialBudget+2*time.Second, "a failed read took %v, beyond the %v dial budget: %v", took, dialBudget, lastErr)
		if time.Since(firstRead) > 60*time.Second {
			t.Fatalf("reads did not recover within 60s of the first one after the proxy exit: %v", lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("recovered %v after the first read following the exit", time.Since(firstRead))

	// Once recovered, concurrent readers share the new endpoint.
	const clients = 4
	errorsSeen := make(chan error, clients)
	for range clients {
		go func() {
			request, done := context.WithTimeout(ctx, 20*time.Second)
			defer done()
			errorsSeen <- readProbe(request, p, database)
		}()
	}
	for range clients {
		require.NoError(t, <-errorsSeen)
	}

	fresh, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	assert.True(t, old.Pid != fresh.Pid || old.Birth != fresh.Birth, "proxy process identity must change")
	assert.NotEqual(t, old.Port, fresh.Port, "the old port is taken by the unrelated listener")
	assert.Equal(t, fresh.Port, p.resolver.Endpoint().Port)
	assert.Equal(t, bootstrapEndpoint, p.serverEndpoint, "bootstrap authority stays bound to its original endpoint")

	// The replacement runs under the workspace's own finite idle policy: it
	// exits again once the pool is idle.
	waitProxyExit(t, storeRootDir, fresh, 30*time.Second)
}
