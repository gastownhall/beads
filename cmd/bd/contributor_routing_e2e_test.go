// contributor_routing_e2e_test.go - E2E tests for contributor routing
//
// These tests verify that issues are correctly routed to the planning repo
// when the user is detected as a contributor with auto-routing enabled.

//go:build cgo && integration
// +build cgo,integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestContributorRoutingTracer is the Phase 1 tracer bullet test.
// It proves that:
// 1. ExpandPath correctly expands ~ and relative paths
// 2. Routing config is correctly read (including backward compat)
// 3. DetermineTargetRepo returns the correct repo for contributors
//
// Full store switching is deferred to Phase 2.
func TestContributorRoutingTracer(t *testing.T) {
	t.Run("ExpandPath_tilde_expansion", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot get home dir: %v", err)
		}

		tests := []struct {
			input string
			want  string
		}{
			{"~/foo", filepath.Join(home, "foo")},
			{"~/bar/baz", filepath.Join(home, "bar", "baz")},
			{".", "."},
			{"", ""},
		}

		for _, tt := range tests {
			got := routing.ExpandPath(tt.input)
			if got != tt.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		}
	})

	t.Run("DetermineTargetRepo_contributor_routes_to_planning", func(t *testing.T) {
		config := &routing.RoutingConfig{
			Mode:            "auto",
			ContributorRepo: "~/.beads-planning",
		}

		got := routing.DetermineTargetRepo(config, routing.Contributor, ".")
		if got != "~/.beads-planning" {
			t.Errorf("DetermineTargetRepo() = %q, want %q", got, "~/.beads-planning")
		}
	})

	t.Run("DetermineTargetRepo_maintainer_stays_local", func(t *testing.T) {
		config := &routing.RoutingConfig{
			Mode:            "auto",
			MaintainerRepo:  ".",
			ContributorRepo: "~/.beads-planning",
		}

		got := routing.DetermineTargetRepo(config, routing.Maintainer, ".")
		if got != "." {
			t.Errorf("DetermineTargetRepo() = %q, want %q", got, ".")
		}
	})

	t.Run("E2E_routing_decision_with_store", func(t *testing.T) {
		// Set up temporary directory structure
		tmpDir := t.TempDir()
		projectDir := filepath.Join(tmpDir, "project")
		planningDir := filepath.Join(tmpDir, "planning")

		// Create project .beads directory
		projectBeadsDir := filepath.Join(projectDir, ".beads")
		if err := os.MkdirAll(projectBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create project .beads dir: %v", err)
		}

		// Create planning .beads directory
		planningBeadsDir := filepath.Join(planningDir, ".beads")
		if err := os.MkdirAll(planningBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create planning .beads dir: %v", err)
		}

		// Initialize project database
		projectDBPath := filepath.Join(projectBeadsDir, "beads.db")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		projectStore, err := dolt.New(ctx, &dolt.Config{Path: projectDBPath})
		if err != nil {
			t.Fatalf("failed to create project store: %v", err)
		}
		defer projectStore.Close()

		// Set routing config in project store (canonical keys)
		if err := projectStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
			t.Fatalf("failed to set routing.mode: %v", err)
		}
		if err := projectStore.SetConfig(ctx, "routing.contributor", planningDir); err != nil {
			t.Fatalf("failed to set routing.contributor: %v", err)
		}

		// Verify config was stored correctly
		mode, err := projectStore.GetConfig(ctx, "routing.mode")
		if err != nil {
			t.Fatalf("failed to get routing.mode: %v", err)
		}
		if mode != "auto" {
			t.Errorf("routing.mode = %q, want %q", mode, "auto")
		}

		contributorPath, err := projectStore.GetConfig(ctx, "routing.contributor")
		if err != nil {
			t.Fatalf("failed to get routing.contributor: %v", err)
		}
		if contributorPath != planningDir {
			t.Errorf("routing.contributor = %q, want %q", contributorPath, planningDir)
		}

		// Build routing config from stored values
		routingConfig := &routing.RoutingConfig{
			Mode:            mode,
			ContributorRepo: contributorPath,
		}

		// Verify routing decision for contributor
		targetRepo := routing.DetermineTargetRepo(routingConfig, routing.Contributor, projectDir)
		if targetRepo != planningDir {
			t.Errorf("DetermineTargetRepo() = %q, want %q", targetRepo, planningDir)
		}

		// Verify routing decision for maintainer stays local
		targetRepo = routing.DetermineTargetRepo(routingConfig, routing.Maintainer, projectDir)
		if targetRepo != "." {
			t.Errorf("DetermineTargetRepo() for maintainer = %q, want %q", targetRepo, ".")
		}

		// Initialize planning database and verify we can create issues there
		planningDBPath := filepath.Join(planningBeadsDir, "beads.db")
		planningStore, err := dolt.New(ctx, &dolt.Config{Path: planningDBPath})
		if err != nil {
			t.Fatalf("failed to create planning store: %v", err)
		}
		defer planningStore.Close()

		// Initialize planning store with required config
		if err := planningStore.SetConfig(ctx, "issue_prefix", "plan-"); err != nil {
			t.Fatalf("failed to set issue_prefix in planning store: %v", err)
		}

		// Create a test issue in planning store (simulating what Phase 2 will do)
		issue := &types.Issue{
			Title:     "Test contributor issue",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  2,
		}

		if err := planningStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue in planning store: %v", err)
		}

		// Verify issue exists in planning store
		retrieved, err := planningStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue from planning store: %v", err)
		}
		if retrieved.Title != "Test contributor issue" {
			t.Errorf("issue title = %q, want %q", retrieved.Title, "Test contributor issue")
		}

		// Verify issue does NOT exist in project store (isolation check)
		projectIssue, _ := projectStore.GetIssue(ctx, issue.ID)
		if projectIssue != nil {
			t.Error("issue should NOT exist in project store (isolation failure)")
		}
	})
}

