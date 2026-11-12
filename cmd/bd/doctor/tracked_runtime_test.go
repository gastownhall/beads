package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func disableGlobalGitIgnore(t *testing.T, repoDir string) {
	t.Helper()

	cmd := gitCommand("config", "core.excludesFile", "")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config core.excludesFile failed: %v\n%s", err, out)
	}
}

// TestShouldFlagTrackedFile_RuntimeArtifacts pins the untrack patterns to the
// gitignore template's runtime entries. bd-578h9.6: last_pull was added to
// the gitignore template (7ebf4df6a) but gitignore cannot untrack an
// already-committed file, so clones kept committing and churning it until the
// untrack mechanism knew the name too.
func TestShouldFlagTrackedFile_RuntimeArtifacts(t *testing.T) {
	cases := []struct {
		rel  string
		flag bool
	}{
		{"last_pull", true}, // bd-578h9.6
		{"last-touched", true},
		{"push-state.json", true},
		{"sync-state.json", true},
		{"daemon.pid", true},
		{"bd.lock", true},
		{"dolt-server-config.yaml", true}, // GH#6191: absolute cfg_dir + per-machine port
		{"dolt-server.activity", true},    // GH#6191
		{"dolt-backup.json", true},        // GH#8318: gitignored, but gitignore cannot untrack already-committed copies
		{"dolt-backup-state.json", true},  // GH#8318
		{"dolt-config.log", true},         // GH#8318
		{".auto-import-issues.jsonl", true},
		{"dolt-mode-migration.json", true}, // GH#8318
		{"sub/dolt-config.log", false},     // patterns apply to top-level .beads/ files only
		{"dolt/noms/manifest", true},
		{"issues.jsonl", false},
		{"config.json", false},
		{"config.yaml", false},   // user config, tracked on purpose
		{"sub/last_pull", false}, // patterns apply to top-level .beads/ files only
	}
	for _, tc := range cases {
		if got := shouldFlagTrackedFile(tc.rel); got != tc.flag {
			t.Errorf("shouldFlagTrackedFile(%q) = %v, want %v", tc.rel, got, tc.flag)
		}
	}
}

func TestCheckTrackedRuntimeFiles_WorktreeFallbackUsesSharedBeads(t *testing.T) {
	mainRepoDir, worktreeDir := setupWorktreeRepo(t)
	disableGlobalGitIgnore(t, mainRepoDir)
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatalf("failed to create main .beads dir: %v", err)
	}

	lastTouched := filepath.Join(mainBeadsDir, "last-touched")
	if err := os.WriteFile(lastTouched, []byte("tracked runtime"), 0o644); err != nil {
		t.Fatalf("failed to write last-touched: %v", err)
	}

	add := gitCommand("add", "-f", ".beads/last-touched")
	add.Dir = mainRepoDir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commit := gitCommand("commit", "-m", "Track runtime artifact for test")
	commit.Dir = mainRepoDir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	clearResolveBeadsDirCache()

	check := CheckTrackedRuntimeFiles(worktreeDir)
	if check.Status != "warning" {
		t.Fatalf("expected warning status, got %s: %s", check.Status, check.Detail)
	}
	if check.Detail == "" || !contains(check.Detail, ".beads/last-touched") {
		t.Fatalf("expected tracked runtime detail for shared .beads, got: %s", check.Detail)
	}
}

func TestFixTrackedRuntimeFiles_WorktreeFallbackUsesSharedBeads(t *testing.T) {
	mainRepoDir, worktreeDir := setupWorktreeRepo(t)
	disableGlobalGitIgnore(t, mainRepoDir)
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0o755); err != nil {
		t.Fatalf("failed to create main .beads dir: %v", err)
	}

	lastTouched := filepath.Join(mainBeadsDir, "last-touched")
	if err := os.WriteFile(lastTouched, []byte("tracked runtime"), 0o644); err != nil {
		t.Fatalf("failed to write last-touched: %v", err)
	}

	add := gitCommand("add", "-f", ".beads/last-touched")
	add.Dir = mainRepoDir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commit := gitCommand("commit", "-m", "Track runtime artifact for fix test")
	commit.Dir = mainRepoDir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	clearResolveBeadsDirCache()

	if err := FixTrackedRuntimeFiles(worktreeDir); err != nil {
		t.Fatalf("FixTrackedRuntimeFiles() error = %v", err)
	}

	status := gitCommand("status", "--short", "--", ".beads/last-touched")
	status.Dir = mainRepoDir
	out, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("git status failed: %v\n%s", err, out)
	}
	if !contains(string(out), "D  .beads/last-touched") {
		t.Fatalf("expected staged untrack for shared runtime file, got:\n%s", out)
	}

	if _, err := os.Stat(lastTouched); err != nil {
		t.Fatalf("expected runtime file to remain on disk after git rm --cached: %v", err)
	}
}

// TestFixTrackedRuntimeFiles_UntracksMachineLocalStateFiles verifies the
// GH#8318 end-to-end path: copies of the machine-local state files committed
// before the ignore rule existed are flagged by the check, untracked by the
// fix (git rm --cached keeps them on disk), while issues.jsonl stays tracked.
func TestFixTrackedRuntimeFiles_UntracksMachineLocalStateFiles(t *testing.T) {
	dir := setupGitRepo(t)
	disableGlobalGitIgnore(t, dir)
	beadsDir := filepath.Join(dir, ".beads")

	allNames := append([]string{}, machineLocalStateFiles...)
	allNames = append(allNames, "issues.jsonl")
	for _, name := range allNames {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte("state"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	// Committed before the ignore rule existed: force past .beads/.gitignore.
	add := gitCommand("add", "-f", "--", ".beads")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commit := gitCommand("commit", "-m", "Track machine-local state for test")
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	clearResolveBeadsDirCache()

	check := CheckTrackedRuntimeFiles(dir)
	if check.Status != "warning" {
		t.Fatalf("expected warning status, got %s: %s", check.Status, check.Detail)
	}
	for _, name := range machineLocalStateFiles {
		if !contains(check.Detail, ".beads/"+name) {
			t.Errorf("check detail does not name .beads/%s: %s", name, check.Detail)
		}
	}
	if contains(check.Detail, ".beads/issues.jsonl") {
		t.Errorf("check detail names .beads/issues.jsonl, which must stay tracked: %s", check.Detail)
	}

	if err := FixTrackedRuntimeFiles(dir); err != nil {
		t.Fatalf("FixTrackedRuntimeFiles() error = %v", err)
	}

	ls := gitCommand("ls-files", "--", ".beads")
	ls.Dir = dir
	out, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files failed: %v", err)
	}
	tracked := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, name := range machineLocalStateFiles {
		if contains(string(out), ".beads/"+name) {
			t.Errorf(".beads/%s still tracked: %s", name, tracked)
		}
		path := filepath.Join(beadsDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s removed from disk (git rm --cached must keep it): %v", name, err)
		}
	}
	foundIssues := false
	for _, line := range tracked {
		if strings.TrimSpace(line) == ".beads/issues.jsonl" {
			foundIssues = true
		}
	}
	if !foundIssues {
		t.Errorf(".beads/issues.jsonl is no longer tracked: %v", tracked)
	}
}
