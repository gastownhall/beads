package configfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantDoltRepository creates the .dolt marker that makes dir look like a Dolt
// database root, creating parents as needed, and returns dir.
func plantDoltRepository(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
		t.Fatalf("plant dolt repository at %s: %v", dir, err)
	}
	return dir
}

// TestExistingDoltDataDir covers the layouts a workspace's Dolt data can live
// in. Detection drives the removed-backend message's branch, so a miss sends a
// user with a live database down the destructive export/reinitialize path.
func TestExistingDoltDataDir(t *testing.T) {
	// Each case seeds a workspace and returns the config to detect with plus the
	// directory detection must find ("" for none).
	tests := []struct {
		name  string
		setup func(t *testing.T, beadsDir string) (*Config, string)
	}{
		{
			name: "embedded layout, database one level down",
			setup: func(t *testing.T, beadsDir string) (*Config, string) {
				return &Config{}, plantDoltRepository(t, filepath.Join(beadsDir, "embeddeddolt", "beads"))
			},
		},
		{
			name: "embedded layout, database at the root",
			setup: func(t *testing.T, beadsDir string) (*Config, string) {
				return &Config{}, plantDoltRepository(t, filepath.Join(beadsDir, "embeddeddolt"))
			},
		},
		{
			name: "server layout at the root",
			setup: func(t *testing.T, beadsDir string) (*Config, string) {
				return &Config{}, plantDoltRepository(t, filepath.Join(beadsDir, "dolt"))
			},
		},
		{
			name: "server layout, database one level down",
			setup: func(t *testing.T, beadsDir string) (*Config, string) {
				return &Config{}, plantDoltRepository(t, filepath.Join(beadsDir, "dolt", "beads"))
			},
		},
		{
			name: "relative dolt_data_dir",
			setup: func(t *testing.T, beadsDir string) (*Config, string) {
				return &Config{DoltDataDir: "custom-data"}, plantDoltRepository(t, filepath.Join(beadsDir, "custom-data", "beads"))
			},
		},
		{
			name: "absolute dolt_data_dir",
			setup: func(t *testing.T, _ string) (*Config, string) {
				dir := plantDoltRepository(t, filepath.Join(t.TempDir(), "elsewhere"))
				return &Config{DoltDataDir: dir}, dir
			},
		},
		{
			name: "BEADS_DOLT_DATA_DIR overrides the configured data dir",
			setup: func(t *testing.T, _ string) (*Config, string) {
				dir := plantDoltRepository(t, filepath.Join(t.TempDir(), "env-data"))
				t.Setenv("BEADS_DOLT_DATA_DIR", dir)
				return &Config{DoltDataDir: "ignored-when-env-is-set"}, dir
			},
		},
		{
			name: "nil config still finds the default layouts",
			setup: func(t *testing.T, beadsDir string) (*Config, string) {
				return nil, plantDoltRepository(t, filepath.Join(beadsDir, "dolt"))
			},
		},
		{
			name: "nothing present",
			setup: func(t *testing.T, beadsDir string) (*Config, string) {
				// A directory without a .dolt marker is not a database, a .dolt
				// that is a regular file is not one either, and neither is a
				// data-dir root that is itself a file.
				if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "beads"), 0o755); err != nil {
					t.Fatalf("create decoy database dir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt"), []byte("not a dir"), 0o600); err != nil {
					t.Fatalf("create decoy .dolt file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(beadsDir, "dolt"), []byte("not a dir"), 0o600); err != nil {
					t.Fatalf("create decoy data dir file: %v", err)
				}
				return &Config{}, ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			cfg, want := tt.setup(t, beadsDir)
			if got := ExistingDoltDataDir(beadsDir, cfg); got != want {
				t.Errorf("ExistingDoltDataDir() = %q, want %q", got, want)
			}
		})
	}
}

// TestExistingDoltDataDirWithoutBeadsDir pins the degradation contract: a caller
// with no workspace directory gets "" (and so the generic message) rather than
// a guess rooted at the process working directory.
func TestExistingDoltDataDirWithoutBeadsDir(t *testing.T) {
	if got := ExistingDoltDataDir("", &Config{}); got != "" {
		t.Errorf("ExistingDoltDataDir(\"\") = %q, want \"\"", got)
	}
	if got := ExistingDoltDataDir("", nil); got != "" {
		t.Errorf("ExistingDoltDataDir(\"\", nil) = %q, want \"\"", got)
	}
}

