package uow

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

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

// This fixture must observe the old process exiting without rediscovering or
// starting another proxy. The pool drops idle sessions only for this fixture;
// production pool limits and finite proxy retirement remain unchanged.
func TestManagedProviderRecoversAfterProxyIdleExit(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const database = "managed_reconnect"
	provider, err := NewDoltServerUOWProvider(ctx, storeRootDir, database,
		filepath.Join(storeRootDir, "server.log"), writeServerConfig(t, port),
		proxy.BackendLocalServer, "root", "", bin, 0, 2*time.Second, false, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	p := provider.(*doltSQLProvider)
	p.db.SetMaxIdleConns(0)
	p.db.SetMaxOpenConns(4)
	_, err = p.db.ExecContext(ctx, "CREATE TABLE reconnect_probe (id INT PRIMARY KEY, value VARCHAR(40))")
	require.NoError(t, err)
	write, err := p.BeginTx(ctx)
	require.NoError(t, err)
	_, err = write.Runner().ExecContext(ctx, "INSERT INTO reconnect_probe VALUES (1, 'persisted before idle')")
	if err != nil {
		_ = write.Rollback(ctx)
		t.Fatal(err)
	}
	require.NoError(t, write.Commit(ctx, ""))
	old, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, old)
	bootstrapEndpoint := p.serverEndpoint
	deadline := time.Now().Add(30 * time.Second)
	for {
		alive, err := procid.Verify(old.Pid, procid.Token(old.Birth))
		require.NoError(t, err)
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(storeRootDir, proxy.LogFileName))
			t.Fatalf("proxy did not retire on idle: %s", log)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A static-DSN regression would hit this unrelated listener. Do not accept
	// its connections: a wrong dial must fail within the request deadline.
	unrelated, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", old.Port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = unrelated.Close() })
	const clients = 4
	errorsSeen := make(chan error, clients)
	for range clients {
		go func() {
			request, done := context.WithTimeout(ctx, 20*time.Second)
			defer done()
			tx, err := p.BeginTx(request)
			if err != nil {
				errorsSeen <- err
				return
			}
			defer func() { _ = tx.Rollback(request) }()
			// Read on this transaction's pinned session. The persisted value
			// and DATABASE() distinguish a fresh empty database.
			conn := tx.(*doltServerTx).conn
			var gotDatabase, value string
			err = conn.QueryRowContext(request, "SELECT DATABASE(), value FROM reconnect_probe WHERE id = 1").Scan(&gotDatabase, &value)
			if err == nil && (gotDatabase != database || value != "persisted before idle") {
				err = fmt.Errorf("reconnected to database %q with value %q", gotDatabase, value)
			}
			errorsSeen <- err
		}()
	}
	for range clients {
		require.NoError(t, <-errorsSeen)
	}
	fresh, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	assert.NotEqual(t, old.Port, fresh.Port)
	assert.True(t, old.Pid != fresh.Pid || old.Birth != fresh.Birth, "proxy process identity must change")
	assert.Equal(t, bootstrapEndpoint, p.serverEndpoint, "bootstrap authority stays bound to its original endpoint")
}

func TestManagedProxyBeforeConnectCancellation(t *testing.T) {
	var initialized atomic.Bool
	initialized.Store(true)
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		before := managedProxyBeforeConnect(&initialized, func(context.Context) (proxy.Endpoint, error) {
			called = true
			return proxy.Endpoint{}, errors.New("unexpected discovery")
		})
		err := before(ctx, mysql.NewConfig())
		require.ErrorIs(t, err, context.Canceled)
		assert.False(t, called)
	})
	t.Run("during discovery", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered := make(chan struct{})
		before := managedProxyBeforeConnect(&initialized, func(ctx context.Context) (proxy.Endpoint, error) {
			close(entered)
			<-ctx.Done()
			return proxy.Endpoint{}, ctx.Err()
		})
		done := make(chan error, 1)
		go func() { done <- before(ctx, mysql.NewConfig()) }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("discovery did not start")
		}
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("discovery did not receive caller cancellation")
		}
	})
}

// Exercise the real driver's ordering, not a direct callback call: a startup
// longer than cfg.Timeout must finish before the driver starts its dial budget.
func TestManagedProxyStartupHasSeparateDialBudget(t *testing.T) {
	var initialized atomic.Bool
	initialized.Store(true)
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr = "tcp", "127.0.0.1:1"
	cfg.Timeout = 20 * time.Millisecond
	var callerDeadline time.Time
	resolved := false
	before := managedProxyBeforeConnect(&initialized, func(ctx context.Context) (proxy.Endpoint, error) {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(callerDeadline) {
			return proxy.Endpoint{}, fmt.Errorf("discovery received dial deadline %v, want caller deadline %v", deadline, callerDeadline)
		}
		timer := time.NewTimer(3 * cfg.Timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return proxy.Endpoint{}, ctx.Err()
		}
		resolved = true
		return proxy.Endpoint{Host: "127.0.0.1", Port: 4321}, nil
	})
	stopBeforeHandshake := errors.New("dial reached after slow startup")
	cfg.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !resolved || addr != "127.0.0.1:4321" {
			return nil, fmt.Errorf("dial before resolution or with stale address: %s", addr)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Before(callerDeadline) {
			return nil, errors.New("TCP dial budget was not applied after discovery")
		}
		return nil, stopBeforeHandshake
	}
	require.NoError(t, cfg.Apply(mysql.BeforeConnect(before)))
	connector, err := mysql.NewConnector(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callerDeadline, _ = ctx.Deadline()
	_, err = connector.Connect(ctx)
	require.ErrorIs(t, err, stopBeforeHandshake)
	assert.True(t, resolved)
}

func TestManagedProxyKeepsBootstrapEndpointUntilInitialized(t *testing.T) {
	var initialized atomic.Bool
	calls := 0
	before := managedProxyBeforeConnect(&initialized, func(context.Context) (proxy.Endpoint, error) {
		calls++
		return proxy.Endpoint{Host: "127.0.0.1", Port: 4321}, nil
	})
	cfg := mysql.NewConfig()
	cfg.Addr = "127.0.0.1:1234"
	require.NoError(t, before(context.Background(), cfg))
	assert.Equal(t, 0, calls, "schema initialization must not rediscover")
	assert.Equal(t, "127.0.0.1:1234", cfg.Addr)
	initialized.Store(true)
	require.NoError(t, before(context.Background(), cfg))
	assert.Equal(t, 1, calls)
	assert.Equal(t, "127.0.0.1:4321", cfg.Addr)
}
