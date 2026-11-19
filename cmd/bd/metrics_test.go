package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
)

// TestMetricsOnOffWritesUserConfig verifies `bd metrics on/off` persists the
// preference to the user-global config without any manual editing or env var.
func TestMetricsOnOffWritesUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("USERPROFILE", home)

	run := func(c *cobra.Command) {
		t.Helper()
		var buf bytes.Buffer
		c.SetOut(&buf)
		c.SetErr(&buf)
		if err := c.RunE(c, nil); err != nil {
			t.Fatalf("%s.RunE: %v", c.Name(), err)
		}
	}

	run(metricsOffCmd)
	if got := readUserConfigYAML(t); !strings.Contains(got, "disabled: true") {
		t.Errorf("after `metrics off`, user config = %q, want it to contain `disabled: true`", got)
	}

	run(metricsOnCmd)
	if got := readUserConfigYAML(t); !strings.Contains(got, "disabled: false") {
		t.Errorf("after `metrics on`, user config = %q, want it to contain `disabled: false`", got)
	}
}

func readUserConfigYAML(t *testing.T) string {
	t.Helper()
	path, err := config.UserConfigYamlPath()
	if err != nil {
		t.Fatalf("resolve user config path: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config %s: %v", path, err)
	}
	return string(b)
}

// TestMetricsExampleShowsBdCommandEvent verifies `bd metrics example` shows a
// concrete cli_command payload matching the real wire shape, and never claims to
// send anything bd does not actually emit (e.g. Dolt engine events).
func TestMetricsExampleShowsBdCommandEvent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	runMetricsExample(cmd)
	out := buf.String()
	for _, want := range []string{"cli_command", `"command"`, `"platform"`, `"app_name"`} {
		if !strings.Contains(out, want) {
			t.Errorf("`bd metrics example` output missing %q\n--- output ---\n%s", want, out)
		}
	}
	// We only emit a single cli_command event; never imply otherwise.
	for _, unwanted := range []string{"Dolt engine", "dolt_mode"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("`bd metrics example` should not mention %q (not actually sent)\n--- output ---\n%s", unwanted, out)
		}
	}
	// The example must not be HTML-escaped (no < for the < in the placeholder).
	if strings.Contains(out, `<`) || strings.Contains(out, `&`) {
		t.Errorf("`bd metrics example` output is HTML-escaped; want raw characters\n--- output ---\n%s", out)
	}
}

// TestMetricsFirstRunNoticeSuppressedForMetricsSubcommands ensures the one-time
// notice never fires while the user is already managing metrics.
func TestMetricsFirstRunNoticeSuppressedForMetricsSubcommands(t *testing.T) {
	parent := &cobra.Command{Use: "metrics"}
	for _, name := range []string{"on", "off", "example"} {
		sub := &cobra.Command{Use: name}
		parent.AddCommand(sub)
	}
	// The parent itself and each subcommand must be treated as "metrics" context.
	if got := isMetricsCommandContext(parent); !got {
		t.Errorf("parent metrics command should be metrics context")
	}
	for _, sub := range parent.Commands() {
		if got := isMetricsCommandContext(sub); !got {
			t.Errorf("`metrics %s` should be metrics context", sub.Name())
		}
	}
	other := &cobra.Command{Use: "ready"}
	if isMetricsCommandContext(other) {
		t.Errorf("`ready` should not be metrics context")
	}
}

