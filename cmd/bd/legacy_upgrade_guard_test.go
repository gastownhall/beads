package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

func TestLegacyUpgradeGuardRefusesHistoricalLayoutsWithoutMutatingMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		version  string
		sqlite   bool
		dolt     bool
	}{
		{
			name:     "classic sqlite",
			metadata: `{"database":"beads.db","backend":"sqlite"}`,
			version:  "0.50.3",
			sqlite:   true,
		},
		{
			name:     "legacy server dolt",
			metadata: `{"backend":"dolt","dolt_mode":"server"}`,
			version:  "0.62.0",
			dolt:     true,
		},
		{
			name:     "legacy default dolt",
			metadata: `{"backend":"dolt"}`,
			version:  "0.55.4",
			dolt:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			metadataPath := filepath.Join(beadsDir, "metadata.json")
			if err := os.WriteFile(metadataPath, []byte(tt.metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.sqlite {
				if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("SQLite format 3\x00"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.dolt {
				if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte(tt.version+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			before, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatal(err)
			}
			err = guardLegacyUpgradeWorkspace(beadsDir)
			if !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
			}
			after, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("guard rewrote metadata: got %q, want %q", after, before)
			}
		})
	}
}

func TestLegacyUpgradeGuardMetadataLessSQLiteAndCurrentEmbeddedPrecedence(t *testing.T) {
	t.Run("metadata-less v091 vc database", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "vc.db"), []byte("SQLite format 3\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !isLegacyUpgradeRefusal(guardLegacyUpgradeWorkspace(beadsDir)) {
			t.Fatal("metadata-less v0.9.1 layout was not refused")
		}
	})

	t.Run("metadata-less source with unrelated file is ignored", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "vc.db"), []byte("SQLite format 3\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "unrelated.txt"), []byte("not source evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			t.Fatalf("ambiguous metadata-less layout was refused: %v", err)
		}
	})

	t.Run("metadata-less non-SQLite database is ignored", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "cache.db"), []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			t.Fatalf("non-SQLite database was refused as historical SQLite: %v", err)
		}
	})

	t.Run("current embedded root wins over stale sqlite artifact", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("stale SQLite artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt", "repo-entry"), []byte("opaque"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			t.Fatalf("current embedded workspace was refused due to stale artifacts: %v", err)
		}
	})

	t.Run("empty embedded root does not hide metadata-less sqlite", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "vc.db"), []byte("SQLite format 3\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
		}
	})

	t.Run("empty embedded root does not hide legacy dolt", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("0.55.4\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
		}
	})

	t.Run("current server metadata wins over stale sqlite artifact", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("SQLite format 3\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("1.1.2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			t.Fatalf("current server workspace was refused as SQLite: %v", err)
		}
	})

	t.Run("legacy external server needs no local Dolt root", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("0.62.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want external-server migration refusal", err)
		}
	})

	t.Run("explicit server metadata with missing version witness and local Dolt root is refused", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
		}
	})

	t.Run("explicit server metadata with malformed version witness and local Dolt root is refused", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("not-a-version\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
			t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
		}
	})

	t.Run("explicit server metadata with current version witness and local Dolt root is admitted", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("1.1.2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			t.Fatalf("current server workspace with current witness was refused: %v", err)
		}
	})

	t.Run("empty selection is ignored", func(t *testing.T) {
		if err := guardLegacyUpgradeWorkspace(""); err != nil {
			t.Fatalf("guardLegacyUpgradeWorkspace(\"\") = %v, want nil", err)
		}
	})
}

