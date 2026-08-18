package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/beads/internal/routing"
	"github.com/jonbaldie/beads/internal/testutil"
	"github.com/spf13/cobra"
)

func TestResolveChangeDirBeadsDirDoesNotChangeCWD(t *testing.T) {
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	startDir := t.TempDir()
	t.Chdir(startDir)

	projectDir := canonicalTestPath(t, t.TempDir())
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := resolveChangeDirBeadsDir(projectDir)
	if err != nil {
		t.Fatalf("resolveChangeDirBeadsDir: %v", err)
	}
	if got != beadsDir {
		t.Fatalf("resolveChangeDirBeadsDir() = %q, want %q", got, beadsDir)
	}

	afterWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd after resolve: %v", err)
	}
	if afterWD != startDir {
		t.Fatalf("working directory changed to %q, want %q", afterWD, startDir)
	}
}

func TestResolveChangeDirBeadsDirRejectsFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := resolveChangeDirBeadsDir(filePath); err == nil {
		t.Fatal("expected non-directory -C target to fail")
	}
}

func TestResolveChangeDirBeadsDirRejectsDirectoryWithoutProject(t *testing.T) {
	if _, err := resolveChangeDirBeadsDir(t.TempDir()); err == nil {
		t.Fatal("expected -C target without a beads project to fail")
	}
}

func resetChangeDirState(t *testing.T) {
	t.Helper()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	oldChangeDir := changeDir
	t.Cleanup(func() {
		changeDir = oldChangeDir
		restoreChangeDirSelection()
		_ = os.Chdir(origWD)
	})
	changeDir = ""
	restoreChangeDirSelection()
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return wd
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

func TestApplyChangeDirSelectionInitAllowsMissingProject(t *testing.T) {
	resetChangeDirState(t)

	startWD := canonicalTestPath(t, mustGetwd(t))

	target := canonicalTestPath(t, t.TempDir())
	t.Setenv("BEADS_DIR", "sentinel")
	if err := os.Unsetenv("BEADS_DIR"); err != nil {
		t.Fatalf("Unsetenv BEADS_DIR: %v", err)
	}

	changeDir = target
	if err := applyChangeDirSelection(initCmd); err != nil {
		t.Fatalf("init -C on a directory without .beads: %v", err)
	}
	wd := canonicalTestPath(t, mustGetwd(t))
	if wd != target {
		t.Fatalf("cwd after init -C = %q, want %q", wd, target)
	}
	if _, ok := os.LookupEnv("BEADS_DIR"); ok {
		t.Fatal("init -C without a beads project must not set BEADS_DIR")
	}

	restoreChangeDirSelection()
	after := canonicalTestPath(t, mustGetwd(t))
	if after != startWD {
		t.Fatalf("cwd after restore = %q, want %q", after, startWD)
	}
}

func TestApplyChangeDirSelectionListRejectsMissingProject(t *testing.T) {
	resetChangeDirState(t)

	startWD := mustGetwd(t)
	changeDir = t.TempDir()
	if err := applyChangeDirSelection(listCmd); err == nil {
		t.Fatal("list -C without a beads project must fail")
	}
	if wd := mustGetwd(t); wd != startWD {
		t.Fatalf("failed list -C changed cwd to %q, want %q", wd, startWD)
	}
}

func TestApplyChangeDirSelectionNotionInitRejectsMissingProject(t *testing.T) {
	resetChangeDirState(t)

	changeDir = t.TempDir()
	if err := applyChangeDirSelection(notionInitCmd); err == nil {
		t.Fatal("notion init -C without a beads project must fail")
	}
}

func TestApplyChangeDirSelectionReadsTargetGitConfig(t *testing.T) {
	resetChangeDirState(t)

	target := canonicalTestPath(t, t.TempDir())
	beadsDir := filepath.Join(target, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = target
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := testutil.ForceRepoLocalHooksPath(target); err != nil {
		t.Fatalf("force repo-local hooks path: %v", err)
	}
	cmd = exec.Command("git", "config", "beads.role", "maintainer")
	cmd.Dir = target
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	changeDir = target
	if err := applyChangeDirSelection(listCmd); err != nil {
		t.Fatalf("list -C: %v", err)
	}
	wd := canonicalTestPath(t, mustGetwd(t))
	if wd != target {
		t.Fatalf("cwd after list -C = %q, want %q", wd, target)
	}
	if got := os.Getenv("BEADS_DIR"); got != beadsDir {
		t.Fatalf("BEADS_DIR = %q, want %q", got, beadsDir)
	}
	role, err := routing.DetectUserRole(".")
	if err != nil {
		t.Fatalf("DetectUserRole: %v", err)
	}
	if role != routing.Maintainer {
		t.Fatalf("DetectUserRole(.) = %q, want maintainer after -C into a repo with beads.role", role)
	}
}

func TestIsPreviewCommand(t *testing.T) {
	tests := []struct {
		name string
		flag string
		set  string
		want bool
	}{
		{name: "dry run", flag: "dry-run", set: "true", want: true},
		{name: "inspect", flag: "inspect", set: "true", want: true},
		{name: "false preview flag", flag: "dry-run", set: "false", want: false},
		{name: "no preview flag", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test"}
			if tt.flag != "" {
				cmd.Flags().Bool(tt.flag, false, "")
				if err := cmd.Flags().Set(tt.flag, tt.set); err != nil {
					t.Fatalf("set %s: %v", tt.flag, err)
				}
			}
			if got := isPreviewCommand(cmd); got != tt.want {
				t.Fatalf("isPreviewCommand() = %v, want %v", got, tt.want)
			}
		})
	}
}
