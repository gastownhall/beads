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

	clock := newFakeClock()
	var conns int64
	opts.Timeout = time.Minute
	outcome, err := testIdleWatcher(opts, clock, quietStatus(&conns)).run(context.Background())
	if err != nil || outcome != IdleWatchReaped {
		t.Fatalf("run = (%s, %v), want reaped", outcome, err)
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
