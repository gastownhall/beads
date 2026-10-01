package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/stretchr/testify/require"
)

func (s *testSuite) TestIsGitRepo_FalseOutsideRepo() {
	ok, err := s.repo.IsGitRepo(s.Ctx())
	s.Require().NoError(err)
	s.False(ok)
}

func (s *testSuite) TestIsGitRepo_TrueInsideRepo() {
	s.gitInit()
	ok, err := s.repo.IsGitRepo(s.Ctx())
	s.Require().NoError(err)
	s.True(ok)
}

func (s *testSuite) TestIsBareGitRepo_FalseForRegularRepo() {
	s.gitInit()
	bare, err := s.repo.IsBareGitRepo(s.Ctx())
	s.Require().NoError(err)
	s.False(bare)
}

func (s *testSuite) TestIsBareGitRepo_FalseOutsideRepo() {
	bare, err := s.repo.IsBareGitRepo(s.Ctx())
	s.Require().NoError(err)
	s.False(bare)
}

func (s *testSuite) TestInit_CreatesRepo() {
	s.Require().NoError(s.repo.Init(s.Ctx()))
	ok, err := s.repo.IsGitRepo(s.Ctx())
	s.Require().NoError(err)
	s.True(ok)
}

func (s *testSuite) TestConfig_GetMissing() {
	s.gitInit()
	value, found, err := s.repo.GetConfig(s.Ctx(), "beads.role")
	s.Require().NoError(err)
	s.False(found)
	s.Empty(value)
}

func (s *testSuite) TestConfig_RoundTrip() {
	s.gitInit()
	s.Require().NoError(s.repo.SetConfig(s.Ctx(), "beads.role", "maintainer"))

	value, found, err := s.repo.GetConfig(s.Ctx(), "beads.role")
	s.Require().NoError(err)
	s.True(found)
	s.Equal("maintainer", value)
}

func (s *testSuite) TestConfig_ReadFailuresAreNotMissing() {
	s.gitInit()
	configPath := filepath.Join(s.tmpDir, ".git", "config")
	original, err := os.ReadFile(configPath)
	s.Require().NoError(err)
	for _, tc := range []struct {
		name, key     string
		exitCode      int
		roleReadFails bool
	}{
		{"malformed_config", "beads.role", 128, true},
		{"invalid_key", "invalid", 1, false},
		// A beads.role read scrubs inherited config suppression, so a bad
		// GIT_CONFIG_NOSYSTEM value never reaches that one key. Read a key that
		// still inherits it, so the env-borne failure stays covered, and pin the
		// role read's immunity below rather than dropping the case.
		{"invalid_routing_boolean", "core.bare", 128, false},
	} {
		s.Run(tc.name, func() {
			switch tc.name {
			case "malformed_config":
				s.Require().NoError(os.WriteFile(configPath, []byte("[broken\n"), 0600))
				t := s.T()
				t.Cleanup(func() {
					if err := os.WriteFile(configPath, original, 0600); err != nil {
						t.Errorf("restore repository config: %v", err)
					}
				})
			case "invalid_routing_boolean":
				s.T().Setenv("GIT_CONFIG_NOSYSTEM", "not-a-boolean")
			}
			value, found, err := s.repo.GetConfig(s.Ctx(), tc.key)
			s.Require().Error(err)
			s.False(found)
			s.Empty(value)
			var exitErr *exec.ExitError
			s.Require().ErrorAs(err, &exitErr)
			s.Equal(tc.exitCode, exitErr.ExitCode())
			diagnostic := strings.TrimSpace(string(exitErr.Stderr))
			s.Require().NotEmpty(diagnostic)
			s.Contains(err.Error(), diagnostic)
			if tc.roleReadFails {
				_, _, roleErr := domain.NewGitUseCase(s.tmpDir, s.repo).BeadsRole(s.Ctx())
				s.Require().Error(roleErr)
				s.ErrorAs(roleErr, &exitErr)
			} else if tc.name == "invalid_routing_boolean" {
				role, found, roleErr := domain.NewGitUseCase(s.tmpDir, s.repo).BeadsRole(s.Ctx())
				s.Require().NoError(roleErr)
				s.False(found)
				s.Empty(role)
			}
		})
	}
}

