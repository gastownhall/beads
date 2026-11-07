//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
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
	// Role detection reads the process-wide RepoContext cache, which an
	// earlier test in this binary may have built for its own, now-deleted
	// repo. Rebuild it from repoDir.
	resetRepoContextCachesForTest(t)

	got, rule := determineAutoRoutedRepoPath(ctx, sourceStore)
	if got != planningDir {
		t.Fatalf("determineAutoRoutedRepoPath() = %q, want %q", got, planningDir)
	}
	if rule != routing.RuleContributor {
		t.Fatalf("determineAutoRoutedRepoPath() rule = %v, want %v", rule, routing.RuleContributor)
	}
}

func TestDetermineAutoRoutedRepoPath_UsesSelectedBeadsDirRole(t *testing.T) {
	initConfigForTest(t)
	// Viper binds BEADS_ROUTING_MODE / BD_ROUTING_MODE onto the routing.mode
	// key (internal/config), which outranks the routing.mode=auto this test
	// writes into the store. Hosts that export either var (developer shells and
	// agent runtimes do) would otherwise read routing.mode=off and get "." back.
	t.Setenv("BEADS_ROUTING_MODE", "")
	t.Setenv("BD_ROUTING_MODE", "")
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	tmpDir := t.TempDir()
	callerDir := filepath.Join(tmpDir, "caller")
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", callerDir)
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
	if err := os.Chdir(callerDir); err != nil {
		t.Fatalf("chdir callerDir: %v", err)
	}
	t.Setenv("BEADS_DIR", filepath.Join(repoDir, ".beads"))
	// This test simulates a caller who exported BEADS_DIR before bd started;
	// selection provenance is captured at process start, so pin it explicitly.
	setBeadsDirStartupProvenanceForTest(t, true)

	got, _ := determineAutoRoutedRepoPath(ctx, sourceStore)
	if got != planningDir {
		t.Fatalf("determineAutoRoutedRepoPath() = %q, want %q", got, planningDir)
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
	// Role detection reads the process-wide RepoContext cache, which an
	// earlier test in this binary may have built for its own, now-deleted
	// repo. Rebuild it from repoDir.
	resetRepoContextCachesForTest(t)

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
	// Role detection reads the process-wide RepoContext cache, which an
	// earlier test in this binary may have built for its own, now-deleted
	// repo. Rebuild it from repoDir.
	resetRepoContextCachesForTest(t)

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

// TestOpenAutoRoutedStoreMissingPlanningRepoNamesRouting pins the error a
// contributor gets when the planning repository routing selected no longer
// exists (deleted, never cloned to this machine): every routed read and
// write must fail naming the target and how to recover, keeping the
// ErrNoBeadsDatabase identity callers match on, and must not recreate the
// target. A maintainer route to a missing target fails the same way but
// without contributor remediation — the guidance names the rule that
// matched, or it misattributes the failure to beads.role=contributor
// (GH#5957).
func TestOpenAutoRoutedStoreMissingPlanningRepoNamesRouting(t *testing.T) {
	initConfigForTest(t)

	ctx := context.Background()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "contributor")

	sourceStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "src")
	targetStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "plan")
	if err := targetStore.Close(); err != nil {
		t.Fatalf("failed to close planning store: %v", err)
	}

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
	// Role detection reads the process-wide RepoContext cache, which an
	// earlier test in this binary may have built for its own, now-deleted
	// repo. Rebuild it from repoDir.
	resetRepoContextCachesForTest(t)

	// The planning repository is gone: every routed read and write now
	// fails at the open.
	if err := os.RemoveAll(planningDir); err != nil {
		t.Fatalf("RemoveAll(planningDir): %v", err)
	}

	for _, writable := range []bool{false, true} {
		routedStore, routed, rule, err := openAutoRoutedStore(ctx, sourceStore, writable)
		if err == nil {
			t.Fatalf("openAutoRoutedStore(writable=%v) succeeded (routed=%v); want failure naming the missing planning repo", writable, routed)
		}
		if routed {
			t.Fatalf("openAutoRoutedStore(writable=%v) routed = true, want false", writable)
		}
		if routedStore != nil {
			t.Fatalf("openAutoRoutedStore(writable=%v) returned a store, want nil", writable)
		}
		if rule != routing.RuleContributor {
			t.Fatalf("openAutoRoutedStore(writable=%v) rule = %v, want %v", writable, rule, routing.RuleContributor)
		}
		if !errors.Is(err, ErrNoBeadsDatabase) {
			t.Errorf("openAutoRoutedStore(writable=%v) error = %v; want errors.Is ErrNoBeadsDatabase", writable, err)
		}
		msg := err.Error()
		for _, want := range []string{
			planningDir,
			ErrNoBeadsDatabase.Error(),
			"contributor routing selected this planning repository",
			"restore the planning repository to recover",
			"git config beads.role maintainer",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("openAutoRoutedStore(writable=%v) error %q does not name %q", writable, msg, want)
			}
		}
		if _, statErr := os.Stat(planningDir); !os.IsNotExist(statErr) {
			t.Errorf("planning repo at %s was recreated by the failed open (stat err = %v)", planningDir, statErr)
		}
	}

	// Maintainer route to a missing target: same open failure, no
	// contributor remediation.
	maintTmp := t.TempDir()
	maintRepo := filepath.Join(maintTmp, "repo")
	maintTarget := filepath.Join(maintTmp, "planning")

	runCmd(t, maintTmp, "git", "init", maintRepo)
	runCmd(t, maintRepo, "git", "config", "beads.role", "maintainer")

	maintStore := newTestStoreIsolatedDB(t, filepath.Join(maintRepo, ".beads", "beads.db"), "mrm")
	if err := maintStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("failed to set routing.mode: %v", err)
	}
	if err := maintStore.SetConfig(ctx, "routing.maintainer", maintTarget); err != nil {
		t.Fatalf("failed to set routing.maintainer: %v", err)
	}

	if err := os.Chdir(maintRepo); err != nil {
		t.Fatalf("chdir maintRepo: %v", err)
	}
	resetRepoContextCachesForTest(t)

	_, routed, rule, err := openAutoRoutedStore(ctx, maintStore, false)
	if err == nil {
		t.Fatalf("openAutoRoutedStore(maintainer, missing target) succeeded (routed=%v); want failure", routed)
	}
	if rule != routing.RuleMaintainer {
		t.Fatalf("openAutoRoutedStore(maintainer) rule = %v, want %v", rule, routing.RuleMaintainer)
	}
	if !errors.Is(err, ErrNoBeadsDatabase) {
		t.Errorf("maintainer route error = %v; want errors.Is ErrNoBeadsDatabase", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, maintTarget) {
		t.Errorf("maintainer route error %q does not name the missing target %q", msg, maintTarget)
	}
	if strings.Contains(msg, "contributor routing selected") || strings.Contains(msg, "git config beads.role maintainer") {
		t.Errorf("maintainer route error %q carries contributor remediation", msg)
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
