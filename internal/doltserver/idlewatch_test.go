package doltserver

import (
	"errors"
	"testing"
	"time"
)

// TestIdleDecision drives the poller's activity decision directly, the way
// the watcher drives it against SHOW GLOBAL STATUS samples.
func TestIdleDecision(t *testing.T) {
	t.Run("first poll is never idle", func(t *testing.T) {
		var p idlePoller
		if !p.record(serverStatus{connections: 10, threadsConnected: 1}, true, nil, false) {
			t.Fatal("first poll with no baseline must count as activity")
		}
	})
	t.Run("own dial delta is idle", func(t *testing.T) {
		var p idlePoller
		p.record(serverStatus{connections: 10, threadsConnected: 1}, true, nil, false)
		if p.record(serverStatus{connections: 11, threadsConnected: 1}, true, nil, false) {
			t.Fatal("delta == the watcher's own dial must be idle")
		}
	})
	t.Run("one more connection is active", func(t *testing.T) {
		var p idlePoller
		p.record(serverStatus{connections: 10, threadsConnected: 1}, true, nil, false)
		p.record(serverStatus{connections: 11, threadsConnected: 1}, true, nil, false)
		if !p.record(serverStatus{connections: 13, threadsConnected: 1}, true, nil, false) {
			t.Fatal("a client connection between polls must be activity")
		}
	})
	t.Run("threads connected above the watcher's own is active", func(t *testing.T) {
		var p idlePoller
		p.record(serverStatus{connections: 10, threadsConnected: 1}, true, nil, false)
		if !p.record(serverStatus{connections: 11, threadsConnected: 2}, true, nil, false) {
			t.Fatal("Threads_connected > 1 must be activity")
		}
	})
	t.Run("dial or query error is active and resets the baseline", func(t *testing.T) {
		var p idlePoller
		p.record(serverStatus{connections: 10, threadsConnected: 1}, true, nil, false)
		if !p.record(serverStatus{}, false, errors.New("dial: refused"), false) {
			t.Fatal("an error must count as activity")
		}
		// The failed interval is unknown, so the next good sample is a fresh
		// baseline, not a reap decision.
		if !p.record(serverStatus{connections: 11, threadsConnected: 1}, true, nil, false) {
			t.Fatal("the first sample after an error must count as activity")
		}
		// ...and the interval after it, quiet, is idle again.
		if p.record(serverStatus{connections: 12, threadsConnected: 1}, true, nil, false) {
			t.Fatal("a quiet interval after re-establishing the baseline must be idle")
		}
	})
	t.Run("missing variable is active", func(t *testing.T) {
		var p idlePoller
		p.record(serverStatus{connections: 10, threadsConnected: 1}, true, nil, false)
		if !p.record(serverStatus{connections: 11, threadsConnected: 1}, false, nil, false) {
			t.Fatal("a missing status variable must count as activity")
		}
	})
	t.Run("live lease is active", func(t *testing.T) {
		var p idlePoller
		p.record(serverStatus{connections: 10, threadsConnected: 1}, true, nil, false)
		if !p.record(serverStatus{connections: 11, threadsConnected: 1}, true, nil, true) {
			t.Fatal("a live client lease must count as activity")
		}
	})
}

// TestIdleWatchPollInterval pins the poll cadence: one poll at every sixth
// of the window, clamped to [1s, 5m].
func TestIdleWatchPollInterval(t *testing.T) {
	cases := []struct {
		window time.Duration
		want   time.Duration
	}{
		{6 * time.Second, time.Second},
		{30 * time.Minute, 5 * time.Minute},
		{time.Hour, 5 * time.Minute},
		{18 * time.Second, 3 * time.Second},
	}
	for _, c := range cases {
		if got := clampDuration(c.window/6, time.Second, 5*time.Minute); got != c.want {
			t.Errorf("window %s: poll interval = %s, want %s", c.window, got, c.want)
		}
	}
}

