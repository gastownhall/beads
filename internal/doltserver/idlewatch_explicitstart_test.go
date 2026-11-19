package doltserver

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestStart_ExplicitStartLaunchesNoIdleWatcher pins the explicit half of the
// spawn rule (GH#4282): `bd dolt start` is an operator decision that this
// server should be up, so Start never launches an idle watcher for the
// server it starts, and the server it starts is still up.
func TestStart_ExplicitStartLaunchesNoIdleWatcher(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "90s")

	var spawned []IdleWatchOpts
	SetIdleWatchSpawner(func(opts IdleWatchOpts) { spawned = append(spawned, opts) })
	t.Cleanup(func() { SetIdleWatchSpawner(nil) })

	if _, err := startForTest(t, beadsDir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := launchedPorts(t, launches); len(got) != 1 {
		t.Fatalf("launched %v sql-servers, want exactly one", got)
	}
	if len(spawned) != 0 {
		t.Fatalf("Start launched %d idle watchers, want none: %+v", len(spawned), spawned)
	}

	// No watcher means nothing ends the server: still running, state files
	// still in place, past any window the config could have set.
	state, err := IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if state == nil || !state.Running {
		t.Fatal("an explicitly started server must stay running")
	}
	if _, err := os.Stat(filepath.Join(beadsDir, PIDFileName)); err != nil {
		t.Errorf("explicitly started server's pid file: %v", err)
	}
}

// TestStart_AdoptingAutoStartedServerStandsWatcherDown pins the adoption
// half: `bd dolt start` over a server an auto-start left watched adopts it
// as an explicitly started server — no watcher keeps running for it.
func TestStart_AdoptingAutoStartedServerStandsWatcherDown(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "90s")

	var spawned []IdleWatchOpts
	SetIdleWatchSpawner(func(opts IdleWatchOpts) { spawned = append(spawned, opts) })
	t.Cleanup(func() { SetIdleWatchSpawner(nil) })

	if _, _, err := EnsureRunningDetailed(beadsDir); err != nil {
		t.Fatalf("EnsureRunningDetailed: %v", err)
	}
	t.Cleanup(func() { _ = IgnoreNotRunning(Stop(beadsDir)) })
	if len(spawned) != 1 {
		t.Fatalf("auto-start launched %d idle watchers, want 1", len(spawned))
	}
	// The watcher the spawner stands for is this process; write its marker
	// the way RunIdleWatch does, so adoption has one to remove.
	if err := os.MkdirAll(filepath.Join(beadsDir, ActivityDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	watcherPID := filepath.Join(beadsDir, ActivityDirName, watcherPIDName)
	if err := os.WriteFile(watcherPID, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	// An explicit Start over the running server adopts it and stands the
	// watcher down: watcher.pid is gone and no new watcher launches.
	if _, err := startForTest(t, beadsDir); err != nil {
		t.Fatalf("Start adopting the running server: %v", err)
	}
	if len(spawned) != 1 {
		t.Fatalf("Start over a running server launched %d more idle watchers, want 0", len(spawned)-1)
	}
	if _, err := os.Stat(watcherPID); !os.IsNotExist(err) {
		t.Errorf("adoption left watcher.pid behind (err %v); the watcher never stands down", err)
	}
	if got := launchedPorts(t, launches); len(got) != 1 {
		t.Errorf("Start over a running server launched %d sql-servers, want the one already running", len(got))
	}
}