// TestRemovedBackendErrorHealBranch pins the message a workspace with live Dolt
// data gets: metadata.json is the only thing wrong, so the error must name the
// file, the exact edit, and the untouched-data guarantee — and must NOT send the
// user to export and reinitialize a database that is one JSON edit from healthy.
func TestRemovedBackendErrorHealBranch(t *testing.T) {
	for _, backend := range []string{BackendSQLite, BackendPostgres, BackendMySQL} {
		t.Run(backend, func(t *testing.T) {
			beadsDir := t.TempDir()
			doltDir := plantDoltRepository(t, filepath.Join(beadsDir, "embeddeddolt", "beads"))

			message := RemovedBackendErrorAt(backend, beadsDir, &Config{Backend: backend}).Error()
			for _, want := range []string{
				"no longer supported",
				removedBackendRationale(backend),
				doltDir,
				ConfigPath(beadsDir),
				`"backend": "` + backend + `"`,
				`"backend": "dolt"`,
				BackendNotOpenedGuarantee,
			} {
				if !strings.Contains(message, want) {
					t.Errorf("heal-branch message missing %q:\n%s", want, message)
				}
			}
			if strings.Contains(strings.ToLower(message), "export") {
				t.Errorf("heal-branch message must not offer the destructive export path:\n%s", message)
			}
		})
	}
}

// TestRemovedBackendErrorRecoveryBranch pins the no-Dolt-data message: the
// configured database really is the only copy, so the export/reinitialize path
// stays — along with every substring the existing rejection tests match on.
func TestRemovedBackendErrorRecoveryBranch(t *testing.T) {
	for _, backend := range []string{BackendSQLite, BackendPostgres, BackendMySQL} {
		t.Run(backend, func(t *testing.T) {
			beadsDir := t.TempDir()
			message := RemovedBackendErrorAt(backend, beadsDir, &Config{Backend: backend}).Error()

			wants := []string{"no longer supported", "was not opened", "export", "dolt", beadsDir, "no Dolt database was found"}
			if backend == BackendSQLite {
				wants = append(wants, "single engine")
			} else {
				wants = append(wants, "general-purpose server databases", "simple and resource-light")
			}
			lowered := strings.ToLower(message)
			for _, want := range wants {
				if !strings.Contains(lowered, strings.ToLower(want)) {
					t.Errorf("recovery-branch message missing %q:\n%s", want, message)
				}
			}
			if strings.Contains(message, "to heal") {
				t.Errorf("recovery-branch message must not offer a metadata heal when there is no Dolt data:\n%s", message)
			}
		})
	}
}

// TestRemovedBackendErrorWithoutWorkspace pins the beadsDir-less fallback: the
// recovery branch without the "no Dolt database was found" clause, which is what
// the old RemovedBackendError said and what callers with no directory still get.
func TestRemovedBackendErrorWithoutWorkspace(t *testing.T) {
	message := RemovedBackendError(BackendSQLite).Error()
	for _, want := range []string{"no longer supported", "single engine", "was not opened", "export"} {
		if !strings.Contains(message, want) {
			t.Errorf("workspace-less message missing %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "no Dolt database was found") {
		t.Errorf("workspace-less message must not claim a search it never ran:\n%s", message)
	}
}

// TestUnknownBackendErrorHealBranch covers the same enrichment for a value bd
// never recognized at all: with Dolt data present, say so and name the edit.
func TestUnknownBackendErrorHealBranch(t *testing.T) {
	beadsDir := t.TempDir()
	doltDir := plantDoltRepository(t, filepath.Join(beadsDir, "dolt"))

	message := UnknownBackendErrorAt("mystery", beadsDir, &Config{Backend: "mystery"}).Error()
	for _, want := range []string{"not recognized", BackendNotOpenedGuarantee, doltDir, ConfigPath(beadsDir), `"backend": "dolt"`} {
		if !strings.Contains(message, want) {
			t.Errorf("unknown-backend heal message missing %q:\n%s", want, message)
		}
	}

	// Without Dolt data the text is exactly today's.
	plain := UnknownBackendErrorAt("mystery", t.TempDir(), &Config{Backend: "mystery"}).Error()
	if plain != UnknownBackendError("mystery").Error() {
		t.Errorf("unknown-backend message changed for a workspace with no Dolt data:\n got %s\nwant %s", plain, UnknownBackendError("mystery").Error())
	}
	if !strings.Contains(plain, "fix or restore metadata.json and retry") {
		t.Errorf("unknown-backend message lost its original remedy:\n%s", plain)
	}
}
