package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

func snapshotMigrationTree(t *testing.T, roots ...string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if strings.HasSuffix(path, ".gate.lock") || strings.HasSuffix(path, migrateLockFileName) {
				return nil
			}
			if err != nil {
				return nil
			}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				target, _ := os.Readlink(path)
				out[path] = []byte("symlink:" + target)
			case info.IsDir():
				out[path] = []byte("dir:" + info.Mode().String())
			case info.Mode().IsRegular():
				b, _ := os.ReadFile(path)
				out[path] = append([]byte("file:"+info.Mode().String()+"\x00"), b...)
			}
			return nil
		})
	}
	return out
}

func migrateModeWorkspace(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
	t.Setenv("BEADS_PROXIED_SERVER_CONFIG", "")
	t.Setenv("BEADS_PROXIED_SERVER_LOG", "")
	t.Setenv("BEADS_DOLT_DATA_DIR", "")

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "dolt", ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
	writeMetadataConfig(t, beadsDir, mode, "myproj")
	t.Chdir(dir)
	return beadsDir
}

func touchFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
}

func serverAssetNames() []string {
	return []string{"dolt-server.pid", "dolt-server.port", "dolt-server.lock", "dolt-server.log", "dolt-server.log.1"}
}

func proxiedAssetNames() []string {
	return []string{"proxy.pid", "proxy.lock", "proxy.log", "proxy-child.pid", "proxy-child.lock", "server.log"}
}

func TestMigrateToProxiedServer_FlipsMode(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)

	for _, n := range serverAssetNames() {
		touchFile(t, filepath.Join(beadsDir, n))
	}
	touchFile(t, filepath.Join(beadsDir, "dolt-pprof", "cpu.pprof"))

	require.NoError(t, runMigrateToProxiedServer(false, 0, false))

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltProxiedServerMode())
	assert.Equal(t, "myproj", cfg.GetDoltDatabase())

	_, statErr := os.Stat(configfile.ProxiedServerClientInfoPath(beadsDir))
	require.NoError(t, statErr, "sidecar must be written")

	for _, n := range serverAssetNames() {
		_, err := os.Stat(filepath.Join(beadsDir, n))
		assert.True(t, os.IsNotExist(err), "server asset %s must be removed", n)
	}
	_, err = os.Stat(filepath.Join(beadsDir, "dolt-pprof"))
	assert.True(t, os.IsNotExist(err), "dolt-pprof/ must be removed")
}

func TestMigrateToProxiedServer_RejectsNonServerMode(t *testing.T) {
	migrateModeWorkspace(t, configfile.DoltModeEmbedded)
	err := runMigrateToProxiedServer(false, 0, false)
	require.Error(t, err)
}

func TestMigrateToProxiedServer_MissingRootFailsClosed(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	require.NoError(t, os.RemoveAll(filepath.Join(beadsDir, "dolt")))
	before, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	require.NoError(t, err)
	require.Error(t, runMigrateToProxiedServer(false, 0, false))
	after, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestMigrateToProxiedServer_MalformedRootIdentityFailsClosed(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "dolt", ".dolt", "repo_state.json"), []byte("not-json"), 0o600))
	require.Error(t, runMigrateToProxiedServer(false, 0, false))
}

