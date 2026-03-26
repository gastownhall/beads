package dolt

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Circuit breaker states.
const (
	circuitClosed   = "closed"
	circuitOpen     = "open"
	circuitHalfOpen = "half-open"
)

// Circuit breaker configuration.
const (
	// circuitFailureThreshold is the number of consecutive connection failures
	// required to trip the breaker.
	circuitFailureThreshold = 5

	// circuitFailureWindow is the time window in which failures are counted.
	// Failures older than this are ignored (the counter resets).
	circuitFailureWindow = 60 * time.Second

	// circuitCooldown is how long to stay open before allowing a half-open probe.
	// Keep this short — planned restarts (e.g. gt dolt sync) only take 2-3s.
	circuitCooldown = 5 * time.Second

	// circuitStaleTTL is the maximum age of an open circuit breaker state file
	// before it is considered stale and auto-reset to closed. This prevents old
	// breaker files from poisoning fresh inits when the server was stopped long
	// ago or the machine was rebooted. The TTL is based on the TrippedAt timestamp
	// (or LastFailure if TrippedAt is zero).
	circuitStaleTTL = 5 * time.Minute
)

// circuitState is the shared file-based circuit breaker state.
// Multiple processes read/write this file to coordinate fail-fast behavior
// when the Dolt server is down.
type circuitState struct {
	State        string    `json:"state"`
	Failures     int       `json:"failures"`
	FirstFailure time.Time `json:"first_failure,omitempty"`
	LastFailure  time.Time `json:"last_failure,omitempty"`
	TrippedAt    time.Time `json:"tripped_at,omitempty"`
}

// circuitBreaker manages the circuit breaker for a specific Dolt server host:port.
// It uses a file in /tmp for cross-process state sharing and an in-process
// mutex for thread safety within a single process.
type circuitBreaker struct {
	host     string
	port     int
	filePath string
	mu       sync.Mutex
}

// ErrCircuitOpen is returned when the circuit breaker is open and rejecting requests.
var ErrCircuitOpen = fmt.Errorf("dolt circuit breaker is open: server appears down, failing fast (cooldown %s)", circuitCooldown)

// maybeNewCircuitBreaker returns a file-backed circuit breaker only for a
// concrete port. Port 0 means "not yet resolved" during standalone auto-start,
// and sharing breaker state on port 0 poisons every fresh init on the machine.
func maybeNewCircuitBreaker(host string, port int) *circuitBreaker {
	if port <= 0 {
		return nil
	}
	return newCircuitBreaker(host, port)
}

// circuitBreakerDir is the dedicated directory for circuit breaker state files.
// Using a subdirectory avoids scanning all of /tmp (which may contain millions
// of entries) when cleaning up stale breaker files on startup.
const circuitBreakerDir = "/tmp/beads-circuit"

// newCircuitBreaker creates a circuit breaker for the given Dolt server host:port.
func newCircuitBreaker(host string, port int) *circuitBreaker {
	// Sanitize host for use in filename (replace dots/colons with dashes)
	safeHost := strings.NewReplacer(".", "-", ":", "-").Replace(host)
	_ = os.MkdirAll(circuitBreakerDir, 0755)
	return &circuitBreaker{
		host:     host,
		port:     port,
		filePath: filepath.Join(circuitBreakerDir, fmt.Sprintf("beads-dolt-circuit-%s-%d.json", safeHost, port)),
	}
}

