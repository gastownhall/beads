package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

// TestResolveBeadsDirForDBPath_EmbeddedNonDotBeads guards GH#4574: an embedded
// store stores its data under <beadsDir>/embeddeddolt/, but Config.DatabasePath
// returns <beadsDir>/dolt. When the beads dir is not literally named ".beads"
// the early-return in resolveBeadsDirForDBPath does not fire, so resolution
// must still map <beadsDir>/embeddeddolt back to <beadsDir> via the parent
// relationship — otherwise commands silently fall back to an ancestor ~/.beads
// and report zero issues.
func TestResolveBeadsDirForDBPath_EmbeddedNonDotBeads(t *testing.T) {
	root := t.TempDir()
	// leaf is intentionally NOT ".beads" (mirrors an embedding tool that keeps
	// its store at e.g. ~/.local/state/<app>/beads).
	beadsDir := filepath.Join(root, "beads")
	dbPath := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatalf("mkdir embeddeddolt dir: %v", err)
	}

	cfg := &configfile.Config{
		Database:     "dolt",
		Backend:      configfile.BackendDolt,
		DoltMode:     "embedded",
		DoltDatabase: "mystore",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	if got := resolveBeadsDirForDBPath(dbPath); !utils.PathsEqual(got, beadsDir) {
		t.Fatalf("resolveBeadsDirForDBPath(%q) = %q, want %q", dbPath, got, beadsDir)
	}
}
