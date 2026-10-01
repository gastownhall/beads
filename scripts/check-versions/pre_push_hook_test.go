package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// The repository hook must keep ordinary pushes working under macOS's system
// Bash 3.2. In particular, nounset treats an empty array as unset there.
func TestPrePushHookOrdinaryPushWorksWithSystemBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide the oldest supported /bin/bash")
	}

	const bash = "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		t.Skipf("system Bash unavailable: %v", err)
	}

	repoRoot := bazeltest.RepoRoot(t)
	hook := filepath.Join(repoRoot, ".githooks", "pre-push")

	command := exec.Command(bash, hook, "origin", "https://example.invalid/repo.git")
	command.Dir = repoRoot
	command.Env = append(os.Environ(), "PATH=/bin")
	command.Stdin = strings.NewReader(
		"refs/heads/main 1111111111111111111111111111111111111111 " +
			"refs/heads/main 0000000000000000000000000000000000000000\n",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ordinary pre-push hook under %s: %v\n%s", bash, err, output)
	}

	// Keep the shell entrypoint's status contract covered on the oldest Bash,
	// alongside the ordinary-push compatibility that brought us here.
	//
	// Unlike the ordinary push above, the entrypoint builds the Go checker from
	// source at the repository root, so it needs a full module checkout there.
	// Under Bazel the tree the test sees is its runfiles, which holds only the
	// declared data and no go.mod, so the wrapper could only ever report its
	// build failure (127); `go test` runs against the real checkout. The guard
	// sits inside each subtest, not above the loop, so that the ordinary-push
	// assertion above still reports as run rather than hiding behind a skip.
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"unknown option", []string{"--unknown"}, 2},
		{"positional argument", []string{"unexpected"}, 2},
		{"version mismatch", []string{"--expect", "0.0.0-checker-test"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if bazeltest.IsBazel() {
				t.Skip("entrypoint builds from source: needs the real module checkout, not runfiles")
			}
			scratch := t.TempDir()
			t.Setenv("TMPDIR", scratch)
			checker := filepath.Join(repoRoot, "scripts", "check-versions.sh")
			cmd := exec.Command(bash, append([]string{checker}, tc.args...)...)
			cmd.Dir = repoRoot
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
				t.Fatalf("entrypoint status = %v, want %d: %s", err, tc.code, output)
			}
			if strings.Contains(string(output), "exit status ") {
				t.Fatalf("launcher added its own exit diagnostic: %s", output)
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("checker temporary files remain: %v, %v", entries, err)
			}
		})
	}
}

// On drift (checker status 1) the hook defers to the checker's remedies rather
// than prescribing scripts/update-versions.sh itself: with cmd/bd/version.go
// already at the release version, that re-run leaves a drifted file as it was.
// Stub git and checker keep this independent of the checkout, so it also runs
// under Bazel.
func TestPrePushHookDriftRefusalPointsAtCheckerRemedy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide the oldest supported /bin/bash")
	}

	const bash = "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		t.Skipf("system Bash unavailable: %v", err)
	}

	hook := filepath.Join(bazeltest.RepoRoot(t), ".githooks", "pre-push")
	scratch := t.TempDir()
	for name, body := range map[string]string{
		"bin/git":                   "#!/bin/sh\necho '" + scratch + "'\n",
		"scripts/check-versions.sh": "#!/bin/sh\necho 'stub drift report'\nexit 1\n",
	} {
		path := filepath.Join(scratch, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command(bash, hook, "origin", "https://example.invalid/repo.git")
	command.Dir = scratch
	command.Env = append(os.Environ(), "PATH="+filepath.Join(scratch, "bin")+":/bin:/usr/bin")
	command.Stdin = strings.NewReader(
		"refs/tags/v1.1.0 1111111111111111111111111111111111111111 " +
			"refs/tags/v1.1.0 0000000000000000000000000000000000000000\n",
	)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("hook status = %v, want 1: %s", err, output)
	}
	for _, want := range []string{
		"stub drift report",
		"versions are inconsistent",
		"See the checker output above",
	} {
		if !strings.Contains(string(output), want) {
			t.Errorf("hook output lacks %q:\n%s", want, output)
		}
	}
	if strings.Contains(string(output), "update-versions.sh") {
		t.Errorf("hook prescribes update-versions.sh for drift:\n%s", output)
	}
}
