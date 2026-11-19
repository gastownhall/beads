package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateUserConfigYamlDarwinUnreadableLegacyFailsClosed is AC for a
// legacy probe that fails with anything but not-exist (a TCC denial leaves
// EPERM/EACCES): the created file holds metrics.disabled: true.
func TestCreateUserConfigYamlDarwinUnreadableLegacyFailsClosed(t *testing.T) {
	home, nativeDir := darwinSeamForTest(t)
	probeCountingSeam(t)
	path := seedUserConfigDir(t, home)

	legacyPath := filepath.Join(nativeDir, "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("metrics:\n  disabled: false\n"), 0o600); err != nil {
		t.Fatalf("plant legacy config: %v", err)
	}
	// A permission-rejected probe: the stat itself fails with EACCES (the
	// stand-in Application Support directory is unreadable).
	if err := os.Chmod(filepath.Dir(legacyPath), 0o000); err != nil {
		t.Fatalf("chmod legacy dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(legacyPath), 0o755) })

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
		t.Error("unreadable legacy config must fail closed with metrics off")
	}
}