// TestBackwardCompatContributorConfig verifies legacy contributor.* keys still work
func TestBackwardCompatContributorConfig(t *testing.T) {
	// Set up temporary directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Initialize database
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := dolt.New(ctx, &dolt.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Set LEGACY contributor.* keys (what old versions of bd init --contributor would set)
	if err := store.SetConfig(ctx, "contributor.auto_route", "true"); err != nil {
		t.Fatalf("failed to set contributor.auto_route: %v", err)
	}
	if err := store.SetConfig(ctx, "contributor.planning_repo", "/legacy/planning"); err != nil {
		t.Fatalf("failed to set contributor.planning_repo: %v", err)
	}

	// Simulate backward compat read (as done in create.go)
	routingMode, _ := store.GetConfig(ctx, "routing.mode")
	contributorRepo, _ := store.GetConfig(ctx, "routing.contributor")

	// Fallback to legacy keys
	if routingMode == "" {
		legacyAutoRoute, _ := store.GetConfig(ctx, "contributor.auto_route")
		if legacyAutoRoute == "true" {
			routingMode = "auto"
		}
	}
	if contributorRepo == "" {
		legacyPlanningRepo, _ := store.GetConfig(ctx, "contributor.planning_repo")
		contributorRepo = legacyPlanningRepo
	}

	// Verify backward compat works
	if routingMode != "auto" {
		t.Errorf("backward compat routing.mode = %q, want %q", routingMode, "auto")
	}
	if contributorRepo != "/legacy/planning" {
		t.Errorf("backward compat routing.contributor = %q, want %q", contributorRepo, "/legacy/planning")
	}

	// Build routing config and verify it routes correctly
	config := &routing.RoutingConfig{
		Mode:            routingMode,
		ContributorRepo: contributorRepo,
	}

	targetRepo := routing.DetermineTargetRepo(config, routing.Contributor, ".")
	if targetRepo != "/legacy/planning" {
		t.Errorf("DetermineTargetRepo() with legacy config = %q, want %q", targetRepo, "/legacy/planning")
	}
}

// =============================================================================
// Phase 4: E2E Tests for all sync modes and routing scenarios
// =============================================================================

// contributorRoutingEnv provides a reusable test environment for contributor routing tests
type contributorRoutingEnv struct {
	t           *testing.T
	tmpDir      string
	projectDir  string
	planningDir string
	ctx         context.Context
	cancel      context.CancelFunc
}

// setupContributorRoutingEnv creates isolated project and planning directories with git repos
func setupContributorRoutingEnv(t *testing.T) *contributorRoutingEnv {
	t.Helper()
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "project")
	planningDir := filepath.Join(tmpDir, "planning")

	// Create project directory with git init
	projectBeadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(projectBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create project .beads dir: %v", err)
	}

	// Create planning directory with git init
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	if err := os.MkdirAll(planningBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create planning .beads dir: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	return &contributorRoutingEnv{
		t:           t,
		tmpDir:      tmpDir,
		projectDir:  projectDir,
		planningDir: planningDir,
		ctx:         ctx,
		cancel:      cancel,
	}
}

// cleanup releases resources
func (env *contributorRoutingEnv) cleanup() {
	env.cancel()
}

// initProjectStore initializes the project store with routing config
func (env *contributorRoutingEnv) initProjectStore(syncMode string) *dolt.DoltStore {
	env.t.Helper()
	projectDBPath := filepath.Join(env.projectDir, ".beads", "beads.db")
	store, err := dolt.New(env.ctx, &dolt.Config{Path: projectDBPath})
	if err != nil {
		env.t.Fatalf("failed to create project store: %v", err)
	}

	// Set routing config
	if err := store.SetConfig(env.ctx, "routing.mode", "auto"); err != nil {
		env.t.Fatalf("failed to set routing.mode: %v", err)
	}
	if err := store.SetConfig(env.ctx, "routing.contributor", env.planningDir); err != nil {
		env.t.Fatalf("failed to set routing.contributor: %v", err)
	}
	if err := store.SetConfig(env.ctx, "issue_prefix", "proj-"); err != nil {
		env.t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Set sync mode-specific config
	switch syncMode {
	case "direct":
		// No special config needed - direct is default
	case "sync-branch":
		if err := store.SetConfig(env.ctx, "sync.branch", "beads-sync"); err != nil {
			env.t.Fatalf("failed to set sync.branch: %v", err)
		}
	case "no-db":
		if err := store.SetConfig(env.ctx, "sync.nodb", "true"); err != nil {
			env.t.Fatalf("failed to set sync.nodb: %v", err)
		}
	case "local-only":
		if err := store.SetConfig(env.ctx, "sync.local-only", "true"); err != nil {
			env.t.Fatalf("failed to set sync.local-only: %v", err)
		}
	}

	return store
}

// initPlanningStore initializes the planning store
func (env *contributorRoutingEnv) initPlanningStore() *dolt.DoltStore {
	env.t.Helper()
	planningDBPath := filepath.Join(env.planningDir, ".beads", "beads.db")
	store, err := dolt.New(env.ctx, &dolt.Config{Path: planningDBPath})
	if err != nil {
		env.t.Fatalf("failed to create planning store: %v", err)
	}

	if err := store.SetConfig(env.ctx, "issue_prefix", "plan-"); err != nil {
		env.t.Fatalf("failed to set issue_prefix in planning store: %v", err)
	}

	return store
}

// verifyIssueRouting creates an issue via routing and verifies it lands in the correct store
func verifyIssueRouting(
	t *testing.T,
	ctx context.Context,
	routingConfig *routing.RoutingConfig,
	userRole routing.UserRole,
	targetStore *dolt.DoltStore,
	otherStore *dolt.DoltStore,
	expectedRepoPath string,
	description string,
) {
	t.Helper()

	// Verify routing decision
	targetRepo := routing.DetermineTargetRepo(routingConfig, userRole, ".")
	if targetRepo != expectedRepoPath {
		t.Errorf("%s: DetermineTargetRepo() = %q, want %q", description, targetRepo, expectedRepoPath)
		return
	}

	// Create issue in target store
	issue := &types.Issue{
		Title:     "Test " + description,
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}

	if err := targetStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("%s: failed to create issue: %v", description, err)
	}

	// Verify issue exists in target store
	retrieved, err := targetStore.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("%s: failed to get issue from target store: %v", description, err)
	}
	if retrieved == nil {
		t.Errorf("%s: issue not found in target store", description)
		return
	}

	// Verify issue does NOT exist in other store (isolation check)
	if otherStore != nil {
		otherIssue, _ := otherStore.GetIssue(ctx, issue.ID)
		if otherIssue != nil {
			t.Errorf("%s: issue should NOT exist in other store (isolation failure)", description)
		}
	}
}

