package dolt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
)

func TestCircuitBreaker_InitiallyAllows(t *testing.T) {
	cb := newTestCircuitBreaker(t)
	if !cb.Allow() {
		t.Fatal("new circuit breaker should allow requests")
	}
}

func TestMaybeNewCircuitBreaker_PortZeroDisabled(t *testing.T) {
	if cb := maybeNewCircuitBreaker("127.0.0.1", 0, "test"); cb != nil {
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
	cb1 := newCircuitBreaker("127.0.0.1", 99999, "")
	cb2 := newCircuitBreaker("10.0.0.1", 99999, "")
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

func TestCircuitBreaker_DifferentDatabasesSeparateState(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	// Two breakers for the same host:port but different databases should have
	// independent state. This prevents one degraded project from tripping the
	// breaker for all worktrees on a shared server (GH#3140).
	cb1 := newCircuitBreaker("127.0.0.1", 99999, "project_alpha")
	cb2 := newCircuitBreaker("127.0.0.1", 99999, "project_beta")
	t.Cleanup(func() {
		os.Remove(cb1.filePath)
		os.Remove(cb2.filePath)
	})

	if cb1.filePath == cb2.filePath {
		t.Fatalf("different databases should have different file paths: %s vs %s", cb1.filePath, cb2.filePath)
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

	// Call the cleanup function with the test directory. The closed file was
	// just written, so it reflects an in-use breaker and is left alone.
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

// TestCleanStaleCircuitBreakerFilesIn_AgesOutClosed verifies that closed
// breaker files ARE swept, but only past the closedSweepTTL mtime threshold.
// Every directory the sweep visits has live writers — the live one by
// definition, and the legacy "/tmp/beads-circuit" location (GH#4636) wherever
// TMPDIR-less processes (launchd, cron, bare ssh) resolve os.TempDir() to /tmp
// — and an unconditional remove churned against them forever: recreate,
// delete, log, on every TMPDIR-set invocation (bd-uann8). A fresh closed file
// is a live writer's state and must survive; an aged one is the GH#4636
// ephemeral-port accumulation and must go.
func TestCleanStaleCircuitBreakerFilesIn_AgesOutClosed(t *testing.T) {
	dir := t.TempDir()

	// A closed file no writer has touched past the TTL: the true GH#4636 case.
	agedClosedFile := filepath.Join(dir, "beads-dolt-circuit-127-0-0-1-9999.json")
	closedData, _ := json.Marshal(circuitState{State: circuitClosed})
	if err := os.WriteFile(agedClosedFile, closedData, 0600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-closedSweepTTL - time.Hour)
	if err := os.Chtimes(agedClosedFile, aged, aged); err != nil {
		t.Fatal(err)
	}

	// A freshly-written closed file: a live writer's state.
	freshClosedFile := filepath.Join(dir, "beads-dolt-circuit-127-0-0-1-8888.json")
	if err := os.WriteFile(freshClosedFile, closedData, 0600); err != nil {
		t.Fatal(err)
	}

	// Fresh open file should still survive the sweep (the age-based rule for
	// open/half-open state is the TrippedAt one).
	freshFile := filepath.Join(dir, "beads-dolt-circuit-127-0-0-1-5555.json")
	freshData, _ := json.Marshal(circuitState{State: circuitOpen, TrippedAt: time.Now()})
	if err := os.WriteFile(freshFile, freshData, 0600); err != nil {
		t.Fatal(err)
	}

	cleanStaleCircuitBreakerFilesIn(dir)

	if _, err := os.Stat(agedClosedFile); !os.IsNotExist(err) {
		t.Errorf("aged closed breaker file should have been removed: %s", agedClosedFile)
	}
	if _, err := os.Stat(freshClosedFile); err != nil {
		t.Errorf("fresh closed breaker file (live writer) should NOT have been removed: %s", freshClosedFile)
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Errorf("fresh open breaker file should NOT have been removed: %s", freshFile)
	}
}

// TestCleanStaleCircuitBreakerFiles_AgesOutClosedLiveFiles is the live
// directory's half of the GH#4636 cleanup. Every host:port:db a process opens
// leaves a closed-state file behind, and ephemeral test-server ports mint a
// new name each time, so the live directory only ever grew: a dev host
// accumulated 2335 closed files, and every store open read every one of them.
// A closed file nobody has written for the TTL says nothing an absent file
// does not (readState reads absent as closed, and a closed state's failure
// count only matters inside circuitFailureWindow), so it goes. A recently
// written one is a breaker some process is using, whatever its failure
// count, and stays.
func TestCleanStaleCircuitBreakerFiles_AgesOutClosedLiveFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(testCircuitBreakerDirEnv, dir)

	write := func(name string, state circuitState, mtime time.Time) string {
		t.Helper()
		path := filepath.Join(dir, name)
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return path
	}
	now := time.Now()
	// Literal ages, not ones derived from closedSweepTTL, so the test pins the
	// TTL's width: a writer that runs once a day keeps its state, and a file
	// idle for more than a day goes.
	aged := write("beads-dolt-circuit-127-0-0-1-41869-mtxdl.json",
		circuitState{State: circuitClosed}, now.Add(-25*time.Hour))
	daily := write("beads-dolt-circuit-127-0-0-1-3307-gb.json",
		circuitState{State: circuitClosed}, now.Add(-23*time.Hour))
	fresh := write("beads-dolt-circuit-127-0-0-1-3307-ga.json",
		circuitState{State: circuitClosed}, now)
	counting := write("beads-dolt-circuit-127-0-0-1-3307-gc.json",
		circuitState{State: circuitClosed, Failures: 2, FirstFailure: now, LastFailure: now}, now)

	CleanStaleCircuitBreakerFiles()

	if _, err := os.Stat(aged); !os.IsNotExist(err) {
		t.Errorf("closed live breaker file untouched past the TTL should have been removed: %s (stat err %v)", aged, err)
	}
	if _, err := os.Stat(daily); err != nil {
		t.Errorf("closed live breaker file written within the last day should NOT have been removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("recently written closed live breaker file should NOT have been removed: %v", err)
	}
	if _, err := os.Stat(counting); err != nil {
		t.Errorf("closed live breaker file counting failures should NOT have been removed: %v", err)
	}
}

func TestCircuitBreakerDir_UsesSubdirectory(t *testing.T) {
	// Verify that circuit breaker files are created in the dedicated
	// subdirectory, not directly in the temp root (which can have millions of
	// entries).
	cb := newCircuitBreaker("127.0.0.1", 44444, "")
	t.Cleanup(func() { os.Remove(cb.filePath) })

	wantDir, _ := circuitBreakerPaths()
	if filepath.Dir(cb.filePath) != wantDir {
		t.Errorf("circuit breaker file should be in %s, got dir %s",
			wantDir, filepath.Dir(cb.filePath))
	}

	// Write state and verify file lands in the subdirectory
	cb.writeState(circuitState{State: circuitClosed})
	if _, err := os.Stat(cb.filePath); err != nil {
		t.Errorf("circuit breaker file should exist at %s: %v", cb.filePath, err)
	}
}

// TestCircuitBreakerDir_DerivedFromTempDir verifies the breaker directory is
// derived from os.TempDir() rather than a hardcoded "/tmp", so on Windows it
// lands under %TEMP% instead of C:\tmp (GH#4636).
func TestCircuitBreakerDir_DerivedFromTempDir(t *testing.T) {
	custom := t.TempDir()
	// os.TempDir() honors these across platforms (TMPDIR on unix; TMP/TEMP on
	// Windows), so the breaker dir must follow.
	t.Setenv("TMPDIR", custom)
	t.Setenv("TMP", custom)
	t.Setenv("TEMP", custom)

	got := circuitBreakerDir()
	if want := filepath.Join(os.TempDir(), "beads-circuit"); got != want {
		t.Errorf("circuitBreakerDir() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("circuitBreakerDir() = %q, want it under os.TempDir() %q", got, os.TempDir())
	}
	// When the temp root is not "/tmp", the path must not be the old literal.
	if os.TempDir() != "/tmp" && got == "/tmp/beads-circuit" {
		t.Errorf("circuitBreakerDir() still hardcoded to /tmp: %q", got)
	}
}

func TestCircuitBreakerPathsTestOverrideIsolatesCurrentAndLegacyState(t *testing.T) {
	testDir := t.TempDir()
	t.Setenv(testCircuitBreakerDirEnv, testDir)
	t.Setenv("BEADS_TEST_MODE", "")

	dir, legacy := circuitBreakerPaths()
	if dir != testDir {
		t.Fatalf("circuit directory = %q, want %q", dir, testDir)
	}
	wantLegacy := filepath.Join(testDir, "beads-dolt-circuit-0.json")
	if legacy != wantLegacy {
		t.Fatalf("legacy circuit file = %q, want %q", legacy, wantLegacy)
	}
	cb := newCircuitBreaker("127.0.0.1", 44444, "isolated")
	if filepath.Dir(cb.filePath) != testDir {
		t.Fatalf("breaker path = %q, want directory %q", cb.filePath, testDir)
	}
	if err := os.WriteFile(wantLegacy, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write isolated legacy state: %v", err)
	}
	CleanStaleCircuitBreakerFiles()
	if _, err := os.Stat(wantLegacy); !os.IsNotExist(err) {
		t.Fatalf("isolated legacy cleanup error = %v, want not-exist", err)
	}
}

func TestCircuitBreakerPathsProductionDefaultsUnchanged(t *testing.T) {
	t.Setenv(testCircuitBreakerDirEnv, "")
	dir, legacy := circuitBreakerPaths()
	if want := circuitBreakerDir(); dir != want {
		t.Fatalf("production circuit directory = %q, want %q", dir, want)
	}
	if legacy != legacyCircuitBreakerFile {
		t.Fatalf("production legacy circuit file = %q, want %q", legacy, legacyCircuitBreakerFile)
	}
}

func TestCircuitBreakerPathsRejectsRelativeOverride(t *testing.T) {
	t.Setenv(testCircuitBreakerDirEnv, "relative-test-dir")
	dir, legacy := circuitBreakerPaths()
	if dir != circuitBreakerDir() || legacy != legacyCircuitBreakerFile {
		t.Fatalf("relative override selected paths dir=%q legacy=%q", dir, legacy)
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
		{"typed 1105 with connection-like wording", &mysql.MySQLError{Number: 1105, Message: "connection lost while validating commit"}, false},
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

func TestWithRetryTyped1105DoesNotRetryOrTripCircuit(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	for range circuitFailureThreshold - 1 {
		breaker.RecordFailure()
	}
	store := &DoltStore{breaker: breaker}
	err := &mysql.MySQLError{Number: 1105, Message: "connection lost while validating commit"}

	calls := 0
	got := store.withRetry(context.Background(), func() error {
		calls++
		return err
	})
	if !errors.Is(got, err) {
		t.Fatalf("withRetry() error = %v, want %v", got, err)
	}
	if calls != 1 {
		t.Fatalf("withRetry() calls = %d, want 1", calls)
	}
	if state := breaker.State(); state != circuitClosed {
		t.Fatalf("circuit state = %q, want %q", state, circuitClosed)
	}
}

func TestRunInTransactionPermanentCallbackErrorsDoNotTripCircuit(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	store := &DoltStore{breaker: breaker}
	callbackErr := errors.New("invalid connection")
	callbackCalls := 0
	runnerCalls := 0

	for i := 0; i < circuitFailureThreshold; i++ {
		err := store.runInTransaction(context.Background(), "test: external callback", func(storage.Transaction) error {
			callbackCalls++
			return callbackErr
		}, func(_ context.Context, _ string, fn func(storage.Transaction) error) error {
			runnerCalls++
			return fn(nil)
		})
		if !errors.Is(err, callbackErr) {
			t.Fatalf("RunInTransaction attempt %d error = %v, want %v", i+1, err, callbackErr)
		}
	}

	if callbackCalls != circuitFailureThreshold {
		t.Fatalf("callback calls = %d, want %d", callbackCalls, circuitFailureThreshold)
	}
	if runnerCalls != circuitFailureThreshold {
		t.Fatalf("transaction runner calls = %d, want %d", runnerCalls, circuitFailureThreshold)
	}
	if state := breaker.State(); state != circuitClosed {
		t.Fatalf("circuit state after external callback errors = %q, want %q", state, circuitClosed)
	}

	unrelatedCalls := 0
	if err := store.withRetry(context.Background(), func() error {
		unrelatedCalls++
		return nil
	}); err != nil {
		t.Fatalf("unrelated operation after callback errors: %v", err)
	}
	if unrelatedCalls != 1 {
		t.Fatalf("unrelated operation calls = %d, want 1", unrelatedCalls)
	}
}

func TestRunInTransactionPostCallbackIndeterminateFailureTripsCircuitWithoutReplay(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	breaker := newTestCircuitBreaker(t)
	store := &DoltStore{breaker: breaker}
	infraErr := fmt.Errorf("commit response lost: %w: %w", testConnectionLoss, ErrCommitIndeterminate)
	callbackCalls := 0
	runnerCalls := 0

	for i := 0; i < circuitFailureThreshold; i++ {
		err := store.runInTransaction(context.Background(), "test: infrastructure commit", func(storage.Transaction) error {
			callbackCalls++
			return nil
		}, func(_ context.Context, _ string, fn func(storage.Transaction) error) error {
			runnerCalls++
			if err := fn(nil); err != nil {
				return err
			}
			return infraErr
		})
		if !errors.Is(err, ErrCommitIndeterminate) {
			t.Fatalf("RunInTransaction attempt %d error = %v, want ErrCommitIndeterminate", i+1, err)
		}
		if callbackCalls != i+1 {
			t.Fatalf("callback calls after attempt %d = %d, want %d (no replay)", i+1, callbackCalls, i+1)
		}
		if runnerCalls != i+1 {
			t.Fatalf("transaction runner calls after attempt %d = %d, want %d (no replay)", i+1, runnerCalls, i+1)
		}
	}

	if state := breaker.State(); state != circuitOpen {
		t.Fatalf("circuit state after infrastructure commit failures = %q, want %q", state, circuitOpen)
	}
}

// dialError is the error a dial returns when connect fails with errno.
func dialError(errno syscall.Errno) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
}

// GH#7214: a probe this process may not make at all (connect refused with
// EPERM or EACCES, as inside a sandbox that refuses localhost) says nothing
// about the server. It must not reset the cooldown, or a sandboxed caller
// would keep every process that can connect failing fast.
func TestCircuitBreaker_LocallyRefusedProbeKeepsCooldown(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	tests := []struct {
		name      string
		errno     syscall.Errno
		wantReset bool
	}{
		{"EPERM", syscall.EPERM, false},
		{"EACCES", syscall.EACCES, false},
		{"connection refused", syscall.ECONNREFUSED, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := dialProbe
			t.Cleanup(func() { dialProbe = orig })
			dialProbe = func(string, string, time.Duration) error { return dialError(tt.errno) }

			cb := newTestCircuitBreaker(t)
			for i := 0; i < circuitFailureThreshold; i++ {
				cb.RecordFailure()
			}
			cb.mu.Lock()
			state := cb.readState()
			state.TrippedAt = time.Now().Add(-circuitCooldown - time.Second)
			cb.writeState(state)
			trippedAt := cb.readState().TrippedAt
			cb.mu.Unlock()

			if cb.Allow() {
				t.Fatal("an open breaker whose probe failed must reject")
			}
			cb.mu.Lock()
			after := cb.readState()
			cb.mu.Unlock()
			if after.State != circuitOpen {
				t.Errorf("state = %q, want %q", after.State, circuitOpen)
			}
			if reset := !after.TrippedAt.Equal(trippedAt); reset != tt.wantReset {
				t.Errorf("cooldown reset = %v, want %v", reset, tt.wantReset)
			}
		})
	}
}

// GH#7214: a sandboxed bd may not connect to localhost at all. Its refused
// dials, before and after it adopts the tracked server, are not failures of
// the server the other bd processes reach, so they must not count toward
// tripping the breaker those processes read. That holds too where the open
// fails closed because auto-start answered on another port than a shared
// server's or an authoritative one.
func TestNewServerMode_LocalDialRefusalIsNotAServerFailure(t *testing.T) {
	const port, otherPort = 14567, 14568
	sharedServer := func(cfg *Config) { cfg.ServerPortSharedServer = true }
	authoritativePort := func(cfg *Config) { cfg.ServerPortSource = doltserver.PortSourceEnv }
	tests := []struct {
		name         string
		errno        syscall.Errno
		autoStart    bool
		startedPort  int           // the port auto-start answers on
		configure    func(*Config) // nil: no port source declares the port
		wantFailures int
		wantMsg      string // for an adopted server: nothing was auto-started
	}{
		{"EPERM, tracked server adopted", syscall.EPERM, true, port, nil, 0, "local policy"},
		{"EACCES, tracked server adopted", syscall.EACCES, true, port, nil, 0, "local policy"},
		{"connection refused, tracked server adopted", syscall.ECONNREFUSED, true, port, nil, 1, "bd dolt stop"},
		{"EPERM, auto-start off", syscall.EPERM, false, port, nil, 0, ""},
		{"connection refused, auto-start off", syscall.ECONNREFUSED, false, port, nil, 1, ""},
		{"EPERM, shared server's port", syscall.EPERM, true, otherPort, sharedServer, 0, ""},
		{"EACCES, shared server's port", syscall.EACCES, true, otherPort, sharedServer, 0, ""},
		{"connection refused, shared server's port", syscall.ECONNREFUSED, true, otherPort, sharedServer, 1, ""},
		{"EPERM, authoritative port", syscall.EPERM, true, otherPort, authoritativePort, 0, ""},
		{"EACCES, authoritative port", syscall.EACCES, true, otherPort, authoritativePort, 0, ""},
		{"connection refused, authoritative port", syscall.ECONNREFUSED, true, otherPort, authoritativePort, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "") // keep the real breaker
			breaker := newTestCircuitBreakerOnPort(t, port)
			origClean, origNew, origDial := cleanServerCircuitState, newServerCircuitBreaker, dialServer
			t.Cleanup(func() {
				cleanServerCircuitState, newServerCircuitBreaker, dialServer = origClean, origNew, origDial
			})
			cleanServerCircuitState = func() {}
			newServerCircuitBreaker = func(string, int, string) *circuitBreaker { return breaker }
			dialServer = func(string, string, time.Duration) (net.Conn, error) {
				return nil, dialError(tt.errno)
			}
			// Auto-start adopts a tracked server: the one the open dialed, or
			// one on another port.
			stubEnsureRunningDetailed(t, tt.startedPort, false, nil)

			beadsDir := t.TempDir()
			cfg := &Config{
				Database:   "dial_refusal",
				BeadsDir:   beadsDir,
				Path:       filepath.Join(beadsDir, "dolt"),
				ServerHost: "127.0.0.1",
				ServerPort: port,
				AutoStart:  tt.autoStart,
			}
			if tt.configure != nil {
				tt.configure(cfg)
			}
			_, err := newServerMode(context.Background(), cfg)
			if err == nil {
				t.Fatal("an open whose every dial fails must fail")
			}
			if tt.startedPort != port && !strings.Contains(err.Error(), "silently") {
				t.Errorf("open did not fail closed: %v", err)
			}
			breaker.mu.Lock()
			failures := breaker.readState().Failures
			breaker.mu.Unlock()
			if failures != tt.wantFailures {
				t.Errorf("breaker failures = %d, want %d", failures, tt.wantFailures)
			}
			if tt.wantMsg != "" {
				msg := err.Error()
				if !strings.Contains(msg, tt.wantMsg) || strings.Contains(msg, "auto-started") {
					t.Errorf("error for an adopted server = %q, want it to say %q and not \"auto-started\"", msg, tt.wantMsg)
				}
				// A dial the sandbox refused never reached the server, so its
				// log has nothing to explain.
				wantLog := tt.errno == syscall.ECONNREFUSED
				if gotLog := strings.Contains(msg, "Check logs"); gotLog != wantLog {
					t.Errorf("error for an adopted server = %q: names the server log = %v, want %v", msg, gotLog, wantLog)
				}
			}
		})
	}
}

// The error after auto-start's dial fails says "auto-started" only for a
// server this open started, and names the server log only for a dial the
// server refused: one local policy refused never reached it (GH#7214). It
// still wraps the dial error.
func TestAutoStartRedialError(t *testing.T) {
	const addr, logPath = "127.0.0.1:14567", "/repo/.beads/dolt-server.log"
	tests := []struct {
		name        string
		startedByUs bool
		errno       syscall.Errno
		want        []string
		notWant     []string
	}{
		{"started, connection refused", true, syscall.ECONNREFUSED,
			[]string{"auto-started but still unreachable", "Check logs: " + logPath},
			[]string{"local policy", "bd dolt stop"}},
		{"started, EPERM", true, syscall.EPERM,
			[]string{"auto-started but still unreachable", "local policy"},
			[]string{"Check logs", "bd dolt stop"}},
		{"adopted, connection refused", false, syscall.ECONNREFUSED,
			[]string{"Dolt server unreachable at", "Check logs: " + logPath, "bd dolt stop"},
			[]string{"auto-started", "local policy"}},
		{"adopted, EPERM", false, syscall.EPERM,
			[]string{"Dolt server unreachable at", "local policy"},
			[]string{"auto-started", "Check logs", "bd dolt stop"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := autoStartRedialError(addr, dialError(tt.errno), tt.startedByUs, logPath)
			msg := err.Error()
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q does not say %q", msg, w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(msg, w) {
					t.Errorf("error %q says %q", msg, w)
				}
			}
			if !errors.Is(err, tt.errno) {
				t.Errorf("error %q does not wrap %v", msg, tt.errno)
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
