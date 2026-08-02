package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
)

// metricsCmd lets users see and control anonymous usage metrics without ever
// hand-editing config or setting an environment variable. `bd metrics on` /
// `bd metrics off` write the user-global config directly and take effect on the
// next command — no shell/supervisor restart required.
var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Show or change anonymous usage-metrics settings",
	Long: `Show whether anonymous usage metrics are on, see exactly what is sent, and
turn them on or off.

bd shares anonymous usage metrics to learn how people actually use it — just
which commands get run, plus the bd version and OS platform. That's how we decide
what to polish next. We never collect your issues, paths, remotes, identity, or
any user-supplied text.

  bd metrics            show the current status and what is collected
  bd metrics on         turn metrics on
  bd metrics off        turn metrics off
  bd metrics example    show real examples of the events bd sends`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("metrics")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()
		runMetricsStatus(cmd)
		return nil
	},
}

var metricsOnCmd = &cobra.Command{
	Use:           "on",
	Short:         "Turn anonymous usage metrics on",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("metrics-on")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()
		if err := setMetricsDisabled(false); err != nil {
			return HandleError("failed to update metrics setting: %v", err)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "✓ Anonymous usage metrics are now ON. Thank you — this genuinely helps us make bd better!")
		fmt.Fprintln(out, "   See what's sent with `bd metrics example`, or turn it off again with `bd metrics off`.")
		warnIfMetricsEnvOverride(cmd, false)
		return nil
	},
}

var metricsOffCmd = &cobra.Command{
	Use:           "off",
	Short:         "Turn anonymous usage metrics off",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Opt-out is intentionally the one command that emits no cli_command
		// event. Metrics are still enabled for this invocation (the saved config
		// only takes effect next time), so recording a `metrics-off` event would
		// queue telemetry the user just declined — and if they re-enable later it
		// would flush — directly contradicting the "No usage data will be
		// collected or sent" promise printed below. So we write the opt-out and
		// add nothing to the collector.
		if err := setMetricsDisabled(true); err != nil {
			return HandleError("failed to update metrics setting: %v", err)
		}
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "Anonymous usage metrics are now OFF. No usage data will be collected or sent.")
		fmt.Fprintln(out, "Turn them back on anytime with `bd metrics on`. Thanks for giving them a try!")
		warnIfMetricsEnvOverride(cmd, true)
		return nil
	},
}

var metricsExampleCmd = &cobra.Command{
	Use:           "example",
	Short:         "Show real examples of the anonymous metrics bd sends",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("metrics-example")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()
		runMetricsExample(cmd)
		return nil
	},
}

func init() {
	metricsCmd.AddCommand(metricsOnCmd, metricsOffCmd, metricsExampleCmd)
	rootCmd.AddCommand(metricsCmd)
}

// setMetricsDisabled writes metrics.disabled to the user-global config. The
// metrics.* prefix routes to the user config automatically, so this is the same
// store the runtime reads — no manual editing and no env var required.
func setMetricsDisabled(disabled bool) error {
	val := "false"
	if disabled {
		val = "true"
	}
	return config.SetUserYamlConfig("metrics.disabled", val)
}

// metricsEnabledByConfig reports the config-driven state (ignoring the env
// override), which is what `bd metrics on/off` controls. It reads the
// user-global config only — the same store `bd metrics on/off` writes — so a
// project config can never skew the reported "saved" state.
func metricsEnabledByConfig() bool {
	return !config.MetricsDisabledByUserConfig()
}

// metricsEnvOverride returns the metrics opt-out env var in effect for this
// shell, if any, preferring BD_DISABLE_METRICS over its DO_NOT_TRACK alias.
// BD_DISABLE_METRICS is a bidirectional override, so it counts whenever it is
// set. DO_NOT_TRACK is disable-only: a falsey or empty DO_NOT_TRACK falls
// through to saved config and overrides nothing, so it is reported as an active
// override only when truthy — matching resolveMetricsEnabled.
func metricsEnvOverride() (name, value string, ok bool) {
	if v, found := os.LookupEnv(metrics.EnvDisableMetrics); found {
		return metrics.EnvDisableMetrics, v, true
	}
	if v, found := os.LookupEnv(metrics.EnvDoNotTrack); found && envTruthyValue(v) {
		return metrics.EnvDoNotTrack, v, true
	}
	return "", "", false
}

// warnIfMetricsEnvOverride tells the user when an environment override is set
// and would override the config they just changed, so the toggle never silently
// does nothing.
func warnIfMetricsEnvOverride(cmd *cobra.Command, wantDisabled bool) {
	name, v, ok := metricsEnvOverride()
	if !ok {
		return
	}
	envDisabled := envTruthyValue(v)
	if envDisabled == wantDisabled {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"\nNote: %s=%s is set in your environment and overrides this for the current shell.\n"+
			"      Your config preference is saved; unset %s to let it take effect.\n",
		name, v, name)
}

