package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the entire package away from the developer's real profile,
// including tests that do not need their own isolated assertions.
func TestMain(m *testing.M) {
	profileRoot, err := os.MkdirTemp("", "beads-metrics-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "metrics tests: create isolated profile: %v\n", err)
		os.Exit(1)
	}

	for key, value := range isolatedUserProfileEnv(profileRoot) {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintf(os.Stderr, "metrics tests: set %s: %v\n", key, err)
			_ = os.RemoveAll(profileRoot)
			os.Exit(1)
		}
	}

	code := m.Run()
	if err := os.RemoveAll(profileRoot); err != nil {
		fmt.Fprintf(os.Stderr, "metrics tests: remove isolated profile %s: %v\n", profileRoot, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// isolateUserProfile redirects every platform-specific home and config lookup
// used by the metrics package into one test-owned directory.
func isolateUserProfile(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	for key, value := range isolatedUserProfileEnv(home) {
		t.Setenv(key, value)
	}
	return home
}

func isolatedUserProfileEnv(home string) map[string]string {
	env := map[string]string{
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
		"BEADS_DIR":       "",
	}
	if volume := filepath.VolumeName(home); volume != "" {
		env["HOMEDRIVE"] = volume
		env["HOMEPATH"] = strings.TrimPrefix(home, volume)
	}
	return env
}

func TestPackageUserProfileIsIsolated(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve test home: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(home), "beads-metrics-tests-") {
		t.Fatalf("test home %q is not the package-owned profile", home)
	}

	for key, want := range isolatedUserProfileEnv(home) {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("resolve test config dir: %v", err)
	}
	rel, err := filepath.Rel(home, configDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("test config dir %q escapes package profile %q", configDir, home)
	}
}
