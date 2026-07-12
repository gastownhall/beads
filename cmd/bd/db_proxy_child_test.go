package main

import (
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
		srv, err := newDatabaseServer(
			proxy.BackendExternal,
			"", "", "", "", "",
			configfile.ExternalDoltConfig{Socket: "/var/run/dolt.sock"},
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
		{"external-tls", "false"},
		{"external-tls-cert-path", ""},
		{"external-tls-key-path", ""},
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
