package metrics

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// useLegacyCarryOver installs goos as the bootstrap platform and records every
// read of the legacy darwin user config. It returns the recorded paths.
func useLegacyCarryOver(t *testing.T, goos string) *[]string {
	t.Helper()
	var probes []string
	restore := SetLegacyDarwinCarryOverForTest(goos, func(path string) ([]byte, error) {
		probes = append(probes, path)
		return config.ReadLegacyDarwinUserConfigYaml(path)
	})
	t.Cleanup(restore)
	return &probes
}

// useLegacyCarryOverRead is useLegacyCarryOver with an injected read result.
func useLegacyCarryOverRead(t *testing.T, read func(path string) ([]byte, error)) *[]string {
	t.Helper()
	var probes []string
	restore := SetLegacyDarwinCarryOverForTest("darwin", func(path string) ([]byte, error) {
		probes = append(probes, path)
		return read(path)
	})
	t.Cleanup(restore)
	return &probes
}

// captureCarryOverNotice collects the fail-closed notice.
func captureCarryOverNotice(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := legacyCarryOverNotice
	legacyCarryOverNotice = &buf
	t.Cleanup(func() { legacyCarryOverNotice = prev })
	return &buf
}

// assertFailedClosed checks the user config holds the fail-closed result
// (metrics.disabled true, the default endpoint, nothing else carried) and that
// exactly one notice line names the legacy path and the way back.
func assertFailedClosed(t *testing.T, home string, notice *bytes.Buffer) {
	t.Helper()
	parsed := parseUserConfig(t, userConfigPath(home))
	if got := metricsLeaf(t, parsed, "disabled"); got != true {
		t.Errorf("metrics.disabled = %v, want true (fail closed)", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != DefaultEndpoint {
		t.Errorf("metrics.endpoint = %v, want the default (nothing carried)", got)
	}
	if m, _ := parsed["metrics"].(map[string]interface{}); m != nil {
		if _, ok := m["notice_shown"]; ok {
			t.Errorf("metrics.notice_shown written on a fail-closed carry-over: %#v", m)
		}
	}
	if !config.MetricsDisabledByUserConfig() {
		t.Error("MetricsDisabledByUserConfig() = false after failing closed")
	}
	got := notice.String()
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("notice = %q, want exactly one line", got)
	}
	for _, want := range []string{"metrics were left off", legacyDarwinConfigPath(home), "bd metrics on"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q does not mention %q", got, want)
		}
	}
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

// userConfigWrite is the user config as another bd reads it just before and
// just after one write the bootstrap makes ("" while there is no file).
type userConfigWrite struct{ before, after string }

// recordUserConfigWrites records every write the bootstrap makes to the user
// config (publishUserConfigHook).
func recordUserConfigWrites(t *testing.T) *[]userConfigWrite {
	t.Helper()
	var writes []userConfigWrite
	prev := publishUserConfigHook
	publishUserConfigHook = func(path string, publish func() error) error {
		before := readUserConfigIfAny(t, path)
		err := publish()
		writes = append(writes, userConfigWrite{before: before, after: readUserConfigIfAny(t, path)})
		return err
	}
	t.Cleanup(func() { publishUserConfigHook = prev })
	return &writes
}

func readUserConfigIfAny(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// seedUserConfig writes body as the user config bd reads under home.
func seedUserConfig(t *testing.T, home, body string) string {
	t.Helper()
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
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
	for _, goos := range []string{"darwin", "ios"} {
		t.Run(goos, func(t *testing.T) { testDisabledOptOutCarried(t, goos) })
	}
}

func testDisabledOptOutCarried(t *testing.T, goos string) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, goos)
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
	// node_id is a per-machine setting bd config set wrote there, so it is
	// carried; actor is not one, so bd doctor lists it instead.
	if got := parsed["node_id"]; got != "legacy-node" {
		t.Errorf("node_id = %#v, want the carried per-machine setting %q", got, "legacy-node")
	}
	if _, ok := parsed["actor"]; ok {
		t.Errorf("carry-over copied actor, which is not a per-machine setting: %#v", parsed)
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

// The per-machine settings bd config set wrote to the legacy config (node_id,
// mail.delegate, dolt.remotesapi-port) are carried with the metrics choice, in
// either YAML spelling. Other settings are left for bd doctor to list.
func TestLegacyDarwinCarryOver_MachineSettingsCarried(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "actor: legacy-actor\n"+
		"node_id: mac-mini\n"+
		"mail:\n  delegate: gt mail send\n"+
		"dolt.remotesapi-port: 8085\n"+
		"prime:\n  disabled: true\n"+
		"metrics:\n  disabled: true\n")

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	for key, want := range map[string]string{
		"node_id":              "mac-mini",
		"mail.delegate":        "gt mail send",
		"dolt.remotesapi-port": "8085",
		"metrics.disabled":     "true",
		"actor":                "",
		"prime.disabled":       "",
	} {
		if got := config.GetUserYamlConfig(key); got != want {
			t.Errorf("GetUserYamlConfig(%q) = %q, want %q", key, got, want)
		}
	}

	// Carried once: a later run neither probes nor rewrites the user config.
	first, err := os.ReadFile(userConfigPath(home))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("second EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	if again, err := os.ReadFile(userConfigPath(home)); err != nil || string(again) != string(first) {
		t.Fatalf("second run rewrote the user config: %v %q -> %q", err, first, again)
	}
}

// The carry-over fills in only the per-machine settings that applied before
// the move and that an existing user config does not settle itself. node_id is
// read through the merged config, where the legacy value applied under any
// user config that did not set it, so it is carried into one. mail.delegate
// and dolt.remotesapi-port were read from ~/.config/bd/config.yaml alone
// whenever that file existed, so the legacy values never applied and stay
// out. A value the user config sets, in either spelling, and a key bd config
// unset commented out are the user's choice and are left alone.
func TestLegacyDarwinCarryOver_MachineSettingsNeverReplaceTheUserConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		seed  string
		unset string // a key bd config unset removes from seed first
		want  map[string]string
	}{
		"set in either spelling": {
			seed: "node_id: chosen-since\nmail.delegate: current-mail\n",
			want: map[string]string{"node_id": "chosen-since", "mail.delegate": "current-mail", "dolt.remotesapi-port": ""},
		},
		"absent from an existing user config": {
			seed: "actor: me\n",
			want: map[string]string{"node_id": "mac-mini", "mail.delegate": "", "dolt.remotesapi-port": "", "actor": "me"},
		},
		"unset with bd config unset": {
			seed: "node_id: \"A\"\n", unset: "node_id",
			want: map[string]string{"node_id": "", "mail.delegate": "", "dolt.remotesapi-port": ""},
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := setupUserConfigHome(t)
			probes := useLegacyCarryOver(t, "darwin")
			plantLegacyDarwinConfig(t, home, "node_id: mac-mini\nmail:\n  delegate: legacy-mail\ndolt:\n  remotesapi-port: 8085\n")
			path := seedUserConfig(t, home, tc.seed)
			if tc.unset != "" {
				if changed, err := config.UnsetUserYamlConfig(tc.unset); err != nil || !changed {
					t.Fatalf("UnsetUserYamlConfig(%q) = %v, %v; want the key commented out", tc.unset, changed, err)
				}
			}

			if err := EnsureUserConfigDefaults(); err != nil {
				t.Fatalf("EnsureUserConfigDefaults: %v", err)
			}
			assertLegacyProbes(t, probes, home, 1)
			for key, want := range tc.want {
				if got := config.GetUserYamlConfig(key); got != want {
					t.Errorf("GetUserYamlConfig(%q) = %q, want %q", key, got, want)
				}
			}
			if !config.UserConfigSetsBoolKey("metrics.disabled") {
				t.Error("metrics.disabled was not written")
			}
			if line := "# " + tc.unset + ":"; tc.unset != "" && !strings.Contains(readUserConfigIfAny(t, path), line) {
				t.Errorf("the %q line bd config unset left is gone:\n%s", line, readUserConfigIfAny(t, path))
			}
		})
	}
}

