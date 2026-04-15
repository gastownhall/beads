package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestCheckRemoteConsistency_WorktreeFallbackUsesSharedConfig(t *testing.T) {
	clearResolveBeadsDirCache()
	t.Cleanup(clearResolveBeadsDirCache)

	mainRepoDir, worktreeDir := setupWorktreeRepo(t)
	beadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create shared .beads: %v", err)
	}
	if err := (&configfile.Config{}).Save(beadsDir); err != nil {
		t.Fatalf("failed to write shared metadata: %v", err)
	}

	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")

	check := CheckRemoteConsistency(worktreeDir)
	if check.Status != StatusWarning {
		t.Fatalf("expected warning when shared config resolves but server is unavailable, got %q: %s", check.Status, check.Message)
	}
	if check.Message == "N/A (not using Dolt backend)" {
		t.Fatalf("expected shared worktree config to be used, got %q", check.Message)
	}
}
