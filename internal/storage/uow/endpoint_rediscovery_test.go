package uow

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/stretchr/testify/require"
)

// newScriptedResolver arms a resolver whose discovery is the injected closure,
// the instance-injected seam the resolver tests use instead of package
// globals.
func newScriptedResolver(resolve func(context.Context) (proxy.Endpoint, error)) *proxyEndpointResolver {
	r := &proxyEndpointResolver{resolve: resolve}
	r.arm()
	return r
}

// TestProxyEndpointResolverCoalesces pins #7295's overlap rule: several new
// connections that need the endpoint while ONE discovery is running share that
// discovery's result, and only later connections — after the overlap — start a
// fresh one. Overlap coalesces; nothing is cached.
//
// The scripted discovery makes overlap observable: its first call blocks until
// the test releases it, and any call that is NOT coalesced into that first one
// answers immediately with a different endpoint, so a waiter that ran its own
// discovery dials the wrong address and the count says how many ran.
func TestProxyEndpointResolverCoalesces(t *testing.T) {
	released := make(chan struct{})
	var calls atomic.Int32
	const (
		sharedPort  = 42424 // the one blocked discovery every waiter must join
		freshPort   = 42425 // what an uncoalesced later discovery returns
		concurrency = 3
	)
	shared := proxy.Endpoint{Host: "127.0.0.1", Port: sharedPort}
	fresh := proxy.Endpoint{Host: "127.0.0.1", Port: freshPort}
	r := newScriptedResolver(func(ctx context.Context) (proxy.Endpoint, error) {
		switch calls.Add(1) {
		case 1:
			select {
			case <-released:
				return shared, nil
			case <-ctx.Done():
				return proxy.Endpoint{}, ctx.Err()
			}
		default:
			return fresh, nil
		}
	})

	addrs := make([]string, concurrency)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			cfg := mysql.NewConfig()
			cfg.Addr = "127.0.0.1:1"
			if err := r.beforeConnect(context.Background(), cfg); err != nil {
				t.Errorf("beforeConnect() = %v, want nil", err)
				return
			}
			addrs[i] = cfg.Addr
		}(i)
	}
	close(start)

	// Let the overlap form: one discovery running, every other waiter joined
	// to it. The generous window is for scheduling only — correctness below
	// does not depend on tighter timing.
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("discovery never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(time.Second)
	close(released)
	wg.Wait()

	for i, addr := range addrs {
		if addr != shared.Address() {
			t.Errorf("waiter %d dialed %q, want the shared discovery's %q", i, addr, shared.Address())
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("discovery ran %d times for one overlap, want exactly 1", n)
	}

	// Nothing is cached for later connections: the next one discovers again.
	cfg := mysql.NewConfig()
	if err := r.beforeConnect(context.Background(), cfg); err != nil {
		t.Fatalf("next beforeConnect() = %v, want nil", err)
	}
	if addr := cfg.Addr; addr != fresh.Address() {
		t.Fatalf("the next connection dialed %q, want a fresh discovery's %q", addr, fresh.Address())
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("discovery ran %d times total, want a fresh one after the overlap (2)", n)
	}
}

// TestProxyEndpointResolverCancellation pins #7295's cancellation rule: a
// caller's cancellation ends THAT caller's wait, promptly, and nothing else —
// the shared discovery keeps running (a spawned proxy child may already be
// adopted by a peer), the other waiters still get its result, and an
// already-canceled caller is refused before it can start work.
func TestProxyEndpointResolverCancellation(t *testing.T) {
	released := make(chan struct{})
	var calls atomic.Int32
	var completed atomic.Bool
	shared := proxy.Endpoint{Host: "127.0.0.1", Port: 42434}
	r := newScriptedResolver(func(ctx context.Context) (proxy.Endpoint, error) {
		calls.Add(1)
		select {
		case <-released:
			completed.Store(true)
			return shared, nil
		case <-ctx.Done():
			return proxy.Endpoint{}, ctx.Err()
		}
	})

	// The worker: admitted first, runs the blocked discovery.
	workerDone := make(chan error, 1)
	go func() {
		cfg := mysql.NewConfig()
		workerDone <- r.beforeConnect(context.Background(), cfg)
	}()
	waitForDiscoveryEntered(t, &calls, 1)

	// Two waiters join the pending flight; one of them cancels.
	canceledDone := make(chan error, 1)
	canceledCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		cfg := mysql.NewConfig()
		canceledDone <- r.beforeConnect(canceledCtx, cfg)
	}()
	peerDone := make(chan error, 1)
	go func() {
		cfg := mysql.NewConfig()
		peerDone <- r.beforeConnect(context.Background(), cfg)
	}()
	// Give both waiters a generous window to join the flight before cancelling.
	time.Sleep(500 * time.Millisecond)

	cancel()
	select {
	case err := <-canceledDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("canceled waiter got %v, want its context error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiter was not released while its discovery was still pending")
	}
	if calls.Load() != 1 {
		t.Fatalf("discovery was entered %d times, want the one still running", calls.Load())
	}
	if completed.Load() {
		t.Fatal("discovery completed before its release; cancellation must not run it down")
	}

	// Release: the surviving waiter and the worker both succeed from the one
	// shared discovery, which cancellation did not multiply.
	close(released)
	if err := <-peerDone; err != nil {
		t.Fatalf("surviving waiter: beforeConnect() = %v, want nil", err)
	}
	if err := <-workerDone; err != nil {
		t.Fatalf("worker: beforeConnect() = %v, want nil", err)
	}
	if !completed.Load() {
		t.Fatal("discovery never completed after release")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("cancellation started %d extra discoveries, want none", n-1)
	}

	// An already-canceled caller is refused before it can start work: no new
	// discovery is entered on its behalf, and it does not wait on anything.
	canceledCalls := calls.Load()
	canceledAgain, cancelAgain := context.WithCancel(context.Background())
	cancelAgain()
	cfg := mysql.NewConfig()
	err := r.beforeConnect(canceledAgain, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("beforeConnect() on a canceled context = %v, want the context error", err)
	}
	if cfg.Addr != "" {
		t.Errorf("a canceled caller still resolved the endpoint %q", cfg.Addr)
	}
	if n := calls.Load(); n != canceledCalls {
		t.Fatalf("a canceled caller started discovery anyway (%d entries, want %d)", n, canceledCalls)
	}
}

