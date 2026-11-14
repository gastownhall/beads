package dolt

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	mysql "github.com/go-sql-driver/mysql"
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

	// closedSweepTTL is how long a CLOSED breaker file must sit unmodified
	// before the sweep removes it, in the live directory and the legacy
	// "/tmp/beads-circuit" one alike. Every host:port:db a process opens
	// leaves a closed file behind, and ephemeral ports (test servers above
	// all) mint a distinct name each time that nothing ever reuses, so
	// without an age-out both directories only grow (GH#4636) — and the sweep
	// reads every file on every run. A closed file nobody has written for
	// this long is indistinguishable from no file: readState reads an absent
	// file as closed, and a closed state's failure count only matters inside
	// circuitFailureWindow. A breaker in use is rewritten on every successful
	// open, so its mtime stays fresh and it is never swept.
	//
	// The age-out is by mtime, never unconditional: the legacy directory is
	// not abandoned wherever os.TempDir() still resolves to /tmp — any
	// TMPDIR-less process (launchd, cron, a bare ssh command) uses it as its
	// LIVE directory — and an unconditional remove fought those writers
	// forever: recreate, delete, log, on every alternation of TMPDIR-set and
	// TMPDIR-less invocations. Generous on purpose: a writer that runs even
	// once a day keeps its state.
	closedSweepTTL = 24 * time.Hour
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

// circuitBreaker manages the circuit breaker for a specific Dolt server
// host:port:database combination. Using per-database granularity prevents
// degradation in one project from tripping the breaker for all worktrees
// sharing the same server (GH#3140).
//
// It uses a file under os.TempDir() for cross-process state sharing and an in-process
// mutex for thread safety within a single process.
type circuitBreaker struct {
	host     string
	port     int
	database string
	filePath string
	mu       sync.Mutex
}

// ErrCircuitOpen is returned when the circuit breaker is open and rejecting requests.
var ErrCircuitOpen = fmt.Errorf("dolt circuit breaker is open: server appears down, failing fast (cooldown %s)", circuitCooldown)

// maybeNewCircuitBreaker returns a file-backed circuit breaker only for a
// concrete port. Port 0 means "not yet resolved" during standalone auto-start,
// and sharing breaker state on port 0 poisons every fresh init on the machine.
// The database parameter scopes the breaker to a specific project so that
// degradation in one database doesn't trip the breaker for others (GH#3140).
func maybeNewCircuitBreaker(host string, port int, database string) *circuitBreaker {
	if port <= 0 {
		return nil
	}
	return newCircuitBreaker(host, port, database)
}

// circuitBreakerDir returns the dedicated directory for circuit breaker state
// files. Using a subdirectory avoids scanning all of the temp root (which may
// contain millions of entries) when cleaning up stale breaker files on
// startup. Derived from os.TempDir() so it is correct on every platform:
// hardcoding "/tmp" resolved to C:\tmp on Windows, silently accumulating
// breaker files there forever (GH#4636).
func circuitBreakerDir() string {
	return filepath.Join(os.TempDir(), "beads-circuit")
}

const (
	legacyCircuitBreakerFile = "/tmp/beads-dolt-circuit-0.json"
	testCircuitBreakerDirEnv = "BEADS_TEST_CIRCUIT_DIR"
)

// circuitBreakerPaths returns production paths unless the test harness provides
// a suite-owned circuit directory. The override redirects both current and
// legacy state even when an individual test temporarily unsets BEADS_TEST_MODE.
func circuitBreakerPaths() (dir, legacyFile string) {
	if testDir := filepath.Clean(os.Getenv(testCircuitBreakerDirEnv)); filepath.IsAbs(testDir) {
		return testDir, filepath.Join(testDir, "beads-dolt-circuit-0.json")
	}
	return circuitBreakerDir(), legacyCircuitBreakerFile
}

