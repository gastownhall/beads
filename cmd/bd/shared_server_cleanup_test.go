package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// stopSharedServerCleanup is the cleanup every fixture in this package that
// runs a subprocess `bd init` with BEADS_DOLT_SHARED_SERVER=1 owes.
//
// That init takes the shared-global branch in init.go, which calls
// doltserver.Start(sharedDir) — and Start DAEMONIZES: cmd.Process.Release(),
// no Wait, cmd.Dir = <sharedDir>/dolt (doltserver.go:1443-1446). The `bd`
// subprocess then exits and the sql-server keeps running, serving a directory
// under the suite's temp HOME that TestMain deletes on the way out. Nothing in
// those fixtures ever stopped it, so a full cmd/bd run finished with a live
// `dolt sql-server --config <suite root>/.beads/shared-server/…` still in the
// process table — the leak this bead is about (wy-j2zc8q).
//
// Because every one of those fixtures resolves the SAME $HOME/.beads/
// shared-server, only the last one to start a server leaves a survivor, which
// is why the process table names one fixture while three carry the defect.
//
// Register it after the fixture's directories are built and BEFORE the first
// subprocess `bd` runs, so it covers the t.Fatalf paths too.
func stopSharedServerCleanup(t *testing.T) {
	t.Helper()
	// SharedServerPath, not SharedServerDir: resolving must not create the
	// tree the command under test is supposed to create.
	sharedDir, err := doltserver.SharedServerPath()
	if err != nil {
		t.Fatalf("resolve shared server dir: %v", err)
	}
	stopDoltServerCleanup(t, sharedDir)
}

// stopDoltServerCleanup is the same cleanup for a server whose pid file lives
// under an explicit beads directory: a per-project server under
// <repo>/.beads, or a shared server under a HOME the fixture hands ONLY to
// the subprocess (`cmd.Env = ...HOME=<tmp>...`), where this process's own
// SharedServerPath would resolve the wrong tree. Register it BEFORE the first
// subprocess `bd` runs; the directory need not exist yet.
func stopDoltServerCleanup(t *testing.T, beadsDir string) {
	t.Helper()
	sharedDir := beadsDir
	t.Cleanup(func() {
		// Read the record BEFORE stopping: a clean Stop removes the pid
		// file, so afterwards there is nothing left to verify against.
		state, err := doltserver.IsRunning(sharedDir)
		if err != nil {
			t.Errorf("doltserver.IsRunning(%s): %v", sharedDir, err)
			return
		}
		if state == nil || !state.Running {
			return
		}
		pid := state.PID
		// The birth token makes the PID safe to ask about after the stop:
		// the server is not this process's child, so its PID is reusable the
		// instant it exits.
		token, tokenErr := procid.Capture(pid)

		if stopErr := doltserver.Stop(sharedDir); stopErr != nil &&
			!errors.Is(stopErr, doltserver.ErrServerNotRunning) {
			t.Errorf("doltserver.Stop(%s) for pid %d: %v", sharedDir, pid, stopErr)
		}

		if tokenErr != nil {
			t.Logf("procid.Capture(%d): %v; skipping the exit check", pid, tokenErr)
			return
		}
		requireSharedServerExited(t, pid, token)
	})
}

// stopProxiedServerCleanup is the same duty for a subprocess `bd init
// --proxied-server`: that init leaves a proxy (proxy.pid) and its backend
// dolt sql-server (proxy-child.pid) running under <beadsDir>/dolt, with no
// dolt-server.pid for stopDoltServerCleanup to find — the post-run sweep
// named the metrics fixture's repo by exactly that cwd. proxy.Shutdown is
// the verified stop `bd dolt stop` performs and covers both records. A root
// that never started (the embedded and per-project cases of the same table)
// has neither record and is not an error.
func stopProxiedServerCleanup(t *testing.T, proxyRoot string) {
	t.Helper()
	t.Cleanup(func() {
		recorded := false
		for _, name := range []string{proxy.PIDFileName, server.PIDFileName} {
			if _, err := os.Stat(filepath.Join(proxyRoot, name)); err == nil {
				recorded = true
			}
		}
		if !recorded {
			return
		}
		if err := proxy.Shutdown(proxyRoot); err != nil {
			t.Errorf("proxy.Shutdown(%s): %v", proxyRoot, err)
		}
	})
}

const (
	// sharedServerExitTimeout is how long the cleanup waits for the shared
	// dolt sql-server to leave the process table. doltserver.Stop's own
	// gracefulStop budget is 5s, so this only ever elapses when the stop did
	// not take — the outcome worth reporting.
	sharedServerExitTimeout = 30 * time.Second
	sharedServerExitPoll    = 50 * time.Millisecond
)

// requireSharedServerExited fails the test if the recorded shared server is
// still running after doltserver.Stop, then force-kills it so the leak does
// not outlive the run.
//
// t.Errorf, not t.Logf: a surviving server holds the temp HOME the suite is
// about to delete, and reporting it where it is caused is the whole point —
// the previous machinery printed a stderr line and exited 0, which is how the
// same leak was fixed three times and rediscovered from a process table
// (wy-j2zc8q).
func requireSharedServerExited(t *testing.T, pid int, token procid.Token) {
	t.Helper()
	deadline := time.Now().Add(sharedServerExitTimeout)
	for {
		same, err := procid.Verify(pid, token)
		if err != nil {
			t.Logf("procid.Verify(%d): %v", pid, err)
			return
		}
		if !same {
			// Gone, or the PID now belongs to something else. Either way
			// our server is not running.
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(sharedServerExitPoll)
	}

	handle, err := procid.Open(pid, token)
	if err != nil {
		if sharedServerStillRunning(t, pid, token) {
			t.Errorf("shared dolt sql-server pid %d survived Stop by more than %s and could not be opened to kill: %v",
				pid, sharedServerExitTimeout, err)
		}
		return
	}
	killErr := handle.Kill()
	_ = handle.Close()
	if killErr != nil {
		if sharedServerStillRunning(t, pid, token) {
			t.Errorf("shared dolt sql-server pid %d survived Stop by more than %s and could not be killed: %v",
				pid, sharedServerExitTimeout, killErr)
		}
		return
	}
	t.Errorf("shared dolt sql-server pid %d survived Stop by more than %s (force-killed)",
		pid, sharedServerExitTimeout)
}

// sharedServerStillRunning re-checks birth identity after procid.Open or
// Handle.Kill failed. Both verify the token themselves and return a plain
// "does not match token" error when it no longer does — exactly what a
// process that exited between the last poll and the kill attempt produces.
// Without this second look, a server that shut down a few milliseconds late
// would be reported as one that survived Stop entirely.
func sharedServerStillRunning(t *testing.T, pid int, token procid.Token) bool {
	t.Helper()
	same, err := procid.Verify(pid, token)
	if err != nil {
		t.Logf("procid.Verify(%d) after a failed kill attempt: %v", pid, err)
		return false
	}
	return same
}