// TestIdleWatchEligibility pins which auto-starts get a watcher: only an
// installed spawner, an enabled window, an owned non-shared server, and
// listeners bd chose itself (no operator-pinned port, no remotesapi).
func TestIdleWatchEligibility(t *testing.T) {
	SetIdleWatchSpawner(func(IdleWatchOpts) {})
	t.Cleanup(func() { SetIdleWatchSpawner(nil) })

	eligible := func(mode ServerMode, sharedMode bool, port int, portSource PortSource, remotesAPIPort int, timeout time.Duration, spawnerSet bool) bool {
		if !spawnerSet {
			SetIdleWatchSpawner(nil)
			defer SetIdleWatchSpawner(func(IdleWatchOpts) {})
		}
		return idleWatchEligible(mode, sharedMode, port, portSource, false, remotesAPIPort, timeout)
	}

	const somePort = 3307
	if !eligible(ServerModeOwned, false, 0, PortSourceUnset, 0, DefaultIdleTimeout, true) {
		t.Fatal("the default owned auto-start (ephemeral port) must be eligible")
	}
	// bd's own port-file record is movable (a failed bind may move ports),
	// so a previous bd-chosen port still gets a watcher.
	if !eligible(ServerModeOwned, false, somePort, PortSourcePortFile, 0, DefaultIdleTimeout, true) {
		t.Error("a port from bd's own port file must be eligible")
	}
	for name, args := range map[string]struct {
		mode           ServerMode
		sharedMode     bool
		port           int
		portSource     PortSource
		remotesAPIPort int
		timeout        time.Duration
		spawnerSet     bool
	}{
		"no spawner":       {ServerModeOwned, false, 0, PortSourceUnset, 0, DefaultIdleTimeout, false},
		"timeout disabled": {ServerModeOwned, false, 0, PortSourceUnset, 0, 0, true},
		"external mode":    {ServerModeExternal, false, 0, PortSourceUnset, 0, DefaultIdleTimeout, true},
		"embedded mode":    {ServerModeEmbedded, false, 0, PortSourceUnset, 0, DefaultIdleTimeout, true},
		"shared server mode": {ServerModeOwned, true, 0, PortSourceUnset, 0,
			DefaultIdleTimeout, true},
		"pinned env port":    {ServerModeOwned, false, somePort, PortSourceEnv, 0, DefaultIdleTimeout, true},
		"pinned config port": {ServerModeOwned, false, somePort, PortSourceConfigYaml, 0, DefaultIdleTimeout, true},
		"remotesapi port":    {ServerModeOwned, false, 0, PortSourceUnset, 54321, DefaultIdleTimeout, true},
	} {
		if eligible(args.mode, args.sharedMode, args.port, args.portSource, args.remotesAPIPort, args.timeout, args.spawnerSet) {
			t.Errorf("%s: watcher must not be eligible", name)
		}
	}
}

// TestEnsureRunningAutoStartSpawnsIdleWatcher drives the real auto-start
// with the fake dolt and a recording spawner: the watcher is launched for
// the server that was started, with the resolved idle window.
func TestEnsureRunningAutoStartSpawnsIdleWatcher(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "90s")

	var spawned []IdleWatchOpts
	SetIdleWatchSpawner(func(opts IdleWatchOpts) { spawned = append(spawned, opts) })
	t.Cleanup(func() { SetIdleWatchSpawner(nil) })

	port, startedByUs, err := EnsureRunningDetailed(beadsDir)
	if err != nil {
		t.Fatalf("EnsureRunningDetailed: %v", err)
	}
	if !startedByUs {
		t.Fatal("EnsureRunningDetailed on a fresh workspace must start the server")
	}
	t.Cleanup(func() { _ = IgnoreNotRunning(Stop(beadsDir)) })
	if got := launchedPorts(t, launches); len(got) != 1 {
		t.Fatalf("launched %v sql-servers, want exactly one", got)
	}
	if len(spawned) != 1 {
		t.Fatalf("spawned %d idle watchers, want 1", len(spawned))
	}
	opts := spawned[0]
	if opts.ServerDir != beadsDir || opts.Host != "127.0.0.1" || opts.Port != port {
		t.Errorf("watcher opts = %+v, want the started server (dir %s, 127.0.0.1:%d)", opts, beadsDir, port)
	}
	if opts.PID <= 0 {
		t.Errorf("watcher opts = %+v, want the started server's PID", opts)
	}
	if opts.Timeout != 90*time.Second {
		t.Errorf("watcher timeout = %s, want the resolved 90s window", opts.Timeout)
	}
}