// newCircuitBreaker creates a circuit breaker for the given Dolt server
// host:port:database. The database name is included in the file path so each
// project gets independent circuit breaker state on shared servers (GH#3140).
func newCircuitBreaker(host string, port int, database string) *circuitBreaker {
	// Sanitize host and database for use in filename
	sanitize := strings.NewReplacer(".", "-", ":", "-", "/", "-")
	safeHost := sanitize.Replace(host)
	safeDB := sanitize.Replace(database)

	// Include database in filename when non-empty. This keeps backward
	// compatibility for callers that don't pass a database name (the old
	// per-host:port files are still valid and will be cleaned up by
	// CleanStaleCircuitBreakerFiles).
	var filename string
	if safeDB != "" {
		filename = fmt.Sprintf("beads-dolt-circuit-%s-%d-%s.json", safeHost, port, safeDB)
	} else {
		filename = fmt.Sprintf("beads-dolt-circuit-%s-%d.json", safeHost, port)
	}

	dir, _ := circuitBreakerPaths()
	_ = os.MkdirAll(dir, 0755)
	return &circuitBreaker{
		host:     host,
		port:     port,
		database: database,
		filePath: filepath.Join(dir, filename),
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
			probeErr := cb.probe()
			if probeErr == nil {
				state.State = circuitClosed
				state.Failures = 0
				state.FirstFailure = time.Time{}
				cb.writeState(state)
				log.Printf("[circuit-breaker] %s:%d/%s: open → closed (active probe succeeded)", cb.host, cb.port, cb.database)
				return true
			}
			if isLocalDialRefusal(probeErr) {
				// This process may not connect at all, which says nothing
				// about the server: stay open without resetting the
				// cooldown, so a process that can connect probes next.
				return false
			}
			// Probe failed — stay open, reset the tripped timer
			state.TrippedAt = time.Now()
			cb.writeState(state)
			log.Printf("[circuit-breaker] %s:%d/%s: open → open (active probe failed, cooldown reset)", cb.host, cb.port, cb.database)
			return false
		}
		return false
	case circuitHalfOpen:
		// Legacy state from older breaker versions — treat as open with
		// immediate probe since we no longer use half-open passively.
		probeErr := cb.probe()
		if probeErr == nil {
			state.State = circuitClosed
			state.Failures = 0
			state.FirstFailure = time.Time{}
			cb.writeState(state)
			log.Printf("[circuit-breaker] %s:%d/%s: half-open → closed (active probe succeeded)", cb.host, cb.port, cb.database)
			return true
		}
		if isLocalDialRefusal(probeErr) {
			return false // as for an open breaker, above
		}
		state.State = circuitOpen
		state.TrippedAt = time.Now()
		cb.writeState(state)
		log.Printf("[circuit-breaker] %s:%d/%s: half-open → open (active probe failed)", cb.host, cb.port, cb.database)
		return false
	default:
		return true
	}
}

// probe performs a quick TCP dial to check if the Dolt server is reachable,
// and returns the dial error (nil when it is reachable).
// One-shot: reachability is based on dial success alone, so a drain timeout
// (dial-accepted-but-mute) still counts as reachable here. Draining before
// close still applies — it avoids sending the dolt sql-server a TCP RST that
// it would interpret as an aborted MySQL handshake (gastownhall/beads#4132,
// #4133); dialProbe drains through doltserver.ProbeSQLServer.
func (cb *circuitBreaker) probe() error {
	addr := net.JoinHostPort(cb.host, fmt.Sprintf("%d", cb.port))
	return dialProbe("tcp", addr, 1*time.Second)
}

