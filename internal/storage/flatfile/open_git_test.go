package flatfile

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TASKS-4gnf: InGitRepo must distinguish "no git repo" from "in a repo" —
// CheckGitignored alone cannot (git check-ignore exits non-zero for both
// "not ignored" and "not a repo").
func TestInGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")

	if InGitRepo(beadsDir) {
		t.Errorf("InGitRepo(%q) = true before git init; want false", beadsDir)
	}

	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	if !InGitRepo(beadsDir) {
		t.Errorf("InGitRepo(%q) = false after git init; want true", beadsDir)
	}
}
