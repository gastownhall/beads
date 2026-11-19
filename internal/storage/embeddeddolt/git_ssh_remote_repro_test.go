//go:build cgo

package embeddeddolt

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// GH#6472: embedded Dolt must push to a git+ssh:// remote (the documented
// GitHub form). The ssh hop is faked with GIT_SSH_COMMAND so git's own ssh
// transport runs end to end against a local bare repository.
func TestGitSSHRemotePushOverSSHTransport(t *testing.T) {
	ctx := t.Context()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "repo.git")
	git("init", "-q", work)
	git("-C", work, "commit", "-q", "--allow-empty", "-m", "init")
	git("clone", "-q", "--bare", work, bare)

	marker := filepath.Join(root, "ssh-invoked")
	fakeSSH := filepath.Join(root, "fake-ssh.sh")
	script := "#!/bin/sh\necho \"$@\" >> " + marker + "\nfor a; do last=$a; done\nexec sh -c \"$last\"\n"
	if err := os.WriteFile(fakeSSH, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", fakeSSH)
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	store := newPeerAuthTestStore(t)
	if err := store.SetConfig(ctx, "issue_prefix", "fedauth"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := store.AddRemote(ctx, defaultRemote, "git+ssh://git@github.example"+bare); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("Push over git+ssh:// failed: %v", err)
	}

	invoked, _ := os.ReadFile(marker)
	if !strings.Contains(string(invoked), "git-receive-pack") {
		t.Fatalf("push did not go through the ssh transport; fake ssh saw:\n%s", invoked)
	}
	if out, err := exec.Command("git", "--git-dir", bare, "for-each-ref", "refs/dolt/").CombinedOutput(); err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Fatalf("remote has no refs/dolt/* after push (err=%v):\n%s", err, out)
	}
}

// oldGitUsageBanner is git's argument-parse rejection, with the trace line
// git emits ahead of it and the exit code usage() exits with (129).
const oldGitUsageBanner = "usage: git ls-remote [--heads] [--tags] [-u <exec> | --upload-pack <exec>] [<repository> [<refs>...]]"

// writeOldGitShim installs a `git` on PATH that plays the part of a git too
// old for the `--` separator Dolt's remote transport passes (git 2.6.1
// rejected it with usage(); GH#6472), delegating every other invocation to the
// real git binary it returns.
func writeOldGitShim(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	shim := `#!/bin/sh
# fake old git: reject the separator-bearing ls-remote Dolt's remote runs
# (git 2.6.1 rejected the -- separator with usage()), pass everything else
# through.
seen=
for a in "$@"; do
  if [ "$a" = ls-remote ]; then
    seen=1
  fi
  if [ "$a" = "--" ] && [ "$seen" = 1 ]; then
    echo "trace: built-in: git ls-remote" >&2
    echo "` + oldGitUsageBanner + `" >&2
    exit 129
  fi
done
exec "` + realGit + `" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	return realGit
}

// TestGitSSHRemoteOldGitUsageRefusal pins the classification against the real
// wrapped error: with an old-style git on PATH, an embedded push to a
// git+ssh:// remote fails inside Dolt's git transport, and the failure Dolt
// surfaces — git's usage banner wearing Dolt's appended credential hints —
// reads as a Git invocation refusal, not as an authentication problem
// (GH#6472).
func TestGitSSHRemoteOldGitUsageRefusal(t *testing.T) {
	ctx := t.Context()
	shimDir := t.TempDir()
	writeOldGitShim(t, shimDir)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The remote names a host that does not resolve; the shim must fail the
	// probe before any transport runs, so the ssh hop is never reached.
	remote := "git+ssh://git@github.example" + filepath.Join(t.TempDir(), "repo.git")

	store := newPeerAuthTestStore(t)
	if err := store.SetConfig(ctx, "issue_prefix", "fedauth"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var pushErr error
	if err := store.AddRemote(ctx, defaultRemote, remote); err != nil {
		pushErr = err
	} else {
		pushErr = store.Push(ctx)
	}
	if pushErr == nil {
		t.Fatal("push to a git+ssh:// remote with an old git on PATH unexpectedly succeeded")
	}
	if !dberrors.IsGitUsageRefusal(pushErr) {
		t.Fatalf("IsGitUsageRefusal(push error) = false; the wrapped refusal does not classify:\n%v", pushErr)
	}
	msg := pushErr.Error()
	if !strings.Contains(msg, oldGitUsageBanner) {
		t.Errorf("push error does not carry git's usage banner:\n%s", msg)
	}
	if !strings.Contains(msg, "dolt does not support interactive credential prompts") {
		t.Errorf("push error does not carry the credential hints Dolt appends (they are what make the refusal misleading):\n%s", msg)
	}
}
