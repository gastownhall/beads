package uow

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	publicops "github.com/steveyegge/beads/issueops"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/stretchr/testify/require"
)

// The provider-level recovery contract of gastownhall/beads#7295, driven
// against the real subprocess topology (a bd proxy supervising a local dolt
// sql-server): a long-lived provider — the one bd serve holds — whose
// workspace proxy exits must rediscover the endpoint through the same
// adopt-or-spawn machinery a fresh opener uses. No restart, no change to the
// workspace's idle policy, and never while the provider is still under
// construction. These are Dolt-backed tests; the lane env that makes them run
// is BEADS_TEST_DOLT_SERVER=local with BEADS_TEST_SKIP= (see
// engdocs/TESTING.md).

// recoveryFixture is one disposable proxied workspace plus the binaries its
// proxy respawn machinery needs. The server config and log paths are fixed
// for the fixture's lifetime the way a workspace's are, so every opener —
// and every rediscovery — resolves the same topology.
type recoveryFixture struct {
	t       *testing.T
	root    string
	bin     string // dolt
	cfgPath string
	logPath string
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	shutdownOnInterrupt(t, root)
	verifiedShutdownCleanup(t, root)

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	return &recoveryFixture{
		t:       t,
		root:    root,
		bin:     bin,
		cfgPath: writeServerConfig(t, port),
		logPath: filepath.Join(t.TempDir(), "server.log"),
	}
}

// openE constructs one provider on the fixture's workspace. The pool limits
// mirror serve's shape: idle connections are retired quickly so a test can
// drive the proxy's idle watcher without serving traffic.
func (f *recoveryFixture) openE(idleTimeout time.Duration, opts ...ProviderOption) (UnitOfWorkProvider, error) {
	f.t.Helper()
	provider, err := NewDoltServerUOWProvider(
		context.Background(),
		f.root,
		"beads",
		f.logPath,
		f.cfgPath,
		proxy.BackendLocalServer,
		"root",
		"",
		f.bin,
		0,
		idleTimeout,
		false,
		"",
		opts...,
	)
	if err != nil {
		return nil, err
	}
	f.t.Cleanup(func() { _ = provider.Close(context.Background()) })
	if tuner, ok := provider.(PoolTuner); ok {
		tuner.SetPoolLimits(PoolLimits{MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxIdleTime: 200 * time.Millisecond})
	}
	return provider, nil
}

func (f *recoveryFixture) open(idleTimeout time.Duration, opts ...ProviderOption) UnitOfWorkProvider {
	f.t.Helper()
	provider, err := f.openE(idleTimeout, opts...)
	require.NoError(f.t, err, "provider construction")
	return provider
}

// read is one database-backed read: a read transaction that must dial (or
// reuse) a pool connection to answer. Errors are returned, not asserted, so
// recovery loops can decide what to tolerate.
func (f *recoveryFixture) read(provider UnitOfWorkProvider) error {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return f.readCtx(ctx, provider)
}

// readCtx is read on the caller's clock: a caller can bound one below a
// proxy spawn's duration and observe the read fail while the recovery its
// failed dial started keeps running.
func (f *recoveryFixture) readCtx(ctx context.Context, provider UnitOfWorkProvider) error {
	f.t.Helper()
	reconciler, err := NewVersionReconciler(provider)
	if err != nil {
		return err
	}
	_, err = reconciler.RecordedVersion(ctx, publicops.RecordedVersionRequest{})
	return err
}

// recoverableForLoop reports whether err is a shape a recovery loop may wait
// out: the endpoint-unavailability class (matched on the marker's message, the
// same text on every build), or the raw connection refusal a dial against an
// exited proxy produces on a build without recovery.
func recoverableForLoop(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "proxy endpoint unavailable") ||
		strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "unexpected EOF") ||
		strings.Contains(err.Error(), "invalid connection")
}

