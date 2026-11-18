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

// retirementIdleTimeout is the proxy idle timeout the rediscovery fixtures use:
// short enough that a drained pool sees its proxy retire within seconds.
const retirementIdleTimeout = 2 * time.Second

// waitForProxyRetirement waits until the proxy recorded in rootDir has exited
// and returns its record. Retirement is observed through the record's
// authenticated process identity, never assumed from elapsed time.
func waitForProxyRetirement(t *testing.T, rootDir string) *pidfile.PidFile {
	t.Helper()
	old, err := pidfile.Read(rootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, old, "no proxy record in %s", rootDir)
	deadline := time.Now().Add(60 * time.Second)
	for {
		alive, err := procid.Verify(old.Pid, procid.Token(old.Birth))
		require.NoError(t, err)
		if !alive {
			return old
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(rootDir, proxy.LogFileName))
			t.Fatalf("proxy pid %d never retired on idle; proxy log:\n%s", old.Pid, log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// staleListener owns a retired proxy's port and counts every connection a
// static-address pool would make to it. It accepts and immediately closes, so
// a wrong dial fails fast instead of reaching any database.
type staleListener struct {
	accepts atomic.Int64
}

func listenOnRetiredPort(t *testing.T, port int) *staleListener {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	// Not evidence either way: without the old port the fixture cannot tell a
	// stale dial from a rediscovered one.
	require.NoError(t, err, "fixture setup: could not take over the retired proxy port %d", port)
	sl := &staleListener{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sl.accepts.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return sl
}

// commitProbeRow creates a one-row table through a committed unit of work.
func commitProbeRow(ctx context.Context, t *testing.T, p *doltSQLProvider, table, value string) {
	t.Helper()
	_, err := p.db.ExecContext(ctx, "CREATE TABLE "+table+" (id INT PRIMARY KEY, value VARCHAR(64))")
	require.NoError(t, err)
	tx, err := p.BeginTx(ctx)
	require.NoError(t, err)
	if _, err := tx.Runner().ExecContext(ctx, "INSERT INTO "+table+" VALUES (1, ?)", value); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	require.NoError(t, tx.Commit(ctx, ""))
}

// readProbeRow reads the probe row inside a fresh unit of work, on that unit's
// pinned session, together with the database the session landed on.
func readProbeRow(ctx context.Context, p *doltSQLProvider, table string) (database, value string, err error) {
	tx, err := p.BeginTx(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	conn := tx.(*doltServerTx).conn
	err = conn.QueryRowContext(ctx, "SELECT DATABASE(), value FROM "+table+" WHERE id = 1").Scan(&database, &value)
	return database, value, err
}

// TestManagedProviderRediscoversAfterProxyRetirement pins #7295 for the managed
// constructor: after the proxy retires on idle, a long-lived provider's next
// physical connection must reach a rediscovered proxy and the same database,
// never the port the retired proxy published.
func TestManagedProviderRediscoversAfterProxyRetirement(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const database = "managed_rediscovery"
	provider, err := NewDoltServerUOWProvider(ctx, storeRootDir, database,
		filepath.Join(t.TempDir(), "server.log"), writeServerConfig(t, port),
		proxy.BackendLocalServer, "root", "", bin, 0, retirementIdleTimeout, false, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	p, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "NewDoltServerUOWProvider returned %T, want *doltSQLProvider", provider)
	bootstrapEndpoint := p.serverEndpoint

	commitProbeRow(ctx, t, p, "rediscovery_probe", "persisted before idle")
	// Retain no idle sessions, like bd serve after its pool idle time: the
	// proxy then sees zero clients and every later unit of work dials anew.
	// The caller's open-connection limit must survive rediscovery.
	const clients = 4
	p.SetPoolLimits(PoolLimits{MaxOpenConns: clients, MaxIdleConns: -1})

	old := waitForProxyRetirement(t, storeRootDir)
	stale := listenOnRetiredPort(t, old.Port)

	// Several units of work open new physical connections at once, each
	// reading on its own pinned session under a bounded request context.
	// Concurrent recovery adapted from PR #7998 by @vishnujayvel.
	type probeResult struct {
		database, value string
		err             error
	}
	results := make([]probeResult, clients)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request, done := context.WithTimeout(ctx, 30*time.Second)
			defer done()
			r := &results[i]
			r.database, r.value, r.err = readProbeRow(request, p, "rediscovery_probe")
		}()
	}
	// Every worker finishes before any assertion can end the test and run
	// fixture cleanup underneath it.
	wg.Wait()
	for i, r := range results {
		require.NoError(t, r.err, "unit of work %d after the proxy retired on idle", i)
		assert.Equal(t, database, r.database, "unit of work %d database", i)
		assert.Equal(t, "persisted before idle", r.value, "unit of work %d value", i)
	}
	assert.Zero(t, stale.accepts.Load(), "the provider dialed the retired proxy's port")
	assert.Equal(t, clients, p.db.Stats().MaxOpenConnections, "caller pool limit after rediscovery")

	fresh, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	assert.NotEqual(t, old.Port, fresh.Port, "replacement proxy port")
	assert.True(t, old.Pid != fresh.Pid || old.Birth != fresh.Birth, "proxy process identity must change")
	assert.Equal(t, bootstrapEndpoint, p.serverEndpoint, "bootstrap authority stays bound to its original endpoint")
}
