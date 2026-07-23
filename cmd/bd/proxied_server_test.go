package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
	"github.com/dolthub/dolt/go/libraries/utils/filesys"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// renderManagedConfigWithAutoGCBehavior builds a Beads-managed config.yaml
// (marker included) whose auto_gc_behavior block is exactly gc — unlike
// renderProxiedServerConfig, which only ever sets ArchiveLevel_. Used to
// seed the sibling-field-preservation tests below (a user-set "enable"
// alongside — or in place of — archive_level).
func renderManagedConfigWithAutoGCBehavior(t *testing.T, port int, gc *servercfg.AutoGCBehaviorYAMLConfig) []byte {
	t.Helper()
	host := proxiedServerListenerHost
	logLevel := string(servercfg.LogLevel_Info)
	yc := &servercfg.YAMLConfig{
		LogLevelStr: &logLevel,
		ListenerConfig: servercfg.ListenerYAMLConfig{
			HostStr:    &host,
			PortNumber: &port,
		},
		BehaviorConfig: servercfg.BehaviorYAMLConfig{
			AutoGCBehavior: gc,
		},
	}
	body, err := yaml.Marshal(yc)
	require.NoError(t, err)
	return append([]byte(managedProxiedServerConfigMarker), body...)
}

func TestRenderProxiedServerConfig_RoundTrips(t *testing.T) {
	body, err := renderProxiedServerConfig(54321, true)
	require.NoError(t, err)

	cfg, err := servercfg.NewYamlConfig(body)
	require.NoError(t, err)

	assert.Equal(t, proxiedServerListenerHost, cfg.Host(), "Host mismatch")
	assert.Equal(t, 54321, cfg.Port(), "Port mismatch")
	assert.Equal(t, servercfg.LogLevel_Info, cfg.LogLevel(), "LogLevel mismatch")
}

// TestRenderProxiedServerConfig_ArchiveLevel covers the actual Snappy-GC fix
// (gastownhall/beads#4986): when archiveLevelSupported is true, the
// generated config sets auto_gc_behavior.archive_level: 0 with auto-GC left
// enabled; when false, the key is omitted entirely so an older external
// dolt's yaml.UnmarshalStrict loader never sees an unrecognized key.
func TestRenderProxiedServerConfig_ArchiveLevel(t *testing.T) {
	t.Run("supported: archive_level 0, auto-GC enabled", func(t *testing.T) {
		body, err := renderProxiedServerConfig(54321, true)
		require.NoError(t, err)
		assert.Contains(t, string(body), "archive_level: 0")

		cfg, err := servercfg.NewYamlConfig(body)
		require.NoError(t, err)
		gc := cfg.AutoGCBehavior()
		require.NotNil(t, gc, "AutoGCBehavior must be set when archiveLevelSupported is true")
		assert.Equal(t, 0, gc.ArchiveLevel())
		assert.True(t, gc.Enable(), "auto-GC must remain enabled")
	})

	t.Run("unsupported: no auto_gc_behavior key at all", func(t *testing.T) {
		body, err := renderProxiedServerConfig(54321, false)
		require.NoError(t, err)
		assert.NotContains(t, string(body), "archive_level")
		assert.NotContains(t, string(body), "auto_gc_behavior")

		cfg, err := servercfg.NewYamlConfig(body)
		require.NoError(t, err)
		assert.Nil(t, cfg.AutoGCBehavior())
	})
}

func TestEnsureProxiedServerConfig_CreatesAndIsIdempotent(t *testing.T) {
	beadsDir := t.TempDir()

	path1, err := ensureProxiedServerConfig(beadsDir, true)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(beadsDir, "dolt", "config.yaml"), path1)

	body1, err := os.ReadFile(path1)
	require.NoError(t, err)
	require.NotEmpty(t, body1)
	require.True(t, strings.Contains(string(body1), proxiedServerListenerHost))

	// Second call must NOT rewrite — running daemon is bound to the existing port.
	path2, err := ensureProxiedServerConfig(beadsDir, true)
	require.NoError(t, err)
	assert.Equal(t, path1, path2)

	body2, err := os.ReadFile(path2)
	require.NoError(t, err)
	assert.Equal(t, body1, body2, "second call must not rewrite the file")

	// Round-trip: dolt's own loader must accept what we wrote.
	loaded, err := servercfg.YamlConfigFromFile(filesys.LocalFS, path2)
	require.NoError(t, err)
	assert.Equal(t, proxiedServerListenerHost, loaded.Host())
	assert.Greater(t, loaded.Port(), 0)
}

func TestEnsureProxiedServerConfig_ReusesExistingConfig(t *testing.T) {
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	require.NoError(t, os.MkdirAll(root, 0o755))

	existing, err := renderProxiedServerConfig(45678, true)
	require.NoError(t, err)
	cfgPath := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, existing, 0o600))

	path, err := ensureProxiedServerConfig(beadsDir, true)
	require.NoError(t, err)
	assert.Equal(t, cfgPath, path)

	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, existing, got, "existing config.yaml must be reused unchanged")
}

// TestIsManagedProxiedServerConfig covers the marker-detection helper in
// isolation from any file I/O.
func TestIsManagedProxiedServerConfig(t *testing.T) {
	managed, err := renderProxiedServerConfig(1234, true)
	require.NoError(t, err)
	assert.True(t, isManagedProxiedServerConfig(managed))

	assert.False(t, isManagedProxiedServerConfig([]byte("listener:\n  port: 1234\n")))
	assert.False(t, isManagedProxiedServerConfig(nil))
}

