package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func disableGlobalGitIgnore(t *testing.T, repoDir string) {
	t.Helper()

	cmd := exec.Command("git", "config", "core.excludesFile", "")
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
		{"dolt/noms/manifest", true},
		{"issues.jsonl", false},
		{"config.json", false},
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

	add := exec.Command("git", "add", "-f", ".beads/last-touched")
	add.Dir = mainRepoDir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commit := exec.Command("git", "commit", "-m", "Track runtime artifact for test")
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

	add := exec.Command("git", "add", "-f", ".beads/last-touched")
	add.Dir = mainRepoDir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	commit := exec.Command("git", "commit", "-m", "Track runtime artifact for fix test")
	commit.Dir = mainRepoDir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	clearResolveBeadsDirCache()

	if err := FixTrackedRuntimeFiles(worktreeDir); err != nil {
		t.Fatalf("FixTrackedRuntimeFiles() error = %v", err)
	}

	status := exec.Command("git", "status", "--short", "--", ".beads/last-touched")
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