// TestConfig_ReadFailuresAreNotMissing's "invalid_routing_boolean" row used to
// poison GIT_CONFIG_NOSYSTEM and expect the beads.role read to fail. beads.role
// reads now scrub inherited suppression, so that poison never reaches git for
// this key; that row therefore reads a key which still inherits it (core.bare)
// and asserts the role read's immunity alongside. This test covers the half that
// row cannot: under the same poison, the role read returns the value it was
// given rather than merely resolving as absent.
func (s *testSuite) TestConfig_BeadsRoleScrubsInheritedRoutingEnv() {
	s.gitInit()
	s.Require().NoError(s.repo.SetConfig(s.Ctx(), "beads.role", "maintainer"))
	s.T().Setenv("GIT_CONFIG_NOSYSTEM", "not-a-boolean")

	// beads.role is read with routing scrubbed, so the poisoned value is gone
	// before git parses its environment.
	value, found, err := s.repo.GetConfig(s.Ctx(), "beads.role")
	s.Require().NoError(err)
	s.True(found)
	s.Equal("maintainer", value)

	// Keys outside that scrub keep the inherited environment, so the same poison
	// still surfaces as a read error rather than as an absent key.
	value, found, err = s.repo.GetConfig(s.Ctx(), "user.name")
	s.Require().Error(err)
	s.False(found)
	s.Empty(value)
	var exitErr *exec.ExitError
	s.Require().ErrorAs(err, &exitErr)
	s.Equal(128, exitErr.ExitCode())
}

// Regression guard: pre-canceled contexts already propagated before the config fix.
func (s *testSuite) TestConfig_CancellationIsNotMissing() {
	ctx, cancel := context.WithCancel(s.Ctx())
	cancel()
	value, found, err := s.repo.GetConfig(ctx, "beads.role")
	s.ErrorIs(err, context.Canceled)
	s.False(found)
	s.Empty(value)
}

func (s *testSuite) TestConfig_SetEmptyKeyErrors() {
	s.gitInit()
	err := s.repo.SetConfig(s.Ctx(), "", "x")
	s.Require().Error(err)
}

func (s *testSuite) TestRemote_GetMissing() {
	s.gitInit()
	url, found, err := s.repo.GetRemoteURL(s.Ctx(), "origin")
	s.Require().NoError(err)
	s.False(found)
	s.Empty(url)
}

func (s *testSuite) TestRemote_AddedRemoteVisible() {
	s.gitInit()
	s.run("git", "remote", "add", "origin", "https://example.com/repo.git")

	url, found, err := s.repo.GetRemoteURL(s.Ctx(), "origin")
	s.Require().NoError(err)
	s.True(found)
	s.Equal("https://example.com/repo.git", url)
}

func (s *testSuite) TestRemote_ListEmpty() {
	s.gitInit()
	names, err := s.repo.ListRemoteNames(s.Ctx())
	s.Require().NoError(err)
	s.Empty(names)
}

func (s *testSuite) TestRemote_ListMultiple() {
	s.gitInit()
	s.run("git", "remote", "add", "origin", "https://example.com/a.git")
	s.run("git", "remote", "add", "upstream", "https://example.com/b.git")

	names, err := s.repo.ListRemoteNames(s.Ctx())
	s.Require().NoError(err)
	s.ElementsMatch([]string{"origin", "upstream"}, names)
}

func (s *testSuite) TestCurrentBranch_NoErrorOnFreshRepo() {
	s.gitInit()
	_, err := s.repo.CurrentBranch(s.Ctx())
	s.Require().NoError(err)
}