func TestLegacyUpgradeGuardServerSelectionBeatsStaleEmbeddedRepository(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		wantRefusal bool
	}{
		{name: "historical witness", version: "0.62.0", wantRefusal: true},
		{name: "missing witness", wantRefusal: true},
		{name: "malformed witness", version: "not-a-version", wantRefusal: true},
		{name: "current witness", version: "1.1.2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			metadata := []byte(`{"backend":"dolt","dolt_mode":"server","dolt_database":"selected_server_db"}`)
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.version != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte(tt.version+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
				t.Fatal(err)
			}
			staleRepo := filepath.Join(beadsDir, "embeddeddolt", "stale", ".dolt")
			if err := os.MkdirAll(staleRepo, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(staleRepo, "repo-entry"), []byte("opaque"), 0o600); err != nil {
				t.Fatal(err)
			}

			err := guardLegacyUpgradeWorkspace(beadsDir)
			if tt.wantRefusal && !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
			}
			if !tt.wantRefusal && err != nil {
				t.Fatalf("current selected server workspace was refused: %v", err)
			}
		})
	}
}

func TestLegacyUpgradeGuardRefusesOldDoltRootWithoutTrustingVersionWitness(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		version  string
	}{
		{name: "metadata-less and unversioned"},
		{name: "blank mode without witness", metadata: `{"backend":"dolt"}`},
		{name: "blank mode with malformed witness", metadata: `{"backend":"dolt"}`, version: "not-a-version"},
		{name: "v0.49.6 opt-in embedded", metadata: `{"backend":"dolt"}`, version: "0.49.6"},
		{name: "explicit embedded mode", metadata: `{"backend":"dolt","dolt_mode":"embedded"}`, version: "0.62.21"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if tt.metadata != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(tt.metadata), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.version != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte(tt.version+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := guardLegacyUpgradeWorkspace(beadsDir); !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
			}
		})
	}
}

func TestLegacyUpgradeGuardSharedServerAdmission(t *testing.T) {
	tests := []struct {
		name        string
		metadata    string
		version     string
		shared      string
		wantRefusal bool
	}{
		{name: "metadata-less shared without witness", shared: "1"},
		{name: "blank mode shared with malformed witness", metadata: `{"backend":"dolt"}`, version: "not-a-version", shared: "true"},
		{name: "metadata-less without shared server", shared: "0", wantRefusal: true},
		{name: "blank mode without shared server", metadata: `{"backend":"dolt"}`, version: "not-a-version", shared: "0", wantRefusal: true},
		{name: "metadata-less shared with historical witness", version: "0.62.0", shared: "1", wantRefusal: true},
		{name: "blank mode shared with historical witness", metadata: `{"backend":"dolt"}`, version: "0.62.0", shared: "true", wantRefusal: true},
		{name: "explicit server shared with historical witness", metadata: `{"backend":"dolt","dolt_mode":"server"}`, version: "0.62.0", shared: "1", wantRefusal: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			t.Setenv("BEADS_DOLT_SHARED_SERVER", tt.shared)

			beadsDir := t.TempDir()
			if tt.metadata != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(tt.metadata), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.version != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte(tt.version+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
				t.Fatal(err)
			}

			err := guardLegacyUpgradeWorkspace(beadsDir)
			if tt.wantRefusal && !isLegacyUpgradeRefusal(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want migration refusal", err)
			}
			if !tt.wantRefusal && err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want nil", err)
			}
		})
	}
}

func TestLegacyUpgradeGuardLeavesCurrentDoltWorkspaceAlone(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"embedded"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("1.1.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
		t.Fatalf("guardLegacyUpgradeWorkspace(%s) = %v, want nil", metadata, err)
	}
}

func TestLegacyUpgradeGuardRejectsBackendTombstonesWithoutMigratingConfig(t *testing.T) {
	for _, backend := range []string{"postgres", "mysql", "mystery"} {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			beadsDir := t.TempDir()
			legacyPath := filepath.Join(beadsDir, "config.json")
			legacy := []byte(`{"backend":"` + backend + `"}`)
			if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := guardLegacyUpgradeWorkspace(beadsDir); err == nil {
				t.Fatal("guardLegacyUpgradeWorkspace() = nil, want backend refusal")
			}
			after, err := os.ReadFile(legacyPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(legacy) {
				t.Fatalf("guard rewrote config.json: got %q, want %q", after, legacy)
			}
			if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(err) {
				t.Fatalf("guard created metadata.json: %v", err)
			}
		})
	}
}
