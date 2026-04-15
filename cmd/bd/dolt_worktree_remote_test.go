//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDoltRemoteAddPersistsSyncRemoteToSharedWorktreeConfig(t *testing.T) {
	skipIfNoDolt(t)
	if runtime.GOOS == "windows" {
		t.Skip("Skipping worktree test on Windows")
	}

	bd := buildBDForInitTests(t)
	bareDir, worktreeDir := setupBareParentInitWorktree(t)
	bareBeadsDir := filepath.Join(bareDir, ".beads")
	sharedEnv := append(os.Environ(), "BEADS_DOLT_SHARED_SERVER=1")

	initCmd := exec.Command(bd, "init", "--prefix", "remote-sync", "--skip-hooks", "--quiet")
	initCmd.Dir = worktreeDir
	initCmd.Env = sharedEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init from bare-parent worktree failed: %v\n%s", err, out)
	}

	remoteURL := "git+ssh://git@example.com/acme/beads.git"
	addCmd := exec.Command(bd, "dolt", "remote", "add", "origin", remoteURL)
	addCmd.Dir = worktreeDir
	addCmd.Env = sharedEnv
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd dolt remote add from bare-parent worktree failed: %v\n%s", err, out)
	}

	configPath := filepath.Join(bareBeadsDir, "config.yaml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read shared config.yaml: %v", err)
	}
	if !strings.Contains(string(content), `sync.remote: "`+remoteURL+`"`) {
		t.Fatalf("expected shared config.yaml to contain sync.remote, got:\n%s", string(content))
	}

	if _, err := os.Stat(filepath.Join(worktreeDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("expected no worktree-local .beads directory after remote add, got err=%v", err)
	}
}
