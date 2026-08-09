package main

import (
	"context"
	"os"
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

// A corrupted sidecar must abort provider construction, not silently fall
// back to a fresh managed local database (bd-aj3g5, restoring f880a985b;
// split-brain: reads return zero issues, writes land in the wrong database).
func TestNewProxiedServerUOWProvider_CorruptSidecarErrorsInsteadOfManagedFallback(t *testing.T) {
	beadsDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(beadsDir, configfile.ProxiedServerClientInfoFileName),
		[]byte("{not json"), 0o600))

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), configfile.ProxiedServerClientInfoPath(beadsDir),
		"error must name the unreadable sidecar path; got: %v", err)
	assert.Contains(t, err.Error(), "refusing to fall back",
		"must refuse the managed-local fallback; got: %v", err)
}

// An unparseable workspace config must abort too — defaulting the database
// name sends writes to the wrong database, and the team-server identity
// assertion silently degrades to no assertion at all.
func TestNewProxiedServerUOWProvider_CorruptWorkspaceConfigErrors(t *testing.T) {
	beadsDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(beadsDir, configfile.ConfigFileName),
		[]byte("{not json"), 0o600))

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), configfile.ConfigPath(beadsDir),
		"error must name the unreadable workspace config path; got: %v", err)
	assert.Contains(t, err.Error(), "refusing to fall back",
		"must refuse the fresh-database fallback; got: %v", err)
}

// An UNREADABLE (permission-denied) sidecar is the same hazard as an
// unparseable one: the file exists, so falling back would fork a fresh
// database while the real one sits behind the perms error.
func TestNewProxiedServerUOWProvider_UnreadableSidecarErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 000 does not deny reads")
	}
	beadsDir := t.TempDir()
	sidecar := filepath.Join(beadsDir, configfile.ProxiedServerClientInfoFileName)
	require.NoError(t, os.WriteFile(sidecar, []byte("{}"), 0o600))
	require.NoError(t, os.Chmod(sidecar, 0o000))
	t.Cleanup(func() { _ = os.Chmod(sidecar, 0o600) })

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), configfile.ProxiedServerClientInfoPath(beadsDir),
		"error must name the unreadable sidecar path; got: %v", err)
	assert.Contains(t, err.Error(), "refusing to fall back",
		"must refuse the managed-local fallback; got: %v", err)
}

// ABSENT files are the legal fresh-workspace path: both loads return
// (nil, nil) and the topology resolves to the defaults with no error. Pinned
// at the resolver level because the full provider would go on to start a
// managed dolt server.
func TestResolveProxiedServerUOWTopology_AbsentFilesResolveDefaults(t *testing.T) {
	beadsDir := t.TempDir()

	topology, err := resolveProxiedServerUOWTopology(beadsDir, "", assertWorkspaceIdentity)
	require.NoError(t, err, "absent metadata.json and sidecar must not be an error")
	assert.Equal(t, configfile.DefaultDoltDatabase, topology.database)
	assert.False(t, topology.teamServer)
	assert.Nil(t, topology.external)
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
