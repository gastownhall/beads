//go:build cgo

package doctor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func TestCheckRepoFingerprint_UsesTargetRepoOutsideCWD(t *testing.T) {
	outerRepo := t.TempDir()
	targetRepo := t.TempDir()

	setupGitRepoInDir(t, outerRepo)
	setupGitRepoInDir(t, targetRepo)

	targetRepoID, err := beads.ComputeRepoIDForPath(targetRepo)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(targetRepo) failed: %v", err)
	}

	beadsDir := filepath.Join(targetRepo, ".beads")
	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:     filepath.Join(beadsDir, "dolt"),
		Database: "beads",
	})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SetMetadata(ctx, "repo_id", targetRepoID); err != nil {
		t.Fatalf("failed to set repo_id metadata: %v", err)
	}

	runInDir(t, outerRepo, func() {
		check := CheckRepoFingerprint(targetRepo)

		if check.Status != StatusOK {
			t.Fatalf("Status = %q, want %q (message=%q detail=%q)", check.Status, StatusOK, check.Message, check.Detail)
		}
		if check.Message != "Verified ("+targetRepoID[:8]+")" {
			t.Fatalf("Message = %q, want %q", check.Message, "Verified ("+targetRepoID[:8]+")")
		}
	})
}