func (s *testSuite) TestBranchHasUpstream_FalseWhenUnset() {
	s.gitInit()
	s.writeFile("a.txt", "x")
	s.run("git", "add", "a.txt")
	s.run("git", "commit", "-q", "-m", "init")

	branch, err := s.repo.CurrentBranch(s.Ctx())
	s.Require().NoError(err)
	s.Require().NotEmpty(branch)

	has, err := s.repo.BranchHasUpstream(s.Ctx(), branch)
	s.Require().NoError(err)
	s.False(has)
}

func (s *testSuite) TestBranchHasUpstream_TrueWhenSet() {
	s.gitInit()
	s.writeFile("a.txt", "x")
	s.run("git", "add", "a.txt")
	s.run("git", "commit", "-q", "-m", "init")

	branch, err := s.repo.CurrentBranch(s.Ctx())
	s.Require().NoError(err)
	s.run("git", "config", "branch."+branch+".remote", "origin")
	s.run("git", "config", "branch."+branch+".merge", "refs/heads/"+branch)

	has, err := s.repo.BranchHasUpstream(s.Ctx(), branch)
	s.Require().NoError(err)
	s.True(has)
}

func (s *testSuite) TestAdd_StageAndCommitFile() {
	s.gitInit()
	s.writeFile("a.txt", "x")

	s.Require().NoError(s.repo.Add(s.Ctx(), "a.txt"))
	result, err := s.repo.Commit(s.Ctx(), domain.GitCommitParams{Message: "test"})
	s.Require().NoError(err)
	s.True(result.DidCommit)
}

func (s *testSuite) TestCommit_NoVerifyBypassesHook() {
	s.gitInit()
	hookPath := filepath.Join(s.tmpDir, ".git", "hooks", "pre-commit")
	s.Require().NoError(os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0755)) //nolint:gosec // test hook
	s.writeFile("a.txt", "x")
	s.Require().NoError(s.repo.Add(s.Ctx(), "a.txt"))

	result, err := s.repo.Commit(s.Ctx(), domain.GitCommitParams{Message: "test", NoVerify: true})
	s.Require().NoError(err)
	s.True(result.DidCommit)
}

func (s *testSuite) TestCommit_SkipHooksBypassesPrepareCommitMsgHook() {
	s.gitInit()
	hookPath := filepath.Join(s.tmpDir, ".git", "hooks", "prepare-commit-msg")
	s.Require().NoError(os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0755)) //nolint:gosec // test hook
	s.writeFile("a.txt", "x")
	s.Require().NoError(s.repo.Add(s.Ctx(), "a.txt"))

	result, err := s.repo.Commit(s.Ctx(), domain.GitCommitParams{Message: "test", SkipHooks: true})
	s.Require().NoError(err)
	s.True(result.DidCommit)
}

func (s *testSuite) TestCommit_NothingToCommitDidCommitFalse() {
	s.gitInit()
	s.writeFile("a.txt", "x")
	s.Require().NoError(s.repo.Add(s.Ctx(), "a.txt"))
	_, err := s.repo.Commit(s.Ctx(), domain.GitCommitParams{Message: "first"})
	s.Require().NoError(err)

	result, err := s.repo.Commit(s.Ctx(), domain.GitCommitParams{Message: "second"})
	s.Require().NoError(err)
	s.False(result.DidCommit)
	s.True(strings.Contains(string(result.Output), "nothing to commit"))
}

func (s *testSuite) TestAdd_EmptyPathsErrors() {
	s.gitInit()
	err := s.repo.Add(s.Ctx())
	s.Require().Error(err)
}

func (s *testSuite) TestIsJujutsuRepo_FalseOnTempDir() {
	ok, err := s.repo.IsJujutsuRepo(s.Ctx())
	s.Require().NoError(err)
	s.False(ok)
}

func (s *testSuite) TestIsJujutsuRepo_TrueWhenJJDirPresent() {
	s.Require().NoError(os.MkdirAll(filepath.Join(s.tmpDir, ".jj"), 0700))
	ok, err := s.repo.IsJujutsuRepo(s.Ctx())
	s.Require().NoError(err)
	s.True(ok)
}