// The carried per-machine settings are published in the same write as the
// metrics keys, into a new user config and into an existing one. Another bd
// that finds metrics.disabled no longer waits for the lock (with metrics off
// it skips the bootstrap altogether), so it goes on with whatever node_id the
// file holds: no write may show metrics.disabled without the settings carried
// with it.
func TestLegacyDarwinCarryOver_MachineSettingsPublishedWithTheMetricsKeys(t *testing.T) {
	const legacy = "node_id: mac-mini\nmail:\n  delegate: gt mail send\ndolt:\n  remotesapi-port: 8085\nmetrics:\n  disabled: true\n"
	for name, tc := range map[string]struct {
		seed string // the user config before the bootstrap; "" for none
		want map[string]string
	}{
		"new user config": {want: map[string]string{
			"metrics.disabled": "true", "node_id": "mac-mini",
			"mail.delegate": "gt mail send", "dolt.remotesapi-port": "8085",
		}},
		"existing user config": {seed: "actor: me\n", want: map[string]string{
			"metrics.disabled": "true", "node_id": "mac-mini", "actor": "me",
			"mail.delegate": "", "dolt.remotesapi-port": "",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			home := setupUserConfigHome(t)
			probes := useLegacyCarryOver(t, "darwin")
			plantLegacyDarwinConfig(t, home, legacy)
			path := userConfigPath(home)
			if tc.seed != "" {
				seedUserConfig(t, home, tc.seed)
			}
			writes := recordUserConfigWrites(t)

			if err := EnsureUserConfigDefaults(); err != nil {
				t.Fatalf("EnsureUserConfigDefaults: %v", err)
			}
			assertLegacyProbes(t, probes, home, 1)
			if len(*writes) != 1 {
				t.Fatalf("the bootstrap wrote the user config %d times (%q), want once", len(*writes), *writes)
			}
			w := (*writes)[0]
			if w.before != tc.seed {
				t.Errorf("just before its write the user config held %q, want the untouched %q", w.before, tc.seed)
			}
			for key, want := range tc.want {
				if got, _, _ := config.YamlValueFromBytes([]byte(w.after), key); strings.TrimSpace(got) != want {
					t.Errorf("the published user config sets %s = %q, want %q:\n%s", key, got, want, w.after)
				}
			}
			if final := readUserConfigIfAny(t, path); final != w.after {
				t.Errorf("the user config changed after its one write: %q -> %q", w.after, final)
			}
		})
	}
}