// TestResolveMetricsIgnoresProjectConfigOverride is the runtime-resolver
// regression guard for the metrics opt-out authority blocker on PR #4419:
// resolveMetricsEnabled / resolveMetricsEndpoint must honor the user-global
// opt-out and endpoint and must not be overridden by a project / BEADS_DIR
// config (highest viper precedence). Mirrors the manual precedence check that
// surfaced the blocker.
func TestResolveMetricsIgnoresProjectConfigOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

	// The env escape hatches short-circuit the config path, so they must be
	// genuinely unset for this test to exercise config resolution. CI sets
	// BD_DISABLE_METRICS, so restore whatever was there afterward.
	unsetEnv := func(key string) {
		if orig, ok := os.LookupEnv(key); ok {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, orig) })
		}
	}
	unsetEnv("BD_DISABLE_METRICS")
	unsetEnv("BEADS_METRICS_ENDPOINT")

	const userEndpoint = "https://user-global.example/collect"
	const projectEndpoint = "https://project-override.example/collect"

	userCfgDir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir user config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"),
		[]byte("metrics:\n  disabled: true\n  endpoint: "+userEndpoint+"\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	projectBeadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(projectBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir project .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectBeadsDir, "config.yaml"),
		[]byte("metrics.disabled: false\nmetrics.endpoint: "+projectEndpoint+"\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Setenv("BEADS_DIR", projectBeadsDir)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	// Precondition: the project override is live in the merged config, so a
	// naive merged read would treat metrics as enabled.
	if config.GetBool("metrics.disabled") {
		t.Fatalf("precondition: merged metrics.disabled should be false (project override), got true")
	}

	if resolveMetricsEnabled() {
		t.Errorf("resolveMetricsEnabled() = true; project config must not re-enable a user who opted out")
	}
	if got := resolveMetricsEndpoint(); got != userEndpoint {
		t.Errorf("resolveMetricsEndpoint() = %q, want user-global %q; project config must not redirect", got, userEndpoint)
	}
}

// TestFirstRunNoticeSuppressedByContext guards the first-run metrics notice
// contract finding on PR #4419: the friendly notice must never write to stderr
// in machine-readable (JSON/quiet/hook-json), git-hook, hook/protocol, or
// stealth contexts, and must still fire for an ordinary interactive command.
func TestFirstRunNoticeSuppressedByContext(t *testing.T) {
	// Save/restore the output-mode globals this decision reads.
	origJSON, origQuiet, origHookJSON := jsonOutput, quietFlag, primeHookJSONMode
	t.Cleanup(func() {
		jsonOutput, quietFlag, primeHookJSONMode = origJSON, origQuiet, origHookJSON
	})
	reset := func() { jsonOutput, quietFlag, primeHookJSONMode = false, false, false }

	// Build a command tree rooted at "bd" so topLevelCommandName resolves the
	// suppressed-subtree names (e.g. `bd hooks run` -> "hooks").
	newTree := func() map[string]*cobra.Command {
		root := &cobra.Command{Use: "bd"}
		// Mirror the real root command's local --version/-V probe flag.
		root.Flags().BoolP("version", "V", false, "Print version information")
		cmds := map[string]*cobra.Command{"root": root}
		for _, name := range []string{"list", "version", "prime", "codex-hook"} {
			c := &cobra.Command{Use: name}
			root.AddCommand(c)
			cmds[name] = c
		}
		hooks := &cobra.Command{Use: "hooks"}
		root.AddCommand(hooks)
		hooksRun := &cobra.Command{Use: "run"}
		hooks.AddCommand(hooksRun)
		cmds["hooks-run"] = hooksRun

		initCmd := &cobra.Command{Use: "init"}
		initCmd.Flags().Bool("stealth", false, "")
		root.AddCommand(initCmd)
		cmds["init"] = initCmd
		return cmds
	}

	t.Run("plain interactive command fires", func(t *testing.T) {
		reset()
		if firstRunNoticeSuppressedByContext(newTree()["list"]) {
			t.Errorf("plain `bd list` should NOT be suppressed")
		}
	})

	for _, name := range []string{"version", "prime", "codex-hook", "hooks-run"} {
		t.Run(name+" is suppressed", func(t *testing.T) {
			reset()
			if !firstRunNoticeSuppressedByContext(newTree()[name]) {
				t.Errorf("%q context should suppress the first-run notice", name)
			}
		})
	}

	for _, mode := range []struct {
		name string
		set  func()
	}{
		{"json", func() { jsonOutput = true }},
		{"quiet", func() { quietFlag = true }},
		{"hook-json", func() { primeHookJSONMode = true }},
	} {
		t.Run(mode.name+" output is suppressed", func(t *testing.T) {
			reset()
			mode.set()
			if !firstRunNoticeSuppressedByContext(newTree()["list"]) {
				t.Errorf("%s output mode should suppress the first-run notice", mode.name)
			}
		})
	}

	t.Run("BD_GIT_HOOK context is suppressed", func(t *testing.T) {
		reset()
		t.Setenv("BD_GIT_HOOK", "1")
		if !firstRunNoticeSuppressedByContext(newTree()["list"]) {
			t.Errorf("BD_GIT_HOOK=1 should suppress the first-run notice")
		}
	})

	t.Run("stealth init is suppressed", func(t *testing.T) {
		reset()
		cmds := newTree()
		if err := cmds["init"].Flags().Set("stealth", "true"); err != nil {
			t.Fatalf("set stealth flag: %v", err)
		}
		if !firstRunNoticeSuppressedByContext(cmds["init"]) {
			t.Errorf("`bd init --stealth` should suppress the first-run notice")
		}
	})

	t.Run("plain init fires", func(t *testing.T) {
		reset()
		if firstRunNoticeSuppressedByContext(newTree()["init"]) {
			t.Errorf("plain `bd init` should NOT be suppressed (interactive consent is expected)")
		}
	})

	t.Run("root --version flag is suppressed", func(t *testing.T) {
		reset()
		cmds := newTree()
		if err := cmds["root"].Flags().Set("version", "true"); err != nil {
			t.Fatalf("set version flag: %v", err)
		}
		if !firstRunNoticeSuppressedByContext(cmds["root"]) {
			t.Errorf("`bd --version` / `bd -V` should suppress the first-run notice")
		}
	})

	t.Run("root without version flag fires", func(t *testing.T) {
		reset()
		if firstRunNoticeSuppressedByContext(newTree()["root"]) {
			t.Errorf("root command without --version set should NOT be suppressed by the version-flag rule")
		}
	})
}

// TestResolveMetricsEnabledHonorsDoNotTrack verifies the cross-tool DO_NOT_TRACK
// standard (https://donottrack.sh/) is honored as a disable-only opt-out: a
// truthy DO_NOT_TRACK opts out, a falsey or empty DO_NOT_TRACK falls through to
// the (here default-enabled) config rather than force-enabling, and
// BD_DISABLE_METRICS takes precedence when both are set. The guard that a falsey
// DO_NOT_TRACK must not re-enable a saved `bd metrics off` lives in
// TestResolveMetricsDoNotTrackIsDisableOnly.
func TestResolveMetricsEnabledHonorsDoNotTrack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

	for _, key := range []string{metrics.EnvDisableMetrics, metrics.EnvDoNotTrack} {
		if orig, ok := os.LookupEnv(key); ok {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, orig) })
		}
	}

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if !metricsEnabledByConfig() {
		t.Fatalf("precondition: metrics should be enabled by default config")
	}

	cases := []struct {
		name        string
		disableEnv  *string
		doNotTrack  *string
		wantEnabled bool
	}{
		{name: "no env uses config default", wantEnabled: true},
		{name: "DO_NOT_TRACK=1 disables", doNotTrack: strPtr("1"), wantEnabled: false},
		{name: "DO_NOT_TRACK=true disables", doNotTrack: strPtr("true"), wantEnabled: false},
		{name: "DO_NOT_TRACK=0 falls through to config (enabled here)", doNotTrack: strPtr("0"), wantEnabled: true},
		{name: "DO_NOT_TRACK empty falls through to config (enabled here)", doNotTrack: strPtr(""), wantEnabled: true},
		{name: "BD_DISABLE_METRICS=0 wins over DO_NOT_TRACK=1", disableEnv: strPtr("0"), doNotTrack: strPtr("1"), wantEnabled: true},
		{name: "BD_DISABLE_METRICS=1 wins over DO_NOT_TRACK=0", disableEnv: strPtr("1"), doNotTrack: strPtr("0"), wantEnabled: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableEnv != nil {
				t.Setenv(metrics.EnvDisableMetrics, *tc.disableEnv)
			}
			if tc.doNotTrack != nil {
				t.Setenv(metrics.EnvDoNotTrack, *tc.doNotTrack)
			}
			if got := resolveMetricsEnabled(); got != tc.wantEnabled {
				t.Errorf("resolveMetricsEnabled() = %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

// TestResolveMetricsDoNotTrackIsDisableOnly is the regression guard for the
// PR #4938 review finding that a falsey DO_NOT_TRACK could re-enable telemetry
// over an explicit opt-out. With a saved `bd metrics off`, DO_NOT_TRACK=0,
// false, or empty must fall through to that saved-off preference and never turn
// metrics back on. BD_DISABLE_METRICS stays a bidirectional override that can
// re-enable.
func TestResolveMetricsDoNotTrackIsDisableOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

	for _, key := range []string{metrics.EnvDisableMetrics, metrics.EnvDoNotTrack} {
		if orig, ok := os.LookupEnv(key); ok {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, orig) })
		}
	}

	// Persist `bd metrics off` to the user-global config before initializing.
	userCfgDir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir user config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"),
		[]byte("metrics:\n  disabled: true\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if metricsEnabledByConfig() {
		t.Fatalf("precondition: saved config should be metrics-off")
	}

	cases := []struct {
		name        string
		disableEnv  *string
		doNotTrack  *string
		wantEnabled bool
	}{
		{name: "saved-off, no env stays off", wantEnabled: false},
		{name: "DO_NOT_TRACK=0 does not re-enable saved-off", doNotTrack: strPtr("0"), wantEnabled: false},
		{name: "DO_NOT_TRACK=false does not re-enable saved-off", doNotTrack: strPtr("false"), wantEnabled: false},
		{name: "DO_NOT_TRACK empty does not re-enable saved-off", doNotTrack: strPtr(""), wantEnabled: false},
		{name: "DO_NOT_TRACK=1 keeps saved-off disabled", doNotTrack: strPtr("1"), wantEnabled: false},
		{name: "BD_DISABLE_METRICS=0 re-enables saved-off (bidirectional)", disableEnv: strPtr("0"), wantEnabled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableEnv != nil {
				t.Setenv(metrics.EnvDisableMetrics, *tc.disableEnv)
			}
			if tc.doNotTrack != nil {
				t.Setenv(metrics.EnvDoNotTrack, *tc.doNotTrack)
			}
			if got := resolveMetricsEnabled(); got != tc.wantEnabled {
				t.Errorf("resolveMetricsEnabled() = %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

// TestMetricsEnvOverrideReporting locks the env-override reporting contract that
// backs the `bd metrics` status/on/off notes: BD_DISABLE_METRICS is preferred
// when both are set, a truthy DO_NOT_TRACK is named as the effective override,
// and a disable-only falsey DO_NOT_TRACK is not reported as an override at all
// (it overrides nothing, so a note would be misleading).
func TestMetricsEnvOverrideReporting(t *testing.T) {
	for _, key := range []string{metrics.EnvDisableMetrics, metrics.EnvDoNotTrack} {
		if orig, ok := os.LookupEnv(key); ok {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, orig) })
		}
	}

	cases := []struct {
		name       string
		disableEnv *string
		doNotTrack *string
		wantName   string // "" means no override should be reported
	}{
		{name: "no env reports nothing", wantName: ""},
		{name: "DO_NOT_TRACK=1 is reported", doNotTrack: strPtr("1"), wantName: metrics.EnvDoNotTrack},
		{name: "DO_NOT_TRACK=0 is not reported", doNotTrack: strPtr("0"), wantName: ""},
		{name: "DO_NOT_TRACK empty is not reported", doNotTrack: strPtr(""), wantName: ""},
		{name: "BD_DISABLE_METRICS preferred over DO_NOT_TRACK", disableEnv: strPtr("1"), doNotTrack: strPtr("1"), wantName: metrics.EnvDisableMetrics},
		{name: "BD_DISABLE_METRICS=0 is reported (bidirectional)", disableEnv: strPtr("0"), wantName: metrics.EnvDisableMetrics},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableEnv != nil {
				t.Setenv(metrics.EnvDisableMetrics, *tc.disableEnv)
			}
			if tc.doNotTrack != nil {
				t.Setenv(metrics.EnvDoNotTrack, *tc.doNotTrack)
			}
			name, _, ok := metricsEnvOverride()
			if tc.wantName == "" {
				if ok {
					t.Errorf("metricsEnvOverride() reported %q, want no override", name)
				}
				return
			}
			if !ok || name != tc.wantName {
				t.Errorf("metricsEnvOverride() = (%q, ok=%v), want name %q", name, ok, tc.wantName)
			}
		})
	}
}