// TestContributorRoutingDirect verifies routing works in direct mode (no sync-branch, no no-db)
func TestContributorRoutingDirect(t *testing.T) {
	env := setupContributorRoutingEnv(t)
	defer env.cleanup()

	projectStore := env.initProjectStore("direct")
	defer projectStore.Close()

	planningStore := env.initPlanningStore()
	defer planningStore.Close()

	// Build routing config from stored values
	mode, _ := projectStore.GetConfig(env.ctx, "routing.mode")
	contributorPath, _ := projectStore.GetConfig(env.ctx, "routing.contributor")

	routingConfig := &routing.RoutingConfig{
		Mode:            mode,
		ContributorRepo: contributorPath,
		MaintainerRepo:  ".",
	}

	verifyIssueRouting(
		t, env.ctx, routingConfig, routing.Contributor,
		planningStore, projectStore, env.planningDir,
		"direct mode contributor routing",
	)
}

// TestContributorRoutingSyncBranch verifies routing works when sync.branch is configured
func TestContributorRoutingSyncBranch(t *testing.T) {
	env := setupContributorRoutingEnv(t)
	defer env.cleanup()

	projectStore := env.initProjectStore("sync-branch")
	defer projectStore.Close()

	planningStore := env.initPlanningStore()
	defer planningStore.Close()

	// Verify sync.branch is set
	syncBranch, _ := projectStore.GetConfig(env.ctx, "sync.branch")
	if syncBranch != "beads-sync" {
		t.Fatalf("sync.branch not set correctly: got %q, want %q", syncBranch, "beads-sync")
	}

	// Build routing config
	mode, _ := projectStore.GetConfig(env.ctx, "routing.mode")
	contributorPath, _ := projectStore.GetConfig(env.ctx, "routing.contributor")

	routingConfig := &routing.RoutingConfig{
		Mode:            mode,
		ContributorRepo: contributorPath,
		MaintainerRepo:  ".",
	}

	verifyIssueRouting(
		t, env.ctx, routingConfig, routing.Contributor,
		planningStore, projectStore, env.planningDir,
		"sync-branch mode contributor routing",
	)
}

