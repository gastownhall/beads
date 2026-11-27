//go:build integration && !windows

package dolt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// idleWatchedWorkspace is a workspace whose server bd auto-starts with an
// idle watcher. The watchers run on goroutines of this test process (the
// test binary cannot re-exec itself as bd), named by its PID in watcher.pid.
type idleWatchedWorkspace struct {
	beadsDir string
	reg      *integration.ProcessRegistry

	mu       sync.Mutex
	launches int
}

// newIdleWatchedWorkspace makes an initialized workspace whose auto-started
// servers get an idle watcher with the given window: nothing pins, shares or
// routes its server, and auto-start is on.
func newIdleWatchedWorkspace(t *testing.T, window time.Duration) *idleWatchedWorkspace {
	t.Helper()
	doltBin := integration.RequireDolt(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOLT_ROOT_PATH", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	for _, key := range []string{"BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_MODE"} {
		t.Setenv(key, "")
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")
	t.Setenv(doltserver.IdleTimeoutEnv, window.String())

	w := &idleWatchedWorkspace{beadsDir: filepath.Join(home, ".beads"), reg: integration.NewProcessRegistry(t)}
	doltDir := filepath.Join(w.beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o700); err != nil {
		t.Fatal(err)
	}
	initCmd := exec.Command(doltBin, "init", "--name", "beads-test", "--email", "beads@test")
	initCmd.Dir = doltDir
	initCmd.Env = append(os.Environ(), "HOME="+home, "DOLT_ROOT_PATH="+home)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init: %v\n%s", err, out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	orig := doltserver.IdleWatchSpawner
	doltserver.IdleWatchSpawner = func(opts doltserver.IdleWatchOpts) (int, error) {
		w.mu.Lock()
		w.launches++
		w.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = doltserver.RunIdleWatch(ctx, opts)
		}()
		return os.Getpid(), nil
	}
	// Cleanups run LIFO: the server stops first, so a watcher still running
	// exits as server-gone, then the watchers are waited for, and only then
	// is the hook restored and the config reset above.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		doltserver.IdleWatchSpawner = orig
	})
	t.Cleanup(func() { _ = doltserver.IgnoreNotRunning(doltserver.Stop(w.beadsDir)) })
	return w
}

// ensureRunning auto-starts the workspace's server, or finds the one running,
// the way a store open does, and returns its port and PID. The server must
// have a live idle watcher.
func (w *idleWatchedWorkspace) ensureRunning(t *testing.T) (port, pid int) {
	t.Helper()
	port, _, err := doltserver.EnsureRunningDetailed(w.beadsDir)
	if err != nil {
		t.Fatalf("EnsureRunningDetailed: %v", err)
	}
	pid = w.serverPID()
	if pid <= 0 {
		t.Fatal("no pid file after EnsureRunningDetailed")
	}
	if p, err := os.FindProcess(pid); err == nil {
		w.reg.Register(p)
	}
	if !doltserver.IdleWatchRunning(w.beadsDir) {
		w.mu.Lock()
		n := w.launches
		w.mu.Unlock()
		t.Fatalf("premise: the auto-started server has no live idle watcher (%d launched)", n)
	}
	return port, pid
}

// serverPID returns the PID the pid file names, or 0.
func (w *idleWatchedWorkspace) serverPID() int {
	data, err := os.ReadFile(filepath.Join(w.beadsDir, doltserver.PIDFileName))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// config is an open of the workspace's database on the server at port whose
// pool drops an idle connection within about a second.
func (w *idleWatchedWorkspace) config(port int) *Config {
	return &Config{
		Path:             filepath.Join(w.beadsDir, "dolt"),
		BeadsDir:         w.beadsDir,
		CommitterName:    "idle-watch",
		CommitterEmail:   "idle-watch@example.com",
		Database:         "idle_watch_probe",
		ServerHost:       "127.0.0.1",
		ServerPort:       port,
		ServerPortSource: doltserver.PortSourcePortFile,
		ConnMaxIdleTime:  100 * time.Millisecond,
	}
}

// TestIdleWatch_StoreThatCannotAutoStartKeepsWatchedServerUp is GH#4282's
// store side for opens that may not start a server, on a real auto-started
// server with its idle watcher: a store opened with auto-start off, or strict
// read-only, keeps the server up for as long as it is open, past its pool's
// idle timeout and several idle windows, and still queries through the same
// pool. Once it closes, the watcher stops the server within a window, so the
// store was what kept it up.
func TestIdleWatch_StoreThatCannotAutoStartKeepsWatchedServerUp(t *testing.T) {
	const window = 2 * time.Second
	w := newIdleWatchedWorkspace(t, window)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// The database the stores below open. Its creating open may not start a
	// server either: auto-start is off, as for bd init's open of the server
	// it has just started.
	port, _ := w.ensureRunning(t)
	setup := w.config(port)
	setup.CreateIfMissing = true
	created, err := New(ctx, setup)
	if err != nil {
		t.Fatalf("creating the probe database: %v", err)
	}
	if err := created.Close(); err != nil {
		t.Fatalf("closing the creating store: %v", err)
	}

	for _, tc := range []struct {
		name string
		set  func(*Config)
	}{
		{name: "auto-start off", set: func(c *Config) { c.AutoStart = false }},
		{name: "strict read-only", set: func(c *Config) { c.ReadOnly, c.DisableAutoStart = true, true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The first case finds the server the setup used; the second, a
			// fresh one, since the first case ends with the reap.
			port, pid := w.ensureRunning(t)
			cfg := w.config(port)
			tc.set(cfg)
			if serverOpenCanAutoStart(cfg) {
				t.Fatal("premise: this open must not be allowed to start a server")
			}
			store, err := New(ctx, cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			closed := false
			t.Cleanup(func() {
				if !closed {
					_ = store.Close()
				}
			})

			// The pool's connections go within about a second; the watcher
			// polls every second and stops an idle server after the window.
			time.Sleep(3*window + 2*time.Second)
			if got := w.serverPID(); got != pid {
				t.Fatalf("the idle watcher stopped the server under an open store (pid file names %d, want %d)", got, pid)
			}
			var issues int
			if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&issues); err != nil {
				t.Fatalf("query through the open store after %s: %v", 3*window+2*time.Second, err)
			}

			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			closed = true
			// A poll, the window and a graceful stop: well inside 30s.
			deadline := time.Now().Add(30 * time.Second)
			for w.serverPID() == pid {
				if time.Now().After(deadline) {
					t.Fatal("the idle watcher did not stop the server after the store closed")
				}
				time.Sleep(250 * time.Millisecond)
			}
		})
	}
}
