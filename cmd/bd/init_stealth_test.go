package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// TestSetupGitExclude_Worktree verifies that setupGitExclude writes to the main
// repo's .git/info/exclude, not the worktree's .git/worktrees/<name>/info/exclude.
// This is the fix for GH#1053.
func TestSetupGitExclude_Worktree(t *testing.T) {
	// Create main repo
	mainDir := newGitRepo(t)

	// Create initial commit (required for worktree)
	dummyFile := filepath.Join(mainDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = mainDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create worktree
	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "feature")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// Change to worktree directory and run setupGitExclude
	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatalf("failed to chdir to worktree: %v", err)
	}
	defer os.Chdir(origDir)

	if err := setupGitExclude(false); err != nil {
		t.Fatalf("setupGitExclude failed: %v", err)
	}

	// Verify: main repo's .git/info/exclude should have the patterns
	mainExcludePath := filepath.Join(mainDir, ".git", "info", "exclude")
	content, err := os.ReadFile(mainExcludePath)
	if err != nil {
		t.Fatalf("failed to read main exclude file: %v", err)
	}

	if !strings.Contains(string(content), ".beads/") {
		t.Errorf("main repo exclude missing .beads/ pattern: %s", content)
	}
	if !strings.Contains(string(content), ".claude/settings.local.json") {
		t.Errorf("main repo exclude missing .claude/settings.local.json pattern: %s", content)
	}

	// Verify: worktree's .git/worktrees/<name>/info/exclude should NOT exist
	// (or should not have the patterns if it exists)
	worktreeGitDir, err := exec.Command("git", "-C", worktreeDir, "rev-parse", "--git-dir").Output()
	if err != nil {
		t.Fatalf("failed to get worktree git dir: %v", err)
	}
	worktreeExcludePath := filepath.Join(strings.TrimSpace(string(worktreeGitDir)), "info", "exclude")
	if worktreeContent, err := os.ReadFile(worktreeExcludePath); err == nil {
		// If worktree exclude file exists, it should NOT have the beads patterns
		if strings.Contains(string(worktreeContent), ".beads/") {
			t.Errorf("worktree exclude should not have .beads/ pattern (it was written to wrong location)")
		}
	}
	// If the file doesn't exist, that's fine - we didn't create it
}

// TestSetupForkExclude_Worktree verifies that setupForkExclude writes to the main
// repo's .git/info/exclude, not the worktree's path. This is part of GH#1053.
func TestSetupForkExclude_Worktree(t *testing.T) {
	// Create main repo
	mainDir := newGitRepo(t)

	// Create initial commit (required for worktree)
	dummyFile := filepath.Join(mainDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = mainDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create worktree
	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "feature")
	cmd.Dir = mainDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// Change to worktree directory and run setupForkExclude
	origDir, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatalf("failed to chdir to worktree: %v", err)
	}
	defer os.Chdir(origDir)

	if err := setupForkExclude(false); err != nil {
		t.Fatalf("setupForkExclude failed: %v", err)
	}

	// Verify: main repo's .git/info/exclude should have the patterns
	mainExcludePath := filepath.Join(mainDir, ".git", "info", "exclude")
	content, err := os.ReadFile(mainExcludePath)
	if err != nil {
		t.Fatalf("failed to read main exclude file: %v", err)
	}

	if !strings.Contains(string(content), ".beads/") {
		t.Errorf("main repo exclude missing .beads/ pattern: %s", content)
	}

	// Verify: worktree's .git/worktrees/<name>/info/exclude should NOT exist
	// (or should not have the patterns if it exists)
	worktreeGitDir, err := exec.Command("git", "-C", worktreeDir, "rev-parse", "--git-dir").Output()
	if err != nil {
		t.Fatalf("failed to get worktree git dir: %v", err)
	}
	worktreeExcludePath := filepath.Join(strings.TrimSpace(string(worktreeGitDir)), "info", "exclude")
	if worktreeContent, err := os.ReadFile(worktreeExcludePath); err == nil {
		// If worktree exclude file exists, it should NOT have the beads patterns
		if strings.Contains(string(worktreeContent), ".beads/") {
			t.Errorf("worktree exclude should not have .beads/ pattern (it was written to wrong location)")
		}
	}
}

