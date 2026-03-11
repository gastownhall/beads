package dolt

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
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

// circuitBreaker manages the circuit breaker for a specific Dolt server port.
// It uses a file in /tmp for cross-process state sharing and an in-process
// mutex for thread safety within a single process.
type circuitBreaker struct {
	port     int
	filePath string
	mu       sync.Mutex
}

// ErrCircuitOpen is returned when the circuit breaker is open and rejecting requests.
var ErrCircuitOpen = fmt.Errorf("dolt circuit breaker is open: server appears down, failing fast (cooldown %s)", circuitCooldown)

// newCircuitBreaker creates a circuit breaker for the given Dolt server port.
func newCircuitBreaker(port int) *circuitBreaker {
	return &circuitBreaker{
		port:     port,
		filePath: fmt.Sprintf("/tmp/beads-dolt-circuit-%d.json", port),
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
				log.Printf("[circuit-breaker] port %d: open → closed (active probe succeeded)", cb.port)
				return true
			}
			// Probe failed — stay open, reset the tripped timer
			state.TrippedAt = time.Now()
			cb.writeState(state)
			log.Printf("[circuit-breaker] port %d: open → open (active probe failed, cooldown reset)", cb.port)
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
			log.Printf("[circuit-breaker] port %d: half-open → closed (active probe succeeded)", cb.port)
			return true
		}
		state.State = circuitOpen
		state.TrippedAt = time.Now()
		cb.writeState(state)
		log.Printf("[circuit-breaker] port %d: half-open → open (active probe failed)", cb.port)
		return false
	default:
		return true
	}
}

// probe performs a quick TCP dial to check if the Dolt server is reachable.
func (cb *circuitBreaker) probe() bool {
	addr := fmt.Sprintf("127.0.0.1:%d", cb.port)
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
		log.Printf("[circuit-breaker] port %d: half-open → closed (probe succeeded)", cb.port)
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
		log.Printf("[circuit-breaker] port %d: half-open → open (probe failed)", cb.port)
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
			log.Printf("[circuit-breaker] port %d: closed → open (tripped after %d failures in %s)",
				cb.port, state.Failures, now.Sub(state.FirstFailure).Round(time.Millisecond))
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
