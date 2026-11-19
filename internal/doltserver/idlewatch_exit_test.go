package doltserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runIdleWatcherForTest runs one watcher goroutine for opts and returns a
// channel closed when RunIdleWatch returns, plus the watcher's start marker
// path to wait on.
func runIdleWatcherForTest(t *testing.T, beadsDir string, opts IdleWatchOpts) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunIdleWatch(context.Background(), opts)
	}()
	// The watcher writes its pid marker before its first liveness check;
	// wait for it so the exit timing below measures the watcher, not its
	// own startup.
	watcherPID := filepath.Join(beadsDir, ActivityDirName, watcherPIDName)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(watcherPID); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher did not write its pid marker within 2s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return done
}

// TestIdleWatch_ExitsWhenServerStopped pins the hand-off rule (GH#4282): a
// server stopped through bd's own stop path — by anyone, through any other
// process — leaves the watcher with nothing to watch, and it exits within
// 5s without stopping or starting anything else.
func TestIdleWatch_ExitsWhenServerStopped(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "90s")

	port, _, err := EnsureRunningDetailed(beadsDir)
	if err != nil {
		t.Fatalf("EnsureRunningDetailed: %v", err)
	}
	t.Cleanup(func() { _ = IgnoreNotRunning(Stop(beadsDir)) })
	state, err := IsRunning(beadsDir)
	if err != nil || state == nil || !state.Running {
		t.Fatalf("IsRunning after auto-start: %v, %v", state, err)
	}

	done := runIdleWatcherForTest(t, beadsDir, IdleWatchOpts{
		ServerDir: beadsDir, PID: state.PID, Host: "127.0.0.1", Port: port, Timeout: 90 * time.Second,
	})

	// The server stops through the ordinary stop path.
	if err := Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher still running 5s after the server it watched was stopped")
	}
	// It stopped and started nothing: no server is running and no state
	// remains for one.
	state, err = IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("IsRunning after the stop: %v", err)
	}
	if state != nil && state.Running {
		t.Error("the watcher restarted a server after the stop")
	}
	if _, err := os.Stat(filepath.Join(beadsDir, PIDFileName)); err == nil {
		t.Error("a pid file remains after the stop")
	}
}

// TestIdleWatch_ExitsWhenWatchChangesHands pins the other way a server can
// stop being the watcher's business: its pid file disappears (an external
// stop or cleanup). The watcher exits within 5s and touches nothing.
func TestIdleWatch_ExitsWhenWatchChangesHands(t *testing.T) {
	beadsDir, _ := fakeStartWorkspace(t)
	t.Setenv("BEADS_DOLT_IDLE_TIMEOUT", "90s")

	port, _, err := EnsureRunningDetailed(beadsDir)
	if err != nil {
		t.Fatalf("EnsureRunningDetailed: %v", err)
	}
	t.Cleanup(func() { _ = IgnoreNotRunning(Stop(beadsDir)) })
	state, err := IsRunning(beadsDir)
	if err != nil || state == nil || !state.Running {
		t.Fatalf("IsRunning after auto-start: %v, %v", state, err)
	}

	done := runIdleWatcherForTest(t, beadsDir, IdleWatchOpts{
		ServerDir: beadsDir, PID: state.PID, Host: "127.0.0.1", Port: port, Timeout: 90 * time.Second,
	})

	// The pid file goes away without this watcher's involvement.
	if err := os.Remove(filepath.Join(beadsDir, PIDFileName)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher still running 5s after its pid file went away")
	}
	// It stopped and started nothing: the server process it watched is
	// still alive, untouched by the watcher that lost it.
	if !isProcessAlive(state.PID) {
		t.Error("the watcher stopped the server after its pid file went away")
	}
}