func TestMigrateToProxiedServer_InvalidRootIdentityShapesFailClosed(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"garbage":true}`, `{"head":null,"remotes":{},"backups":{},"branches":{}}`, `{"head":"refs/heads/","remotes":{},"backups":{},"branches":{}}`} {
		t.Run(body, func(t *testing.T) {
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
			require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "dolt", ".dolt", "repo_state.json"), []byte(body), 0o600))
			require.Error(t, runMigrateToProxiedServer(false, 0, false))
		})
	}
}

func TestMigrateToProxiedServer_DryRunWritesNothing(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	touchFile(t, filepath.Join(beadsDir, "dolt-server.log"))

	require.NoError(t, runMigrateToProxiedServer(true, 0, false))

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltServerMode(), "mode must be unchanged in dry-run")

	_, statErr := os.Stat(configfile.ProxiedServerClientInfoPath(beadsDir))
	assert.True(t, os.IsNotExist(statErr), "sidecar must not be written in dry-run")

	_, assetErr := os.Stat(filepath.Join(beadsDir, "dolt-server.log"))
	require.NoError(t, assetErr, "dry-run must not delete assets")
}

func TestMigrateToProxiedServer_MalformedWorkspaceYAMLFailsClosed(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	configPath := filepath.Join(beadsDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("dolt: ["), 0o600))
	before := snapshotMigrationTree(t, beadsDir)
	var err error
	stderr := captureStderr(t, func() { err = runMigrateToProxiedServer(false, 0, false) })
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(stderr), "parsing workspace config")
	assert.Equal(t, before, snapshotMigrationTree(t, beadsDir))
}

func TestMigrateToProxiedServer_AlreadyProxiedRejectsStaleControl(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	root := filepath.Join(beadsDir, "dolt")
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: root}))
	touchFile(t, filepath.Join(root, "proxy.log"))
	before := snapshotMigrationTree(t, beadsDir, root)
	require.Error(t, runMigrateToProxiedServer(false, 0, false))
	assert.Equal(t, before, snapshotMigrationTree(t, beadsDir, root))
}

func TestMigrateToProxiedServer_AlreadyProxiedRejectsBadSidecar(t *testing.T) {
	for _, tc := range []struct {
		name string
		info *configfile.ProxiedServerClientInfo
	}{
		{"external", &configfile.ProxiedServerClientInfo{RootPath: "/tmp/ext", External: &configfile.ExternalDoltConfig{Host: "db.example", Port: 3307}}},
		{"mismatch", &configfile.ProxiedServerClientInfo{RootPath: "/tmp/other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
			require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, tc.info))
			before := snapshotMigrationTree(t, beadsDir)
			require.Error(t, runMigrateToProxiedServer(false, 0, false))
			assert.Equal(t, before, snapshotMigrationTree(t, beadsDir))
		})
	}
}

func TestMigrateToProxiedServer_StaleSidecarFailsClosed(t *testing.T) {
	rootCases := []struct {
		name string
		info *configfile.ProxiedServerClientInfo
		want string
	}{
		{
			name: "external endpoint",
			info: &configfile.ProxiedServerClientInfo{
				RootPath: filepath.Join("/", "external-root"),
				External: &configfile.ExternalDoltConfig{Host: "db.example", Port: 3307},
			},
			want: "externally hosted proxied dolt endpoint",
		},
		{
			name: "mismatched local root",
			info: &configfile.ProxiedServerClientInfo{RootPath: filepath.Join("/", "another-repo")},
			want: "sidecar root",
		},
		{
			name: "matching relative root",
			info: &configfile.ProxiedServerClientInfo{RootPath: "dolt"},
			want: "exists without a recoverable journal",
		},
	}
	for _, tc := range rootCases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
			root := filepath.Join(beadsDir, "dolt")
			require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, tc.info))
			before := snapshotMigrationTree(t, beadsDir, root)
			var migrateErr error
			stderr := captureStderr(t, func() {
				migrateErr = runMigrateToProxiedServer(false, 0, false)
			})
			require.Error(t, migrateErr)
			assert.Contains(t, strings.ToLower(stderr), tc.want)
			assert.Equal(t, before, snapshotMigrationTree(t, beadsDir, root))
			cfg, err := configfile.Load(beadsDir)
			require.NoError(t, err)
			assert.True(t, cfg.IsDoltServerMode())
			journal, err := loadMigrateJournal(beadsDir)
			require.NoError(t, err)
			assert.Nil(t, journal)
		})
	}
}

func TestMigrateToServer_FlipsModeAndRemovesSidecar(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{}))

	rootDir := filepath.Join(beadsDir, "dolt")
	require.NoError(t, os.MkdirAll(filepath.Join(rootDir, ".dolt"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(rootDir, "myproj", ".dolt"), 0o755))
	touchFile(t, filepath.Join(rootDir, "config.yaml"))
	for _, n := range proxiedAssetNames() {
		touchFile(t, filepath.Join(rootDir, n))
	}

	require.NoError(t, runMigrateFromProxiedServer(false, false))

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltServerMode())

	_, statErr := os.Stat(configfile.ProxiedServerClientInfoPath(beadsDir))
	assert.True(t, os.IsNotExist(statErr), "sidecar must be removed")

	_, markerErr := os.Stat(filepath.Join(rootDir, ".bd-dolt-ok"))
	require.NoError(t, markerErr, "compatibility marker must be written")

	for _, n := range proxiedAssetNames() {
		_, err := os.Stat(filepath.Join(rootDir, n))
		assert.True(t, os.IsNotExist(err), "proxied asset %s must be removed", n)
	}

	_, dotDoltErr := os.Stat(filepath.Join(rootDir, ".dolt"))
	require.NoError(t, dotDoltErr, "shared .dolt must be preserved")
	_, dbErr := os.Stat(filepath.Join(rootDir, "myproj", ".dolt"))
	require.NoError(t, dbErr, "database subdir must be preserved")

	_, configErr := os.Stat(filepath.Join(rootDir, "config.yaml"))
	require.NoError(t, configErr, "shared config.yaml must be preserved as the server-mode config")
}

func TestMigrateToServer_KeepsCustomConfig(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	customConfig := filepath.Join(t.TempDir(), "custom.yaml")
	touchFile(t, customConfig)
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
		ConfigPath: customConfig,
	}))
	require.NoError(t, os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o755))

	require.NoError(t, runMigrateFromProxiedServer(false, false))

	_, err := os.Stat(customConfig)
	require.NoError(t, err, "user-supplied config path must not be deleted")
}

func TestMigrateToServer_RejectsNonProxiedMode(t *testing.T) {
	migrateModeWorkspace(t, configfile.DoltModeEmbedded)
	err := runMigrateFromProxiedServer(false, false)
	require.Error(t, err)
}

func TestMigrateMode_RefusesWhenLockHeld(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)

	held, err := util.TryLock(filepath.Join(beadsDir, migrateLockFileName))
	require.NoError(t, err)

	require.Error(t, runMigrateToProxiedServer(false, 0, false), "must refuse while the lock is held")
	require.Error(t, runMigrateFromProxiedServer(false, false), "must refuse while the lock is held")

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltServerMode(), "mode must be unchanged while blocked")

	held.Unlock()
	require.NoError(t, runMigrateToProxiedServer(false, 0, false), "must succeed once the lock is released")
}

func TestMigrateToServer_RefusesWhenLifecycleLockHeld(t *testing.T) {
	cases := []struct {
		name string
		rel  func(beadsDir string) string
	}{
		{"proxy.lock", func(b string) string { return filepath.Join(b, "dolt", "proxy.lock") }},
		{"proxy-child.lock", func(b string) string { return filepath.Join(b, "dolt", "proxy-child.lock") }},
		{"dolt-server.lock", func(b string) string { return filepath.Join(b, "dolt-server.lock") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
			require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{}))
			require.NoError(t, os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o755))

			held, err := util.TryLock(tc.rel(beadsDir))
			require.NoError(t, err)

			require.Error(t, runMigrateFromProxiedServer(false, false), "must refuse while %s is held", tc.name)

			cfg, err := configfile.Load(beadsDir)
			require.NoError(t, err)
			assert.True(t, cfg.IsDoltProxiedServerMode(), "mode must be unchanged when blocked")

			held.Unlock()
			require.NoError(t, runMigrateFromProxiedServer(false, false), "must succeed once %s is released", tc.name)

			free, err := util.TryLock(tc.rel(beadsDir))
			require.NoError(t, err, "%s must be released after a successful migration", tc.name)
			free.Unlock()
		})
	}
}

func TestMigrateMode_DryRunIgnoresLock(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	held, err := util.TryLock(filepath.Join(beadsDir, migrateLockFileName))
	require.NoError(t, err)
	defer held.Unlock()

	require.NoError(t, runMigrateToProxiedServer(true, 0, false), "dry-run must not require the lock")
}

func TestMigrateMode_ReleasesLockAfterSuccess(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	require.NoError(t, runMigrateToProxiedServer(false, 0, false))

	_, statErr := os.Stat(filepath.Join(beadsDir, migrateLockFileName))
	assert.True(t, os.IsNotExist(statErr), "migrate.lock must be removed after the command completes")

	lock, err := util.TryLock(filepath.Join(beadsDir, migrateLockFileName))
	require.NoError(t, err, "lock must be released after the command completes")
	lock.Unlock()
	_ = os.Remove(filepath.Join(beadsDir, migrateLockFileName))
}

func TestMigrateSharedToProxiedServer_RootsAtSharedDir(t *testing.T) {
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	require.NoError(t, os.MkdirAll(filepath.Join(sharedDir, "dolt", ".dolt"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "dolt", ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600))

	require.NoError(t, runMigrateToProxiedServer(false, 0, true))

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltProxiedServerMode())

	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, filepath.Join(sharedDir, "dolt"), info.RootPath, "proxy must be rooted at the shared dolt dir")

	body, _ := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	assert.NotContains(t, string(body), "shared-server: true", "dolt.shared-server must be turned off")
}

func TestMigrateSharedToProxiedServer_AllCheckpointFaultsRetry(t *testing.T) {
	for _, phase := range []migratePhase{migratePrepared, migrateTargetConfigured, migrateOldControlsRetired, migrateVerified, migrateCommitted} {
		t.Run(string(phase), func(t *testing.T) {
			sharedDir := t.TempDir()
			t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
			t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			root := filepath.Join(sharedDir, "dolt")
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
			require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600))
			t.Setenv("BEADS_MIGRATION_FAIL_PHASE", string(phase))
			require.Error(t, runMigrateToProxiedServer(false, 0, true))
			j, err := loadMigrateJournal(beadsDir)
			require.NoError(t, err)
			require.NotNil(t, j)
			assert.Equal(t, phase, j.Phase)
			t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
			require.NoError(t, runMigrateToProxiedServer(false, 0, true))
			require.NoError(t, runMigrateToProxiedServer(false, 0, true))
		})
	}
}

func TestMigrateSharedToProxiedServer_DryRunDoesNotCreateRoot(t *testing.T) {
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600))
	require.NoError(t, runMigrateToProxiedServer(true, 0, true))
	_, err := os.Stat(filepath.Join(sharedDir, "dolt"))
	assert.True(t, os.IsNotExist(err), "dry-run must not create shared root")
}

func TestMigrateToProxiedServer_RejectsSharedRepo(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	migrateModeWorkspace(t, configfile.DoltModeServer)
	require.Error(t, runMigrateToProxiedServer(false, 0, false), "non-shared command must reject a shared repo")
}

func TestMigrateSharedToProxiedServer_RejectsNonShared(t *testing.T) {
	migrateModeWorkspace(t, configfile.DoltModeServer)
	require.Error(t, runMigrateToProxiedServer(false, 0, true), "shared command must reject a non-shared repo")
}

func TestMigrateProxiedToSharedServer_Reverse(t *testing.T) {
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: false\n"), 0o600))
	sharedDolt := filepath.Join(sharedDir, "dolt")
	require.NoError(t, os.MkdirAll(filepath.Join(sharedDolt, ".dolt"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDolt, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: sharedDolt}))

	require.NoError(t, runMigrateFromProxiedServer(false, true))

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltServerMode())

	_, statErr := os.Stat(configfile.ProxiedServerClientInfoPath(beadsDir))
	assert.True(t, os.IsNotExist(statErr), "sidecar must be removed")

	body, _ := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	assert.Contains(t, string(body), "shared-server: true", "dolt.shared-server must be re-enabled")
}

func TestMigrateFromProxiedToSharedServer_AllCheckpointFaultsRetry(t *testing.T) {
	for _, phase := range []migratePhase{migratePrepared, migrateTargetConfigured, migrateOldControlsRetired, migrateVerified, migrateCommitted} {
		t.Run(string(phase), func(t *testing.T) {
			sharedDir := t.TempDir()
			t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
			t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
			root := filepath.Join(sharedDir, "dolt")
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
			require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: root}))
			require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: false\n"), 0o600))
			t.Setenv("BEADS_MIGRATION_FAIL_PHASE", string(phase))
			require.Error(t, runMigrateFromProxiedServer(false, true))
			j, err := loadMigrateJournal(beadsDir)
			require.NoError(t, err)
			require.NotNil(t, j)
			assert.Equal(t, phase, j.Phase)
			assert.Equal(t, 1, j.Attempt)
			t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
			require.NoError(t, runMigrateFromProxiedServer(false, true))
			require.NoError(t, runMigrateFromProxiedServer(false, true))
			cfg, _ := configfile.Load(beadsDir)
			assert.True(t, cfg.IsDoltServerMode())
			v, ok := config.WorkspaceYamlValue(beadsDir, "dolt.shared-server")
			assert.True(t, ok)
			assert.Equal(t, "true", strings.ToLower(v))
			_, err = os.Stat(configfile.ProxiedServerClientInfoPath(beadsDir))
			assert.True(t, os.IsNotExist(err))
		})
	}
}

func TestMigrateProxiedToSharedServer_DryRunDoesNotCreateRoot(t *testing.T) {
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	sharedDolt := filepath.Join(sharedDir, "dolt")
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: sharedDolt}))
	require.NoError(t, runMigrateFromProxiedServer(true, true))
	_, err := os.Stat(sharedDolt)
	assert.True(t, os.IsNotExist(err), "dry-run must not create shared root")
}

func TestMigrateFromProxiedToServer_RejectsSharedRooted(t *testing.T) {
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	sharedDolt := filepath.Join(sharedDir, "dolt")
	require.NoError(t, os.MkdirAll(filepath.Join(sharedDolt, ".dolt"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDolt, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: sharedDolt}))

	require.Error(t, runMigrateFromProxiedServer(false, false), "non-shared reverse must reject a shared-rooted proxied repo")
}

func TestMigrateToProxiedServer_AlreadyProxiedIsNoop(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: filepath.Join(beadsDir, "dolt")}))
	require.NoError(t, runMigrateToProxiedServer(false, 0, false))

	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltProxiedServerMode())
}

func TestMigrateToProxiedServer_RetryAfterCheckpointFault(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	t.Setenv("BEADS_MIGRATION_FAIL_PHASE", string(migrateTargetConfigured))
	require.Error(t, runMigrateToProxiedServer(false, 0, false))
	j, err := loadMigrateJournal(beadsDir)
	require.NoError(t, err)
	require.NotNil(t, j)
	assert.Equal(t, migrateTargetConfigured, j.Phase)
	t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
	require.NoError(t, runMigrateToProxiedServer(false, 0, false))
	j, err = loadMigrateJournal(beadsDir)
	assert.NoError(t, err)
	assert.Nil(t, j, "successful migration removes its journal")
}

func TestMigrateJournal_UnknownPhaseFailsClosed(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	path := migrateJournalPath(beadsDir)
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"source_mode":"server","target_mode":"proxied-server","phase":"surprise","attempt":1}`), 0o600))
	_, err := loadMigrateJournal(beadsDir)
	require.Error(t, err)
}

