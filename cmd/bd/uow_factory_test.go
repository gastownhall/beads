package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreviewProviderOptions pins the CLI-side wiring the reviewer flagged as
// untested: the root pre-run turns previewMode into providerOpts by calling
// this function, and a refactor that dropped or inverted that could not fail
// any existing test. uow.providerOptions is unexported, so this cannot poke
// inside the returned uow.ProviderOption values (see
// internal/storage/uow/preview_provider_test.go's TestApplyProviderOptions for
// the same-package introspection); it instead pins the one thing an external
// caller can observe — that preview=true yields exactly one option and
// preview=false yields none — which is what the CLI wiring is responsible for.
func TestPreviewProviderOptions(t *testing.T) {
	if got := previewProviderOptions(false); got != nil {
		t.Fatalf("previewProviderOptions(false) = %#v, want nil", got)
	}

	opts := previewProviderOptions(true)
	if len(opts) != 1 {
		t.Fatalf("previewProviderOptions(true) len = %d, want 1", len(opts))
	}
	if opts[0] == nil {
		t.Fatal("previewProviderOptions(true)[0] is nil, want uow.WithPreview()")
	}
}

func TestNewProxiedServerUOWProvider_RoutesExternalConfigToExternalProvider(t *testing.T) {
	beadsDir := t.TempDir()
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		External: &configfile.ExternalDoltConfig{
			Host: "db.invalid",
		},
	}))

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Host requires Port",
		"expected external validation error proving the external code path was taken; got: %v", err)
}

func TestNewExternalProxiedServerUOWProvider_CreatesRootDir(t *testing.T) {
	beadsDir := t.TempDir()
	external := &configfile.ExternalDoltConfig{Host: "db.invalid"}

	_, err := newExternalProxiedServerUOWProvider(context.Background(), beadsDir, sqlServerUOWTopology{
		database: "beads_test",
		external: external,
	})
	require.Error(t, err, "invalid external config must surface a validation error")

	wantRoot := proxiedServerRoot(beadsDir)
	assert.DirExists(t, wantRoot, "external provider should create the proxied server root dir before validating")
}

func TestNewExternalProxiedServerUOWProvider_HonorsCustomRootPath(t *testing.T) {
	beadsDir := t.TempDir()
	customRoot := filepath.Join(t.TempDir(), "custom-proxy-root")

	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		RootPath: customRoot,
		External: &configfile.ExternalDoltConfig{Host: "db.invalid"},
	}))

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir, "")
	require.Error(t, err, "invalid external config must surface a validation error")

	assert.DirExists(t, customRoot, "external provider should create the custom root dir, not the default")
	assert.NoDirExists(t, proxiedServerRoot(beadsDir), "default root must not be created when a custom RootPath is set")
}

func TestNewExternalProxiedServerUOWProvider_HonorsCustomLogPath(t *testing.T) {
	beadsDir := t.TempDir()
	customLogDir := t.TempDir()
	customLog := filepath.Join(customLogDir, "external.log")

	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		LogPath:  customLog,
		External: &configfile.ExternalDoltConfig{Host: "db.invalid"},
	}))

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir, "")
	require.Error(t, err, "invalid external config must surface a validation error")
	assert.Contains(t, err.Error(), "Host requires Port",
		"external code path must be the one reached; got: %v", err)
}