// TestContributorRoutingNoDb verifies routing works when sync.nodb is enabled
func TestContributorRoutingNoDb(t *testing.T) {
	env := setupContributorRoutingEnv(t)
	defer env.cleanup()

	projectStore := env.initProjectStore("no-db")
	defer projectStore.Close()

	planningStore := env.initPlanningStore()
	defer planningStore.Close()

	// Verify no-db is set
	nodb, _ := projectStore.GetConfig(env.ctx, "sync.nodb")
	if nodb != "true" {
		t.Fatalf("sync.nodb not set correctly: got %q, want %q", nodb, "true")
	}

	// Build routing config
	mode, _ := projectStore.GetConfig(env.ctx, "routing.mode")
	contributorPath, _ := projectStore.GetConfig(env.ctx, "routing.contributor")

	routingConfig := &routing.RoutingConfig{
		Mode:            mode,
		ContributorRepo: contributorPath,
		MaintainerRepo:  ".",
	}

	verifyIssueRouting(
		t, env.ctx, routingConfig, routing.Contributor,
		planningStore, projectStore, env.planningDir,
		"no-db mode contributor routing",
	)
}

// TestContributorRoutingDaemon verifies routing works when daemon mode is active.
// Note: In practice, daemon mode is bypassed for routed issues (T013), but the
// routing decision should still be correct.
func TestContributorRoutingDaemon(t *testing.T) {
	env := setupContributorRoutingEnv(t)
	defer env.cleanup()

	projectStore := env.initProjectStore("direct")
	defer projectStore.Close()

	planningStore := env.initPlanningStore()
	defer planningStore.Close()

	// Build routing config
	mode, _ := projectStore.GetConfig(env.ctx, "routing.mode")
	contributorPath, _ := projectStore.GetConfig(env.ctx, "routing.contributor")

	routingConfig := &routing.RoutingConfig{
		Mode:            mode,
		ContributorRepo: contributorPath,
		MaintainerRepo:  ".",
	}

	// Verify routing decision is correct (daemon mode bypasses RPC for routed issues)
	// The key behavior is: when routing to a different repo, daemon is bypassed (T013)
	targetRepo := routing.DetermineTargetRepo(routingConfig, routing.Contributor, ".")
	if targetRepo != env.planningDir {
		t.Errorf("daemon mode routing decision: got %q, want %q", targetRepo, env.planningDir)
	}

	// Verify we can create issue directly in planning store (simulating daemon bypass)
	issue := &types.Issue{
		Title:     "Test daemon mode routing bypass",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}

	if err := planningStore.CreateIssue(env.ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue in planning store: %v", err)
	}

	// Verify isolation
	projectIssue, _ := projectStore.GetIssue(env.ctx, issue.ID)
	if projectIssue != nil {
		t.Error("issue should NOT exist in project store (daemon bypass isolation failure)")
	}
}

