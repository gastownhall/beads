package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRootProviderOptions pins the CLI-side wiring the reviewer flagged as
// untested: the root pre-run turns its previewMode/useReadOnly classification
// into providerOpts by calling this function, and a refactor that dropped or
// inverted that could not fail any existing test. uow.providerOptions is
// unexported, so this cannot poke inside the returned uow.ProviderOption
// values (see internal/storage/uow/preview_provider_test.go's
// TestApplyProviderOptions for the same-package introspection); it instead
// pins what an external caller can observe — how many options each posture
// yields — which is what the CLI wiring is responsible for.
func TestRootProviderOptions(t *testing.T) {
	for _, tt := range []struct {
		name              string
		preview, readOnly bool
		want              int
	}{
		{name: "ordinary write open", want: 0},
		{name: "preview", preview: true, want: 1},
		{name: "read-only", readOnly: true, want: 1},
		// Preview is the stronger posture and must win: it neither creates nor
		// migrates, while read-only opens normally.
		{name: "preview wins over read-only", preview: true, readOnly: true, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := rootProviderOptions(tt.preview, tt.readOnly)
			if len(opts) != tt.want {
				t.Fatalf("rootProviderOptions(%t, %t) len = %d, want %d", tt.preview, tt.readOnly, len(opts), tt.want)
			}
			for i, o := range opts {
				if o == nil {
					t.Fatalf("rootProviderOptions(%t, %t)[%d] is nil", tt.preview, tt.readOnly, i)
				}
			}
		})
	}
}

// TestProxiedProviderOptions pins the proxied-only half of the open posture:
// every proxied open gains exactly one option on top of the caller's, the
// proxied-server marker the data-behind refusal reads to say where `bd dolt
// pull` can be run. Dropping that append would put a proxied workspace back on
// the runnable-here remedy its own front door refuses. As with
// TestRootProviderOptions, the options are opaque from here: uow's
// TestApplyProviderOptions pins what WithProxiedServerMode sets, and
// TestNewDoltServerUOWProvider_CarriesProxiedServerMode that the open carries
// it to the gate.
func TestProxiedProviderOptions(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   []uow.ProviderOption
	}{
		{name: "no caller options"},
		{name: "preview", in: []uow.ProviderOption{uow.WithPreview()}},
		{name: "read-only", in: []uow.ProviderOption{uow.WithReadOnly()}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := proxiedProviderOptions(tt.in)
			if len(out) != len(tt.in)+1 {
				t.Fatalf("proxiedProviderOptions(%d options) len = %d, want %d", len(tt.in), len(out), len(tt.in)+1)
			}
			for i, o := range out {
				if o == nil {
					t.Fatalf("proxiedProviderOptions(%d options)[%d] is nil", len(tt.in), i)
				}
			}
		})
	}

	// The append must not land in the caller's backing array: with spare
	// capacity an in-place append would write the marker into a slice the
	// caller still owns.
	in := make([]uow.ProviderOption, 1, 4)
	in[0] = uow.WithPreview()
	out := proxiedProviderOptions(in)
	if &out[0] == &in[0] {
		t.Fatal("proxiedProviderOptions returned the caller's backing array")
	}
	if in[:cap(in)][1] != nil {
		t.Fatal("proxiedProviderOptions wrote into the caller's spare capacity")
	}
}

// TestServerLogProviderOptions pins the ownership half of the proxy-start
// rotation: an open of bd's default server log gains exactly one option on top
// of the caller's, and an open of a log the operator chose gains none, so bd
// never renames a log that proxiedLogAssets does not retire on migration. As
// with TestProxiedProviderOptions, the options are opaque from here: uow's
// TestApplyProviderOptions pins what WithServerLogRotation sets, and proxy's
// TestForkExecChild_RotatesOversizedLogs what RotateLogFile does.
func TestServerLogProviderOptions(t *testing.T) {
	for _, tt := range []struct {
		name        string
		in          []uow.ProviderOption
		isCustomLog bool
		want        int
	}{
		{name: "default log", want: 1},
		{name: "default log, read-only", in: []uow.ProviderOption{uow.WithReadOnly()}, want: 2},
		{name: "custom log", isCustomLog: true, want: 0},
		{name: "custom log, read-only", in: []uow.ProviderOption{uow.WithReadOnly()}, isCustomLog: true, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := serverLogProviderOptions(tt.in, tt.isCustomLog)
			if len(out) != tt.want {
				t.Fatalf("serverLogProviderOptions(%d options, custom=%t) len = %d, want %d", len(tt.in), tt.isCustomLog, len(out), tt.want)
			}
			for i, o := range out {
				if o == nil {
					t.Fatalf("serverLogProviderOptions(%d options, custom=%t)[%d] is nil", len(tt.in), tt.isCustomLog, i)
				}
			}
		})
	}

	// The append must not land in the caller's backing array, as in
	// TestProxiedProviderOptions.
	in := make([]uow.ProviderOption, 1, 4)
	in[0] = uow.WithReadOnly()
	out := serverLogProviderOptions(in, false)
	if &out[0] == &in[0] {
		t.Fatal("serverLogProviderOptions returned the caller's backing array")
	}
	if in[:cap(in)][1] != nil {
		t.Fatal("serverLogProviderOptions wrote into the caller's spare capacity")
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

// The sidecar's External.AllowCleartextPassword has to reach the topology
// unchanged — this is the setting ExternalDoltConfig.Validate actually
// enforces on bd's connection, per its own doc comment on the field.
func TestResolveProxiedServerUOWTopology_PlumbsAllowCleartextPassword(t *testing.T) {
	beadsDir := t.TempDir()
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		External: &configfile.ExternalDoltConfig{
			Host:                   "db.invalid",
			Port:                   3306,
			TLSRequired:            true,
			TLSServerName:          "db.invalid",
			AllowCleartextPassword: true,
		},
	}))

	topology, err := resolveProxiedServerUOWTopology(beadsDir, "", assertWorkspaceIdentity)
	require.NoError(t, err)
	require.NotNil(t, topology.external)
	assert.True(t, topology.external.AllowCleartextPassword,
		"the sidecar's External.AllowCleartextPassword must reach the topology unchanged")
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

// TestResolveAndProbeDoltRefusesBinaryBelowRequiredMin pins the managed
// proxied-server floor: bd init --proxied-server's preflight and every
// managed proxied-server open go through resolveAndProbeDolt, so a dolt
// older than doltversion.RequiredMin must come back as an error that names
// the binary, its version and the minimum, before bd launches it.
func TestResolveAndProbeDoltRefusesBinaryBelowRequiredMin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script dolt stub")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))

	write := func(name, version string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho 'dolt version "+version+"'\n"), 0o755))
		return path
	}

	old := write("dolt-old", "2.5.1")
	t.Setenv(doltversion.DoltBinEnvVar, old)
	_, err := resolveAndProbeDolt(context.Background(), "test", true)
	require.ErrorIs(t, err, doltversion.ErrBelowRequiredMin)
	// The refusal quotes the path with %q, which escapes a backslash (in
	// TMPDIR, say), so match it quoted.
	for _, want := range []string{fmt.Sprintf("%q", old), "2.5.1", "requires dolt " + doltversion.RequiredMin.String(), "Upgrade dolt"} {
		assert.Contains(t, err.Error(), want)
	}

	current := write("dolt-current", doltversion.RequiredMin.String())
	t.Setenv(doltversion.DoltBinEnvVar, current)
	got, err := resolveAndProbeDolt(context.Background(), "test", true)
	require.NoError(t, err)
	assert.Equal(t, current, got)
}
