package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// wantDataDir mirrors DataDir()'s own construction, so tests assert against
// the documented location (beside the user-global config.yaml) rather than
// duplicating a hardcoded path that could drift from the implementation.
func wantDataDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(filepath.Dir(config.UserConfigYamlPath()), "eventsData")
}

func TestDataDirUsesUserConfigDir(t *testing.T) {
	isolateUserProfile(t)

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := wantDataDir(t); got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

// TestDataDirIgnoresBeadsDir is the regression for the maintainer-reported gap
// in GH#4807 that the earlier BEADS_DIR-aware design left open: a fresh
// install with BEADS_DIR set but no workspace created yet still queued events
// under a phantom directory. Since the queue is machine-scoped, not
// workspace-scoped, DataDir() must not consult BEADS_DIR at all — set,
// unset, or naming a directory that does not exist.
func TestDataDirErrorsWithoutResolvableHome(t *testing.T) {
	// config.UserConfigYamlPath returns the literal "~/.config/bd/config.yaml"
	// display string when the home dir cannot be resolved. DataDir must reject
	// that rather than queue events into a directory named "~" under the cwd.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	got, err := DataDir()
	if err == nil {
		t.Fatalf("DataDir() = %q, want error when the home dir cannot be resolved", got)
	}
	if got != "" {
		t.Errorf("DataDir() returned path %q alongside its error, want empty", got)
	}
}

// TestDataDirErrorsOnNonAbsoluteHome guards HOME=~, which os.UserHomeDir()
// returns unchanged with no error and would otherwise yield a relative path.
func TestDataDirErrorsOnNonAbsoluteHome(t *testing.T) {
	t.Setenv("HOME", "~")
	t.Setenv("USERPROFILE", "~")

	got, err := DataDir()
	if err == nil {
		t.Fatalf("DataDir() = %q, want error when the home dir is not absolute", got)
	}
	if got != "" {
		t.Errorf("DataDir() returned path %q alongside its error, want empty", got)
	}
}

func TestDataDirIgnoresBeadsDir(t *testing.T) {
	isolateUserProfile(t)
	want := wantDataDir(t)

	for _, tc := range []struct {
		name     string
		beadsDir string
	}{
		{"unset", ""},
		{"existing_dir", filepath.Join(t.TempDir(), "existing-workspace")},
		{"not_yet_created", filepath.Join(t.TempDir(), "not-created-yet")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.beadsDir != "" {
				if tc.name == "existing_dir" {
					if err := os.MkdirAll(tc.beadsDir, 0o755); err != nil {
						t.Fatalf("mkdir workspace: %v", err)
					}
				}
				t.Setenv("BEADS_DIR", tc.beadsDir)
			} else {
				t.Setenv("BEADS_DIR", "")
			}

			got, err := DataDir()
			if err != nil {
				t.Fatalf("DataDir: %v", err)
			}
			if got != want {
				t.Fatalf("DataDir() = %q, want %q (BEADS_DIR must not affect the machine-scoped queue)", got, want)
			}
			if tc.beadsDir != "" && tc.name == "not_yet_created" {
				if _, err := os.Stat(tc.beadsDir); !os.IsNotExist(err) {
					t.Fatalf("DataDir() created %s (stat error: %v)", tc.beadsDir, err)
				}
			}
		})
	}
}

func TestInitDisabledKeepsEnabledFalse(t *testing.T) {
	isolateUserProfile(t)
	dir := wantDataDir(t)

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

	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".evtq" {
				t.Errorf("disabled Init produced .evtq file: %s", e.Name())
			}
		}
	}
}

