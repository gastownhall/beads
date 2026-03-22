package dolt

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCircuitBreaker_InitiallyAllows(t *testing.T) {
	cb := newTestCircuitBreaker(t)
	if !cb.Allow() {
		t.Fatal("new circuit breaker should allow requests")
	}
}

func TestMaybeNewCircuitBreaker_PortZeroDisabled(t *testing.T) {
	if cb := maybeNewCircuitBreaker("127.0.0.1", 0); cb != nil {
		t.Fatalf("maybeNewCircuitBreaker(0) = %#v, want nil", cb)
	}
}

func TestCircuitBreaker_TripsAfterThreshold(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "") // need real breaker behavior
	cb := newTestCircuitBreaker(t)

	// Record failures up to threshold
	for i := 0; i < circuitFailureThreshold; i++ {
		if !cb.Allow() {
			t.Fatalf("breaker should allow on failure %d (threshold=%d)", i+1, circuitFailureThreshold)
		}
		cb.RecordFailure()
	}

	// Should now be open
	if cb.State() != circuitOpen {
		t.Fatalf("expected state %q after %d failures, got %q", circuitOpen, circuitFailureThreshold, cb.State())
	}
	if cb.Allow() {
		t.Fatal("open breaker should reject requests")
	}
}

func TestCircuitBreaker_DoesNotTripBelowThreshold(t *testing.T) {
	cb := newTestCircuitBreaker(t)

	for i := 0; i < circuitFailureThreshold-1; i++ {
		cb.RecordFailure()
	}

	if cb.State() != circuitClosed {
		t.Fatalf("expected closed with %d failures (threshold=%d), got %q",
			circuitFailureThreshold-1, circuitFailureThreshold, cb.State())
	}
	if !cb.Allow() {
		t.Fatal("breaker below threshold should allow requests")
	}
}

func TestCircuitBreaker_SuccessResets(t *testing.T) {
	cb := newTestCircuitBreaker(t)

	// Accumulate some failures
	for i := 0; i < circuitFailureThreshold-1; i++ {
		cb.RecordFailure()
	}

	// Success resets
	cb.RecordSuccess()

	// Now failures should count from zero
	for i := 0; i < circuitFailureThreshold-1; i++ {
		cb.RecordFailure()
	}

	if cb.State() != circuitClosed {
		t.Fatal("breaker should still be closed after reset + sub-threshold failures")
	}
}

func TestCircuitBreaker_ActiveProbeAfterCooldown_NoServer(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	cb := newTestCircuitBreaker(t)

	// Trip the breaker
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure()
	}
	if cb.State() != circuitOpen {
		t.Fatal("expected open state")
	}

	// Simulate cooldown by manipulating the state file directly
	cb.mu.Lock()
	state := cb.readState()
	state.TrippedAt = time.Now().Add(-circuitCooldown - time.Second)
	cb.writeState(state)
	cb.mu.Unlock()

	// With no server listening, active probe fails — stays open
	if cb.Allow() {
		t.Fatal("breaker should reject when active probe fails (no server)")
	}
	if cb.State() != circuitOpen {
		t.Fatalf("expected open after failed probe, got %q", cb.State())
	}
}

func TestCircuitBreaker_ActiveProbeAfterCooldown_ServerUp(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	// Start a TCP listener to simulate a healthy server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	cb := newTestCircuitBreakerOnPort(t, port)

	// Trip the breaker
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure()
	}

	// Simulate cooldown
	cb.mu.Lock()
	state := cb.readState()
	state.TrippedAt = time.Now().Add(-circuitCooldown - time.Second)
	cb.writeState(state)
	cb.mu.Unlock()

	// Active probe should succeed — transitions directly to closed
	if !cb.Allow() {
		t.Fatal("breaker should allow after successful active probe")
	}
	if cb.State() != circuitClosed {
		t.Fatalf("expected closed after successful probe, got %q", cb.State())
	}
}

