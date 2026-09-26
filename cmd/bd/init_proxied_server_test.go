package main

import (
	"context"
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
	"github.com/steveyegge/beads/internal/storage/domain"
	storagefs "github.com/steveyegge/beads/internal/storage/fs"
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
		for _, poison := range []string{"repository", "inline_config"} {
			t.Run(tc.name+"/"+poison, func(t *testing.T) {
				target, decoy := t.TempDir(), t.TempDir()
				for _, dir := range []string{target, decoy} {
					runGit(t, dir, "init", "--quiet")
					runGit(t, dir, "config", "--local", "core.hooksPath", ".git/hooks")
				}
				if tc.initial != "" {
					runGit(t, target, "config", "--local", "beads.role", tc.initial)
				}
				runGit(t, decoy, "config", "--local", "beads.role", "decoy-role")
				if poison == "repository" {
					t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
					t.Setenv("GIT_WORK_TREE", decoy)
				} else {
					t.Setenv("GIT_CONFIG_COUNT", "1")
					t.Setenv("GIT_CONFIG_KEY_0", "beads.role")
					t.Setenv("GIT_CONFIG_VALUE_0", "injected-role")
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

type proxiedRoleProbeUseCase struct {
	domain.GitUseCase
	roleReads int
}

func (u *proxiedRoleProbeUseCase) BeadsRole(ctx context.Context) (string, bool, error) {
	u.roleReads++
	return u.GitUseCase.BeadsRole(ctx)
}

func TestProxiedInitTailRoleProbeUsesSelectedDirectory(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			require.NoError(t, os.Unsetenv(key))
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	runGit := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitenv.ScrubRouting(os.Environ())
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "fixture git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	for _, tc := range []struct {
		name, kind, initial, flag, want string
	}{
		{"explicit", "ordinary", "maintainer", "contributor", "contributor"},
		{"default", "ordinary", "", "", "maintainer"},
		{"retained", "ordinary", "contributor", "", "contributor"},
		{"bare_explicit", "bare", "maintainer", "contributor", "contributor"},
		{"bare_default", "bare", "", "", "maintainer"},
		{"bare_retained", "bare", "contributor", "", "contributor"},
		{"nonrepo_with_decoy_and_global_role", "nonrepo", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, target, decoy := t.TempDir(), t.TempDir(), t.TempDir()
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
				t.Setenv(key, home)
			}
			globalData := ""
			if tc.kind == "nonrepo" {
				globalData = "[beads]\n\trole = global-role\n"
			}
			globalPath := filepath.Join(home, ".gitconfig")
			require.NoError(t, os.WriteFile(globalPath, []byte(globalData), 0600))
			runGit(t, decoy, "init", "--quiet")
			runGit(t, decoy, "config", "--local", "core.hooksPath", ".git/hooks")
			runGit(t, decoy, "config", "--local", "beads.role", "decoy-role")
			if tc.kind == "bare" {
				runGit(t, target, "init", "--bare", "--quiet")
				runGit(t, target, "config", "--local", "core.hooksPath", filepath.Join(target, "hooks"))
			} else if tc.kind == "ordinary" {
				runGit(t, target, "init", "--quiet")
				runGit(t, target, "config", "--local", "core.hooksPath", ".git/hooks")
			}
			if tc.initial != "" {
				runGit(t, target, "config", "--local", "beads.role", tc.initial)
			}
			probe := exec.Command("git", "rev-parse", "--git-dir")
			probe.Dir, probe.Env = target, gitenv.ScrubRouting(os.Environ())
			require.Equal(t, tc.kind != "nonrepo", probe.Run() == nil, "owned target repository precondition")
			if tc.kind == "nonrepo" {
				t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
			} else {
				invalid := filepath.Join(t.TempDir(), "invalid-git-dir")
				require.NoError(t, os.WriteFile(invalid, []byte("not a Git directory\n"), 0600))
				t.Setenv("GIT_DIR", invalid)
			}
			t.Setenv("GIT_WORK_TREE", decoy)
			t.Chdir(decoy)
			base := storagegit.NewGitProvider(target).GitUseCase()
			require.Equal(t, tc.kind == "nonrepo", base.IsGitRepo(t.Context()), "inherited probe precondition")
			if tc.kind == "nonrepo" {
				role, found, err := base.BeadsRole(t.Context())
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, "global-role", role, "global config alone must not manufacture a repository")
			}
			gitUC := &proxiedRoleProbeUseCase{GitUseCase: base}
			before, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
			require.NoError(t, err)
			env := os.Environ()
			cmd := &cobra.Command{}
			cmd.Flags().Bool("setup-exclude", false, "")
			in := initProxiedServerInput{roleFlag: tc.flag, quiet: true, stealth: true, skipHooks: true, skipAgents: true}
			require.NoError(t, runInitProxiedServerTail(cmd, t.Context(), in, runInitTailContext{workDir: target, gitUC: gitUC}))
			require.Zero(t, gitUC.roleReads, "selected tail must bypass the inherited role provider")
			if tc.kind == "nonrepo" {
				entries, err := os.ReadDir(target)
				require.NoError(t, err)
				require.Empty(t, entries)
			} else {
				require.Equal(t, tc.want, runGit(t, target, "config", "--local", "--get", "beads.role"))
			}
			after, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "tail changed decoy config")
			globalAfter, err := os.ReadFile(globalPath)
			require.NoError(t, err)
			require.Equal(t, globalData, string(globalAfter))
			require.True(t, slices.Equal(env, os.Environ()), "tail changed inherited environment")
		})
	}
}

