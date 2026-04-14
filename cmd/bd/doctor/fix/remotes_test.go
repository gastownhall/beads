package fix

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

func TestResolveRemoteConsistencyContext_SharedWorktreeFallback(t *testing.T) {
	mainRepoDir, worktreeDir := setupSharedWorktreeWorkspace(t)
	sharedBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(sharedBeadsDir, 0o755); err != nil {
		t.Fatalf("failed to create shared .beads dir: %v", err)
	}

	cfg := &configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltDatabase: "shared_beads",
	}
	if err := cfg.Save(sharedBeadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	ctx, err := resolveRemoteConsistencyContext(worktreeDir)
	if err != nil {
		t.Fatalf("resolveRemoteConsistencyContext() error = %v", err)
	}

	wantBeadsDir := resolvePathForTest(t, sharedBeadsDir)
	if ctx.beadsDir != wantBeadsDir {
		t.Fatalf("beadsDir = %q, want %q", ctx.beadsDir, wantBeadsDir)
	}

	wantDBDir := filepath.Join(doltserver.ResolveDoltDir(wantBeadsDir), cfg.GetDoltDatabase())
	if ctx.dbDir != wantDBDir {
		t.Fatalf("dbDir = %q, want %q", ctx.dbDir, wantDBDir)
	}
}