// TestAddProjectPatternsToGitExclude_DoesNotTouchGitignore is a regression test for stealth mode
// leaking into the tracked project-root .gitignore. In stealth mode bd must route the Dolt-file
// ignore patterns into .git/info/exclude and must NEVER create or modify the project .gitignore
// (which collaborators see). Previously bd init --stealth called doctor.EnsureProjectGitignore
// unconditionally, adding a "# Beads / Dolt files" section to the tracked .gitignore.
func TestAddProjectPatternsToGitExclude_DoesNotTouchGitignore(t *testing.T) {
	dir := newGitRepo(t)

	// Pre-existing project .gitignore unrelated to beads.
	gitignorePath := filepath.Join(dir, ".gitignore")
	originalGitignore := "node_modules/\n"
	if err := os.WriteFile(gitignorePath, []byte(originalGitignore), 0644); err != nil {
		t.Fatalf("failed to seed project .gitignore: %v", err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	if err := addProjectPatternsToGitExclude(dir, doctor.ProjectGitignorePatterns, false); err != nil {
		t.Fatalf("addProjectPatternsToGitExclude failed: %v", err)
	}

	// The project .gitignore must be byte-for-byte unchanged.
	got, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read project .gitignore: %v", err)
	}
	if string(got) != originalGitignore {
		t.Errorf("project .gitignore was modified in stealth mode:\nwant: %q\ngot:  %q", originalGitignore, string(got))
	}

	// The Dolt-file patterns must land in .git/info/exclude instead.
	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	excludeContent, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("failed to read exclude file: %v", err)
	}
	for _, pattern := range doctor.ProjectGitignorePatterns {
		if !containsExactPattern(string(excludeContent), pattern) {
			t.Errorf("exclude file missing pattern %q:\n%s", pattern, excludeContent)
		}
	}
}

// TestAddProjectPatternsToGitExclude_NoGitignoreCreated verifies that stealth mode does not create
// a project-root .gitignore when none exists.
func TestAddProjectPatternsToGitExclude_NoGitignoreCreated(t *testing.T) {
	dir := newGitRepo(t)

	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	if err := addProjectPatternsToGitExclude(dir, doctor.ProjectGitignorePatterns, false); err != nil {
		t.Fatalf("addProjectPatternsToGitExclude failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("stealth mode created a project .gitignore (err=%v); patterns should go to .git/info/exclude only", err)
	}
}

// TestAddProjectPatternsToGitExclude_Idempotent verifies repeated calls do not duplicate patterns
// in the exclude file.
func TestAddProjectPatternsToGitExclude_Idempotent(t *testing.T) {
	dir := newGitRepo(t)

	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	for i := 0; i < 2; i++ {
		if err := addProjectPatternsToGitExclude(dir, doctor.ProjectGitignorePatterns, false); err != nil {
			t.Fatalf("addProjectPatternsToGitExclude call %d failed: %v", i, err)
		}
	}

	excludeContent, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("failed to read exclude file: %v", err)
	}
	for _, pattern := range doctor.ProjectGitignorePatterns {
		if n := strings.Count(string(excludeContent), "\n"+pattern+"\n"); n > 1 {
			t.Errorf("pattern %q duplicated %d times in exclude file:\n%s", pattern, n, excludeContent)
		}
	}
}

// TestIsStealthRepo verifies stealth detection via the persisted no-git-ops flag.
func TestIsStealthRepo(t *testing.T) {
	dir := newGitRepo(t)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	if isStealthRepo(dir) {
		t.Error("expected non-stealth repo before no-git-ops is set")
	}

	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("no-git-ops: true\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	if !isStealthRepo(dir) {
		t.Error("expected stealth repo after no-git-ops: true")
	}
}

