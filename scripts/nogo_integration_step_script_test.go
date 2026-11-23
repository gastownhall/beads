package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNogoIntegrationStepScriptFailsWithoutMaskingBazelsExitCode (independent
// review of #7486, should-fix 1): runs bazel-integration's "Nogo: integration
// library files" step's own script text -- extracted from bazel.yml, not
// reimplemented -- the way GitHub Actions actually runs a `run:` step with no
// `shell:` override: `bash --noprofile --norc -e {0}`. GitHub does not pass
// `-o pipefail` itself; the step's own `set -o pipefail` line is what makes
// its `bazel build | tee` pipeline report bazel's exit code instead of tee's,
// so this test must not add `-o pipefail` on the bash invocation either, or
// it would mask exactly the class of bug it exists to catch. That rules out
// reusing runRRCScript (bazel_rrc_test.go), whose own invocation always
// passes "-eo pipefail".
//
// scripts/*.go's own checks (checkNogoOnlyStepRow in nogo_lint_policy_test.go)
// pin the script's text -- "set -o pipefail" is present, no "|| true" on the
// tee pipeline, the last line is exactly `exit "$rc"` -- but a textual pin can
// miss a mutation its author did not anticipate (e.g. `rc=0` inserted right
// before the final `exit "$rc"`, which satisfies every one of those text pins
// while still always exiting 0). Running the real script end to end against a
// stub `bazel` closes that gap: it fails if the step ever stops propagating a
// real `bazel build` failure, regardless of how.
func TestNogoIntegrationStepScriptFailsWithoutMaskingBazelsExitCode(t *testing.T) {
	workflow := readCIWorkflow(t, bazelWorkflowName)
	step := workflow.job(t, bazelIntegJobName).step(t, "Nogo: integration library files")
	if strings.TrimSpace(step.Run) == "" {
		t.Fatal("bazel-integration has no \"Nogo: integration library files\" step with a run: script")
	}

	if got := nogoOnlyStepScriptExitCode(t, step.Run, 1); got == 0 {
		t.Error("the step's real script exits 0 when `bazel build` exits 1 with a finding; CI would report this lane green despite a real nogo finding " +
			"(e.g. a stray `rc=0` before `exit \"$rc\"`, `|| :` or `|| true` in place of `|| rc=$?`, or a missing/negated `set -o pipefail` would all produce this)")
	}
	if got := nogoOnlyStepScriptExitCode(t, step.Run, 0); got != 0 {
		t.Errorf("the step's real script exits %d when `bazel build` exits 0 (no finding); want 0", got)
	}
}

// nogoOnlyStepScriptExitCode runs script under bash with exactly the flags
// GitHub Actions uses for a plain `run:` step (no `shell:` override): `bash
// --noprofile --norc -e {0}`. It puts a stub `bazel` on PATH that prints a
// line a real nogo finding would produce and exits bazelExit, and returns the
// script's own exit code (-1 if bash itself could not be run, which should
// not happen with "bash" resolved from PATH).
func nogoOnlyStepScriptExitCode(t *testing.T, script string, bazelExit int) int {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := fmt.Sprintf("#!/bin/sh\necho 'internal/testutil/integration/harness.go:1:1: fake nogo finding'\nexit %d\n", bazelExit)
	if err := os.WriteFile(filepath.Join(bin, "bazel"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + dir,
		"RUNNER_TEMP=" + t.TempDir(),
		"GITHUB_STEP_SUMMARY=" + filepath.Join(dir, "step-summary.txt"),
	}
	err := cmd.Run()
	return exitCode(err)
}