type initTailForkObservation struct {
	domain.BeadsDirFSUseCase
	calls int
}

func (f *initTailForkObservation) SetupForkExclude(context.Context, bool) error {
	f.calls++
	return nil // Observe fork selection; the exclude writer has separate coverage.
}

func TestProxiedInitTailGitUsesSelectedDirectory(t *testing.T) {
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
	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitenv.ScrubRouting(os.Environ())
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "fixture git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return data
	}
	for _, name := range []string{"decoy", "invalid", "nonrepo"} {
		t.Run(name, func(t *testing.T) {
			target, decoy := t.TempDir(), t.TempDir()
			for _, dir := range []string{target, decoy} {
				if dir != target || name != "nonrepo" {
					runGit(dir, "init", "--quiet")
					for key, value := range map[string]string{"user.name": "Fixture", "user.email": "fixture@example.test", "commit.gpgSign": "false", "core.hooksPath": ".git/hooks"} {
						runGit(dir, "config", "--local", key, value)
					}
					require.NoError(t, os.WriteFile(filepath.Join(dir, "seed"), []byte("seed\n"), 0600))
					runGit(dir, "add", "seed")
					runGit(dir, "-c", "core.hooksPath=", "commit", "-m", "seed")
				}
				require.NoError(t, os.Mkdir(filepath.Join(dir, ".beads"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".beads", "artifact"), []byte(dir), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("optional\n"), 0600))
			}
			runGit(decoy, "remote", "add", "upstream", "https://example.test/decoy.git")
			runGit(decoy, "config", "beads.role", "decoy-role")
			foreignIndex := filepath.Join(t.TempDir(), "foreign-index")
			require.NoError(t, os.WriteFile(foreignIndex, read(filepath.Join(decoy, ".git", "index")), 0600))
			preserved := map[string][]byte{}
			for _, path := range []string{foreignIndex, filepath.Join(decoy, ".git", "index"), filepath.Join(decoy, ".git", "config")} {
				preserved[path] = read(path)
			}
			decoyHead := runGit(decoy, "rev-parse", "HEAD")
			gitDir := filepath.Join(decoy, ".git")
			if name == "invalid" {
				gitDir = filepath.Join(t.TempDir(), "missing-git-dir")
			}
			t.Setenv("GIT_DIR", gitDir)
			t.Setenv("GIT_WORK_TREE", decoy)
			t.Setenv("GIT_INDEX_FILE", foreignIndex)
			t.Chdir(decoy)
			env := os.Environ()
			cmd := &cobra.Command{}
			cmd.Flags().Bool("setup-exclude", false, "")
			fs := &initTailForkObservation{}
			in := initProxiedServerInput{skipHooks: true, skipAgents: true, nonInteractive: true}
			stderr := captureStderr(t, func() {
				require.NoError(t, runInitProxiedServerTail(cmd, t.Context(), in, runInitTailContext{workDir: target, beadsDir: filepath.Join(target, ".beads"), useLocalBeads: true, fsUseCase: fs, gitUC: storagegit.NewGitProvider(target).GitUseCase()}))
			})
			require.Zero(t, fs.calls, "decoy fork selected")
			require.NotContains(t, stderr, "Git upstream not configured", "decoy remotes selected")
			if name == "nonrepo" {
				_, err := os.Stat(filepath.Join(target, ".git"))
				require.True(t, os.IsNotExist(err), "tail manufactured a repository")
			} else {
				require.Equal(t, "2", runGit(target, "rev-list", "--count", "HEAD"), "target artifacts were not committed")
				require.Equal(t, target, runGit(target, "show", "HEAD:.beads/artifact"))
				require.Equal(t, "optional", runGit(target, "show", "HEAD:CLAUDE.md"))
				require.Empty(t, runGit(target, "diff", "--cached", "--name-only"))
			}
			require.Equal(t, decoyHead, runGit(decoy, "rev-parse", "HEAD"), "decoy HEAD changed")
			for path, before := range preserved {
				require.Equal(t, before, read(path), "tail changed %s", path)
			}
			require.True(t, slices.Equal(env, os.Environ()), "tail changed inherited environment")
		})
	}
}

