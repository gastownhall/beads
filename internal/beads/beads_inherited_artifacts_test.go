package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// TestFindBeadsDir_WorktreeWithInheritedArtifacts covers the case where a
// git worktree inherits the parent repo's tracked .beads/ artifacts —
// metadata.json, config.yaml, issues.jsonl, etc. — but NOT an actual Dolt
// database directory (which is gitignored under .beads/.gitignore). Without
// a real database, the worktree must resolve to the shared .beads/ via the
// git common-dir fallback; otherwise bd would spawn a sidecar Dolt server
// against an empty data directory and fail every query.
//
// Regression test for "bd worktree create --help" promise: "The worktree
// automatically shares the same beads database as the main repository via
// git common directory discovery — no manual redirect configuration needed."
func TestFindBeadsDir_WorktreeWithInheritedArtifacts(t *testing.T) {
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	tmpDir, err := os.MkdirTemp("", "beads-inherited-artifacts-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit(mainRepoDir, "config", "user.email", "test@example.com")
	runGit(mainRepoDir, "config", "user.name", "Test User")

	// Simulate a real parent-repo beads install: metadata.json, config.yaml,
	// issues.jsonl, AND a database. The database is what makes this a real
	// beads project.
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(mainBeadsDir, "metadata.json"), `{"database":"dolt","dolt_database":"beads"}`)
	mustWrite(filepath.Join(mainBeadsDir, "config.yaml"), "issue-prefix: \"proj\"\n")
	mustWrite(filepath.Join(mainBeadsDir, "issues.jsonl"), "")
	mustWrite(filepath.Join(mainBeadsDir, "beads.db"), "")

	// Commit the artifact files (NOT the db — gitignore it as real projects do).
	mustWrite(filepath.Join(mainBeadsDir, ".gitignore"), "*.db\ndolt/\nembeddeddolt/\n")
	mustWrite(filepath.Join(mainRepoDir, "README.md"), "# Test\n")
	runGit(mainRepoDir, "add", "README.md", ".beads/metadata.json", ".beads/config.yaml", ".beads/issues.jsonl", ".beads/.gitignore")
	runGit(mainRepoDir, "commit", "-m", "initial")

	// Create a worktree by git — `git worktree add` checks out the tracked
	// files, so the worktree's .beads/ ends up with metadata.json + config.yaml
	// + issues.jsonl but no database (since *.db is gitignored).
	worktreeDir := filepath.Join(tmpDir, "worktree")
	runGit(mainRepoDir, "worktree", "add", worktreeDir, "HEAD")
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Sanity-check the shape of the worktree's .beads/.
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if _, err := os.Stat(filepath.Join(worktreeBeadsDir, "metadata.json")); err != nil {
		t.Fatalf("precondition: worktree should have inherited metadata.json, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreeBeadsDir, "beads.db")); err == nil {
		t.Fatalf("precondition: worktree's *.db should be gitignored, but beads.db exists")
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	result := FindBeadsDir()

	// Expect the shared (main repo's) .beads/ — not the worktree's
	// metadata-only one. Resolve symlinks for macOS /var → /private/var.
	resultResolved, _ := filepath.EvalSymlinks(result)
	mainResolved, _ := filepath.EvalSymlinks(mainBeadsDir)
	worktreeResolved, _ := filepath.EvalSymlinks(worktreeBeadsDir)

	if resultResolved == worktreeResolved {
		t.Fatalf("FindBeadsDir() returned the worktree's inherited-artifacts .beads/ (%q); "+
			"expected the shared .beads/ (%q). This is the sidecar-Dolt-server bug: "+
			"bd should not try to run a database out of a directory that only contains "+
			"tracked metadata files.", result, mainBeadsDir)
	}
	if resultResolved != mainResolved {
		t.Errorf("FindBeadsDir() = %q, want shared .beads %q", result, mainBeadsDir)
	}
}

// TestFindBeadsDir_WorktreeSeparateDBPreservesLocal verifies the companion
// case: if a worktree really does own its own database (true separate-DB
// mode), FindBeadsDir must continue to return the worktree's .beads/ — the
// inherited-artifacts fix must not regress separate-DB mode.
func TestFindBeadsDir_WorktreeSeparateDBPreservesLocal(t *testing.T) {
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	tmpDir, err := os.MkdirTemp("", "beads-separate-db-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit(mainRepoDir, "config", "user.email", "test@example.com")
	runGit(mainRepoDir, "config", "user.name", "Test User")

	// Main repo has its own beads install WITH a database.
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainBeadsDir, "beads.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepoDir, "add", "README.md")
	runGit(mainRepoDir, "commit", "-m", "initial")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	runGit(mainRepoDir, "worktree", "add", worktreeDir, "HEAD")
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Worktree has its OWN real database — separate-DB mode.
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeBeadsDir, "beads.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	result := FindBeadsDir()
	resultResolved, _ := filepath.EvalSymlinks(result)
	worktreeResolved, _ := filepath.EvalSymlinks(worktreeBeadsDir)

	if resultResolved != worktreeResolved {
		t.Errorf("FindBeadsDir() = %q, want worktree .beads %q (separate-DB mode)",
			result, worktreeBeadsDir)
	}
}

// TestFindBeadsDir_WorktreeSeparateDBPreservesLocalWithDoltDir is the same
// separate-DB regression guard but exercises the `dolt/` directory branch of
// hasBeadsDatabase (server mode) rather than the *.db branch.
func TestFindBeadsDir_WorktreeSeparateDBPreservesLocalWithDoltDir(t *testing.T) {
	runWorktreeSeparateDBPreservedTest(t, "dolt")
}

// TestFindBeadsDir_WorktreeSeparateDBPreservesLocalWithEmbeddedDolt exercises
// the `embeddeddolt/` directory branch of hasBeadsDatabase (embedded-engine
// mode) for the separate-DB regression guard.
func TestFindBeadsDir_WorktreeSeparateDBPreservesLocalWithEmbeddedDolt(t *testing.T) {
	runWorktreeSeparateDBPreservedTest(t, "embeddeddolt")
}

// runWorktreeSeparateDBPreservedTest is shared by the three separate-DB
// regression tests. databaseMarker names what to create inside the worktree's
// .beads/ so hasBeadsDatabase returns true: "dolt" / "embeddeddolt" (directory)
// or "beads.db" (file).
func runWorktreeSeparateDBPreservedTest(t *testing.T, databaseMarker string) {
	t.Helper()

	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	tmpDir, err := os.MkdirTemp("", "beads-separate-db-variant-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit(mainRepoDir, "config", "user.email", "test@example.com")
	runGit(mainRepoDir, "config", "user.name", "Test User")

	// Main repo has a real DB so the shared fallback would win if the
	// worktree's own database weren't recognized.
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainBeadsDir, "beads.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepoDir, "add", "README.md")
	runGit(mainRepoDir, "commit", "-m", "initial")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	runGit(mainRepoDir, "worktree", "add", worktreeDir, "HEAD")
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	switch databaseMarker {
	case "dolt", "embeddeddolt":
		if err := os.MkdirAll(filepath.Join(worktreeBeadsDir, databaseMarker), 0o755); err != nil {
			t.Fatal(err)
		}
	default:
		if err := os.WriteFile(filepath.Join(worktreeBeadsDir, databaseMarker), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	result := FindBeadsDir()
	resultResolved, _ := filepath.EvalSymlinks(result)
	worktreeResolved, _ := filepath.EvalSymlinks(worktreeBeadsDir)

	if resultResolved != worktreeResolved {
		t.Errorf("FindBeadsDir() = %q, want worktree .beads %q (separate-DB mode via %q)",
			result, worktreeBeadsDir, databaseMarker)
	}
}

// TestFindBeadsDir_WorktreeNoDatabaseAnywhereFallsBackToLocal exercises the
// lenient-fallback escape hatch in step 3b: when the worktree has inherited
// metadata but no database AND the shared fallback also has no database,
// FindBeadsDir should return the worktree-local .beads/ so a fresh
// `bd init` in the worktree can still bootstrap. If the strict check ran
// unconditionally, a brand-new worktree with no main-repo database would
// return empty and block init.
func TestFindBeadsDir_WorktreeNoDatabaseAnywhereFallsBackToLocal(t *testing.T) {
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	tmpDir, err := os.MkdirTemp("", "beads-no-db-anywhere-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit(mainRepoDir, "config", "user.email", "test@example.com")
	runGit(mainRepoDir, "config", "user.name", "Test User")

	// Main repo has a .beads/ with metadata only — NO database. This is the
	// "pre-bd-init" state on both sides.
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainBeadsDir, "metadata.json"), []byte(`{"database":"dolt"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainBeadsDir, ".gitignore"), []byte("*.db\ndolt/\nembeddeddolt/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepoDir, "add", "README.md", ".beads/metadata.json", ".beads/.gitignore")
	runGit(mainRepoDir, "commit", "-m", "seed .beads without db")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	runGit(mainRepoDir, "worktree", "add", worktreeDir, "HEAD")
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if _, err := os.Stat(filepath.Join(worktreeBeadsDir, "metadata.json")); err != nil {
		t.Fatalf("precondition: worktree should have inherited metadata.json, got: %v", err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	result := FindBeadsDir()
	if result == "" {
		t.Fatal("FindBeadsDir returned empty; lenient escape hatch should return the worktree .beads/ when no DB exists anywhere")
	}
	resultResolved, _ := filepath.EvalSymlinks(result)
	worktreeResolved, _ := filepath.EvalSymlinks(worktreeBeadsDir)
	if resultResolved != worktreeResolved {
		t.Errorf("FindBeadsDir() = %q, want worktree .beads %q (lenient fallback — no DB anywhere)",
			result, worktreeBeadsDir)
	}
}