// waitForDiscoveryEntered polls calls until want discoveries have been
// entered, failing the test if they never are.
func waitForDiscoveryEntered(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("discovery never entered call %d", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestProxyEndpointResolverErrors pins #7295's failure contract: a discovery
// failure fails the connection with the wrapped discovery error — for the
// worker AND for the waiters sharing its flight — preserving errors.Is/As on
// the cause, never silently dialing the stale construction address, and never
// poisoning the resolver: the next connection discovers again and can
// succeed.
func TestProxyEndpointResolverErrors(t *testing.T) {
	t.Run("worker failure wraps the cause and keeps the stale address", func(t *testing.T) {
		var calls atomic.Int32
		sentinel := errors.New("discovery refused")
		r := newScriptedResolver(func(context.Context) (proxy.Endpoint, error) {
			calls.Add(1)
			return proxy.Endpoint{}, sentinel
		})
		cfg := mysql.NewConfig()
		cfg.Addr = "127.0.0.1:1" // the stale construction address

		err := r.beforeConnect(context.Background(), cfg)
		if err == nil {
			t.Fatal("beforeConnect() = nil, want the wrapped discovery error")
		}
		if !strings.Contains(err.Error(), "uow: rediscover proxy endpoint") {
			t.Errorf("beforeConnect() = %v, want the uow rediscovery wrapper", err)
		}
		if !errors.Is(err, sentinel) {
			t.Errorf("beforeConnect() = %v, want errors.Is on the discovery cause", err)
		}
		if cfg.Addr != "127.0.0.1:1" {
			t.Errorf("a failed discovery still dialed %q; want no fallback to any address change", cfg.Addr)
		}
	})

	t.Run("waiters sharing a failed flight get the same wrapped error", func(t *testing.T) {
		released := make(chan struct{})
		var calls atomic.Int32
		sentinel := errors.New("discovery refused")
		r := newScriptedResolver(func(context.Context) (proxy.Endpoint, error) {
			calls.Add(1)
			<-released
			return proxy.Endpoint{}, sentinel
		})

		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				cfg := mysql.NewConfig()
				results <- r.beforeConnect(context.Background(), cfg)
			}()
		}
		waitForDiscoveryEntered(t, &calls, 1)
		time.Sleep(500 * time.Millisecond) // let the second caller join the flight
		close(released)

		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				t.Fatal("beforeConnect() = nil, want the wrapped discovery error")
			}
			if !strings.Contains(err.Error(), "uow: rediscover proxy endpoint") {
				t.Errorf("caller %d: beforeConnect() = %v, want the uow rediscovery wrapper", i, err)
			}
			if !errors.Is(err, sentinel) {
				t.Errorf("caller %d: beforeConnect() = %v, want errors.Is on the discovery cause", i, err)
			}
		}
		if n := calls.Load(); n != 1 {
			t.Fatalf("the failed overlap ran %d discoveries, want 1", n)
		}
	})

	t.Run("a later discovery can succeed after a failure", func(t *testing.T) {
		var calls atomic.Int32
		sentinel := errors.New("discovery refused")
		recovered := proxy.Endpoint{Host: "127.0.0.1", Port: 42444}
		r := newScriptedResolver(func(context.Context) (proxy.Endpoint, error) {
			if calls.Add(1) == 1 {
				return proxy.Endpoint{}, sentinel
			}
			return recovered, nil
		})

		cfg := mysql.NewConfig()
		if err := r.beforeConnect(context.Background(), cfg); err == nil {
			t.Fatal("first beforeConnect() = nil, want the discovery failure")
		}

		cfg = mysql.NewConfig()
		if err := r.beforeConnect(context.Background(), cfg); err != nil {
			t.Fatalf("second beforeConnect() = %v, want recovery on a fresh discovery", err)
		}
		if cfg.Addr != recovered.Address() {
			t.Errorf("second beforeConnect() dialed %q, want the recovered endpoint %q", cfg.Addr, recovered.Address())
		}
	})
}

