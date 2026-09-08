package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProxiedServerClientInfo(t *testing.T) {
	t.Run("all empty returns nil", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("", "", "", nil)
		require.NoError(t, err)
		assert.Nil(t, info)
	})

	t.Run("absolute paths pass through cleaned", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("/var/lib/beads/proxieddb", "/etc/dolt/server.yaml", "/var/log/server.log", nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "/var/lib/beads/proxieddb", info.RootPath)
		assert.Equal(t, "/etc/dolt/server.yaml", info.ConfigPath)
		assert.Equal(t, "/var/log/server.log", info.LogPath)
		assert.Nil(t, info.External)
	})

	t.Run("filepath.Clean normalizes redundant separators and . segments", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("/var/lib//beads/./proxieddb", "", "", nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "/var/lib/beads/proxieddb", info.RootPath)
	})

	t.Run("mixed absolute + empty", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("/var/lib/beads/proxieddb", "", "/var/log/server.log", nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "/var/lib/beads/proxieddb", info.RootPath)
		assert.Equal(t, "", info.ConfigPath)
		assert.Equal(t, "/var/log/server.log", info.LogPath)
	})

	t.Run("relative root path is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("alt-root", "", "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not absolute")
	})

	t.Run("relative config path is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("", "configs/server.yaml", "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not absolute")
	})

	t.Run("relative log path is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("", "", "logs/server.log", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not absolute")
	})

	t.Run("absolute paths survive a round-trip through the sidecar resolver", func(t *testing.T) {
		const beadsDir = "/proj/.beads"
		info, err := buildProxiedServerClientInfo("/var/lib/beads/proxieddb", "", "", nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, info.RootPath, (&configfile.ProxiedServerClientInfo{RootPath: info.RootPath}).ResolvedRootPath(beadsDir))
	})

	t.Run("external config alone populates External section", func(t *testing.T) {
		ext := &configfile.ExternalDoltConfig{Host: "db.internal", Port: 3306}
		info, err := buildProxiedServerClientInfo("", "", "", ext)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Empty(t, info.RootPath)
		assert.Empty(t, info.ConfigPath)
		assert.Empty(t, info.LogPath)
		require.NotNil(t, info.External)
		assert.Equal(t, "db.internal", info.External.Host)
		assert.Equal(t, 3306, info.External.Port)
	})

	t.Run("external tls config flows through", func(t *testing.T) {
		ext := &configfile.ExternalDoltConfig{
			Host:        "hosted-dolt.example.com",
			Port:        3306,
			TLSRequired: true,
			TLSCert:     "/etc/beads/client.pem",
			TLSKey:      "/etc/beads/client.key",
		}
		info, err := buildProxiedServerClientInfo("", "", "", ext)
		require.NoError(t, err)
		require.NotNil(t, info.External)
		assert.True(t, info.External.TLSRequired)
		assert.Equal(t, "/etc/beads/client.pem", info.External.TLSCert)
		assert.Equal(t, "/etc/beads/client.key", info.External.TLSKey)
	})

	t.Run("external unix socket config flows through", func(t *testing.T) {
		ext := &configfile.ExternalDoltConfig{Socket: "/var/run/dolt.sock"}
		info, err := buildProxiedServerClientInfo("", "", "", ext)
		require.NoError(t, err)
		require.NotNil(t, info.External)
		assert.Equal(t, "/var/run/dolt.sock", info.External.Socket)
		assert.Empty(t, info.External.Host)
		assert.Zero(t, info.External.Port)
	})

	t.Run("invalid external config is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("", "", "", &configfile.ExternalDoltConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ExternalDoltConfig")
	})

	t.Run("invalid external config with tls cert without key is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("", "", "", &configfile.ExternalDoltConfig{
			Host:    "db",
			Port:    3306,
			TLSCert: "/etc/beads/client.pem",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TLSCert set without TLSKey")
	})

	t.Run("external survives round-trip via SaveProxiedServerClientInfo", func(t *testing.T) {
		dir := t.TempDir()
		ext := &configfile.ExternalDoltConfig{Host: "db.internal", Port: 3306, TLSRequired: true}
		info, err := buildProxiedServerClientInfo("", "", "", ext)
		require.NoError(t, err)
		require.NotNil(t, info)
		require.NoError(t, configfile.SaveProxiedServerClientInfo(dir, info))
		loaded, err := configfile.LoadProxiedServerClientInfo(dir)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		require.NotNil(t, loaded.External)
		assert.Equal(t, "db.internal", loaded.External.Host)
		assert.Equal(t, 3306, loaded.External.Port)
		assert.True(t, loaded.External.TLSRequired)
	})
}
