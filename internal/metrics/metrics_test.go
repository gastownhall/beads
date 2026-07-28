package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataDirDefaultUsesHomeBeads(t *testing.T) {
	home := isolateUserProfile(t)

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	want := filepath.Join(home, ".beads", "eventsData")
	if got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestDataDirRespectsBeadsDir(t *testing.T) {
	home := isolateUserProfile(t)
	beadsDir := filepath.Join(t.TempDir(), "custom-beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	want := filepath.Join(beadsDir, "eventsData")
	if got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
	// Must not place events under the default home .beads when BEADS_DIR is set.
	if strings.HasPrefix(got, filepath.Join(home, ".beads")) {
		t.Fatalf("DataDir() = %q still under $HOME/.beads despite BEADS_DIR", got)
	}
}

// TestDataDirIgnoresMissingBeadsDir is the regression for the canary in
// TestInitBackendFlag/sqlite_is_no_longer_supported: telemetry must not be what
// creates a workspace. When BEADS_DIR names a directory that does not exist yet,
// queue events under the home dir so a command rejected before it established
// its workspace leaves nothing behind.
func TestDataDirIgnoresMissingBeadsDir(t *testing.T) {
	home := isolateUserProfile(t)
	beadsDir := filepath.Join(t.TempDir(), "not-created-yet")
	t.Setenv("BEADS_DIR", beadsDir)

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := filepath.Join(home, ".beads", "eventsData"); got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
	if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
		t.Fatalf("DataDir() created %s (stat error: %v)", beadsDir, err)
	}
}

func TestInitDisabledKeepsEnabledFalse(t *testing.T) {
	home := isolateUserProfile(t)

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
	home := isolateUserProfile(t)

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
	if want := filepath.Join(home, ".beads", "eventsData"); dir != want {
		t.Fatalf("DataDir() = %q, want %q", dir, want)
	}
	if err := AttachFileEmitter(dir); err != nil {
		t.Fatalf("AttachFileEmitter: %v", err)
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

// TestInitDoesNotCreateDataDirBeforeAttach is the regression for the eager
// mkdir bug (GH#4807): Init alone (before AttachFileEmitter) must not touch
// disk, because at PersistentPreRunE time the workspace directory may not be
// resolved yet (see applyChangeDirSelection in cmd/bd/main.go). Only
// AttachFileEmitter, called after the workspace is known, may create it.
func TestInitDoesNotCreateDataDirBeforeAttach(t *testing.T) {
	home := isolateUserProfile(t)

	closeFn, err := Init("0.0.0-test", true, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closeFn(context.Background())

	dir := filepath.Join(home, ".beads", "eventsData")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Init created %s before AttachFileEmitter (stat error: %v)", dir, err)
	}
}

func TestRunSendMetricsNoOpWhenDisabled(t *testing.T) {
	isolateUserProfile(t)
	_, err := Init("0.0.0-test", false, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if code := RunSendMetrics(); code != 0 {
		t.Errorf("RunSendMetrics() = %d, want 0", code)
	}
}

func TestMaybeSpawnFlusherNoOpWhenDisabled(t *testing.T) {
	isolateUserProfile(t)
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

	got := flusherChildEnv(parent, sanctioned, "")

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

// TestFlusherChildEnvPinsResolvedDataDir covers the parent/child split-write
// half of GH#4807: the parent writes its queue under the workspace it selected,
// so the child must flush that same directory instead of re-deriving one from an
// inherited BEADS_DIR.
func TestFlusherChildEnvPinsResolvedDataDir(t *testing.T) {
	parent := []string{
		"BEADS_DIR=/ambient/workspace",
		// A previous run leaked its own resolved dir into the environment.
		EnvDataDir + "=/stale/eventsData",
	}
	const resolved = "/selected/workspace/eventsData"

	got := flusherChildEnv(parent, "", resolved)

	var dirs []string
	for _, kv := range got {
		if strings.HasPrefix(kv, EnvDataDir+"=") {
			dirs = append(dirs, kv)
		}
	}
	if len(dirs) != 1 || dirs[0] != EnvDataDir+"="+resolved {
		t.Errorf("data dir env = %v, want exactly [%s=%s]", dirs, EnvDataDir, resolved)
	}

	// BEADS_DIR itself is still handed through; only the queue path is pinned.
	if !envContains(got, "BEADS_DIR=/ambient/workspace") {
		t.Errorf("flusherChildEnv dropped BEADS_DIR: %v", got)
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
	home := isolateUserProfile(t)
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
	if err := AttachFileEmitter(dir); err != nil {
		t.Fatalf("AttachFileEmitter: %v", err)
	}

	evt := NewCommandEvent("create")
	Global().CloseEventAndAdd(evt)

	// Simulate an os.Exit guard finalizing metrics without the RunE/ExecuteC tail.
	CloseAndFlush()

	if want := filepath.Join(home, ".beads", "eventsData"); dir != want {
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
	home := isolateUserProfile(t)
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