func TestCircuitBreaker_LegacyHalfOpenState(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	// If a state file has half-open from an older version, the breaker
	// should handle it gracefully via active probe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	cb := newTestCircuitBreakerOnPort(t, port)

	// Manually write a half-open state (simulating old breaker)
	cb.mu.Lock()
	cb.writeState(circuitState{
		State:    circuitHalfOpen,
		Failures: circuitFailureThreshold,
	})
	cb.mu.Unlock()

	// With server up, probe succeeds → closed
	if !cb.Allow() {
		t.Fatal("legacy half-open with server up should allow via active probe")
	}
	if cb.State() != circuitClosed {
		t.Fatalf("expected closed, got %q", cb.State())
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cb := newTestCircuitBreaker(t)

	// Trip the breaker
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure()
	}
	if cb.State() != circuitOpen {
		t.Fatal("expected open")
	}

	cb.Reset()
	if cb.State() != circuitClosed {
		t.Fatalf("expected closed after reset, got %q", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("should allow after reset")
	}
}

func TestCircuitBreaker_SharedState(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	// Two breakers for the same port should share state via the file
	dir := t.TempDir()
	path := filepath.Join(dir, "circuit.json")

	cb1 := &circuitBreaker{host: "127.0.0.1", port: 99999, filePath: path}
	cb2 := &circuitBreaker{host: "127.0.0.1", port: 99999, filePath: path}

	// Trip via cb1
	for i := 0; i < circuitFailureThreshold; i++ {
		cb1.RecordFailure()
	}

	// cb2 should see the tripped state
	if cb2.State() != circuitOpen {
		t.Fatalf("cb2 expected open (shared state), got %q", cb2.State())
	}
	if cb2.Allow() {
		t.Fatal("cb2 should reject when breaker is open")
	}
}

func TestCircuitBreaker_DifferentHostsSeparateState(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	// Two breakers for the same port but different hosts should have independent state.
	// This is the core fix: previously keyed on port only, which caused cross-host blocking.
	cb1 := newCircuitBreaker("127.0.0.1", 99999)
	cb2 := newCircuitBreaker("10.0.0.1", 99999)
	t.Cleanup(func() {
		os.Remove(cb1.filePath)
		os.Remove(cb2.filePath)
	})

	// Verify different file paths
	if cb1.filePath == cb2.filePath {
		t.Fatalf("different hosts should have different file paths: %s vs %s", cb1.filePath, cb2.filePath)
	}

	// Trip cb1
	for i := 0; i < circuitFailureThreshold; i++ {
		cb1.RecordFailure()
	}
	if cb1.State() != circuitOpen {
		t.Fatal("cb1 should be open")
	}

	// cb2 should be unaffected
	if cb2.State() != circuitClosed {
		t.Fatalf("cb2 should be closed (independent of cb1), got %q", cb2.State())
	}
	if !cb2.Allow() {
		t.Fatal("cb2 should allow requests (independent of cb1)")
	}
}

func TestCircuitBreaker_FileDeleted(t *testing.T) {
	cb := newTestCircuitBreaker(t)

	// Trip it
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure()
	}

	// Delete the state file — should gracefully degrade to closed
	os.Remove(cb.filePath)

	if cb.State() != circuitClosed {
		t.Fatal("missing file should default to closed")
	}
	if !cb.Allow() {
		t.Fatal("should allow when state file is missing")
	}
}

func TestCircuitBreaker_StaleStateAutoResets(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	cb := newTestCircuitBreaker(t)

	// Trip the breaker
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure()
	}
	if cb.State() != circuitOpen {
		t.Fatal("expected open state after tripping")
	}

	// Simulate a stale breaker by backdating TrippedAt beyond the TTL
	cb.mu.Lock()
	state := cb.readState()
	state.TrippedAt = time.Now().Add(-circuitStaleTTL - time.Minute)
	state.LastFailure = state.TrippedAt
	cb.writeState(state)
	cb.mu.Unlock()

	// readState should auto-reset the stale open state to closed
	if cb.State() != circuitClosed {
		t.Fatalf("stale open breaker should auto-reset to closed, got %q", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("stale breaker should allow requests after auto-reset")
	}
}

func TestCircuitBreaker_RecentStateNotReset(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	cb := newTestCircuitBreaker(t)

	// Trip the breaker
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure()
	}
	if cb.State() != circuitOpen {
		t.Fatal("expected open state after tripping")
	}

	// The breaker was just tripped (TrippedAt is recent) — it should NOT auto-reset
	// (Allow returns false because cooldown hasn't elapsed and probe fails)
	if cb.Allow() {
		t.Fatal("recently-tripped breaker should NOT auto-reset or allow")
	}
}

