package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveProxiedServerRootPath_SharedProxyMode proves the opt-in shared
// proxy mode: with BEADS_SHARED_PROXY set, every scope resolves to one
// machine-wide rootDir (so they collapse onto a single db-proxy-child); without
// it, each scope keeps its own per-scope proxieddb (live behavior, unchanged).
func TestResolveProxiedServerRootPath_SharedProxyMode(t *testing.T) {
	sharedHome := t.TempDir()
	// Hermetic: neutralize any ambient proxied/shared overrides from the host.
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedHome)
	t.Setenv("BEADS_SHARED_PROXY_ROOT_PATH", "")
	t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")

	t.Run("off by default keeps per-scope proxieddb", func(t *testing.T) {
		t.Setenv("BEADS_SHARED_PROXY", "")
		beadsDir := t.TempDir()
		got, err := resolveProxiedServerRootPath(beadsDir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(beadsDir, proxiedServerRootName), got)
	})

	t.Run("on resolves every scope to one shared rootDir", func(t *testing.T) {
		t.Setenv("BEADS_SHARED_PROXY", "1")
		want := filepath.Join(sharedHome, "proxy")

		a, err := resolveProxiedServerRootPath(t.TempDir())
		require.NoError(t, err)
		b, err := resolveProxiedServerRootPath(t.TempDir())
		require.NoError(t, err)

		assert.Equal(t, want, a, "shared mode must resolve to SharedProxyRootDir")
		assert.Equal(t, a, b, "shared mode must resolve all scopes to one rootDir")
	})
}
