package proxy_test

import (
	"errors"
	"net"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listenLoopback opens a throwaway loopback listener and returns its port,
// registering cleanup. A live listener makes readAndDial's port probe succeed,
// so GetCreateDatabaseProxyServerEndpoint takes the reuse path instead of
// forking a child — letting these tests exercise the shared-backend reuse and
// upstream-ID guard without a real dolt server (mirrors endpoint_mismatch_test).
func listenLoopback(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// TestSharedRootDir_ReusesOneChild proves that a GetCreateDatabaseProxyServerEndpoint
// call for the BackendLocalSharedServer backend at a rootDir already served by a
// live proxy (simulated via a pidfile + listener) reuses that one child instead
// of forking another — the process-collapse half of be-pen9. Two scopes pointed
// at one shared rootDir therefore resolve to a single db-proxy-child.
func TestSharedRootDir_ReusesOneChild(t *testing.T) {
	root := t.TempDir()
	port := listenLoopback(t)

	cfg := configfile.ExternalDoltConfig{Host: "10.0.0.1", Port: 3306}
	require.NoError(t, pidfile.Write(root, proxy.PIDFileName, pidfile.PidFile{
		Pid:        4321,
		Port:       port,
		UpstreamID: server.ExternalDoltServerID(cfg),
	}))

	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(root, proxy.OpenOpts{
		Backend:     proxy.BackendLocalSharedServer,
		External:    cfg,
		LogFilePath: root + "/server.log",
	})
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ep.Host)
	assert.Equal(t, port, ep.Port, "shared backend must reuse the already-running proxy, not fork a new one")
}

// TestSharedBackend_RejectsUpstreamMismatch proves the shared backend honors the
// upstream-ID guard: a proxy already fronting a *different* managed dolt must be
// rejected, never silently reused. A shared proxy pointed at the wrong upstream
// would otherwise serve every collapsed scope the wrong store.
func TestSharedBackend_RejectsUpstreamMismatch(t *testing.T) {
	root := t.TempDir()
	port := listenLoopback(t)

	existing := configfile.ExternalDoltConfig{Host: "10.0.0.1", Port: 3306}
	require.NoError(t, pidfile.Write(root, proxy.PIDFileName, pidfile.PidFile{
		Pid:        4321,
		Port:       port,
		UpstreamID: server.ExternalDoltServerID(existing),
	}))

	want := configfile.ExternalDoltConfig{Host: "10.0.0.2", Port: 3306}
	_, err := proxy.GetCreateDatabaseProxyServerEndpoint(root, proxy.OpenOpts{
		Backend:     proxy.BackendLocalSharedServer,
		External:    want,
		LogFilePath: root + "/server.log",
	})
	require.Error(t, err)

	var mismatch *proxy.ErrUpstreamMismatch
	require.True(t, errors.As(err, &mismatch), "expected ErrUpstreamMismatch, got %T: %v", err, err)
	assert.Equal(t, server.ExternalDoltServerID(want), mismatch.Want)
	assert.Equal(t, server.ExternalDoltServerID(existing), mismatch.Have)
}

// TestSharedBackend_RequiresExternal proves the shared backend validates its
// upstream config up front: without an External target there is no managed dolt
// to front and no upstream ID to match, so the call must fail validation rather
// than spawn a proxy pointed at nothing.
func TestSharedBackend_RequiresExternal(t *testing.T) {
	root := t.TempDir()
	_, err := proxy.GetCreateDatabaseProxyServerEndpoint(root, proxy.OpenOpts{
		Backend:     proxy.BackendLocalSharedServer,
		LogFilePath: root + "/server.log",
		// External omitted on purpose.
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "External")
}
