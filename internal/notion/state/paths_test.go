package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsPrefersBeadsEnvAndIgnoresLegacyEnvForConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("BDNOTION_CONFIG_DIR", filepath.Join(t.TempDir(), "legacy-env"))

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths returned error: %v", err)
	}
	wantConfigDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "beads", "notion")
	if paths.ConfigDir != wantConfigDir {
		t.Fatalf("ConfigDir = %q, want %q", paths.ConfigDir, wantConfigDir)
	}
	if paths.LegacyConfigDir != os.Getenv("BDNOTION_CONFIG_DIR") {
		t.Fatalf("LegacyConfigDir = %q, want %q", paths.LegacyConfigDir, os.Getenv("BDNOTION_CONFIG_DIR"))
	}
}

func TestDefaultPathsMigratesLegacyFilesIntoBeadsConfigDir(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, "legacy")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("BDNOTION_CONFIG_DIR", legacyDir)

	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatalf("MkdirAll legacyDir: %v", err)
	}
	legacyFiles := map[string]string{
		"client.json":      `{"client_id":"abc"}`,
		"tokens.json":      `{"access_token":"tok"}`,
		"auth-state.json":  `{"codeVerifier":"cv"}`,
		"beads.json":       `{"database_id":"db"}`,
		"beads-state.json": `{"page_ids":{"bd-1":"page-1"}}`,
	}
	for name, content := range legacyFiles {
		if err := os.WriteFile(filepath.Join(legacyDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths returned error: %v", err)
	}
	for name, want := range legacyFiles {
		data, err := os.ReadFile(filepath.Join(paths.ConfigDir, name))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", name, string(data), want)
		}
	}
}
