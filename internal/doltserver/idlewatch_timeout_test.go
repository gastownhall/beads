package doltserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeIdleConfig writes one dolt.idle-timeout setting into the workspace's
// config.yaml, the only file ResolveIdleTimeoutForDir reads.
func writeIdleConfig(t *testing.T, beadsDir, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt:\n  idle-timeout: "+value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestIdleTimeoutResolution pins how the idle window is resolved
// (GH#4282): BEADS_DOLT_IDLE_TIMEOUT first, then dolt.idle-timeout in the
// workspace's config.yaml, then the 30m default, with a bare 0 disabling
// the watcher and an unparsable value warning and yielding the default.
func TestIdleTimeoutResolution(t *testing.T) {
	beadsDir := t.TempDir()
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "")

	cases := []struct {
		name  string
		env   string
		cfgOn bool
		cfg   string
		want  time.Duration
	}{
		{name: "default", want: DefaultIdleTimeout},
		{name: "config sets the window", cfgOn: true, cfg: "10m", want: 10 * time.Minute},
		{name: "config bare 0 disables", cfgOn: true, cfg: "0", want: 0},
		{name: "config garbage yields the default", cfgOn: true, cfg: "soon", want: DefaultIdleTimeout},
		{name: "env sets the window", env: "90s", want: 90 * time.Second},
		{name: "env 0 disables", env: "0", want: 0},
		{name: "env duration 0s disables", env: "0s", want: 0},
		{name: "env garbage yields the default", env: "later", want: DefaultIdleTimeout},
		{name: "env overrides config", env: "45s", cfgOn: true, cfg: "10m", want: 45 * time.Second},
		{name: "empty env falls through to config", env: "", cfgOn: true, cfg: "10m", want: 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", tc.env)
			if tc.cfgOn {
				writeIdleConfig(t, beadsDir, tc.cfg)
			} else {
				_ = os.Remove(filepath.Join(beadsDir, "config.yaml"))
			}
			if got := ResolveIdleTimeoutForDir(beadsDir); got != tc.want {
				t.Errorf("ResolveIdleTimeoutForDir = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestIdleTimeoutDisabledSpawnsNoWatcher pins the disabled window's
// behavior: BEADS_DOLT_IDLE_TIMEOUT=0 auto-starts the server as usual but
// no watcher runs for it, so it stays up the way auto-started servers
// always did.
func TestIdleTimeoutDisabledSpawnsNoWatcher(t *testing.T) {
	beadsDir, launches := fakeStartWorkspace(t)
	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "0")

	var spawned []IdleWatchOpts
	SetIdleWatchSpawner(func(opts IdleWatchOpts) { spawned = append(spawned, opts) })
	t.Cleanup(func() { SetIdleWatchSpawner(nil) })

	if _, _, err := EnsureRunningDetailed(beadsDir); err != nil {
		t.Fatalf("EnsureRunningDetailed: %v", err)
	}
	t.Cleanup(func() { _ = IgnoreNotRunning(Stop(beadsDir)) })
	if got := launchedPorts(t, launches); len(got) != 1 {
		t.Fatalf("launched %v sql-servers, want exactly one", got)
	}
	if len(spawned) != 0 {
		t.Fatalf("a disabled idle window still launched %d watchers: %+v", len(spawned), spawned)
	}
	state, err := IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if state == nil || !state.Running {
		t.Fatal("a disabled idle window must leave the auto-started server running")
	}
}
