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

func TestGetCreateDatabaseProxyServerEndpoint_RejectsUpstreamMismatch(t *testing.T) {
	root := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	existingCfg := configfile.ExternalDoltConfig{Host: "10.0.0.1", Port: 3306}
	require.NoError(t, pidfile.Write(root, proxy.PIDFileName, pidfile.PidFile{
		Pid:        12345,
		Port:       port,
		UpstreamID: server.ExternalDoltServerID(existingCfg),
	}))

	wantCfg := configfile.ExternalDoltConfig{Host: "10.0.0.2", Port: 3306}
	_, err = proxy.GetCreateDatabaseProxyServerEndpoint(root, proxy.OpenOpts{
		Backend:     proxy.BackendExternal,
		External:    wantCfg,
		LogFilePath: root + "/server.log",
	})
	require.Error(t, err)

	var mismatch *proxy.ErrUpstreamMismatch
	require.True(t, errors.As(err, &mismatch), "expected ErrUpstreamMismatch, got %T: %v", err, err)
	assert.Equal(t, root, mismatch.RootDir)
	assert.Equal(t, server.ExternalDoltServerID(wantCfg), mismatch.Want)
	assert.Equal(t, server.ExternalDoltServerID(existingCfg), mismatch.Have)
	assert.True(t, proxy.IsUpstreamMismatch(err))
}

func TestGetCreateDatabaseProxyServerEndpoint_ReusesMatchingUpstream(t *testing.T) {
	root := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := configfile.ExternalDoltConfig{Host: "10.0.0.1", Port: 3306}
	require.NoError(t, pidfile.Write(root, proxy.PIDFileName, pidfile.PidFile{
		Pid:        12345,
		Port:       port,
		UpstreamID: server.ExternalDoltServerID(cfg),
	}))

	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(root, proxy.OpenOpts{
		Backend:     proxy.BackendExternal,
		External:    cfg,
		LogFilePath: root + "/server.log",
	})
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ep.Host)
	assert.Equal(t, port, ep.Port)
}

func TestGetCreateDatabaseProxyServerEndpoint_LegacyPidfileWithoutIDReused(t *testing.T) {
	root := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	require.NoError(t, pidfile.Write(root, proxy.PIDFileName, pidfile.PidFile{
		Pid:  12345,
		Port: port,
	}))

	ep, err := proxy.GetCreateDatabaseProxyServerEndpoint(root, proxy.OpenOpts{
		Backend:     proxy.BackendExternal,
		External:    configfile.ExternalDoltConfig{Host: "10.0.0.1", Port: 3306},
		LogFilePath: root + "/server.log",
	})
	require.NoError(t, err)
	assert.Equal(t, port, ep.Port)
}

func TestErrUpstreamMismatch_Message(t *testing.T) {
	e := &proxy.ErrUpstreamMismatch{
		RootDir: "/tmp/myserver",
		Want:    "want_hash",
		Have:    "have_hash",
	}
	assert.Equal(t, "proxy at /tmp/myserver fronts upstream have_hash, not want_hash", e.Error())
	assert.True(t, proxy.IsUpstreamMismatch(e))
}
