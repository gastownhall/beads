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

// symlinkedBeadsDirTrapFixture lays out GH#4627: <home>/project/.beads is a
// symlink to an embedded workspace whose real directory is not named ".beads",
// and an unrelated leftover <home>/.beads (embeddeddolt only, no config) sits
// on the ancestor walk from that workspace. It chdirs into the project and
// returns the canonical target workspace, the symlink path and the trap.
func symlinkedBeadsDirTrapFixture(t *testing.T) (target, link, trap string) {
	t.Helper()
	root := utils.CanonicalizePath(t.TempDir())
	t.Setenv("BEADS_CEILING_DIRECTORIES", root)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BD_DB", "")

	home := filepath.Join(root, "home")
	trap = filepath.Join(home, ".beads")
	if err := os.MkdirAll(filepath.Join(trap, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("mkdir trap: %v", err)
	}

	target = filepath.Join(home, "Application Support", "beads", "myproject")
	if err := os.MkdirAll(filepath.Join(target, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte("issue-prefix: MYPROJECT\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	cfg := &configfile.Config{
		Database:     "dolt",
		Backend:      configfile.BackendDolt,
		DoltMode:     "embedded",
		DoltDatabase: "myproject",
	}
	if err := cfg.Save(target); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	link = filepath.Join(project, ".beads")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Chdir(project)
	return utils.CanonicalizePath(target), link, trap
}

// TestResolveCommandBeadsDir_SymlinkedBeadsDirTrapAncestor guards GH#4627:
// discovery follows a symlinked .beads and canonicalizes the store path to
// <target>/embeddeddolt, so PersistentPreRun's reverse mapping must bind that
// path back to the target workspace rather than walking up to an unrelated
// ancestor .beads (fixed by the GH#4574 parent-relationship match).
func TestResolveCommandBeadsDir_SymlinkedBeadsDirTrapAncestor(t *testing.T) {
	target, _, trap := symlinkedBeadsDirTrapFixture(t)

	dbPath := beads.FindDatabasePath()
	if dbPath == "" {
		t.Fatal("FindDatabasePath() found no database through the symlinked .beads")
	}
	got := resolveCommandBeadsDir(dbPath)
	if utils.PathsEqual(got, trap) {
		t.Fatalf("resolveCommandBeadsDir(%q) = %q, walked past the symlinked workspace to the ancestor trap", dbPath, got)
	}
	if !utils.PathsEqual(got, target) {
		t.Fatalf("resolveCommandBeadsDir(%q) = %q, want %q", dbPath, got, target)
	}
}

// TestResolveCommandBeadsDir_SymlinkedBeadsDirExplicitSpellings guards the
// explicit --db forms from GH#4627: the real target directory, the .beads
// symlink path and <target>/embeddeddolt must all bind the same workspace,
// never the ancestor trap.
func TestResolveCommandBeadsDir_SymlinkedBeadsDirExplicitSpellings(t *testing.T) {
	target, link, trap := symlinkedBeadsDirTrapFixture(t)

	for name, dbPath := range map[string]string{
		"target dir":      target,
		"symlink path":    link,
		"target data dir": filepath.Join(target, "embeddeddolt"),
	} {
		got := resolveCommandBeadsDir(dbPath)
		if utils.PathsEqual(got, trap) {
			t.Errorf("%s: resolveCommandBeadsDir(%q) = %q, the ancestor trap", name, dbPath, got)
			continue
		}
		if !utils.PathsEqual(got, target) {
			t.Errorf("%s: resolveCommandBeadsDir(%q) = %q, want workspace %q", name, dbPath, got, target)
		}
	}
}
