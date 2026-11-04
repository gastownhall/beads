package main

import (
	"os"
	"path/filepath"
	"testing"
)

// populateLegacyDoltRoot lays down a .beads/dolt root that holds a Dolt
// database, the shape a historical local sql-server left behind. The guard has
// to treat such a root as evidence worth protecting.
func populateLegacyDoltRoot(t *testing.T, beadsDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads", ".dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// writeEmptyDoltRootServerWorkspace lays down the shape a current bd leaves
// behind after `bd init --server --external`: server mode selected, a
// .beads/dolt directory that bd created on use but that holds nothing because
// the data lives on the external server, and — once the gitignored witness has
// been lost, or when a provisioner created the empty root before bd init —
// no .local_version.
func writeEmptyDoltRootServerWorkspace(t *testing.T, metadata, configYaml, version string) string {
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
	if version != "" {
		if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), version); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	bindSelectedWorkspaceConfig(t, beadsDir)
	return beadsDir
}

const externalServerMetadata = `{"backend":"dolt","dolt_mode":"server","dolt_server_host":"db.example.test","dolt_database":"ext_db"}`

// TestLegacyUpgradeGuardAdmitsServerWorkspaceWithEmptyDoltRoot pins the
// external-host recovery: a server-mode workspace whose .beads/dolt is empty
// and whose witness is missing holds no local Dolt data a legacy release could
// have left there, so it carries no more evidence than a workspace with no
// .beads/dolt at all — which the guard already admits. Refusing it made the
// workspace unusable after `bd init --server --external` whenever the witness
// was absent, with no bd command able to repair it.
func TestLegacyUpgradeGuardAdmitsServerWorkspaceWithEmptyDoltRoot(t *testing.T) {
	cases := []struct {
		name       string
		metadata   string
		configYaml string
	}{
		{name: "metadata.json external server", metadata: externalServerMetadata},
		{name: "metadata.json server", metadata: `{"backend":"dolt","dolt_mode":"server"}`},
		{name: "config.yaml server mode", configYaml: configYamlServerMode},
		// Proxied-server does not select server mode, so it never reaches the
		// empty-root branch; it is admitted by the final non-embedded arm. Pin
		// that so a later change to either arm cannot start refusing it.
		{name: "metadata.json proxied server", metadata: `{"backend":"dolt","dolt_mode":"proxied-server"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := captureLegacyUpgradeWarnings(t)
			beadsDir := writeEmptyDoltRootServerWorkspace(t, tc.metadata, tc.configYaml, "")

			if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
			}
			if warnings.Len() != 0 {
				t.Fatalf("guard warned about an absent witness: %q", warnings.String())
			}
		})
	}
}

// TestLegacyUpgradeGuardEmptyDoltRootAdmissionStaysNarrow proves the empty-root
// admission relaxes nothing else: pre-1.0 evidence still refuses, a root that
// holds anything still refuses, and outside server mode an empty root is still
// the legacy embedded shape.
func TestLegacyUpgradeGuardEmptyDoltRootAdmissionStaysNarrow(t *testing.T) {
	t.Run("historical server witness", func(t *testing.T) {
		captureLegacyUpgradeWarnings(t)
		beadsDir := writeEmptyDoltRootServerWorkspace(t, externalServerMetadata, "", "0.62.0")

		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
		}
	})

	t.Run("historical embedded witness", func(t *testing.T) {
		captureLegacyUpgradeWarnings(t)
		beadsDir := writeEmptyDoltRootServerWorkspace(t, externalServerMetadata, "", "0.49.6")

		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
		}
	})

	for _, entry := range []string{"beads", ".dolt", "stray-file"} {
		t.Run("root holding "+entry, func(t *testing.T) {
			captureLegacyUpgradeWarnings(t)
			beadsDir := writeEmptyDoltRootServerWorkspace(t, externalServerMetadata, "", "")
			path := filepath.Join(beadsDir, "dolt", entry)
			var err error
			if entry == "stray-file" {
				err = os.WriteFile(path, nil, 0o600)
			} else {
				err = os.Mkdir(path, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}

			if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
			}
		})
	}

	for _, metadata := range []string{`{"backend":"dolt"}`, `{"backend":"dolt","dolt_mode":"embedded"}`} {
		t.Run("embedded "+metadata, func(t *testing.T) {
			captureLegacyUpgradeWarnings(t)
			beadsDir := writeEmptyDoltRootServerWorkspace(t, metadata, "", "")

			if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
			}
		})
	}
}
