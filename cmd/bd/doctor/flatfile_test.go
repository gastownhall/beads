package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TASKS-v479: git does not track empty directories, so a fresh clone of a
// flatfile repo is missing comments/, events/, memories/ (and issues/ when
// no issues exist yet). NewFlatFileStore auto-creates them on the next store
// open, so doctor must not report the workspace as broken.
func TestCheckFlatfileDirsFreshCloneIsHealthy(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"flatfile","project_id":"test"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	check := checkFlatfileDirs(beadsDir)
	if check.Status != StatusOK {
		t.Errorf("fresh clone without empty subdirs: got status %q (message %q), want %q — "+
			"missing dirs are auto-created on next store open and must not fail doctor",
			check.Status, check.Message, StatusOK)
	}
}

// TASKS-4gnf: outside any git repo there is nothing for flat-file sync to
// work through — the Git Tracking check must not report the false all-clear
// "tracked by git" (git check-ignore exits non-zero both for "not ignored"
// and "not a repo").
func TestCheckFlatfileGitTrackingNoRepoIsNotOK(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "issues"), 0o755); err != nil {
		t.Fatal(err)
	}

	check := checkFlatfileGitTracking(beadsDir)
	if check.Status == StatusOK {
		t.Errorf("no git repo: got StatusOK (%q); want a warning that there is no repo to sync through", check.Message)
	}

	// After git init, an un-ignored issues dir is a clean pass again.
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	check = checkFlatfileGitTracking(beadsDir)
	if check.Status != StatusOK {
		t.Errorf("git repo, not ignored: got status %q (%q); want %q", check.Status, check.Message, StatusOK)
	}
}

// TASKS-awth: when the issues directory cannot be read, the orphan check must
// report the read failure — not compute orphans from an empty ID set, which
// flags every comment and event as orphaned and tells the user to delete them.
func TestCheckFlatfileOrphansUnreadableIssuesDir(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	for _, d := range []string{"comments/TEST-1", "events"} {
		if err := os.MkdirAll(filepath.Join(beadsDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "events", "TEST-1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// "issues" as a regular file makes os.ReadDir fail with ENOTDIR — a read
	// failure that is deterministic even when running as root.
	if err := os.WriteFile(filepath.Join(beadsDir, "issues"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	check := checkFlatfileOrphans(beadsDir)
	if check.Status != StatusWarning {
		t.Errorf("unreadable issues dir: got status %q, want %q", check.Status, StatusWarning)
	}
	if !strings.Contains(check.Message, "Cannot read issues directory") {
		t.Errorf("unreadable issues dir: message %q should report the read failure", check.Message)
	}
	if strings.Contains(check.Message, "orphaned") || check.Detail != "" || check.Fix != "" {
		t.Errorf("unreadable issues dir must not report orphans or a destructive fix: message=%q detail=%q fix=%q",
			check.Message, check.Detail, check.Fix)
	}

	// Same failure must also guard the dangling-deps check's ID set.
	dep := checkFlatfileDanglingDeps(beadsDir)
	if dep.Status != StatusWarning || !strings.Contains(dep.Message, "Cannot read issues directory") {
		t.Errorf("dangling deps with unreadable issues dir: got status %q message %q", dep.Status, dep.Message)
	}
}

// A readable workspace still detects genuine orphans (the fix must not
// suppress real findings).
func TestCheckFlatfileOrphansStillDetectsRealOrphans(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	for _, d := range []string{"issues", "comments/TEST-9", "events"} {
		if err := os.MkdirAll(filepath.Join(beadsDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	check := checkFlatfileOrphans(beadsDir)
	if check.Status != StatusWarning || !strings.Contains(check.Detail, "comments/TEST-9/") {
		t.Errorf("real orphan: got status %q detail %q, want warning naming comments/TEST-9/", check.Status, check.Detail)
	}
}

// All dirs present stays OK.
func TestCheckFlatfileDirsAllPresent(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	for _, d := range []string{"issues", "comments", "events", "memories"} {
		if err := os.MkdirAll(filepath.Join(beadsDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	check := checkFlatfileDirs(beadsDir)
	if check.Status != StatusOK {
		t.Errorf("all dirs present: got status %q, want %q", check.Status, StatusOK)
	}
}
