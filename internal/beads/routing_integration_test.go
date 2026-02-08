//go:build integration
// +build integration

package beads_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestRoutingIntegration(t *testing.T) {
	// Isolate from user's git config (e.g. url.insteadOf) to ensure deterministic URLs
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	tests := []struct {
		name               string
		setupGit           func(t *testing.T, dir string)
		expectedRole       routing.UserRole
		expectedTargetRepo string
	}{
		{
			name: "maintainer detected by git config",
			setupGit: func(t *testing.T, dir string) {
				runGitCmd(t, dir, "git", "init")
				runGitCmd(t, dir, "git", "config", "user.email", "maintainer@example.com")
				runGitCmd(t, dir, "git", "config", "beads.role", "maintainer")
			},
			expectedRole:       routing.Maintainer,
			expectedTargetRepo: ".",
		},
		{
			name: "contributor detected by fork remote",
			setupGit: func(t *testing.T, dir string) {
				runGitCmd(t, dir, "git", "init")
				runGitCmd(t, dir, "git", "remote", "add", "upstream", "https://github.com/original/repo.git")
				runGitCmd(t, dir, "git", "remote", "add", "origin", "https://github.com/forker/repo.git")
			},
			expectedRole:       routing.Contributor,
			expectedTargetRepo: "", // Will use default from config
		},
		{
			name: "maintainer with SSH remote",
			setupGit: func(t *testing.T, dir string) {
				runGitCmd(t, dir, "git", "init")
				runGitCmd(t, dir, "git", "remote", "add", "origin", "git@github.com:owner/repo.git")
				// Set explicit role to avoid relying on deprecated URL heuristic,
				// which can be flaky when git config rewrites SSH to HTTPS.
				runGitCmd(t, dir, "git", "config", "beads.role", "maintainer")
			},
			expectedRole:       routing.Maintainer,
			expectedTargetRepo: ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory
			tmpDir := t.TempDir()

			// Set up git
			tt.setupGit(t, tmpDir)

			// Detect user role
			role, err := routing.DetectUserRole(tmpDir)
			if err != nil {
				t.Fatalf("DetectUserRole() error = %v", err)
			}

			if role != tt.expectedRole {
				t.Errorf("expected role %v, got %v", tt.expectedRole, role)
			}

			// Test routing configuration
			routingCfg := &routing.RoutingConfig{
				Mode:             "auto",
				DefaultRepo:      "~/.beads-planning",
				MaintainerRepo:   ".",
				ContributorRepo:  "~/.beads-planning",
				ExplicitOverride: "",
			}

			targetRepo := routing.DetermineTargetRepo(routingCfg, role, tmpDir)

			if tt.expectedTargetRepo != "" && targetRepo != tt.expectedTargetRepo {
				t.Errorf("expected target repo %q, got %q", tt.expectedTargetRepo, targetRepo)
			}

			// For contributor, verify it routes to planning repo
			if role == routing.Contributor && !strings.Contains(targetRepo, "beads-planning") {
				t.Errorf("contributor should route to planning repo, got %q", targetRepo)
			}
		})
	}
}

func TestRoutingWithExplicitOverride(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up as contributor
	runGitCmd(t, tmpDir, "git", "init")
	runGitCmd(t, tmpDir, "git", "remote", "add", "upstream", "https://github.com/original/repo.git")
	runGitCmd(t, tmpDir, "git", "remote", "add", "origin", "https://github.com/forker/repo.git")

	role, err := routing.DetectUserRole(tmpDir)
	if err != nil {
		t.Fatalf("DetectUserRole() error = %v", err)
	}

	// Even though we're a contributor, --repo flag should override
	routingCfg := &routing.RoutingConfig{
		Mode:             "auto",
		DefaultRepo:      "~/.beads-planning",
		MaintainerRepo:   ".",
		ContributorRepo:  "~/.beads-planning",
		ExplicitOverride: "/custom/repo/path",
	}

	targetRepo := routing.DetermineTargetRepo(routingCfg, role, tmpDir)

	if targetRepo != "/custom/repo/path" {
		t.Errorf("expected explicit override to win, got %q", targetRepo)
	}
}

func TestMultiRepoEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}

	// Create primary repo
	primaryDir := t.TempDir()
	beadsDir := filepath.Join(primaryDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Initialize database
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	// Set up as maintainer
	runGitCmd(t, primaryDir, "git", "init")
	runGitCmd(t, primaryDir, "git", "config", "beads.role", "maintainer")

	// Configure multi-repo
	planningDir := t.TempDir()
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	if err := os.MkdirAll(planningBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create planning .beads dir: %v", err)
	}

	// Set config for multi-repo
	reposConfig := map[string][]string{
		"additional": {planningDir},
	}
	configJSON, err := json.Marshal(reposConfig)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}

	ctx := context.Background()
	if err := store.SetConfig(ctx, "repos.additional", string(configJSON)); err != nil {
		t.Fatalf("failed to set repos config: %v", err)
	}

	// Verify routing works
	role, err := routing.DetectUserRole(primaryDir)
	if err != nil {
		t.Fatalf("DetectUserRole() error = %v", err)
	}

	if role != routing.Maintainer {
		t.Errorf("expected maintainer role, got %v", role)
	}

	routingCfg := &routing.RoutingConfig{
		Mode:            "auto",
		DefaultRepo:     planningDir,
		MaintainerRepo:  ".",
		ContributorRepo: planningDir,
	}

	targetRepo := routing.DetermineTargetRepo(routingCfg, role, primaryDir)
	if targetRepo != "." {
		t.Errorf("maintainer should route to current repo, got %q", targetRepo)
	}

	t.Logf("Multi-repo end-to-end test passed")
	t.Logf("  Primary: %s", primaryDir)
	t.Logf("  Planning: %s", planningDir)
	t.Logf("  User role: %v", role)
	t.Logf("  Target repo: %s", targetRepo)
}

// Helper to run git commands
func runGitCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Command failed: %s %v\nOutput: %s", name, args, output)
		t.Fatalf("failed to run %s: %v", name, err)
	}
}
