package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// TestFindBeadsDirFrom_WorktreeDoesNotAdoptUnrelatedParentProject is a
// regression test for gastownhall/beads#7782: `bd -C <linked worktree>`
// resolved to an unrelated PARENT folder's .beads/ instead of the
// worktree's own project, because FindBeadsDirFrom's ancestor walk kept
// going past the worktree's own repo root looking for any .beads/,
// finding the parent's real project before ever consulting the
// worktree's own main-checkout fallback. FindBeadsDir (the cwd path)
// already bounds its walk at the worktree root; FindBeadsDirFrom must
// agree, since `-C <dir>` is documented to behave like `cd <dir>`.
func TestFindBeadsDirFrom_WorktreeDoesNotAdoptUnrelatedParentProject(t *testing.T) {
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	tmpDir, err := os.MkdirTemp("", "beads-worktree-parent-fallback-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	if cmd := exec.Command("git", "--version"); cmd.Run() != nil {
		t.Skip("git not available")
	}

	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Parent folder with its OWN, unrelated beads project.
	parentDir := filepath.Join(tmpDir, "parent")
	parentBeadsDir := filepath.Join(parentDir, ".beads")
	if err := os.MkdirAll(filepath.Join(parentBeadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(parentBeadsDir, "metadata.json"), `{"database":"dolt","dolt_database":"parent"}`)
	mustWrite(filepath.Join(parentBeadsDir, "config.yaml"), "issue-prefix: \"parent\"\n")

	// A real git project nested under the parent, with its own real
	// beads project (metadata + an actual database directory).
	projDir := filepath.Join(parentDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(projDir, "init")
	runGit(projDir, "config", "user.email", "test@example.com")
	runGit(projDir, "config", "user.name", "Test User")

	projBeadsDir := filepath.Join(projDir, ".beads")
	if err := os.MkdirAll(filepath.Join(projBeadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(projBeadsDir, "metadata.json"), `{"database":"dolt","dolt_database":"proj"}`)
	mustWrite(filepath.Join(projBeadsDir, "config.yaml"), "issue-prefix: \"proj\"\n")
	mustWrite(filepath.Join(projDir, "README.md"), "# proj\n")
	// Deliberately do NOT track any .beads/* files, so the linked worktree
	// below ends up with no .beads/ at all (not even inherited metadata) —
	// the plain case from gastownhall/beads#7782's own repro, distinct from
	// the inherited-artifacts case TestFindBeadsDir_WorktreeWithInheritedArtifacts
	// already covers.
	runGit(projDir, "add", "README.md")
	runGit(projDir, "commit", "-m", "initial")

	// A linked worktree of proj, created as proj's sibling (so its
	// filesystem ancestor chain passes back through the parent's own
	// unrelated project before it would ever reach proj's via a plain
	// upward walk that ignores repo boundaries).
	worktreeDir := filepath.Join(parentDir, "wt-proj")
	runGit(projDir, "worktree", "add", worktreeDir, "-b", "wt")
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = projDir
		_ = cmd.Run()
	}()

	// Precondition: the worktree itself has no .beads/ at all (this is
	// the plain worktree case, distinct from the inherited-artifacts
	// case covered by TestFindBeadsDir_WorktreeWithInheritedArtifacts).
	if _, err := os.Stat(filepath.Join(worktreeDir, ".beads")); err == nil {
		t.Fatalf("precondition: worktree should have no local .beads/")
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()
	defer git.ResetCaches()

	cwdResult := FindBeadsDir()
	fromResult := FindBeadsDirFrom(worktreeDir)

	cwdResolved, _ := filepath.EvalSymlinks(cwdResult)
	fromResolved, _ := filepath.EvalSymlinks(fromResult)
	projResolved, _ := filepath.EvalSymlinks(projBeadsDir)
	parentResolved, _ := filepath.EvalSymlinks(parentBeadsDir)

	if fromResolved == parentResolved {
		t.Fatalf("FindBeadsDirFrom(%q) = %q, adopted the unrelated PARENT project's .beads/ (%q) "+
			"instead of the worktree's own project (%q) — this is gastownhall/beads#7782",
			worktreeDir, fromResult, parentBeadsDir, projBeadsDir)
	}
	if fromResolved != projResolved {
		t.Fatalf("FindBeadsDirFrom(%q) = %q, want the worktree's own project %q", worktreeDir, fromResult, projBeadsDir)
	}
	if fromResolved != cwdResolved {
		t.Fatalf("FindBeadsDirFrom(%q) = %q disagrees with FindBeadsDir() = %q from the same directory; "+
			"-C must resolve the same project as cd", worktreeDir, fromResult, cwdResult)
	}
}