func TestMigrateToProxiedServer_AllCheckpointFaultsRetry(t *testing.T) {
	for _, phase := range []migratePhase{migratePrepared, migrateTargetConfigured, migrateOldControlsRetired, migrateVerified, migrateCommitted} {
		t.Run(string(phase), func(t *testing.T) {
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
			t.Setenv("BEADS_MIGRATION_FAIL_PHASE", string(phase))
			require.Error(t, runMigrateToProxiedServer(false, 0, false))
			j, err := loadMigrateJournal(beadsDir)
			require.NoError(t, err)
			require.NotNil(t, j)
			assert.Equal(t, phase, j.Phase)
			assert.Equal(t, 1, j.Attempt)
			t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
			require.NoError(t, runMigrateToProxiedServer(false, 0, false))
			after := snapshotMigrationTree(t, beadsDir)
			j, err = loadMigrateJournal(beadsDir)
			require.NoError(t, err)
			assert.Nil(t, j)
			require.NoError(t, runMigrateToProxiedServer(false, 0, false))
			assert.Equal(t, after, snapshotMigrationTree(t, beadsDir))
		})
	}
}

func TestMigrateFromProxiedServer_MalformedSidecarFailsClosed(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	require.NoError(t, os.WriteFile(configfile.ProxiedServerClientInfoPath(beadsDir), []byte("{bad"), 0o600))
	require.Error(t, runMigrateFromProxiedServer(false, false))
	data, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), configfile.DoltModeProxiedServer)
}