// TestCheckProjectExcludeStealth is the doctor-side regression guard: in stealth mode the project
// ignore patterns must be checked against .git/info/exclude, not a tracked .gitignore, so bd doctor
// never re-creates the .gitignore.
func TestCheckProjectExcludeStealth(t *testing.T) {
	dir := newGitRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	// Before patterns are excluded, the stealth check should warn.
	if check := checkProjectExcludeStealth(dir); check.Status != doctor.StatusWarning {
		t.Errorf("expected warning when exclude lacks Dolt patterns, got %q (%s)", check.Status, check.Message)
	}

	// After routing patterns to exclude, the check should pass and no
	// project .gitignore should have been created.
	if err := addProjectPatternsToGitExclude(dir, doctor.ProjectGitignorePatterns, false); err != nil {
		t.Fatalf("addProjectPatternsToGitExclude failed: %v", err)
	}
	if check := checkProjectExcludeStealth(dir); check.Status != doctor.StatusOK {
		t.Errorf("expected OK after patterns added to exclude, got %q (%s)", check.Status, check.Message)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("stealth doctor check must not create a project .gitignore (err=%v)", err)
	}
}

// leakedGitignore returns the content doctor.EnsureProjectGitignore writes when it appends the
// beads section beneath existing user content.
func leakedGitignore(userContent string) string {
	s := userContent
	if len(s) > 0 && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	s += "\n" + doctor.ProjectGitignoreHeader + "\n"
	for _, p := range doctor.ProjectGitignorePatterns {
		s += p + "\n"
	}
	return s
}

// TestRemoveBeadsProjectGitignoreSection_PreservesUserContent verifies that remediation strips only
// the bd-managed section and leaves unrelated user patterns intact.
func TestRemoveBeadsProjectGitignoreSection_PreservesUserContent(t *testing.T) {
	dir := newGitRepo(t)
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(leakedGitignore("node_modules/\n*.log")), 0644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}

	changed, err := removeBeadsProjectGitignoreSection(dir)
	if err != nil {
		t.Fatalf("removeBeadsProjectGitignoreSection failed: %v", err)
	}
	if !changed {
		t.Fatal("expected the beads section to be removed")
	}

	got, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if strings.Contains(string(got), doctor.ProjectGitignoreHeader) {
		t.Errorf("beads header still present:\n%s", got)
	}
	for _, p := range doctor.ProjectGitignorePatterns {
		if containsExactPattern(string(got), p) {
			t.Errorf("beads pattern %q still present:\n%s", p, got)
		}
	}
	for _, want := range []string{"node_modules/", "*.log"} {
		if !containsExactPattern(string(got), want) {
			t.Errorf("user pattern %q was removed:\n%s", want, got)
		}
	}
}

// TestRemoveBeadsProjectGitignoreSection_DeletesWhenOnlyBeads verifies that a .gitignore beads
// created solely for its own section is removed entirely, restoring true stealth.
func TestRemoveBeadsProjectGitignoreSection_DeletesWhenOnlyBeads(t *testing.T) {
	dir := newGitRepo(t)
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(leakedGitignore("")), 0644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}

	changed, err := removeBeadsProjectGitignoreSection(dir)
	if err != nil {
		t.Fatalf("removeBeadsProjectGitignoreSection failed: %v", err)
	}
	if !changed {
		t.Fatal("expected removal")
	}
	if _, err := os.Stat(gitignorePath); !os.IsNotExist(err) {
		t.Errorf("expected .gitignore removed when beads was its only content (err=%v)", err)
	}
}

