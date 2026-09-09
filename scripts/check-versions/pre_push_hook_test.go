package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(workingDir, "..", ".."))
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
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"unknown option", []string{"--unknown"}, 2},
		{"positional argument", []string{"unexpected"}, 2},
		{"version mismatch", []string{"--expect", "0.0.0-checker-test"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
