//go:build !windows

package doltserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a watcher clock whose sleep advances time without waiting.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

// stubIdleWatchSpawner installs a spawner that launches nothing and names
// this test process as the watcher, and returns the options it was called
// with.
func stubIdleWatchSpawner(t *testing.T) *[]IdleWatchOpts {
	t.Helper()
	var calls []IdleWatchOpts
	orig := IdleWatchSpawner
	IdleWatchSpawner = func(opts IdleWatchOpts) (int, error) {
		calls = append(calls, opts)
		return os.Getpid(), nil
	}
	t.Cleanup(func() { IdleWatchSpawner = orig })
	return &calls
}

// autoStartForTest auto-starts beadsDir's server through EnsureRunningDetailed
// and makes sure it dies with the test.
func autoStartForTest(t *testing.T, beadsDir string) *State {
	t.Helper()
	if _, startedByUs, err := EnsureRunningDetailed(beadsDir); err != nil || !startedByUs {
		t.Fatalf("EnsureRunningDetailed = (startedByUs %v, %v), want a server started", startedByUs, err)
	}
	state, err := IsRunning(beadsDir)
	if err != nil || !state.Running {
		t.Fatalf("IsRunning after auto-start = (%+v, %v)", state, err)
	}
	pid := state.PID
	t.Cleanup(func() {
		if p, findErr := os.FindProcess(pid); findErr == nil {
			_ = p.Kill()
		}
	})
	return state
}

// quietStatus is a server with one connection open (the poll's own) and no
// connection accepted since the last poll beyond the poll's own dial.
func quietStatus(connections *int64) func(context.Context, string, int) (serverStatus, error) {
	return func(context.Context, string, int) (serverStatus, error) {
		*connections++
		return serverStatus{connections: *connections, threads: 1, ownDials: 1}, nil
	}
}

func testIdleWatcher(opts IdleWatchOpts, clock *fakeClock, read func(context.Context, string, int) (serverStatus, error)) *idleWatcher {
	w := newIdleWatcher(opts)
	w.now = clock.Now
	w.sleep = clock.Sleep
	w.readStatus = read
	return w
}

