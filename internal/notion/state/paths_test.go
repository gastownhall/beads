package state

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsUsesBeadsConfigDirByDefault(t *testing.T) {
	xdgDir := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdgDir)

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths returned error: %v", err)
	}
	wantConfigDir := filepath.Join(xdgDir, "beads", "notion")
	if paths.ConfigDir != wantConfigDir {
		t.Fatalf("ConfigDir = %q, want %q", paths.ConfigDir, wantConfigDir)
	}
}

func TestDefaultPathsPrefersBeadsConfigDirEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	overrideDir := filepath.Join(t.TempDir(), "override")
	t.Setenv("BEADS_NOTION_CONFIG_DIR", overrideDir)

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths returned error: %v", err)
	}
	if paths.ConfigDir != overrideDir {
		t.Fatalf("ConfigDir = %q, want %q", paths.ConfigDir, overrideDir)
	}
}
