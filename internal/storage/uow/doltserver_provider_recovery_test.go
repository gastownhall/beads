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