// TestReconcileManagedProxiedServerConfig_AddsKey covers the (a) direction
// (gastownhall/beads#4986 round 2 major 2): a Beads-managed config that
// predates archive_level support gains the key once the resolved dolt
// supports it, with every other field (including port) preserved via the
// servercfg struct round trip.
func TestReconcileManagedProxiedServerConfig_AddsKey(t *testing.T) {
	before, err := renderProxiedServerConfig(45678, false)
	require.NoError(t, err)
	require.NotContains(t, string(before), "archive_level")

	after, changed, err := reconcileManagedProxiedServerConfig(before, true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.True(t, isManagedProxiedServerConfig(after), "marker must survive reconcile")

	cfg, err := servercfg.NewYamlConfig(after)
	require.NoError(t, err)
	assert.Equal(t, 45678, cfg.Port(), "port must be preserved")
	gc := cfg.AutoGCBehavior()
	require.NotNil(t, gc)
	assert.Equal(t, 0, gc.ArchiveLevel())
	assert.True(t, gc.Enable())
}

// TestReconcileManagedProxiedServerConfig_StripsKey covers the (b)
// direction: a Beads-managed config written with archive_level set loses
// the key when the resolved dolt no longer supports it (e.g. a downgrade),
// so a later yaml.UnmarshalStrict on that older dolt cannot hard-fail.
func TestReconcileManagedProxiedServerConfig_StripsKey(t *testing.T) {
	before, err := renderProxiedServerConfig(45678, true)
	require.NoError(t, err)
	require.Contains(t, string(before), "archive_level")

	after, changed, err := reconcileManagedProxiedServerConfig(before, false)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotContains(t, string(after), "archive_level")
	assert.NotContains(t, string(after), "auto_gc_behavior")

	cfg, err := servercfg.NewYamlConfig(after)
	require.NoError(t, err)
	assert.Equal(t, 45678, cfg.Port(), "port must be preserved")
	assert.Nil(t, cfg.AutoGCBehavior())
}

// TestReconcileManagedProxiedServerConfig_NoOpWhenAlreadyAligned asserts
// reconcile reports changed=false (and ensureProxiedServerConfig therefore
// skips the rewrite) when the config already matches the probe result —
// the steady-state case on every ordinary bd invocation.
func TestReconcileManagedProxiedServerConfig_NoOpWhenAlreadyAligned(t *testing.T) {
	t.Run("already has key, still supported", func(t *testing.T) {
		body, err := renderProxiedServerConfig(1, true)
		require.NoError(t, err)
		after, changed, err := reconcileManagedProxiedServerConfig(body, true)
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, body, after)
	})
	t.Run("already lacks key, still unsupported", func(t *testing.T) {
		body, err := renderProxiedServerConfig(1, false)
		require.NoError(t, err)
		after, changed, err := reconcileManagedProxiedServerConfig(body, false)
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, body, after)
	})
}

// TestReconcileManagedProxiedServerConfig_PreservesSiblingFields covers the
// round-3 verification finding (gastownhall/beads#4986): reconcile must
// inspect and modify only ArchiveLevel_, not treat "the whole
// auto_gc_behavior block is present" as equivalent to "archive_level is
// set". A block carrying only a user-set "enable" (no archive_level) must
// both gain archive_level when supported, and keep its "enable" setting
// when the (now-absent-again) archive_level is later stripped on an
// unsupported binary — the earlier (buggy) whole-block check would have
// either left archive_level unset in the first case, or discarded "enable"
// entirely in the second.
func TestReconcileManagedProxiedServerConfig_PreservesSiblingFields(t *testing.T) {
	enable := true

	t.Run("block with only enable gains archive_level; enable preserved", func(t *testing.T) {
		before := renderManagedConfigWithAutoGCBehavior(t, 45678, &servercfg.AutoGCBehaviorYAMLConfig{
			Enable_: &enable,
		})
		require.Contains(t, string(before), "enable: true")
		require.NotContains(t, string(before), "archive_level")

		after, changed, err := reconcileManagedProxiedServerConfig(before, true)
		require.NoError(t, err)
		assert.True(t, changed)

		cfg, err := servercfg.NewYamlConfig(after)
		require.NoError(t, err)
		gc := cfg.AutoGCBehavior()
		require.NotNil(t, gc)
		assert.Equal(t, 0, gc.ArchiveLevel())
		assert.True(t, gc.Enable(), "enable must be preserved, not defaulted/lost")
	})

	t.Run("block with only enable on unsupported binary is untouched", func(t *testing.T) {
		before := renderManagedConfigWithAutoGCBehavior(t, 45678, &servercfg.AutoGCBehaviorYAMLConfig{
			Enable_: &enable,
		})

		after, changed, err := reconcileManagedProxiedServerConfig(before, false)
		require.NoError(t, err)
		assert.False(t, changed, "nothing to strip when archive_level was never set")
		assert.Equal(t, before, after)
	})

	t.Run("block with both on unsupported binary keeps enable, loses archive_level", func(t *testing.T) {
		archiveLevel := 0
		before := renderManagedConfigWithAutoGCBehavior(t, 45678, &servercfg.AutoGCBehaviorYAMLConfig{
			Enable_:       &enable,
			ArchiveLevel_: &archiveLevel,
		})
		require.Contains(t, string(before), "enable: true")
		require.Contains(t, string(before), "archive_level: 0")

		after, changed, err := reconcileManagedProxiedServerConfig(before, false)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.NotContains(t, string(after), "archive_level")

		cfg, err := servercfg.NewYamlConfig(after)
		require.NoError(t, err)
		gc := cfg.AutoGCBehavior()
		require.NotNil(t, gc, "block must survive since enable is still set")
		assert.True(t, gc.Enable(), "enable must be preserved when only archive_level is stripped")
	})
}