func TestMigrateFromProxiedServer_AlreadyServerRejectsMalformedSidecar(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	path := configfile.ProxiedServerClientInfoPath(beadsDir)
	require.NoError(t, os.WriteFile(path, []byte("{bad"), 0o600))
	before := snapshotMigrationTree(t, beadsDir)
	var err error
	stderr := captureStderr(t, func() { err = runMigrateFromProxiedServer(false, false) })
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(stderr), "sidecar")
	assert.Equal(t, before, snapshotMigrationTree(t, beadsDir))
}

func TestMigrateFromProxiedServer_AlreadyTargetRejectsStaleSidecar(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: filepath.Join(beadsDir, "dolt")}))
	before := snapshotMigrationTree(t, beadsDir)
	var err error
	stderr := captureStderr(t, func() { err = runMigrateFromProxiedServer(false, false) })
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(stderr), "stale")
	assert.Equal(t, before, snapshotMigrationTree(t, beadsDir))
}

func TestMigrateFromProxiedToSharedServer_AlreadyTargetRejectsStaleSidecar(t *testing.T) {
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600))
	root := filepath.Join(sharedDir, "dolt")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: root}))
	before := snapshotMigrationTree(t, beadsDir, root)
	var err error
	stderr := captureStderr(t, func() { err = runMigrateFromProxiedServer(false, true) })
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(stderr), "stale")
	assert.Equal(t, before, snapshotMigrationTree(t, beadsDir, root))
}

