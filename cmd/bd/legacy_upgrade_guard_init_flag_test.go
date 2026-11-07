package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestLegacyUpgradeGuardForInitAdmitsEmptyDoltRoot pins the one thing init's own
// intent unlocks. `bd init --server` resolves server mode from the flag, a source
// the workspace does not record until init has written it. A retry over the empty
// .beads/dolt that a failed first attempt leaves behind therefore looks like an
// embedded workspace with a local Dolt root, and the guard refuses it. Told what
// init already knows, the guard gives that root the admission a config-selected
// server workspace gets. Asked without it, in either form, it still refuses, so
// the admission cannot pass vacuously.
func TestLegacyUpgradeGuardForInitAdmitsEmptyDoltRoot(t *testing.T) {
	warnings := captureLegacyUpgradeWarnings(t)
	beadsDir := writeEmptyDoltRootServerWorkspace(t, "", "", "")

	if err := guardLegacyUpgradeWorkspaceForInit(beadsDir, true); err != nil {
		t.Fatalf("guardLegacyUpgradeWorkspaceForInit(dir, true) = %v, want nil", err)
	}
	if warnings.Len() != 0 {
		t.Fatalf("guard warned about an absent witness: %q", warnings.String())
	}
	if err := guardLegacyUpgradeWorkspaceForInit(beadsDir, false); !isLegacyUpgradeRefusal(err) {
		t.Fatalf("guardLegacyUpgradeWorkspaceForInit(dir, false) = %v, want migration refusal", err)
	}
	if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
		t.Fatalf("guardLegacyUpgradeWorkspace(dir) = %v, want migration refusal", err)
	}
}

// TestLegacyUpgradeGuardForInitAdmissionStaysNarrow proves init's intent relaxes
// nothing beyond an empty, witness-less .beads/dolt. With it set, anything that
// could be legacy data still refuses, and the outcomes a broader reading of the
// flag would move (treating it as if the workspace itself selected server mode)
// stay where they are.
func TestLegacyUpgradeGuardForInitAdmissionStaysNarrow(t *testing.T) {
	for _, entry := range []string{"beads", ".dolt", "stray-file", "symlink"} {
		t.Run("root holding "+entry, func(t *testing.T) {
			captureLegacyUpgradeWarnings(t)
			beadsDir := writeEmptyDoltRootServerWorkspace(t, "", "", "")
			path := filepath.Join(beadsDir, "dolt", entry)
			var err error
			switch entry {
			case "stray-file":
				err = os.WriteFile(path, nil, 0o600)
			case "symlink":
				if err = os.Symlink(t.TempDir(), path); err != nil {
					t.Skipf("symlink capability unavailable: %v", err)
				}
			default:
				err = os.Mkdir(path, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}

			if err := guardLegacyUpgradeWorkspaceForInit(beadsDir, true); !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspaceForInit(dir, true) = %v, want migration refusal", err)
			}
		})
	}

	for _, version := range []string{"0.49.6", "0.62.0"} {
		t.Run("pre-1.0 witness "+version, func(t *testing.T) {
			captureLegacyUpgradeWarnings(t)
			beadsDir := writeEmptyDoltRootServerWorkspace(t, "", "", version)

			if err := guardLegacyUpgradeWorkspaceForInit(beadsDir, true); !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspaceForInit(dir, true) = %v, want migration refusal", err)
			}
		})
	}

	t.Run("blank witness over a non-empty root", func(t *testing.T) {
		warnings := captureLegacyUpgradeWarnings(t)
		beadsDir := writeEmptyDoltRootServerWorkspace(t, "", "", "")
		if err := os.Mkdir(filepath.Join(beadsDir, "dolt", "beads"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		// A workspace that selects server mode itself warns and opens over an
		// unreadable witness. Init's intent must not move this root onto that arm.
		if err := guardLegacyUpgradeWorkspaceForInit(beadsDir, true); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspaceForInit(dir, true) = %v, want migration refusal", err)
		}
		if warnings.Len() != 0 {
			t.Fatalf("guard took the unreadable-witness arm: %q", warnings.String())
		}
	})

	t.Run("symlinked dolt root", func(t *testing.T) {
		captureLegacyUpgradeWarnings(t)
		beadsDir := writeEmptyDoltRootServerWorkspace(t, "", "", "")
		root := filepath.Join(beadsDir, "dolt")
		if err := os.Remove(root); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), root); err != nil {
			t.Skipf("symlink capability unavailable: %v", err)
		}

		// hasLegacyDoltRoot does not follow a symlink, so the guard never takes
		// such a root for a local Dolt root and it never reaches the empty-root
		// admission. Init's intent has to leave its outcome exactly where every
		// other caller finds it.
		want := fmt.Sprint(guardLegacyUpgradeWorkspace(beadsDir))
		if got := fmt.Sprint(guardLegacyUpgradeWorkspaceForInit(beadsDir, true)); got != want {
			t.Fatalf("guardLegacyUpgradeWorkspaceForInit(dir, true) = %s, want %s (unchanged by init's intent)", got, want)
		}
	})

	t.Run("embedded repository beside a non-empty root", func(t *testing.T) {
		captureLegacyUpgradeWarnings(t)
		beadsDir := writeEmptyDoltRootServerWorkspace(t, "", "", "")
		repo := filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt")
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "repo-entry"), []byte("opaque"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "dolt", "stray-file"), nil, 0o600); err != nil {
			t.Fatal(err)
		}

		// An embedded repository admits the workspace before server intent is
		// consulted at all. Folding init's intent into the workspace's own mode
		// would send this one to the refusing arm.
		for _, initSelectsServer := range []bool{false, true} {
			if err := guardLegacyUpgradeWorkspaceForInit(beadsDir, initSelectsServer); err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspaceForInit(dir, %t) = %v, want nil", initSelectsServer, err)
			}
		}
	})
}