// TestEnsureProxiedServerConfig_ManagedWithoutKeyGainsIt is the
// ensureProxiedServerConfig-level counterpart to
// TestReconcileManagedProxiedServerConfig_AddsKey: an upgraded install
// whose config.yaml predates this fix gets archive_level added in place on
// the very next command, without a manual regenerate.
func TestEnsureProxiedServerConfig_ManagedWithoutKeyGainsIt(t *testing.T) {
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	require.NoError(t, os.MkdirAll(root, 0o755))
	cfgPath := filepath.Join(root, "config.yaml")

	seed, err := renderProxiedServerConfig(45678, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, seed, 0o600))

	path, err := ensureProxiedServerConfig(beadsDir, true)
	require.NoError(t, err)
	assert.Equal(t, cfgPath, path)

	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Contains(t, string(got), "archive_level: 0")

	cfg, err := servercfg.NewYamlConfig(got)
	require.NoError(t, err)
	assert.Equal(t, 45678, cfg.Port(), "port must survive the rewrite")
}

// TestEnsureProxiedServerConfig_ManagedWithKeyOnOldBinaryLosesIt is the
// ensureProxiedServerConfig-level counterpart to
// TestReconcileManagedProxiedServerConfig_StripsKey: a dolt downgrade after
// a managed config already set archive_level must not hard-fail server
// startup on the next command.
func TestEnsureProxiedServerConfig_ManagedWithKeyOnOldBinaryLosesIt(t *testing.T) {
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	require.NoError(t, os.MkdirAll(root, 0o755))
	cfgPath := filepath.Join(root, "config.yaml")

	seed, err := renderProxiedServerConfig(45678, true)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, seed, 0o600))

	path, err := ensureProxiedServerConfig(beadsDir, false)
	require.NoError(t, err)
	assert.Equal(t, cfgPath, path)

	got, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.NotContains(t, string(got), "archive_level")

	cfg, err := servercfg.NewYamlConfig(got)
	require.NoError(t, err)
	assert.Equal(t, 45678, cfg.Port(), "port must survive the rewrite")
}

// TestEnsureProxiedServerConfig_CustomConfigUntouchedWithWarning asserts a
// genuinely user-owned config (env var / sidecar custom path) is NEVER
// rewritten, and instead gets a one-line stderr warning when it lacks
// archive_level on a capable binary.
func TestEnsureProxiedServerConfig_CustomConfigUntouchedWithWarning(t *testing.T) {
	t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
	bd := t.TempDir()

	customDir := t.TempDir()
	customPath := filepath.Join(customDir, "my-server.yaml")
	before, err := renderProxiedServerConfig(54321, false)
	require.NoError(t, err)
	// Strip the marker: this is meant to look like a genuinely hand-written
	// custom config, not one Beads happened to generate elsewhere.
	before = before[len(managedProxiedServerConfigMarker):]
	require.NoError(t, os.WriteFile(customPath, before, 0o600))

	writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: customPath})

	var got string
	var path string
	got = captureStderr(t, func() {
		path, err = ensureProxiedServerConfig(bd, true)
	})
	require.NoError(t, err)
	assert.Equal(t, customPath, path)

	after, err := os.ReadFile(customPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "custom config must never be rewritten")
	assert.Contains(t, got, "not Beads-managed")
	assert.Contains(t, got, customPath)
}

// TestEnsureProxiedServerConfig_PreMarkerDefaultConfigUntouchedWithWarning
// covers the other unmanaged case: a default-path config.yaml written by a
// Beads version that predates the marker. Same contract as the custom-path
// case — never rewritten, only warned about — since Beads cannot prove it
// owns the file.
func TestEnsureProxiedServerConfig_PreMarkerDefaultConfigUntouchedWithWarning(t *testing.T) {
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	require.NoError(t, os.MkdirAll(root, 0o755))
	cfgPath := filepath.Join(root, "config.yaml")

	seed, err := renderProxiedServerConfig(45678, false)
	require.NoError(t, err)
	seed = seed[len(managedProxiedServerConfigMarker):] // simulate a pre-marker file
	require.NoError(t, os.WriteFile(cfgPath, seed, 0o600))

	var path string
	got := captureStderr(t, func() {
		path, err = ensureProxiedServerConfig(beadsDir, true)
	})
	require.NoError(t, err)
	assert.Equal(t, cfgPath, path)

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, seed, after, "pre-marker default config must never be rewritten")
	assert.Contains(t, got, "not Beads-managed")
}

// TestEnsureProxiedServerConfig_CustomConfigEnvVarInterpolation covers the
// round-3 verification finding (gastownhall/beads#4986): a custom config
// using Dolt's ${VAR}-style environment interpolation (see
// servercfg/env_interpolate.go, applied by YamlConfigFromFile before
// parsing) must be ACCEPTED, not rejected. An earlier version of this
// function validated custom configs via servercfg.NewYamlConfig on raw
// bytes, which skips interpolation entirely and would fail on a valid
// ${VAR} placeholder with a YAML/type error before the server ever starts.
func TestEnsureProxiedServerConfig_CustomConfigEnvVarInterpolation(t *testing.T) {
	t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
	t.Setenv("BEADS_TEST_PROXIED_SERVER_PORT", "54321")
	bd := t.TempDir()

	customDir := t.TempDir()
	customPath := filepath.Join(customDir, "my-server.yaml")
	// listener.port as a raw ${VAR} placeholder — only valid after
	// interpolation; NewYamlConfig on the raw bytes would fail to unmarshal
	// "${BEADS_TEST_PROXIED_SERVER_PORT}" into the int Port field.
	const tmpl = "listener:\n  host: 127.0.0.1\n  port: ${BEADS_TEST_PROXIED_SERVER_PORT}\n"
	require.NoError(t, os.WriteFile(customPath, []byte(tmpl), 0o600))

	writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: customPath})

	path, err := ensureProxiedServerConfig(bd, true)
	require.NoError(t, err, "a valid ${VAR} custom config must not be rejected pre-start")
	assert.Equal(t, customPath, path)

	// The file on disk must still carry the literal, un-interpolated
	// placeholder — ensureProxiedServerConfig never rewrites custom configs.
	after, err := os.ReadFile(customPath)
	require.NoError(t, err)
	assert.Equal(t, tmpl, string(after))
}

