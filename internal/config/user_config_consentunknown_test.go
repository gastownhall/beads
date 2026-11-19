package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// writeLegacyOptOut plants a legacy Application Support config that opts out
// of metrics, so a probe that reached it would have something to carry.
func writeLegacyOptOut(t *testing.T, nativeDir string) {
	t.Helper()
	legacy := filepath.Join(nativeDir, "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("metrics:\n  disabled: true\n"), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
}

// TestCreateUserConfigYamlDarwinDanglingSymlinkNeverProbes: a symlink to a
// missing file at the user config path reads as missing, yet neither link(2)
// nor O_EXCL can publish over it. On darwin the create must see that before
// it stages a file or probes the legacy config, and report both
// ErrUserConfigExists (SetUserYamlConfig then surfaces its own read error, as
// designed) and ErrUserConfigConsentUnknown (the metrics bootstrap keeps
// metrics off). The symlink and its missing target are left alone.
func TestCreateUserConfigYamlDarwinDanglingSymlinkNeverProbes(t *testing.T) {
	home, nativeDir := darwinCreateSeamForTest(t)
	writeLegacyOptOut(t, nativeDir)
	probes := countLegacyProbes(t)
	path := mkUserConfigDir(t, home)
	target := filepath.Join(home, "dotfiles", "bd", "config.yaml") // never created
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := CreateUserConfigYaml(path, []byte("seed"))
	if !errors.Is(err, ErrUserConfigExists) || !errors.Is(err, ErrUserConfigConsentUnknown) {
		t.Fatalf("CreateUserConfigYaml error = %v; want it to wrap ErrUserConfigExists and ErrUserConfigConsentUnknown", err)
	}
	if !strings.Contains(err.Error(), "dangling symlink") {
		t.Errorf("error %q does not say the path is a dangling symlink", err)
	}

	// `bd metrics off` and `bd config set` take the same create path.
	err = SetUserYamlConfig("metrics.disabled", "true")
	if err == nil || !strings.Contains(err.Error(), "failed to read config.yaml") {
		t.Errorf("SetUserYamlConfig through a dangling symlink = %v, want its read error", err)
	}

	if n := atomic.LoadInt32(probes); n != 0 {
		t.Errorf("legacy probes = %d, want 0 (nothing can be published at a dangling symlink)", n)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("symlink target was created: %v", err)
	}
	if got, err := os.Readlink(path); err != nil || got != target {
		t.Errorf("symlink = %q, %v; want it untouched (-> %s)", got, err, target)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Errorf("config dir holds %v (%v); want only the symlink, no staging file", entries, err)
	}
}

// TestCreateUserConfigYamlDarwinPublishFailureIsConsentUnknown: once bd has
// read the legacy config, a failure to publish the new file must not drop a
// legacy opt-out silently: the error wraps ErrUserConfigConsentUnknown, so
// the run keeps metrics off, and never ErrUserConfigExists, which the
// bootstrap would swallow. Here the config dir disappears between the probe
// and the publish, which fails the link and the exclusive-create fallback.
func TestCreateUserConfigYamlDarwinPublishFailureIsConsentUnknown(t *testing.T) {
	home, nativeDir := darwinCreateSeamForTest(t)
	writeLegacyOptOut(t, nativeDir)
	path := mkUserConfigDir(t, home)
	dir := filepath.Dir(path)
	probes := 0
	t.Cleanup(SetLegacyUserConfigStatForTest(func(name string) (fs.FileInfo, error) {
		probes++
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove config dir: %v", err)
		}
		return os.Stat(name)
	}))

	err := CreateUserConfigYaml(path, []byte("seed"))
	if !errors.Is(err, ErrUserConfigConsentUnknown) {
		t.Fatalf("CreateUserConfigYaml error = %v; want it to wrap ErrUserConfigConsentUnknown", err)
	}
	if errors.Is(err, ErrUserConfigExists) {
		t.Errorf("error %v wraps ErrUserConfigExists, which the bootstrap swallows", err)
	}
	if probes != 1 {
		t.Errorf("legacy probes = %d, want 1", probes)
	}
}

// TestUserConfigReadErrorConsentUnknownOnlyWhenNoFile: on darwin a failure to
// read the user config leaves metrics consent unknown only when the path
// names no file at all; an existing path bd cannot read is the user's own
// config and keeps the plain error. Every other GOOS gets the error back
// unchanged.
func TestUserConfigReadErrorConsentUnknownOnlyWhenNoFile(t *testing.T) {
	readErr := func(t *testing.T, path string) error {
		t.Helper()
		_, err := os.ReadFile(path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("read %s = %v; the fixture must fail with something other than not-exist", path, err)
		}
		return err
	}
	blockConfigDir := func(t *testing.T, home string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a dir"), 0o600); err != nil {
			t.Fatalf("plant blocker: %v", err)
		}
		return filepath.Join(home, ".config", "bd", "config.yaml")
	}

	t.Run("darwin: ~/.config is a file", func(t *testing.T) {
		home, _ := darwinCreateSeamForTest(t)
		path := blockConfigDir(t, home)
		if err := UserConfigReadError(path, readErr(t, path)); !errors.Is(err, ErrUserConfigConsentUnknown) {
			t.Errorf("UserConfigReadError = %v; want it to wrap ErrUserConfigConsentUnknown", err)
		}
	})

	t.Run("darwin: an existing path bd cannot read keeps the plain error", func(t *testing.T) {
		home, _ := darwinCreateSeamForTest(t)
		path := mkUserConfigDir(t, home)
		if err := os.Mkdir(path, 0o755); err != nil { // a directory: stat works, read fails
			t.Fatalf("mkdir at config path: %v", err)
		}
		orig := readErr(t, path)
		if err := UserConfigReadError(path, orig); err != orig {
			t.Errorf("UserConfigReadError = %v; want the original error %v", err, orig)
		}
	})

	t.Run("non-darwin: unchanged", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Cleanup(SetUserConfigPlatformForTest("linux", func() (string, error) { return filepath.Join(home, ".config"), nil }))
		path := blockConfigDir(t, home)
		orig := readErr(t, path)
		if err := UserConfigReadError(path, orig); err != orig {
			t.Errorf("UserConfigReadError = %v; want the original error %v", err, orig)
		}
	})
}
