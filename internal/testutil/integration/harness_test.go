//go:build integration && !windows

package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSubprocessRunnerPreservesBuildFailure(t *testing.T) {
	const helperEnv = "BEADS_TEST_SUBPROCESS_RUNNER_BUILD_FAILURE"
	if os.Getenv(helperEnv) == "1" {
		NewSubprocessRunner(t.TempDir(), "./missing-package").Build(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessRunnerPreservesBuildFailure$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("helper unexpectedly succeeded:\n%s", out)
	}
	got := string(out)
	if !strings.Contains(got, "failed to build test binary") || !strings.Contains(got, "stderr:") {
		t.Fatalf("build failure was not preserved:\n%s", got)
	}
	if strings.Contains(got, "failed to chmod test binary") {
		t.Fatalf("chmod failure masked the build failure:\n%s", got)
	}
}
