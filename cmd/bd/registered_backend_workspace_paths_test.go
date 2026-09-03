package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/molecules"
)

// TestRegisteredBackendResolvesHookAndMoleculeDirsUnderBeads guards the root
// command's directory derivation for a registered WorkspaceIsBeadsDir backend.
//
// For such a backend the command sets dbPath to the .beads directory itself
// (main.go: `dbPath = bd` when registeredBackendWorkspaceIsBeadsDir(cfg) is
// true), not to a .beads/<db-file>. The hook runner and molecule loader must
// therefore resolve their directory from the beadsDir the command already
// computed with resolveCommandBeadsDir(dbPath), never from filepath.Dir(dbPath):
// for this case filepath.Dir(dbPath) is the repository root, so hooks and
// molecule templates would load from <repo>/hooks and <repo>/molecules.jsonl
// instead of .beads/hooks and .beads/molecules.jsonl.
//
// Driving the full PersistentPreRunE end-to-end would require a live
// storage.DoltStorage from the fake backend, which is impractical — the
// interface is large and has no test double (see
// internal/storage/hook_decorator_test.go). This test instead reproduces the
// exact directory-derivation chain the command uses and asserts the .beads
// hooks directory and molecule catalog are selected over repo-root decoys.
func TestRegisteredBackendResolvesHookAndMoleculeDirsUnderBeads(t *testing.T) {
	const name = "contract-workspace-paths"
	registerContractBackend(t, name)

	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := (&configfile.Config{Backend: name}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	// The workspace's real hooks directory and molecule catalog live under .beads.
	wantHooksDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(wantHooksDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads/hooks: %v", err)
	}
	wantMoleculeFile := filepath.Join(beadsDir, molecules.MoleculeFileName)
	if err := os.WriteFile(wantMoleculeFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write .beads/molecules.jsonl: %v", err)
	}

	// Decoys at the repository root: the pre-fix filepath.Dir(dbPath) code would
	// have selected these instead of the .beads copies.
	decoyHooksDir := filepath.Join(repo, "hooks")
	if err := os.MkdirAll(decoyHooksDir, 0o755); err != nil {
		t.Fatalf("mkdir decoy hooks: %v", err)
	}
	decoyMoleculeFile := filepath.Join(repo, molecules.MoleculeFileName)
	if err := os.WriteFile(decoyMoleculeFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write decoy molecules.jsonl: %v", err)
	}

	// This workspace is exactly the case that makes the command use the .beads
	// directory as dbPath.
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load metadata.json: %v", err)
	}
	if !registeredBackendWorkspaceIsBeadsDir(cfg) {
		t.Fatal("registered WorkspaceIsBeadsDir backend not recognized; dbPath would not be the .beads directory")
	}

	// Mirror the command: dbPath is the .beads directory and the hook/molecule
	// setup resolves the beads dir with resolveCommandBeadsDir(dbPath).
	dbPath := beadsDir
	gotBeadsDir := resolveCommandBeadsDir(dbPath)

	requireSameFile(t, "resolved beads dir", gotBeadsDir, beadsDir)
	requireSameFile(t, "hook runner dir", filepath.Join(gotBeadsDir, "hooks"), wantHooksDir)
	requireSameFile(t, "molecule catalog", filepath.Join(gotBeadsDir, molecules.MoleculeFileName), wantMoleculeFile)

	// Regression guard: the pre-fix filepath.Dir(dbPath) resolves to the repo
	// root, so hooks and molecules would have loaded from the decoys, not .beads.
	preFix := filepath.Dir(dbPath)
	if sameExistingFile(preFix, beadsDir) {
		t.Fatalf("filepath.Dir(dbPath)=%q unexpectedly equals .beads %q; regression guard is vacuous", preFix, beadsDir)
	}
	requireSameFile(t, "pre-fix hook dir resolves to repo-root decoy", filepath.Join(preFix, "hooks"), decoyHooksDir)
}

func requireSameFile(t *testing.T, label, got, want string) {
	t.Helper()
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("%s: stat %q: %v", label, got, err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatalf("%s: stat %q: %v", label, want, err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("%s: got %q, want %q (different paths)", label, got, want)
	}
}

func sameExistingFile(a, b string) bool {
	aInfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}
