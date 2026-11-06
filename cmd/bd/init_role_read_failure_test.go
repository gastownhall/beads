package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/storage/domain"
	storagegit "github.com/steveyegge/beads/internal/storage/git"
)

const roleReadFailureDiagnostic = "fatal: bad config line 7 in file .git/config"

// installRoleReadFailingGitForTest puts a git wrapper first on PATH that
// fails every `--get beads.role` read with exit code exitCode and git's
// malformed-config diagnostic, and hands every other call to the real git.
// Real git fails rev-parse on the same malformed file, so this is how a test
// reaches a role read that fails after a repository gate passed.
func installRoleReadFailingGitForTest(t *testing.T, exitCode int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git wrapper is a POSIX shell script")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
get=; role=
for arg in "$@"; do
	[ "$arg" = "--get" ] && get=1
	[ "$arg" = "beads.role" ] && role=1
done
if [ -n "$get" ] && [ -n "$role" ]; then
	echo %q >&2
	exit %d
fi
exec %q "$@"
`, roleReadFailureDiagnostic, exitCode, realGit)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// roleReadFailureRepoForTest makes a valid repository the working directory,
// with routing scrubbed and an empty home, and returns its local config path.
func roleReadFailureRepoForTest(t *testing.T) string {
	t.Helper()
	for _, entry := range os.Environ() {
		key := gitenv.EntryKey(entry)
		if gitenv.IsRoutingKeyForOS(key, runtime.GOOS) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepoForContextTest(t, repo)
	t.Chdir(repo)
	return filepath.Join(repo, ".git", "config")
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestInitRoleReadFailureWritesNoImplicitRole pins bd-a2qnf4 (GH#6765) for
// direct init: when the beads.role read fails after the repository gate and
// no --role was given, the default branch, the prompt and the GH#2950 safety
// net write no role and do not prompt. Non-quiet init reports the failure
// once; quiet init stays silent; neither fails.
func TestInitRoleReadFailureWritesNoImplicitRole(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt bool
		quiet  bool
	}{
		{"default branch", false, false},
		{"prompt", true, false},
		{"quiet", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := roleReadFailureRepoForTest(t)
			installRoleReadFailingGitForTest(t, 128)
			before := readFileForTest(t, configPath)

			// A prompt that did run would read this answer and write the role.
			stdin, err := os.CreateTemp(t.TempDir(), "stdin-*")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stdin.WriteString("n\n"); err != nil {
				t.Fatal(err)
			}
			if _, err := stdin.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			oldStdin := os.Stdin
			os.Stdin = stdin
			t.Cleanup(func() { os.Stdin = oldStdin; _ = stdin.Close() })

			roleRead := &initRoleReadFailure{quiet: tc.quiet}
			stdout, stderr, err := captureStdoutAndStderr(t, func() error {
				if tc.prompt {
					if contributor, err := promptContributorMode(roleRead); err != nil || contributor {
						return fmt.Errorf("prompt = (%v, %v), want (false, nil)", contributor, err)
					}
				} else {
					applyInitRoleWithoutPrompt("", tc.quiet, roleRead)
				}
				ensureInitRole("", tc.quiet, roleRead)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if after := readFileForTest(t, configPath); after != before {
				t.Errorf("init wrote an implicit role:\n%s", after)
			}
			if strings.Contains(stdout, "?") {
				t.Errorf("init prompted for a role it could not read: %q", stdout)
			}
			if strings.Contains(stderr, "failed to set") {
				t.Errorf("init attempted a role write: %q", stderr)
			}
			want := 1
			if tc.quiet {
				want = 0
			}
			if got := strings.Count(stderr, "bad config"); got != want {
				t.Errorf("read failure reported %d times, want %d: %q", got, want, stderr)
			}
		})
	}
}

// TestInitRoleReadFailureExplicitRole pins the explicit-role half of
// bd-a2qnf4 (GH#6765) for direct init: over a failed beads.role read, the
// no-prompt branch still honors an explicit --role, while the GH#2950 safety
// net never writes, even with --role, and reports the failure instead.
func TestInitRoleReadFailureExplicitRole(t *testing.T) {
	t.Run("no-prompt branch honors an explicit role", func(t *testing.T) {
		configPath := roleReadFailureRepoForTest(t)
		installRoleReadFailingGitForTest(t, 128)
		roleRead := &initRoleReadFailure{}
		_, stderr, err := captureStdoutAndStderr(t, func() error {
			applyInitRoleWithoutPrompt("contributor", false, roleRead)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if after := readFileForTest(t, configPath); !strings.Contains(after, "contributor") {
			t.Errorf("explicit --role was not written over the failed read:\n%s", after)
		}
		if strings.Contains(stderr, "failed to set") {
			t.Errorf("explicit role write failed: %q", stderr)
		}
	})

	t.Run("safety net never writes on a failed read", func(t *testing.T) {
		configPath := roleReadFailureRepoForTest(t)
		installRoleReadFailingGitForTest(t, 128)
		before := readFileForTest(t, configPath)
		roleRead := &initRoleReadFailure{}
		_, stderr, err := captureStdoutAndStderr(t, func() error {
			ensureInitRole("contributor", false, roleRead)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if after := readFileForTest(t, configPath); after != before {
			t.Errorf("safety net wrote beads.role over a failed read:\n%s", after)
		}
		if strings.Contains(stderr, "failed to set") {
			t.Errorf("safety net attempted a role write: %q", stderr)
		}
		if got := strings.Count(stderr, "bad config"); got != 1 {
			t.Errorf("read failure reported %d times, want 1: %q", got, stderr)
		}
	})
}

// roleReadFailingUseCase answers BeadsRole with a read failure and records
// role writes, over a real repository for everything else.
type roleReadFailingUseCase struct {
	domain.GitUseCase
	writes []string
}

func (u *roleReadFailingUseCase) BeadsRole(context.Context) (string, bool, error) {
	return "", false, fmt.Errorf("git: GetConfig beads.role: %w: %s", errors.New("exit status 128"), roleReadFailureDiagnostic)
}

func (u *roleReadFailingUseCase) SetBeadsRole(_ context.Context, role string) error {
	u.writes = append(u.writes, role)
	return nil
}

// TestProxiedInitTailRoleReadFailureWritesNoImplicitRole pins bd-a2qnf4
// (GH#6765) for the proxied init tail: a failed role read without --role
// writes no role, is reported unless quiet, and does not fail the tail; an
// explicit --role is still written.
func TestProxiedInitTailRoleReadFailureWritesNoImplicitRole(t *testing.T) {
	for _, tc := range []struct {
		name       string
		quiet      bool
		roleFlag   string
		wantWrites []string
		wantReport bool
	}{
		{name: "quiet=false", wantReport: true},
		{name: "quiet=true", quiet: true},
		{name: "explicit role still written", roleFlag: "contributor", wantWrites: []string{"contributor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roleReadFailureRepoForTest(t)
			gitUC := &roleReadFailingUseCase{GitUseCase: storagegit.NewGitProvider(".").GitUseCase()}
			cmd := &cobra.Command{}
			cmd.Flags().Bool("setup-exclude", false, "")
			// Existing flags exclude all filesystem integrations; nil fsUseCase must stay unused.
			in := initProxiedServerInput{quiet: tc.quiet, roleFlag: tc.roleFlag, stealth: true, skipHooks: true, skipAgents: true}
			_, stderr, err := captureStdoutAndStderr(t, func() error {
				return runInitProxiedServerTail(cmd, t.Context(), in, runInitTailContext{gitUC: gitUC})
			})
			if err != nil {
				t.Fatalf("tail = %v, want success", err)
			}
			if !reflect.DeepEqual(gitUC.writes, tc.wantWrites) {
				t.Errorf("tail beads.role writes = %v, want %v", gitUC.writes, tc.wantWrites)
			}
			if reported := strings.Contains(stderr, "bad config"); reported != tc.wantReport {
				t.Errorf("stderr %q: reported=%v, want %v", stderr, reported, tc.wantReport)
			}
		})
	}
}