// TestWarnIfMetricsEnvOverrideNamesActiveVar verifies the toggle warning names
// the actually-effective override variable dynamically (regression guard for the
// helper refactor that replaced a hard-coded BD_DISABLE_METRICS string), and
// that a disable-only falsey DO_NOT_TRACK produces no warning.
func TestWarnIfMetricsEnvOverrideNamesActiveVar(t *testing.T) {
	for _, key := range []string{metrics.EnvDisableMetrics, metrics.EnvDoNotTrack} {
		if orig, ok := os.LookupEnv(key); ok {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, orig) })
		}
	}

	// The user is turning metrics on, so a truthy disable env disagrees and warns.
	warn := func() string {
		var buf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		warnIfMetricsEnvOverride(cmd, false)
		return buf.String()
	}

	t.Run("names DO_NOT_TRACK when it is the override", func(t *testing.T) {
		t.Setenv(metrics.EnvDoNotTrack, "1")
		got := warn()
		if !strings.Contains(got, "DO_NOT_TRACK") {
			t.Errorf("warning = %q, want it to name DO_NOT_TRACK", got)
		}
		if strings.Contains(got, "BD_DISABLE_METRICS") {
			t.Errorf("warning = %q, should not name BD_DISABLE_METRICS when only DO_NOT_TRACK is set", got)
		}
	})

	t.Run("prefers BD_DISABLE_METRICS when both set", func(t *testing.T) {
		t.Setenv(metrics.EnvDisableMetrics, "1")
		t.Setenv(metrics.EnvDoNotTrack, "1")
		if got := warn(); !strings.Contains(got, "BD_DISABLE_METRICS") {
			t.Errorf("warning = %q, want it to name BD_DISABLE_METRICS", got)
		}
	})

	t.Run("falsey DO_NOT_TRACK does not warn", func(t *testing.T) {
		t.Setenv(metrics.EnvDoNotTrack, "0")
		if got := warn(); got != "" {
			t.Errorf("warning = %q, want no warning for disable-only falsey DO_NOT_TRACK", got)
		}
	})
}

