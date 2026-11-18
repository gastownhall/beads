package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
)

// isolateMetricsBootstrap points every user-config root at a fresh home,
// clears the metrics env overrides, and installs goos as the bootstrap
// platform with a counting legacy reader. Not parallel: it swaps
// package-level seams.
func isolateMetricsBootstrap(t *testing.T, goos string) (home string, probes *[]string) {
	t.Helper()
	return isolateMetricsBootstrapWithRead(t, goos, func(path string) ([]byte, error) {
		return os.ReadFile(path) //nolint:gosec // test-owned temp path
	})
}

// isolateMetricsBootstrapWithRead is isolateMetricsBootstrap with the legacy
// read answered by read.
func isolateMetricsBootstrapWithRead(t *testing.T, goos string, read func(path string) ([]byte, error)) (home string, probes *[]string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	for _, key := range []string{metrics.EnvDisableMetrics, metrics.EnvDoNotTrack, metrics.EnvEndpoint} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
	var recorded []string
	t.Cleanup(metrics.SetLegacyDarwinCarryOverForTest(goos, func(path string) ([]byte, error) {
		recorded = append(recorded, path)
		return read(path)
	}))
	t.Cleanup(func() { _, _ = metrics.Init(Version, false, "") })
	return home, &recorded
}

func plantLegacyDarwinMetricsOptOut(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, "Library", "Application Support", "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("metrics:\n  disabled: true\n"), 0o600); err != nil {
		t.Fatalf("plant: %v", err)
	}
}

// TestMetricsBootstrapCarriedOptOutStopsSendingThisRun drives the
// PersistentPreRun metrics step: a Mac whose only metrics opt-out lives in the
// pre-GH#8113 Application Support config must not send on the first run after
// upgrade.
func TestMetricsBootstrapCarriedOptOutStopsSendingThisRun(t *testing.T) {
	home, probes := isolateMetricsBootstrap(t, "darwin")
	plantLegacyDarwinMetricsOptOut(t, home)
	if !resolveMetricsEnabled() {
		t.Fatal("precondition: metrics should resolve enabled before the bootstrap")
	}

	bootstrapAndInitMetrics("ready")

	if len(*probes) != 1 {
		t.Fatalf("legacy probes = %q, want exactly one", *probes)
	}
	if metrics.Enabled() {
		t.Fatal("metrics.Enabled() = true after carrying a metrics.disabled: true opt-out")
	}
	if dir, err := metrics.DataDir(); err == nil {
		if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
			t.Fatalf("metrics data dir %s was created on an opted-out run: %v", dir, statErr)
		}
	}

	bootstrapAndInitMetrics("ready")
	if len(*probes) != 1 {
		t.Fatalf("second run probed the legacy config again: %q", *probes)
	}
}

// TestMetricsBootstrapNeverProbesWhenDisabledOrOffDarwin covers the gates in
// front of the carry-over: an env opt-out skips the bootstrap entirely, and a
// non-darwin platform never looks at Application Support.
func TestMetricsBootstrapNeverProbesWhenDisabledOrOffDarwin(t *testing.T) {
	t.Run("env opt-out", func(t *testing.T) {
		home, probes := isolateMetricsBootstrap(t, "darwin")
		plantLegacyDarwinMetricsOptOut(t, home)
		t.Setenv(metrics.EnvDisableMetrics, "1")
		bootstrapAndInitMetrics("ready")
		if len(*probes) != 0 {
			t.Fatalf("legacy probes = %q, want none under %s", *probes, metrics.EnvDisableMetrics)
		}
		if metrics.Enabled() {
			t.Fatal("metrics enabled under an env opt-out")
		}
	})
	t.Run("send-metrics flusher", func(t *testing.T) {
		home, probes := isolateMetricsBootstrap(t, "darwin")
		plantLegacyDarwinMetricsOptOut(t, home)
		bootstrapMetricsUserConfig(metrics.SendMetricsSubcommand)
		if len(*probes) != 0 {
			t.Fatalf("legacy probes = %q, want none for the flusher", *probes)
		}
	})
	t.Run("linux", func(t *testing.T) {
		home, probes := isolateMetricsBootstrap(t, "linux")
		plantLegacyDarwinMetricsOptOut(t, home)
		// Keep the run from enabling a real collector: the bootstrap still
		// runs (metrics resolve enabled), Init is then told to stay off.
		bootstrapMetricsUserConfig("ready")
		if len(*probes) != 0 {
			t.Fatalf("legacy probes = %q, want none off darwin", *probes)
		}
	})
}

// TestMetricsBootstrapUnreadableLegacyConfigFailsClosed: a legacy config that
// exists but cannot be read (EPERM after a macOS privacy-prompt denial) or
// parsed leaves metrics off on this very run, with one stderr line naming the
// file and the way back.
func TestMetricsBootstrapUnreadableLegacyConfigFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		body string // planted legacy content; "" plants nothing
		read func(path string) ([]byte, error)
	}{
		"EPERM": {read: func(path string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
		}},
		"malformed yaml": {body: "metrics:\n  disabled: [unclosed\n"},
	} {
		t.Run(name, func(t *testing.T) {
			read := tc.read
			if read == nil {
				read = func(path string) ([]byte, error) {
					return os.ReadFile(path) //nolint:gosec // test-owned temp path
				}
			}
			home, probes := isolateMetricsBootstrapWithRead(t, "darwin", read)
			legacy := filepath.Join(home, "Library", "Application Support", "bd", "config.yaml")
			if tc.body != "" {
				if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(legacy, []byte(tc.body), 0o600); err != nil {
					t.Fatalf("plant: %v", err)
				}
			}

			stderr := captureStderrDuring(t, func() { bootstrapAndInitMetrics("ready") })

			if len(*probes) != 1 {
				t.Fatalf("legacy probes = %q, want exactly one", *probes)
			}
			if metrics.Enabled() {
				t.Fatal("metrics.Enabled() = true after an unreadable legacy config; want fail closed")
			}
			if strings.Count(stderr, "\n") != 1 ||
				!strings.Contains(stderr, "metrics were left off") ||
				!strings.Contains(stderr, legacy) ||
				!strings.Contains(stderr, "bd metrics on") {
				t.Fatalf("stderr = %q, want one line naming %s and bd metrics on", stderr, legacy)
			}

			stderr = captureStderrDuring(t, func() { bootstrapAndInitMetrics("ready") })
			if len(*probes) != 1 || stderr != "" || metrics.Enabled() {
				t.Fatalf("second run: probes=%q stderr=%q enabled=%v; want no probe, no notice, still off",
					*probes, stderr, metrics.Enabled())
			}
		})
	}
}

// TestMetricsBootstrapSkipsShellCompletion: a Tab press never bootstraps the
// user config, so it neither probes the legacy file nor writes anything.
func TestMetricsBootstrapSkipsShellCompletion(t *testing.T) {
	for _, name := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		t.Run(name, func(t *testing.T) {
			home, probes := isolateMetricsBootstrap(t, "darwin")
			plantLegacyDarwinMetricsOptOut(t, home)
			bootstrapMetricsUserConfig(name)
			if len(*probes) != 0 {
				t.Fatalf("legacy probes = %q, want none for %s", *probes, name)
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "bd", "config.yaml")); !os.IsNotExist(err) {
				t.Fatalf("%s created the user config: %v", name, err)
			}
		})
	}
}