func TestInitEnabledFlipsEnabledTrue(t *testing.T) {
	isolateUserProfile(t)

	closeFn, err := Init("0.0.0-test", true, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closeFn(context.Background())

	if !Enabled() {
		t.Fatalf("Enabled() = false, want true")
	}

	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := wantDataDir(t); dir != want {
		t.Fatalf("DataDir() = %q, want %q", dir, want)
	}

	evt := NewCommandEvent("init")
	evt.SetAttribute("dolt_mode", "embedded")
	Global().CloseEventAndAdd(evt)
	closeFn(context.Background())

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

// TestInitEnabledCreatesDataDirImmediately documents the design this PR
// reverted to: DataDir() is a fixed, machine-scoped location
// (~/.config/bd/eventsData), never a workspace directory, so it is safe for
// Init to construct the file emitter (and its eager MkdirAll) unconditionally
// and immediately, with no ordering dependency on workspace selection.
func TestInitEnabledCreatesDataDirImmediately(t *testing.T) {
	isolateUserProfile(t)

	closeFn, err := Init("0.0.0-test", true, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closeFn(context.Background())

	dir := wantDataDir(t)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("Init did not create %s: %v", dir, err)
	}
}

// TestRunSendMetricsDisabledPrunesWithoutUploading is the regression for
// GH#5712: RunSendMetrics used to early-return on !Enabled() BEFORE PruneQueue,
// so the machine that just opted out of telemetry — the one whose queue can
// never again drain by upload — kept its eventsData backlog forever (2M+ files
// / 15.8GB observed on one control VM). Disabled mode must prune by the normal
// policy and must not upload.
func TestRunSendMetricsDisabledPrunesWithoutUploading(t *testing.T) {
	_ = isolateUserProfile(t)
	dir := wantDataDir(t)
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
	t.Setenv("BEADS_DIR", "")
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
	_ = isolateUserProfile(t)
	// Hermetically clear the two env suppressors (the repo's own runner exports
	// BEADS_TEST_MODE=1, and CI exports BD_DISABLE_EVENT_FLUSH=1 workflow-wide)
	// so the assertion below is gated on Enabled() alone. shouldSpawnFlusher is
	// a pure decision — nothing forks here.
	t.Setenv(EnvTestMode, "")
	os.Unsetenv(EnvTestMode)
	t.Setenv(EnvDisableEventFlush, "")
	os.Unsetenv(EnvDisableEventFlush)

	t.Setenv("BEADS_DIR", "")
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
	isolateUserProfile(t)
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
	isolateUserProfile(t)
	// Keep the detached uploader from actually forking during the test; we only
	// assert the on-disk write that CloseAndFlush guarantees before an os.Exit.
	t.Setenv(EnvDisableEventFlush, "1")

	if _, err := Init("0.0.0-test", true, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}

	evt := NewCommandEvent("create")
	Global().CloseEventAndAdd(evt)

	// Simulate an os.Exit guard finalizing metrics without the RunE/ExecuteC tail.
	CloseAndFlush()

	if want := wantDataDir(t); dir != want {
		t.Fatalf("DataDir() = %q, want %q", dir, want)
	}
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
	isolateUserProfile(t)
	t.Setenv(EnvDisableEventFlush, "1")

	if _, err := Init("0.0.0-test", false, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	CloseAndFlush()

	dir := wantDataDir(t)
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

// TestRunSendMetricsPrunesUnderFlushDeadline is the GH#5871 ordering
// regression. RunSendMetrics advertises a flushTimeout budget for the detached
// child, but built the context only after the prune, so the prune — the
// expensive half on a backed-up spool — ran with no deadline at all and the
// advertised budget bounded nothing.
func TestRunSendMetricsPrunesUnderFlushDeadline(t *testing.T) {
	_ = isolateUserProfile(t)
	if err := os.MkdirAll(wantDataDir(t), 0o750); err != nil {
		t.Fatalf("mkdir eventsData: %v", err)
	}

	var called, hasDeadline bool
	var budget time.Duration
	orig := pruneQueueFn
	t.Cleanup(func() { pruneQueueFn = orig })
	pruneQueueFn = func(ctx context.Context, dir string, now time.Time) (int, int64) {
		called = true
		if dl, ok := ctx.Deadline(); ok {
			hasDeadline = true
			budget = time.Until(dl)
		}
		return 0, 0
	}

	if _, err := Init("0.0.0-test", false, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if code := RunSendMetrics(); code != 0 {
		t.Fatalf("RunSendMetrics() = %d, want 0", code)
	}
	if !called {
		t.Fatal("RunSendMetrics did not prune")
	}
	if !hasDeadline {
		t.Fatal("RunSendMetrics ran the prune with a deadline-free context: the advertised flushTimeout budget does not bound the child's expensive half")
	}
	if budget <= 0 || budget > flushTimeout {
		t.Errorf("prune context budget = %v, want (0, %v]", budget, flushTimeout)
	}
}
