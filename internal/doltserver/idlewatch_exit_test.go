//go:build !windows

package doltserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runIdleWatcherInRealTime auto-starts beadsDir's (fake dolt) server with an
// in-process watcher record and runs the real watcher on it, on the real
// clock, with a window far longer than the test. It returns the server
// state and a channel that receives the watcher's outcome.
func runIdleWatcherInRealTime(t *testing.T, beadsDir string) (*State, <-chan IdleWatchOutcome) {
	t.Helper()
	calls := stubIdleWatchSpawner(t)
	state := autoStartForTest(t, beadsDir)
	if len(*calls) != 1 {
		t.Fatalf("spawner called %d times, want once for the auto-start", len(*calls))
	}
	opts := (*calls)[0]
	opts.Timeout = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan IdleWatchOutcome, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		outcome, err := RunIdleWatch(ctx, opts)
		if err != nil {
			t.Errorf("RunIdleWatch: %v", err)
		}
		done <- outcome
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
	})

	// Let the watcher get past its start-up lock and into its loop, so the
	// timing below measures its liveness check, not its start-up.
	select {
	case outcome := <-done:
		t.Fatalf("watcher exited (%s) before the server went away", outcome)
	case <-time.After(1500 * time.Millisecond):
	}
	return state, done
}

// awaitWatcherExit fails the test unless the watcher exits within 5s.
func awaitWatcherExit(t *testing.T, done <-chan IdleWatchOutcome) IdleWatchOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("watcher still running 5s after its server went away")
		return IdleWatchCanceled
	}
}

// TestIdleWatch_ExitsWhenServerStopped is AC-6 on the real clock with a real
// server process: a server stopped through the ordinary stop path (what
// `bd dolt stop` runs) leaves the watcher with nothing to watch, and it
// exits within 5s without starting anything.
func TestIdleWatch_ExitsWhenServerStopped(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	_, done := runIdleWatcherInRealTime(t, beadsDir)

	if err := Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	outcome := awaitWatcherExit(t, done)
	if outcome != IdleWatchServerGone && outcome != IdleWatchSuperseded {
		t.Errorf("watcher outcome = %s, want %s or %s", outcome, IdleWatchServerGone, IdleWatchSuperseded)
	}
	state, err := IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("IsRunning after the stop: %v", err)
	}
	if state != nil && state.Running {
		t.Error("a server is running after the stop: the watcher started one")
	}
}

// TestIdleWatch_ExitsWhenWatchChangesHands is AC-6's other shape on the real
// clock: the server's pid file goes away without the watcher (an external
// stop or cleanup, as `bd dolt killall` leaves it). The watcher exits within
// 5s and does not stop the server it lost.
func TestIdleWatch_ExitsWhenWatchChangesHands(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	state, done := runIdleWatcherInRealTime(t, beadsDir)

	if err := os.Remove(pidPath(beadsDir)); err != nil {
		t.Fatal(err)
	}
	if outcome := awaitWatcherExit(t, done); outcome != IdleWatchServerGone {
		t.Errorf("watcher outcome = %s, want %s", outcome, IdleWatchServerGone)
	}
	if !isProcessAlive(state.PID) {
		t.Error("the watcher stopped the server after its pid file went away")
	}
}

// TestIdleWatch_RelativeServerDirSpawnsAnAbsoluteOne pins the path the
// watcher gets: bd init auto-starts with the relative ".beads", and the
// watcher, which runs inside the server directory, must still name it.
func TestIdleWatch_RelativeServerDirSpawnsAnAbsoluteOne(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	calls := stubIdleWatchSpawner(t)
	t.Chdir(filepath.Dir(beadsDir))
	autoStartForTest(t, filepath.Base(beadsDir))
	if len(*calls) != 1 {
		t.Fatalf("spawner called %d times, want once for the auto-start", len(*calls))
	}
	got := (*calls)[0].ServerDir
	if !filepath.IsAbs(got) {
		t.Fatalf("watcher server dir = %q, want an absolute path", got)
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("watcher server dir %q: %v", got, err)
	}
	wantInfo, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Errorf("watcher server dir = %q, want %q", got, beadsDir)
	}
}