// TestProxyEndpointResolverClose pins #7295's shutdown rule: closing the
// provider marks rediscovery closed, waits for a discovery that is already
// running — the worker always finishes before Close returns, and no worker
// outlives the resolver — and every later hook is refused without starting
// discovery or dialing anything.
func TestProxyEndpointResolverClose(t *testing.T) {
	t.Run("shutdown waits for the in-flight worker and rejects later hooks", func(t *testing.T) {
		released := make(chan struct{})
		var calls atomic.Int32
		var completed atomic.Bool
		r := newScriptedResolver(func(context.Context) (proxy.Endpoint, error) {
			calls.Add(1)
			<-released
			completed.Store(true)
			return proxy.Endpoint{Host: "127.0.0.1", Port: 42454}, nil
		})

		hookDone := make(chan error, 1)
		go func() {
			cfg := mysql.NewConfig()
			hookDone <- r.beforeConnect(context.Background(), cfg)
		}()
		waitForDiscoveryEntered(t, &calls, 1)

		shutdownDone := make(chan struct{})
		go func() {
			r.shutdown()
			close(shutdownDone)
		}()
		time.Sleep(250 * time.Millisecond)
		select {
		case <-shutdownDone:
			t.Fatal("shutdown returned while its discovery was still in flight")
		default:
		}

		close(released)
		<-shutdownDone
		if !completed.Load() {
			t.Fatal("shutdown returned before the in-flight worker finished")
		}
		<-hookDone

		cfg := mysql.NewConfig()
		cfg.Addr = "127.0.0.1:1"
		if err := r.beforeConnect(context.Background(), cfg); !errors.Is(err, errProxyEndpointResolverClosed) {
			t.Errorf("beforeConnect() after shutdown = %v, want the closed error", err)
		}
		if cfg.Addr != "127.0.0.1:1" {
			t.Errorf("a closed resolver still resolved the endpoint %q; want no dial", cfg.Addr)
		}
		if n := calls.Load(); n != 1 {
			t.Fatalf("a closed resolver started another discovery (%d entries, want 1)", n)
		}
	})

	t.Run("provider Close drains the resolver before it closes the pool", func(t *testing.T) {
		front := newFlappingFront(t, 0)
		released := make(chan struct{})
		var calls atomic.Int32
		var completed atomic.Bool
		r := &proxyEndpointResolver{resolve: func(context.Context) (proxy.Endpoint, error) {
			calls.Add(1)
			<-released
			completed.Store(true)
			return proxy.Endpoint{Host: "127.0.0.1", Port: 42455}, nil
		}}

		pool, err := openPool("root@tcp("+front.addr+")/?timeout=5s", r, func(*sql.DB) error { return nil })
		if err != nil {
			t.Fatalf("openPool() = %v", err)
		}
		provider := &doltSQLProvider{db: pool, resolver: r}
		r.arm()

		hookDone := make(chan error, 1)
		go func() {
			cfg := mysql.NewConfig()
			hookDone <- r.beforeConnect(context.Background(), cfg)
		}()
		waitForDiscoveryEntered(t, &calls, 1)

		closed := make(chan error, 1)
		go func() { closed <- provider.Close(context.Background()) }()
		time.Sleep(250 * time.Millisecond)
		select {
		case err := <-closed:
			t.Fatalf("provider Close returned %v while its discovery was still in flight", err)
		default:
		}

		close(released)
		if err := <-closed; err != nil {
			t.Fatalf("provider Close() = %v", err)
		}
		if !completed.Load() {
			t.Fatal("provider Close returned before the in-flight worker finished")
		}
		<-hookDone

		cfg := mysql.NewConfig()
		if err := r.beforeConnect(context.Background(), cfg); !errors.Is(err, errProxyEndpointResolverClosed) {
			t.Errorf("beforeConnect() after provider Close = %v, want the closed error", err)
		}
		if n := calls.Load(); n != 1 {
			t.Fatalf("provider Close was followed by another discovery (%d entries, want 1)", n)
		}
	})
}

