//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/metrics"
)

type metricsEvent struct {
	AppName string `json:"app_name"`
	Events  []struct {
		Name       string `json:"name"`
		Attributes []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"attributes"`
	} `json:"events"`
}

func readInitEvent(t *testing.T, home, expectedCommand string) metricsEvent {
	t.Helper()
	dir := filepath.Join(home, ".beads", "eventsData")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read eventsData dir: %v", err)
	}
	var evtqs []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".evtq") {
			evtqs = append(evtqs, e.Name())
		}
	}
	if len(evtqs) == 0 {
		t.Fatalf("expected at least 1 .evtq file, got 0")
	}
	for _, name := range evtqs {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read evtq %s: %v", name, err)
		}
		var got metricsEvent
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal evtq %s: %v\n%s", name, err, body)
		}
		if got.commandAttr() == expectedCommand {
			return got
		}
	}
	t.Fatalf("no .evtq with command=%s among %v", expectedCommand, evtqs)
	return metricsEvent{}
}

func (e metricsEvent) commandAttr() string {
	if len(e.Events) == 0 {
		return ""
	}
	for _, a := range e.Events[0].Attributes {
		if a.Key == "command" {
			return a.Value
		}
	}
	return ""
}

func (e metricsEvent) attr(key string) (string, bool) {
	if len(e.Events) == 0 {
		return "", false
	}
	for _, a := range e.Events[0].Attributes {
		if a.Key == key {
			return a.Value, true
		}
	}
	return "", false
}

func metricsTestEnv(home string, extra ...string) []string {
	base := bdEnv(home)
	out := make([]string, 0, len(base)+len(extra)+1)
	for _, e := range base {
		if strings.HasPrefix(e, "BD_DISABLE_METRICS=") || strings.HasPrefix(e, "DO_NOT_TRACK=") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, "BD_DISABLE_EVENT_FLUSH=1")
	return append(out, extra...)
}

func runBdInitForMetrics(t *testing.T, home string, args ...string) {
	t.Helper()
	bd := buildEmbeddedBD(t)
	repo, err := testTempDir("bd-metrics-repo-*")
	if err != nil {
		t.Fatalf("temp repo: %v", err)
	}
	initGitRepoAt(t, repo)

	full := append([]string{"init", "--non-interactive", "--quiet"}, args...)
	cmd := exec.Command(bd, full...)
	cmd.Dir = repo
	cmd.Env = metricsTestEnv(home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
}

func TestInitMetricsEmittedPerDoltMode(t *testing.T) {
	cases := []struct {
		name            string
		extraArgs       []string
		extraEnv        []string
		expectedCommand string
	}{
		{
			name:            "embedded_default",
			expectedCommand: "init-embedded",
		},
		{
			name:            "server_via_flag",
			extraArgs:       []string{"--server"},
			expectedCommand: "init-server",
		},
		{
			name:            "shared_server_via_flag",
			extraArgs:       []string{"--shared-server"},
			expectedCommand: "init-shared-server",
		},
		{
			name:            "proxied_server_via_flag",
			extraArgs:       []string{"--proxied-server"},
			expectedCommand: "init-proxied-server",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			home, err := testTempDir("bd-metrics-home-*")
			if err != nil {
				t.Fatalf("temp home: %v", err)
			}
			runBdInitForMetrics(t, home, tc.extraArgs...)
			evt := readInitEvent(t, home, tc.expectedCommand)

			if evt.AppName != "beads" {
				t.Errorf("app_name = %q, want %q", evt.AppName, "beads")
			}
			if len(evt.Events) != 1 {
				t.Fatalf("events len = %d, want 1", len(evt.Events))
			}
			if evt.Events[0].Name != "cli_command" {
				t.Errorf("event name = %q, want %q", evt.Events[0].Name, "cli_command")
			}
			if got, _ := evt.attr("command"); got != tc.expectedCommand {
				t.Errorf("command attr = %q, want %q", got, tc.expectedCommand)
			}
			if got, ok := evt.attr("dolt_mode"); ok {
				t.Errorf("dolt_mode attr should no longer be set, got %q", got)
			}
		})
	}
}

func writeUserMetricsDisabled(t *testing.T, home string, disabled bool) {
	t.Helper()
	dir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir user config: %v", err)
	}
	body := []byte("metrics.disabled: false\n")
	if disabled {
		body = []byte("metrics.disabled: true\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), body, 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
}

func evtqFilesIn(t *testing.T, home string) []string {
	t.Helper()
	dir := filepath.Join(home, ".beads", "eventsData")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".evtq") {
			out = append(out, e.Name())
		}
	}
	return out
}

func runBdInitWithEnv(t *testing.T, home string, extraEnv []string) {
	t.Helper()
	bd := buildEmbeddedBD(t)
	repo, err := testTempDir("bd-metrics-repo-*")
	if err != nil {
		t.Fatalf("temp repo: %v", err)
	}
	initGitRepoAt(t, repo)

	cmd := exec.Command(bd, "init", "--non-interactive", "--quiet")
	cmd.Dir = repo
	cmd.Env = metricsTestEnv(home, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
}

func TestInitMetricsEnvConfigPrecedence(t *testing.T) {
	cases := []struct {
		name           string
		setUserConfig  bool
		configDisabled bool
		envVar         []string
		wantEvtq       bool
	}{
		{
			name:     "default_no_config_no_env",
			wantEvtq: true,
		},
		{
			name:           "config_disabled_env_unset",
			setUserConfig:  true,
			configDisabled: true,
			wantEvtq:       false,
		},
		{
			name:           "config_enabled_env_disables",
			setUserConfig:  true,
			configDisabled: false,
			envVar:         []string{"BD_DISABLE_METRICS=1"},
			wantEvtq:       false,
		},
		{
			name:           "config_disabled_env_overrides_to_enabled",
			setUserConfig:  true,
			configDisabled: true,
			envVar:         []string{"BD_DISABLE_METRICS=0"},
			wantEvtq:       true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			home, err := testTempDir("bd-metrics-prec-*")
			if err != nil {
				t.Fatalf("temp home: %v", err)
			}
			if tc.setUserConfig {
				writeUserMetricsDisabled(t, home, tc.configDisabled)
			}

			runBdInitWithEnv(t, home, tc.envVar)

			files := evtqFilesIn(t, home)
			if tc.wantEvtq && len(files) == 0 {
				t.Errorf("expected an .evtq file, got none")
			}
			if !tc.wantEvtq && len(files) > 0 {
				t.Errorf("expected no .evtq files, got %v", files)
			}
		})
	}
}

func TestInitBootstrapsUserConfigWhenMissing(t *testing.T) {
	home, err := testTempDir("bd-bootstrap-fresh-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}

	runBdInitWithEnv(t, home, nil)

	path := filepath.Join(home, ".config", "bd", "config.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	got := string(body)
	if !strings.Contains(got, metrics.DefaultEndpoint) {
		t.Errorf("user config missing metrics endpoint.\nfile: %q", got)
	}
	if !strings.Contains(got, "endpoint:") {
		t.Errorf("user config missing endpoint key.\nfile: %q", got)
	}
}

func TestInitPreservesExistingMetricsValuesAndFillsMissingLeaves(t *testing.T) {
	home, err := testTempDir("bd-bootstrap-existing-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}

	dir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	original := []byte("metrics:\n  endpoint: https://example.invalid/custom\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	runBdInitWithEnv(t, home, nil)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(got)
	if !strings.Contains(body, "https://example.invalid/custom") {
		t.Errorf("user-set endpoint was clobbered.\nfile: %q", body)
	}
	if !strings.Contains(body, "disabled:") {
		t.Errorf("missing metrics.disabled was not filled in.\nfile: %q", body)
	}
}

func TestInitMetricsDisabledSuppresses(t *testing.T) {
	bd := buildEmbeddedBD(t)
	home, err := testTempDir("bd-metrics-disabled-home-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	repo, err := testTempDir("bd-metrics-disabled-repo-*")
	if err != nil {
		t.Fatalf("temp repo: %v", err)
	}
	initGitRepoAt(t, repo)

	cmd := exec.Command(bd, "init", "--non-interactive", "--quiet")
	cmd.Dir = repo
	env := append([]string{}, bdEnv(home)...)
	env = append(env, "BD_DISABLE_METRICS=1")
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bd init failed: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}

	dir := filepath.Join(home, ".beads", "eventsData")
	if _, err := os.Stat(dir); err == nil {
		entries, _ := os.ReadDir(dir)
		var evtqs []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".evtq") {
				evtqs = append(evtqs, e.Name())
			}
		}
		if len(evtqs) > 0 {
			t.Errorf("BD_DISABLE_METRICS=1 still produced .evtq files: %v", evtqs)
		}
	}
}

// allCommandEvents returns the `command` attribute of every cli_command event
// across all queued .evtq files, so a test can assert the exact emission
// cardinality of a single bd invocation (one user command must record exactly
// one cli_command event).
func allCommandEvents(t *testing.T, home string) []string {
	t.Helper()
	dir := filepath.Join(home, ".beads", "eventsData")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var cmds []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".evtq") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read evtq %s: %v", e.Name(), err)
		}
		var got metricsEvent
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal evtq %s: %v\n%s", e.Name(), err, body)
		}
		for _, ev := range got.Events {
			if ev.Name != "cli_command" {
				continue
			}
			for _, a := range ev.Attributes {
				if a.Key == "command" {
					cmds = append(cmds, a.Value)
				}
			}
		}
	}
	return cmds
}

func runBdForMetrics(t *testing.T, bd, repo, home string, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = repo
	cmd.Env = metricsTestEnv(home)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	_ = cmd.Run()
	return outBuf.String(), errBuf.String()
}

// TestMetricsTodoAliasEmitsSingleEvent is the double-emit regression for PR
// #4419: bare `bd todo` delegates to the todo-list behavior, but it must record
// exactly one cli_command event ("todo"), not also a phantom "todo-list".
func TestMetricsTodoAliasEmitsSingleEvent(t *testing.T) {
	bd := buildEmbeddedBD(t)
	home, err := testTempDir("bd-metrics-todo-home-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	repo, err := testTempDir("bd-metrics-todo-repo-*")
	if err != nil {
		t.Fatalf("temp repo: %v", err)
	}
	initGitRepoAt(t, repo)

	// A store is needed so `bd todo` (which lists task issues) reaches its RunE.
	if _, errOut := runBdForMetrics(t, bd, repo, home, "init", "--non-interactive", "--quiet"); errOut != "" {
		// init may print warnings; only fail later if todo produces no event.
		_ = errOut
	}

	// Isolate the next invocation by dropping init's queued event.
	if err := os.RemoveAll(filepath.Join(home, ".beads", "eventsData")); err != nil {
		t.Fatalf("clear eventsData: %v", err)
	}

	_, errOut := runBdForMetrics(t, bd, repo, home, "todo")

	got := allCommandEvents(t, home)
	if len(got) != 1 || got[0] != "todo" {
		t.Errorf("bd todo emitted %v, want exactly [todo] (double-emit regression)\nstderr:\n%s", got, errOut)
	}
}

// TestMetricsReadyGatedAliasEmitsSingleEvent is the double-emit regression for
// PR #4419: `bd ready --gated` delegates to the gate-ready molecule discovery,
// but it must record exactly one cli_command event ("ready"), not also a phantom
// "mol-ready-gated".
func TestMetricsReadyGatedAliasEmitsSingleEvent(t *testing.T) {
	bd := buildEmbeddedBD(t)
	home, err := testTempDir("bd-metrics-readygated-home-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	repo, err := testTempDir("bd-metrics-readygated-repo-*")
	if err != nil {
		t.Fatalf("temp repo: %v", err)
	}
	initGitRepoAt(t, repo)

	// A store is needed so `bd ready --gated` reaches its discovery body.
	if _, errOut := runBdForMetrics(t, bd, repo, home, "init", "--non-interactive", "--quiet"); errOut != "" {
		_ = errOut
	}

	// Isolate the next invocation by dropping init's queued event.
	if err := os.RemoveAll(filepath.Join(home, ".beads", "eventsData")); err != nil {
		t.Fatalf("clear eventsData: %v", err)
	}

	_, errOut := runBdForMetrics(t, bd, repo, home, "ready", "--gated")

	got := allCommandEvents(t, home)
	if len(got) != 1 || got[0] != "ready" {
		t.Errorf("bd ready --gated emitted %v, want exactly [ready] (double-emit regression)\nstderr:\n%s", got, errOut)
	}
}

// TestMetricsWispAliasEmitsSingleEvent is the double-emit regression for PR
// #4419: bare `bd mol wisp <proto>` delegates to the wisp-create behavior, but it
// must record exactly one cli_command event ("wisp"), not also a phantom
// "wisp-create". The proto does not exist, so the command fails after the event
// is recorded; the event count is what this guards.
func TestMetricsWispAliasEmitsSingleEvent(t *testing.T) {
	bd := buildEmbeddedBD(t)
	home, err := testTempDir("bd-metrics-wisp-home-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	repo, err := testTempDir("bd-metrics-wisp-repo-*")
	if err != nil {
		t.Fatalf("temp repo: %v", err)
	}
	initGitRepoAt(t, repo)

	// A store is needed so `bd mol wisp <proto>` reaches its RunE body.
	if _, errOut := runBdForMetrics(t, bd, repo, home, "init", "--non-interactive", "--quiet"); errOut != "" {
		_ = errOut
	}

	// Isolate the next invocation by dropping init's queued event.
	if err := os.RemoveAll(filepath.Join(home, ".beads", "eventsData")); err != nil {
		t.Fatalf("clear eventsData: %v", err)
	}

	// A non-existent proto makes the create fail, but the "wisp" event is still
	// recorded before delegation, which is exactly what this regression checks.
	_, errOut := runBdForMetrics(t, bd, repo, home, "mol", "wisp", "mol-nonexistent-proto")

	got := allCommandEvents(t, home)
	if len(got) != 1 || got[0] != "wisp" {
		t.Errorf("bd mol wisp <proto> emitted %v, want exactly [wisp] (double-emit regression)\nstderr:\n%s", got, errOut)
	}
}

// TestMetricsRootVersionFlagSuppressesFirstRunNotice is the fresh-home regression
// for PR #4419: `bd --version` and `bd -V` are version probes (like the `version`
// subcommand) and must not print the one-time consent notice to stderr or mark it
// shown, even on a brand-new home with metrics enabled. A positive control proves
// the harness can still fire the notice for a non-suppressed command, so a missing
// notice below reflects real suppression rather than a dead test.
func TestMetricsRootVersionFlagSuppressesFirstRunNotice(t *testing.T) {
	bd := buildEmbeddedBD(t)
	const noticeMarker = "anonymous usage metrics"

	noticeShown := func(home string) bool {
		b, err := os.ReadFile(filepath.Join(home, ".config", "bd", "config.yaml"))
		if err != nil {
			return false
		}
		return strings.Contains(string(b), "notice_shown")
	}

	// Positive control: a non-suppressed command on a fresh home prints the notice
	// (the consent notice fires before the no-database exit), so the negative
	// assertions below are meaningful.
	control, err := testTempDir("bd-metrics-version-control-home-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	controlRepo, err := testTempDir("bd-metrics-version-control-repo-*")
	if err != nil {
		t.Fatalf("temp repo: %v", err)
	}
	if _, errOut := runBdForMetrics(t, bd, controlRepo, control, "list"); !strings.Contains(errOut, noticeMarker) {
		t.Fatalf("positive control: `bd list` on a fresh home should print the first-run notice; stderr:\n%s", errOut)
	}

	for _, flag := range []string{"--version", "-V"} {
		flag := flag
		t.Run("root "+flag+" suppresses notice", func(t *testing.T) {
			home, err := testTempDir("bd-metrics-version-home-*")
			if err != nil {
				t.Fatalf("temp home: %v", err)
			}
			repo, err := testTempDir("bd-metrics-version-repo-*")
			if err != nil {
				t.Fatalf("temp repo: %v", err)
			}
			stdout, errOut := runBdForMetrics(t, bd, repo, home, flag)
			if !strings.Contains(stdout, "bd version") {
				t.Errorf("`bd %s` should print version to stdout, got stdout:\n%s\nstderr:\n%s", flag, stdout, errOut)
			}
			if strings.Contains(errOut, noticeMarker) {
				t.Errorf("`bd %s` printed the first-run metrics notice to stderr (must be suppressed):\n%s", flag, errOut)
			}
			if noticeShown(home) {
				t.Errorf("`bd %s` marked metrics.notice_shown in user config (must not for a version probe)", flag)
			}
		})
	}
}

// metrics off must not queue a usage event, honoring the "No usage data will be
// collected or sent" promise even though metrics are still enabled for the
// off invocation itself.
func TestMetricsOffEmitsNoEvent(t *testing.T) {
	bd := buildEmbeddedBD(t)
	home, err := testTempDir("bd-metrics-off-home-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	repo, err := testTempDir("bd-metrics-off-repo-*")
	if err != nil {
		t.Fatalf("temp repo: %v", err)
	}
	initGitRepoAt(t, repo)

	// Establish the metrics-enabled baseline and confirm a normal command emits.
	runBdForMetrics(t, bd, repo, home, "init", "--non-interactive", "--quiet")
	if got := allCommandEvents(t, home); len(got) == 0 {
		t.Fatalf("precondition: metrics-enabled `bd init` produced no event; env may be misconfigured")
	}
	if err := os.RemoveAll(filepath.Join(home, ".beads", "eventsData")); err != nil {
		t.Fatalf("clear eventsData: %v", err)
	}

	_, errOut := runBdForMetrics(t, bd, repo, home, "metrics", "off")

	if got := allCommandEvents(t, home); len(got) != 0 {
		t.Errorf("bd metrics off emitted %v, want no events\nstderr:\n%s", got, errOut)
	}
}
