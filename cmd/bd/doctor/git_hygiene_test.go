package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mkTmpDirInTmp(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		// Fallback for platforms without /tmp (e.g. Windows).
		dir, err = os.MkdirTemp("", prefix)
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func initRepo(t *testing.T, dir string, branch string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Join(dir, ".beads"), 0755)
	runGit(t, dir, "init", "-b", branch)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test User")
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", msg)
}

func TestCheckGitWorkingTree(t *testing.T) {
	t.Run("not a git repo", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-git-nt-*")
		check := CheckGitWorkingTree(dir)
		if check.Status != StatusOK {
			t.Fatalf("status=%q want %q", check.Status, StatusOK)
		}
		if !strings.Contains(check.Message, "N/A") {
			t.Fatalf("message=%q want N/A", check.Message)
		}
	})

	t.Run("clean", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-git-clean-*")
		initRepo(t, dir, "main")
		commitFile(t, dir, "README.md", "# test\n", "initial")

		check := CheckGitWorkingTree(dir)
		if check.Status != StatusOK {
			t.Fatalf("status=%q want %q (msg=%q)", check.Status, StatusOK, check.Message)
		}
	})

	t.Run("dirty", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-git-dirty-*")
		initRepo(t, dir, "main")
		commitFile(t, dir, "README.md", "# test\n", "initial")
		if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0644); err != nil {
			t.Fatalf("write dirty file: %v", err)
		}

		check := CheckGitWorkingTree(dir)
		if check.Status != StatusWarning {
			t.Fatalf("status=%q want %q (msg=%q)", check.Status, StatusWarning, check.Message)
		}
	})
}

func TestCheckGitUpstream(t *testing.T) {
	t.Run("no upstream", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-git-up-*")
		initRepo(t, dir, "main")
		commitFile(t, dir, "README.md", "# test\n", "initial")

		check := CheckGitUpstream(dir)
		if check.Status != StatusWarning {
			t.Fatalf("status=%q want %q (msg=%q)", check.Status, StatusWarning, check.Message)
		}
		if !strings.Contains(check.Message, "No upstream") {
			t.Fatalf("message=%q want to mention upstream", check.Message)
		}
	})

	t.Run("up to date", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-git-up2-*")
		remote := mkTmpDirInTmp(t, "bd-git-remote-*")
		runGit(t, remote, "init", "--bare", "--initial-branch=main")

		initRepo(t, dir, "main")
		commitFile(t, dir, "README.md", "# test\n", "initial")
		runGit(t, dir, "remote", "add", "origin", remote)
		runGit(t, dir, "push", "-u", "origin", "main")

		check := CheckGitUpstream(dir)
		if check.Status != StatusOK {
			t.Fatalf("status=%q want %q (msg=%q)", check.Status, StatusOK, check.Message)
		}
	})

	t.Run("ahead of upstream", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-git-ahead-*")
		remote := mkTmpDirInTmp(t, "bd-git-remote2-*")
		runGit(t, remote, "init", "--bare", "--initial-branch=main")

		initRepo(t, dir, "main")
		commitFile(t, dir, "README.md", "# test\n", "initial")
		runGit(t, dir, "remote", "add", "origin", remote)
		runGit(t, dir, "push", "-u", "origin", "main")

		commitFile(t, dir, "file2.txt", "x", "local commit")

		check := CheckGitUpstream(dir)
		if check.Status != StatusWarning {
			t.Fatalf("status=%q want %q (msg=%q)", check.Status, StatusWarning, check.Message)
		}
		if !strings.Contains(check.Message, "Ahead") {
			t.Fatalf("message=%q want to mention ahead", check.Message)
		}
	})

	t.Run("behind upstream", func(t *testing.T) {
		dir := mkTmpDirInTmp(t, "bd-git-behind-*")
		remote := mkTmpDirInTmp(t, "bd-git-remote3-*")
		runGit(t, remote, "init", "--bare", "--initial-branch=main")

		initRepo(t, dir, "main")
		commitFile(t, dir, "README.md", "# test\n", "initial")
		runGit(t, dir, "remote", "add", "origin", remote)
		runGit(t, dir, "push", "-u", "origin", "main")

		// Advance remote via another clone.
		clone := mkTmpDirInTmp(t, "bd-git-clone-*")
		runGit(t, clone, "clone", remote, ".")
		runGit(t, clone, "config", "user.email", "test@test.com")
		runGit(t, clone, "config", "user.name", "Test User")
		commitFile(t, clone, "remote.txt", "y", "remote commit")
		runGit(t, clone, "push", "origin", "main")

		// Update tracking refs.
		runGit(t, dir, "fetch", "origin")

		check := CheckGitUpstream(dir)
		if check.Status != StatusWarning {
			t.Fatalf("status=%q want %q (msg=%q)", check.Status, StatusWarning, check.Message)
		}
		if !strings.Contains(check.Message, "Behind") {
			t.Fatalf("message=%q want to mention behind", check.Message)
		}
	})
}
