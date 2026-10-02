package scripts_test

import (
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// skipOrFailWithoutHostTool handles a missing host tool. Under go test the
// test skips, as it always has. Under Bazel it fails: //scripts:scripts_test
// is tagged host-tools, which promises git, python3, bash and friends on the
// host that runs it, so a skip there would hide a host that breaks that promise
// behind a green result.
func skipOrFailWithoutHostTool(t *testing.T, format string, args ...any) {
	t.Helper()
	if bazeltest.IsBazel() {
		t.Fatalf("host-tools target: "+format, args...)
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
