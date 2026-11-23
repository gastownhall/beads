package scripts_test

// check-testing-short.sh over the whole Go tree: //scripts:go_sources_test,
// which declares every .go file (test and non-test) and nothing else. The
// script's own fixture tests are in check_testing_short_test.go.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckTestingShortPassesOnCleanRepoTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checker is a Bash boundary")
	}
	repo := sourceRepoRoot(t)
	// Under Bazel the tree is //:repo_go_srcs and //:repo_go_test_srcs; a tree
	// missing the allowlisted call sites would let the scan pass vacuously.
	for _, rel := range []string{"internal/hooks/hooks_test.go", "internal/storage/dolt/concurrent_test.go", "internal/workapi/sweep_test.go"} {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("the scanned tree lacks %s, which holds an allowlisted call: %v", rel, err)
		}
	}
	cmd := exec.Command("bash", filepath.Join(repo, "scripts", "check-testing-short.sh"))
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check-testing-short.sh failed on clean repo tree (all 9 allowlist entries must still be recognized): %v\noutput: %s", err, out)
	}
}