// darwinMetricsRunFixture is a macOS home after an upgrade: a legacy
// ~/Library/Application Support/bd/config.yaml, no ~/.config/bd/config.yaml,
// XDG_CONFIG_HOME and the metrics env overrides unset. It returns the home
// and a pointer to the number of times os.UserConfigDir (Application Support)
// was consulted.
func darwinMetricsRunFixture(t *testing.T, legacyBody string) (home string, nativeCalls *int) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	for _, key := range []string{metrics.EnvDisableMetrics, metrics.EnvDoNotTrack} {
		if orig, ok := os.LookupEnv(key); ok {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, orig) })
		}
	}
	appSupport := filepath.Join(home, "Library", "Application Support")
	if legacyBody != "" {
		legacy := filepath.Join(appSupport, "bd", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(legacy, []byte(legacyBody), 0o600); err != nil {
			t.Fatalf("write legacy config: %v", err)
		}
	}
	calls := 0
	t.Cleanup(config.SetUserConfigPlatformForTest("darwin", func() (string, error) {
		calls++
		return appSupport, nil
	}))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Cleanup(func() { metricsConsentUnknownForRun = false })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	return home, &calls
}

// runMetricsLaunch drives the metrics part of a bd launch the way
// PersistentPreRunE and PersistentPostRun do, and reports whether metrics
// were enabled for the run and what reached stderr.
func runMetricsLaunch(t *testing.T) (enabled bool, stderr string) {
	t.Helper()
	cmd := &cobra.Command{Use: "ready"}
	stderr = captureStderr(t, func() {
		enabled = prepareRunMetrics(cmd.Name())
		maybeShowMetricsFirstRunNotice(cmd)
	})
	return enabled, stderr
}