func runMetricsStatus(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	effective := resolveMetricsEnabled()
	state := "OFF"
	if effective {
		state = "ON"
	}
	fmt.Fprintf(out, "Anonymous usage metrics: %s\n\n", state)
	fmt.Fprintln(out, "What we collect: the name of each bd command you run, plus the bd version")
	fmt.Fprintln(out, "and OS platform, keyed by a machine-derived, HMAC-protected ID. We never")
	fmt.Fprintln(out, "collect your issues, paths, remotes, identity, or any text you type. Seeing")
	fmt.Fprintln(out, "real usage patterns is how we decide what to improve next — thank you for")
	fmt.Fprintln(out, "helping make bd better.")
	fmt.Fprintf(out, "\nWhere it goes: %s\n", resolveMetricsEndpoint())
	fmt.Fprintln(out, "\n  See real examples:  bd metrics example")
	if effective {
		fmt.Fprintln(out, "  Turn off:           bd metrics off")
	} else {
		fmt.Fprintln(out, "  Turn on:            bd metrics on")
	}

	// Surface the env override if it disagrees with the saved config.
	if name, v, ok := metricsEnvOverride(); ok {
		if envTruthyValue(v) == metricsEnabledByConfig() {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\nNote: %s=%s is overriding your saved config (which is %q) for this shell.\n",
				name, v, map[bool]string{true: "on", false: "off"}[metricsEnabledByConfig()])
		}
	}
}

func runMetricsExample(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "bd sends one kind of anonymous event: a `cli_command` record — one per")
	fmt.Fprintln(out, "command you run. Each batch carries a machine-derived, HMAC-protected distinct")
	fmt.Fprintln(out, "ID, the bd version, and your OS platform. The only per-event attribute is the")
	fmt.Fprintln(out, "command name — never your issues, IDs, paths, remotes, identity, or anything")
	fmt.Fprintln(out, "you type. A representative payload:")
	fmt.Fprintln(out, "")
	example := map[string]any{
		"distinct_id": "(machine-derived, HMAC-protected — not your identity)",
		"app_name":    "beads",
		"app_version": Version,
		"platform":    runtime.GOOS,
		"events": []map[string]any{{
			"name":       "cli_command",
			"attributes": []map[string]string{{"key": "command", "value": "ready"}},
		}},
	}
	if b, err := marshalIndentNoEscape(example); err == nil {
		fmt.Fprintf(out, "  %s\n\n", b)
	}

	// The most honest "what we send" is the queue itself: show the real events
	// currently buffered locally, waiting to be flushed.
	shown := showQueuedEvents(out)
	if shown == 0 {
		fmt.Fprintln(out, "Nothing is queued locally right now. Run a few bd commands (with metrics on)")
		fmt.Fprintln(out, "and re-run `bd metrics example` to see the exact payloads buffered on your")
		fmt.Fprintln(out, "machine before they are sent.")
	}
}

// marshalIndentNoEscape is json.MarshalIndent without Go's default HTML escaping,
// so <, >, and & render as themselves in human-facing example output.
func marshalIndentNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("  ", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// showQueuedEvents pretty-prints the real, locally-buffered event payloads from
// ~/.beads/eventsData (the same files the flusher reads before sending), so the
// user sees exactly what would leave their machine. Returns how many event
// files were shown.
func showQueuedEvents(out interface{ Write([]byte) (int, error) }) int {
	dir, err := metrics.DataDir()
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		files = append(files, e.Name())
	}
	if len(files) == 0 {
		return 0
	}
	sort.Strings(files)
	const maxShow = 3
	fmt.Fprintf(out, "Currently queued on this machine (%s) — %d batch file(s); showing up to %d:\n\n", dir, len(files), maxShow)
	shown := 0
	for _, name := range files {
		if shown >= maxShow {
			break
		}
		// #nosec G304 -- dir is bd's own metrics data dir and name comes from os.ReadDir of that same dir, not user input
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var pretty json.RawMessage
		if json.Unmarshal(raw, &pretty) != nil {
			continue
		}
		buf, err := marshalIndentNoEscape(pretty)
		if err != nil {
			continue
		}
		fmt.Fprintf(out, "  %s\n\n", buf)
		shown++
	}
	if len(files) > shown {
		fmt.Fprintf(out, "  ... and %d more batch file(s).\n", len(files)-shown)
	}
	return shown
}

// isMetricsCommandContext reports whether cmd is `bd metrics` or one of its
// subcommands — contexts where the first-run notice should not fire because the
// user is already looking at / managing metrics.
func isMetricsCommandContext(cmd *cobra.Command) bool {
	return cmd.Name() == "metrics" || (cmd.Parent() != nil && cmd.Parent().Name() == "metrics")
}

