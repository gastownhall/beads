package scripts_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

const checkSkipCensusTest = "tools/bazel/check_skip_census_test.py"

func TestCheckSkipCensusUnitTests(t *testing.T) {
	root := sourceRepoRoot(t)
	python := requireHostTool(t, "python3")
	cmd := exec.Command(python, filepath.Join(root, checkSkipCensusTest), "-v")
	cmd.Dir = filepath.Join(root, "tools", "bazel")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", checkSkipCensusTest, err, out)
	}
}
