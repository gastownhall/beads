package configfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("0.17.5")
	
	if cfg.Database != "beads.db" {
		t.Errorf("Database = %q, want beads.db", cfg.Database)
	}
	
	if cfg.Version != "0.17.5" {
		t.Errorf("Version = %q, want 0.17.5", cfg.Version)
	}
	
	if cfg.JSONLExport != "beads.jsonl" {
		t.Errorf("JSONLExport = %q, want beads.jsonl", cfg.JSONLExport)
	}
}

func TestLoadSaveRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	
	cfg := DefaultConfig("0.17.5")
	
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	
	loaded, err := Load(beadsDir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	
	if loaded == nil {
		t.Fatal("Load() returned nil config")
	}
	
	if loaded.Database != cfg.Database {
		t.Errorf("Database = %q, want %q", loaded.Database, cfg.Database)
	}
	
	if loaded.Version != cfg.Version {
		t.Errorf("Version = %q, want %q", loaded.Version, cfg.Version)
	}
	
	if loaded.JSONLExport != cfg.JSONLExport {
		t.Errorf("JSONLExport = %q, want %q", loaded.JSONLExport, cfg.JSONLExport)
	}
}

func TestLoadNonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	
	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load() returned error for nonexistent config: %v", err)
	}
	
	if cfg != nil {
		t.Errorf("Load() = %v, want nil for nonexistent config", cfg)
	}
}

func TestDatabasePath(t *testing.T) {
	beadsDir := "/home/user/project/.beads"
	cfg := &Config{Database: "beads.db"}
	
	got := cfg.DatabasePath(beadsDir)
	want := filepath.Join(beadsDir, "beads.db")
	
	if got != want {
		t.Errorf("DatabasePath() = %q, want %q", got, want)
	}
}

func TestJSONLPath(t *testing.T) {
	beadsDir := "/home/user/project/.beads"
	
	tests := []struct {
		name       string
		cfg        *Config
		want       string
	}{
		{
			name: "default",
			cfg:  &Config{JSONLExport: "beads.jsonl"},
			want: filepath.Join(beadsDir, "beads.jsonl"),
		},
		{
			name: "custom",
			cfg:  &Config{JSONLExport: "custom.jsonl"},
			want: filepath.Join(beadsDir, "custom.jsonl"),
		},
		{
			name: "empty falls back to default",
			cfg:  &Config{JSONLExport: ""},
			want: filepath.Join(beadsDir, "beads.jsonl"),
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.JSONLPath(beadsDir)
			if got != tt.want {
				t.Errorf("JSONLPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigPath(t *testing.T) {
	beadsDir := "/home/user/project/.beads"
	got := ConfigPath(beadsDir)
	want := filepath.Join(beadsDir, "metadata.json")
	
	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}
