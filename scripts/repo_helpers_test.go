package scripts_test

// Helpers shared by the package's test files, kept apart so a focused
// go_test over a few of them (//scripts:doc_freshness_required_test) compiles
// without the rest of the package.

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

func sourceRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// Under Bazel the caller path is workspace-relative; CallerDir rebuilds it
	// under the runfiles root, which holds the files scripts_test declares.
	return filepath.Dir(bazeltest.CallerDir(file, "scripts"))
}

func msysPath(path string) string {
	path = filepath.Clean(path)
	path = filepath.ToSlash(path)
	if len(path) >= 3 && path[1] == ':' && path[2] == '/' {
		return "/" + strings.ToLower(path[:1]) + path[2:]
	}
	return path
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
