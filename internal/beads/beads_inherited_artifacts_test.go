package beads

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/utils"
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

// metadatalessStoreFixture is a main checkout plus a linked git worktree whose
// .beads/ holds the tracked config.yaml and a metadata-less embedded store
// (embeddeddolt/beads), the GH#6552 layout.
type metadatalessStoreFixture struct {
	mainRepo      string
	mainBeads     string
	worktree      string
	worktreeBeads string
	subdir        string
}

// clearDiscoveryEnv isolates a discovery test from the caller's routing
// environment.
func clearDiscoveryEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"BEADS_DIR", "BEADS_DB", "BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER", "GIT_DIR", "GIT_WORK_TREE"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

func writeFixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeEmbeddedStore creates beadsDir/embeddeddolt/<name>/.dolt with an
// innocuous entry, the shape configfile.EmbeddedDoltDatabases recognizes.
func writeEmbeddedStore(t *testing.T, beadsDir, name string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(beadsDir, "embeddeddolt", name, ".dolt", "noms", "manifest"), "fixture\n")
}

const (
	mainEmbeddedMetadata = `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"myproject"}`
	mainServerMetadata   = `{"backend":"dolt","dolt_mode":"server","dolt_database":"myproject"}`
)

// newMetadatalessStoreFixture builds the main checkout (mainMetadata written
// to main's metadata.json unless empty; mainStore names an embedded store to
// create in main unless empty) and a linked worktree whose .beads/ carries
// the tracked config.yaml plus a metadata-less embeddeddolt/beads store.
func newMetadatalessStoreFixture(t *testing.T, mainMetadata, mainStore string) *metadatalessStoreFixture {
	t.Helper()
	clearDiscoveryEnv(t)

	tmpDir := utils.CanonicalizePath(t.TempDir())
	f := &metadatalessStoreFixture{mainRepo: filepath.Join(tmpDir, "main-repo")}
	f.mainBeads = filepath.Join(f.mainRepo, ".beads")
	f.worktree = filepath.Join(tmpDir, "worktree")
	f.worktreeBeads = filepath.Join(f.worktree, ".beads")
	f.subdir = filepath.Join(f.worktree, "src", "pkg")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	if err := os.MkdirAll(f.mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(f.mainRepo, "init")
	runGit(f.mainRepo, "config", "user.email", "test@example.com")
	runGit(f.mainRepo, "config", "user.name", "Test User")
	runGit(f.mainRepo, "config", "core.hooksPath", ".git/hooks")

	writeFixtureFile(t, filepath.Join(f.mainBeads, "config.yaml"), "issue-prefix: \"MyProject\"\n")
	writeFixtureFile(t, filepath.Join(f.mainBeads, ".gitignore"), "metadata.json\nconfig.json\ndolt/\nembeddeddolt/\n*.db\n")
	writeFixtureFile(t, filepath.Join(f.mainRepo, "README.md"), "# Test\n")
	if mainMetadata != "" {
		writeFixtureFile(t, filepath.Join(f.mainBeads, "metadata.json"), mainMetadata)
	}
	if mainStore != "" {
		writeEmbeddedStore(t, f.mainBeads, mainStore)
	}
	runGit(f.mainRepo, "add", "README.md", ".beads/config.yaml", ".beads/.gitignore")
	runGit(f.mainRepo, "commit", "-m", "initial")

	runGit(f.mainRepo, "worktree", "add", f.worktree, "HEAD")
	t.Cleanup(func() {
		cmd := gitCommand("worktree", "remove", "--force", f.worktree)
		cmd.Dir = f.mainRepo
		_ = cmd.Run()
	})
	if _, err := os.Stat(filepath.Join(f.worktreeBeads, "config.yaml")); err != nil {
		t.Fatalf("worktree did not check out tracked config.yaml: %v", err)
	}
	if err := os.MkdirAll(f.subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The phantom: a metadata-less embedded store under the default name.
	writeEmbeddedStore(t, f.worktreeBeads, "beads")
	return f
}

// chdirForDiscovery moves the process into dir and drops cached git state.
func chdirForDiscovery(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
	git.ResetCaches()
}

func assertSamePath(t *testing.T, label, got, want string) {
	t.Helper()
	if got == "" || utils.CanonicalizePath(got) != utils.CanonicalizePath(want) {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

// pathWithin reports whether path is root or lies beneath it, comparing
// canonical forms.
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(utils.CanonicalizePath(root), utils.CanonicalizePath(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// TestFindBeadsDir_WorktreeMetadatalessStoreYieldsToSharedWorkspace covers
// GH#6552: a metadata-less embedded store in a linked worktree yields to the
// main checkout's metadata-backed workspace, from the worktree root and from
// an ordinary subdirectory, for both directory and database-path discovery.
func TestFindBeadsDir_WorktreeMetadatalessStoreYieldsToSharedWorkspace(t *testing.T) {
	cases := []struct {
		name      string
		mainStore string
	}{
		{name: "main store named myproject", mainStore: "myproject"},
		{name: "main store also named beads", mainStore: "beads"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMetadatalessStoreFixture(t, mainEmbeddedMetadata, tc.mainStore)
			for _, start := range []string{f.worktree, f.subdir} {
				chdirForDiscovery(t, start)
				assertSamePath(t, "FindBeadsDir() from "+start, FindBeadsDir(), f.mainBeads)
				assertSamePath(t, "FindDatabasePath() from "+start, FindDatabasePath(), filepath.Join(f.mainBeads, "embeddeddolt"))
			}
		})
	}
}

// TestFindBeadsDir_WorktreeMetadatalessStoreYieldsToSharedServer covers the
// server-mode main checkout with no local data directory: its metadata alone
// makes it the shared workspace the metadata-less worktree store yields to.
func TestFindBeadsDir_WorktreeMetadatalessStoreYieldsToSharedServer(t *testing.T) {
	f := newMetadatalessStoreFixture(t, mainServerMetadata, "")
	for _, start := range []string{f.worktree, f.subdir} {
		chdirForDiscovery(t, start)
		assertSamePath(t, "FindBeadsDir() from "+start, FindBeadsDir(), f.mainBeads)
		dbPath := FindDatabasePath()
		assertSamePath(t, "FindDatabasePath() from "+start, dbPath, filepath.Join(f.mainBeads, "dolt"))
		if pathWithin(dbPath, f.worktree) {
			t.Errorf("FindDatabasePath() from %s = %q, lies under the worktree %q", start, dbPath, f.worktree)
		}
	}
}

// TestFindBeadsDirFrom_WorktreeMetadatalessStoreYieldsToSharedWorkspace
// covers FindBeadsDirFrom called with the worktree root or a subdirectory
// while the process cwd is outside the repository.
func TestFindBeadsDirFrom_WorktreeMetadatalessStoreYieldsToSharedWorkspace(t *testing.T) {
	f := newMetadatalessStoreFixture(t, mainEmbeddedMetadata, "myproject")
	chdirForDiscovery(t, t.TempDir())
	for _, start := range []string{f.worktree, f.subdir} {
		assertSamePath(t, "FindBeadsDirFrom("+start+")", FindBeadsDirFrom(start), f.mainBeads)
	}
}

// TestFindBeadsDir_WorktreeStoreWithMetadataPreservesLocal guards the
// separate-database precedence: any readable or unreadable local metadata
// keeps the worktree store selected and emits no missing-metadata warning.
func TestFindBeadsDir_WorktreeStoreWithMetadataPreservesLocal(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		body     string
		wantDBIn bool // FindDatabasePath selects the local embedded store
	}{
		{name: "own metadata", file: "metadata.json", body: `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"wtdb"}`, wantDBIn: true},
		{name: "inherited metadata", file: "metadata.json", body: mainEmbeddedMetadata, wantDBIn: true},
		{name: "metadata without database name", file: "metadata.json", body: `{"backend":"dolt","dolt_mode":"embedded"}`, wantDBIn: true},
		{name: "legacy config.json", file: "config.json", body: `{"backend":"dolt","dolt_mode":"embedded"}`, wantDBIn: true},
		{name: "malformed metadata", file: "metadata.json", body: `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMetadatalessStoreFixture(t, mainEmbeddedMetadata, "myproject")
			writeFixtureFile(t, filepath.Join(f.worktreeBeads, tc.file), tc.body)
			chdirForDiscovery(t, f.worktree)
			var dir, dbPath string
			stderr := captureStderr(t, func() {
				dir = FindBeadsDir()
				dbPath = FindDatabasePath()
				_ = FindBeadsDirFrom(f.subdir)
			})
			assertSamePath(t, "FindBeadsDir()", dir, f.worktreeBeads)
			if tc.wantDBIn {
				assertSamePath(t, "FindDatabasePath()", dbPath, filepath.Join(f.worktreeBeads, "embeddeddolt"))
			}
			if strings.Contains(stderr, "no metadata.json or config.json") {
				t.Errorf("unexpected missing-metadata warning with local %s:\n%s", tc.file, stderr)
			}
			if _, err := os.Stat(filepath.Join(f.worktreeBeads, tc.file)); err != nil {
				t.Errorf("local %s changed by discovery: %v", tc.file, err)
			}
		})
	}
}

// TestFindBeadsDir_ExplicitBeadsDirKeepsMetadatalessWorktreeStore guards
// explicit selection: BEADS_DIR naming the metadata-less local .beads keeps
// it, without the yield warning.
func TestFindBeadsDir_ExplicitBeadsDirKeepsMetadatalessWorktreeStore(t *testing.T) {
	f := newMetadatalessStoreFixture(t, mainEmbeddedMetadata, "myproject")
	chdirForDiscovery(t, f.worktree)
	t.Setenv("BEADS_DIR", f.worktreeBeads)
	var dir, dbPath string
	stderr := captureStderr(t, func() {
		dir = FindBeadsDir()
		dbPath = FindDatabasePath()
	})
	assertSamePath(t, "FindBeadsDir()", dir, f.worktreeBeads)
	assertSamePath(t, "FindDatabasePath()", dbPath, filepath.Join(f.worktreeBeads, "embeddeddolt"))
	if strings.Contains(stderr, "no metadata.json or config.json") {
		t.Errorf("unexpected missing-metadata warning under explicit BEADS_DIR:\n%s", stderr)
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever was written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing stdout pipe writer: %v", err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

// TestFindBeadsDir_WorktreeMetadatalessStoreWarnsOnce asserts the yield is
// reported exactly once per process on stderr, naming both directories, the
// reason, the observed and declared database names and an explicit
// inspection command, and that nothing reaches stdout.
func TestFindBeadsDir_WorktreeMetadatalessStoreWarnsOnce(t *testing.T) {
	f := newMetadatalessStoreFixture(t, mainEmbeddedMetadata, "myproject")
	chdirForDiscovery(t, f.worktree)
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			for i := 0; i < 3; i++ {
				_ = FindBeadsDir()
				_ = FindDatabasePath()
				_ = FindBeadsDirFrom(f.worktree)
				_ = FindBeadsDirFrom(f.subdir)
			}
		})
	})
	if stdout != "" {
		t.Errorf("discovery wrote to stdout:\n%s", stdout)
	}
	if n := strings.Count(stderr, "no metadata.json or config.json"); n != 1 {
		t.Fatalf("missing-metadata warning count = %d, want 1; stderr:\n%s", n, stderr)
	}
	local := utils.CanonicalizePath(f.worktreeBeads)
	for _, want := range []string{
		local,
		utils.CanonicalizePath(f.mainBeads),
		"no metadata.json or config.json",
		`embedded databases observed: beads`,
		`declares dolt_database "myproject"`,
		"BEADS_DIR='" + local + "' bd list",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning missing %q; stderr:\n%s", want, stderr)
		}
	}
}

// TestFindBeadsDir_JJSecondaryMetadatalessStoreYieldsToPrimary mirrors the
// worktree case for a jj secondary workspace: its metadata-less embedded
// store yields to the primary's metadata-backed workspace in all three
// discovery entry points.
func TestFindBeadsDir_JJSecondaryMetadatalessStoreYieldsToPrimary(t *testing.T) {
	clearDiscoveryEnv(t)
	tmpDir := utils.CanonicalizePath(t.TempDir())
	primaryDir := filepath.Join(tmpDir, "primary")
	if err := os.MkdirAll(filepath.Join(primaryDir, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	secondaryDir := filepath.Join(primaryDir, "workspaces", "secondary")
	writeFixtureFile(t, filepath.Join(secondaryDir, ".jj", "repo"), filepath.Join(primaryDir, ".jj", "repo")+"\n")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := gitCommand(args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	runGit(primaryDir, "init")
	runGit(primaryDir, "config", "user.email", "test@example.com")
	runGit(primaryDir, "config", "user.name", "Test User")
	runGit(primaryDir, "config", "core.hooksPath", ".git/hooks")

	primaryBeads := filepath.Join(primaryDir, ".beads")
	writeFixtureFile(t, filepath.Join(primaryBeads, "config.yaml"), "issue-prefix: \"MyProject\"\n")
	writeFixtureFile(t, filepath.Join(primaryBeads, "metadata.json"), mainEmbeddedMetadata)
	writeEmbeddedStore(t, primaryBeads, "myproject")
	writeFixtureFile(t, filepath.Join(primaryDir, "README.md"), "# Test\n")
	runGit(primaryDir, "add", "README.md")
	runGit(primaryDir, "commit", "-m", "initial")

	secondaryBeads := filepath.Join(secondaryDir, ".beads")
	writeFixtureFile(t, filepath.Join(secondaryBeads, "config.yaml"), "issue-prefix: \"MyProject\"\n")
	writeEmbeddedStore(t, secondaryBeads, "beads")
	subdir := filepath.Join(secondaryDir, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, start := range []string{secondaryDir, subdir} {
		chdirForDiscovery(t, start)
		assertSamePath(t, "FindBeadsDir() from "+start, FindBeadsDir(), primaryBeads)
		assertSamePath(t, "FindDatabasePath() from "+start, FindDatabasePath(), filepath.Join(primaryBeads, "embeddeddolt"))
		assertSamePath(t, "FindBeadsDirFrom("+start+")", FindBeadsDirFrom(start), primaryBeads)
	}
}

// TestFindDatabasePath_WorktreeYieldedStoreNotRediscovered covers a main
// checkout whose metadata declares embedded mode but holds no embedded data:
// the yielded worktree store must not come back through lenient acceptance
// or the trailing ancestor walk, and discovery must not touch either side.
func TestFindDatabasePath_WorktreeYieldedStoreNotRediscovered(t *testing.T) {
	f := newMetadatalessStoreFixture(t, mainEmbeddedMetadata, "")
	before := snapshotTree(t, filepath.Dir(f.mainRepo))
	for _, start := range []string{f.worktree, f.subdir} {
		chdirForDiscovery(t, start)
		assertSamePath(t, "FindBeadsDir() from "+start, FindBeadsDir(), f.mainBeads)
		if dbPath := FindDatabasePath(); dbPath != "" && pathWithin(dbPath, f.worktree) {
			t.Errorf("FindDatabasePath() from %s = %q, reselected the yielded worktree store", start, dbPath)
		}
		assertSamePath(t, "FindBeadsDirFrom("+start+")", FindBeadsDirFrom(start), f.mainBeads)
	}
	if after := snapshotTree(t, filepath.Dir(f.mainRepo)); after != before {
		t.Errorf("discovery changed the fixture tree:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestFindDatabasePath_JJSecondaryYieldedStoreNotRediscovered is the jj
// variant of the AC-8 layout: the primary declares embedded mode without
// embedded data.
func TestFindDatabasePath_JJSecondaryYieldedStoreNotRediscovered(t *testing.T) {
	clearDiscoveryEnv(t)
	tmpDir := utils.CanonicalizePath(t.TempDir())
	primaryDir := filepath.Join(tmpDir, "primary")
	if err := os.MkdirAll(filepath.Join(primaryDir, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	secondaryDir := filepath.Join(primaryDir, "workspaces", "secondary")
	writeFixtureFile(t, filepath.Join(secondaryDir, ".jj", "repo"), filepath.Join(primaryDir, ".jj", "repo")+"\n")
	cmd := gitCommand("init")
	cmd.Dir = primaryDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	primaryBeads := filepath.Join(primaryDir, ".beads")
	writeFixtureFile(t, filepath.Join(primaryBeads, "config.yaml"), "issue-prefix: \"MyProject\"\n")
	writeFixtureFile(t, filepath.Join(primaryBeads, "metadata.json"), mainEmbeddedMetadata)
	secondaryBeads := filepath.Join(secondaryDir, ".beads")
	writeFixtureFile(t, filepath.Join(secondaryBeads, "config.yaml"), "issue-prefix: \"MyProject\"\n")
	writeEmbeddedStore(t, secondaryBeads, "beads")

	chdirForDiscovery(t, secondaryDir)
	assertSamePath(t, "FindBeadsDir()", FindBeadsDir(), primaryBeads)
	if dbPath := FindDatabasePath(); dbPath != "" && pathWithin(dbPath, secondaryDir) {
		t.Errorf("FindDatabasePath() = %q, reselected the yielded secondary store", dbPath)
	}
	assertSamePath(t, "FindBeadsDirFrom()", FindBeadsDirFrom(secondaryDir), primaryBeads)
}

// snapshotTree lists every non-.git path under root with its size, so a test
// can assert discovery created, removed or rewrote nothing.
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(&b, "%s %d %v\n", rel, info.Size(), info.IsDir())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestFindBeadsDir_YieldRequiresRecognizedSharedWorkspace guards the other
// side of the yield decision: it fires only beside a shared workspace that
// discovery recognizes by readable metadata. A shared candidate without
// workspace metadata does not displace the metadata-less local store, and a
// standalone repository is never subject to the yield.
func TestFindBeadsDir_YieldRequiresRecognizedSharedWorkspace(t *testing.T) {
	t.Run("shared workspace without metadata does not displace the local store", func(t *testing.T) {
		f := newMetadatalessStoreFixture(t, "", "myproject")
		chdirForDiscovery(t, f.worktree)
		var dir, dbPath string
		stderr := captureStderr(t, func() {
			dir = FindBeadsDir()
			dbPath = FindDatabasePath()
		})
		assertSamePath(t, "FindBeadsDir()", dir, f.worktreeBeads)
		assertSamePath(t, "FindDatabasePath()", dbPath, filepath.Join(f.worktreeBeads, "embeddeddolt"))
		if strings.Contains(stderr, "no metadata.json or config.json") {
			t.Errorf("unexpected missing-metadata warning beside an unrecognized shared candidate:\n%s", stderr)
		}
	})

	t.Run("standalone repository keeps its metadata-less store", func(t *testing.T) {
		clearDiscoveryEnv(t)
		repo := filepath.Join(utils.CanonicalizePath(t.TempDir()), "repo")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init"}, {"config", "core.hooksPath", ".git/hooks"}} {
			cmd := gitCommand(args...)
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		beadsDir := filepath.Join(repo, ".beads")
		writeFixtureFile(t, filepath.Join(beadsDir, "config.yaml"), "issue-prefix: \"R\"\n")
		writeEmbeddedStore(t, beadsDir, "beads")
		chdirForDiscovery(t, repo)
		var dir, dbPath string
		stderr := captureStderr(t, func() {
			dir = FindBeadsDir()
			dbPath = FindDatabasePath()
		})
		assertSamePath(t, "FindBeadsDir()", dir, beadsDir)
		assertSamePath(t, "FindDatabasePath()", dbPath, filepath.Join(beadsDir, "embeddeddolt"))
		if strings.Contains(stderr, "no metadata.json or config.json") {
			t.Errorf("unexpected missing-metadata warning in a standalone repository:\n%s", stderr)
		}
	})
}