// Allow checks whether a request should be allowed through.
// Returns true if the circuit is closed or half-open (probe allowed).
// Returns false if the circuit is open and cooldown hasn't elapsed.
//
// When the cooldown elapses, Allow performs an active TCP health probe
// rather than passively waiting for the next request to succeed or fail.
// If the probe succeeds, the breaker resets to closed immediately. This
// avoids the half-open→open re-trip race that can leave the breaker stuck.
func (cb *circuitBreaker) Allow() bool {
	// In test mode, bypass the circuit breaker entirely. Tests manage their
	// own server lifecycle via testcontainers, and the file-based breaker
	// state persists across test runs causing cascading false-positive trips.
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		return true
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	state := cb.readState()
	switch state.State {
	case circuitOpen:
		if time.Since(state.TrippedAt) >= circuitCooldown {
			// Cooldown elapsed — actively probe the server
			if cb.probe() {
				state.State = circuitClosed
				state.Failures = 0
				state.FirstFailure = time.Time{}
				cb.writeState(state)
				log.Printf("[circuit-breaker] %s:%d: open → closed (active probe succeeded)", cb.host, cb.port)
				return true
			}
			// Probe failed — stay open, reset the tripped timer
			state.TrippedAt = time.Now()
			cb.writeState(state)
			log.Printf("[circuit-breaker] %s:%d: open → open (active probe failed, cooldown reset)", cb.host, cb.port)
			return false
		}
		return false
	case circuitHalfOpen:
		// Legacy state from older breaker versions — treat as open with
		// immediate probe since we no longer use half-open passively.
		if cb.probe() {
			state.State = circuitClosed
			state.Failures = 0
			state.FirstFailure = time.Time{}
			cb.writeState(state)
			log.Printf("[circuit-breaker] %s:%d: half-open → closed (active probe succeeded)", cb.host, cb.port)
			return true
		}
		state.State = circuitOpen
		state.TrippedAt = time.Now()
		cb.writeState(state)
		log.Printf("[circuit-breaker] %s:%d: half-open → open (active probe failed)", cb.host, cb.port)
		return false
	default:
		return true
	}
}

// probe performs a quick TCP dial to check if the Dolt server is reachable.
func (cb *circuitBreaker) probe() bool {
	addr := net.JoinHostPort(cb.host, fmt.Sprintf("%d", cb.port))
	conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// RecordSuccess records a successful connection. Resets the breaker to closed.
func (cb *circuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state := cb.readState()
	if state.State == circuitHalfOpen {
		log.Printf("[circuit-breaker] %s:%d: half-open → closed (probe succeeded)", cb.host, cb.port)
	}
	// Reset to clean closed state
	cb.writeState(circuitState{State: circuitClosed})
}

// RecordFailure records a connection failure. May trip the breaker.
func (cb *circuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state := cb.readState()
	now := time.Now()

	switch state.State {
	case circuitHalfOpen:
		// Probe failed — re-trip immediately
		state.State = circuitOpen
		state.TrippedAt = now
		state.LastFailure = now
		cb.writeState(state)
		log.Printf("[circuit-breaker] %s:%d: half-open → open (probe failed)", cb.host, cb.port)
		return

	case circuitOpen:
		// Already open — update last failure timestamp
		state.LastFailure = now
		cb.writeState(state)
		return

	default: // closed
		// Check if first failure is within the window
		if state.Failures > 0 && now.Sub(state.FirstFailure) > circuitFailureWindow {
			// Window expired — reset counter
			state.Failures = 0
			state.FirstFailure = time.Time{}
		}

		state.Failures++
		state.LastFailure = now
		if state.Failures == 1 {
			state.FirstFailure = now
		}

		if state.Failures >= circuitFailureThreshold {
			state.State = circuitOpen
			state.TrippedAt = now
			cb.writeState(state)
			log.Printf("[circuit-breaker] %s:%d: closed → open (tripped after %d failures in %s)",
				cb.host, cb.port, state.Failures, now.Sub(state.FirstFailure).Round(time.Millisecond))
			return
		}

		cb.writeState(state)
	}
}

// State returns the current circuit state string (for diagnostics).
func (cb *circuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.readState().State
}

// Reset forces the circuit breaker to closed state. Used in tests and recovery.
func (cb *circuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.writeState(circuitState{State: circuitClosed})
}

// readState reads the circuit state from the shared file.
// Returns closed state if the file doesn't exist or can't be read.
// Stale open/half-open states (older than circuitStaleTTL) are auto-reset
// to closed so that leftover breaker files from previous sessions don't
// poison fresh inits (GH#2598).
func (cb *circuitBreaker) readState() circuitState {
	data, err := os.ReadFile(cb.filePath)
	if err != nil {
		return circuitState{State: circuitClosed}
	}
	var state circuitState
	if err := json.Unmarshal(data, &state); err != nil {
		return circuitState{State: circuitClosed}
	}
	if state.State == "" {
		state.State = circuitClosed
	}

	// Auto-expire stale open/half-open breaker state. Use TrippedAt as the
	// reference timestamp; fall back to LastFailure if TrippedAt is zero
	// (e.g. from an older breaker format).
	if state.State == circuitOpen || state.State == circuitHalfOpen {
		ref := state.TrippedAt
		if ref.IsZero() {
			ref = state.LastFailure
		}
		if !ref.IsZero() && time.Since(ref) > circuitStaleTTL {
			log.Printf("[circuit-breaker] %s:%d: stale %s state (age %s > TTL %s), auto-resetting to closed",
				cb.host, cb.port, state.State, time.Since(ref).Round(time.Second), circuitStaleTTL)
			reset := circuitState{State: circuitClosed}
			cb.writeState(reset)
			return reset
		}
	}

	return state
}

// writeState atomically writes the circuit state to the shared file.
// Uses write-to-temp + rename for atomic updates visible to other processes.
func (cb *circuitBreaker) writeState(state circuitState) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	tmp := cb.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return
	}
	_ = os.Rename(tmp, cb.filePath)
}

