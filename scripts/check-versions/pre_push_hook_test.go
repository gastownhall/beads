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
	// source at the repository root (`go build ./scripts/check-versions`),
	// which needs a module checkout and a Go toolchain. Under Bazel there is
	// neither, so a stand-in `go` first on PATH "builds" by copying the checker
	// Bazel built from the same source (BEADS_TEST_CHECK_VERSIONS); everything
	// the contract covers (status passthrough, no launcher diagnostic, temp
	// cleanup) is the entrypoint's own.
	path := os.Getenv("PATH")
	if bazeltest.IsBazel() {
		path = fakeGoBuildDir(t) + string(os.PathListSeparator) + path
	}
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
			scratch := t.TempDir()
			t.Setenv("TMPDIR", scratch)
			t.Setenv("PATH", path)
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

// fakeGoBuildDir returns a directory holding a stand-in `go` whose `build ...
// -o OUT ...` copies the Bazel-built checker (BEADS_TEST_CHECK_VERSIONS) to
// OUT, for check-versions.sh under Bazel, where there is no module checkout
// or Go toolchain to build it from.
func fakeGoBuildDir(t *testing.T) string {
	t.Helper()
	built, err := bazeltest.RunfileEnv("BEADS_TEST_CHECK_VERSIONS")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := `#!/bin/sh
[ "$1" = build ] || { echo "stand-in go: only build is supported" >&2; exit 2; }
out=
while [ $# -gt 0 ]; do
	if [ "$1" = -o ]; then out=$2; shift; fi
	shift
done
[ -n "$out" ] || { echo "stand-in go: no -o" >&2; exit 2; }
exec cp "` + built + `" "$out"
`
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
