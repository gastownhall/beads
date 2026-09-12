package git

import (
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
				role, found, err := inherited.GetConfig(t.Context(), "beads.role")
				require.NoError(t, err) // The existing reader maps Git exit errors to absence.
				require.False(t, found, "generic role reads still use the current caller environment")
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