// TestProxyEndpointResolverAdoptsWithoutSpawnLock pins the adoption half of
// #7295's design over real discovery: a healthy, authenticated, running
// proxy is ADOPTED before its spawn lock is ever taken (endpoint.go adopts on
// the lock-free read-and-dial path), so an armed provider whose workspace's
// spawn lock is held by another actor still gets its new physical connection
// — discovery must not wait for the lock — and no respawn happens that would
// need it.
func TestProxyEndpointResolverAdoptsWithoutSpawnLock(t *testing.T) {
	testutil.RequireDoltBinary(t)
	doltBin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	verifiedShutdownCleanup(t, storeRootDir)
	cfgPort, err := proxy.PickFreePort()
	require.NoError(t, err)
	cfgPath := writeServerConfig(t, cfgPort)
	logPath := filepath.Join(t.TempDir(), "server.log")

	openOpts := proxy.OpenOpts{
		Backend:        proxy.BackendLocalServer,
		ConfigFilePath: cfgPath,
		LogFilePath:    logPath,
		DoltBinPath:    doltBin,
		Database:       "beads",
	}
	var rediscoveries atomic.Int32
	resolver := &proxyEndpointResolver{resolve: func(ctx context.Context) (proxy.Endpoint, error) {
		rediscoveries.Add(1)
		return proxy.GetCreateDatabaseProxyServerEndpoint(storeRootDir, openOpts)
	}}

	ctx := context.Background()
	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(storeRootDir, openOpts)
	require.NoError(t, err, "bootstrap discovery for the fixed endpoint")
	provider, err := openAndInitSchema(ctx, ep, "", "beads", "root", "", "", false, "", providerOptions{}, resolver)
	require.NoError(t, err, "open")
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	sqlProvider, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "openAndInitSchema returned %T, want *doltSQLProvider", provider)
	sqlProvider.SetPoolLimits(PoolLimits{MaxIdleConns: -1})

	pf, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	portBefore := pf.Port

	// Prove the scenario: the healthy proxy holds proxy.lock for its whole
	// lifetime (server.go), so the workspace's spawn lock is genuinely
	// unavailable to anyone else — a rediscovery that waits for it, or that
	// needs it to adopt, cannot succeed while the proxy runs.
	if _, err := util.TryLock(filepath.Join(storeRootDir, proxy.LockFileName)); !lockfile.IsLocked(err) {
		t.Fatalf("the live proxy's spawn lock was available (%v); the scenario does not hold", err)
	}

	// Bounded by a deadline: the connection must adopt the live proxy while
	// its spawn lock stays held, not eventually.
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var one int
	require.NoError(t, sqlProvider.db.QueryRowContext(queryCtx, "SELECT 1").Scan(&one),
		"a new physical connection must adopt the live proxy without waiting for the spawn lock")
	require.Equal(t, int32(1), rediscoveries.Load(),
		"the new physical connection must have rediscovered exactly once")

	// Adoption, not respawn: the same live proxy still owns the same port.
	pf, err = pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.Equal(t, portBefore, pf.Port,
		"the armed provider respawned a proxy under a held spawn lock instead of adopting the live one")
}
