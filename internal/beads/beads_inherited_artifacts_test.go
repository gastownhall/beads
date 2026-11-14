package beads

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		cmd := gitCommand(args...)
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
		cmd := gitCommand(args...)
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
		cmd := gitCommand(args...)
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
		cmd := gitCommand(args...)
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

// TestFindDatabasePath_WorktreeServerModeSharesMainRepo verifies that
// FindDatabasePath in a worktree with inherited server-mode metadata.json
// resolves to the main repo's .beads/dolt — NOT the worktree's metadata-only
// .beads/dolt. Without this, each worktree spawns its own dolt sql-server
// against an empty data directory, fails to find the project database, and
// leaves orphaned server processes.
//
// Regression test for the "worktree server mode duplicate server" bug.
func TestFindDatabasePath_WorktreeServerModeSharesMainRepo(t *testing.T) {
	originalEnvDir := os.Getenv("BEADS_DIR")
	originalEnvDB := os.Getenv("BEADS_DB")
	defer func() {
		if originalEnvDir != "" {
			os.Setenv("BEADS_DIR", originalEnvDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		if originalEnvDB != "" {
			os.Setenv("BEADS_DB", originalEnvDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	}()
	os.Unsetenv("BEADS_DIR")
	os.Unsetenv("BEADS_DB")

	tmpDir, err := os.MkdirTemp("", "beads-worktree-server-mode-*")
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
		cmd := gitCommand(args...)
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

	// Simulate a server-mode beads install: metadata.json has dolt_mode=server,
	// and the main repo has a dolt/ data directory (created by bd init --server).
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainDoltDir := filepath.Join(mainBeadsDir, "dolt")
	if err := os.MkdirAll(mainDoltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Server-mode metadata — the key trigger for the bug.
	mustWrite(filepath.Join(mainBeadsDir, "metadata.json"),
		`{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"test_project","project_id":"aaaa-bbbb"}`)
	mustWrite(filepath.Join(mainBeadsDir, "config.yaml"), "issue-prefix: \"tp\"\n")
	// .gitignore excludes the dolt data dir (as real projects do)
	mustWrite(filepath.Join(mainBeadsDir, ".gitignore"), "dolt/\nembeddeddolt/\n*.db\ndolt-server.*\n")

	mustWrite(filepath.Join(mainRepoDir, "README.md"), "# Test\n")
	runGit(mainRepoDir, "add", "README.md", ".beads/metadata.json", ".beads/config.yaml", ".beads/.gitignore")
	runGit(mainRepoDir, "commit", "-m", "initial with server-mode beads")

	// Create a worktree — git checks out the tracked .beads/ files
	// (metadata.json, config.yaml, .gitignore) but NOT the gitignored dolt/ dir.
	worktreeDir := filepath.Join(tmpDir, "worktree")
	runGit(mainRepoDir, "worktree", "add", worktreeDir, "HEAD")
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Sanity checks: worktree has metadata but no dolt/ dir.
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if _, err := os.Stat(filepath.Join(worktreeBeadsDir, "metadata.json")); err != nil {
		t.Fatalf("precondition: worktree should have inherited metadata.json, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreeBeadsDir, "dolt")); err == nil {
		t.Fatalf("precondition: worktree's dolt/ should be gitignored, but it exists")
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	// FindDatabasePath must resolve to the main repo's dolt dir, not the worktree's.
	result := FindDatabasePath()
	resultResolved, _ := filepath.EvalSymlinks(result)
	mainDoltResolved, _ := filepath.EvalSymlinks(mainDoltDir)
	worktreeDoltDir := filepath.Join(worktreeBeadsDir, "dolt")

	if resultResolved == worktreeDoltDir {
		t.Fatalf("FindDatabasePath() returned worktree's .beads/dolt (%q); "+
			"expected main repo's .beads/dolt (%q). "+
			"This is the duplicate-server bug: the worktree's inherited "+
			"server-mode metadata causes a separate dolt server to spawn "+
			"against an empty data directory.",
			result, mainDoltDir)
	}
	if resultResolved != mainDoltResolved {
		t.Errorf("FindDatabasePath() = %q, want main repo dolt dir %q", result, mainDoltDir)
	}

	// Also verify FindBeadsDir resolves to the main repo's .beads.
	beadsDirResult := FindBeadsDir()
	beadsDirResolved, _ := filepath.EvalSymlinks(beadsDirResult)
	mainBeadsDirResolved, _ := filepath.EvalSymlinks(mainBeadsDir)
	if beadsDirResolved != mainBeadsDirResolved {
		t.Errorf("FindBeadsDir() = %q, want main repo .beads %q", beadsDirResult, mainBeadsDir)
	}
}

// TestFindBeadsDir_JJSecondaryWithInheritedArtifacts covers the case where a
// jujutsu secondary workspace (created via `jj workspace add` without
// --colocate from a colocated jj+git primary workspace) is placed inside the
// primary's git working tree. The secondary's .beads/ contains git-tracked
// metadata files but no database, while the primary has the real database.
// FindBeadsDir must return the primary's .beads/, not the secondary's.
func TestFindBeadsDir_JJSecondaryWithInheritedArtifacts(t *testing.T) {
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	tmpDir, err := os.MkdirTemp("", "beads-jj-secondary-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	// Primary workspace: colocated jj+git, has .jj/repo as a directory and .git.
	primaryDir := filepath.Join(tmpDir, "primary")
	if err := os.MkdirAll(filepath.Join(primaryDir, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Secondary workspace: inside the primary's tree (like `jj workspace add ./workspaces/secondary`).
	// Has .jj/repo as a FILE pointing to primary's .jj/repo.
	secondaryDir := filepath.Join(primaryDir, "workspaces", "secondary")
	if err := os.MkdirAll(filepath.Join(secondaryDir, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	primaryRepoPath := filepath.Join(primaryDir, ".jj", "repo")
	if err := os.WriteFile(filepath.Join(secondaryDir, ".jj", "repo"), []byte(primaryRepoPath+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Set up git in the primary (so git rev-parse works from the secondary).
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		out, gitErr := cmd.CombinedOutput()
		if gitErr != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, gitErr, out)
		}
	}
	if err := func() error {
		cmd := exec.Command("git", "init")
		cmd.Dir = primaryDir
		return cmd.Run()
	}(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit(primaryDir, "config", "user.email", "test@example.com")
	runGit(primaryDir, "config", "user.name", "Test User")

	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Primary .beads/: real database + config.
	primaryBeadsDir := filepath.Join(primaryDir, ".beads")
	if err := os.MkdirAll(primaryBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(primaryBeadsDir, "metadata.json"), `{"database":"dolt"}`)
	mustWrite(filepath.Join(primaryBeadsDir, "config.yaml"), "issue-prefix: \"proj\"\n")
	mustWrite(filepath.Join(primaryBeadsDir, "beads.db"), "")
	mustWrite(filepath.Join(primaryBeadsDir, ".gitignore"), "*.db\ndolt/\nembeddeddolt/\n")

	// Commit the metadata so the secondary inherits it via git checkout.
	mustWrite(filepath.Join(primaryDir, "README.md"), "# Test\n")
	runGit(primaryDir, "add", "README.md", ".beads/metadata.json", ".beads/config.yaml", ".beads/.gitignore")
	runGit(primaryDir, "commit", "-m", "initial")

	// Simulate the secondary workspace inheriting tracked .beads/ artifacts (no DB).
	secondaryBeadsDir := filepath.Join(secondaryDir, ".beads")
	if err := os.MkdirAll(secondaryBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(secondaryBeadsDir, "metadata.json"), `{"database":"dolt"}`)
	mustWrite(filepath.Join(secondaryBeadsDir, "config.yaml"), "issue-prefix: \"proj\"\n")
	// No database in secondary — this is the inherited-artifacts scenario.

	t.Chdir(secondaryDir)
	git.ResetCaches()

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	primaryBeadsDirResolved, _ := filepath.EvalSymlinks(primaryBeadsDir)
	secondaryBeadsDirResolved, _ := filepath.EvalSymlinks(secondaryBeadsDir)

	if resultResolved == secondaryBeadsDirResolved {
		t.Fatalf("FindBeadsDir() returned secondary workspace's inherited .beads/ (%q); "+
			"expected primary workspace's .beads/ (%q). "+
			"Secondary has config files but no database; bd should use the primary's DB.",
			result, primaryBeadsDir)
	}
	if resultResolved != primaryBeadsDirResolved {
		t.Errorf("FindBeadsDir() = %q, want primary workspace .beads %q", result, primaryBeadsDir)
	}

	fromResult := FindBeadsDirFrom(secondaryDir)
	fromResultResolved, _ := filepath.EvalSymlinks(fromResult)
	if fromResultResolved == secondaryBeadsDirResolved {
		t.Fatalf("FindBeadsDirFrom() returned secondary workspace's inherited .beads/ (%q); "+
			"expected primary workspace's .beads/ (%q).",
			fromResult, primaryBeadsDir)
	}
	if fromResultResolved != primaryBeadsDirResolved {
		t.Errorf("FindBeadsDirFrom() = %q, want primary workspace .beads %q", fromResult, primaryBeadsDir)
	}
}

// TestFindBeadsDir_JJSecondarySeparateDBPreservesLocal verifies that when a jj
// secondary workspace genuinely has its own database (separate-DB mode),
// FindBeadsDir returns the secondary's .beads/ — not the primary's.
func TestFindBeadsDir_JJSecondarySeparateDBPreservesLocal(t *testing.T) {
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	tmpDir, err := os.MkdirTemp("", "beads-jj-secondary-sep-db-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	primaryDir := filepath.Join(tmpDir, "primary")
	if err := os.MkdirAll(filepath.Join(primaryDir, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	secondaryDir := filepath.Join(primaryDir, "workspaces", "secondary")
	if err := os.MkdirAll(filepath.Join(secondaryDir, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	primaryRepoPath := filepath.Join(primaryDir, ".jj", "repo")
	if err := os.WriteFile(filepath.Join(secondaryDir, ".jj", "repo"), []byte(primaryRepoPath+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := func() error {
		cmd := exec.Command("git", "init")
		cmd.Dir = primaryDir
		return cmd.Run()
	}(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		out, gitErr := cmd.CombinedOutput()
		if gitErr != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, gitErr, out)
		}
	}
	runGit(primaryDir, "config", "user.email", "test@example.com")
	runGit(primaryDir, "config", "user.name", "Test User")

	// Primary has a real DB.
	primaryBeadsDir := filepath.Join(primaryDir, ".beads")
	if err := os.MkdirAll(primaryBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primaryBeadsDir, "beads.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primaryDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(primaryDir, "add", "README.md")
	runGit(primaryDir, "commit", "-m", "initial")

	// Secondary ALSO has a real DB — separate-DB mode.
	secondaryBeadsDir := filepath.Join(secondaryDir, ".beads")
	if err := os.MkdirAll(secondaryBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondaryBeadsDir, "beads.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(secondaryDir)
	git.ResetCaches()

	result := FindBeadsDir()
	resultResolved, _ := filepath.EvalSymlinks(result)
	secondaryBeadsDirResolved, _ := filepath.EvalSymlinks(secondaryBeadsDir)

	if resultResolved != secondaryBeadsDirResolved {
		t.Errorf("FindBeadsDir() = %q, want secondary workspace .beads %q (separate-DB mode)",
			result, secondaryBeadsDir)
	}

	fromResult := FindBeadsDirFrom(secondaryDir)
	fromResultResolved, _ := filepath.EvalSymlinks(fromResult)
	if fromResultResolved != secondaryBeadsDirResolved {
		t.Errorf("FindBeadsDirFrom() = %q, want secondary workspace .beads %q (separate-DB mode)",
			fromResult, secondaryBeadsDir)
	}
}

// TestFindDatabasePath_WorktreeServerModeFromSubdir is the same scenario as
// TestFindDatabasePath_WorktreeServerModeSharesMainRepo but with the CWD set
// to a subdirectory of the worktree rather than the worktree root. This
// exercises the walk-up and worktree-specific code paths (as opposed to the
// CWD check which only fires at the worktree root).
func TestFindDatabasePath_WorktreeServerModeFromSubdir(t *testing.T) {
	originalEnvDir := os.Getenv("BEADS_DIR")
	originalEnvDB := os.Getenv("BEADS_DB")
	defer func() {
		if originalEnvDir != "" {
			os.Setenv("BEADS_DIR", originalEnvDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		if originalEnvDB != "" {
			os.Setenv("BEADS_DB", originalEnvDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	}()
	os.Unsetenv("BEADS_DIR")
	os.Unsetenv("BEADS_DB")

	tmpDir, err := os.MkdirTemp("", "beads-worktree-server-subdir-*")
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
		cmd := gitCommand(args...)
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

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainDoltDir := filepath.Join(mainBeadsDir, "dolt")
	if err := os.MkdirAll(mainDoltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(mainBeadsDir, "metadata.json"),
		`{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"test_project"}`)
	mustWrite(filepath.Join(mainBeadsDir, "config.yaml"), "issue-prefix: \"tp\"\n")
	mustWrite(filepath.Join(mainBeadsDir, ".gitignore"), "dolt/\nembeddeddolt/\n*.db\ndolt-server.*\n")

	mustWrite(filepath.Join(mainRepoDir, "README.md"), "# Test\n")
	runGit(mainRepoDir, "add", "README.md", ".beads/metadata.json", ".beads/config.yaml", ".beads/.gitignore")
	runGit(mainRepoDir, "commit", "-m", "initial")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	runGit(mainRepoDir, "worktree", "add", worktreeDir, "HEAD")
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// CWD is a subdirectory of the worktree — tests the worktree-specific
	// code path rather than the CWD check.
	subDir := filepath.Join(worktreeDir, "src", "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(subDir)
	git.ResetCaches()

	result := FindDatabasePath()
	resultResolved, _ := filepath.EvalSymlinks(result)
	mainDoltResolved, _ := filepath.EvalSymlinks(mainDoltDir)

	if resultResolved != mainDoltResolved {
		t.Errorf("FindDatabasePath() from subdir = %q, want main repo dolt dir %q", result, mainDoltDir)
	}
}

// unsetDiscoveryEnvForTest removes environment variables that redirect beads
// discovery or Dolt mode resolution, so a fixture's on-disk layout is the
// only input to the resolver under test. t.Setenv records each restore; the
// empty-then-unset dance keeps an ambient value (agent hosts export
// BEADS_DIR) from leaking into the test body.
func unsetDiscoveryEnvForTest(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"BEADS_DIR", "BEADS_DB",
		"BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_DATA_DIR", "BEADS_DOLT_CREDENTIAL_COMMAND",
		"BEADS_CEILING_DIRECTORIES",
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CEILING_DIRECTORIES",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

// resolveForTest canonicalizes a discovered path for comparison, failing the
// test when it cannot be resolved (discovery returned a path that does not
// exist).
func resolveForTest(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolving discovered path %q: %v", path, err)
	}
	return resolved
}

// gh6552Fixture is the GH#6552 layout: a main repository whose .beads is a
// recognized metadata-backed workspace, and a linked worktree whose .beads
// holds only a metadata-less embedded store (the phantom).
type gh6552Fixture struct {
	tmpDir           string
	mainRepoDir      string
	mainBeadsDir     string
	worktreeDir      string
	worktreeBeadsDir string
}

// newGH6552Fixture builds the layout. mainMetadata is the main .beads/
// metadata.json body (machine-local: written in the main checkout only,
// never committed, matching the gitignore template bd init writes).
// mainStoreRel is the main store directory to create relative to the main
// .beads (e.g. embeddeddolt/myproject or dolt/myproject), or "" for no local
// data directory (a server-mode main). phantomDB names the phantom database
// directory created under the worktree's .beads/embeddeddolt/.
func newGH6552Fixture(t *testing.T, mainMetadata, mainStoreRel, phantomDB string) *gh6552Fixture {
	t.Helper()

	fx := &gh6552Fixture{}
	// Resolve the temp root once (macOS /var -> /private/var) so fixture
	// paths and discovery's canonicalized answers compare as strings even
	// when an expected path, like a server-mode dolt/ directory, does not
	// exist on disk.
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fx.tmpDir = tmpDir
	fx.mainRepoDir = filepath.Join(fx.tmpDir, "main-repo")
	if err := os.MkdirAll(fx.mainRepoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fx.mainBeadsDir = filepath.Join(fx.mainRepoDir, ".beads")
	fx.worktreeDir = filepath.Join(fx.tmpDir, "worktree")
	fx.worktreeBeadsDir = filepath.Join(fx.worktreeDir, ".beads")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	cmd := gitCommand("init")
	cmd.Dir = fx.mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	// Keep the fixture's commits off any host-level hook configuration.
	runGit(fx.mainRepoDir, "config", "core.hooksPath", ".git/hooks")
	runGit(fx.mainRepoDir, "config", "user.email", "test@example.com")
	runGit(fx.mainRepoDir, "config", "user.name", "Test User")

	mustMkdir := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Main repository: a recognized shared workspace. metadata.json is
	// machine-local (gitignored); config.yaml is tracked, so the worktree
	// checks it out — the issue's exact layout.
	mustMkdir(fx.mainBeadsDir)
	mustWrite(filepath.Join(fx.mainBeadsDir, "metadata.json"), mainMetadata)
	mustWrite(filepath.Join(fx.mainBeadsDir, "config.yaml"), "issue-prefix: \"MyProject\"\n")
	mustWrite(filepath.Join(fx.mainBeadsDir, ".gitignore"), "metadata.json\ndolt/\nembeddeddolt/\n*.db\n")
	if mainStoreRel != "" {
		mustMkdir(filepath.Join(fx.mainBeadsDir, mainStoreRel))
	}
	mustWrite(filepath.Join(fx.mainRepoDir, "README.md"), "# Test\n")
	runGit(fx.mainRepoDir, "add", "README.md", ".beads/config.yaml", ".beads/.gitignore")
	runGit(fx.mainRepoDir, "commit", "-m", "initial")

	runGit(fx.mainRepoDir, "worktree", "add", fx.worktreeDir, "HEAD")
	t.Cleanup(func() {
		remove := exec.Command("git", "worktree", "remove", "--force", fx.worktreeDir)
		remove.Dir = fx.mainRepoDir
		_ = remove.Run()
	})

	// The phantom: a metadata-less embedded store in the worktree, as left
	// behind by an embedded fallback (e.g. `bd prime`) run there.
	mustMkdir(filepath.Join(fx.worktreeBeadsDir, "embeddeddolt", phantomDB))

	// Preconditions: the worktree inherited the tracked config but no
	// metadata file, and its .beads owns a database directory.
	if _, err := os.Stat(filepath.Join(fx.worktreeBeadsDir, "config.yaml")); err != nil {
		t.Fatalf("precondition: worktree .beads should have inherited config.yaml, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fx.worktreeBeadsDir, "metadata.json")); err == nil {
		t.Fatal("precondition: worktree .beads must not have metadata.json (gitignored)")
	}
	if !hasBeadsDatabase(fx.worktreeBeadsDir) {
		t.Fatal("precondition: worktree .beads should satisfy hasBeadsDatabase via the phantom embedded store")
	}
	return fx
}

// TestFindBeadsDir_WorktreeMetadatalessStoreYieldsToSharedWorkspace covers
// GH#6552: a worktree whose .beads holds only a metadata-less embedded store
// (the phantom left behind by an embedded fallback) must yield to the main
// repository's recognized workspace in directory and database-path
// discovery, from the worktree root and from an ordinary subdirectory.
// Database names do not participate: the yield is the same when the phantom
// is named like the main database and when both carry the default name. A
// server-mode main with no local data directory also qualifies: its metadata
// routes discovery, and the configured server database path is returned
// without falling back to the worktree's phantom.
func TestFindBeadsDir_WorktreeMetadatalessStoreYieldsToSharedWorkspace(t *testing.T) {
	unsetDiscoveryEnvForTest(t)

	embeddedMain := `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`
	serverMain := `{"backend":"dolt","dolt_mode":"server","dolt_database":"myproject"}`
	cases := []struct {
		name         string
		mainMetadata string
		mainStoreRel string
		phantomDB    string
		wantDBRel    string // expected FindDatabasePath relative to the main .beads
	}{
		{
			name:         "embedded main, differently named phantom",
			mainMetadata: embeddedMain,
			mainStoreRel: filepath.Join("embeddeddolt", "myproject"),
			phantomDB:    "beads",
			wantDBRel:    "embeddeddolt",
		},
		{
			name:         "phantom named like the main database",
			mainMetadata: embeddedMain,
			mainStoreRel: filepath.Join("embeddeddolt", "myproject"),
			phantomDB:    "myproject",
			wantDBRel:    "embeddeddolt",
		},
		{
			name:         "both databases carry the default name",
			mainMetadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"beads"}`,
			mainStoreRel: filepath.Join("embeddeddolt", "beads"),
			phantomDB:    "beads",
			wantDBRel:    "embeddeddolt",
		},
		{
			// AC-2: server-mode metadata with no local data directory. The
			// server database path does not need to exist (server mode has
			// no local data), so it is compared as a configured path.
			name:         "server main without local data directories",
			mainMetadata: serverMain,
			mainStoreRel: "",
			phantomDB:    "beads",
			wantDBRel:    "dolt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newGH6552Fixture(t, tc.mainMetadata, tc.mainStoreRel, tc.phantomDB)
			mainBeads := resolveForTest(t, fx.mainBeadsDir)
			mainDatabase := filepath.Join(fx.mainBeadsDir, tc.wantDBRel)

			for _, from := range []struct {
				name string
				dir  string
			}{
				{"from worktree root", fx.worktreeDir},
				{"from ordinary subdirectory", filepath.Join(fx.worktreeDir, "src", "pkg")},
			} {
				t.Run(from.name, func(t *testing.T) {
					if err := os.MkdirAll(from.dir, 0o755); err != nil {
						t.Fatal(err)
					}
					t.Chdir(from.dir)
					git.ResetCaches()

					if got := resolveForTest(t, FindBeadsDir()); got != mainBeads {
						t.Errorf("FindBeadsDir() = %q, want shared .beads %q (GH#6552: the metadata-less worktree store must yield)", FindBeadsDir(), fx.mainBeadsDir)
					}
					gotDB := FindDatabasePath()
					if tc.mainStoreRel != "" {
						// The expected database directory exists; compare
						// resolved paths.
						if got := resolveForTest(t, gotDB); got != resolveForTest(t, mainDatabase) {
							t.Errorf("FindDatabasePath() = %q, want main database %q", gotDB, mainDatabase)
						}
					} else if gotDB != mainDatabase {
						// Server mode: the configured path need not exist.
						t.Errorf("FindDatabasePath() = %q, want configured server database path %q", gotDB, mainDatabase)
					}
				})
			}
		})
	}
}

// TestFindBeadsDirFrom_WorktreeMetadatalessStoreYieldsToSharedWorkspace is
// GH#6552 with the process cwd outside the repository: FindBeadsDirFrom must
// resolve the shared main workspace for the worktree root and for a
// directory inside it, instead of the worktree's metadata-less phantom
// store.
func TestFindBeadsDirFrom_WorktreeMetadatalessStoreYieldsToSharedWorkspace(t *testing.T) {
	unsetDiscoveryEnvForTest(t)

	fx := newGH6552Fixture(t,
		`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`,
		filepath.Join("embeddeddolt", "myproject"),
		"beads")

	// The process cwd stays outside the repository, so only the explicit
	// startDir arguments reach the worktree.
	outside := filepath.Join(fx.tmpDir, "unrelated")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(outside)
	git.ResetCaches()

	mainBeads := resolveForTest(t, fx.mainBeadsDir)
	subDir := filepath.Join(fx.worktreeDir, "src", "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, start := range []struct {
		name string
		dir  string
	}{
		{"worktree root", fx.worktreeDir},
		{"subdirectory", subDir},
	} {
		t.Run(start.name, func(t *testing.T) {
			got := FindBeadsDirFrom(start.dir)
			if got == "" {
				t.Fatalf("FindBeadsDirFrom(%q) = empty, want shared .beads %q", start.dir, fx.mainBeadsDir)
			}
			if resolved := resolveForTest(t, got); resolved != mainBeads {
				t.Errorf("FindBeadsDirFrom(%q) = %q, want shared .beads %q (GH#6552: the metadata-less worktree store must yield)", start.dir, got, fx.mainBeadsDir)
			}
		})
	}
}

// captureStderrForTest swaps os.Stderr for the duration of fn and returns
// what was written to it. Discovery writes diagnostics to os.Stderr only.
func captureStderrForTest(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// captureOutputForTest swaps os.Stdout and os.Stderr for the duration of fn
// and returns what was written to each.
func captureOutputForTest(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = savedOut, savedErr }()
	fn()
	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errW.Close(); err != nil {
		t.Fatal(err)
	}
	stdoutBytes, err := io.ReadAll(outR)
	if err != nil {
		t.Fatal(err)
	}
	stderrBytes, err := io.ReadAll(errR)
	if err != nil {
		t.Fatal(err)
	}
	return string(stdoutBytes), string(stderrBytes)
}

// TestFindBeadsDir_WorktreeStoreWithMetadataNotYielded is the AC-4 guard for
// the GH#6552 fix: a worktree store that carries readable workspace metadata
// of its own — however it got there, and whether or not it names a database
// — keeps existing separate-database precedence beside a metadata-backed
// shared workspace, in directory and database-path discovery, with no
// missing-metadata warning. Passes before and after the fix.
func TestFindBeadsDir_WorktreeStoreWithMetadataNotYielded(t *testing.T) {
	unsetDiscoveryEnvForTest(t)

	cases := []struct {
		name string
		// localMetadata is written into the worktree's .beads; legacy
		// selects the legacy config.json filename for it.
		localMetadata string
		legacy        bool
	}{
		{
			name:          "own metadata with database name",
			localMetadata: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"workdb"}`,
		},
		{
			name:          "metadata without a database name",
			localMetadata: `{"backend":"dolt","dolt_mode":"embedded"}`,
		},
		{
			name:          "legacy config.json metadata",
			localMetadata: `{"backend":"dolt","dolt_database":"workdb"}`,
			legacy:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newGH6552Fixture(t,
				`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`,
				filepath.Join("embeddeddolt", "myproject"),
				"beads")

			metadataName := "metadata.json"
			if tc.legacy {
				metadataName = "config.json"
			}
			if err := os.WriteFile(filepath.Join(fx.worktreeBeadsDir, metadataName), []byte(tc.localMetadata), 0o644); err != nil {
				t.Fatal(err)
			}

			t.Chdir(fx.worktreeDir)
			git.ResetCaches()

			worktreeBeads := resolveForTest(t, fx.worktreeBeadsDir)
			worktreeEmbedded := resolveForTest(t, filepath.Join(fx.worktreeBeadsDir, "embeddeddolt"))

			stderr := strings.TrimSpace(captureStderrForTest(t, func() {
				if got := resolveForTest(t, FindBeadsDir()); got != worktreeBeads {
					t.Errorf("FindBeadsDir() = %q, want worktree .beads %q (a store with its own metadata is separate-DB mode, not a phantom)", FindBeadsDir(), fx.worktreeBeadsDir)
				}
				if got := resolveForTest(t, FindDatabasePath()); got != worktreeEmbedded {
					t.Errorf("FindDatabasePath() = %q, want worktree embedded store %q", FindDatabasePath(), filepath.Join(fx.worktreeBeadsDir, "embeddeddolt"))
				}
				if got := resolveForTest(t, FindBeadsDirFrom(fx.worktreeDir)); got != worktreeBeads {
					t.Errorf("FindBeadsDirFrom(%q) = %q, want worktree .beads %q", fx.worktreeDir, FindBeadsDirFrom(fx.worktreeDir), fx.worktreeBeadsDir)
				}
			}))

			if strings.Contains(stderr, "GH#6552") {
				t.Errorf("no missing-metadata warning expected for a store with its own metadata, got stderr:\n%s", stderr)
			}
		})
	}
}

// TestFindBeadsDir_YieldRequiresRecognizedSharedWorkspace guards the other
// direction of the GH#6552 decision: the yield fires only beside a shared
// workspace that existing directory discovery recognizes by readable
// metadata. A shared candidate without workspace metadata does not displace
// the local store, and a standalone (non-worktree) repository is never
// subject to the yield at all. Passes before and after the fix.
func TestFindBeadsDir_YieldRequiresRecognizedSharedWorkspace(t *testing.T) {
	unsetDiscoveryEnvForTest(t)

	t.Run("shared workspace without metadata does not displace the local store", func(t *testing.T) {
		// Same layout as the GH#6552 fixture, minus the main metadata.json:
		// the shared candidate has project files and a store, but nothing
		// that declares it a workspace.
		fx := newGH6552Fixture(t,
			`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`,
			filepath.Join("embeddeddolt", "myproject"),
			"beads")
		if err := os.Remove(filepath.Join(fx.mainBeadsDir, "metadata.json")); err != nil {
			t.Fatal(err)
		}

		t.Chdir(fx.worktreeDir)
		git.ResetCaches()

		worktreeBeads := resolveForTest(t, fx.worktreeBeadsDir)
		worktreeEmbedded := resolveForTest(t, filepath.Join(fx.worktreeBeadsDir, "embeddeddolt"))
		if got := resolveForTest(t, FindBeadsDir()); got != worktreeBeads {
			t.Errorf("FindBeadsDir() = %q, want worktree .beads %q (an unrecognized shared candidate must not displace the local store)", FindBeadsDir(), fx.worktreeBeadsDir)
		}
		if got := resolveForTest(t, FindDatabasePath()); got != worktreeEmbedded {
			t.Errorf("FindDatabasePath() = %q, want worktree embedded store %q", FindDatabasePath(), filepath.Join(fx.worktreeBeadsDir, "embeddeddolt"))
		}
	})

	t.Run("standalone repository keeps its metadata-less store", func(t *testing.T) {
		repoDir := filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(filepath.Join(repoDir, ".beads", "embeddeddolt", "beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoDir, ".beads", "config.yaml"), []byte("issue-prefix: \"R\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		t.Chdir(repoDir)
		git.ResetCaches()

		repoBeads := resolveForTest(t, filepath.Join(repoDir, ".beads"))
		if got := resolveForTest(t, FindBeadsDir()); got != repoBeads {
			t.Errorf("FindBeadsDir() = %q, want %q (standalone stores are never subject to the yield)", FindBeadsDir(), filepath.Join(repoDir, ".beads"))
		}
	})
}

// TestFindBeadsDir_ExplicitOverridesBypassYield is the AC-5 guard for
// GH#6552: explicit choices — BEADS_DIR, BEADS_DB and a valid per-worktree
// redirect — keep selecting the worktree's own store; the implicit yield
// never applies to them and no missing-metadata warning is emitted. Passes
// before and after the fix.
func TestFindBeadsDir_ExplicitOverridesBypassYield(t *testing.T) {
	unsetDiscoveryEnvForTest(t)

	newFixture := func(t *testing.T) *gh6552Fixture {
		t.Helper()
		return newGH6552Fixture(t,
			`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`,
			filepath.Join("embeddeddolt", "myproject"),
			"beads")
	}
	worktreePaths := func(t *testing.T, fx *gh6552Fixture) (string, string) {
		t.Helper()
		return resolveForTest(t, fx.worktreeBeadsDir),
			resolveForTest(t, filepath.Join(fx.worktreeBeadsDir, "embeddeddolt"))
	}

	t.Run("BEADS_DIR names the local store", func(t *testing.T) {
		fx := newFixture(t)
		t.Setenv("BEADS_DIR", fx.worktreeBeadsDir)
		t.Chdir(fx.worktreeDir)
		git.ResetCaches()

		wantBeads, wantEmbedded := worktreePaths(t, fx)
		stderr := strings.TrimSpace(captureStderrForTest(t, func() {
			if got := resolveForTest(t, FindBeadsDir()); got != wantBeads {
				t.Errorf("FindBeadsDir() = %q, want the BEADS_DIR-named %q", FindBeadsDir(), fx.worktreeBeadsDir)
			}
			if got := resolveForTest(t, FindDatabasePath()); got != wantEmbedded {
				t.Errorf("FindDatabasePath() = %q, want the BEADS_DIR-named store %q", FindDatabasePath(), filepath.Join(fx.worktreeBeadsDir, "embeddeddolt"))
			}
		}))
		if strings.Contains(stderr, "GH#6552") {
			t.Errorf("no missing-metadata warning expected for an explicit BEADS_DIR, got stderr:\n%s", stderr)
		}
	})

	t.Run("BEADS_DB names the local store", func(t *testing.T) {
		fx := newFixture(t)
		t.Setenv("BEADS_DB", fx.worktreeBeadsDir)
		t.Chdir(fx.worktreeDir)
		git.ResetCaches()

		_, wantEmbedded := worktreePaths(t, fx)
		stderr := strings.TrimSpace(captureStderrForTest(t, func() {
			if got := resolveForTest(t, FindDatabasePath()); got != wantEmbedded {
				t.Errorf("FindDatabasePath() = %q, want the BEADS_DB-named store %q", FindDatabasePath(), filepath.Join(fx.worktreeBeadsDir, "embeddeddolt"))
			}
		}))
		if strings.Contains(stderr, "GH#6552") {
			t.Errorf("no missing-metadata warning expected for an explicit BEADS_DB, got stderr:\n%s", stderr)
		}
	})

	t.Run("valid worktree redirect wins before the yield", func(t *testing.T) {
		fx := newFixture(t)

		// A third workspace the redirect selects explicitly.
		externalBeads := filepath.Join(fx.tmpDir, "external", ".beads")
		if err := os.MkdirAll(filepath.Join(externalBeads, "embeddeddolt", "ext"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(externalBeads, "metadata.json"),
			[]byte(`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"ext"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fx.worktreeBeadsDir, "redirect"),
			[]byte(externalBeads+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		t.Chdir(fx.worktreeDir)
		git.ResetCaches()

		wantBeads := resolveForTest(t, externalBeads)
		wantEmbedded := resolveForTest(t, filepath.Join(externalBeads, "embeddeddolt"))
		stderr := strings.TrimSpace(captureStderrForTest(t, func() {
			if got := resolveForTest(t, FindBeadsDir()); got != wantBeads {
				t.Errorf("FindBeadsDir() = %q, want the redirect target %q", FindBeadsDir(), externalBeads)
			}
			if got := resolveForTest(t, FindDatabasePath()); got != wantEmbedded {
				t.Errorf("FindDatabasePath() = %q, want the redirect target's store %q", FindDatabasePath(), filepath.Join(externalBeads, "embeddeddolt"))
			}
		}))
		if strings.Contains(stderr, "GH#6552") {
			t.Errorf("no missing-metadata warning expected for an explicit redirect, got stderr:\n%s", stderr)
		}
	})
}

// TestFindBeadsDir_WorktreeMetadatalessStoreWarnsOnce covers the GH#6552
// warning: invoking every discovery entry point repeatedly in one process
// yields exactly one warning on stderr — naming the skipped and shared
// directories, the missing-metadata reason, the observed and declared
// database names, and the BEADS_DIR inspection override — and writes
// nothing to stdout.
func TestFindBeadsDir_WorktreeMetadatalessStoreWarnsOnce(t *testing.T) {
	unsetDiscoveryEnvForTest(t)

	fx := newGH6552Fixture(t,
		`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`,
		filepath.Join("embeddeddolt", "myproject"),
		"beads")

	// Give the phantom a recognizable database marker so the warning can
	// name it: the marker probe requires a nonempty .dolt directory.
	markerDir := filepath.Join(fx.worktreeBeadsDir, "embeddeddolt", "beads", ".dolt")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "placeholder"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(fx.worktreeDir)
	git.ResetCaches()

	stdout, stderr := captureOutputForTest(t, func() {
		for i := 0; i < 3; i++ {
			_ = FindBeadsDir()
			_ = FindDatabasePath()
			_ = FindBeadsDirFrom(fx.worktreeDir)
		}
	})

	if strings.TrimSpace(stdout) != "" {
		t.Errorf("discovery wrote diagnostics to stdout:\n%s", stdout)
	}

	if count := strings.Count(stderr, "GH#6552"); count != 1 {
		t.Errorf("got %d missing-metadata warnings for repeated discovery of one store, want exactly 1; stderr:\n%s", count, stderr)
	}
	for _, want := range []string{
		fx.worktreeBeadsDir,
		fx.mainBeadsDir,
		"no metadata.json or config.json",
		"marker probe, not a content check): beads",
		`dolt_database "myproject"`,
		"BEADS_DIR='" + fx.worktreeBeadsDir + "' bd list",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning is missing %q; stderr:\n%s", want, stderr)
		}
	}
}

// TestFindBeadsDir_JJSecondaryMetadatalessStoreYieldsToPrimary is GH#6552 in
// a jj secondary workspace: an embedded store with no workspace metadata of
// its own yields to the recognized primary workspace consistently in
// directory, database-path and start-directory discovery.
func TestFindBeadsDir_JJSecondaryMetadatalessStoreYieldsToPrimary(t *testing.T) {
	unsetDiscoveryEnvForTest(t)

	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	primaryDir := filepath.Join(tmpDir, "primary")
	if err := os.MkdirAll(filepath.Join(primaryDir, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	secondaryDir := filepath.Join(primaryDir, "workspaces", "secondary")
	if err := os.MkdirAll(filepath.Join(secondaryDir, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	primaryRepoPath := filepath.Join(primaryDir, ".jj", "repo")
	if err := os.WriteFile(filepath.Join(secondaryDir, ".jj", "repo"), []byte(primaryRepoPath+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := func() error {
		cmd := exec.Command("git", "init")
		cmd.Dir = primaryDir
		return cmd.Run()
	}(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		out, gitErr := cmd.CombinedOutput()
		if gitErr != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, gitErr, out)
		}
	}
	runGit(primaryDir, "config", "user.email", "test@example.com")
	runGit(primaryDir, "config", "user.name", "Test User")

	// Primary: recognized metadata-backed embedded workspace.
	primaryBeadsDir := filepath.Join(primaryDir, ".beads")
	if err := os.MkdirAll(filepath.Join(primaryBeadsDir, "embeddeddolt", "myproject"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primaryBeadsDir, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primaryDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(primaryDir, "add", "README.md")
	runGit(primaryDir, "commit", "-m", "initial")

	// Secondary: a metadata-less phantom embedded store, nothing else.
	secondaryBeadsDir := filepath.Join(secondaryDir, ".beads")
	if err := os.MkdirAll(filepath.Join(secondaryBeadsDir, "embeddeddolt", "beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(secondaryDir)
	git.ResetCaches()

	primaryBeadsDirResolved := resolveForTest(t, primaryBeadsDir)

	if got := FindBeadsDir(); resolveForTest(t, got) != primaryBeadsDirResolved {
		t.Errorf("FindBeadsDir() = %q, want primary workspace .beads %q", got, primaryBeadsDir)
	}
	wantDB := filepath.Join(primaryBeadsDir, "embeddeddolt")
	if got := FindDatabasePath(); resolveForTest(t, got) != resolveForTest(t, wantDB) {
		t.Errorf("FindDatabasePath() = %q, want %q", got, wantDB)
	}
	if got := FindBeadsDirFrom(secondaryDir); resolveForTest(t, got) != primaryBeadsDirResolved {
		t.Errorf("FindBeadsDirFrom(secondaryDir) = %q, want primary workspace .beads %q", got, primaryBeadsDir)
	}
	subDir := filepath.Join(secondaryDir, "src", "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindBeadsDirFrom(subDir); resolveForTest(t, got) != primaryBeadsDirResolved {
		t.Errorf("FindBeadsDirFrom(subDir) = %q, want primary workspace .beads %q", got, primaryBeadsDir)
	}
}

// TestFindDatabasePath_WorktreeYieldedStoreNotRediscovered pins the
// readoption half of GH#6552: when a metadata-less worktree store yields to
// a shared workspace that is recognized by metadata but owns no database
// directory yet (fresh checkout, data never fetched), neither the lenient
// branch nor the trailing ancestor walk quietly readopts the yielded phantom
// store, and discovery does not materialize anything on disk. The jj
// secondary layout is the same decision's edge case.
func TestFindDatabasePath_WorktreeYieldedStoreNotRediscovered(t *testing.T) {
	unsetDiscoveryEnvForTest(t)

	resolveLoose := func(p string) string {
		if p == "" {
			return ""
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}

	fx := newGH6552Fixture(t,
		`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`,
		"", // no store directory in the shared .beads: recognized by metadata alone
		"beads",
	)
	mainBeadsDirResolved := resolveForTest(t, fx.mainBeadsDir)
	worktreeDirResolved := resolveForTest(t, fx.worktreeDir)

	for _, cwd := range []string{fx.worktreeDir, filepath.Join(fx.worktreeDir, "src", "pkg")} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(cwd)
		git.ResetCaches()

		if got := FindBeadsDir(); resolveForTest(t, got) != mainBeadsDirResolved {
			t.Errorf("cwd %s: FindBeadsDir() = %q, want shared workspace .beads %q", cwd, got, fx.mainBeadsDir)
		}
		if got := FindDatabasePath(); got != "" &&
			strings.HasPrefix(resolveLoose(got), worktreeDirResolved+string(os.PathSeparator)) {
			t.Errorf("cwd %s: FindDatabasePath() = %q readopts the yielded worktree store; want \"\" or a path outside the worktree", cwd, got)
		}
	}

	// The jj secondary layout: primary recognized by metadata with no store
	// directory, secondary holding the metadata-less phantom.
	jjTmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	primaryDir := filepath.Join(jjTmp, "primary")
	if err := os.MkdirAll(filepath.Join(primaryDir, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	secondaryDir := filepath.Join(primaryDir, "workspaces", "secondary")
	if err := os.MkdirAll(filepath.Join(secondaryDir, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondaryDir, ".jj", "repo"),
		[]byte(filepath.Join(primaryDir, ".jj", "repo")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		cmd := exec.Command("git", "init")
		cmd.Dir = primaryDir
		return cmd.Run()
	}(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		out, gitErr := cmd.CombinedOutput()
		if gitErr != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, gitErr, out)
		}
	}
	runGit(primaryDir, "config", "user.email", "test@example.com")
	runGit(primaryDir, "config", "user.name", "Test User")

	primaryBeadsDir := filepath.Join(primaryDir, ".beads")
	if err := os.MkdirAll(primaryBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primaryBeadsDir, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primaryDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(primaryDir, "add", "README.md")
	runGit(primaryDir, "commit", "-m", "initial")
	secondaryBeadsDir := filepath.Join(secondaryDir, ".beads")
	if err := os.MkdirAll(filepath.Join(secondaryBeadsDir, "embeddeddolt", "beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(secondaryDir)
	git.ResetCaches()

	primaryBeadsDirResolved := resolveForTest(t, primaryBeadsDir)
	if got := FindBeadsDir(); resolveForTest(t, got) != primaryBeadsDirResolved {
		t.Errorf("jj secondary: FindBeadsDir() = %q, want primary workspace .beads %q", got, primaryBeadsDir)
	}
	if got := FindDatabasePath(); got != "" &&
		strings.HasPrefix(resolveLoose(got), resolveLoose(secondaryDir)+string(os.PathSeparator)) {
		t.Errorf("jj secondary: FindDatabasePath() = %q readopts the yielded secondary store; want \"\" or a path outside the secondary", got)
	}

	// Discovery is read-only: the shared workspace's absent store directory
	// stays absent, and the yielded local store is neither deleted nor given
	// metadata.
	if _, err := os.Stat(filepath.Join(fx.mainBeadsDir, "embeddeddolt")); !os.IsNotExist(err) {
		t.Errorf("shared .beads materialized a store directory during discovery (stat err: %v)", err)
	}
	if _, err := os.Stat(filepath.Join(primaryBeadsDir, "embeddeddolt")); !os.IsNotExist(err) {
		t.Errorf("jj primary .beads materialized a store directory during discovery (stat err: %v)", err)
	}
	if _, err := os.Stat(filepath.Join(fx.worktreeBeadsDir, "metadata.json")); err == nil {
		t.Error("yielded worktree store gained a metadata.json during discovery")
	}
	if _, err := os.Stat(filepath.Join(fx.worktreeBeadsDir, "embeddeddolt", "beads")); err != nil {
		t.Errorf("yielded worktree store was modified or removed: %v", err)
	}
}
