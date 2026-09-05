package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/migration"
)

// newTownRoot builds a directory that findTownRoot recognises: the marker it
// looks for is mayor/town.json and nothing else.
func newTownRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	// Same symlink reason as newBeadsDir: findTownRoot walks up from the cwd,
	// which the shell resolves, so the root it reports is the resolved one.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve town root: %v", err)
	}
	return resolved
}

// newBeadsDir builds a directory FindBeadsDir will accept. It validates that a
// candidate holds real project files, so an empty directory is not enough.
func newBeadsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	// FindBeadsDir canonicalizes the path it returns, and on macOS t.TempDir()
	// hands back /var/... which is a symlink to /private/var/.... Resolve here
	// so the comparison is about the DIRECTORY and not about which spelling of
	// it each side happened to keep.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve .beads: %v", err)
	}
	return resolved
}

// TestFreezeRootPrefersTheTownRoot pins the half that must not change. A town
// freeze is town-wide by design — every rig under it stands down from one
// sentinel — so a .beads directory inside a town must not be able to shadow it
// with a freeze of its own, and must not be consulted when the town is frozen.
func TestFreezeRootPrefersTheTownRoot(t *testing.T) {
	town := newTownRoot(t)
	t.Setenv("BEADS_DIR", newBeadsDir(t))
	t.Chdir(town)

	if got := freezeRoot(); got != town {
		t.Errorf("freezeRoot() = %q, want the town root %q — a town-wide freeze must not be shadowed by a workspace one", got, town)
	}
}

// TestFreezeRootFallsBackToTheBeadsDir is the change. Outside a town
// findTownRoot returns "", IsFrozen("") is false, and the stand-down was
// unreachable for every workspace that is not Gas Town.
func TestFreezeRootFallsBackToTheBeadsDir(t *testing.T) {
	beadsDir := newBeadsDir(t)
	t.Setenv("BEADS_DIR", beadsDir)
	// Somewhere with no mayor/town.json above it.
	t.Chdir(t.TempDir())
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")

	if got := freezeRoot(); got != beadsDir {
		t.Errorf("freezeRoot() = %q, want the .beads dir %q", got, beadsDir)
	}
}

// TestFreezeRootIsFrozenEndToEnd is the composition that matters: a sentinel
// dropped in the .beads directory must actually read as frozen. freezeRoot
// returning a path proves nothing on its own if IsFrozen then looks elsewhere.
func TestFreezeRootIsFrozenEndToEnd(t *testing.T) {
	beadsDir := newBeadsDir(t)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(t.TempDir())
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")

	if migration.IsFrozen(freezeRoot()) {
		t.Fatal("must not be frozen before the sentinel exists")
	}

	sentinel := migration.FilePath(freezeRoot())
	if err := os.WriteFile(sentinel, []byte("kb\t2026-09-03T10:00:00Z\tschema bump"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if !migration.IsFrozen(freezeRoot()) {
		t.Fatalf("a MIGRATION-FREEZE at %s must read as frozen", sentinel)
	}

	// And the parsed contents reach the caller, so the refusal can name the
	// operator and the reason rather than just saying no.
	info := migration.Read(freezeRoot())
	if info == nil || info.Operator != "kb" || info.Reason != "schema bump" {
		t.Errorf("Read() = %+v, want operator=kb reason=\"schema bump\"", info)
	}

	if err := os.Remove(sentinel); err != nil {
		t.Fatalf("remove sentinel: %v", err)
	}
	if migration.IsFrozen(freezeRoot()) {
		t.Error("removing the sentinel must thaw the workspace")
	}
}

// TestFreezeRootWithNoWorkspaceIsNotFrozen pins the no-op case: bd run outside
// any beads workspace and outside any town must not start refusing writes.
func TestFreezeRootWithNoWorkspaceIsNotFrozen(t *testing.T) {
	t.Setenv("BEADS_DIR", "")
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")
	t.Chdir(t.TempDir())

	if migration.IsFrozen(freezeRoot()) {
		t.Error("no town and no .beads dir must never read as frozen")
	}
}
