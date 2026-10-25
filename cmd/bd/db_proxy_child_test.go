package main

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDatabaseServer_BackendExternal(t *testing.T) {
	t.Run("valid tcp config builds an ExternalDoltServer", func(t *testing.T) {
		srv, err := newDatabaseServer(
			proxy.BackendExternal,
			"", "", "", "", "",
			configfile.ExternalDoltConfig{Host: "db.internal", Port: 3306},
		)
		require.NoError(t, err)
		require.NotNil(t, srv)
		_, ok := srv.(*server.ExternalDoltServer)
		assert.True(t, ok, "expected *server.ExternalDoltServer, got %T", srv)
	})

	t.Run("invalid config bubbles validation error", func(t *testing.T) {
		_, err := newDatabaseServer(
			proxy.BackendExternal,
			"", "", "", "", "",
			configfile.ExternalDoltConfig{},
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ExternalDoltConfig")
	})

	t.Run("unix socket config builds an ExternalDoltServer", func(t *testing.T) {
		// ExternalDoltConfig.Validate() requires an absolute Socket via filepath.IsAbs,
		// which is platform-dependent: /var/run/dolt.sock is NOT absolute on Windows
		// (no volume). Use a platform-absolute socket path so the same construction
		// path is exercised everywhere.
		socket := "/var/run/dolt.sock"
		if runtime.GOOS == "windows" {
			socket = filepath.Join(t.TempDir(), "dolt.sock")
		}
		srv, err := newDatabaseServer(
			proxy.BackendExternal,
			"", "", "", "", "",
			configfile.ExternalDoltConfig{Socket: socket},
		)
		require.NoError(t, err)
		require.NotNil(t, srv)
		_, ok := srv.(*server.ExternalDoltServer)
		assert.True(t, ok)
	})
}

// TestDbProxyChildProxyOpts_TraceConnectionsFollowsDebug pins the only wiring
// that turns proxy.log's per-connection lines back on: db-proxy-child sets
// TraceConnections from debug.Enabled(), i.e. the BD_DEBUG it inherits.
func TestDbProxyChildProxyOpts_TraceConnectionsFollowsDebug(t *testing.T) {
	t.Cleanup(func() { debug.SetVerbose(false) })
	debug.SetVerbose(true)
	assert.True(t, dbProxyChildProxyOpts(nil).TraceConnections, "debug on must trace connections")
	debug.SetVerbose(false)
	// False unless BD_DEBUG is set in the test's own environment.
	assert.Equal(t, debug.Enabled(), dbProxyChildProxyOpts(nil).TraceConnections, "TraceConnections must follow debug.Enabled()")
}

func TestNewDatabaseServer_BackendLocalSharedServerStillStubbed(t *testing.T) {
	_, err := newDatabaseServer(
		proxy.BackendLocalSharedServer,
		"/tmp/root", "/tmp/cfg", "/tmp/log", "/usr/bin/dolt", "",
		configfile.ExternalDoltConfig{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestNewDatabaseServer_UnknownBackendRejected(t *testing.T) {
	_, err := newDatabaseServer(
		proxy.Backend("bogus"),
		"", "", "", "", "",
		configfile.ExternalDoltConfig{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown backend")
}

func TestDbProxyChildRegistersExternalFlags(t *testing.T) {
	cases := []struct {
		name        string
		defaultText string
	}{
		{"external-host", ""},
		{"external-port", "0"},
		{"external-socket-path", ""},
		{"external-keep-alive", "0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := dbProxyChildCmd.Flags().Lookup(tc.name)
			require.NotNil(t, f, "db-proxy-child does not register --%s", tc.name)
			assert.Equal(t, tc.defaultText, f.DefValue, "--%s default", tc.name)
		})
	}
}
