package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SyncState tracks daemon sync health for backoff and user hints.
// Stored in .beads/sync-state.json (gitignored, local-only).
type SyncState struct {
	LastFailure     time.Time `json:"last_failure,omitempty"`
	FailureCount    int       `json:"failure_count"`
	BackoffUntil    time.Time `json:"backoff_until,omitempty"`
	NeedsManualSync bool      `json:"needs_manual_sync"`
	FailureReason   string    `json:"failure_reason,omitempty"`
}

const (
	syncStateFile = "sync-state.json"
	// Backoff schedule: 30s, 1m, 2m, 5m, 10m, 30m (cap)
	maxBackoffDuration = 30 * time.Minute
	// Clear stale state after 24 hours
	staleStateThreshold = 24 * time.Hour
)

var (
	// backoffSchedule defines the exponential backoff durations
	backoffSchedule = []time.Duration{
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
	}
	// syncStateMu protects concurrent access to sync state file
	syncStateMu sync.Mutex
)

// LoadSyncState loads the sync state from .beads/sync-state.json.
// Returns empty state if file doesn't exist or is stale.
func LoadSyncState(beadsDir string) SyncState {
	syncStateMu.Lock()
	defer syncStateMu.Unlock()

	statePath := filepath.Join(beadsDir, syncStateFile)
	data, err := os.ReadFile(statePath) // #nosec G304 - path constructed from beadsDir
	if err != nil {
		return SyncState{}
	}

	var state SyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return SyncState{}
	}

	// Clear stale state (older than 24h with no recent failures)
	if !state.LastFailure.IsZero() && time.Since(state.LastFailure) > staleStateThreshold {
		_ = os.Remove(statePath)
		return SyncState{}
	}

	return state
}

// SaveSyncState saves the sync state to .beads/sync-state.json.
func SaveSyncState(beadsDir string, state SyncState) error {
	syncStateMu.Lock()
	defer syncStateMu.Unlock()

	statePath := filepath.Join(beadsDir, syncStateFile)

	// If state is empty/reset, remove the file
	if state.FailureCount == 0 && !state.NeedsManualSync {
		_ = os.Remove(statePath)
		return nil
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(statePath, data, 0600)
}

// ClearSyncState removes the sync state file.
func ClearSyncState(beadsDir string) error {
	syncStateMu.Lock()
	defer syncStateMu.Unlock()

	statePath := filepath.Join(beadsDir, syncStateFile)
	err := os.Remove(statePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RecordSyncFailure updates the sync state after a failure.
// Returns the duration until next retry.
func RecordSyncFailure(beadsDir string, reason string) time.Duration {
	state := LoadSyncState(beadsDir)

	state.LastFailure = time.Now()
	state.FailureCount++
	state.FailureReason = reason

	// Calculate backoff duration
	backoffIndex := state.FailureCount - 1
	if backoffIndex >= len(backoffSchedule) {
		backoffIndex = len(backoffSchedule) - 1
	}
	backoff := backoffSchedule[backoffIndex]

	state.BackoffUntil = time.Now().Add(backoff)

	// Mark as needing manual sync after 3 failures (likely a conflict)
	if state.FailureCount >= 3 {
		state.NeedsManualSync = true
	}

	_ = SaveSyncState(beadsDir, state)
	return backoff
}

// RecordSyncSuccess clears the sync state after a successful sync.
func RecordSyncSuccess(beadsDir string) {
	_ = ClearSyncState(beadsDir)
}

// ShouldSkipSync returns true if we're still in the backoff period.
func ShouldSkipSync(beadsDir string) bool {
	state := LoadSyncState(beadsDir)
	if state.BackoffUntil.IsZero() {
		return false
	}
	return time.Now().Before(state.BackoffUntil)
}

// ResetBackoffOnDaemonStart resets backoff counters when daemon starts,
// but preserves NeedsManualSync flag so hints still show.
// This allows a fresh start while keeping user informed of conflicts.
func ResetBackoffOnDaemonStart(beadsDir string) {
	state := LoadSyncState(beadsDir)

	// Nothing to reset
	if state.FailureCount == 0 && !state.NeedsManualSync {
		return
	}

	// Reset backoff but preserve NeedsManualSync
	needsManual := state.NeedsManualSync
	reason := state.FailureReason

	state = SyncState{
		NeedsManualSync: needsManual,
		FailureReason:   reason,
	}

	_ = SaveSyncState(beadsDir, state)
}