// TestIdleDecision pins when the tracker calls a server idle.
func TestIdleDecision(t *testing.T) {
	const window = 60 * time.Second
	quiet := idleSample{status: serverStatus{connections: 7, threads: 1, ownDials: 1}}
	for _, tc := range []struct {
		name    string
		samples []idleSample // one per 10s poll, starting at t0
		want    bool         // idle at t0 + window + 10s
	}{
		{name: "quiet for the window", samples: []idleSample{quiet, quiet, quiet, quiet, quiet, quiet, quiet, quiet}, want: true},
		{name: "no poll succeeded", samples: []idleSample{{statusErr: errors.New("refused")}}, want: false},
		{name: "a failed poll restarts the window", samples: []idleSample{quiet, quiet, quiet, {statusErr: errors.New("refused")}, quiet, quiet, quiet, quiet}, want: false},
		// A client that connects and disconnects between two polls is seen
		// only as a Connections increment beyond the poll's own dial.
		{name: "connection churn between polls", samples: []idleSample{quiet, quiet, quiet, quiet,
			{status: serverStatus{connections: 9, threads: 1, ownDials: 1}}, {status: serverStatus{connections: 9, threads: 1, ownDials: 0}},
			{status: serverStatus{connections: 9, threads: 1}}, {status: serverStatus{connections: 9, threads: 1}}}, want: false},
		{name: "the poll's own retried dials are not activity", samples: []idleSample{
			{status: serverStatus{connections: 3, threads: 1, ownDials: 1}}, {status: serverStatus{connections: 6, threads: 1, ownDials: 3}},
			{status: serverStatus{connections: 7, threads: 1, ownDials: 1}}, {status: serverStatus{connections: 8, threads: 1, ownDials: 1}},
			{status: serverStatus{connections: 9, threads: 1, ownDials: 1}}, {status: serverStatus{connections: 10, threads: 1, ownDials: 1}},
			{status: serverStatus{connections: 11, threads: 1, ownDials: 1}}, {status: serverStatus{connections: 12, threads: 1, ownDials: 1}}}, want: true},
		{name: "one held connection", samples: []idleSample{quiet, quiet, quiet, quiet, quiet, quiet, quiet,
			{status: serverStatus{connections: 7, threads: 2, ownDials: 1}}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t0 := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
			tr := newIdleTracker(window, t0)
			at := t0
			for _, s := range tc.samples {
				tr.observe(at, s)
				at = at.Add(10 * time.Second)
			}
			if got := tr.idle(t0.Add(window + 10*time.Second)); got != tc.want {
				t.Errorf("idle = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIdleWatch_ReapsIdleAutoStartedServer: an auto-started server gets a
// watcher, the watcher stops it after the window with no client, the pid and
// port files go, and the next auto-start brings a server back.
func TestIdleWatch_ReapsIdleAutoStartedServer(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	calls := stubIdleWatchSpawner(t)

	state := autoStartForTest(t, beadsDir)
	if len(*calls) != 1 {
		t.Fatalf("spawner called %d times, want once for the auto-start", len(*calls))
	}
	opts := (*calls)[0]
	if opts.PID != state.PID || opts.Port != state.Port || opts.ServerDir != beadsDir || opts.Timeout != DefaultIdleTimeout {
		t.Fatalf("watcher opts = %+v, want PID %d port %d dir %s timeout %s", opts, state.PID, state.Port, beadsDir, DefaultIdleTimeout)
	}
	if _, err := os.Stat(watcherPIDPath(beadsDir)); err != nil {
		t.Fatalf("watcher.pid not recorded: %v", err)
	}
	if !IdleWatchRunning(beadsDir) {
		t.Error("IdleWatchRunning = false for a server whose watcher is recorded and alive")
	}

	clock := newFakeClock()
	var conns int64
	opts.Timeout = time.Minute
	outcome, err := testIdleWatcher(opts, clock, quietStatus(&conns)).run(context.Background())
	if err != nil || outcome != IdleWatchReaped {
		t.Fatalf("run = (%s, %v), want reaped", outcome, err)
	}
	if IdleWatchRunning(beadsDir) {
		t.Error("IdleWatchRunning = true after the reap removed watcher.pid")
	}
	if isProcessAlive(state.PID) {
		t.Errorf("server PID %d still running after the reap", state.PID)
	}
	for _, p := range []string{pidPath(beadsDir), portPath(beadsDir), watcherPIDPath(beadsDir)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s kept after the reap (stat err %v)", filepath.Base(p), err)
		}
	}
	logText, _ := os.ReadFile(logPath(beadsDir))
	if !strings.Contains(string(logText), "after 1m0s with no clients") {
		t.Errorf("server log does not record the idle stop:\n%s", logText)
	}

	next := autoStartForTest(t, beadsDir)
	if next.PID == state.PID {
		t.Errorf("next auto-start reports the reaped PID %d", next.PID)
	}
}

// TestIdleWatch_LeaseKeepsServerAlive: an open store's client lease keeps an
// idle server up, and the server is stopped within the window plus one poll
// after the store closes or its process dies.
func TestIdleWatch_LeaseKeepsServerAlive(t *testing.T) {
	const window = time.Minute
	const holdFor = 5 * time.Minute

	t.Run("store closes", func(t *testing.T) {
		beadsDir, _ := fakeStartWorkspace(t)
		calls := stubIdleWatchSpawner(t)
		state := autoStartForTest(t, beadsDir)
		release, err := AcquireClientLease(beadsDir)
		if err != nil {
			t.Fatalf("AcquireClientLease: %v", err)
		}

		clock := newFakeClock()
		t0 := clock.Now()
		var conns int64
		quiet := quietStatus(&conns)
		var releasedAt time.Time
		read := func(ctx context.Context, host string, port int) (serverStatus, error) {
			if releasedAt.IsZero() && clock.Now().Sub(t0) >= holdFor {
				release()
				releasedAt = clock.Now()
			}
			return quiet(ctx, host, port)
		}
		opts := (*calls)[0]
		opts.Timeout = window
		w := testIdleWatcher(opts, clock, read)
		outcome, err := w.run(context.Background())
		if err != nil || outcome != IdleWatchReaped {
			t.Fatalf("run = (%s, %v), want reaped", outcome, err)
		}
		if releasedAt.IsZero() {
			t.Fatalf("server reaped at +%s while the lease was held", clock.Now().Sub(t0))
		}
		if late := clock.Now().Sub(releasedAt); late > window+w.pollEvery {
			t.Errorf("reaped %s after the store closed, want within %s", late, window+w.pollEvery)
		}
		if isProcessAlive(state.PID) {
			t.Errorf("server PID %d still running", state.PID)
		}
	})

	t.Run("store process dies", func(t *testing.T) {
		beadsDir, _ := fakeStartWorkspace(t)
		calls := stubIdleWatchSpawner(t)
		state := autoStartForTest(t, beadsDir)

		// A lease left by a store process that dies at t0+holdFor.
		const storePID = 1<<22 + 4242
		if err := ensureActivityDir(beadsDir); err != nil {
			t.Fatal(err)
		}
		lease := filepath.Join(activityDir(beadsDir), fmt.Sprintf("client-%d-0a0b0c", storePID))
		if err := os.WriteFile(lease, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		clock := newFakeClock()
		t0 := clock.Now()
		diesAt := t0.Add(holdFor)
		var conns int64
		opts := (*calls)[0]
		opts.Timeout = window
		w := testIdleWatcher(opts, clock, quietStatus(&conns))
		w.alive = func(pid int) bool {
			if pid == storePID {
				return clock.Now().Before(diesAt)
			}
			return isProcessAlive(pid)
		}
		outcome, err := w.run(context.Background())
		if err != nil || outcome != IdleWatchReaped {
			t.Fatalf("run = (%s, %v), want reaped", outcome, err)
		}
		if clock.Now().Before(diesAt) {
			t.Fatalf("server reaped at +%s while the store process was alive", clock.Now().Sub(t0))
		}
		if late := clock.Now().Sub(diesAt); late > window+w.pollEvery {
			t.Errorf("reaped %s after the store process died, want within %s", late, window+w.pollEvery)
		}
		if _, err := os.Stat(lease); !os.IsNotExist(err) {
			t.Errorf("dead process's lease kept (stat err %v)", err)
		}
		if isProcessAlive(state.PID) {
			t.Errorf("server PID %d still running", state.PID)
		}
	})
}

// TestAcquireClientLease_WaitsForInFlightReap: a store that opens while a
// watcher is stopping the server waits for the stop to finish, so its
// auto-start finds the server gone rather than racing the stop.
func TestAcquireClientLease_WaitsForInFlightReap(t *testing.T) {
	serverDir := t.TempDir()
	lockF, err := acquireLifecycleLock(serverDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureActivityDir(serverDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reapingMarkerPath(serverDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	type result struct {
		release func()
		err     error
	}
	done := make(chan result, 1)
	go func() {
		release, err := AcquireClientLease(serverDir)
		done <- result{release, err}
	}()
	select {
	case <-done:
		releaseLifecycleLock(lockF)
		t.Fatal("AcquireClientLease returned while a reap held the lifecycle lock")
	case <-time.After(300 * time.Millisecond):
	}
	releaseLifecycleLock(lockF)
	var r result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("AcquireClientLease did not return after the reap released the lock")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !liveClientLeases(serverDir, isProcessAlive) {
		t.Error("lease not recorded")
	}
	r.release()
	if liveClientLeases(serverDir, isProcessAlive) {
		t.Error("lease kept after release")
	}
}

// TestAcquireClientLease_PrunesDeadLeases: a bd command that fails or is
// killed never closes its store, so its lease outlives it. With no watcher
// running to prune it (a server bd dolt start started, a pinned port,
// dolt.idle-timeout 0), the next lease taken on the server dir removes it,
// and keeps the lease of a process that is still alive.
func TestAcquireClientLease_PrunesDeadLeases(t *testing.T) {
	serverDir := t.TempDir()
	if err := ensureActivityDir(serverDir); err != nil {
		t.Fatal(err)
	}
	// Above Linux's largest pid_max (1<<22), so no process has it.
	const deadPID = 1<<22 + 4242
	dead := filepath.Join(activityDir(serverDir), fmt.Sprintf("%s%d-0a0b0c", clientLeasePrefix, deadPID))
	live := filepath.Join(activityDir(serverDir), fmt.Sprintf("%s%d-0d0e0f", clientLeasePrefix, os.Getpid()))
	for _, p := range []string{dead, live} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if IdleWatchRunning(serverDir) {
		t.Fatal("premise: no watcher runs for this server dir")
	}

	release, err := AcquireClientLease(serverDir)
	if err != nil {
		t.Fatalf("AcquireClientLease: %v", err)
	}
	defer release()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the dead process's lease was kept (stat err %v)", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("a live process's lease was removed: %v", err)
	}
}

// TestStart_ExplicitStartLaunchesNoIdleWatcher: `bd dolt start` and
// `bd dolt restart` launch no watcher, and a start that adopts an
// auto-started server cancels its watcher, which then exits without
// stopping the server however long it stays idle.
func TestStart_ExplicitStartLaunchesNoIdleWatcher(t *testing.T) {
	t.Run("fresh start", func(t *testing.T) {
		beadsDir, _ := fakeStartWorkspace(t)
		calls := stubIdleWatchSpawner(t)
		if _, err := startForTest(t, beadsDir); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 0 {
			t.Errorf("Start launched %d watcher(s), want none", len(*calls))
		}
		if _, err := os.Stat(watcherPIDPath(beadsDir)); !os.IsNotExist(err) {
			t.Errorf("watcher.pid recorded for an explicit start (stat err %v)", err)
		}
	})

	for _, explicit := range []struct {
		name string
		run  func(string) (*State, error)
	}{
		{name: "start adopts an auto-started server", run: Start},
		{name: "restart replaces an auto-started server", run: Restart},
	} {
		t.Run(explicit.name, func(t *testing.T) {
			beadsDir, _ := fakeStartWorkspace(t)
			calls := stubIdleWatchSpawner(t)
			autoStartForTest(t, beadsDir)
			opts := (*calls)[0]

			state, err := explicit.run(beadsDir)
			if state != nil && state.PID > 0 {
				pid := state.PID
				t.Cleanup(func() {
					if p, findErr := os.FindProcess(pid); findErr == nil {
						_ = p.Kill()
					}
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(*calls) != 1 {
				t.Errorf("explicit start launched a watcher (%d spawns)", len(*calls))
			}
			if _, err := os.Stat(watcherPIDPath(beadsDir)); !os.IsNotExist(err) {
				t.Errorf("watcher.pid kept after the explicit start (stat err %v)", err)
			}

			// The auto-start's watcher, still running, must leave the
			// explicitly started server alone.
			opts.PID, opts.Port, opts.Timeout = state.PID, state.Port, time.Minute
			clock := newFakeClock()
			var conns int64
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := testIdleWatcher(opts, clock, quietStatus(&conns))
			sleep := w.sleep
			w.sleep = func(ctx context.Context, d time.Duration) error {
				if clock.Now().Sub(newFakeClock().Now()) > time.Hour {
					cancel()
				}
				return sleep(ctx, d)
			}
			outcome, err := w.run(ctx)
			if err != nil || outcome == IdleWatchReaped {
				t.Errorf("watcher run = (%s, %v), want it to leave the server alone", outcome, err)
			}
			if !isProcessAlive(state.PID) {
				t.Errorf("explicitly started server PID %d was stopped", state.PID)
			}
		})
	}
}

// TestIdleTimeoutResolution pins where the idle window comes from:
// BEADS_DOLT_IDLE_TIMEOUT, then dolt.idle-timeout in config.yaml, then 30m.
func TestIdleTimeoutResolution(t *testing.T) {
	for _, tc := range []struct {
		name, env, yaml string
		want            time.Duration
		warn            bool
	}{
		{name: "unset gives the default", want: DefaultIdleTimeout},
		{name: "env 0 turns it off", env: "0", want: 0},
		{name: "env duration", env: "45m", want: 45 * time.Minute},
		{name: "env wins over config", env: "10m", yaml: "dolt:\n  idle-timeout: 2h\n", want: 10 * time.Minute},
		{name: "config 0 turns it off", yaml: "dolt:\n  idle-timeout: 0\n", want: 0},
		{name: "config duration", yaml: "dolt:\n  idle-timeout: 2h\n", want: 2 * time.Hour},
		{name: "invalid env warns and keeps the default", env: "soon", want: DefaultIdleTimeout, warn: true},
		{name: "negative config warns and keeps the default", yaml: "dolt:\n  idle-timeout: -5m\n", want: DefaultIdleTimeout, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if tc.yaml != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(tc.yaml), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(IdleTimeoutEnv, tc.env)
			var got time.Duration
			stderr := captureStderr(t, func() { got = ResolveIdleTimeout(beadsDir) })
			if got != tc.want {
				t.Errorf("ResolveIdleTimeout = %s, want %s", got, tc.want)
			}
			if warned := strings.Contains(stderr, "Warning:"); warned != tc.warn {
				t.Errorf("warning printed = %v, want %v (stderr %q)", warned, tc.warn, stderr)
			}
		})
	}
}

// TestAutoStart_IdleTimeoutZeroLaunchesNoWatcher: with the idle window set to
// 0, through the environment or config.yaml, an auto-start launches no
// watcher, and an unset window launches one with the 30m default.
func TestAutoStart_IdleTimeoutZeroLaunchesNoWatcher(t *testing.T) {
	for _, tc := range []struct {
		name, env, yaml string
		wantSpawns      int
	}{
		{name: "env 0", env: "0"},
		{name: "config.yaml 0", yaml: "dolt:\n  idle-timeout: 0\n"},
		{name: "unset", wantSpawns: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, _ := fakeStartWorkspace(t)
			if tc.yaml != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(tc.yaml), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(IdleTimeoutEnv, tc.env)
			calls := stubIdleWatchSpawner(t)
			autoStartForTest(t, beadsDir)
			if len(*calls) != tc.wantSpawns {
				t.Fatalf("auto-start launched %d watcher(s), want %d", len(*calls), tc.wantSpawns)
			}
			if tc.wantSpawns == 1 && (*calls)[0].Timeout != DefaultIdleTimeout {
				t.Errorf("watcher timeout = %s, want %s", (*calls)[0].Timeout, DefaultIdleTimeout)
			}
			if _, err := os.Stat(watcherPIDPath(beadsDir)); (tc.wantSpawns == 0) != os.IsNotExist(err) {
				t.Errorf("watcher.pid presence wrong for %d spawn(s) (stat err %v)", tc.wantSpawns, err)
			}
		})
	}
}

// TestIdleWatch_ExitsWhenServerGone: when the server stops or is replaced by
// other means, the watcher notices within 5 seconds and exits without
// stopping anything, even when its idle poll is minutes away.
func TestIdleWatch_ExitsWhenServerGone(t *testing.T) {
	const serverPID = 4242
	for _, tc := range []struct {
		name   string
		vanish func(serverDir string, alive *bool) error
	}{
		{name: "server process died", vanish: func(_ string, alive *bool) error {
			*alive = false
			return nil
		}},
		{name: "stopped elsewhere, pid file removed", vanish: func(serverDir string, _ *bool) error {
			return os.Remove(pidPath(serverDir))
		}},
		{name: "replaced by another server", vanish: func(serverDir string, _ *bool) error {
			return os.WriteFile(pidPath(serverDir), []byte(fmt.Sprintf("%d\n", serverPID+1)), 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverDir := t.TempDir()
			if err := os.WriteFile(pidPath(serverDir), []byte(fmt.Sprintf("%d\n", serverPID)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := ensureActivityDir(serverDir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(watcherPIDPath(serverDir), []byte(fmt.Sprintf("%d\n30m0s\n", os.Getpid())), 0o600); err != nil {
				t.Fatal(err)
			}

			opts := IdleWatchOpts{ServerDir: serverDir, PID: serverPID, Host: "127.0.0.1", Port: 3307, Timeout: 30 * time.Minute}
			clock := newFakeClock()
			var conns int64
			w := testIdleWatcher(opts, clock, quietStatus(&conns))
			serverAlive := true
			w.alive = func(pid int) bool { return pid == serverPID && serverAlive }
			stops := 0
			w.stop = func(string) error { stops++; return nil }

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var goneAt time.Time
			sleep := w.sleep
			w.sleep = func(ctx context.Context, d time.Duration) error {
				switch {
				case goneAt.IsZero():
					if err := tc.vanish(serverDir, &serverAlive); err != nil {
						t.Fatal(err)
					}
					goneAt = clock.Now()
				case clock.Now().Sub(goneAt) >= 5*time.Second:
					cancel() // still watching 5s after the server went away
				}
				return sleep(ctx, d)
			}

			outcome, err := w.run(ctx)
			if err != nil || outcome != IdleWatchServerGone {
				t.Fatalf("watcher run = (%s, %v), want %s", outcome, err, IdleWatchServerGone)
			}
			if waited := clock.Now().Sub(goneAt); waited > 5*time.Second {
				t.Errorf("watcher exited %s after the server went away, want within 5s", waited)
			}
			if stops != 0 {
				t.Errorf("watcher called stop %d time(s) for a server it no longer owns", stops)
			}
		})
	}
}
