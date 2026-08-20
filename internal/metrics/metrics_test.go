package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestRunSendMetricsDisabledPrunesWithoutUploading is the regression for
// GH#5712: RunSendMetrics used to early-return on !Enabled() BEFORE PruneQueue,
// so the machine that just opted out of telemetry — the one whose queue can
// never again drain by upload — kept its eventsData backlog forever (2M+ files
// / 15.8GB observed on one control VM). Disabled mode must prune by the normal
// policy and must not upload.
func TestRunSendMetricsDisabledPrunesWithoutUploading(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".beads", "eventsData")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir eventsData: %v", err)
	}
	stale := filepath.Join(dir, "stale"+queuedEventExt)
	orphan := filepath.Join(dir, writeTempPrefix+"orphan")
	fresh := filepath.Join(dir, "fresh"+queuedEventExt)
	for _, p := range []string{stale, orphan, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	old := time.Now().Add(-pruneTTL - time.Hour)
	for _, p := range []string{stale, orphan} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	// The endpoint is unreachable by construction: if the disabled path ever
	// fell through to the upload half, Flush would fail on fresh.evtq and
	// RunSendMetrics would return nonzero.
	if _, err := Init("0.0.0-test", false, "http://127.0.0.1:1/collect"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if code := RunSendMetrics(); code != 0 {
		t.Fatalf("RunSendMetrics() = %d, want 0 (disabled mode must prune and skip the upload)", code)
	}

	for _, p := range []string{stale, orphan} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived a disabled-mode prune (stat err %v)", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh batch did not survive: %v (disabled mode prunes by the same TTL/cap policy, it does not purge)", err)
	}
}

// TestSpawnGateIgnoresDisabledMetrics pins the spawn-side half of the GH#5712
// fix: the env half of the spawn gate must NOT require Enabled(), or the
// disable path could never schedule the prune-only child that drains a
// leftover queue. The stateful half still applies — with no queued backlog
// nothing is due, so a machine that never enabled telemetry never forks.
func TestSpawnGateIgnoresDisabledMetrics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Hermetically clear the two env suppressors (the repo's own runner exports
	// BEADS_TEST_MODE=1, and CI exports BD_DISABLE_EVENT_FLUSH=1 workflow-wide)
	// so the assertion below is gated on Enabled() alone. shouldSpawnFlusher is
	// a pure decision — nothing forks here.
	t.Setenv(EnvTestMode, "")
	os.Unsetenv(EnvTestMode)
	t.Setenv(EnvDisableEventFlush, "")
	os.Unsetenv(EnvDisableEventFlush)

	if _, err := Init("0.0.0-test", false, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if Enabled() {
		t.Fatalf("Enabled() = true, want false")
	}
	if !shouldSpawnFlusher() {
		t.Errorf("shouldSpawnFlusher() = false with metrics disabled, want true (prune-only child, GH#5712)")
	}

	// Stateful half: Init(disabled) never creates eventsData, so nothing is
	// due. MaybeSpawnFlusher is exercised only with the test-mode guard
	// restored, so a regression in flusherDue cannot fork a real child.
	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if flusherDue(dir, time.Now()) {
		t.Errorf("flusherDue(no queue dir) = true, want false")
	}
	t.Setenv(EnvTestMode, "1")
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