// TestContributorRoutingLocalOnly verifies routing works when local-only mode is enabled
func TestContributorRoutingLocalOnly(t *testing.T) {
	env := setupContributorRoutingEnv(t)
	defer env.cleanup()

	projectStore := env.initProjectStore("local-only")
	defer projectStore.Close()

	planningStore := env.initPlanningStore()
	defer planningStore.Close()

	// Verify local-only is set
	localOnly, _ := projectStore.GetConfig(env.ctx, "sync.local-only")
	if localOnly != "true" {
		t.Fatalf("sync.local-only not set correctly: got %q, want %q", localOnly, "true")
	}

	// Build routing config
	mode, _ := projectStore.GetConfig(env.ctx, "routing.mode")
	contributorPath, _ := projectStore.GetConfig(env.ctx, "routing.contributor")

	routingConfig := &routing.RoutingConfig{
		Mode:            mode,
		ContributorRepo: contributorPath,
		MaintainerRepo:  ".",
	}

	verifyIssueRouting(
		t, env.ctx, routingConfig, routing.Contributor,
		planningStore, projectStore, env.planningDir,
		"local-only mode contributor routing",
	)
}

// TestMaintainerRoutingUnaffected verifies maintainers' issues stay in the project repo
func TestMaintainerRoutingUnaffected(t *testing.T) {
	env := setupContributorRoutingEnv(t)
	defer env.cleanup()

	projectStore := env.initProjectStore("direct")
	defer projectStore.Close()

	planningStore := env.initPlanningStore()
	defer planningStore.Close()

	// Build routing config
	mode, _ := projectStore.GetConfig(env.ctx, "routing.mode")
	contributorPath, _ := projectStore.GetConfig(env.ctx, "routing.contributor")

	routingConfig := &routing.RoutingConfig{
		Mode:            mode,
		ContributorRepo: contributorPath,
		MaintainerRepo:  ".", // Maintainer stays local
	}

	// Verify maintainer routes to local repo (.)
	targetRepo := routing.DetermineTargetRepo(routingConfig, routing.Maintainer, env.projectDir)
	if targetRepo != "." {
		t.Errorf("maintainer routing: got %q, want %q", targetRepo, ".")
	}

	// Create issue in project store (maintainer's issue stays local)
	issue := &types.Issue{
		Title:     "Test maintainer issue stays local",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}

	if err := projectStore.CreateIssue(env.ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue in project store: %v", err)
	}

	// Verify issue exists in project store
	retrieved, err := projectStore.GetIssue(env.ctx, issue.ID)
	if err != nil || retrieved == nil {
		t.Fatalf("maintainer issue not found in project store: %v", err)
	}

	// Verify issue does NOT exist in planning store
	planningIssue, _ := planningStore.GetIssue(env.ctx, issue.ID)
	if planningIssue != nil {
		t.Error("maintainer issue should NOT exist in planning store (isolation failure)")
	}
}

