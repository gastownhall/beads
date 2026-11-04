package uow

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/stretchr/testify/require"
)

// shutdownProxyAndWait stops the workspace's proxy the deterministic way and
// waits for its process to leave the process table.
func shutdownProxyAndWait(t *testing.T, storeRootDir string) {
	t.Helper()
	old, err := pidfile.Read(storeRootDir, proxy.PIDFileName)
	require.NoError(t, err)
	require.NotNil(t, old, "a proxy must be running before it is stopped")
	require.NoError(t, proxy.Shutdown(storeRootDir))
	waitProxyExit(t, storeRootDir, old, 5*time.Second)
}

// TestNewDoltServerUOWProvider_CloseStopsRediscovery is GH#7295's Close
// contract at the provider boundary, on the real subprocess topology: Close
// succeeds while a discovery flight is still running (and the flight's spawn
// survives — a shared proxy is never killed for having been discovered), and
// a provider whose proxy is already gone does not spawn a replacement while
// closing or after it. The resolver-level barrier test pins the flight join
// deterministically; this one pins what a workspace sees.
func TestNewDoltServerUOWProvider_CloseStopsRediscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const database = "managed_close"

	// Closing with no flight in sight must neither fail nor respawn
	// anything: recovery is disabled before the pool closes.
	quiet, quietRoot := openManagedProvider(ctx, t, database, 5*time.Minute)
	quiet.db.SetMaxIdleConns(0)
	seedProbe(ctx, t, quiet)
	require.NoError(t, readProbe(ctx, quiet, database), "read before close")
	shutdownProxyAndWait(t, quietRoot)
	closeCtx, closeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer closeCancel()
	require.NoError(t, quiet.Close(closeCtx), "Close with no running flight")
	require.NoError(t, quiet.Close(closeCtx), "a repeated Close is quiet")
	require.False(t, proxy.ReadStatus(quietRoot).ProxyRunning,
		"a closed provider must not spawn a replacement proxy")

	// A provider whose read is mid-recovery — a flight fetching a fresh
	// proxy — closes cleanly, joining the flight. The flight's spawn is not
	// killed for the provider's sake; it keeps the workspace's own idle
	// policy.
	armed, armedRoot := openManagedProvider(ctx, t, database, 5*time.Minute)
	armed.db.SetMaxIdleConns(0)
	seedProbe(ctx, t, armed)
	require.NoError(t, readProbe(ctx, armed, database), "read before the shutdown")
	shutdownProxyAndWait(t, armedRoot)

	// Short reads in a loop until the flight's spawn is visible in the
	// workspace records. The first refused dial starts the discovery
	// flight; every further short read joins that running flight and
	// returns on its own budget, so the loop covers both a spawn that
	// finishes inside one read and one that takes several.
	var running proxy.Status
	require.Eventually(t, func() bool {
		boundedCtx, boundedCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer boundedCancel()
		_ = readProbe(boundedCtx, armed, database)
		running = proxy.ReadStatus(armedRoot)
		return running.ProxyRunning
	}, 30*time.Second, 10*time.Millisecond, "a refused dial must start a flight that spawns a replacement")

	// A concurrent reader holds the pool Close is about to shut: Close must
	// not deadlock against it, and the reader must be woken by the shutdown
	// rather than left hanging on a pool nobody will serve.
	readDone := make(chan error, 1)
	go func() {
		readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
		defer readCancel()
		readDone <- readProbe(readCtx, armed, database)
	}()
	require.NoError(t, armed.Close(closeCtx), "Close must join the running discovery flight")

	// Close stops discovery, never the shared proxy: same PID, still
	// running.
	afterClose := proxy.ReadStatus(armedRoot)
	require.True(t, afterClose.ProxyRunning, "the flight's proxy spawn must survive the provider's close")
	require.Equal(t, running.ProxyPID, afterClose.ProxyPID,
		"Close stops discovery, never the shared proxy: same PID before and after")

	select {
	case <-readDone:
	case <-time.After(20 * time.Second):
		t.Fatal("the mid-recovery reader must be woken by Close")
	}

	// A closed provider never resurrects: stop the shared proxy after the
	// close and a read must fail with no respawn behind the failure.
	shutdownProxyAndWait(t, armedRoot)
	require.Error(t, readProbe(ctx, armed, database), "a read on the closed provider fails")
	require.False(t, proxy.ReadStatus(armedRoot).ProxyRunning,
		"a closed provider must not respawn a replacement proxy")
}