// TestEnsureProxiedServerConfig_ManagedFileWithPlaceholderDegradesGracefully
// covers the round-4 verification finding (gastownhall/beads#4986): the
// managedProxiedServerConfigMarker only proves Beads generated a file
// ORIGINALLY, not that an operator hasn't hand-edited it since (e.g. to add
// a ${VAR} placeholder). reconcileManagedProxiedServerConfig's raw-bytes
// parse (deliberately non-interpolating — see its doc comment) then fails.
//
// Verified before this fix: that parse error propagated as a hard error
// out of ensureProxiedServerConfig, which cmd/bd/uow_factory.go's
// newManagedProxiedServerUOWProvider returns directly — aborting the
// ENTIRE bd command before ever attempting to start the managed dolt
// server, even though a real dolt process would happily interpolate and
// start against the same file. This test asserts the fixed behavior:
// degrade to the unmanaged (warn-only, never-rewrite) treatment instead.
func TestEnsureProxiedServerConfig_ManagedFileWithPlaceholderDegradesGracefully(t *testing.T) {
	t.Setenv("BEADS_TEST_PROXIED_SERVER_PORT2", "54321")
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	require.NoError(t, os.MkdirAll(root, 0o755))
	cfgPath := filepath.Join(root, "config.yaml")

	// Marker present (looks Beads-managed) but hand-edited afterward with a
	// ${VAR} placeholder in a field NewYamlConfig cannot unmarshal without
	// interpolation (an int field fed a string).
	seed := managedProxiedServerConfigMarker + "listener:\n  host: 127.0.0.1\n  port: ${BEADS_TEST_PROXIED_SERVER_PORT2}\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(seed), 0o600))

	var path string
	var err error
	got := captureStderr(t, func() {
		path, err = ensureProxiedServerConfig(beadsDir, true)
	})
	require.NoError(t, err, "a managed file that fails the raw parse must not fail the whole command")
	assert.Equal(t, cfgPath, path)
	assert.Contains(t, got, "failed to parse", "must explain why it's degrading to unmanaged treatment")

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, seed, string(after), "file must be left untouched when the managed parse fails")
}

// TestWarnUnmanagedProxiedServerConfig_EnableOnlyFieldLevel covers the
// round-4 verification finding (gastownhall/beads#4986): the unmanaged
// warning must inspect ArchiveLevel_ specifically, not "the whole
// auto_gc_behavior block is present" — otherwise an enable-only custom
// config both misses the zstd-risk warning on a capable dolt, and gets a
// FALSE incompatibility warning on an incapable one (since the block is
// present even though archive_level itself was never set).
func TestWarnUnmanagedProxiedServerConfig_EnableOnlyFieldLevel(t *testing.T) {
	enable := true

	newEnableOnlyCustomConfig := func(t *testing.T) string {
		t.Helper()
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		body := renderManagedConfigWithAutoGCBehavior(t, 54321, &servercfg.AutoGCBehaviorYAMLConfig{
			Enable_: &enable,
		})
		// Strip the marker: this must be evaluated as a genuinely
		// hand-written custom config, not a Beads-managed one.
		body = body[len(managedProxiedServerConfigMarker):]
		require.NotContains(t, string(body), "archive_level")

		bd := t.TempDir()
		customPath := filepath.Join(t.TempDir(), "my-server.yaml")
		require.NoError(t, os.WriteFile(customPath, body, 0o600))
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: customPath})
		return bd
	}

	t.Run("capable dolt: gets the zstd-risk warning", func(t *testing.T) {
		bd := newEnableOnlyCustomConfig(t)
		var err error
		got := captureStderr(t, func() {
			_, err = ensureProxiedServerConfig(bd, true)
		})
		require.NoError(t, err)
		assert.Contains(t, got, "zstd auto-GC may still be active",
			"an enable-only block must still be treated as archive_level-absent")
	})

	t.Run("incapable dolt: no false incompatibility warning", func(t *testing.T) {
		bd := newEnableOnlyCustomConfig(t)
		var err error
		got := captureStderr(t, func() {
			_, err = ensureProxiedServerConfig(bd, false)
		})
		require.NoError(t, err)
		assert.Empty(t, got,
			"an enable-only block (no archive_level) has nothing incompatible with an older dolt; "+
				"the old block-presence check would have warned here anyway")
	})
}

// TestEnsureProxiedServerConfig_ManagedConfigBehindSymlinkPreservesSymlink
// covers the round-4 verification finding (gastownhall/beads#4986):
// os.Rename's destination argument does not follow a symlink — it
// replaces whatever is directly AT that path. Before this fix, reconciling
// a managed config.yaml that was itself a symlink (e.g. an operator
// centralizing config outside the per-project .beads/dolt directory) would
// silently replace the symlink with a regular file at the SAME location,
// losing the indirection and leaving whatever the symlink pointed at
// unmodified and now orphaned.
func TestEnsureProxiedServerConfig_ManagedConfigBehindSymlinkPreservesSymlink(t *testing.T) {
	beadsDir := t.TempDir()
	root := filepath.Join(beadsDir, "dolt")
	require.NoError(t, os.MkdirAll(root, 0o755))

	// The physical file lives elsewhere; config.yaml in the managed
	// location is a symlink to it.
	physicalPath := filepath.Join(t.TempDir(), "real-config.yaml")
	seed, err := renderProxiedServerConfig(45678, false) // lacks archive_level: 0
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(physicalPath, seed, 0o600))

	cfgPath := filepath.Join(root, "config.yaml")
	if err := os.Symlink(physicalPath, cfgPath); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	path, err := ensureProxiedServerConfig(beadsDir, true) // now supported -> should reconcile
	require.NoError(t, err)
	assert.Equal(t, cfgPath, path)

	// The symlink itself must survive, still pointing at the same physical file.
	linkInfo, err := os.Lstat(cfgPath)
	require.NoError(t, err)
	require.True(t, linkInfo.Mode()&os.ModeSymlink != 0, "config.yaml must still be a symlink")
	target, err := os.Readlink(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, physicalPath, target)

	// The PHYSICAL file must carry the reconciled content.
	physicalBody, err := os.ReadFile(physicalPath)
	require.NoError(t, err)
	assert.Contains(t, string(physicalBody), "archive_level: 0")

	// Reading through the symlink must observe the same reconciled content.
	throughLink, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, physicalBody, throughLink)
}

