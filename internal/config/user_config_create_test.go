package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// darwinCreateSeamForTest simulates a macOS host from any OS: HOME is the real
// source of ~/.config, and nativeDir stands in for the
// ~/Library/Application Support directory os.UserConfigDir returns on darwin.
// XDG_CONFIG_HOME is cleared so the darwin rule resolves HOME/.config.
func darwinCreateSeamForTest(t *testing.T) (home, nativeDir string) {
	t.Helper()
	home = t.TempDir()
	nativeDir = filepath.Join(home, "Library", "Application Support")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatalf("mkdir application support stand-in: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Cleanup(SetUserConfigPlatformForTest("darwin", func() (string, error) { return nativeDir, nil }))
	return home, nativeDir
}

// countLegacyProbes wraps the legacy-config stat seam and returns the number
// of times CreateUserConfigYaml probed the legacy Application Support path.
func countLegacyProbes(t *testing.T) *int32 {
	t.Helper()
	count := new(int32)
	t.Cleanup(SetLegacyUserConfigStatForTest(func(name string) (fs.FileInfo, error) {
		atomic.AddInt32(count, 1)
		return os.Stat(name)
	}))
	return count
}

func mkUserConfigDir(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".config", "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	return path
}

func readCreatedUserConfig(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	return string(got)
}

// TestCreateUserConfigYamlDarwinProbeBounds covers the carry-over's edge
// inputs: no legacy file, a legacy directory (never opened), a dangling
// legacy symlink (treated as absent) and a legacy file over the size cap
// (fail closed). Each is probed at creation only.
func TestCreateUserConfigYamlDarwinProbeBounds(t *testing.T) {
	seed := []byte("metrics:\n  disabled: false\n")

	t.Run("no legacy file: seed bytes, one probe", func(t *testing.T) {
		home, _ := darwinCreateSeamForTest(t)
		probes := countLegacyProbes(t)
		path := mkUserConfigDir(t, home)

		if err := CreateUserConfigYaml(path, seed); err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
		if got := readCreatedUserConfig(t, path); got != string(seed) {
			t.Errorf("created config = %q, want seed %q", got, seed)
		}
		if n := atomic.LoadInt32(probes); n != 1 {
			t.Errorf("legacy probes = %d, want 1 (the single stat of the one-time probe)", n)
		}
	})

	t.Run("legacy path is a directory: seed bytes, never opened", func(t *testing.T) {
		home, nativeDir := darwinCreateSeamForTest(t)
		countLegacyProbes(t)
		path := mkUserConfigDir(t, home)
		if err := os.MkdirAll(filepath.Join(nativeDir, "bd", "config.yaml"), 0o755); err != nil {
			t.Fatalf("mkdir legacy dir: %v", err)
		}

		if err := CreateUserConfigYaml(path, seed); err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
		if got := readCreatedUserConfig(t, path); got != string(seed) {
			t.Errorf("created config = %q, want seed %q (a non-regular legacy file must not be read)", got, seed)
		}
	})

	t.Run("dangling legacy symlink: treated as absent", func(t *testing.T) {
		home, nativeDir := darwinCreateSeamForTest(t)
		countLegacyProbes(t)
		path := mkUserConfigDir(t, home)
		if err := os.MkdirAll(filepath.Join(nativeDir, "bd"), 0o755); err != nil {
			t.Fatalf("mkdir legacy dir: %v", err)
		}
		legacyPath := filepath.Join(nativeDir, "bd", "config.yaml")
		if err := os.Symlink(filepath.Join(nativeDir, "nowhere"), legacyPath); err != nil {
			t.Fatalf("plant dangling legacy symlink: %v", err)
		}

		if err := CreateUserConfigYaml(path, seed); err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
		if got := readCreatedUserConfig(t, path); got != string(seed) {
			t.Errorf("created config = %q, want seed %q", got, seed)
		}
	})

	// readCapped's over-limit branch: a legacy file one byte past the cap is
	// not a config bd can trust, so the new file fails closed.
	t.Run("legacy file over the size cap: fail closed", func(t *testing.T) {
		home, nativeDir := darwinCreateSeamForTest(t)
		countLegacyProbes(t)
		path := mkUserConfigDir(t, home)
		legacyPath := filepath.Join(nativeDir, "bd", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
			t.Fatalf("mkdir legacy dir: %v", err)
		}
		big := make([]byte, legacyUserConfigReadCap+1)
		for i := range big {
			big[i] = '#'
		}
		if err := os.WriteFile(legacyPath, big, 0o600); err != nil {
			t.Fatalf("plant oversized legacy config: %v", err)
		}

		if err := CreateUserConfigYaml(path, seed); err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
		if got, want := readCreatedUserConfig(t, path), string(failClosedUserConfig(legacyPath)); got != want {
			t.Errorf("created config = %q, want fail-closed config %q", got, want)
		}
		if !MetricsDisabledByUserConfig() {
			t.Error("an oversized legacy config must fail closed with metrics off")
		}
	})
}

// TestCreateUserConfigYamlAlreadyExistsIsTyped: a pre-existing target makes
// the create report ErrUserConfigExists instead of replacing it, on darwin and
// elsewhere (the caller re-reads once; it never retries the create).
func TestCreateUserConfigYamlAlreadyExistsIsTyped(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("APPDATA", "")
			native := filepath.Join(home, "AppData", "Roaming")
			t.Cleanup(SetUserConfigPlatformForTest(goos, func() (string, error) { return native, nil }))
			path := mkUserConfigDir(t, home)
			if err := os.WriteFile(path, []byte("existing: true\n"), 0o600); err != nil {
				t.Fatalf("plant existing config: %v", err)
			}

			err := CreateUserConfigYaml(path, []byte("seed"))
			if !errors.Is(err, ErrUserConfigExists) {
				t.Fatalf("CreateUserConfigYaml error = %v, want ErrUserConfigExists", err)
			}
			if got := readCreatedUserConfig(t, path); got != "existing: true\n" {
				t.Errorf("existing file was disturbed: %q", got)
			}
		})
	}
}
