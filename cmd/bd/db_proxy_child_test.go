package main

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
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

// TestDbProxyChildIdleTimeoutHelpAgreesWithInit guards AC3: a reader of
// `bd db-proxy-child --help` alone must not conclude that 0 is a safe
// default. init's --proxied-server-idle-timeout already documents the
// three-way contract (omit -> 30s default; explicit 0 -> never; positive ->
// that duration); db-proxy-child's own --idle-timeout must state the same
// contract rather than describing 0/negative in isolation.
func TestDbProxyChildIdleTimeoutHelpAgreesWithInit(t *testing.T) {
	childFlag := dbProxyChildCmd.Flags().Lookup("idle-timeout")
	require.NotNil(t, childFlag, "db-proxy-child does not register --idle-timeout")

	initFlag := initCmd.Flags().Lookup("proxied-server-idle-timeout")
	require.NotNil(t, initFlag, "init does not register --proxied-server-idle-timeout")
	require.Contains(t, initFlag.Usage, "30s", "precondition: init's help is expected to already document the 30s default")

	assert.Contains(t, childFlag.Usage, "30s",
		"db-proxy-child's --idle-timeout help must mention the 30s default applied upstream, before this flag is ever set to 0")
	assert.Contains(t, childFlag.Usage, "never",
		"db-proxy-child's --idle-timeout help must explicitly say 0 means never, matching init's contract")
}