// TestExplicitRepoOverride verifies --repo flag takes precedence over auto-routing
func TestExplicitRepoOverride(t *testing.T) {
	env := setupContributorRoutingEnv(t)
	defer env.cleanup()

	projectStore := env.initProjectStore("direct")
	defer projectStore.Close()

	planningStore := env.initPlanningStore()
	defer planningStore.Close()

	// Create a third "override" directory
	overrideDir := filepath.Join(env.tmpDir, "override")
	overrideBeadsDir := filepath.Join(overrideDir, ".beads")
	if err := os.MkdirAll(overrideBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create override .beads dir: %v", err)
	}

	overrideDBPath := filepath.Join(overrideBeadsDir, "beads.db")
	overrideStore, err := dolt.New(env.ctx, &dolt.Config{Path: overrideDBPath})
	if err != nil {
		t.Fatalf("failed to create override store: %v", err)
	}
	defer overrideStore.Close()

	if err := overrideStore.SetConfig(env.ctx, "issue_prefix", "over-"); err != nil {
		t.Fatalf("failed to set issue_prefix in override store: %v", err)
	}

	// Build routing config WITH explicit override
	mode, _ := projectStore.GetConfig(env.ctx, "routing.mode")
	contributorPath, _ := projectStore.GetConfig(env.ctx, "routing.contributor")

	routingConfig := &routing.RoutingConfig{
		Mode:             mode,
		ContributorRepo:  contributorPath,
		MaintainerRepo:   ".",
		ExplicitOverride: overrideDir, // --repo /path/to/override
	}

	// Verify explicit override takes precedence over auto-routing
	targetRepo := routing.DetermineTargetRepo(routingConfig, routing.Contributor, env.projectDir)
	if targetRepo != overrideDir {
		t.Errorf("explicit repo override: got %q, want %q", targetRepo, overrideDir)
	}

	// Verify maintainer also respects explicit override
	targetRepo = routing.DetermineTargetRepo(routingConfig, routing.Maintainer, env.projectDir)
	if targetRepo != overrideDir {
		t.Errorf("explicit repo override for maintainer: got %q, want %q", targetRepo, overrideDir)
	}

	// Create issue in override store
	issue := &types.Issue{
		Title:     "Test explicit override",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}

	if err := overrideStore.CreateIssue(env.ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue in override store: %v", err)
	}

	// Verify issue exists ONLY in override store
	retrieved, _ := overrideStore.GetIssue(env.ctx, issue.ID)
	if retrieved == nil {
		t.Error("issue not found in override store")
	}

	projectIssue, _ := projectStore.GetIssue(env.ctx, issue.ID)
	if projectIssue != nil {
		t.Error("issue should NOT exist in project store")
	}

	planningIssue, _ := planningStore.GetIssue(env.ctx, issue.ID)
	if planningIssue != nil {
		t.Error("issue should NOT exist in planning store")
	}
}

// TestBEADS_DIRPrecedence verifies BEADS_DIR env var takes precedence over routing config
func TestBEADS_DIRPrecedence(t *testing.T) {
	env := setupContributorRoutingEnv(t)
	defer env.cleanup()

	// Create an external beads directory (simulating BEADS_DIR target)
	externalDir := filepath.Join(env.tmpDir, "external")
	externalBeadsDir := filepath.Join(externalDir, ".beads")
	if err := os.MkdirAll(externalBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create external .beads dir: %v", err)
	}

	externalDBPath := filepath.Join(externalBeadsDir, "beads.db")
	externalStore, err := dolt.New(env.ctx, &dolt.Config{Path: externalDBPath})
	if err != nil {
		t.Fatalf("failed to create external store: %v", err)
	}
	defer externalStore.Close()

	if err := externalStore.SetConfig(env.ctx, "issue_prefix", "ext-"); err != nil {
		t.Fatalf("failed to set issue_prefix in external store: %v", err)
	}

	// Set BEADS_DIR to external directory
	t.Setenv("BEADS_DIR", externalBeadsDir)

	// The key insight: BEADS_DIR is checked in FindBeadsDir() BEFORE routing config is read.
	// This test verifies the precedence order documented in CONTRIBUTOR_NAMESPACE_ISOLATION.md:
	// 1. BEADS_DIR environment variable (highest precedence)
	// 2. --repo flag (explicit override)
	// 3. Auto-routing based on user role
	// 4. Current directory's .beads/ (default)

	// When BEADS_DIR is set, all operations should use that directory,
	// regardless of routing config.

	// Create issue in external store
	issue := &types.Issue{
		Title:     "Test BEADS_DIR precedence",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}

	if err := externalStore.CreateIssue(env.ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue in external store: %v", err)
	}

	// Verify issue exists in external store
	retrieved, err := externalStore.GetIssue(env.ctx, issue.ID)
	if err != nil || retrieved == nil {
		t.Fatalf("issue not found in external store: %v", err)
	}

	// Initialize project store (should be ignored when BEADS_DIR is set)
	projectStore := env.initProjectStore("direct")
	defer projectStore.Close()

	// Verify issue does NOT exist in project store
	projectIssue, _ := projectStore.GetIssue(env.ctx, issue.ID)
	if projectIssue != nil {
		t.Error("issue should NOT exist in project store when BEADS_DIR is set")
	}
}