func TestProxiedServerPathHelpers(t *testing.T) {
	bd := "/tmp/some/.beads"
	assert.Equal(t, "/tmp/some/.beads/dolt", proxiedServerRoot(bd))
	assert.Equal(t, "/tmp/some/.beads/dolt/config.yaml", proxiedServerConfigPath(bd))
	assert.Equal(t, "/tmp/some/.beads/dolt/server.log", proxiedServerLogPath(bd))
}

// TestInitCommandRegistersProxiedServerFlag verifies the --proxied-server flag
// is wired into initCmd. Flag-presence regression test.
func TestInitCommandRegistersProxiedServerFlag(t *testing.T) {
	flag := initCmd.Flags().Lookup("proxied-server")
	require.NotNil(t, flag, "init command does not register --proxied-server")
	assert.Equal(t, "false", flag.DefValue, "--proxied-server should default to false")
}

// TestInitCommandRegistersServerConfigFlag verifies the --proxied-server-config-path flag
// is wired into initCmd.
func TestInitCommandRegistersServerConfigFlag(t *testing.T) {
	flag := initCmd.Flags().Lookup("proxied-server-config-path")
	require.NotNil(t, flag, "init command does not register --proxied-server-config-path")
	assert.Equal(t, "", flag.DefValue, "--proxied-server-config-path should default to empty")
}

func writeProxiedClientInfo(t *testing.T, beadsDir string, info *configfile.ProxiedServerClientInfo) {
	t.Helper()
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, info))
}

func TestResolveProxiedServerConfigPath(t *testing.T) {
	t.Run("no sidecar, no env, returns default and !isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		bd := t.TempDir()
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, proxiedServerConfigPath(bd), path)
		assert.False(t, isCustom)
	})

	t.Run("empty sidecar, no env, returns default and !isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{})
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, proxiedServerConfigPath(bd), path)
		assert.False(t, isCustom)
	})

	t.Run("sidecar relative joins beadsDir and isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: "configs/server.yaml"})
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(bd, "configs/server.yaml"), path)
		assert.True(t, isCustom)
	})

	t.Run("sidecar absolute returned as-is and isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: "/etc/dolt/server.yaml"})
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/etc/dolt/server.yaml", path)
		assert.True(t, isCustom)
	})

	t.Run("env beats sidecar and isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "/from/env.yaml")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: "configs/from-meta.yaml"})
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/from/env.yaml", path)
		assert.True(t, isCustom)
	})

	t.Run("env with no sidecar still wins", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "/from/env.yaml")
		bd := t.TempDir()
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/from/env.yaml", path)
		assert.True(t, isCustom)
	})
}

// writeValidServerYAML writes a minimal valid dolt sql-server YAML to path
// and returns the path. Used to exercise the custom-config success path.
func writeValidServerYAML(t *testing.T, path string) string {
	t.Helper()
	body, err := renderProxiedServerConfig(54321, true)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	return path
}

// TestEnsureProxiedServerConfig_CustomPathExists asserts that when a custom
// path is configured, ensureProxiedServerConfig returns it unchanged AND does
// not auto-create the default <beadsDir>/proxieddb/config.yaml.
func TestEnsureProxiedServerConfig_CustomPathExists(t *testing.T) {
	t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
	bd := t.TempDir()

	customDir := t.TempDir()
	customPath := writeValidServerYAML(t, filepath.Join(customDir, "my-server.yaml"))

	writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: customPath})
	got, err := ensureProxiedServerConfig(bd, true)
	require.NoError(t, err)
	assert.Equal(t, customPath, got)

	defaultPath := proxiedServerConfigPath(bd)
	_, statErr := os.Stat(defaultPath)
	assert.True(t, os.IsNotExist(statErr), "default config must not be auto-created when a custom path is configured (got err=%v)", statErr)
}

// TestEnsureProxiedServerConfig_CustomPathMissing asserts a clear error when
// the user-supplied path doesn't exist. bd never auto-creates user files.
func TestEnsureProxiedServerConfig_CustomPathMissing(t *testing.T) {
	t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
	bd := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: missing})
	_, err := ensureProxiedServerConfig(bd, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), missing)
}

// TestEnsureProxiedServerConfig_CustomPathInvalidYAML asserts that a
// non-parsable YAML at the custom path is rejected up front rather than
// crashing the daemon downstream.
func TestEnsureProxiedServerConfig_CustomPathInvalidYAML(t *testing.T) {
	t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
	bd := t.TempDir()
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	// Unclosed flow sequence — guaranteed YAML parse error.
	require.NoError(t, os.WriteFile(bad, []byte("listener: [host: 127.0.0.1\n"), 0o600))

	writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: bad})
	_, err := ensureProxiedServerConfig(bd, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), bad)
	assert.Contains(t, strings.ToLower(err.Error()), "parse")
}

// TestEnsureProxiedServerConfig_CustomPathIsDirectory asserts that pointing
// the custom path at a directory (or other non-regular file) is rejected.
func TestEnsureProxiedServerConfig_CustomPathIsDirectory(t *testing.T) {
	t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
	bd := t.TempDir()
	dir := t.TempDir()

	writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{ConfigPath: dir})
	_, err := ensureProxiedServerConfig(bd, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), dir)
	assert.Contains(t, err.Error(), "not a regular file")
}

