package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProxiedServerUOWProvider_RoutesExternalConfigToExternalProvider(t *testing.T) {
	beadsDir := t.TempDir()
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		External: &configfile.ExternalDoltConfig{
			Host: "db.invalid",
		},
	}))

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Host requires Port",
		"expected external validation error proving the external code path was taken; got: %v", err)
}

func TestNewExternalProxiedServerUOWProvider_CreatesRootDir(t *testing.T) {
	beadsDir := t.TempDir()
	external := &configfile.ExternalDoltConfig{Host: "db.invalid"}

	_, err := newExternalProxiedServerUOWProvider(context.Background(), beadsDir, "beads_test", external, 0, 0)
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

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir)
	require.Error(t, err, "invalid external config must surface a validation error")

	assert.DirExists(t, customRoot, "external provider should create the custom root dir, not the default")
	assert.NoDirExists(t, proxiedServerRoot(beadsDir), "default root must not be created when a custom RootPath is set")
}

// TestDoltArchiveLevelSupported pins the gastownhall/beads#4986 fail-closed
// contract: a clean version at or above MinDoltVersionForArchiveLevelConfig
// supports archive_level, a clean version below it does not, and — the
// regression this test was added to catch — a *prerelease* build of the
// floor version must also fail closed, even though
// doltversion.Version.AtLeast itself ignores Prerelease for the separate
// warn-only RecommendedMin comparison. Before this fix,
// doltArchiveLevelSupported delegated straight to AtLeast and so silently
// flipped fail-open for "1.52.1-rc1", reopening #4986: an old build with
// that suffix satisfies ">= 1.52.1" under AtLeast alone, but is not
// guaranteed to have the config field just because its numeric segments
// match the floor.
func TestDoltArchiveLevelSupported(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{name: "above floor", version: "2.2.2", want: true},
		{name: "at floor", version: "1.52.1", want: true},
		{name: "below floor", version: "1.40.0", want: false},
		{
			name:    "prerelease at floor fails closed",
			version: "1.52.1-rc1",
			want:    false,
		},
		{
			name:    "prerelease above floor fails closed",
			version: "2.0.0-beta1",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := doltversion.Identity{Version: doltversion.MustParse(tt.version)}
			got := doltArchiveLevelSupported(id)
			assert.Equal(t, tt.want, got, "doltArchiveLevelSupported(%q)", tt.version)
		})
	}
}

// TestDoltArchiveLevelSupported_UnparsedVersionFailsClosed covers the
// zero-value Version case (Probe returned ErrUnparseableVersion, demoted to
// a warning by ProbeWithPolicy): no Segments means the floor comparison
// cannot be trusted, so this must fail closed too.
func TestDoltArchiveLevelSupported_UnparsedVersionFailsClosed(t *testing.T) {
	assert.False(t, doltArchiveLevelSupported(doltversion.Identity{}))
}

func TestNewExternalProxiedServerUOWProvider_HonorsCustomLogPath(t *testing.T) {
	beadsDir := t.TempDir()
	customLogDir := t.TempDir()
	customLog := filepath.Join(customLogDir, "external.log")

	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		LogPath:  customLog,
		External: &configfile.ExternalDoltConfig{Host: "db.invalid"},
	}))

	_, err := newProxiedServerUOWProvider(context.Background(), beadsDir)
	require.Error(t, err, "invalid external config must surface a validation error")
	assert.Contains(t, err.Error(), "Host requires Port",
		"external code path must be the one reached; got: %v", err)
}