func TestMigrateFromProxiedServer_JournalPreservesExternalSidecar(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	root := filepath.Join(beadsDir, "dolt")
	ext := &configfile.ExternalDoltConfig{Host: "db.example", Port: 3307, User: "alice"}
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: root, External: ext}))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
	require.Error(t, runMigrateFromProxiedServer(false, false), "external endpoint cannot be converted to local server")
	got, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	require.NoError(t, err)
	assert.Equal(t, ext, got.External)
	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltProxiedServerMode())
}

func TestMigrateFromProxiedServer_AllCheckpointFaultsRetry(t *testing.T) {
	for _, phase := range []migratePhase{migratePrepared, migrateTargetConfigured, migrateOldControlsRetired, migrateVerified, migrateCommitted} {
		t.Run(string(phase), func(t *testing.T) {
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
			root := filepath.Join(beadsDir, "dolt")
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
			require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{RootPath: root}))
			t.Setenv("BEADS_MIGRATION_FAIL_PHASE", string(phase))
			require.Error(t, runMigrateFromProxiedServer(false, false))
			t.Setenv("BEADS_MIGRATION_FAIL_PHASE", "")
			require.NoError(t, runMigrateFromProxiedServer(false, false))
			after := snapshotMigrationTree(t, beadsDir)
			j, err := loadMigrateJournal(beadsDir)
			require.NoError(t, err)
			assert.Nil(t, j)
			require.NoError(t, runMigrateFromProxiedServer(false, false))
			assert.Equal(t, after, snapshotMigrationTree(t, beadsDir))
		})
	}
}

