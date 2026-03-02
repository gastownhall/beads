//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetermineAutoRoutedRepoPath_ContributorToPlanning(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "contributor")

	sourceStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "src")
	ctx := context.Background()

	if err := sourceStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("failed to set routing.mode: %v", err)
	}
	if err := sourceStore.SetConfig(ctx, "routing.contributor", planningDir); err != nil {
		t.Fatalf("failed to set routing.contributor: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir repoDir: %v", err)
	}

	got := determineAutoRoutedRepoPath(ctx, sourceStore)
	if got != planningDir {
		t.Fatalf("determineAutoRoutedRepoPath() = %q, want %q", got, planningDir)
	}
}

func TestOpenRoutedReadStore_ContributorRouting(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "contributor")

	sourceStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "src")
	ctx := context.Background()

	if err := sourceStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("failed to set routing.mode: %v", err)
	}
	if err := sourceStore.SetConfig(ctx, "routing.contributor", planningDir); err != nil {
		t.Fatalf("failed to set routing.contributor: %v", err)
	}

	targetStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "plan")
	if err := targetStore.Close(); err != nil {
		t.Fatalf("failed to close planning store: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir repoDir: %v", err)
	}

	routedStore, routed, err := openRoutedReadStore(ctx, sourceStore)
	if err != nil {
		t.Fatalf("openRoutedReadStore() error = %v", err)
	}
	if !routed {
		t.Fatal("openRoutedReadStore() routed = false, want true")
	}
	defer func() { _ = routedStore.Close() }()

	prefix, err := routedStore.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("failed reading issue_prefix from routed store: %v", err)
	}
	if prefix != "plan" {
		t.Fatalf("routed store prefix = %q, want %q", prefix, "plan")
	}
}

func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, string(output))
	}
}