func TestCleanStaleCircuitBreakerFiles(t *testing.T) {
	// Create a temp directory to simulate /tmp
	dir := t.TempDir()

	// Create a legacy port-0 file
	port0File := filepath.Join(dir, "beads-dolt-circuit-0.json")
	if err := os.WriteFile(port0File, []byte(`{"state":"open"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Create a stale open breaker file
	staleFile := filepath.Join(dir, "beads-dolt-circuit-127-0-0-1-3307.json")
	staleState := circuitState{
		State:     circuitOpen,
		TrippedAt: time.Now().Add(-circuitStaleTTL - time.Hour),
	}
	staleData, _ := json.Marshal(staleState)
	if err := os.WriteFile(staleFile, staleData, 0600); err != nil {
		t.Fatal(err)
	}

	// Create a fresh (non-stale) open breaker file
	freshFile := filepath.Join(dir, "beads-dolt-circuit-127-0-0-1-5555.json")
	freshState := circuitState{
		State:     circuitOpen,
		TrippedAt: time.Now(),
	}
	freshData, _ := json.Marshal(freshState)
	if err := os.WriteFile(freshFile, freshData, 0600); err != nil {
		t.Fatal(err)
	}

	// Create a closed breaker file (should be left alone)
	closedFile := filepath.Join(dir, "beads-dolt-circuit-127-0-0-1-9999.json")
	closedData, _ := json.Marshal(circuitState{State: circuitClosed})
	if err := os.WriteFile(closedFile, closedData, 0600); err != nil {
		t.Fatal(err)
	}

	// Call the cleanup function with the test directory
	cleanStaleCircuitBreakerFilesIn(dir)

	// Legacy port-0 file should be removed
	if _, err := os.Stat(port0File); !os.IsNotExist(err) {
		t.Errorf("legacy port-0 file should have been removed: %s", port0File)
	}

	// Stale open file should be removed
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Errorf("stale open breaker file should have been removed: %s", staleFile)
	}

	// Fresh open file should still exist
	if _, err := os.Stat(freshFile); err != nil {
		t.Errorf("fresh open breaker file should NOT have been removed: %s", freshFile)
	}

	// Closed file should still exist
	if _, err := os.Stat(closedFile); err != nil {
		t.Errorf("closed breaker file should NOT have been removed: %s", closedFile)
	}
}

func TestIsConnectionError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"connection refused", errors.New("dial tcp: connection refused"), true},
		{"connection reset", errors.New("read: connection reset by peer"), true},
		{"broken pipe", errors.New("write: broken pipe"), true},
		{"i/o timeout", errors.New("read tcp 127.0.0.1:3307: i/o timeout"), true},
		{"bad connection", errors.New("driver: bad connection"), true},
		{"invalid connection", errors.New("invalid connection"), true},
		{"lost connection", errors.New("Error 2013: Lost connection to MySQL server"), true},
		{"gone away", errors.New("Error 2006: MySQL server has gone away"), true},
		{"syntax error (not connection)", errors.New("Error 1064: SQL syntax error"), false},
		{"table not found (not connection)", errors.New("Error 1146: Table doesn't exist"), false},
		{"unknown database (not connection)", errors.New("Unknown database 'test'"), false},
		{"read only (not connection)", errors.New("database is read only"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isConnectionError(tt.err)
			if got != tt.expected {
				t.Errorf("isConnectionError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

// newTestCircuitBreaker creates a circuit breaker with a temp file for testing.
// Uses port 99999 which has no listener, so active probes will fail.
func newTestCircuitBreaker(t *testing.T) *circuitBreaker {
	t.Helper()
	dir := t.TempDir()
	return &circuitBreaker{
		host:     "127.0.0.1",
		port:     99999,
		filePath: filepath.Join(dir, "circuit.json"),
	}
}

// newTestCircuitBreakerOnPort creates a circuit breaker targeting a specific port.
func newTestCircuitBreakerOnPort(t *testing.T, port int) *circuitBreaker {
	t.Helper()
	dir := t.TempDir()
	return &circuitBreaker{
		host:     "127.0.0.1",
		port:     port,
		filePath: filepath.Join(dir, "circuit.json"),
	}
}