func TestMigrateFromProxiedServer_ExternalJournalAlwaysFailsClosed(t *testing.T) {
	for _, phase := range []migratePhase{migratePrepared, migrateTargetConfigured, migrateOldControlsRetired, migrateVerified, migrateCommitted} {
		t.Run(string(phase), func(t *testing.T) {
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
			root := filepath.Join(beadsDir, "dolt")
			require.NoError(t, os.MkdirAll(root, 0o755))
			if phase != migratePrepared {
				writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, "myproj")
			}
			wantMode := configfile.DoltModeServer
			if phase == migratePrepared {
				wantMode = configfile.DoltModeProxiedServer
			}
			ext := &configfile.ExternalDoltConfig{Host: "db.example", Port: 3307}
			j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeProxiedServer, TargetMode: configfile.DoltModeServer, RootPath: root, External: ext, Ownership: "external", Attempt: 1, Phase: phase, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: root, External: ext}}
			b, err := json.Marshal(j)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
			require.Error(t, runMigrateFromProxiedServer(false, false))
			cfg, err := configfile.Load(beadsDir)
			require.NoError(t, err)
			assert.Equal(t, wantMode, cfg.GetDoltMode())
		})
	}
}

func TestMigrateExternalJournalAlwaysFailsClosed_TCPAndUnixForwardReverse(t *testing.T) {
	endpoints := []struct {
		name string
		cfg  *configfile.ExternalDoltConfig
	}{
		{"tcp", &configfile.ExternalDoltConfig{Host: "db.example", Port: 3307}},
		{"unix", &configfile.ExternalDoltConfig{Socket: "/tmp/beads-dolt.sock"}},
	}
	for _, ep := range endpoints {
		for _, reverse := range []bool{false, true} {
			for _, phase := range []migratePhase{migratePrepared, migrateTargetConfigured, migrateOldControlsRetired, migrateVerified, migrateCommitted} {
				t.Run(ep.name+"/"+string(phase), func(t *testing.T) {
					beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
					root := filepath.Join(beadsDir, "dolt")
					want := configfile.DoltModeServer
					src := configfile.DoltModeServer
					target := configfile.DoltModeProxiedServer
					if reverse {
						src, target = configfile.DoltModeProxiedServer, configfile.DoltModeServer
						want = configfile.DoltModeProxiedServer
					}
					// A prepared journal may legitimately coexist with either
					// source or target metadata. Later phases require target
					// metadata so validation reaches the ownership refusal.
					metadataMode := src
					if phase != migratePrepared {
						metadataMode = target
					}
					want = metadataMode
					writeMetadataConfig(t, beadsDir, metadataMode, "myproj")
					j := migrateJournal{Version: 1, SourceMode: src, TargetMode: target, RootPath: root, External: ep.cfg, Ownership: "external", Attempt: 1, Phase: phase, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: root, External: ep.cfg}}
					b, err := json.Marshal(j)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
					treeBefore := snapshotMigrationTree(t, beadsDir, root)
					before, _ := os.ReadFile(configfile.ConfigPath(beadsDir))
					var migrateErr error
					stderr := captureStderr(t, func() {
						if reverse {
							migrateErr = runMigrateFromProxiedServer(false, false)
						} else {
							migrateErr = runMigrateToProxiedServer(false, 0, false)
						}
					})
					require.Error(t, migrateErr)
					assert.Contains(t, strings.ToLower(stderr), "externally hosted proxied dolt endpoint")
					after, _ := os.ReadFile(configfile.ConfigPath(beadsDir))
					assert.Equal(t, before, after)
					cfg, _ := configfile.Load(beadsDir)
					assert.Equal(t, want, cfg.GetDoltMode())
					assert.Equal(t, treeBefore, snapshotMigrationTree(t, beadsDir, root))
				})
			}
		}
	}
}

