package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// useLegacyCarryOver installs goos as the bootstrap platform and records every
// read of the legacy darwin user config. It returns the recorded paths.
func useLegacyCarryOver(t *testing.T, goos string) *[]string {
	t.Helper()
	var probes []string
	restore := SetLegacyDarwinCarryOverForTest(goos, func(path string) ([]byte, error) {
		probes = append(probes, path)
		return readRegularFileCapped(path)
	})
	t.Cleanup(restore)
	return &probes
}

func legacyDarwinConfigPath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "bd", "config.yaml")
}

func plantLegacyDarwinConfig(t *testing.T, home, body string) string {
	t.Helper()
	path := legacyDarwinConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("plant legacy config: %v", err)
	}
	return path
}

func assertLegacyProbes(t *testing.T, probes *[]string, home string, want int) {
	t.Helper()
	if len(*probes) != want {
		t.Fatalf("legacy darwin config probes = %d (%q), want %d", len(*probes), *probes, want)
	}
	for _, probe := range *probes {
		if probe != legacyDarwinConfigPath(home) {
			t.Fatalf("probed %q, want only %q", probe, legacyDarwinConfigPath(home))
		}
	}
}

func assertDefaultMetricsConfig(t *testing.T, home string) {
	t.Helper()
	data, err := os.ReadFile(userConfigPath(home))
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	want := "metrics:\n  disabled: false\n  endpoint: " + DefaultEndpoint + "\n"
	if string(data) != want {
		t.Fatalf("user config = %q, want the historical defaults %q", data, want)
	}
}

func TestLegacyDarwinCarryOver_DisabledOptOutCarried(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home,
		"actor: legacy-actor\nnode_id: legacy-node\nmetrics:\n  disabled: true\n  notice_shown: true\n")

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	parsed := parseUserConfig(t, userConfigPath(home))
	if got := metricsLeaf(t, parsed, "disabled"); got != true {
		t.Errorf("metrics.disabled = %v, want true (carried)", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != DefaultEndpoint {
		t.Errorf("metrics.endpoint = %v, want default", got)
	}
	if got := metricsLeaf(t, parsed, "notice_shown"); got != true {
		t.Errorf("metrics.notice_shown = %v, want true (carried)", got)
	}
	for _, key := range []string{"actor", "node_id"} {
		if _, ok := parsed[key]; ok {
			t.Errorf("carry-over copied non-metrics key %q", key)
		}
	}
	// The value main.go re-resolves before metrics.Init: a carried opt-out
	// must disable sending on this very run.
	if !config.MetricsDisabledByUserConfig() {
		t.Fatal("MetricsDisabledByUserConfig() = false after carrying metrics.disabled: true")
	}
}

func TestLegacyDarwinCarryOver_CustomEndpointCarried(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	const custom = "https://metrics.example.com/collect?a=1: b"
	plantLegacyDarwinConfig(t, home, "metrics:\n  endpoint: \""+custom+"\"\n")

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	parsed := parseUserConfig(t, userConfigPath(home))
	if got := metricsLeaf(t, parsed, "endpoint"); got != custom {
		t.Errorf("metrics.endpoint = %v, want carried %q", got, custom)
	}
	if got := metricsLeaf(t, parsed, "disabled"); got != false {
		t.Errorf("metrics.disabled = %v, want false", got)
	}
	if got := config.UserMetricsEndpoint(); got != custom {
		t.Errorf("UserMetricsEndpoint() = %q, want %q", got, custom)
	}
}

func TestLegacyDarwinCarryOver_FlatKeysCarried(t *testing.T) {
	home := setupUserConfigHome(t)
	useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "metrics.disabled: \"yes-not-a-bool\"\nmetrics.endpoint: https://flat.example.com\n")

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	parsed := parseUserConfig(t, userConfigPath(home))
	if got := metricsLeaf(t, parsed, "disabled"); got != false {
		t.Errorf("metrics.disabled = %v, want default false for an unparseable value", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != "https://flat.example.com" {
		t.Errorf("metrics.endpoint = %v, want flat-key carry", got)
	}
}

func TestLegacyDarwinCarryOver_NoLegacyFileUsesDefaults(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	assertDefaultMetricsConfig(t, home)
}

func TestLegacyDarwinCarryOver_UnusableLegacyFileUsesDefaults(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, home string){
		"malformed yaml": func(t *testing.T, home string) {
			plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: [unclosed\n")
		},
		"not a mapping": func(t *testing.T, home string) {
			plantLegacyDarwinConfig(t, home, "- just\n- a list\n")
		},
		"directory": func(t *testing.T, home string) {
			if err := os.MkdirAll(legacyDarwinConfigPath(home), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := setupUserConfigHome(t)
			probes := useLegacyCarryOver(t, "darwin")
			plant(t, home)

			if err := EnsureUserConfigDefaults(); err != nil {
				t.Fatalf("EnsureUserConfigDefaults: %v", err)
			}
			assertLegacyProbes(t, probes, home, 1)
			assertDefaultMetricsConfig(t, home)
		})
	}
}

func TestLegacyDarwinCarryOver_NeverProbesOffDarwin(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "ios", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			home := setupUserConfigHome(t)
			probes := useLegacyCarryOver(t, goos)
			plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: true\n")

			if err := EnsureUserConfigDefaults(); err != nil {
				t.Fatalf("EnsureUserConfigDefaults: %v", err)
			}
			assertLegacyProbes(t, probes, home, 0)
			assertDefaultMetricsConfig(t, home)
		})
	}
}

