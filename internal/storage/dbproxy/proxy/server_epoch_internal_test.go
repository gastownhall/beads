package proxy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

// epochAdvancingServer simulates the F4 race deterministically: `bd dolt
// stop` advancing the stop epoch while the proxy child is inside its (slow)
// backend Start, after the spawn marker has already been cleared under
// proxy.lock.
type epochAdvancingServer struct {
	*server.TestDatabaseServerImpl
	rootDir string
	t       *testing.T
}

func (s *epochAdvancingServer) Start(ctx context.Context) error {
	if err := s.TestDatabaseServerImpl.Start(ctx); err != nil {
		return err
	}
	require.NoError(s.t, advanceStopEpoch(s.rootDir))
	return nil
}

// TestListenAndServe_StopEpochAdvanceDuringStartupAbortsPublish asserts that
// a stop which begins after the child took proxy.lock (and cleared the spawn
// marker) but before proxy.pid is written makes the child abort instead of
// publishing a running proxy after the stop returned.
func TestListenAndServe_StopEpochAdvanceDuringStartupAbortsPublish(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	baseline, err := readStopEpoch(root)
	require.NoError(t, err)

	ts := server.New()
	backend := &epochAdvancingServer{TestDatabaseServerImpl: ts, rootDir: root, t: t}

	p := NewProxyServer(ProxyOpts{
		RootDir:   root,
		Port:      0,
		Server:    backend,
		StopEpoch: baseline,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = p.ListenAndServe(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, errStartInterrupted)

	pf, readErr := pidfile.Read(root, PIDFileName)
	require.NoError(t, readErr)
	assert.Nil(t, pf, "proxy.pid must not be published after the stop epoch advanced")

	counters := ts.Snapshot()
	assert.Equal(t, int64(1), counters.StartCalls)
	assert.Equal(t, int64(1), counters.StopCalls, "aborting the publish must still stop the backend")
}

// TestListenAndServe_StopEpochAdvancedBeforeStartupNeverBootsBackend asserts
// the fast-abort: a stop epoch that advanced between the spawning parent's
// read and the child taking proxy.lock aborts the start before any backend
// boot, so the child holds proxy.lock for milliseconds, not a boot cycle.
func TestListenAndServe_StopEpochAdvancedBeforeStartupNeverBootsBackend(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	baseline, err := readStopEpoch(root)
	require.NoError(t, err)
	require.NoError(t, advanceStopEpoch(root))

	ts := server.New()
	p := NewProxyServer(ProxyOpts{
		RootDir:   root,
		Port:      0,
		Server:    ts,
		StopEpoch: baseline,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = p.ListenAndServe(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, errStartInterrupted)

	pf, readErr := pidfile.Read(root, PIDFileName)
	require.NoError(t, readErr)
	assert.Nil(t, pf, "proxy.pid must not be published after a pre-startup stop")

	counters := ts.Snapshot()
	assert.Equal(t, int64(0), counters.StartCalls, "a pre-doomed start must never boot the backend")
	assert.Equal(t, int64(0), counters.StopCalls)
}

// blockingStopServer holds Stop open until released, exposing the window in
// which a doomed start is tearing its backend down.
type blockingStopServer struct {
	*epochAdvancingServer
	stopEntered chan struct{}
	stopRelease chan struct{}
}

func (s *blockingStopServer) Stop(ctx context.Context) error {
	close(s.stopEntered)
	<-s.stopRelease
	return s.epochAdvancingServer.TestDatabaseServerImpl.Stop(ctx)
}

// TestListenAndServe_InterruptedStartReleasesProxyLockBeforeBackendStop pins
// the bd-ill4f invariant directly: a start doomed by a concurrent stop must
// release proxy.lock BEFORE its backend teardown, because the interrupting
// Shutdown polls that lock under shutdownConfirmDeadline (5s) while a backend
// stop may take far longer. Before the fix this deadlocked the stopper into
// "timeout (5s) acquiring proxy.lock after inspecting pid 0".
func TestListenAndServe_InterruptedStartReleasesProxyLockBeforeBackendStop(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	baseline, err := readStopEpoch(root)
	require.NoError(t, err)

	ts := server.New()
	backend := &blockingStopServer{
		epochAdvancingServer: &epochAdvancingServer{TestDatabaseServerImpl: ts, rootDir: root, t: t},
		stopEntered:          make(chan struct{}),
		stopRelease:          make(chan struct{}),
	}

	p := NewProxyServer(ProxyOpts{
		RootDir:   root,
		Port:      0,
		Server:    backend,
		StopEpoch: baseline,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- p.ListenAndServe(ctx) }()

	select {
	case <-backend.stopEntered:
	case err := <-serveDone:
		t.Fatalf("ListenAndServe returned before backend Stop was entered: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("doomed start never reached backend Stop")
	}

	// The doomed backend teardown is now in flight; proxy.lock must already
	// be free for the concurrent stopper.
	lock, lockErr := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, lockErr, "proxy.lock must be released before the doomed backend teardown, not after")
	lock.Unlock()

	close(backend.stopRelease)
	select {
	case err := <-serveDone:
		require.Error(t, err)
		assert.ErrorIs(t, err, errStartInterrupted)
	case <-time.After(15 * time.Second):
		t.Fatal("ListenAndServe did not return after backend Stop was released")
	}

	pf, readErr := pidfile.Read(root, PIDFileName)
	require.NoError(t, readErr)
	assert.Nil(t, pf, "proxy.pid must not be published after the stop epoch advanced")
	counters := ts.Snapshot()
	assert.Equal(t, int64(1), counters.StopCalls, "aborting the publish must still stop the backend")
}

// TestListenAndServe_StopEpochAdvanceAbortsSlowReadyWait asserts the startup
// epoch watcher: a stop that lands while the backend is booting (here: never
// becoming ready) aborts the start within a poll interval or two instead of
// riding out the full serverReadyTimeout with proxy.lock held.
func TestListenAndServe_StopEpochAdvanceAbortsSlowReadyWait(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	baseline, err := readStopEpoch(root)
	require.NoError(t, err)

	ts := server.New()
	ts.DialErr = errors.New("backend never becomes ready")
	backend := &epochAdvancingServer{TestDatabaseServerImpl: ts, rootDir: root, t: t}

	p := NewProxyServer(ProxyOpts{
		RootDir:   root,
		Port:      0,
		Server:    backend,
		StopEpoch: baseline,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	started := time.Now()
	err = p.ListenAndServe(ctx)
	elapsed := time.Since(started)
	require.Error(t, err)
	assert.ErrorIs(t, err, errStartInterrupted)
	// Without the watcher the doomed start only notices the epoch after the
	// full ready wait (serverReadyTimeout, 30s). The generous bound still
	// proves the watcher fired; typical runtimes are a few hundred ms.
	assert.Less(t, elapsed, 15*time.Second,
		"doomed start must abort its ready wait via the epoch watcher, not ride out serverReadyTimeout")

	pf, readErr := pidfile.Read(root, PIDFileName)
	require.NoError(t, readErr)
	assert.Nil(t, pf, "proxy.pid must not be published after the stop epoch advanced")
	counters := ts.Snapshot()
	assert.Equal(t, int64(1), counters.StopCalls, "aborting the ready wait must still stop the backend")
}