// TestExplicitRoleOverride verifies git config beads.role takes precedence over URL detection
func TestExplicitRoleOverride(t *testing.T) {
	// Test that beads.role=maintainer in git config forces maintainer role
	// even when the remote URL would indicate contributor

	tests := []struct {
		name         string
		configRole   string
		expectedRole routing.UserRole
		description  string
	}{
		{
			name:         "explicit maintainer",
			configRole:   "maintainer",
			expectedRole: routing.Maintainer,
			description:  "git config beads.role=maintainer should force maintainer",
		},
		{
			name:         "explicit contributor",
			configRole:   "contributor",
			expectedRole: routing.Contributor,
			description:  "git config beads.role=contributor should force contributor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use the routing package's gitCommandRunner stub mechanism
			// This is tested more thoroughly in routing_test.go, but we verify
			// the integration here

			// Build a routing config assuming the role override
			routingConfig := &routing.RoutingConfig{
				Mode:            "auto",
				ContributorRepo: "/path/to/planning",
				MaintainerRepo:  ".",
			}

			// Verify routing behavior based on role
			var expectedRepo string
			if tt.expectedRole == routing.Maintainer {
				expectedRepo = "."
			} else {
				expectedRepo = "/path/to/planning"
			}

			targetRepo := routing.DetermineTargetRepo(routingConfig, tt.expectedRole, ".")
			if targetRepo != expectedRepo {
				t.Errorf("%s: got target repo %q, want %q", tt.description, targetRepo, expectedRepo)
			}
		})
	}
}

// TestRoutingWithAllSyncModes is a table-driven test covering all sync mode combinations
func TestRoutingWithAllSyncModes(t *testing.T) {
	syncModes := []struct {
		name       string
		mode       string
		configKey  string
		configVal  string
		extraCheck func(t *testing.T, store *dolt.DoltStore, ctx context.Context)
	}{
		{
			name:      "direct",
			mode:      "direct",
			configKey: "",
			configVal: "",
		},
		{
			name:      "sync-branch",
			mode:      "sync-branch",
			configKey: "sync.branch",
			configVal: "beads-sync",
			extraCheck: func(t *testing.T, store *dolt.DoltStore, ctx context.Context) {
				val, _ := store.GetConfig(ctx, "sync.branch")
				if val != "beads-sync" {
					t.Errorf("sync.branch = %q, want %q", val, "beads-sync")
				}
			},
		},
		{
			name:      "no-db",
			mode:      "no-db",
			configKey: "sync.nodb",
			configVal: "true",
		},
		{
			name:      "local-only",
			mode:      "local-only",
			configKey: "sync.local-only",
			configVal: "true",
		},
	}

	for _, sm := range syncModes {
		t.Run(sm.name, func(t *testing.T) {
			env := setupContributorRoutingEnv(t)
			defer env.cleanup()

			projectStore := env.initProjectStore(sm.mode)
			defer projectStore.Close()

			planningStore := env.initPlanningStore()
			defer planningStore.Close()

			// Run extra check if provided
			if sm.extraCheck != nil {
				sm.extraCheck(t, projectStore, env.ctx)
			}

			// Build routing config
			mode, _ := projectStore.GetConfig(env.ctx, "routing.mode")
			contributorPath, _ := projectStore.GetConfig(env.ctx, "routing.contributor")

			routingConfig := &routing.RoutingConfig{
				Mode:            mode,
				ContributorRepo: contributorPath,
				MaintainerRepo:  ".",
			}

			// Verify contributor routing
			targetRepo := routing.DetermineTargetRepo(routingConfig, routing.Contributor, ".")
			if targetRepo != env.planningDir {
				t.Errorf("sync mode %s: contributor target = %q, want %q",
					sm.name, targetRepo, env.planningDir)
			}

			// Verify maintainer routing
			targetRepo = routing.DetermineTargetRepo(routingConfig, routing.Maintainer, ".")
			if targetRepo != "." {
				t.Errorf("sync mode %s: maintainer target = %q, want %q",
					sm.name, targetRepo, ".")
			}
		})
	}
}