// TestRemoveBeadsProjectGitignoreSection_NoSection verifies remediation is a no-op (and reports no
// change) for a .gitignore that has no beads section.
func TestRemoveBeadsProjectGitignoreSection_NoSection(t *testing.T) {
	dir := newGitRepo(t)
	gitignorePath := filepath.Join(dir, ".gitignore")
	orig := "node_modules/\n*.log\n"
	if err := os.WriteFile(gitignorePath, []byte(orig), 0644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}

	changed, err := removeBeadsProjectGitignoreSection(dir)
	if err != nil {
		t.Fatalf("removeBeadsProjectGitignoreSection failed: %v", err)
	}
	if changed {
		t.Error("expected no change for a .gitignore without a beads section")
	}
	got, _ := os.ReadFile(gitignorePath)
	if string(got) != orig {
		t.Errorf("user .gitignore modified:\nwant %q\ngot  %q", orig, string(got))
	}

	// Also a no-op (no error) when there is no .gitignore at all.
	if err := os.Remove(gitignorePath); err != nil {
		t.Fatalf("remove .gitignore: %v", err)
	}
	if changed, err := removeBeadsProjectGitignoreSection(dir); err != nil || changed {
		t.Errorf("expected no-op for missing .gitignore, got changed=%v err=%v", changed, err)
	}
}

// TestCheckProjectExcludeStealth_WarnsOnLeakedGitignore verifies the stealth doctor check flags a
// tracked .gitignore that still exposes the beads section, even when .git/info/exclude is correct.
func TestCheckProjectExcludeStealth_WarnsOnLeakedGitignore(t *testing.T) {
	dir := newGitRepo(t)
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	// Patterns are correctly in exclude, so the only remaining problem is the leaked .gitignore.
	if err := addProjectPatternsToGitExclude(dir, doctor.ProjectGitignorePatterns, false); err != nil {
		t.Fatalf("addProjectPatternsToGitExclude failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(leakedGitignore("")), 0644); err != nil {
		t.Fatalf("seed leaked .gitignore: %v", err)
	}

	if check := checkProjectExcludeStealth(dir); check.Status != doctor.StatusWarning {
		t.Errorf("expected warning for leaked tracked .gitignore, got %q (%s)", check.Status, check.Message)
	}

	// After remediation the check should pass.
	if _, err := removeBeadsProjectGitignoreSection(dir); err != nil {
		t.Fatalf("removeBeadsProjectGitignoreSection failed: %v", err)
	}
	if check := checkProjectExcludeStealth(dir); check.Status != doctor.StatusOK {
		t.Errorf("expected OK after remediation, got %q (%s)", check.Status, check.Message)
	}
}

// TestSetupGitExclude_RegularRepo verifies that setupGitExclude still works
// correctly in a regular (non-worktree) repo.
func TestSetupGitExclude_RegularRepo(t *testing.T) {
	dir := newGitRepo(t)

	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origDir)

	if err := setupGitExclude(false); err != nil {
		t.Fatalf("setupGitExclude failed: %v", err)
	}

	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("failed to read exclude file: %v", err)
	}

	if !strings.Contains(string(content), ".beads/") {
		t.Errorf("exclude file missing .beads/ pattern: %s", content)
	}
	if !strings.Contains(string(content), ".claude/settings.local.json") {
		t.Errorf("exclude file missing .claude/settings.local.json pattern: %s", content)
	}
}

