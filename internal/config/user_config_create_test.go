package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// probeCountingSeam wraps the legacy-probe seams and returns the probe
// counter. Only in-package tests use it: it counts every legacy-file probe
// (stat and read), so tests can assert both "probed once" and "probed zero
// times" (GH#8113's probe bound: creation moments only, never launches).
func probeCountingSeam(t *testing.T) *int32 {
	t.Helper()
	count := new(int32)
	prevStat, prevRead := probeLegacyStat, probeLegacyReadFile
	probeLegacyStat = func(name string) (fs.FileInfo, error) {
		atomic.AddInt32(count, 1)
		return os.Stat(name)
	}
	probeLegacyReadFile = func(name string) ([]byte, error) {
		atomic.AddInt32(count, 1)
		return os.ReadFile(name)
	}
	t.Cleanup(func() { probeLegacyStat, probeLegacyReadFile = prevStat, prevRead })
	return count
}

func seedUserConfigDir(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".config", "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	return path
}

// TestCreateUserConfigYamlDarwinCarriesOverLegacyVerbatim is the GH#8113
// carry-over contract: the first creation of ~/.config/bd/config.yaml probes
// the legacy macOS file exactly once and publishes its bytes verbatim with
// mode 0600, and the next read never probes again.
func TestCreateUserConfigYamlDarwinCarriesOverLegacyVerbatim(t *testing.T) {
	home, nativeDir := darwinSeamForTest(t)
	probes := probeCountingSeam(t)
	path := seedUserConfigDir(t, home)

	legacy := "metrics:\n  disabled: true\nnode_id: n1\n"
	legacyPath := filepath.Join(nativeDir, "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("plant legacy config: %v", err)
	}

	seed := []byte("metrics:\n  disabled: false\n")
	if err := CreateUserConfigYaml(path, seed); err != nil {
		t.Fatalf("CreateUserConfigYaml: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	if string(got) != legacy {
		t.Errorf("created config = %q, want the legacy bytes %q carried verbatim", got, legacy)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("created config mode = %o, want 600", perm)
	}
	if n := atomic.LoadInt32(probes); n != 2 {
		t.Errorf("legacy accesses after creation = %d, want 2 (the one-time probe: stat + read)", n)
	}

	// The next command reads the new file; the legacy path is never probed
	// again.
	if !MetricsDisabledByUserConfig() {
		t.Error("MetricsDisabledByUserConfig() = false; carried consent lost")
	}
	if got := GetUserYamlConfig("node_id"); got != "n1" {
		t.Errorf("GetUserYamlConfig(node_id) = %q, want carried %q", got, "n1")
	}
	if n := atomic.LoadInt32(probes); n != 2 {
		t.Errorf("legacy accesses after the next command = %d, want 2 (zero new probes after the carry-over run)", n)
	}
}

// TestCreateUserConfigYamlDarwinProbeBounds covers the carry-over's edge
// inputs: no legacy file, a legacy directory (never opened), a dangling
// legacy symlink (treated as absent), and a legacy file over the size cap
// (fail closed). Each probes exactly once, at creation only.
func TestCreateUserConfigYamlDarwinProbeBounds(t *testing.T) {
	t.Run("no legacy file: seed bytes, one probe", func(t *testing.T) {
		home, nativeDir := darwinSeamForTest(t)
		probes := probeCountingSeam(t)
		path := seedUserConfigDir(t, home)
		seed := []byte("metrics:\n  disabled: false\n")

		if err := CreateUserConfigYaml(path, seed); err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != string(seed) {
			t.Errorf("created config = %q, want seed %q", got, seed)
		}
		if n := atomic.LoadInt32(probes); n != 1 {
			t.Errorf("legacy accesses = %d, want 1 (the single stat of the one-time probe)", n)
		}
		_ = nativeDir
	})

	t.Run("legacy path is a directory: seed bytes, never opened", func(t *testing.T) {
		home, nativeDir := darwinSeamForTest(t)
		probeCountingSeam(t)
		path := seedUserConfigDir(t, home)
		legacyDir := filepath.Join(nativeDir, "bd", "config.yaml")
		if err := os.MkdirAll(legacyDir, 0o755); err != nil {
			t.Fatalf("mkdir legacy dir: %v", err)
		}
		seed := []byte("metrics:\n  disabled: false\n")

		if err := CreateUserConfigYaml(path, seed); err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != string(seed) {
			t.Errorf("created config = %q, want seed %q (a non-regular legacy file must not be read)", got, seed)
		}
	})

	t.Run("dangling legacy symlink: treated as absent", func(t *testing.T) {
		home, nativeDir := darwinSeamForTest(t)
		probeCountingSeam(t)
		path := seedUserConfigDir(t, home)
		if err := os.MkdirAll(filepath.Join(nativeDir, "bd"), 0o755); err != nil {
			t.Fatalf("mkdir legacy dir: %v", err)
		}
		legacyPath := filepath.Join(nativeDir, "bd", "config.yaml")
		if err := os.Symlink(filepath.Join(nativeDir, "nowhere"), legacyPath); err != nil {
			t.Fatalf("plant dangling legacy symlink: %v", err)
		}
		seed := []byte("metrics:\n  disabled: false\n")

		if err := CreateUserConfigYaml(path, seed); err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != string(seed) {
			t.Errorf("created config = %q, want seed %q", got, seed)
		}
	})

	t.Run("legacy file over the size cap: fail closed", func(t *testing.T) {
		home, nativeDir := darwinSeamForTest(t)
		probeCountingSeam(t)
		path := seedUserConfigDir(t, home)
		legacyPath := filepath.Join(nativeDir, "bd", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
			t.Fatalf("mkdir legacy dir: %v", err)
		}
		big := make([]byte, legacyUserConfigMaxBytes+1)
		for i := range big {
			big[i] = '#'
		}
		if err := os.WriteFile(legacyPath, big, 0o600); err != nil {
			t.Fatalf("plant oversized legacy config: %v", err)
		}

		if err := CreateUserConfigYaml(path, []byte("metrics:\n  disabled: false\n")); err != nil {
			t.Fatalf("CreateUserConfigYaml: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if want := string(userConfigFailClosedSeed(legacyPath)); string(got) != want {
			t.Errorf("created config = %q, want fail-closed seed %q", got, want)
		}
		if !MetricsDisabledByUserConfig() {
			t.Error("oversized legacy config must fail closed with metrics off")
		}
	})
}

// TestCreateUserConfigYamlAlreadyExistsIsTyped: a pre-existing target makes
// the create report ErrUserConfigAlreadyExists instead of racing, on both
// platforms (the caller re-reads; it never retries in a loop).
func TestCreateUserConfigYamlAlreadyExistsIsTyped(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("APPDATA", "")
			platformSeamForTest(t, goos, filepath.Join(home, "AppData", "Roaming"), nil)
			dir := filepath.Join(home, ".config", "bd")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte("existing: true\n"), 0o600); err != nil {
				t.Fatalf("plant existing config: %v", err)
			}

			err := CreateUserConfigYaml(path, []byte("seed"))
			if !errors.Is(err, ErrUserConfigAlreadyExists) {
				t.Fatalf("CreateUserConfigYaml error = %v, want ErrUserConfigAlreadyExists", err)
			}
			got, rerr := os.ReadFile(path)
			if rerr != nil || string(got) != "existing: true\n" {
				t.Errorf("existing file was disturbed: %q, %v", got, rerr)
			}
		})
	}
}