// TestDarwinFirstRunCarriesLegacyMetricsOptOut: the first bd run on macOS
// after the move to ~/.config creates ~/.config/bd/config.yaml from the
// legacy Application Support file, so a saved `bd metrics off` holds on that
// very run (no send, no notice) and every later run reads only the new file
// (GH#8113).
func TestDarwinFirstRunCarriesLegacyMetricsOptOut(t *testing.T) {
	const legacyBody = "metrics:\n  disabled: true\nnode_id: n1\n"
	home, nativeCalls := darwinMetricsRunFixture(t, legacyBody)
	newPath := filepath.Join(home, ".config", "bd", "config.yaml")

	enabled, stderr := runMetricsLaunch(t)
	if enabled {
		t.Fatal("first run enabled metrics over the legacy `bd metrics off`")
	}
	if strings.Contains(stderr, "Thanks for using bd") {
		t.Fatalf("first run showed the metrics notice to an opted-out user:\n%s", stderr)
	}
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("first run did not create %s: %v", newPath, err)
	}
	if string(got) != legacyBody {
		t.Fatalf("new user config = %q, want the legacy bytes %q", got, legacyBody)
	}
	if info, err := os.Stat(newPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new user config mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if *nativeCalls != 1 {
		t.Fatalf("Application Support consulted %d times on the creating run, want 1", *nativeCalls)
	}

	*nativeCalls = 0
	enabled, stderr = runMetricsLaunch(t)
	if enabled || strings.Contains(stderr, "Thanks for using bd") {
		t.Fatalf("second run: enabled=%v stderr=%q; want metrics off and no notice", enabled, stderr)
	}
	if *nativeCalls != 0 {
		t.Fatalf("Application Support consulted %d times once ~/.config/bd/config.yaml exists", *nativeCalls)
	}
	if got, _ := os.ReadFile(newPath); string(got) != legacyBody {
		t.Fatalf("second run rewrote the user config: %q", got)
	}
}