func TestAddExcludePatternsPreservesAppendLineEndings(t *testing.T) {
	const lf = "\n# managed\n.beads/\ncache/\n"
	const crlf = "\r\n# managed\r\n.beads/\r\ncache/\r\n"
	for _, tc := range []struct{ name, existing, want, added string }{
		{"empty", "", "# managed\n.beads/\ncache/\n", ".beads/,cache/"},
		{"whitespace unterminated", " \t", " \t\n" + lf, ".beads/,cache/"},
		{"blank LF", "\n", "\n" + lf, ".beads/,cache/"},
		{"blank CRLF", "\r\n", "\r\n" + crlf, ".beads/,cache/"},
		{"blank pending CR", "\r", "\r\n" + lf, ".beads/,cache/"},
		{"delimiter-free", "local", "local\n" + lf, ".beads/,cache/"},
		{"LF", "local\n", "local\n" + lf, ".beads/,cache/"},
		{"CRLF", "local\r\n", "local\r\n" + crlf, ".beads/,cache/"},
		{"CRLF unterminated", "local\r\nlast", "local\r\nlast\r\n" + crlf, ".beads/,cache/"},
		{"CRLF pending CR", "local\r\nlast\r", "local\r\nlast\r\n" + crlf, ".beads/,cache/"},
		{"LF pending CR", "local\nlast\r", "local\nlast\r\n" + lf, ".beads/,cache/"},
		{"only pending CR", "local\r", "local\r\n" + lf, ".beads/,cache/"},
		{"mixed", "a\r\nb\r\nc\n", "a\r\nb\r\nc\n" + lf, ".beads/,cache/"},
		{"partial", ".beads/\r\n", ".beads/\r\n\r\n# managed\r\ncache/\r\n", "cache/"},
		{"complete", ".beads/\r\ncache/", ".beads/\r\ncache/", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newGitRepo(t)
			gitignorePath := filepath.Join(dir, ".gitignore")
			const tracked = "user-rule\r\n"
			if err := os.WriteFile(gitignorePath, []byte(tracked), 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("git", "-C", dir, "add", "--", ".gitignore").CombinedOutput(); err != nil {
				t.Fatalf("track .gitignore: %v: %s", err, out)
			}
			path, err := resolveGitExcludePath(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.existing), 0600); err != nil {
				t.Fatal(err)
			}
			for pass := 0; pass < 2; pass++ {
				added, gotPath, err := addExcludePatterns(dir, "# managed", []string{".beads/", "cache/"})
				if err != nil || gotPath != path {
					t.Fatalf("addExcludePatterns: path=%q, err=%v", gotPath, err)
				}
				wantAdded := tc.added
				if pass == 1 {
					wantAdded = ""
				}
				if strings.Join(added, ",") != wantAdded {
					t.Errorf("pass %d added=%q, want %q", pass, added, wantAdded)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != tc.want {
					t.Fatalf("pass %d exclude=%q, want %q: %v", pass, got, tc.want, err)
				}
				got, err = os.ReadFile(gitignorePath)
				if err != nil || string(got) != tracked {
					t.Fatalf("tracked .gitignore changed: %q: %v", got, err)
				}
			}
		})
	}
}

func TestAddExcludePatternsRefusesReadErrors(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		if os.Getenv("BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION") == "1" {
			t.Fatal("exclude read-error coverage requires an unprivileged POSIX permission boundary")
		}
		t.Skip("write-only permission coverage requires an unprivileged POSIX host")
	}
	dir := newGitRepo(t)
	path, err := resolveGitExcludePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	const before = "user-rule\r\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0600); err != nil {
			t.Errorf("restore exclude mode: %v", err)
		}
	})
	if err := os.Chmod(path, 0200); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read-denied precondition: %v", err)
	}
	// Prove a write would succeed without truncating the bytes being protected.
	writable, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("write-allowed precondition: %v", err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	added, gotPath, err := addExcludePatterns(dir, "# managed", []string{".beads/"})
	if restoreErr := os.Chmod(path, 0600); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err == nil || !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "failed to read git exclude file") {
		t.Errorf("expected contextual wrapped permission error, got %v", err)
	}
	if added != nil || gotPath != path {
		t.Errorf("read failure returned added=%v path=%q, want nil and %q", added, gotPath, path)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != before {
		t.Errorf("exclude bytes after read failure = %q, want %q: %v", got, before, err)
	}
}

func TestAddExcludePatternsCreatesMissingFile(t *testing.T) {
	dir := newGitRepo(t)
	path, err := resolveGitExcludePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	added, gotPath, err := addExcludePatterns(dir, "# managed", []string{".beads/"})
	if err != nil || gotPath != path || len(added) != 1 || added[0] != ".beads/" {
		t.Fatalf("create missing exclude: added=%v path=%q err=%v", added, gotPath, err)
	}
	const want = "# managed\n.beads/\n"
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Errorf("created exclude = %q, want %q: %v", got, want, err)
	}
}

