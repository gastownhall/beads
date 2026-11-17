package doctor

import (
	"os"
	"path/filepath"
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
		{"dolt-backup.json", true},          // GH#8318: absolute backup destination path
		{"dolt-backup-state.json", true},    // GH#8318: per-machine last backup sync state
		{"dolt-config.log", true},           // GH#8318: bd dolt set audit log records the absolute .beads path
		{".auto-import-issues.jsonl", true}, // GH#8318: size/mtime stamp of this clone's issues.jsonl
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

// TestCheckTrackedRuntimeFiles_FlagsCommittedBackupConfig covers AC-3
// (bd-g6fd4y, GH#8318): a committed .beads/dolt-backup.json is reported as a
// warning (not an error — it is not a credential), and FixTrackedRuntimeFiles
// untracks it while leaving the file on disk.
func TestCheckTrackedRuntimeFiles_FlagsCommittedBackupConfig(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := gitCommand(args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	// A host-level global excludes file could hide the committed copy from
	// git status below, so point core.excludesFile at nothing.
	disableGlobalGitIgnore(t, dir)

	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	backupFile := filepath.Join(beadsDir, "dolt-backup.json")
	if err := os.WriteFile(backupFile, []byte(`{"url":"file:///tmp/backup-dest"}`), 0o644); err != nil {
		t.Fatalf("failed to write dolt-backup.json: %v", err)
	}
	run("add", "-f", ".beads/dolt-backup.json")
	run("commit", "-m", "Track backup config for test")

	clearResolveBeadsDirCache()

	check := CheckTrackedRuntimeFiles(dir)
	if check.Status != "warning" {
		t.Fatalf("expected warning status, got %s: %s", check.Status, check.Detail)
	}
	if !contains(check.Detail, ".beads/dolt-backup.json") {
		t.Fatalf("expected detail to name .beads/dolt-backup.json, got: %s", check.Detail)
	}

	if err := FixTrackedRuntimeFiles(dir); err != nil {
		t.Fatalf("FixTrackedRuntimeFiles failed: %v", err)
	}

	status := gitCommand("status", "--short", "--", ".beads/dolt-backup.json")
	status.Dir = dir
	out, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("git status failed: %v\n%s", err, out)
	}
	if !contains(string(out), "D  .beads/dolt-backup.json") {
		t.Fatalf("expected staged untrack of .beads/dolt-backup.json, got:\n%s", out)
	}

	if _, err := os.Stat(backupFile); err != nil {
		t.Fatalf("expected dolt-backup.json to remain on disk after git rm --cached: %v", err)
	}
}