// TestInitCommandRegistersServerLogPathFlag verifies the --proxied-server-log-path
// flag is wired into initCmd.
func TestInitCommandRegistersServerLogPathFlag(t *testing.T) {
	flag := initCmd.Flags().Lookup("proxied-server-log-path")
	require.NotNil(t, flag, "init command does not register --proxied-server-log-path")
	assert.Equal(t, "", flag.DefValue, "--proxied-server-log-path should default to empty")
}

// TestResolveProxiedServerLogPath mirrors TestResolveProxiedServerConfigPath
// for the log-path resolver.
func TestResolveProxiedServerLogPath(t *testing.T) {
	t.Run("no sidecar, no env, returns default and !isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
		bd := t.TempDir()
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, proxiedServerLogPath(bd), path)
		assert.False(t, isCustom)
	})

	t.Run("empty sidecar, no env, returns default and !isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{})
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, proxiedServerLogPath(bd), path)
		assert.False(t, isCustom)
	})

	t.Run("sidecar relative joins beadsDir and isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{LogPath: "logs/server.log"})
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(bd, "logs/server.log"), path)
		assert.True(t, isCustom)
	})

	t.Run("sidecar absolute returned as-is and isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{LogPath: "/var/log/beads/server.log"})
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/var/log/beads/server.log", path)
		assert.True(t, isCustom)
	})

	t.Run("env beats sidecar and isCustom", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "/from/env.log")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{LogPath: "logs/from-meta.log"})
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/from/env.log", path)
		assert.True(t, isCustom)
	})

	t.Run("env with no sidecar still wins", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "/from/env.log")
		bd := t.TempDir()
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/from/env.log", path)
		assert.True(t, isCustom)
	})
}

// TestValidateProxiedServerLogPath covers the early-bailout validator.
// Contract: parent dir must exist; the file may not exist (the daemon's
// O_CREATE|O_APPEND open handles that); if it exists it must be a regular
// file.
func TestValidateProxiedServerLogPath(t *testing.T) {
	t.Run("parent dir missing rejected", func(t *testing.T) {
		// /<tmp>/nope/server.log — parent /<tmp>/nope doesn't exist.
		path := filepath.Join(t.TempDir(), "nope", "server.log")
		err := validateProxiedServerLogPath(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parent directory")
	})

	t.Run("path doesn't exist but parent does, accepted", func(t *testing.T) {
		// Daemon will create the file via O_CREATE|O_APPEND.
		path := filepath.Join(t.TempDir(), "server.log")
		require.NoError(t, validateProxiedServerLogPath(path))
	})

	t.Run("existing regular file accepted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "server.log")
		require.NoError(t, os.WriteFile(path, []byte("preexisting log content\n"), 0o600))
		require.NoError(t, validateProxiedServerLogPath(path))
	})

	t.Run("existing directory rejected", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "server.log")
		require.NoError(t, os.Mkdir(dir, 0o755))
		err := validateProxiedServerLogPath(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
	})

	t.Run("symlink to directory rejected", func(t *testing.T) {
		base := t.TempDir()
		realDir := filepath.Join(base, "actual-dir")
		require.NoError(t, os.Mkdir(realDir, 0o755))
		link := filepath.Join(base, "server.log")
		if err := os.Symlink(realDir, link); err != nil {
			t.Skipf("symlink not supported on this platform: %v", err)
		}
		err := validateProxiedServerLogPath(link)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
	})

	t.Run("parent path is a regular file, not a dir, rejected", func(t *testing.T) {
		base := t.TempDir()
		fileAsParent := filepath.Join(base, "blocker")
		require.NoError(t, os.WriteFile(fileAsParent, []byte("x"), 0o600))
		// /<tmp>/blocker/server.log — "blocker" is a file, not a dir.
		err := validateProxiedServerLogPath(filepath.Join(fileAsParent, "server.log"))
		require.Error(t, err)
	})
}

// TestValidateProxiedServerConfig covers the standalone validator that
// init.go uses for early proxied-server-config-path validation.
//
// The validator deliberately emits source-neutral errors (just the path)
// because the value may come from a CLI flag, BEADS_PROXIED_SERVER_CONFIG,
// or the proxied_server_client_info.json sidecar. Callers prepend their
// own label.
func TestValidateProxiedServerConfig(t *testing.T) {
	t.Run("valid YAML passes", func(t *testing.T) {
		path := writeValidServerYAML(t, filepath.Join(t.TempDir(), "ok.yaml"))
		require.NoError(t, validateProxiedServerConfig(path))
	})
	t.Run("missing path errors with the path in the message", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.yaml")
		err := validateProxiedServerConfig(missing)
		require.Error(t, err)
		assert.Contains(t, err.Error(), missing)
	})
	t.Run("directory rejected", func(t *testing.T) {
		err := validateProxiedServerConfig(t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
	})
	t.Run("invalid YAML rejected", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.yaml")
		require.NoError(t, os.WriteFile(bad, []byte("listener: [host: 127.0.0.1\n"), 0o600))
		err := validateProxiedServerConfig(bad)
		require.Error(t, err)
		assert.Contains(t, strings.ToLower(err.Error()), "parse")
	})
}

// TestCheckExistingBeadsDataAt_ProxiedServerNoData asserts that a proxied
// workspace with metadata.json but no actual <beadsDir>/proxieddb/<dbName>/.dolt
// directory is treated as a fresh clone — init is allowed to proceed so the
// caller can bootstrap.
func TestCheckExistingBeadsDataAt_ProxiedServerNoData(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0o755))

	metadata := map[string]interface{}{
		"database":      "dolt",
		"backend":       "dolt",
		"dolt_mode":     "proxied-server",
		"dolt_database": "myproj",
	}
	data, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644))

	// No <beadsDir>/proxieddb/myproj/.dolt — fresh-clone scenario.
	if err := checkExistingBeadsDataAt(beadsDir, "myproj"); err != nil {
		t.Fatalf("fresh proxied workspace should allow init, got: %v", err)
	}
}

