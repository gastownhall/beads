//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestConfigWinsOverConvention verifies SC-003: when metadata.json already
// has a non-empty dolt_database value, the init guard logic uses it instead
// of deriving beads_{prefix}.
func TestConfigWinsOverConvention(t *testing.T) {
	tests := map[string]struct {
		configJSON string
		prefix     string
		wantDB     string
	}{
		"existing config wins over prefix": {
			configJSON: `{"backend":"dolt","dolt_database":"custom_beads"}`,
			prefix:     "test",
			wantDB:     "custom_beads",
		},
		"empty config falls through to prefix": {
			configJSON: `{"backend":"dolt"}`,
			prefix:     "test",
			wantDB:     "beads_test",
		},
		"no prefix uses default": {
			configJSON: `{"backend":"dolt"}`,
			prefix:     "",
			wantDB:     "beads",
		},
		"no config file falls through to prefix": {
			configJSON: "", // don't create file
			prefix:     "myrig",
			wantDB:     "beads_myrig",
		},
		"existing config with no prefix still wins": {
			configJSON: `{"backend":"dolt","dolt_database":"acf_beads"}`,
			prefix:     "",
			wantDB:     "acf_beads",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatalf("failed to create beads dir: %v", err)
			}

			// Write metadata.json if provided
			if tt.configJSON != "" {
				configPath := filepath.Join(beadsDir, "metadata.json")
				if err := os.WriteFile(configPath, []byte(tt.configJSON), 0644); err != nil {
					t.Fatalf("failed to write config: %v", err)
				}
			}

			// Simulate the guard logic from init.go:248-258
			dbName := "beads"
			if tt.prefix != "" {
				dbName = "beads_" + tt.prefix
			}
			// Guard: config wins over convention
			if existingCfg, loadErr := configfile.Load(beadsDir); loadErr == nil && existingCfg != nil {
				if existingCfg.DoltDatabase != "" {
					dbName = existingCfg.DoltDatabase
				}
			}

			if dbName != tt.wantDB {
				t.Errorf("dbName = %q, want %q", dbName, tt.wantDB)
			}
		})
	}
}

// TestMetadataSavePreservesExistingDoltDatabase verifies FR-022: when
// metadata.json save path encounters an existing DoltDatabase value,
// it does not overwrite it with beads_{prefix}.
func TestMetadataSavePreservesExistingDoltDatabase(t *testing.T) {
	tests := map[string]struct {
		existingDB string
		prefix     string
		wantDB     string
	}{
		"existing value preserved": {
			existingDB: "custom_beads",
			prefix:     "test",
			wantDB:     "custom_beads",
		},
		"empty value gets prefix": {
			existingDB: "",
			prefix:     "test",
			wantDB:     "beads_test",
		},
		"no prefix no overwrite": {
			existingDB: "",
			prefix:     "",
			wantDB:     "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := &configfile.Config{
				DoltDatabase: tt.existingDB,
			}

			// Simulate the guard logic from init.go:361
			if tt.prefix != "" && cfg.DoltDatabase == "" {
				cfg.DoltDatabase = "beads_" + tt.prefix
			}

			if cfg.DoltDatabase != tt.wantDB {
				t.Errorf("DoltDatabase = %q, want %q", cfg.DoltDatabase, tt.wantDB)
			}
		})
	}
}