func (s *testSuite) TestIsColocatedJJGit_FalseOnTempDir() {
	s.gitInit()
	ok, err := s.repo.IsColocatedJJGit(s.Ctx())
	s.Require().NoError(err)
	s.False(ok)
}

func (s *testSuite) TestIsColocatedJJGit_TrueWhenBothPresent() {
	s.gitInit()
	s.Require().NoError(os.MkdirAll(filepath.Join(s.tmpDir, ".jj"), 0700))
	ok, err := s.repo.IsColocatedJJGit(s.Ctx())
	s.Require().NoError(err)
	s.True(ok)
}

func (s *testSuite) TestExec_HappensInWorkDir() {
	// Confirms cmd.Dir is honored: chdir away, then init via repo bound to tmpDir.
	wd, err := os.Getwd()
	s.Require().NoError(err)
	defer func() { _ = os.Chdir(wd) }()
	s.Require().NoError(os.Chdir(s.T().TempDir())) // unrelated working directory

	s.Require().NoError(s.repo.Init(s.Ctx()))

	info, err := os.Stat(filepath.Join(s.tmpDir, ".git"))
	s.Require().NoError(err)
	s.True(info.IsDir())
}

func TestRoleConfigIgnoresInheritedGitRouting(t *testing.T) {
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
	runGit := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitenv.ScrubRouting(os.Environ())
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "fixture git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	for _, tc := range []struct {
		name, local, global, want string
	}{
		{"repository", "maintainer", "", "maintainer"},
		{"inline_config", "maintainer", "", "maintainer"},
		{"default_global", "", "contributor", "contributor"},
		{"absent", "", "", ""},
		{"literal", "custom role = exact", "", "custom role = exact"},
		{"empty", " \t ", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, decoy := t.TempDir(), t.TempDir()
			for _, dir := range []string{target, decoy} {
				runGit(t, dir, "init", "--quiet")
				runGit(t, dir, "config", "--local", "core.hooksPath", ".git/hooks")
			}
			runGit(t, target, "config", "--local", "test.marker", "target")
			runGit(t, decoy, "config", "--local", "test.marker", "decoy")
			runGit(t, decoy, "config", "--local", "beads.role", "decoy-role")
			if tc.local != "" {
				runGit(t, target, "config", "--local", "beads.role", tc.local)
			}
			global := ""
			if tc.global != "" {
				global = "[beads]\n\trole = " + tc.global + "\n"
			}
			globalPath := filepath.Join(home, ".gitconfig")
			require.NoError(t, os.WriteFile(globalPath, []byte(global), 0600))
			wantGeneric, changedDir := "decoy", decoy
			if tc.name == "inline_config" {
				t.Setenv("GIT_CONFIG_COUNT", "2")
				t.Setenv("GIT_CONFIG_KEY_0", "beads.role")
				t.Setenv("GIT_CONFIG_VALUE_0", "injected-role")
				t.Setenv("GIT_CONFIG_KEY_1", "test.marker")
				t.Setenv("GIT_CONFIG_VALUE_1", "injected-marker")
				wantGeneric, changedDir = "injected-marker", target
			} else {
				t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
				t.Setenv("GIT_WORK_TREE", decoy)
			}
			env := os.Environ()
			before, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
			require.NoError(t, err)
			repo := NewGitRepository(target)
			useCase := domain.NewGitUseCase(target, repo)
			got, found, err := useCase.BeadsRole(t.Context())
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.want != "", found)
			require.NoError(t, useCase.SetBeadsRole(t.Context(), "new role = exact"))
			require.Equal(t, "new role = exact", runGit(t, target, "config", "--local", "--get", "beads.role"))
			after, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "role write changed decoy")
			// Ordinary config commands keep their existing inherited context.
			got, found, err = repo.GetConfig(t.Context(), "test.marker")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, wantGeneric, got)
			require.NoError(t, repo.SetConfig(t.Context(), "test.marker", "changed"))
			require.Equal(t, "changed", runGit(t, changedDir, "config", "--local", "--get", "test.marker"))
			globalAfter, err := os.ReadFile(globalPath)
			require.NoError(t, err)
			require.Equal(t, global, string(globalAfter))
			require.True(t, slices.Equal(env, os.Environ()), "adapter changed inherited environment")
		})
	}
}