// An empty per-machine setting, or one bd refuses to write (a port out of
// range, which bd's readers ignored too), is not carried.
func TestLegacyDarwinCarryOver_MachineSettingsSkipValuesBdRefuses(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "node_id: \"  \"\nmail:\n  delegate:\ndolt:\n  remotesapi-port: 70000\n")

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	assertDefaultMetricsConfig(t, home)
}

// A writer that takes no lock (bd config set) can create the user config
// while the bootstrap reads the legacy file. The file is still new to the
// bootstrap, so the settings carried only into a new user config go in too,
// in one write with the metrics keys. One the file cannot take (mail.delegate
// under a mail key that holds a value) is returned as an error, the others
// are still written, and the next run neither probes nor fails.
func TestLegacyDarwinCarryOver_MachineSettingWriteFailureNeverRepeatsTheProbe(t *testing.T) {
	home := setupUserConfigHome(t)
	plantLegacyDarwinConfig(t, home, "node_id: mac-mini\nmail:\n  delegate: legacy-mail\ndolt:\n  remotesapi-port: 8085\nmetrics:\n  disabled: true\n")
	path := userConfigPath(home)
	probes := useLegacyCarryOverRead(t, func(legacy string) ([]byte, error) {
		// bd config set mail off, landing between the bootstrap's read of
		// the user config and its write.
		if err := os.WriteFile(path, []byte("mail: off\n"), 0o600); err != nil {
			return nil, err
		}
		return config.ReadLegacyDarwinUserConfigYaml(legacy)
	})

	err := EnsureUserConfigDefaults()
	if err == nil || !strings.Contains(err.Error(), "mail.delegate") {
		t.Fatalf("EnsureUserConfigDefaults = %v, want the mail.delegate write failure", err)
	}
	assertLegacyProbes(t, probes, home, 1)
	if !config.MetricsDisabledByUserConfig() {
		t.Error("the carried opt-out is not on disk")
	}
	for key, want := range map[string]string{"node_id": "mac-mini", "dolt.remotesapi-port": "8085"} {
		if got := config.GetUserYamlConfig(key); got != want {
			t.Errorf("%s = %q, want %q written despite the mail.delegate failure", key, got, want)
		}
	}
	if got := config.GetUserYamlConfig("mail"); got != "off" {
		t.Errorf("mail = %q, want the user's own value left alone", got)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("second EnsureUserConfigDefaults: %v", err)
	}
	assertLegacyProbes(t, probes, home, 1)
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

// Only a missing legacy file means "today's defaults". One that exists but
// cannot be read or parsed fails closed (maintainer ruling on GH#8113): a
// metrics opt-out bd can no longer see must not turn into sending.
func TestLegacyDarwinCarryOver_UnusableLegacyFileFailsClosed(t *testing.T) {
	cases := map[string]struct {
		plant func(t *testing.T, home string)
		read  func(path string) ([]byte, error) // nil: the real reader
	}{
		"malformed yaml": {plant: func(t *testing.T, home string) {
			plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: [unclosed\n")
		}},
		"not a mapping": {plant: func(t *testing.T, home string) {
			plantLegacyDarwinConfig(t, home, "- just\n- a list\n")
		}},
		"directory": {plant: func(t *testing.T, home string) {
			if err := os.MkdirAll(legacyDarwinConfigPath(home), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}},
		"over the size cap": {plant: func(t *testing.T, home string) {
			plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: false\n"+strings.Repeat("#", config.LegacyDarwinUserConfigMaxBytes))
		}},
		"EPERM after a privacy-prompt denial": {read: func(path string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
		}},
		"I/O error": {read: func(path string) ([]byte, error) {
			return nil, &fs.PathError{Op: "read", Path: path, Err: fmt.Errorf("input/output error")}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home := setupUserConfigHome(t)
			notice := captureCarryOverNotice(t)
			var probes *[]string
			if tc.read != nil {
				probes = useLegacyCarryOverRead(t, tc.read)
			} else {
				probes = useLegacyCarryOver(t, "darwin")
				tc.plant(t, home)
			}

			if err := EnsureUserConfigDefaults(); err != nil {
				t.Fatalf("EnsureUserConfigDefaults: %v", err)
			}
			assertLegacyProbes(t, probes, home, 1)
			assertFailedClosed(t, home, notice)

			// The opt-out is now on disk: later runs neither probe nor
			// repeat the notice.
			notice.Reset()
			if err := EnsureUserConfigDefaults(); err != nil {
				t.Fatalf("second EnsureUserConfigDefaults: %v", err)
			}
			assertLegacyProbes(t, probes, home, 1)
			if notice.Len() != 0 {
				t.Fatalf("second run repeated the notice: %q", notice.String())
			}
		})
	}
}

// An existing user config that lacks metrics.disabled fails closed the same way.
func TestLegacyDarwinCarryOver_ExistingConfigFailsClosed(t *testing.T) {
	home := setupUserConfigHome(t)
	notice := captureCarryOverNotice(t)
	probes := useLegacyCarryOverRead(t, func(path string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	})
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
	assertFailedClosed(t, home, notice)
	if parsed := parseUserConfig(t, path); parsed["actor"] != "from-config-set" {
		t.Errorf("existing key clobbered: %#v", parsed)
	}
}

func TestLegacyDarwinCarryOver_NeverProbesOffDarwin(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			home := setupUserConfigHome(t)
			probes := useLegacyCarryOver(t, goos)
			plantLegacyDarwinConfig(t, home, "node_id: legacy-node\nmetrics:\n  disabled: true\n")

			if err := EnsureUserConfigDefaults(); err != nil {
				t.Fatalf("EnsureUserConfigDefaults: %v", err)
			}
			assertLegacyProbes(t, probes, home, 0)
			assertDefaultMetricsConfig(t, home)
			// Off macOS there is nothing to probe, so no lock either: the
			// hard-link publish alone keeps creation atomic.
			if _, err := os.Lstat(userConfigPath(home) + ".lock"); !os.IsNotExist(err) {
				t.Errorf("lock file created off macOS: %v", err)
			}
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

// The probe exists to carry a consent choice, so it runs only when
// metrics.disabled is missing. A user file that already records that choice
// only gains the default endpoint, and the legacy file is never touched.
func TestLegacyDarwinCarryOver_DisabledAlreadySetNeverProbes(t *testing.T) {
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
	assertLegacyProbes(t, probes, home, 0)
	parsed := parseUserConfig(t, path)
	if got := metricsLeaf(t, parsed, "disabled"); got != false {
		t.Errorf("metrics.disabled = %v, want the user's existing false", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != DefaultEndpoint {
		t.Errorf("metrics.endpoint = %v, want the default endpoint", got)
	}
}

// A user config bd cannot rewrite never triggers the probe: the carried value
// could not be kept, so the next run would probe again.
func TestLegacyDarwinCarryOver_ReadOnlyConfigNeverProbes(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: true\n")
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("actor: read-only\n"), 0o400); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	requireUnwritable(t, path)

	for run := 1; run <= 2; run++ {
		if err := EnsureUserConfigDefaults(); err == nil {
			t.Fatalf("run %d: EnsureUserConfigDefaults returned nil for a read-only user config", run)
		}
	}
	assertLegacyProbes(t, probes, home, 0)
	if data, _ := os.ReadFile(path); string(data) != "actor: read-only\n" {
		t.Fatalf("read-only user config changed: %q", data)
	}
}

// requireUnwritable fails the test when path can still be opened for writing
// (for example when the suite runs as root), since the test would then not
// exercise what it claims.
func requireUnwritable(t *testing.T, path string) {
	t.Helper()
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		t.Fatalf("precondition: %s is still writable (running as root?)", path)
	}
}

// Concurrent first runs serialize on the lock: the legacy file is read at most
// once, every run succeeds, and a reader only ever sees no user config or the
// complete one, the carried per-machine settings included: the file is
// published once, by hard link of a complete temp file.
func TestLegacyDarwinCarryOver_ConcurrentFirstRunsProbeOnce(t *testing.T) {
	home := setupUserConfigHome(t)
	plantLegacyDarwinConfig(t, home, "node_id: mac-mini\nmail:\n  delegate: gt mail send\nmetrics:\n  disabled: true\n")
	var mu sync.Mutex
	probes := 0
	t.Cleanup(SetLegacyDarwinCarryOverForTest("darwin", func(path string) ([]byte, error) {
		mu.Lock()
		probes++
		mu.Unlock()
		// Hold the lock long enough for the other runs to queue on it.
		time.Sleep(20 * time.Millisecond)
		return config.ReadLegacyDarwinUserConfigYaml(path)
	}))
	path := userConfigPath(home)

	const runs = 8
	start := make(chan struct{})
	stop := make(chan struct{})
	seen := map[string]bool{} // every user config the reader saw; only the reader writes it
	var readerWG, runWG sync.WaitGroup
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		<-start
		for {
			select {
			case <-stop:
				return
			default:
			}
			if data, err := os.ReadFile(path); err == nil {
				seen[string(data)] = true
			}
		}
	}()
	errs := make([]error, runs)
	for i := 0; i < runs; i++ {
		runWG.Add(1)
		go func(i int) {
			defer runWG.Done()
			<-start
			errs[i] = EnsureUserConfigDefaults()
		}(i)
	}
	close(start)
	runWG.Wait()
	close(stop)
	readerWG.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: %v", i, err)
		}
	}
	if probes != 1 {
		t.Errorf("legacy probes = %d across %d concurrent first runs, want 1", probes, runs)
	}
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	for key, want := range map[string]string{
		"metrics.disabled": "true",
		"metrics.endpoint": DefaultEndpoint,
		"node_id":          "mac-mini",
		"mail.delegate":    "gt mail send",
	} {
		if got, _, _ := config.YamlValueFromBytes(final, key); strings.TrimSpace(got) != want {
			t.Errorf("user config sets %s = %q, want %q:\n%s", key, got, want, final)
		}
	}
	for data := range seen {
		if data != string(final) {
			t.Errorf("a reader saw %q, want only the complete %q", data, final)
		}
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