// topLevelCommandName returns the name of cmd's top-level ancestor — the command
// directly under the root "bd" command. For `bd hooks run` it returns "hooks";
// for a top-level command it returns that command's own name. This lets context
// checks match a whole command subtree (e.g. all of `bd hooks ...`) by the
// top-level name even when cmd is a nested leaf.
func topLevelCommandName(cmd *cobra.Command) string {
	c := cmd
	for c.Parent() != nil && c.Parent().Parent() != nil {
		c = c.Parent()
	}
	return c.Name()
}

// firstRunNoticeSuppressedCommands are top-level command names whose contexts are
// machine-facing or hook/protocol bridges, so the friendly first-run metrics
// notice must never write to their stderr (it could corrupt a hook envelope or
// surprise a non-interactive caller). Matched by top-level name so nested
// subcommands like `bd hooks run` are covered too.
var firstRunNoticeSuppressedCommands = map[string]bool{
	metrics.SendMetricsSubcommand: true,
	"prime":                       true,
	"version":                     true,
	"completion":                  true,
	"__complete":                  true,
	"__completeNoDesc":            true,
	"hook":                        true, // bd hook bridge (manages its own store/protocol lifecycle)
	"hooks":                       true, // bd hooks ... (git-hook management/runner)
	"codex-hook":                  true, // codex protocol bridge
	"bash":                        true,
	"zsh":                         true,
	"fish":                        true,
	"powershell":                  true,
}

// firstRunNoticeSuppressedByContext reports whether the current command/output
// context must never emit the friendly first-run metrics notice. This is the
// pure context decision — independent of whether metrics are enabled or the
// notice was already shown — so it stays unit-testable. It suppresses machine
// output (JSON/quiet/hook-json), git-hook execution (BD_GIT_HOOK), the metrics
// command itself, hook/protocol/completion/shell-init commands, the root
// --version/-V probe, and stealth init.
func firstRunNoticeSuppressedByContext(cmd *cobra.Command) bool {
	if jsonOutput || quietFlag || primeHookJSONMode {
		return true
	}
	if os.Getenv("BD_GIT_HOOK") == "1" {
		return true
	}
	if isMetricsCommandContext(cmd) {
		return true
	}
	if firstRunNoticeSuppressedCommands[topLevelCommandName(cmd)] {
		return true
	}
	// `bd --version` / `bd -V` is a version probe just like the `version`
	// subcommand, so it must not emit (or mark shown) the one-time consent notice.
	// The root flag path has top-level command name "bd" and so slips past the
	// suppressed-command map above; check the flag directly. GetBool errors for
	// commands that do not define a "version" flag, which the err guard ignores.
	if v, err := cmd.Flags().GetBool("version"); err == nil && v {
		return true
	}
	// `bd init --stealth` configures invisible per-repository usage; a visible
	// consent notice on stderr would contradict that, so suppress it there.
	if stealth, err := cmd.Flags().GetBool("stealth"); err == nil && stealth {
		return true
	}
	return false
}

// maybeShowMetricsFirstRunNotice prints a one-time, friendly heads-up the first
// time bd runs with metrics enabled, then records that it was shown so it never
// repeats. It writes to stderr and is suppressed in JSON / hook / protocol /
// quiet / stealth contexts (see firstRunNoticeSuppressedByContext) so it can
// never corrupt machine-readable output.
func maybeShowMetricsFirstRunNotice(cmd *cobra.Command) {
	if !resolveMetricsEnabled() {
		return
	}
	// Resolve the "already shown" marker from user-global config only. Reading it
	// from merged config would let a repository's .beads/config.yaml set
	// metrics.notice_shown: true and suppress this one-time disclosure for a user
	// who has never seen it — the same project-override hole closed for consent
	// and endpoint resolution.
	if config.MetricsNoticeShownByUserConfig() {
		return
	}
	if firstRunNoticeSuppressedByContext(cmd) {
		return
	}

	fmt.Fprintln(os.Stderr, "Thanks for using bd! Quick heads-up: bd shares anonymous usage metrics —")
	fmt.Fprintln(os.Stderr, "   just which commands get run (plus the bd version and OS platform), never your")
	fmt.Fprintln(os.Stderr, "   issues, paths, remotes, identity, or anything you type. Seeing how people use")
	fmt.Fprintln(os.Stderr, "   bd is how we decide what to improve next, so it genuinely makes bd better for")
	fmt.Fprintln(os.Stderr, "   everyone.")
	fmt.Fprintln(os.Stderr, "      Curious what's sent?   bd metrics example")
	fmt.Fprintln(os.Stderr, "      Prefer to opt out?     bd metrics off    (one command, no restart needed)")
	fmt.Fprintln(os.Stderr, "")

	// Record that the notice was shown so it never repeats. Best-effort: if the
	// write fails, we simply may show it again — never block the command.
	_ = config.SetUserYamlConfig("metrics.notice_shown", "true")
}