func TestLegacyDarwinCarryOver_SecondRunNeverProbes(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "metrics:\n  endpoint: https://custom.example.com\n")

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("first EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	first, err := os.ReadFile(userConfigPath(home))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for run := 2; run <= 3; run++ {
		if err := EnsureUserConfigDefaults(); err != nil {
			t.Fatalf("EnsureUserConfigDefaults run %d: %v", run, err)
		}
	}
	assertLegacyProbes(t, probes, home, 1)
	again, err := os.ReadFile(userConfigPath(home))
	if err != nil || string(again) != string(first) {
		t.Fatalf("later runs rewrote the user config: %v %q -> %q", err, first, again)
	}
}

// A user config created earlier (for example by bd config set) without the
// metrics keys still triggers the one probe: the trigger is "metrics keys
// absent", not "file absent".
func TestLegacyDarwinCarryOver_ExistingConfigWithoutMetricsKeysProbesOnce(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: true\n  endpoint: https://custom.example.com\n  notice_shown: true\n")
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("actor: from-config-set\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	parsed := parseUserConfig(t, path)
	if parsed["actor"] != "from-config-set" {
		t.Errorf("existing key clobbered: %#v", parsed)
	}
	if got := metricsLeaf(t, parsed, "disabled"); got != true {
		t.Errorf("metrics.disabled = %v, want carried true", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != "https://custom.example.com" {
		t.Errorf("metrics.endpoint = %v, want carried endpoint", got)
	}
	if got := metricsLeaf(t, parsed, "notice_shown"); got != true {
		t.Errorf("metrics.notice_shown = %v, want carried true", got)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("second EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
}

// A partially configured user file only gains the missing key; a key the user
// already set is never replaced by the legacy value.
func TestLegacyDarwinCarryOver_KeepsKeysAlreadySet(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: true\n  endpoint: https://legacy.example.com\n")
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("metrics:\n  disabled: false\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	parsed := parseUserConfig(t, path)
	if got := metricsLeaf(t, parsed, "disabled"); got != false {
		t.Errorf("metrics.disabled = %v, want the user's existing false", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != "https://legacy.example.com" {
		t.Errorf("metrics.endpoint = %v, want carried legacy endpoint", got)
	}
}

func TestLegacyDarwinCarryOver_CommentedMetricsBlockNeverProbes(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: true\n")
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("# metrics:\n#   disabled: false\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 0)
	if data, _ := os.ReadFile(path); !strings.HasPrefix(string(data), "# metrics:") {
		t.Fatalf("commented user config was rewritten: %q", data)
	}
}
