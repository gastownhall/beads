package configfile

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

// TestLoadDiscoveredForOpenRefusesHistoricalSQLiteWorkspace pins the
// invariant's single home: the non-mutating discovery check every path that
// opens or inspects a workspace bd was not launched in runs before its
// migrating Load refuses each historical SQLite layout with the refusal
// command admission prints, and reads the workspace without changing a byte.
func TestLoadDiscoveredForOpenRefusesHistoricalSQLiteWorkspace(t *testing.T) {
	checks := map[string]func(string) error{
		"LoadDiscoveredForOpen": func(beadsDir string) error {
			cfg, err := LoadDiscoveredForOpen(beadsDir)
			if err != nil && cfg != nil {
				t.Errorf("LoadDiscoveredForOpen returned metadata %+v with its refusal", cfg)
			}
			return err
		},
		"CheckDiscoveredForOpen": CheckDiscoveredForOpen,
	}
	for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
		for name, check := range checks {
			t.Run(layout.Name+"/"+name, func(t *testing.T) {
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				layout.Write(t, beadsDir)
				before := legacyworkspace.TreeDigest(t, beadsDir)

				err := check(beadsDir)
				if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
					t.Fatalf("%s changed the workspace: before %v, after %v", name, before, after)
				}
				if err == nil || err.Error() != legacyworkspace.HistoricalSQLiteRefusal {
					t.Fatalf("%s = %v, want admission's refusal %q", name, err, legacyworkspace.HistoricalSQLiteRefusal)
				}
				var legacyErr *LegacyWorkspaceError
				if !errors.As(err, &legacyErr) || legacyErr.Reason != "historical SQLite workspace" {
					t.Fatalf("%s = %#v, want a *LegacyWorkspaceError", name, err)
				}
			})
		}
	}
}

// TestLoadDiscoveredForOpenAdmitsDoltBesideSQLiteLeftovers pins the other
// side: a workspace that holds an embedded Dolt repository, or whose metadata
// selects Dolt, is admitted whatever SQLite files sit beside it, as admission
// admits it.
func TestLoadDiscoveredForOpenAdmitsDoltBesideSQLiteLeftovers(t *testing.T) {
	for name, metadata := range map[string]string{
		"embedded repository": `{"database":"beads.db","jsonl_export":"issues.jsonl"}`,
		"dolt_database":       `{"database":"dolt","dolt_database":"beads"}`,
		"dolt_mode server":    `{"database":"beads.db","dolt_mode":"server"}`,
	} {
		t.Run(name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(beadsDir, ConfigFileName), []byte(metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("SQLite format 3\x00leftover"), 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "embedded repository" {
				if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt", "noms"), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := LoadDiscoveredForOpen(beadsDir)
			if err != nil || cfg == nil {
				t.Fatalf("LoadDiscoveredForOpen = %+v, %v, want the metadata admitted", cfg, err)
			}
		})
	}
}

// TestCheckHistoricalSQLiteWorkspaceIsAdmissionsRefusal pins the refusal's
// text to the one command admission has always printed, which the shared test
// fixture spells out, and pins that the classifier and the refusal agree.
func TestCheckHistoricalSQLiteWorkspaceIsAdmissionsRefusal(t *testing.T) {
	if got := (&LegacyWorkspaceError{Reason: "historical SQLite workspace"}).Error(); got != legacyworkspace.HistoricalSQLiteRefusal {
		t.Fatalf("refusal = %q, want %q", got, legacyworkspace.HistoricalSQLiteRefusal)
	}
	for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
		t.Run(layout.Name, func(t *testing.T) {
			beadsDir := t.TempDir()
			layout.Write(t, beadsDir)
			cfg, err := LoadForDiscovery(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			if !IsHistoricalSQLiteWorkspace(beadsDir, cfg) {
				t.Fatalf("IsHistoricalSQLiteWorkspace = false for %s", layout.Name)
			}
			if err := CheckHistoricalSQLiteWorkspace(beadsDir, cfg); err == nil || err.Error() != legacyworkspace.HistoricalSQLiteRefusal {
				t.Fatalf("CheckHistoricalSQLiteWorkspace = %v, want the refusal", err)
			}
			// An embedded Dolt repository beside it settles the workspace as
			// Dolt, whatever the SQLite files say.
			if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt", "noms"), 0o750); err != nil {
				t.Fatal(err)
			}
			if !HasEmbeddedDoltRepository(beadsDir) || IsHistoricalSQLiteWorkspace(beadsDir, cfg) {
				t.Fatalf("an embedded repository did not settle %s as Dolt", layout.Name)
			}
		})
	}
}