func TestMigrateSharedJournalRootMismatchFailsClosed(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", shared)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600))
	root := filepath.Join(beadsDir, "dolt")
	j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeServer, TargetMode: configfile.DoltModeProxiedServer, Shared: true, RootPath: root, Ownership: "managed-local", Attempt: 1, Phase: migratePrepared, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: root}}
	b, err := json.Marshal(j)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
	require.Error(t, runMigrateToProxiedServer(false, 0, true))
	cfg, err := configfile.Load(beadsDir)
	require.NoError(t, err)
	assert.True(t, cfg.IsDoltServerMode())
}

func TestMigrateSharedJournalMissingYAMLFailsClosed(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", shared)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	root := filepath.Join(shared, "dolt")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".dolt", "repo_state.json"), []byte(`{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`), 0o600))
	j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeServer, TargetMode: configfile.DoltModeProxiedServer, Shared: true, RootPath: root, Ownership: "managed-local", Attempt: 1, Phase: migratePrepared, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: root}}
	b, _ := json.Marshal(j)
	require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
	require.Error(t, runMigrateToProxiedServer(false, 0, true))
}

func TestMigrateNonSharedJournalRejectsPersistedSharedYAML(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1") // stale ambient environment
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0o600))
	root := filepath.Join(beadsDir, "dolt")
	j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeServer, TargetMode: configfile.DoltModeProxiedServer, RootPath: root, Ownership: "managed-local", Attempt: 1, Phase: migrateTargetConfigured, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: root}}
	b, err := json.Marshal(j)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
	err = runMigrateToProxiedServer(false, 0, false)
	require.Error(t, err)
}