// CleanStaleCircuitBreakerFiles removes stale circuit breaker files.
// This cleans up leftover files that could poison fresh inits:
//   - Legacy port-0 files (beads-dolt-circuit-0.json) from before the port-0 fix
//   - Any breaker file whose open/half-open state is older than circuitStaleTTL
//
// Called during init to ensure a clean starting state (GH#2598).
func CleanStaleCircuitBreakerFiles() {
	// Remove legacy files that lived directly in /tmp (before the subdirectory move).
	// Direct path removal — no directory scan needed.
	_ = os.Remove("/tmp/beads-dolt-circuit-0.json")

	// Clean stale files in the dedicated subdirectory (fast — typically 0-2 files).
	_ = os.MkdirAll(circuitBreakerDir, 0755)
	cleanStaleCircuitBreakerFilesIn(circuitBreakerDir)
}

// cleanStaleCircuitBreakerFilesIn is the testable implementation of
// CleanStaleCircuitBreakerFiles that accepts a directory parameter.
func cleanStaleCircuitBreakerFilesIn(dir string) {
	pattern := filepath.Join(dir, "beads-dolt-circuit-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, path := range matches {
		// Always remove legacy port-0 files — they should never exist
		// (the port-0 fix prevents creating them, but old ones may linger).
		base := filepath.Base(path)
		if base == "beads-dolt-circuit-0.json" {
			_ = os.Remove(path)
			log.Printf("[circuit-breaker] removed legacy port-0 breaker file: %s", path)
			continue
		}

		// For other breaker files, check if the state is stale.
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is from filepath.Glob with controlled pattern
		if err != nil {
			continue
		}
		var state circuitState
		if err := json.Unmarshal(data, &state); err != nil {
			// Corrupt file — remove it
			_ = os.Remove(path)
			continue
		}
		if state.State != circuitOpen && state.State != circuitHalfOpen {
			continue
		}
		ref := state.TrippedAt
		if ref.IsZero() {
			ref = state.LastFailure
		}
		if !ref.IsZero() && time.Since(ref) > circuitStaleTTL {
			_ = os.Remove(path)
			log.Printf("[circuit-breaker] removed stale breaker file: %s (age %s)",
				path, time.Since(ref).Round(time.Second))
		}
	}
}

// isConnectionError returns true if the error indicates the Dolt server is
// unreachable or down. Only these errors trip the circuit breaker — query-level
// errors (syntax, missing table, etc.) do not.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())

	// TCP-level failures
	if strings.Contains(errStr, "connection refused") {
		return true
	}
	if strings.Contains(errStr, "connection reset") {
		return true
	}
	if strings.Contains(errStr, "broken pipe") {
		return true
	}
	if strings.Contains(errStr, "i/o timeout") {
		return true
	}

	// MySQL protocol-level disconnects
	if strings.Contains(errStr, "bad connection") {
		return true
	}
	if strings.Contains(errStr, "invalid connection") {
		return true
	}
	if strings.Contains(errStr, "lost connection") {
		return true
	}
	if strings.Contains(errStr, "gone away") {
		return true
	}

	return false
}
