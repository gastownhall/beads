package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// runAutoExportWithGitAdd runs maybeAutoExport with export.auto and
// export.git-add on, the process cwd at cwd and BEADS_DIR at beadsParent/.beads.
func runAutoExportWithGitAdd(t *testing.T, cwd, beadsParent string) (string, error) {
	t.Helper()
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.git-add", true)
	saveAndRestoreGlobals(t)
	t.Setenv("BD_GIT_HOOK", "")
	// No loader fails: one issue exports cleanly.
	store = &failingExportRelationStore{}

	beadsDir := filepath.Join(beadsParent, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"beads","backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Setenv("BEADS_DIR", beadsDir)
	return filepath.Join(beadsDir, "issues.jsonl"), maybeAutoExport(t.Context(), false)
}

func gitInitForGateTest(t *testing.T, dir string) {
	t.Helper()
	c := gitCommand("init", "-q")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git init in %s: %v\n%s", dir, err, out)
	}
}

func stagedNames(t *testing.T, repo string) string {
	t.Helper()
	c := gitCommand("diff", "--cached", "--name-only")
	c.Dir = repo
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --cached in %s: %v\n%s", repo, err, out)
	}
	return strings.TrimSpace(string(out))
}

// GH#4351: the git-add gate checked only the cwd. With the cwd in a git repo
// and BEADS_DIR outside any repo, git add ran in the beads directory and failed
// with exit 128 ("not a git repository"), failing the triggering command.
func TestAutoExportGitAddSkipsBeadsDirOutsideRepo(t *testing.T) {
	if _, err := gitCommand("--version").Output(); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	gitInitForGateTest(t, repo)
	standalone := t.TempDir()

	jsonl, err := runAutoExportWithGitAdd(t, repo, standalone)
	if err != nil {
		t.Fatalf("maybeAutoExport err = %v, want nil (standalone BEADS_DIR skips git add)", err)
	}
	if _, statErr := os.Stat(jsonl); statErr != nil {
		t.Fatalf("export not written: %v", statErr)
	}
	if got := stagedNames(t, repo); got != "" {
		t.Fatalf("cwd repo staged %q, want nothing", got)
	}
}

func TestAutoExportGitAddStagesBeadsDirInsideRepo(t *testing.T) {
	if _, err := gitCommand("--version").Output(); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	gitInitForGateTest(t, repo)

	if _, err := runAutoExportWithGitAdd(t, repo, repo); err != nil {
		t.Fatalf("maybeAutoExport err = %v, want nil", err)
	}
	if got := stagedNames(t, repo); got != ".beads/issues.jsonl" {
		t.Fatalf("staged %q, want .beads/issues.jsonl", got)
	}
}
