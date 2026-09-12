package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	storagegit "github.com/steveyegge/beads/internal/storage/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProxiedServerClientInfo(t *testing.T) {
	t.Run("all empty returns nil", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("", "", "", 0, 0, nil)
		require.NoError(t, err)
		assert.Nil(t, info)
	})

	t.Run("port alone is persisted", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("", "", "", 3306, 0, nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, 3306, info.Port)
		assert.Zero(t, info.IdleTimeout)
	})

	t.Run("idle timeout alone is persisted", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("", "", "", 0, 5*time.Minute, nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, 5*time.Minute, info.IdleTimeout)
		assert.Zero(t, info.Port)
	})

	t.Run("never sentinel is persisted and survives a round-trip", func(t *testing.T) {
		dir := t.TempDir()
		info, err := buildProxiedServerClientInfo("", "", "", 0, proxy.IdleTimeoutNever, nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, proxy.IdleTimeoutNever, info.IdleTimeout)
		require.NoError(t, configfile.SaveProxiedServerClientInfo(dir, info))
		loaded, err := configfile.LoadProxiedServerClientInfo(dir)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		assert.Equal(t, proxy.IdleTimeoutNever, loaded.IdleTimeout)
	})

	t.Run("port and idle timeout survive a round-trip via SaveProxiedServerClientInfo", func(t *testing.T) {
		dir := t.TempDir()
		info, err := buildProxiedServerClientInfo("", "", "", 3306, 5*time.Minute, nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		require.NoError(t, configfile.SaveProxiedServerClientInfo(dir, info))
		loaded, err := configfile.LoadProxiedServerClientInfo(dir)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		assert.Equal(t, 3306, loaded.Port)
		assert.Equal(t, 5*time.Minute, loaded.IdleTimeout)
	})

	t.Run("absolute paths pass through cleaned", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("/var/lib/beads/proxieddb", "/etc/dolt/server.yaml", "/var/log/server.log", 0, 0, nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "/var/lib/beads/proxieddb", info.RootPath)
		assert.Equal(t, "/etc/dolt/server.yaml", info.ConfigPath)
		assert.Equal(t, "/var/log/server.log", info.LogPath)
		assert.Nil(t, info.External)
	})

	t.Run("filepath.Clean normalizes redundant separators and . segments", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("/var/lib//beads/./proxieddb", "", "", 0, 0, nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "/var/lib/beads/proxieddb", info.RootPath)
	})

	t.Run("mixed absolute + empty", func(t *testing.T) {
		info, err := buildProxiedServerClientInfo("/var/lib/beads/proxieddb", "", "/var/log/server.log", 0, 0, nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "/var/lib/beads/proxieddb", info.RootPath)
		assert.Equal(t, "", info.ConfigPath)
		assert.Equal(t, "/var/log/server.log", info.LogPath)
	})

	t.Run("relative root path is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("alt-root", "", "", 0, 0, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not absolute")
	})

	t.Run("relative config path is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("", "configs/server.yaml", "", 0, 0, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not absolute")
	})

	t.Run("relative log path is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("", "", "logs/server.log", 0, 0, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not absolute")
	})

	t.Run("absolute paths survive a round-trip through the sidecar resolver", func(t *testing.T) {
		const beadsDir = "/proj/.beads"
		info, err := buildProxiedServerClientInfo("/var/lib/beads/proxieddb", "", "", 0, 0, nil)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, info.RootPath, (&configfile.ProxiedServerClientInfo{RootPath: info.RootPath}).ResolvedRootPath(beadsDir))
	})

	t.Run("external config alone populates External section", func(t *testing.T) {
		ext := &configfile.ExternalDoltConfig{Host: "db.internal", Port: 3306}
		info, err := buildProxiedServerClientInfo("", "", "", 0, 0, ext)
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
		info, err := buildProxiedServerClientInfo("", "", "", 0, 0, ext)
		require.NoError(t, err)
		require.NotNil(t, info.External)
		assert.True(t, info.External.TLSRequired)
		assert.Equal(t, "/etc/beads/client.pem", info.External.TLSCert)
		assert.Equal(t, "/etc/beads/client.key", info.External.TLSKey)
	})

	t.Run("external unix socket config flows through", func(t *testing.T) {
		ext := &configfile.ExternalDoltConfig{Socket: "/var/run/dolt.sock"}
		info, err := buildProxiedServerClientInfo("", "", "", 0, 0, ext)
		require.NoError(t, err)
		require.NotNil(t, info.External)
		assert.Equal(t, "/var/run/dolt.sock", info.External.Socket)
		assert.Empty(t, info.External.Host)
		assert.Zero(t, info.External.Port)
	})

	t.Run("invalid external config is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("", "", "", 0, 0, &configfile.ExternalDoltConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ExternalDoltConfig")
	})

	t.Run("invalid external config with tls cert without key is rejected", func(t *testing.T) {
		_, err := buildProxiedServerClientInfo("", "", "", 0, 0, &configfile.ExternalDoltConfig{
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
		info, err := buildProxiedServerClientInfo("", "", "", 0, 0, ext)
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

func TestComposeProxiedServerMetadataJSON_TeamServer(t *testing.T) {
	t.Run("team-server flag is persisted and round-trips", func(t *testing.T) {
		body, err := composeProxiedServerMetadataJSON(proxiedMetadataInputs{
			dbName:     "beads_team",
			projectID:  "proj-1",
			teamServer: true,
		})
		require.NoError(t, err)
		assert.Contains(t, string(body), `"dolt_team_server": true`)

		var cfg configfile.Config
		require.NoError(t, json.Unmarshal(body, &cfg))
		assert.True(t, cfg.DoltTeamServer)
		assert.True(t, cfg.IsTeamServerManaged())
	})

	t.Run("default omits the field and is not team-server managed", func(t *testing.T) {
		body, err := composeProxiedServerMetadataJSON(proxiedMetadataInputs{
			dbName:    "beads_team",
			projectID: "proj-1",
		})
		require.NoError(t, err)
		assert.NotContains(t, string(body), "dolt_team_server")

		var cfg configfile.Config
		require.NoError(t, json.Unmarshal(body, &cfg))
		assert.False(t, cfg.IsTeamServerManaged())
	})
}

func TestIsTeamServerManaged_RequiresProxiedServerMode(t *testing.T) {
	cfg := configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltTeamServer: true,
	}
	assert.False(t, cfg.IsTeamServerManaged(),
		"team-server semantics are defined for proxied-server mode only")

	cfg.DoltMode = configfile.DoltModeProxiedServer
	assert.True(t, cfg.IsTeamServerManaged())
}

func TestProxiedInitTailRoleIgnoresInheritedGitRouting(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			require.NoError(t, os.Unsetenv(key))
		}
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	globalPath := filepath.Join(home, ".gitconfig")
	require.NoError(t, os.WriteFile(globalPath, nil, 0600))
	runGit := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitenv.ScrubRouting(os.Environ())
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "fixture git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	for _, tc := range []struct {
		name, initial, flag, want string
	}{
		{"explicit", "maintainer", "contributor", "contributor"},
		{"default", "", "", "maintainer"},
		{"retained", "contributor", "", "contributor"},
	} {
		poisons := []string{"repository", "inline_config"}
		if tc.name == "retained" {
			// An absent decoy role must not make init replace the target contributor role.
			poisons = append(poisons, "roleless_repository")
		}
		for _, poison := range poisons {
			t.Run(tc.name+"/"+poison, func(t *testing.T) {
				target, decoy := t.TempDir(), t.TempDir()
				for _, dir := range []string{target, decoy} {
					runGit(t, dir, "init", "--quiet")
					runGit(t, dir, "config", "--local", "core.hooksPath", ".git/hooks")
				}
				if tc.initial != "" {
					runGit(t, target, "config", "--local", "beads.role", tc.initial)
				}
				if poison != "roleless_repository" {
					runGit(t, decoy, "config", "--local", "beads.role", "decoy-role")
				}
				if poison != "inline_config" {
					t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
					t.Setenv("GIT_WORK_TREE", decoy)
				} else {
					t.Setenv("GIT_CONFIG_COUNT", "1")
					t.Setenv("GIT_CONFIG_KEY_0", "beads.role")
					t.Setenv("GIT_CONFIG_VALUE_0", "injected-role")
				}
				if poison == "roleless_repository" {
					inherited := exec.Command("git", "config", "--get", "beads.role")
					inherited.Dir = target
					out, err := inherited.CombinedOutput()
					var exitErr *exec.ExitError
					require.ErrorAs(t, err, &exitErr, "roleless decoy precondition: %s", out)
					require.Equal(t, 1, exitErr.ExitCode())
					require.Empty(t, out)
				}
				env := os.Environ()
				before, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
				require.NoError(t, err)
				gitUC := storagegit.NewGitProvider(target).GitUseCase()
				require.True(t, gitUC.IsGitRepo(t.Context()), "valid inherited repository must reach role branch")
				cmd := &cobra.Command{}
				cmd.Flags().Bool("setup-exclude", false, "")
				// Existing flags exclude all filesystem integrations; nil fsUseCase must stay unused.
				in := initProxiedServerInput{roleFlag: tc.flag, quiet: true, stealth: true, skipHooks: true, skipAgents: true}
				require.NoError(t, runInitProxiedServerTail(cmd, t.Context(), in, runInitTailContext{gitUC: gitUC}))
				require.Equal(t, tc.want, runGit(t, target, "config", "--local", "--get", "beads.role"))
				after, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
				require.NoError(t, err)
				require.Equal(t, string(before), string(after), "proxied tail changed decoy")
				globalAfter, err := os.ReadFile(globalPath)
				require.NoError(t, err)
				require.Empty(t, globalAfter)
				require.True(t, slices.Equal(env, os.Environ()), "proxied tail changed inherited environment")
			})
		}
	}
}