func TestProxiedInitExcludeUsesSelectedDirectory(t *testing.T) {
	for _, name := range []string{"stealth", "setup_exclude", "target_fork", "decoy_fork"} {
		t.Run(name, func(t *testing.T) {
			worktree, decoy, commonExclude, privateExclude := newInitExcludeRepos(t)
			if name == "target_fork" {
				initExcludeGit(t, worktree, "remote", "add", "upstream", "https://example.test/target.git")
			} else if name == "decoy_fork" {
				initExcludeGit(t, decoy, "remote", "add", "upstream", "https://example.test/decoy.git")
			}
			storage := t.TempDir()
			t.Setenv("BEADS_DIR", storage)
			t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
			t.Setenv("GIT_WORK_TREE", decoy)
			decoyConfig, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
			require.NoError(t, err)
			env := os.Environ()
			// Construct the same selected provider/adapter used before the real init tail.
			fsUseCase := storagefs.NewFileSystemProvider(worktree, newBeadsDirTemplates(), newInitFileSystemAdapters(worktree)).BeadsDirFSUseCase()
			cmd := &cobra.Command{}
			cmd.Flags().Bool("setup-exclude", name == "setup_exclude", "")
			in := initProxiedServerInput{stealth: name == "stealth", quiet: true, nonInteractive: true, skipHooks: true, skipAgents: true}
			tail := runInitTailContext{workDir: worktree, beadsDir: storage, fsUseCase: fsUseCase, gitUC: storagegit.NewGitProvider(worktree).GitUseCase()}
			for range 2 {
				stderr := captureStderr(t, func() {
					if in.stealth {
						require.NoError(t, fsUseCase.SetupStealthMode(t.Context(), false))
					}
					require.NoError(t, runInitProxiedServerTail(cmd, t.Context(), in, tail))
				})
				require.Empty(t, stderr)
			}
			want := "# preserved\r\n"
			if in.stealth {
				want += "\r\n# Beads stealth mode (added by bd init --stealth)\r\n.beads/\r\n.claude/settings.local.json\r\n"
			} else if name != "decoy_fork" {
				want += "\r\n# Beads fork protection (bd init)\r\n.beads/\r\n**/RECOVERY*.md\r\n**/SESSION*.md\r\n"
			}
			data, err := os.ReadFile(commonExclude)
			require.NoError(t, err)
			require.Equal(t, want, string(data), "selected common exclude bytes")
			data, err = os.ReadFile(filepath.Join(decoy, ".git", "info", "exclude"))
			require.NoError(t, err)
			require.Equal(t, "# preserved\r\n", string(data))
			data, err = os.ReadFile(filepath.Join(decoy, ".git", "config"))
			require.NoError(t, err)
			require.Equal(t, decoyConfig, data)
			_, err = os.Stat(privateExclude)
			require.ErrorIs(t, err, os.ErrNotExist)
			entries, err := os.ReadDir(storage)
			require.NoError(t, err)
			require.Empty(t, entries, "storage location became the Git project")
			require.True(t, slices.Equal(env, os.Environ()))
		})
	}
}
