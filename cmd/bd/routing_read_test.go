//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/routing"
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

	got, rule := determineAutoRoutedRepoPath(ctx, sourceStore)
	if got != planningDir {
		t.Fatalf("determineAutoRoutedRepoPath() = %q, want %q", got, planningDir)
	}
	if rule != routing.RuleContributor {
		t.Fatalf("determineAutoRoutedRepoPath() rule = %v, want %v", rule, routing.RuleContributor)
	}
}

func TestDetermineAutoRoutedRepoPath_MaintainerToPlanning(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "maintainer")

	sourceStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "srm")
	ctx := context.Background()

	if err := sourceStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("failed to set routing.mode: %v", err)
	}
	if err := sourceStore.SetConfig(ctx, "routing.maintainer", planningDir); err != nil {
		t.Fatalf("failed to set routing.maintainer: %v", err)
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

	got, rule := determineAutoRoutedRepoPath(ctx, sourceStore)
	if got != planningDir {
		t.Fatalf("determineAutoRoutedRepoPath() = %q, want %q", got, planningDir)
	}
	if rule != routing.RuleMaintainer {
		t.Fatalf("determineAutoRoutedRepoPath() rule = %v, want %v", rule, routing.RuleMaintainer)
	}
}

func TestDetermineAutoRoutedRepoPath_DefaultFallback(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "maintainer")

	sourceStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "srd")
	ctx := context.Background()

	// No role-specific repo configured: the unconditional default matches.
	if err := sourceStore.SetConfig(ctx, "routing.default", planningDir); err != nil {
		t.Fatalf("failed to set routing.default: %v", err)
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

	got, rule := determineAutoRoutedRepoPath(ctx, sourceStore)
	if got != planningDir {
		t.Fatalf("determineAutoRoutedRepoPath() = %q, want %q", got, planningDir)
	}
	if rule != routing.RuleDefault {
		t.Fatalf("determineAutoRoutedRepoPath() rule = %v, want %v", rule, routing.RuleDefault)
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

	routedStore, routed, rule, err := openRoutedReadStore(ctx, sourceStore)
	if err != nil {
		t.Fatalf("openRoutedReadStore() error = %v", err)
	}
	if !routed {
		t.Fatal("openRoutedReadStore() routed = false, want true")
	}
	if rule != routing.RuleContributor {
		t.Fatalf("openRoutedReadStore() rule = %v, want %v", rule, routing.RuleContributor)
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