// isLocalDialRefusal reports whether a dial failed because this process may
// not connect at all: connect returned EPERM or EACCES, as it does inside a
// sandbox that refuses localhost (GH#7214) or under a local firewall policy.
// That says nothing about the server, so it must not move the breaker: every
// process sharing the server reads it, and the ones that can connect would
// then fail fast.
func isLocalDialRefusal(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

// recordDialFailure records a failed dial of the server on breaker (nil means
// no breaker), unless isLocalDialRefusal says the dial never reached it.
func recordDialFailure(breaker *circuitBreaker, dialErr error) {
	if breaker != nil && !isLocalDialRefusal(dialErr) {
		breaker.RecordFailure()
	}
}

// RecordSuccess records a successful connection. Resets the breaker to closed.
func (cb *circuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state := cb.readState()
	if state.State == circuitHalfOpen {
		log.Printf("[circuit-breaker] %s:%d/%s: half-open → closed (probe succeeded)", cb.host, cb.port, cb.database)
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
		log.Printf("[circuit-breaker] %s:%d/%s: half-open → open (probe failed)", cb.host, cb.port, cb.database)
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
			log.Printf("[circuit-breaker] %s:%d/%s: closed → open (tripped after %d failures in %s)",
				cb.host, cb.port, cb.database, state.Failures, now.Sub(state.FirstFailure).Round(time.Millisecond))
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
			log.Printf("[circuit-breaker] %s:%d/%s: stale %s state (age %s > TTL %s), auto-resetting to closed",
				cb.host, cb.port, cb.database, state.State, time.Since(ref).Round(time.Second), circuitStaleTTL)
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
// It also removes files that would otherwise only accumulate (GH#4636):
//   - Closed-state files no writer has modified in closedSweepTTL
//   - Corrupt files that do not parse as breaker state
//
// The same rules apply in the live directory and the legacy
// "/tmp/beads-circuit" one; setting BEADS_TEST_CIRCUIT_DIR (tests) skips the
// legacy directory.
//
// Called during init to ensure a clean starting state (GH#2598). Store open
// reaches it at most once per process, through
// cleanStaleCircuitBreakerFilesOnce; bd doctor --fix calls it directly and
// still sweeps on demand.
func CleanStaleCircuitBreakerFiles() {
	dir, legacyFile := circuitBreakerPaths()

	// Remove the legacy port-0 file that lived directly in the temp root before
	// the subdirectory move (GH#2598). Best-effort: remove from the resolved
	// legacy path and, for real (non-test) runs, from the historical hardcoded
	// "/tmp" location too (which resolved to C:\tmp on Windows, GH#4636).
	// Double-remove is harmless when they coincide.
	_ = os.Remove(legacyFile)
	if os.Getenv(testCircuitBreakerDirEnv) == "" {
		_ = os.Remove(filepath.Join("/tmp", "beads-dolt-circuit-0.json"))
	}

	// Clean stale files in the dedicated subdirectory: open/half-open state
	// past circuitStaleTTL, and closed files no writer has touched in
	// closedSweepTTL. A recently written closed file is a breaker some
	// process is using and is left alone.
	_ = os.MkdirAll(dir, 0755)
	cleanStaleCircuitBreakerFilesIn(dir)

	// Also sweep the legacy hardcoded "/tmp/beads-circuit" location
	// (C:\tmp\beads-circuit on Windows) where older builds accumulated files
	// before the os.TempDir() fix (GH#4636), by the same rules: any
	// TMPDIR-less process (launchd, cron, a bare ssh command) still resolves
	// os.TempDir() to /tmp and uses it as its live directory, so its closed
	// files age out by mtime here too (see closedSweepTTL).
	if os.Getenv(testCircuitBreakerDirEnv) == "" {
		if legacy := filepath.Join("/tmp", "beads-circuit"); legacy != dir {
			cleanStaleCircuitBreakerFilesIn(legacy)
		}
	}
}

// staleCircuitSweep gates the store-open sweep; see
// cleanStaleCircuitBreakerFilesOnce. A pointer so a test can start a fresh one.
var staleCircuitSweep = new(sync.Once)

// cleanStaleCircuitBreakerFilesOnce is the store-open entry point to
// CleanStaleCircuitBreakerFiles, and makes it the init-time cleanup its doc
// describes: at most one sweep per process. A process opens stores more than
// once — every write command opens a non-mutating preview store for the
// workspace-identity check before its writable store (be-0gfcs), and a
// long-lived embedder opens one per request — and the sweep reads every
// breaker file in the directory each time it runs. Nothing it could find
// changes meaningfully between two opens of one process, and correctness never
// waited on it anyway: readState auto-expires a stale open state on the one
// file a breaker reads. bd doctor --fix calls CleanStaleCircuitBreakerFiles
// directly and still sweeps on demand.
func cleanStaleCircuitBreakerFilesOnce() {
	staleCircuitSweep.Do(CleanStaleCircuitBreakerFiles)
}

// cleanStaleCircuitBreakerFilesIn is the testable implementation of
// CleanStaleCircuitBreakerFiles that accepts a directory parameter.
//
// Besides stale open/half-open state, it removes closed-state files whose
// mtime is older than closedSweepTTL (GH#4636): a closed state file is written
// on every successful RecordSuccess, and since ephemeral ports mint a distinct
// filename per server, closed sidecars accumulate with no other cleanup path.
// The TTL is what makes that safe alongside live writers, which keep their
// file's mtime fresh: a recently written closed file reflects a healthy,
// currently-in-use breaker and is left alone.
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
		if state.State == circuitClosed {
			// Only past an mtime TTL: both directories have LIVE writers, so
			// an unconditional remove would churn against them forever (see
			// closedSweepTTL). Silent on purpose: removing routine closed-state
			// hygiene is not worth a stderr line per file per invocation.
			if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > closedSweepTTL {
				_ = os.Remove(path)
			}
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
	// A typed 1105 is a semantic response from Dolt, not evidence that the
	// server is unavailable, even if its message happens to mention a connection.
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1105 {
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
