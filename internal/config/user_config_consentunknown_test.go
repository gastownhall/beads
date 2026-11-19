package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestCreateUserConfigYamlUncreatableDirNeverProbes: on darwin, when the user
// config directory cannot be created or written, the legacy path is probed
// zero times and the error wraps ErrUserConfigConsentUnknown; on every other
// platform the same failure is a plain error and the sentinel is never
// produced.
func TestCreateUserConfigYamlUncreatableDirNeverProbes(t *testing.T) {
	t.Run("config dir blocked by a file", func(t *testing.T) {
		home, _ := darwinCreateSeamForTest(t)
		probes := countLegacyProbes(t)
		if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a dir"), 0o600); err != nil {
			t.Fatalf("plant blocker: %v", err)
		}
		path := filepath.Join(home, ".config", "bd", "config.yaml")

		err := CreateUserConfigYaml(path, nil)
		if err == nil {
			t.Fatal("CreateUserConfigYaml returned nil for an uncreatable config dir")
		}
		if !errors.Is(err, ErrUserConfigConsentUnknown) {
			t.Errorf("error %v does not wrap ErrUserConfigConsentUnknown (metrics must fail closed)", err)
		}
		if n := atomic.LoadInt32(probes); n != 0 {
			t.Errorf("legacy probes = %d, want 0 (an uncreatable config dir must never probe Application Support)", n)
		}
	})

	t.Run("read-only config dir fails after mkdir, before the probe", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("permission bits do not restrict root")
		}
		home, _ := darwinCreateSeamForTest(t)
		probes := countLegacyProbes(t)
		dir := filepath.Dir(mkUserConfigDir(t, home))
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatalf("chmod config dir read-only: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		path := filepath.Join(dir, "config.yaml")

		err := CreateUserConfigYaml(path, nil)
		if err == nil {
			t.Fatal("CreateUserConfigYaml returned nil for a read-only config dir")
		}
		if !errors.Is(err, ErrUserConfigConsentUnknown) {
			t.Errorf("error %v does not wrap ErrUserConfigConsentUnknown", err)
		}
		if n := atomic.LoadInt32(probes); n != 0 {
			t.Errorf("legacy probes = %d, want 0 (the staging file comes before the probe)", n)
		}
	})

	t.Run("non-darwin: same failure is a plain error, sentinel never produced", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("APPDATA", "")
		t.Cleanup(SetUserConfigPlatformForTest("windows", func() (string, error) { return "", nil }))
		probes := countLegacyProbes(t)
		if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a dir"), 0o600); err != nil {
			t.Fatalf("plant blocker: %v", err)
		}
		path := filepath.Join(home, ".config", "bd", "config.yaml")

		err := CreateUserConfigYaml(path, nil)
		if err == nil {
			t.Fatal("CreateUserConfigYaml returned nil for an uncreatable config dir")
		}
		if errors.Is(err, ErrUserConfigConsentUnknown) {
			t.Errorf("error %v wraps the darwin-only sentinel on a non-darwin platform", err)
		}
		if n := atomic.LoadInt32(probes); n != 0 {
			t.Errorf("legacy probes = %d, want 0 (non-darwin never probes)", n)
		}
	})
}
