package scripts_test

import (
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// skipOrFailWithoutHostTool handles a missing host tool. Under go test the
// test skips, as it always has. Under Bazel it fails: the //scripts go_tests
// rely on the executor's bash, git, python3 and friends (the rbe-west
// worker image, or the runner in fork-cache mode), so a skip there would
// hide an executor that lacks one behind a green, cacheable result.
func skipOrFailWithoutHostTool(t *testing.T, format string, args ...any) {
	t.Helper()
	if bazeltest.IsBazel() {
		t.Fatalf("executor lacks a host tool: "+format, args...)
	}
	t.Skipf(format, args...)
}

// requireHostTool returns the path of a host tool, or skips/fails via
// skipOrFailWithoutHostTool when it is missing.
func requireHostTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		skipOrFailWithoutHostTool(t, "%s not available: %v", name, err)
	}
	return path
}

// testGo returns the go binary to run. Under Bazel that is the registered Go
// SDK (BUILD data, BEADS_TEST_GO), which is go.mod's toolchain: the go on the
// executor's PATH may be another release.
func testGo(t *testing.T) string {
	t.Helper()
	if bazeltest.IsBazel() {
		path, err := bazeltest.RunfileEnv("BEADS_TEST_GO")
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	return requireHostTool(t, "go")
}

// requireAutofixBash skips when bash cannot run the autofix scripts: they run
// on Linux runners and use bash 4 associative arrays, and macOS runners ship
// /bin/bash 3.2.
func requireAutofixBash(t *testing.T) {
	t.Helper()
	requireHostTool(t, "bash")
	if err := exec.Command("bash", "-c", "declare -A probe=()").Run(); err != nil {
		t.Skip("bash lacks associative arrays (bash >= 4 required)")
	}
}