// TestDarwinUnreadableLegacyConfigFailsClosed: when the legacy Application
// Support config exists but cannot be read (a macOS privacy denial reports a
// permission error), bd cannot tell whether the user opted out, so the new
// ~/.config/bd/config.yaml turns metrics off. They stay off on that run and
// every later one until `bd metrics on` (GH#8113).
func TestDarwinUnreadableLegacyConfigFailsClosed(t *testing.T) {
	home, nativeCalls := darwinMetricsRunFixture(t, "node_id: n1\n")
	newPath := filepath.Join(home, ".config", "bd", "config.yaml")
	stats := 0
	t.Cleanup(config.SetLegacyUserConfigStatForTest(func(name string) (fs.FileInfo, error) {
		stats++
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrPermission}
	}))

	enabled, stderr := runMetricsLaunch(t)
	if enabled || strings.Contains(stderr, "Thanks for using bd") {
		t.Fatalf("first run: enabled=%v stderr=%q; want metrics off and no notice", enabled, stderr)
	}
	if stats != 1 {
		t.Fatalf("legacy config statted %d times, want 1", stats)
	}
	if !config.MetricsDisabledByUserConfig() {
		got, _ := os.ReadFile(newPath)
		t.Fatalf("new user config does not turn metrics off:\n%s", got)
	}

	*nativeCalls, stats = 0, 0
	if enabled, stderr = runMetricsLaunch(t); enabled || strings.Contains(stderr, "Thanks for using bd") {
		t.Fatalf("second run: enabled=%v stderr=%q; want metrics off and no notice", enabled, stderr)
	}
	if *nativeCalls != 0 || stats != 0 {
		t.Fatalf("second run probed Application Support: native=%d stat=%d", *nativeCalls, stats)
	}

	var buf bytes.Buffer
	metricsOnCmd.SetOut(&buf)
	metricsOnCmd.SetErr(&buf)
	if err := metricsOnCmd.RunE(metricsOnCmd, nil); err != nil {
		t.Fatalf("bd metrics on: %v", err)
	}
	if enabled, _ = runMetricsLaunch(t); !enabled {
		t.Fatal("metrics still off after `bd metrics on`")
	}
}