func TestMigrateJournalSidecarRootMismatchFailsClosed(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeServer)
	j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeServer, TargetMode: configfile.DoltModeProxiedServer, RootPath: filepath.Join(beadsDir, "dolt"), Ownership: "managed-local", Attempt: 1, Phase: migratePrepared, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: filepath.Join(beadsDir, "other")}}
	b, err := json.Marshal(j)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
	_, err = loadMigrateJournal(beadsDir)
	require.Error(t, err)
}

func TestMigrateJournalExternalTopologyMismatchFailsClosed(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	root := filepath.Join(beadsDir, "dolt")
	ext := &configfile.ExternalDoltConfig{Host: "db.example", Port: 3307}
	for _, tc := range []struct {
		name      string
		ownership string
		journal   *configfile.ExternalDoltConfig
		sidecar   *configfile.ExternalDoltConfig
	}{
		{name: "journal external differs", ownership: "external", journal: &configfile.ExternalDoltConfig{Host: "other.example", Port: 3307}, sidecar: ext},
		{name: "journal unexpected external", ownership: "managed-local", journal: ext, sidecar: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeProxiedServer, TargetMode: configfile.DoltModeServer, RootPath: root, Ownership: tc.ownership, Attempt: 1, Phase: migratePrepared, External: tc.journal, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: root, External: tc.sidecar}}
			b, err := json.Marshal(j)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
			_, err = loadMigrateJournal(beadsDir)
			require.Error(t, err)
		})
	}
}

func TestMigrateJournalLegacyExternalFieldInfersSidecar(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	root := filepath.Join(beadsDir, "dolt")
	ext := &configfile.ExternalDoltConfig{Host: "db.example", Port: 3307}
	j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeProxiedServer, TargetMode: configfile.DoltModeServer, RootPath: root, Ownership: "external", Attempt: 1, Phase: migratePrepared, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: root, External: ext}}
	b, err := json.Marshal(j)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
	got, err := loadMigrateJournal(beadsDir)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ext, got.External)
}

func TestMigrateFromProxiedServer_JournalSidecarChangedFailsClosed(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	root := filepath.Join(beadsDir, "dolt")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".dolt"), 0o755))
	wantSidecar := &configfile.ProxiedServerClientInfo{RootPath: root, LogPath: filepath.Join(root, "server.log")}
	gotSidecar := &configfile.ProxiedServerClientInfo{RootPath: root, LogPath: filepath.Join(root, "changed.log")}
	require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, gotSidecar))
	j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeProxiedServer, TargetMode: configfile.DoltModeServer, RootPath: root, Ownership: "managed-local", Attempt: 1, Phase: migratePrepared, Sidecar: wantSidecar}
	b, err := json.Marshal(j)
	require.NoError(t, err)
	journalPath := migrateJournalPath(beadsDir)
	require.NoError(t, os.WriteFile(journalPath, b, 0o600))
	before := snapshotMigrationTree(t, beadsDir, root)
	var migrateErr error
	captureStderr(t, func() { migrateErr = runMigrateFromProxiedServer(false, false) })
	require.Error(t, migrateErr)
	assert.Equal(t, before, snapshotMigrationTree(t, beadsDir, root))
}
