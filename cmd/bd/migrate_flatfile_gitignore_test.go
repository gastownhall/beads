package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFlatfileMigrationGitignoreKeepsDoltIgnored reproduces TASKS-raum: after
// 'bd migrate flatfile' the Dolt database directories are still on disk (the
// success message only optionally suggests removing them), and the flat-file
// auto-commit runs 'git status --porcelain .beads/' + 'git add .beads/'
// (gitops.go CommitPending) after every mutating command. The gitignore the
// migration writes must therefore keep ignoring the Dolt data and runtime
// files, or the first post-migration commit bakes the whole Dolt binary
// database into git history.
//
// Oracle: git itself — the same 'git status --porcelain .beads/' invocation
// auto-commit uses decides what would be staged.
func TestFlatfileMigrationGitignoreKeepsDoltIgnored(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")

	beadsDir := filepath.Join(dir, ".beads")

	// Leftover Dolt state exactly as a pre-migration workspace has it:
	// binary database directories plus runtime files.
	doltFiles := []string{
		filepath.Join("dolt", "beads", ".dolt", "noms", "manifest"),
		filepath.Join("embeddeddolt", "beads", ".dolt", "noms", "manifest"),
		filepath.Join("proxieddb", "data.bin"),
		"daemon.lock",
		"dolt-server.pid",
		"dolt-server.log",
		"bd.sock.startlock",
	}
	for _, f := range doltFiles {
		path := filepath.Join(beadsDir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("dolt-binary-data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Flat-file data the migration produced, which MUST be committed.
	flatfileFiles := []string{
		filepath.Join("issues", "x-1.json"),
		filepath.Join("comments", "x-1.jsonl"),
		"config_kv.json",
		"metadata.json",
	}
	for _, f := range flatfileFiles {
		path := filepath.Join(beadsDir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The gitignore the forward migration writes.
	if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"),
		[]byte(flatfileMigrationGitignore), 0o644); err != nil {
		t.Fatal(err)
	}

	status := git("status", "--porcelain", ".beads/")

	for _, f := range doltFiles {
		if strings.Contains(status, filepath.ToSlash(f)) {
			t.Errorf("dolt leftover %q would be staged by auto-commit:\n%s", f, status)
		}
	}
	// Porcelain collapses untracked dirs to "?? .beads/" when nothing inside
	// is ignored-away; assert the flat-file payload is still visible to git.
	for _, f := range flatfileFiles {
		if !strings.Contains(status, filepath.ToSlash(f)) && !strings.Contains(status, ".beads/") {
			t.Errorf("flat-file data %q invisible to git status:\n%s", f, status)
		}
	}
	// Stage and verify: after 'git add .beads/' (the auto-commit staging
	// step) no dolt content is in the index, and the flat-file data is.
	git("add", ".beads/")
	staged := git("diff", "--cached", "--name-only")
	for _, f := range doltFiles {
		if strings.Contains(staged, filepath.ToSlash(f)) {
			t.Errorf("dolt leftover %q staged into the index:\n%s", f, staged)
		}
	}
	for _, f := range flatfileFiles {
		if !strings.Contains(staged, filepath.ToSlash(f)) {
			t.Errorf("flat-file data %q not staged:\n%s", f, staged)
		}
	}
}