// TestRoleConfigKeySpellingTakesScrubbedPath pins the case-insensitive key
// match. Git resolves config key names case-insensitively, so every spelling of
// beads.role must take the scrubbed path; a byte-exact compare would let
// "Beads.Role" read and write whichever repository inherited routing selects.
func TestRoleConfigKeySpellingTakesScrubbedPath(t *testing.T) {
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
	runGit := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitenv.ScrubRouting(os.Environ())
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "fixture git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	// The canonical spelling is the control: it takes the scrubbed path under
	// either comparator, so it proves the fixture routes as intended rather than
	// the assertions passing vacuously.
	for _, key := range []string{"beads.role", "Beads.Role", "BEADS.ROLE"} {
		t.Run(key, func(t *testing.T) {
			target, decoy := t.TempDir(), t.TempDir()
			for _, dir := range []string{target, decoy} {
				runGit(t, dir, "init", "--quiet")
			}
			runGit(t, target, "config", "--local", "beads.role", "target-role")
			runGit(t, decoy, "config", "--local", "beads.role", "decoy-role")
			t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
			t.Setenv("GIT_WORK_TREE", decoy)
			repo := NewGitRepository(target)
			got, found, err := repo.GetConfig(t.Context(), key)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "target-role", got, "%s read through inherited routing", key)
			require.NoError(t, repo.SetConfig(t.Context(), key, "written-role"))
			require.Equal(t, "written-role", runGit(t, target, "config", "--local", "--get", "beads.role"))
			require.Equal(t, "decoy-role", runGit(t, decoy, "config", "--local", "--get", "beads.role"),
				"%s write escaped to the inherited repository", key)
		})
	}
}

func TestInitGitRepositoryUsesSelectedDirectory(t *testing.T) {
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
	for _, kind := range []string{"ordinary", "nested", "linked", "bare", "nonrepo", "captured_home"} {
		t.Run(kind, func(t *testing.T) {
			target, decoy := t.TempDir(), t.TempDir()
			runGit(decoy, "init", "--quiet")
			runGit(decoy, "config", "test.marker", "decoy")
			if kind == "bare" {
				runGit(target, "init", "--bare", "--quiet")
			} else if kind != "nonrepo" {
				runGit(target, "init", "--quiet")
			}
			if kind != "nonrepo" {
				runGit(target, "config", "test.marker", "target")
			}
			if kind == "nested" {
				target = filepath.Join(target, "nested")
				require.NoError(t, os.Mkdir(target, 0755))
			} else if kind == "linked" {
				runGit(target, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=", "commit", "--allow-empty", "-m", "seed")
				linked := filepath.Join(t.TempDir(), "linked")
				runGit(target, "-c", "core.hooksPath=", "worktree", "add", "--detach", linked)
				target = linked
			}
			t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
			t.Setenv("GIT_WORK_TREE", decoy)
			t.Chdir(decoy)
			inherited := NewGitRepository(target)
			marker, found, err := inherited.GetConfig(t.Context(), "test.marker")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "decoy", marker, "generic constructor must retain inherited routing")
			env := os.Environ()
			selected := NewInitGitRepository(target)
			isRepo, err := selected.IsGitRepo(t.Context())
			require.NoError(t, err)
			require.Equal(t, kind != "nonrepo", isRepo)
			bare, err := selected.IsBareGitRepo(t.Context())
			require.NoError(t, err)
			require.Equal(t, kind == "bare", bare)
			marker, found, err = selected.GetConfig(t.Context(), "test.marker")
			require.NoError(t, err)
			require.Equal(t, kind != "nonrepo", found)
			if found {
				require.Equal(t, "target", marker)
			}
			require.True(t, slices.Equal(env, os.Environ()))
			if kind == "captured_home" {
				changedHome := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(changedHome, ".gitconfig"), []byte("[invalid\n"), 0600))
				for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
					t.Setenv(key, changedHome)
				}
				// gastownhall/beads#6477 made the reader propagate Git's exit
				// error instead of mapping it to absence, so the generic
				// constructor now surfaces the changed HOME's broken .gitconfig
				// rather than reporting the key missing. changedHome is the only
				// HOME here holding a .gitconfig, so Git's refusal to parse that
				// file is itself the proof that generic role reads still use the
				// current caller environment rather than a captured one.
				role, found, err := inherited.GetConfig(t.Context(), "beads.role")
				require.Error(t, err, "generic role reads still use the current caller environment")
				var inheritedExit *exec.ExitError
				require.ErrorAs(t, err, &inheritedExit)
				require.Equal(t, 128, inheritedExit.ExitCode())
				require.ErrorContains(t, err, ".gitconfig")
				require.False(t, found)
				require.Empty(t, role)
				require.Error(t, inherited.SetConfig(t.Context(), "beads.role", "decoy"))
				require.NoError(t, selected.SetConfig(t.Context(), "beads.role", "contributor"))
				role, found, err = selected.GetConfig(t.Context(), "beads.role")
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, "contributor", role, "role reads and writes share the constructor's captured HOME")
			}
		})
	}
}

