//go:build cgo

package dolt

import (
	"context"
	"testing"
	"time"
)

func TestWatchdog_DisableWatchdog(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &Config{
		Path:            tmpDir,
		DisableWatchdog: true,
	}

	store := &DoltStore{}

	// Should not start watchdog
	store.startWatchdog(cfg)

	if store.watchdogCancel != nil {
		t.Error("watchdog should not start when DisableWatchdog is true")
	}
	if store.watchdogDone != nil {
		t.Error("watchdogDone should be nil when DisableWatchdog is true")
	}
}

func TestWatchdog_CleansUpOnClose(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &Config{
		Path:       tmpDir,
	}

	store := &DoltStore{}

	// Start the watchdog
	store.startWatchdog(cfg)

	if store.watchdogCancel == nil {
		t.Fatal("watchdog should have started")
	}
	if store.watchdogDone == nil {
		t.Fatal("watchdogDone should not be nil")
	}

	// Stop the watchdog
	store.stopWatchdog()

	// Verify it stopped by checking the done channel is closed
	select {
	case <-store.watchdogDone:
		// Good - channel is closed
	case <-time.After(2 * time.Second):
		t.Error("watchdog did not stop within timeout")
	}
}

func TestWatchdog_BacksOffAfterRepeatedFailures(t *testing.T) {
	cfg := &Config{
		Path:       t.TempDir(),
		ServerHost: "127.0.0.1",
		ServerPort: 39999, // Port nothing is listening on
	}

	state := &watchdogState{healthy: true}
	store := &DoltStore{}

	ctx := context.Background()

	// Simulate repeated failures
	for i := 0; i < watchdogMaxRestarts+1; i++ {
		store.watchdogCheck(ctx, cfg, state)
	}

	if !state.backingOff {
		t.Error("watchdog should be in backoff after max restarts exceeded")
	}
	if state.restartCount != watchdogMaxRestarts+1 {
		t.Errorf("restartCount = %d, want %d", state.restartCount, watchdogMaxRestarts+1)
	}
}

func TestWatchdog_HealthCheckFailsWhenNoServer(t *testing.T) {
	cfg := &Config{
		ServerHost: "127.0.0.1",
		ServerPort: 39998, // Port nothing is listening on
	}

	store := &DoltStore{}

	if store.isServerHealthy(cfg) {
		t.Error("isServerHealthy should return false when no server is running")
	}
}

func TestCleanStalePID_NoFile(t *testing.T) {
	// Should not panic when PID file doesn't exist
	cleanStalePID(t.TempDir())
}
