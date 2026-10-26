package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// writeWitnessedDoltRootWorkspace lays down the GH#6259 base shape — a
// .beads/dolt root under a bd-1.3.0 version witness, no embedded repository
// unless the case adds one — plus whatever metadata and config.yaml the case
// needs, and binds the process configuration to the workspace as
// PersistentPreRunE does. emptyRoot swaps the populated root for an empty
// one; embeddedRepo adds a .beads/embeddeddolt repository beside it.
func writeWitnessedDoltRootWorkspace(t *testing.T, metadata, configYaml string, emptyRoot, embeddedRepo bool) string {
	t.Helper()
	beadsDir := t.TempDir()
	for name, contents := range map[string]string{
		"metadata.json": metadata,
		"config.yaml":   configYaml,
	} {
		if contents == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), "1.3.0"); err != nil {
		t.Fatal(err)
	}
	if emptyRoot {
		if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
	} else {
		populateLegacyDoltRoot(t, beadsDir)
	}
	if embeddedRepo {
		repo := filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt")
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "repo-entry"), []byte("opaque"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bindSelectedWorkspaceConfig(t, beadsDir)
	return beadsDir
}

// TestLegacyUpgradeGuardKeepsEveryOwnerOfADoltRootAdmitted pins the other
// half of the GH#6259 fix: the unowned-root refusal narrows the current-era
// witness veto without moving any workspace that does own its .beads/dolt
// onto the refusing arm. Every selection that opens the root through a Dolt
// sql-server — metadata's dolt_mode, config.yaml's dolt.mode, the
// environment, a shared server — plus proxied-server workspaces, registered
// non-Dolt backends, a workspace whose embedded repository already owns its
// data, and a root that holds nothing, all stay admitted. The first case is
// the refusal itself, so a change that swings either way fails here.
func TestLegacyUpgradeGuardKeepsEveryOwnerOfADoltRootAdmitted(t *testing.T) {
	tests := []struct {
		name         string
		metadata     string
		configYaml   string
		env          map[string]string
		emptyRoot    bool
		embeddedRepo bool
		wantRefusal  bool
	}{
		{name: "nothing owns the root", metadata: `{"backend":"dolt"}`, wantRefusal: true},
		{name: "metadata dolt_mode server", metadata: `{"backend":"dolt","dolt_mode":"server"}`},
		{name: "config.yaml dolt.mode server", configYaml: configYamlServerMode},
		{name: "environment server mode", env: map[string]string{"BEADS_DOLT_SERVER_MODE": "1"}},
		{name: "config.yaml shared server", configYaml: "dolt.shared-server: true\n"},
		{name: "metadata dolt_mode proxied-server", metadata: `{"backend":"dolt","dolt_mode":"proxied-server"}`},
		{name: "registered non-Dolt backend", metadata: `{"backend":"http"}`},
		{name: "embedded repository beside the root", metadata: `{"backend":"dolt"}`, embeddedRepo: true},
		{name: "empty root", metadata: `{"backend":"dolt"}`, emptyRoot: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := captureLegacyUpgradeWarnings(t)
			beadsDir := writeWitnessedDoltRootWorkspace(t, tt.metadata, tt.configYaml, tt.emptyRoot, tt.embeddedRepo)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			err := guardLegacyUpgradeWorkspace(beadsDir)
			if tt.wantRefusal {
				var legacyErr *configfile.LegacyWorkspaceError
				if !errors.As(err, &legacyErr) || err.Error() != unownedDoltRootRefusal {
					t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want %q", err, unownedDoltRootRefusal)
				}
				return
			}
			if err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
			}
			if warnings.Len() != 0 {
				t.Fatalf("guard warned over an admitted workspace: %q", warnings.String())
			}
		})
	}
}
