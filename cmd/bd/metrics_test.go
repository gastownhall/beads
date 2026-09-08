package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
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
	b, err := os.ReadFile(config.UserConfigYamlPath())
	if err != nil {
		t.Fatalf("read user config %s: %v", config.UserConfigYamlPath(), err)
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
