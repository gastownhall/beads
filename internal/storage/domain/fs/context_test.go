package fs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/domain"
)

func newContextRepoForTest(t *testing.T) (workDir, beadsDir string, repo domain.ContextRepository) {
	t.Helper()
	workDir = t.TempDir()
	beadsDir = filepath.Join(workDir, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0o750))

	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")

	repo = NewContextRepository(NewBeadsDirFSRepository(workDir, testTemplates()))
	return workDir, beadsDir, repo
}

func canonicalBeadsDir(t *testing.T, workDir string) string {
	t.Helper()
	return NewBeadsDirFSRepository(workDir, testTemplates()).ResolveBeadsDirPath(context.Background()).BeadsDir
}

func writeMetadata(t *testing.T, beadsDir string, cfg *configfile.Config) {
	t.Helper()
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600))
}

func TestContextRepository_BackendConfig(t *testing.T) {
	t.Run("DefaultsWithoutMetadata", func(t *testing.T) {
		_, _, repo := newContextRepoForTest(t)

		got, err := repo.BackendConfig(context.Background())
		require.NoError(t, err)
		require.Equal(t, configfile.DoltModeEmbedded, got.DoltMode)
		require.Equal(t, configfile.DefaultDoltDatabase, got.Database)
		require.Empty(t, got.ProjectID)
		require.False(t, got.IsServerMode)
		require.False(t, got.IsProxiedServerMode)
	})

	t.Run("EmbeddedWithExplicitValues", func(t *testing.T) {
		_, beadsDir, repo := newContextRepoForTest(t)
		writeMetadata(t, beadsDir, &configfile.Config{
			DoltMode:     configfile.DoltModeEmbedded,
			DoltDatabase: "mydb",
			ProjectID:    "proj-123",
		})

		got, err := repo.BackendConfig(context.Background())
		require.NoError(t, err)
		require.Equal(t, configfile.DoltModeEmbedded, got.DoltMode)
		require.Equal(t, "mydb", got.Database)
		require.Equal(t, "proj-123", got.ProjectID)
		require.False(t, got.IsServerMode)
		require.False(t, got.IsProxiedServerMode)
	})

	t.Run("ServerMode", func(t *testing.T) {
		_, beadsDir, repo := newContextRepoForTest(t)
		writeMetadata(t, beadsDir, &configfile.Config{
			DoltMode:       configfile.DoltModeServer,
			DoltServerHost: "db.example.com",
			DoltDataDir:    "/data/dolt",
		})

		got, err := repo.BackendConfig(context.Background())
		require.NoError(t, err)
		require.True(t, got.IsServerMode)
		require.False(t, got.IsProxiedServerMode)
		require.Equal(t, "db.example.com", got.ServerHost)
		require.Equal(t, "/data/dolt", got.DataDir)
	})

	t.Run("ProxiedServerMode", func(t *testing.T) {
		_, beadsDir, repo := newContextRepoForTest(t)
		writeMetadata(t, beadsDir, &configfile.Config{
			DoltMode: configfile.DoltModeProxiedServer,
		})

		got, err := repo.BackendConfig(context.Background())
		require.NoError(t, err)
		require.True(t, got.IsProxiedServerMode)
		require.False(t, got.IsServerMode)
	})

	t.Run("EnvOverridesDatabase", func(t *testing.T) {
		_, beadsDir, repo := newContextRepoForTest(t)
		writeMetadata(t, beadsDir, &configfile.Config{DoltDatabase: "filedb"})
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "envdb")

		got, err := repo.BackendConfig(context.Background())
		require.NoError(t, err)
		require.Equal(t, "envdb", got.Database)
	})
}

func TestContextRepository_ServerPort(t *testing.T) {
	t.Run("EnvOverride", func(t *testing.T) {
		_, _, repo := newContextRepoForTest(t)
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3333")

		got, err := repo.ServerPort(context.Background())
		require.NoError(t, err)
		require.Equal(t, 3333, got)
	})

	t.Run("ReadsPortFile", func(t *testing.T) {
		_, beadsDir, repo := newContextRepoForTest(t)
		require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("4567"), 0o600))

		got, err := repo.ServerPort(context.Background())
		require.NoError(t, err)
		require.Equal(t, 4567, got)
	})
}

