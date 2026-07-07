package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitDisabledKeepsEnabledFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	closeFn, err := Init("0.0.0-test", false, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closeFn(context.Background())

	if Enabled() {
		t.Fatalf("Enabled() = true, want false")
	}

	evt := NewCommandEvent("init")
	Global().CloseEventAndAdd(evt)
	closeFn(context.Background())

	dir := filepath.Join(home, ".beads", "eventsData")
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".evtq" {
				t.Errorf("disabled Init produced .evtq file: %s", e.Name())
			}
		}
	}
}

func TestInitEnabledFlipsEnabledTrue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	closeFn, err := Init("0.0.0-test", true, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closeFn(context.Background())

	if !Enabled() {
		t.Fatalf("Enabled() = false, want true")
	}

	evt := NewCommandEvent("init")
	evt.SetAttribute("dolt_mode", "embedded")
	Global().CloseEventAndAdd(evt)
	closeFn(context.Background())

	dir := filepath.Join(home, ".beads", "eventsData")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read eventsData: %v", err)
	}
	var found bool
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".evtq" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("enabled Init did not produce any .evtq file in %s", dir)
	}
}

func TestRunSendMetricsNoOpWhenDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Init("0.0.0-test", false, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if code := RunSendMetrics(); code != 0 {
		t.Errorf("RunSendMetrics() = %d, want 0", code)
	}
}

func TestMaybeSpawnFlusherNoOpWhenDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Init("0.0.0-test", false, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	MaybeSpawnFlusher()
}

// TestFlusherChildEnvPinsSanctionedEndpoint is the security regression for the
// blocker on PR #4419: the detached send-metrics child must not be able to pick
// up a BEADS_METRICS_ENDPOINT that a project .beads/.env loaded into the parent
// environment. flusherChildEnv must drop any inherited endpoint and pin it to
// the value the parent already resolved from env + user-global config.
func TestFlusherChildEnvPinsSanctionedEndpoint(t *testing.T) {
	parent := []string{
		"HOME=/home/user",
		"PATH=/usr/bin",
		// A hostile project .beads/.env redirected the endpoint into the parent.
		EnvEndpoint + "=https://attacker.example/collect",
	}
	const sanctioned = "https://gastownhall-eventsapi.com/mp/collect"

	got := flusherChildEnv(parent, sanctioned)

	// Unrelated environment is preserved so the child can still find HOME/PATH.
	if !envContains(got, "HOME=/home/user") || !envContains(got, "PATH=/usr/bin") {
		t.Errorf("flusherChildEnv dropped unrelated vars: %v", got)
	}

	// The endpoint is pinned to the sanctioned value exactly once; the
	// project-injected attacker value is gone.
	var endpoints []string
	for _, kv := range got {
		if strings.HasPrefix(kv, EnvEndpoint+"=") {
			endpoints = append(endpoints, kv)
		}
	}
	if len(endpoints) != 1 || endpoints[0] != EnvEndpoint+"="+sanctioned {
		t.Errorf("endpoint env = %v, want exactly [%s=%s]", endpoints, EnvEndpoint, sanctioned)
	}

	// The flusher marker is set so the child cannot spawn another flusher.
	if !envContains(got, EnvIsFlusher+"=1") {
		t.Errorf("flusherChildEnv did not set %s=1: %v", EnvIsFlusher, got)
	}
}

// TestMaybeSpawnFlusherNoOpInsideFlusher guards the structural no-recursion
// guard: a process already marked as the flusher must never spawn another one,
// independent of send-metrics' os.Exit.
func TestMaybeSpawnFlusherNoOpInsideFlusher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvIsFlusher, "1")
	if _, err := Init("0.0.0-test", true, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Enabled() is true here; the only thing preventing a spawn is the marker.
	// If the guard regresses this would fork a real child process.
	MaybeSpawnFlusher()
}

// TestCloseAndFlushPersistsQueuedEvents is the regression for the os.Exit
// metrics-cleanup finding on PR #4419: the reachable os.Exit guards (CheckReadonly
// and the pre-run gates in main) finalize metrics through CloseAndFlush instead
// of bypassing main()'s post-command tail, so an event queued earlier in the run
// is still written to disk for the uploader rather than stranded.
func TestCloseAndFlushPersistsQueuedEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Keep the detached uploader from actually forking during the test; we only
	// assert the on-disk write that CloseAndFlush guarantees before an os.Exit.
	t.Setenv(EnvDisableEventFlush, "1")

	if _, err := Init("0.0.0-test", true, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	evt := NewCommandEvent("create")
	Global().CloseEventAndAdd(evt)

	// Simulate an os.Exit guard finalizing metrics without the RunE/ExecuteC tail.
	CloseAndFlush()

	dir := filepath.Join(home, ".beads", "eventsData")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read eventsData: %v", err)
	}
	var found bool
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".evtq" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("CloseAndFlush did not persist the queued event to a .evtq in %s", dir)
	}
}

// TestCloseAndFlushDisabledIsSafe ensures the os.Exit guards can call CloseAndFlush
// when metrics are disabled without panicking, spawning a flusher, or writing any
// queue file.
func TestCloseAndFlushDisabledIsSafe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvDisableEventFlush, "1")

	if _, err := Init("0.0.0-test", false, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	CloseAndFlush()

	dir := filepath.Join(home, ".beads", "eventsData")
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".evtq" {
				t.Errorf("disabled CloseAndFlush produced .evtq file: %s", e.Name())
			}
		}
	}
}

func envContains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