// TestCheckExistingBeadsDataAt_ProxiedServerWithExistingDB asserts that the
// mere existence of the resolved proxied-server root blocks re-init in
// proxied-server mode. We deliberately don't peek deeper than the directory
// itself — the internal layout (wrapper db dir, per-db subdirs) is an
// implementation detail of the daemon. The custom-root sub-test additionally
// asserts that a workspace pointed at a custom root via metadata.json's
// dolt_proxied_server_root_path is also caught — otherwise re-init would
// silently bypass the safety check.
func TestCheckExistingBeadsDataAt_ProxiedServerWithExistingDB(t *testing.T) {
	t.Run("default root", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		require.NoError(t, os.MkdirAll(beadsDir, 0o755))

		metadata := map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "proxied-server",
			"dolt_database": "myproj",
		}
		data, err := json.Marshal(metadata)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644))

		// Materialize <beadsDir>/dolt/ — that alone should be enough to
		// trip the guard, regardless of what's inside.
		proxiedRoot := filepath.Join(beadsDir, "dolt")
		require.NoError(t, os.MkdirAll(proxiedRoot, 0o755))

		err = checkExistingBeadsDataAt(beadsDir, "myproj")
		require.Error(t, err, "existing proxied-server root directory should block init")
		assert.Contains(t, err.Error(), "already initialized")
		assert.Contains(t, err.Error(), proxiedRoot)
	})

	t.Run("custom root via proxied_server_client_info.json", func(t *testing.T) {
		// Ensure no env override leaks across tests — the resolver checks
		// BEADS_PROXIED_SERVER_ROOT_PATH before the sidecar.
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")

		base := t.TempDir()
		beadsDir := filepath.Join(base, ".beads")
		require.NoError(t, os.MkdirAll(beadsDir, 0o755))

		// Custom root well outside <beadsDir>/proxieddb so the test fails
		// loudly if the safety check still hardcodes the default location.
		customRoot := filepath.Join(base, "custom-root")
		require.NoError(t, os.MkdirAll(customRoot, 0o755))

		metadata := map[string]interface{}{
			"database":      "dolt",
			"backend":       "dolt",
			"dolt_mode":     "proxied-server",
			"dolt_database": "myproj",
		}
		data, err := json.Marshal(metadata)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644))
		writeProxiedClientInfo(t, beadsDir, &configfile.ProxiedServerClientInfo{RootPath: customRoot})

		// Sanity: the default location should NOT exist — proves the guard
		// fired off the resolved root, not the default.
		defaultRoot := filepath.Join(beadsDir, "dolt")
		_, statErr := os.Stat(defaultRoot)
		require.True(t, os.IsNotExist(statErr), "default <beadsDir>/dolt must not exist for this test to be meaningful")

		err = checkExistingBeadsDataAt(beadsDir, "myproj")
		require.Error(t, err, "existing custom root should block init")
		assert.Contains(t, err.Error(), "already initialized")
		assert.Contains(t, err.Error(), customRoot, "error must cite the custom root, not the default")
		assert.NotContains(t, err.Error(), defaultRoot, "error must not cite a default location bd never used")
	})
}

// TestInitCommandRegistersServerRootPathFlag verifies the --proxied-server-root-path
// flag is wired into initCmd.
func TestInitCommandRegistersServerRootPathFlag(t *testing.T) {
	flag := initCmd.Flags().Lookup("proxied-server-root-path")
	require.NotNil(t, flag, "init command does not register --proxied-server-root-path")
	assert.Equal(t, "", flag.DefValue, "--proxied-server-root-path should default to empty")
}

func TestInitCommandRegistersProxiedServerExternalFlags(t *testing.T) {
	cases := []struct {
		name        string
		defaultText string
	}{
		{"proxied-server-external-host", ""},
		{"proxied-server-external-port", "0"},
		{"proxied-server-external-socket-path", ""},
		{"proxied-server-external-user", ""},
		{"proxied-server-external-tls", "false"},
		{"proxied-server-external-tls-ca-cert-path", ""},
		{"proxied-server-external-tls-cert-path", ""},
		{"proxied-server-external-tls-key-path", ""},
		{"proxied-server-external-tls-server-name", ""},
		{"proxied-server-external-tls-skip-verify", "false"},
		{"proxied-server-external-keep-alive", "0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := initCmd.Flags().Lookup(tc.name)
			require.NotNil(t, f, "init command does not register --%s", tc.name)
			assert.Equal(t, tc.defaultText, f.DefValue, "--%s default", tc.name)
		})
	}
}

// TestResolveProxiedServerRootPath mirrors TestResolveProxiedServerLogPath /
// TestResolveProxiedServerConfigPath for the root-path resolver.
func TestResolveProxiedServerRootPath(t *testing.T) {
	t.Run("no sidecar, no env, returns default", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		got, err := resolveProxiedServerRootPath(bd)
		require.NoError(t, err)
		assert.Equal(t, proxiedServerRoot(bd), got)
	})

	t.Run("empty sidecar, no env, returns default", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{})
		got, err := resolveProxiedServerRootPath(bd)
		require.NoError(t, err)
		assert.Equal(t, proxiedServerRoot(bd), got)
	})

	t.Run("sidecar relative joins beadsDir", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{RootPath: "alt-proxieddb"})
		got, err := resolveProxiedServerRootPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(bd, "alt-proxieddb"), got)
	})

	t.Run("sidecar absolute returned as-is", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{RootPath: "/var/lib/beads/proxieddb"})
		got, err := resolveProxiedServerRootPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/var/lib/beads/proxieddb", got)
	})

	t.Run("env beats sidecar", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "/from/env-root")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{RootPath: "alt-from-meta"})
		got, err := resolveProxiedServerRootPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/from/env-root", got)
	})

	t.Run("env with no sidecar still wins", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "/from/env-root")
		bd := t.TempDir()
		got, err := resolveProxiedServerRootPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/from/env-root", got)
	})

	t.Run("corrupt sidecar surfaces error", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		require.NoError(t, os.WriteFile(
			configfile.ProxiedServerClientInfoPath(bd),
			[]byte("not json{"),
			0o600,
		))
		_, err := resolveProxiedServerRootPath(bd)
		require.Error(t, err)
	})
}