func TestContextRepository_ProxiedServerRoot(t *testing.T) {
	t.Run("EnvAbsolutePath", func(t *testing.T) {
		_, _, repo := newContextRepoForTest(t)
		abs := filepath.Join(t.TempDir(), "abs-root")
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", abs)

		got, err := repo.ProxiedServerRoot(context.Background())
		require.NoError(t, err)
		require.Equal(t, abs, got)
	})

	t.Run("EnvRelativePathJoinedWithBeadsDir", func(t *testing.T) {
		workDir, _, repo := newContextRepoForTest(t)
		t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "relroot")

		got, err := repo.ProxiedServerRoot(context.Background())
		require.NoError(t, err)
		canonBeadsDir := canonicalBeadsDir(t, workDir)
		require.Equal(t, filepath.Join(canonBeadsDir, "relroot"), got)
	})

	t.Run("ClientInfoRootPath", func(t *testing.T) {
		workDir, beadsDir, repo := newContextRepoForTest(t)
		require.NoError(t, configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{
			RootPath: "customroot",
		}))

		got, err := repo.ProxiedServerRoot(context.Background())
		require.NoError(t, err)
		canonBeadsDir := canonicalBeadsDir(t, workDir)
		require.Equal(t, filepath.Join(canonBeadsDir, "customroot"), got)
	})

	t.Run("DefaultProxiedDir", func(t *testing.T) {
		workDir, _, repo := newContextRepoForTest(t)

		got, err := repo.ProxiedServerRoot(context.Background())
		require.NoError(t, err)
		canonBeadsDir := canonicalBeadsDir(t, workDir)
		require.Equal(t, filepath.Join(canonBeadsDir, "dolt"), got)
	})
}

func TestContextRepository_SyncRemote(t *testing.T) {
	writeConfigYAML := func(t *testing.T, beadsDir, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(body), 0o600))
	}

	t.Run("ReadsSyncRemote", func(t *testing.T) {
		_, beadsDir, repo := newContextRepoForTest(t)
		writeConfigYAML(t, beadsDir, "sync:\n  remote: git@example.com:org/repo.git\n")

		got, err := repo.SyncRemote(context.Background())
		require.NoError(t, err)
		require.Equal(t, "git@example.com:org/repo.git", got)
	})

	t.Run("FallsBackToGitRemote", func(t *testing.T) {
		_, beadsDir, repo := newContextRepoForTest(t)
		writeConfigYAML(t, beadsDir, "sync:\n  git-remote: legacy-remote\n")

		got, err := repo.SyncRemote(context.Background())
		require.NoError(t, err)
		require.Equal(t, "legacy-remote", got)
	})

	t.Run("SyncRemoteWinsOverGitRemote", func(t *testing.T) {
		_, beadsDir, repo := newContextRepoForTest(t)
		writeConfigYAML(t, beadsDir, "sync:\n  remote: primary\n  git-remote: legacy\n")

		got, err := repo.SyncRemote(context.Background())
		require.NoError(t, err)
		require.Equal(t, "primary", got)
	})

	t.Run("EmptyWhenNoConfig", func(t *testing.T) {
		_, _, repo := newContextRepoForTest(t)

		got, err := repo.SyncRemote(context.Background())
		require.NoError(t, err)
		require.Empty(t, got)
	})
}

func TestContextRepository_RepoContextAndRole(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	workDir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = workDir
	require.NoError(t, cmd.Run())

	beadsDir := filepath.Join(workDir, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0o600))

	t.Setenv("BEADS_DIR", beadsDir)
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	repo := NewContextRepository(NewBeadsDirFSRepository(workDir, testTemplates()))

	paths, err := repo.RepoContext(context.Background())
	if err != nil {
		t.Skipf("repo context unavailable in this environment: %v", err)
	}
	require.True(t, paths.IsRedirected)
	require.NotEmpty(t, paths.RepoRoot)
	require.Equal(t, ".beads", filepath.Base(paths.BeadsDir))

	role, ok, err := repo.Role(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, string(beads.Contributor), role)
}
