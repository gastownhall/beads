package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tests/oracle-a/run-oracle-a.sh builds its reference bd in a git worktree
// under $TMPDIR and removes that worktree from its EXIT trap. The trap used to
// remove it only when `git worktree list --porcelain | grep -qF "$REF_SRC"`
// matched, and that check reported a registered worktree as absent in two
// ways. grep -q exits on the first match while git is still writing a large
// listing, git dies of SIGPIPE, and pipefail fails the check. And git lists
// the resolved path, which $REF_SRC never matches when $TMPDIR ends in a slash
// or runs through a symlink. Each case runs the real script in a fixture
// repository with a stand-in go that fails the reference build, so the script
// dies (exit 2) right after `git worktree add`, or before it in the last case.
func TestOracleAScriptRemovesItsReferenceWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script is a Bash boundary")
	}
	bash := requireHostTool(t, "bash")
	requireHostTool(t, "git")
	script, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "tests", "oracle-a", "run-oracle-a.sh"))
	if err != nil {
		t.Fatalf("read run-oracle-a.sh: %v", err)
	}

	for _, tc := range []struct {
		name string
		// tmpdir prepares the fixture and returns the $TMPDIR to run with.
		tmpdir    func(t *testing.T, repo, base string) string
		refRef    string
		wantBuild bool
	}{
		{
			// Porcelain prints a lock reason in full, so locked worktrees
			// that sort after $REF_SRC stand in for the ~950-worktree
			// listing this was found on. Their 3 MB is more than twice the
			// largest default pipe buffer, so git is still writing when
			// grep -q exits on its match.
			name: "listing larger than a pipe buffer",
			tmpdir: func(t *testing.T, repo, base string) string {
				reason := strings.Repeat("x", 100000)
				for i := range 30 {
					pad := filepath.Join(base, "z", fmt.Sprintf("pad-%02d", i))
					oracleAGit(t, repo, "worktree", "add", "-q", "--no-checkout", "--detach", pad, "HEAD")
					oracleAGit(t, repo, "worktree", "lock", "--reason", reason, pad)
				}
				return filepath.Join(base, "a")
			},
			refRef:    "HEAD",
			wantBuild: true,
		},
		{
			// macOS sets TMPDIR this way.
			name: "TMPDIR with a trailing slash",
			tmpdir: func(t *testing.T, _, base string) string {
				return filepath.Join(base, "tmp") + "/"
			},
			refRef:    "HEAD",
			wantBuild: true,
		},
		{
			// macOS's TMPDIR is under /var, a symlink to /private/var.
			name: "TMPDIR through a symlink",
			tmpdir: func(t *testing.T, _, base string) string {
				target := filepath.Join(base, "real")
				if err := os.MkdirAll(target, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(base, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			refRef:    "HEAD",
			wantBuild: true,
		},
		{
			// Dies before `git worktree add`: cleanup has nothing to
			// remove and must stay silent and keep exit 2.
			name: "no worktree to remove",
			tmpdir: func(t *testing.T, _, base string) string {
				return filepath.Join(base, "tmp")
			},
			refRef:    "no-such-ref",
			wantBuild: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			scriptPath := filepath.Join(repo, "tests", "oracle-a", "run-oracle-a.sh")
			if err := os.MkdirAll(filepath.Dir(scriptPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(scriptPath, script, 0o700); err != nil {
				t.Fatal(err)
			}
			oracleAGit(t, repo, "init", "-q")
			oracleAGit(t, repo, "add", "-A")
			oracleAGit(t, repo, "-c", "user.name=beads-test", "-c", "user.email=test@beads.local",
				"-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")

			base := t.TempDir()
			tmpdir := tc.tmpdir(t, repo, base)
			if err := os.MkdirAll(tmpdir, 0o700); err != nil {
				t.Fatal(err)
			}

			// The preflight needs cargo, go and a C compiler on PATH; go
			// records where it ran and fails the reference build.
			bin := t.TempDir()
			built := filepath.Join(t.TempDir(), "go-ran-in")
			writeExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf '%s\\n' \"$PWD\" >>\"$ORACLE_A_TEST_GO_LOG\"\nexit 1\n")
			writeExecutable(t, filepath.Join(bin, "cargo"), "#!/bin/sh\nexit 0\n")
			writeExecutable(t, filepath.Join(bin, "gcc"), "#!/bin/sh\nexit 0\n")

			cmd := exec.Command(bash, scriptPath)
			cmd.Dir = repo
			cmd.Env = oracleAEnv(
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+tmpdir,
				"REF_REF="+tc.refRef,
				"KEEP_ARTIFACTS=",
				"ORACLE_CATALOG=",
				"ORACLE_A_TEST_GO_LOG="+built,
			)
			out, err := cmd.CombinedOutput()
			if got := exitCode(err); got != 2 {
				t.Fatalf("exit = %d, want 2 (setup error)\n%s", got, out)
			}
			if _, err := os.Stat(built); (err == nil) != tc.wantBuild {
				t.Fatalf("reference build ran = %v, want %v\n%s", err == nil, tc.wantBuild, out)
			}
			if strings.Contains(string(out), "fatal:") {
				t.Errorf("cleanup printed a git error:\n%s", out)
			}
			for _, line := range strings.Split(oracleAGit(t, repo, "worktree", "list", "--porcelain"), "\n") {
				if strings.HasPrefix(line, "worktree ") && strings.HasSuffix(line, "/ref-src") {
					t.Errorf("reference worktree still registered after exit: %s\n%s", line, out)
				}
			}
		})
	}
}

func oracleAGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = oracleAEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// oracleAEnv is the test's environment without any GIT_* variable, plus
// extra. git exports GIT_DIR to a pre-push hook run from a linked worktree,
// and with it set, git in the fixture would act on that repository instead.
func oracleAEnv(extra ...string) []string {
	env := []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}