// TestValidateProxiedServerRootPath covers the early-bailout validator.
// Contract: path is allowed to NOT exist (runtime mkdir creates); if it
// exists, info.IsDir() must be true. os.Stat follows symlinks, so a
// symlink-to-dir reports as a dir (accepted) and a symlink-to-file reports
// as a regular file (rejected).
func TestValidateProxiedServerRootPath(t *testing.T) {
	t.Run("path doesn't exist accepted", func(t *testing.T) {
		// Runtime os.MkdirAll in the dolt store will create it.
		path := filepath.Join(t.TempDir(), "does-not-exist")
		require.NoError(t, validateProxiedServerRootPath(path))
	})

	t.Run("nested missing path accepted", func(t *testing.T) {
		// Even nested non-existent paths are fine — MkdirAll handles it.
		path := filepath.Join(t.TempDir(), "a", "b", "c")
		require.NoError(t, validateProxiedServerRootPath(path))
	})

	t.Run("existing directory accepted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing-dir")
		require.NoError(t, os.Mkdir(path, 0o755))
		require.NoError(t, validateProxiedServerRootPath(path))
	})

	t.Run("existing regular file rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "regular-file")
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
		err := validateProxiedServerRootPath(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})

	t.Run("symlink to file rejected", func(t *testing.T) {
		base := t.TempDir()
		realFile := filepath.Join(base, "real-file")
		require.NoError(t, os.WriteFile(realFile, []byte("x"), 0o600))
		link := filepath.Join(base, "link-to-file")
		if err := os.Symlink(realFile, link); err != nil {
			t.Skipf("symlink not supported on this platform: %v", err)
		}
		err := validateProxiedServerRootPath(link)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})

	t.Run("symlink to dir accepted", func(t *testing.T) {
		base := t.TempDir()
		realDir := filepath.Join(base, "real-dir")
		require.NoError(t, os.Mkdir(realDir, 0o755))
		link := filepath.Join(base, "link-to-dir")
		if err := os.Symlink(realDir, link); err != nil {
			t.Skipf("symlink not supported on this platform: %v", err)
		}
		// os.Stat follows symlinks → reports as dir → accepted.
		require.NoError(t, validateProxiedServerRootPath(link))
	})
}

// TestResolveProxiedServerConfigPath_FollowsCustomRoot locks down the
// cascade: with no per-flag override, the config path's default fallback
// must compute against the resolved root, so --proxied-server-root-path
// alone moves config.yaml. The cascaded default is still NOT marked
// isCustom — bd still owns the YAML's lifecycle, just under a custom root.
// When the per-flag override IS set, it wins regardless of the root.
func TestResolveProxiedServerConfigPath_FollowsCustomRoot(t *testing.T) {
	t.Run("custom root cascades into default config path", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		customRoot := filepath.Join(bd, "alt-root")
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{RootPath: customRoot})
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(customRoot, "config.yaml"), path)
		assert.False(t, isCustom, "cascaded default is NOT user-owned")
	})

	t.Run("custom root via env cascades", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		bd := t.TempDir()
		envRoot := filepath.Join(bd, "env-root")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", envRoot)
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(envRoot, "config.yaml"), path)
		assert.False(t, isCustom)
	})

	t.Run("per-flag config override wins over root cascade", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{
			RootPath:   filepath.Join(bd, "alt-root"),
			ConfigPath: "/etc/dolt/explicit.yaml",
		})
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/etc/dolt/explicit.yaml", path)
		assert.True(t, isCustom, "explicit override is user-owned")
	})

	t.Run("no overrides falls back to <beadsDir>/dolt (preserves pre-cascade default)", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		path, isCustom, err := resolveProxiedServerConfigPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(bd, "dolt", "config.yaml"), path)
		assert.False(t, isCustom)
	})
}

// TestResolveProxiedServerLogPath_FollowsCustomRoot mirrors the config
// cascade test for the log resolver.
func TestResolveProxiedServerLogPath_FollowsCustomRoot(t *testing.T) {
	t.Run("custom root cascades into default log path", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		customRoot := filepath.Join(bd, "alt-root")
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{RootPath: customRoot})
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(customRoot, "server.log"), path)
		assert.False(t, isCustom, "cascaded default is NOT user-owned")
	})

	t.Run("custom root via env cascades", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
		bd := t.TempDir()
		envRoot := filepath.Join(bd, "env-root")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", envRoot)
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(envRoot, "server.log"), path)
		assert.False(t, isCustom)
	})

	t.Run("per-flag log override wins over root cascade", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		writeProxiedClientInfo(t, bd, &configfile.ProxiedServerClientInfo{
			RootPath: filepath.Join(bd, "alt-root"),
			LogPath:  "/var/log/explicit.log",
		})
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, "/var/log/explicit.log", path)
		assert.True(t, isCustom)
	})

	t.Run("no overrides falls back to <beadsDir>/dolt (preserves pre-cascade default)", func(t *testing.T) {
		t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
		bd := t.TempDir()
		path, isCustom, err := resolveProxiedServerLogPath(bd)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(bd, "dolt", "server.log"), path)
		assert.False(t, isCustom)
	})
}
