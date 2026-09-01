package doltserver

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestManagesLiveServerOnPort pins the ownership proof: both state files, a
// live PID, and a port file agreeing with the port the caller is connected on.
//
// Every "false" case below is a silent in-place migration of somebody else's
// database if it ever flips, so each one names the topology it stands for.
func TestManagesLiveServerOnPort(t *testing.T) {
	const port = 3307

	// dead is a PID no live process answers to: above every default pid_max.
	const dead = 2147483646

	newDir := func(t *testing.T) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		return dir
	}
	write := func(t *testing.T, dir, name string, v int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strconv.Itoa(v)), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	t.Run("live pid and matching port file is proof", func(t *testing.T) {
		dir := newDir(t)
		write(t, dir, PortFileName, port)
		write(t, dir, PIDFileName, os.Getpid())
		if !ManagesLiveServerOnPort(dir, port) {
			t.Fatal("a workspace with a live recorded server on this port owns it (#6088 must keep migrating)")
		}
	})

	t.Run("dead pid is not proof", func(t *testing.T) {
		// The workspace once ran its own server on this port; it died, and an
		// externally managed server now answers there. 3307 is both the common
		// external port and bd's own default, so the collision is likely.
		dir := newDir(t)
		write(t, dir, PortFileName, port)
		write(t, dir, PIDFileName, dead)
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("a port file records that bd once bound a port, not that bd answers on it now")
		}
	})

	t.Run("port file naming a different port is not proof", func(t *testing.T) {
		dir := newDir(t)
		write(t, dir, PortFileName, 40083)
		write(t, dir, PIDFileName, os.Getpid())
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("bd's server is on another port; this endpoint is somebody else's")
		}
	})

	t.Run("missing state files are not proof", func(t *testing.T) {
		dir := newDir(t)
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("bd never started a server here")
		}
		write(t, dir, PortFileName, port)
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("a port file with no pid file proves nothing about liveness")
		}
	})

	t.Run("garbage pid file is not proof", func(t *testing.T) {
		dir := newDir(t)
		write(t, dir, PortFileName, port)
		if err := os.WriteFile(filepath.Join(dir, PIDFileName), []byte("not-a-pid"), 0o600); err != nil {
			t.Fatalf("write pid: %v", err)
		}
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("an unparseable pid file must fail closed")
		}
	})

	t.Run("degenerate inputs fail closed", func(t *testing.T) {
		if ManagesLiveServerOnPort("", port) {
			t.Fatal("no workspace, no proof")
		}
		if ManagesLiveServerOnPort(newDir(t), 0) {
			t.Fatal("port 0 means unresolved; there is no endpoint to own")
		}
	})

	// Read-only: unlike IsRunning, classifying a connection must never repair
	// or delete a workspace's server state as a side effect.
	t.Run("does not mutate workspace state", func(t *testing.T) {
		dir := newDir(t)
		write(t, dir, PortFileName, port)
		write(t, dir, PIDFileName, dead)
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("dead pid must not be proof")
		}
		for _, name := range []string{PortFileName, PIDFileName} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Fatalf("%s was removed: %v — the predicate must not repair state it merely reads", name, err)
			}
		}
	})
}