func TestCheckProjectExcludeStealthReadBoundaries(t *testing.T) {
	for _, name := range []string{"directory", "directory_clean", "missing", "dangling_symlink", "patterns", "leak"} {
		t.Run(name, func(t *testing.T) {
			dir := newGitRepo(t)
			excludePath := filepath.Join(dir, ".git", "info", "exclude")
			if err := os.Remove(excludePath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			preservedPath := excludePath
			excludeContent := "user-rule\r\n" + strings.Join(doctor.ProjectGitignorePatterns, "\r\n") + "\r\n"
			if strings.HasPrefix(name, "directory") {
				if err := os.Mkdir(excludePath, 0755); err != nil {
					t.Fatal(err)
				}
				preservedPath = filepath.Join(excludePath, "owned")
			}
			if name == "dangling_symlink" {
				if err := os.Symlink(filepath.Join(dir, "missing-exclude-target"), excludePath); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlink capability unavailable: %v", err)
					}
					t.Fatal(err)
				}
			}
			if name != "missing" && name != "dangling_symlink" {
				if err := os.WriteFile(preservedPath, []byte(excludeContent), 0600); err != nil {
					t.Fatal(err)
				}
			}
			gitignorePath := filepath.Join(dir, ".gitignore")
			gitignoreContent := "user-content\r\n"
			if name == "leak" || name == "directory" {
				gitignoreContent = leakedGitignore(gitignoreContent)
			}
			if err := os.WriteFile(gitignorePath, []byte(gitignoreContent), 0600); err != nil {
				t.Fatal(err)
			}
			want := doctor.DoctorCheck{Name: "Project Gitignore", Status: doctor.StatusWarning}
			switch name {
			case "directory", "directory_clean":
				_, readErr := os.ReadFile(excludePath)
				if readErr == nil || os.IsNotExist(readErr) {
					t.Fatalf("non-ENOENT read-error precondition: %v", readErr)
				}
				want.Message = "Unable to read .git/info/exclude"
				want.Detail = readErr.Error()
				if name == "directory" {
					want.Detail += "; tracked .gitignore also contains the beads section"
				}
			case "missing", "dangling_symlink":
				// Git also treats a dangling exclude symlink as missing; repair advice remains valid.
				want.Message = "Stealth mode: .git/info/exclude missing Dolt exclusion patterns"
				want.Detail = "Missing from .git/info/exclude: " + strings.Join(doctor.ProjectGitignorePatterns, ", ")
				want.Fix = "Run: bd doctor --fix"
			case "patterns":
				want.Status = doctor.StatusOK
				want.Message = "Dolt and credential files excluded via .git/info/exclude (stealth)"
			case "leak":
				want.Message = "Stealth mode: Dolt patterns are exposed in the tracked .gitignore"
				want.Detail = "Tracked .gitignore contains the beads section; bd doctor --fix will move it into .git/info/exclude"
				want.Fix = "Run: bd doctor --fix"
			}
			if got := checkProjectExcludeStealth(dir); got != want {
				t.Errorf("check = %+v, want %+v", got, want)
			}
			if got, err := os.ReadFile(gitignorePath); err != nil || string(got) != gitignoreContent {
				t.Errorf("tracked gitignore changed to %q: %v", got, err)
			}
			if name == "missing" || name == "dangling_symlink" {
				if _, err := os.Stat(excludePath); !os.IsNotExist(err) {
					t.Errorf("diagnostic created missing exclude: %v", err)
				}
			} else if got, err := os.ReadFile(preservedPath); err != nil || string(got) != excludeContent {
				t.Errorf("exclude bytes changed to %q: %v", got, err)
			}
		})
	}
}
