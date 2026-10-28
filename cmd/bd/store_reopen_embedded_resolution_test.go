package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
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

// TestResolveCommandBeadsDir_SymlinkedBeadsDirTrapAncestor pins the full
// GH#4627 chain on the fix from PR #4575 (GH#4574): a project whose .beads is
// a symlink to an embedded workspace that is not itself named .beads, with an
// unrelated ancestor .beads holding only embeddeddolt/ (the reporter's
// leftover default database). PersistentPreRun maps the discovered database
// path back to a beads directory with resolveCommandBeadsDir; before PR #4575
// the reverse mapping missed <target>/embeddeddolt and the ancestor walk then
// silently selected the trap, so list/export/ping/config get/show returned
// the ancestor's zero issues while where (FindBeadsDir) stayed correct.
func TestResolveCommandBeadsDir_SymlinkedBeadsDirTrapAncestor(t *testing.T) {
	root := utils.CanonicalizePath(t.TempDir())
	// Bound every ancestor walk (discovery and reverse mapping) to the
	// fixture so no stray ancestor .beads outside it can be selected.
	t.Setenv("BEADS_CEILING_DIRECTORIES", root)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")

	home := filepath.Join(root, "home")

	// Trap ancestor: the report's leftover default database — a .beads with
	// an embeddeddolt/ directory but neither metadata.json nor config.yaml,
	// so reverse mapping must skip it and the ancestor walk must never
	// reach it.
	trap := filepath.Join(home, ".beads")
	if err := os.MkdirAll(filepath.Join(trap, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("mkdir trap embeddeddolt: %v", err)
	}

	// Target: a relocated embedded workspace, not named .beads (mirrors the
	// reporter's ~/Library/Application Support/beads/<project> layout).
	target := filepath.Join(home, "Application Support", "beads", "myproject")
	if err := os.MkdirAll(filepath.Join(target, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("mkdir target embeddeddolt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte("issue-prefix: MYPROJECT\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	cfg := &configfile.Config{
		Database:     "dolt",
		Backend:      configfile.BackendDolt,
		DoltMode:     "embedded",
		DoltDatabase: "mystore",
	}
	if err := cfg.Save(target); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	// Project whose .beads is a symlink to the relocated workspace.
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	symlink := filepath.Join(project, ".beads")
	if err := os.Symlink(target, symlink); err != nil {
		t.Skipf("cannot create .beads symlink (%v); symlink layout not testable here", err)
	}
	t.Chdir(project)

	// AC-1: the full chain PersistentPreRun uses — FindDatabasePath resolves
	// the symlinked .beads on the spot and hands resolveCommandBeadsDir the
	// canonical <target>/embeddeddolt. Reverse mapping must return the
	// workspace, not walk past it to the trap ancestor.
	dbPath := beads.FindDatabasePath()
	if dbPath == "" {
		t.Fatalf("FindDatabasePath() = \"\", want the symlinked workspace's embeddeddolt")
	}
	got := resolveCommandBeadsDir(dbPath)
	if utils.PathsEqual(got, trap) {
		t.Errorf("resolveCommandBeadsDir(FindDatabasePath() = %q) = %q, walked past the symlinked .beads to the trap ancestor %q", dbPath, got, trap)
	}
	if !utils.PathsEqual(got, target) {
		t.Errorf("resolveCommandBeadsDir(FindDatabasePath() = %q) = %q, want workspace %q", dbPath, got, target)
	}
}