// mustRecoverRead loops reads for up to window, measured from the first
// attempt — the recovery window AC-1 promises — and fails the test unless a
// read succeeds inside it. Failures outside the tolerated shapes abort
// immediately: something other than the proxy's absence is broken.
func (f *recoveryFixture) mustRecoverRead(provider UnitOfWorkProvider, window time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(window)
	for {
		err := f.read(provider)
		if err == nil {
			return
		}
		if !recoverableForLoop(err) {
			f.t.Fatalf("read failed with a non-recovery error: %v", err)
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("reads did not recover within %s of the first post-exit read; last error: %v", window, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// waitForProxyStopped polls the workspace's records until no proxy is
// running, without starting or adopting anything on the way (ReadStatus is
// the reporting-only view).
func (f *recoveryFixture) waitForProxyStopped(window time.Duration) proxy.Status {
	f.t.Helper()
	deadline := time.Now().Add(window)
	for {
		st := proxy.ReadStatus(f.root)
		if !st.ProxyRunning {
			return st
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("proxy under %s still running after %s", f.root, window)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestNewDoltServerUOWProvider_RediscoversProxyAfterIdleExit is the
// provider-level half of AC-1 (gastownhall/beads#7295): after the pool
// retires its idle connections and the workspace proxy idle-exits, a
// database-backed read must answer again within 60s of the first post-exit
// read, without restarting the provider and without touching the workspace's
// idle policy. On a build without recovery this loop exhausts the window: the
// provider dials the exited proxy's port forever.
func TestNewDoltServerUOWProvider_RediscoversProxyAfterIdleExit(t *testing.T) {
	f := newRecoveryFixture(t)
	provider := f.open(2 * time.Second)

	if err := f.read(provider); err != nil {
		t.Fatalf("pre-idle read: %v", err)
	}

	// The pool retires the read's connection after 200ms; the proxy's 2s
	// idle watcher (1s tick) then shuts the whole tree down. Six seconds
	// covers both plus margin, without a probe that could keep the proxy
	// alive.
	time.Sleep(6 * time.Second)
	f.waitForProxyStopped(5 * time.Second)

	f.mustRecoverRead(provider, 60*time.Second)
}

// TestNewDoltServerUOWProvider_AdoptsReplacementProxy is AC-2: when another
// opener has started a verified replacement proxy, a provider still holding
// the previous endpoint adopts that replacement on its next read — it must
// not spawn its own, and the replacement must survive the adoption with the
// same PID.
func TestNewDoltServerUOWProvider_AdoptsReplacementProxy(t *testing.T) {
	f := newRecoveryFixture(t)
	stale := f.open(5 * time.Minute)

	if err := f.read(stale); err != nil {
		t.Fatalf("first read through the first proxy: %v", err)
	}
	before := proxy.ReadStatus(f.root)
	require.True(t, before.ProxyRunning, "first proxy must be running after construction")

	// The workspace's proxy exits for some reason outside the provider's
	// control — a stop, an idle exit, a crash; the shutdown here is only
	// the deterministic way to arrange one.
	require.NoError(t, proxy.Shutdown(f.root))
	f.waitForProxyStopped(5 * time.Second)

	// Another opener (any bd command on the workspace) constructs a
	// provider, which spawns a verified replacement.
	replacement := f.open(5 * time.Minute)
	if err := f.read(replacement); err != nil {
		t.Fatalf("read through the replacement proxy: %v", err)
	}
	afterOpen := proxy.ReadStatus(f.root)
	require.True(t, afterOpen.ProxyRunning, "replacement proxy must be running")
	require.NotZero(t, afterOpen.ProxyPID)
	require.NotEqual(t, before.ProxyPID, afterOpen.ProxyPID,
		"the replacement must be a new process, not the respawned pid of the stopped one")

	// The stale provider recovers by adopting the replacement.
	f.mustRecoverRead(stale, 60*time.Second)

	afterAdopt := proxy.ReadStatus(f.root)
	require.Equal(t, afterOpen.ProxyPID, afterAdopt.ProxyPID,
		"recovery must adopt the running replacement, not spawn a second proxy")
	require.True(t, afterAdopt.BackendRunning, "the supervised backend must still be running after adoption")
}

// TestNewDoltServerUOWProvider_ConstructionNeverRediscovers is AC-3: a
// provider under construction gets its own retries and errors — no
// rediscovery behind the constructor's back — and only a construction that
// succeeds arms recovery. The first half stops the fresh proxy while the
// constructor is still dialing and pins that the failure is the historical
// one (raw, unclassified, nothing respawned by construction retries); the
// second half constructs again successfully and proves that THAT provider
// recovers a proxy exit.
func TestNewDoltServerUOWProvider_ConstructionNeverRediscovers(t *testing.T) {
	f := newRecoveryFixture(t)

	// Kill the proxy as soon as the constructor's GetCreate has published
	// one, so the construction dials that follow race only against a
	// proxy that is already gone.
	stopped := make(chan struct{})
	stopperDone := make(chan struct{})
	go func() {
		defer close(stopped)
		defer close(stopperDone)
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			if proxy.ReadStatus(f.root).ProxyRunning {
				_ = proxy.Shutdown(f.root)
				return
			}
			select {
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() { <-stopperDone })

	_, err := f.openE(5 * time.Minute)
	<-stopped
	require.Error(t, err, "construction must fail when its proxy died")
	require.NotContains(t, err.Error(), "proxy endpoint unavailable",
		"a construction failure keeps its historical shape; recovery is unarmed until the open succeeds: %v", err)
	require.ErrorContains(t, err, "uow:", "the historical construction error wrapper")

	// Nothing respawned the proxy while construction was retrying: only
	// the constructor's explicit GetCreate — which the stopper's Shutdown
	// already retired — may start one.
	st := f.waitForProxyStopped(2 * time.Second)
	require.False(t, st.ProxyRunning, "no construction retry may respawn the proxy")

	// Successful construction arms recovery: on a build without recovery
	// (AC-1), this second half is the red one.
	provider := f.open(5 * time.Minute)
	if err := f.read(provider); err != nil {
		t.Fatalf("read after successful construction: %v", err)
	}
	require.NoError(t, proxy.Shutdown(f.root))
	f.waitForProxyStopped(5 * time.Second)
	f.mustRecoverRead(provider, 60*time.Second)
}

// TestNewDoltServerUOWProvider_CloseStopsRediscovery is AC-5 at the provider
// boundary: Close succeeds while a discovery flight is still running (and
// the flight's spawn survives — a shared proxy is never killed for having
// been discovered), and a provider whose proxy is already gone does not
// spawn a replacement while closing.
func TestNewDoltServerUOWProvider_CloseStopsRediscovery(t *testing.T) {
	f := newRecoveryFixture(t)

	// Closing with no flight in sight must neither fail nor respawn
	// anything: the pool is closed before any dial could fail.
	quiet := f.open(5 * time.Minute)
	if err := f.read(quiet); err != nil {
		t.Fatalf("read before close: %v", err)
	}
	require.NoError(t, proxy.Shutdown(f.root))
	f.waitForProxyStopped(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, quiet.Close(ctx), "Close with no running flight")
	_ = quiet.Close(ctx) // f's cleanup closes it again; both must be quiet
	require.False(t, proxy.ReadStatus(f.root).ProxyRunning,
		"a closed provider must not spawn a replacement proxy")

	// A provider whose read is mid-recovery — a flight fetching a fresh
	// proxy — closes cleanly, joining the flight. The flight's spawn is
	// not killed for the provider's sake; it keeps the workspace's own
	// idle policy.
	armed := f.open(5 * time.Minute)
	if err := f.read(armed); err != nil {
		t.Fatalf("read before the shutdown: %v", err)
	}
	require.NoError(t, proxy.Shutdown(f.root))
	f.waitForProxyStopped(5 * time.Second)

	// Short reads in a loop until the flight's spawn is visible in the
	// workspace records. The first fresh dial is refused and starts the
	// discovery flight; every further short read JOINS that same running
	// flight (no cooldown applies to an existing one) and returns on its
	// own budget, so the loop drives both host-speed regimes: a fast
	// spawn finishes inside one read (which then succeeds); a slow one
	// takes a few unavailable reads before ReadStatus sees it. The first
	// read may still burn the pool's stale connection without dialing —
	// its "invalid connection" error is not evidence, the loop simply
	// continues — so the loop requires an end condition it can observe,
	// not one particular error shape.
	var running proxy.Status
	require.Eventually(t, func() bool {
		boundedCtx, boundedCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer boundedCancel()
		_ = f.readCtx(boundedCtx, armed)
		running = proxy.ReadStatus(f.root)
		return running.ProxyRunning
	}, 30*time.Second, 10*time.Millisecond, "a refused dial must start a flight that spawns a replacement")

	// A concurrent reader is mid-recovery — at the least it is holding the
	// pool that Close is about to shut: Close must not deadlock against
	// it, and the reader must be woken by the shutdown rather than left
	// hanging on a pool nobody will serve.
	readDone := make(chan error, 1)
	go func() {
		readDone <- f.read(armed)
	}()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer closeCancel()
	require.NoError(t, armed.Close(closeCtx), "Close must join the running discovery flight")

	// The flight's spawn survives the provider's close — only discovery
	// was stopped, the shared proxy itself is never torn down for the
	// provider's sake — same PID, still running.
	afterClose := proxy.ReadStatus(f.root)
	require.True(t, afterClose.ProxyRunning, "the flight's proxy spawn must survive the provider's close")
	require.Equal(t, running.ProxyPID, afterClose.ProxyPID,
		"Close stops discovery, never the shared proxy: same PID before and after")

	// The woken reader finishes on the closed provider instead of hanging.
	require.Eventually(t, func() bool {
		select {
		case <-readDone:
			return true
		default:
			return false
		}
	}, 20*time.Second, 100*time.Millisecond, "the mid-recovery reader must be woken by Close")

	// A closed provider never resurrects: stop the shared proxy after the
	// close and a read must fail with no respawn behind the failure —
	// recovery is disabled first, and `quiet.Close` above proved the same
	// for a provider whose proxy stopped before its close.
	require.NoError(t, proxy.Shutdown(f.root))
	f.waitForProxyStopped(5 * time.Second)
	require.Error(t, f.read(armed), "a read on the closed provider fails")
	require.False(t, proxy.ReadStatus(f.root).ProxyRunning,
		"a closed provider must not respawn a replacement proxy")
}
