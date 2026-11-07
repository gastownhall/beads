//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
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

func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, string(output))
	}
}

// missingPlanningRouteFixture points auto-routing at a planning repo that was
// never created (or was deleted) for the given role, and returns the routing
// settings store and the missing repo path.
func missingPlanningRouteFixture(t *testing.T, role, key string) (*routingSettingsMapStore, string) {
	t.Helper()
	initConfigForTest(t)
	t.Setenv("BEADS_ROUTING_MODE", "")
	t.Setenv("BD_ROUTING_MODE", "")
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")
	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", role)
	t.Chdir(repoDir)
	resetRepoContextCachesForTest(t)
	values := map[string]string{key: planningDir}
	if key != "routing.default" {
		values["routing.mode"] = "auto"
	}
	return &routingSettingsMapStore{values: values}, planningDir
}

// routingSettingsMapStore answers the settings read routing makes. The
// embedded DoltStorage is nil: any other call panics.
type routingSettingsMapStore struct {
	storage.DoltStorage
	values map[string]string
}

func (s *routingSettingsMapStore) GetAllConfig(context.Context) (map[string]string, error) {
	return s.values, nil
}

// checkMissingPlanningRepoError asserts what a routed open of a missing
// planning repo reports and that it created nothing there (GH#5957).
func checkMissingPlanningRepoError(t *testing.T, writable bool) {
	t.Helper()
	store, planningDir := missingPlanningRouteFixture(t, "contributor", "routing.contributor")
	open := openRoutedReadStore
	if writable {
		open = func(ctx context.Context, s storage.DoltStorage) (storage.DoltStorage, bool, routing.RoutingRule, error) {
			return openAutoRoutedStore(ctx, s, true)
		}
	}
	routedStore, routed, rule, err := open(t.Context(), store)
	if err == nil {
		_ = routedStore.Close()
		t.Fatalf("open of a missing planning repo succeeded (routed=%v)", routed)
	}
	if rule != routing.RuleContributor {
		t.Errorf("rule = %v, want %v", rule, routing.RuleContributor)
	}
	if !errors.Is(err, ErrNoBeadsDatabase) {
		t.Errorf("error lost ErrNoBeadsDatabase: %v", err)
	}
	for _, want := range []string{
		planningDir,
		"contributor routing",
		"if it was deleted, restore the planning repo to recover its issues",
		"does not restore them",
		"to disable contributor-role routing: git config beads.role maintainer",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(planningDir); !os.IsNotExist(statErr) {
		t.Errorf("routed open created the missing planning repo: %v", statErr)
	}
}

// A routed read (bd list/ready) of a missing planning repo fails the same way
// instead of reading as an empty result.
func TestOpenRoutedReadStoreMissingPlanningRepoNamesRouting(t *testing.T) {
	checkMissingPlanningRepoError(t, false)
}

func TestOpenAutoRoutedStoreMissingPlanningRepoNamesRouting(t *testing.T) {
	t.Run("contributor", func(t *testing.T) {
		checkMissingPlanningRepoError(t, true)
	})
	// Other rules keep their plain error: their target was chosen by
	// routing.maintainer / routing.default, not by contributor mode.
	for _, tc := range []struct{ role, key string }{
		{"maintainer", "routing.maintainer"},
		{"maintainer", "routing.default"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			store, planningDir := missingPlanningRouteFixture(t, tc.role, tc.key)
			_, _, _, err := openAutoRoutedStore(t.Context(), store, true)
			if !errors.Is(err, ErrNoBeadsDatabase) || !strings.Contains(err.Error(), planningDir) {
				t.Fatalf("error = %v, want ErrNoBeadsDatabase naming %s", err, planningDir)
			}
			if strings.Contains(err.Error(), "contributor") {
				t.Errorf("%s route misattributed to contributor routing: %v", tc.key, err)
			}
		})
	}
	// A planning repo that is present but fails to open keeps its own cause
	// and the routing explanation, without the restore or not-initialized
	// advice.
	t.Run("contributor_unreadable_metadata", func(t *testing.T) {
		store, planningDir := missingPlanningRouteFixture(t, "contributor", "routing.contributor")
		beadsDir := filepath.Join(planningDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := openAutoRoutedStore(t.Context(), store, true)
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("error lost the metadata parse failure: %v", err)
		}
		if !strings.Contains(err.Error(), "git config beads.role maintainer") || strings.Contains(err.Error(), "restore the planning repo") || strings.Contains(err.Error(), "not initialized") {
			t.Errorf("error = %v, want the routing opt-out without the restore or not-initialized advice", err)
		}
	})
	// bd init --contributor leaves the planning repo's .beads empty until the
	// first routed create. That store is not initialized yet, not lost:
	// telling the user to restore it would report a loss that never happened.
	t.Run("contributor_uninitialized_planning_repo", func(t *testing.T) {
		for _, writable := range []bool{false, true} {
			t.Run(map[bool]string{false: "read", true: "write"}[writable], func(t *testing.T) {
				store, planningDir := missingPlanningRouteFixture(t, "contributor", "routing.contributor")
				beadsDir := filepath.Join(planningDir, ".beads")
				if err := os.MkdirAll(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
				_, _, _, err := openAutoRoutedStore(t.Context(), store, writable)
				if !errors.Is(err, ErrNoBeadsDatabase) {
					t.Fatalf("error = %v, want ErrNoBeadsDatabase", err)
				}
				if !strings.Contains(err.Error(), "its store is not initialized yet (the first bd create routed there initializes it)") || !strings.Contains(err.Error(), "git config beads.role maintainer") {
					t.Errorf("error = %v, want the not-initialized advice and the routing opt-out", err)
				}
				if strings.Contains(err.Error(), "restore the planning repo") || strings.Contains(err.Error(), "recover its issues") {
					t.Errorf("error = %v, reports a never-initialized planning store as lost", err)
				}
				if entries, readErr := os.ReadDir(beadsDir); readErr != nil || len(entries) != 0 {
					t.Errorf("routed open wrote into the uninitialized planning store: %v, %v", entries, readErr)
				}
			})
		}
	})
	// The restore advice keys on the planning repo's .beads, not the repo:
	// with the repo there and its .beads gone, the store may still have been
	// deleted.
	t.Run("contributor_planning_repo_without_beads", func(t *testing.T) {
		store, planningDir := missingPlanningRouteFixture(t, "contributor", "routing.contributor")
		if err := os.MkdirAll(planningDir, 0750); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := openAutoRoutedStore(t.Context(), store, true)
		if !errors.Is(err, ErrNoBeadsDatabase) || !strings.Contains(err.Error(), "if it was deleted, restore the planning repo to recover its issues") || strings.Contains(err.Error(), "not initialized") {
			t.Errorf("error = %v, want ErrNoBeadsDatabase with the restore advice", err)
		}
		if _, statErr := os.Stat(filepath.Join(planningDir, ".beads")); !os.IsNotExist(statErr) {
			t.Errorf("routed open created the planning repo's .beads: %v", statErr)
		}
	})
	// A .beads holding files the open does not take for a store (here a
	// SQLite-era config.json, which a routed create refuses to initialize)
	// is neither lost nor waiting for its first create: no advice.
	t.Run("contributor_legacy_planning_store", func(t *testing.T) {
		store, planningDir := missingPlanningRouteFixture(t, "contributor", "routing.contributor")
		beadsDir := filepath.Join(planningDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "config.json"), []byte(`{"database": "beads.db"}`), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := openAutoRoutedStore(t.Context(), store, true)
		if err == nil {
			t.Fatal("open of a legacy planning store succeeded")
		}
		if !strings.Contains(err.Error(), "git config beads.role maintainer") || strings.Contains(err.Error(), "restore the planning repo") || strings.Contains(err.Error(), "not initialized") {
			t.Errorf("error = %v, want the routing opt-out without the restore or not-initialized advice", err)
		}
	})
}