// TestDarwinUncreatableUserConfigDirLeavesMetricsOffForTheRun: when bd cannot
// create ~/.config/bd (here ~/.config is a dangling symlink), it never probes
// the legacy Application Support config, and since it cannot know whether the
// user opted out there, metrics stay off for the run with no notice (GH#8113).
func TestDarwinUncreatableUserConfigDirLeavesMetricsOffForTheRun(t *testing.T) {
	home, nativeCalls := darwinMetricsRunFixture(t, "metrics:\n  disabled: true\n")
	missing := filepath.Join(home, "dotfiles", "config") // never created
	if err := os.Symlink(missing, filepath.Join(home, ".config")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	stats := 0
	t.Cleanup(config.SetLegacyUserConfigStatForTest(func(name string) (fs.FileInfo, error) {
		stats++
		return os.Stat(name)
	}))

	for run := 1; run <= 2; run++ {
		enabled, stderr := runMetricsLaunch(t)
		if enabled || strings.Contains(stderr, "Thanks for using bd") {
			t.Fatalf("run %d: enabled=%v stderr=%q; want metrics off and no notice", run, enabled, stderr)
		}
	}
	if *nativeCalls != 0 || stats != 0 {
		t.Fatalf("Application Support probed without a user config to create: native=%d stat=%d", *nativeCalls, stats)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("symlink target was created: %v", err)
	}
}

// TestDarwinUnusableUserConfigPathLeavesMetricsOffForTheRun: when bd can
// neither read nor create ~/.config/bd/config.yaml, a `bd metrics off` saved
// in the legacy Application Support config cannot be carried over. Metrics
// then stay off on every run with no notice, Application Support is never
// probed, and `bd metrics` reports them off and why (GH#8113). Before the
// move bd read the legacy file directly in both setups and honored the
// opt-out, so turning metrics on here would be a silent opt-in.
func TestDarwinUnusableUserConfigPathLeavesMetricsOffForTheRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		// plant makes ~/.config/bd/config.yaml unusable and returns a path
		// that must still not exist afterwards ("" for none).
		plant func(t *testing.T, home string) (mustNotExist string)
	}{
		{"config.yaml is a dangling symlink", func(t *testing.T, home string) string {
			dir := filepath.Join(home, ".config", "bd")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			target := filepath.Join(home, "dotfiles", "bd", "config.yaml") // never created
			if err := os.Symlink(target, filepath.Join(dir, "config.yaml")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			return target
		}},
		{"~/.config is a regular file", func(t *testing.T, home string) string {
			if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a directory\n"), 0o600); err != nil {
				t.Fatalf("plant ~/.config file: %v", err)
			}
			return ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, nativeCalls := darwinMetricsRunFixture(t, "metrics:\n  disabled: true\n")
			mustNotExist := tc.plant(t, home)
			stats := 0
			t.Cleanup(config.SetLegacyUserConfigStatForTest(func(name string) (fs.FileInfo, error) {
				stats++
				return os.Stat(name)
			}))

			for run := 1; run <= 2; run++ {
				enabled, stderr := runMetricsLaunch(t)
				if enabled || strings.Contains(stderr, "Thanks for using bd") {
					t.Fatalf("run %d: enabled=%v stderr=%q; want metrics off and no notice over the legacy opt-out", run, enabled, stderr)
				}
			}
			if *nativeCalls != 0 || stats != 0 {
				t.Fatalf("Application Support probed for a user config bd cannot create: native=%d stat=%d", *nativeCalls, stats)
			}
			if mustNotExist != "" {
				if _, err := os.Lstat(mustNotExist); !os.IsNotExist(err) {
					t.Fatalf("symlink target was created: %v", err)
				}
			}

			var out bytes.Buffer
			status := &cobra.Command{Use: "metrics"}
			status.SetOut(&out)
			status.SetErr(&out)
			runMetricsStatus(status)
			got := out.String()
			want := filepath.Join(home, ".config", "bd", "config.yaml")
			if !strings.Contains(got, "Anonymous usage metrics: OFF") || !strings.Contains(got, want) {
				t.Fatalf("`bd metrics` must report OFF and name %s:\n%s", want, got)
			}
			if strings.Contains(got, "Turn off:") || strings.Contains(got, "Turn on:") {
				t.Fatalf("`bd metrics` suggests a toggle that cannot write the config:\n%s", got)
			}
		})
	}
}
