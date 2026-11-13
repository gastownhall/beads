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
		{"dolt-server-config.yaml", true},   // GH#6191: absolute cfg_dir + per-machine port
		{"dolt-server.activity", true},      // GH#6191
		{"dolt-backup.json", true},          // GH#8318: absolute backup destination
		{"dolt-backup-state.json", true},    // GH#8318
		{"dolt-config.log", true},           // GH#8318: beads_dir=<absolute path>
		{".auto-import-issues.jsonl", true}, // GH#8318
		{"dolt-mode-migration.json", true},  // GH#8318: migrate resume journal, absolute paths
		{"dolt/noms/manifest", true},
		{"issues.jsonl", false},
		{"config.json", false},
		{"config.yaml", false},   // user config, tracked on purpose
		{"sub/last_pull", false}, // patterns apply to top-level .beads/ files only
		{"sub/dolt-config.log", false},
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

// TestFixTrackedRuntimeFiles_UntracksMachineLocalStateFiles checks that
// bd doctor flags machine-local state files committed before GH#8318 and that
// --fix untracks them while keeping them on disk, leaving issues.jsonl
// tracked.
func TestFixTrackedRuntimeFiles_UntracksMachineLocalStateFiles(t *testing.T) {
	dir := setupGitRepo(t)
	disableGlobalGitIgnore(t, dir)
	beadsDir := filepath.Join(dir, ".beads")

	files := append(append([]string{}, machineLocalStateFiles...), "issues.jsonl")
	add := []string{"add", "-f", "--"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		add = append(add, ".beads/"+name)
	}
	for _, args := range [][]string{add, {"commit", "-m", "Track machine-local state for test"}} {
		cmd := gitCommand(args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	clearResolveBeadsDirCache()

	check := CheckTrackedRuntimeFiles(dir)
	if check.Status != StatusWarning {
		t.Fatalf("CheckTrackedRuntimeFiles status = %q, want %q (detail: %s)", check.Status, StatusWarning, check.Detail)
	}
	for _, name := range machineLocalStateFiles {
		if !strings.Contains(check.Detail, ".beads/"+name) {
			t.Errorf("check detail does not name .beads/%s: %s", name, check.Detail)
		}
	}
	for _, flagged := range strings.Split(check.Detail, ", ") {
		if flagged == ".beads/issues.jsonl" {
			t.Errorf("check detail flags .beads/issues.jsonl: %s", check.Detail)
		}
	}

	if err := FixTrackedRuntimeFiles(dir); err != nil {
		t.Fatalf("FixTrackedRuntimeFiles: %v", err)
	}

	ls := gitCommand("ls-files", ".beads")
	ls.Dir = dir
	out, err := ls.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != ".beads/issues.jsonl" {
		t.Errorf("tracked under .beads after fix = %q, want only .beads/issues.jsonl", got)
	}
	for _, name := range files {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); err != nil {
			t.Errorf(".beads/%s should stay on disk after the untrack: %v", name, err)
		}
	}
}