// TestInitGitRepositoryRoleIgnoresInheritedConfigSuppression pins the role
// boundary on the environment the production path actually carries. cmd/bd's
// proxied init tail always rebinds the use case to NewInitGitRepository
// (cmd/bd/init_proxied_server.go), so the *captured* environment — not
// os.Environ() — is the one a beads.role lookup has to be strict about. An
// inherited GIT_CONFIG_GLOBAL=/dev/null that survives into that capture blinds
// the read, and the tail answers a missing role by writing "maintainer" into
// repository-local config, where it outranks the global value from then on. A
// guard keyed on the environment's provenance would therefore be dead exactly
// where the escalation happens.
//
// Only the global-file vector is observable end to end here, for the reasons
// internal/routing's counterpart test records: repository config is never
// suppressed by these variables, and GIT_CONFIG_SYSTEM=<path> is itself
// scrubbed. The remaining forms are pinned in gitenv's suppression table.
func TestInitGitRepositoryRoleIgnoresInheritedConfigSuppression(t *testing.T) {
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			require.NoError(t, os.Unsetenv(key))
		}
	}
	home, target := t.TempDir(), t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[beads]\n\trole = contributor\n"), 0600))
	// No local role in the target: the global file is the only place the answer
	// can come from, so blinding it is visible as a miss.
	initCmd := exec.Command("git", "init", "--quiet")
	initCmd.Dir, initCmd.Env = target, gitenv.ScrubRouting(os.Environ())
	out, err := initCmd.CombinedOutput()
	require.NoError(t, err, "fixture git init: %s", out)

	for _, tc := range []struct{ name, blind string }{
		// Fixture guard: the global role must be readable at all, otherwise the
		// blinded case below would pass for the wrong reason.
		{"unblinded", ""},
		{"global_null", os.DevNull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.blind != "" {
				t.Setenv("GIT_CONFIG_GLOBAL", tc.blind)
			}
			// Constructed inside the subtest: the constructor captures the
			// environment, so the suppression entry has to be present first.
			useCase := domain.NewGitUseCase(target, NewInitGitRepository(target))
			role, found, err := useCase.BeadsRole(t.Context())
			require.NoError(t, err)
			require.True(t, found, "a blinded role read makes the init tail persist a maintainer default")
			require.Equal(t, "contributor", role)
		})
	}
}
