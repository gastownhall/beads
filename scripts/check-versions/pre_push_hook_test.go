package main

import (
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
}
