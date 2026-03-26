// Package doltserver manages the lifecycle of a local dolt sql-server process.
// It provides transparent auto-start so that `bd init` and `bd <command>` work
// without manual server management.
//
// Port assignment uses OS-assigned ephemeral ports by default. When no explicit
// port is configured (env var, config.yaml, metadata.json), Start() asks the OS
// for a free port via net.Listen(":0"), passes it to dolt sql-server, and writes
// the actual port to dolt-server.port. This eliminates the birthday-problem
// collisions that plagued the old hash-derived port scheme (GH#2098, GH#2372).
//
// Users with explicit port config via BEADS_DOLT_SERVER_PORT env var or
// config.yaml always use that port instead, with conflict detection via
// reclaimPort.
//
// Server state files (PID, port, log, lock) live in the .beads/ directory.
package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/lockfile"
)

// ErrServerNotRunning is returned by Stop when the Dolt server is not running.
// Callers can use errors.Is to distinguish this expected condition from real
// failures (GH#2670).
var ErrServerNotRunning = errors.New("dolt server is not running")

// IgnoreNotRunning strips ErrServerNotRunning from err and returns any
// remaining errors (typically cleanup failures). If the only error was the
// sentinel, it returns nil. Handles both errors.Join (multi-unwrap) and
// standard fmt.Errorf wrapping (single-unwrap).
//
// IMPORTANT: call directly on Stop()/StopWithForce() return values only.
// Do not wrap the error before passing it here — wrapping may hide joined
// cleanup errors from the multi-unwrap path.
func IgnoreNotRunning(err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrServerNotRunning) {
		return err // unrelated error, pass through
	}
	// Multi-error from errors.Join: filter out the sentinel, keep the rest.
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var remaining []error
		for _, e := range joined.Unwrap() {
			if e != nil && !errors.Is(e, ErrServerNotRunning) {
				remaining = append(remaining, e)
			}
		}
		return errors.Join(remaining...)
	}
	// Single-wrapped error (e.g., fmt.Errorf("%w", ErrServerNotRunning)):
	// the sentinel is the only meaningful content, so treat as pure sentinel.
	return nil
}

// PIDFileName and PortFileName are the canonical state file names used by the
// Dolt server lifecycle. They are exported so cross-package tests can reference
// the same names as the production code.
const (
	PIDFileName  = "dolt-server.pid"
	PortFileName = "dolt-server.port"
)

// maxEphemeralPortAttempts is the number of times Start() retries ephemeral
// port allocation when the TOCTOU race causes a bind failure.
const maxEphemeralPortAttempts = 10

// DefaultSharedServerPort is the default port for shared server mode.
// Uses 3308 to avoid conflict with the orchestrator which uses 3307.
const DefaultSharedServerPort = 3308

// IsSharedServerMode returns true if shared server mode is enabled.
// Checks (in priority order):
//  1. BEADS_DOLT_SHARED_SERVER env var ("1" or "true")
//  2. dolt.shared-server in config.yaml
//
// Shared server mode means all projects on this machine share a single
// dolt sql-server process at SharedServerDir(), each using its own
// database (already unique via prefix-based naming in bd init).
func IsSharedServerMode() bool {
	if v := os.Getenv("BEADS_DOLT_SHARED_SERVER"); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	return config.GetBool("dolt.shared-server")
}

// IsAutoStartDisabled returns true if the dolt server should NOT be
// auto-started or managed by bd. When true, KillStaleServers and
// auto-start are suppressed — the server is externally managed (e.g.,
// by systemd).
//
// Either source can disable auto-start independently — there is no way
// to force-enable via env when the config file says disabled. Accepted
// disable values (case-insensitive): "0", "false", "off".
//
// This is used by KillStaleServers and Start to avoid killing or
// interfering with externally-managed dolt processes (GH#2641).
func IsAutoStartDisabled() bool {
	if v := strings.ToLower(os.Getenv("BEADS_DOLT_AUTO_START")); v == "0" || v == "false" || v == "off" {
		return true
	}
	v := strings.ToLower(config.GetString("dolt.auto-start"))
	return v == "false" || v == "0" || v == "off"
}

// SharedServerDir returns the directory for shared server state files.
// Returns ~/.beads/shared-server/ (created on first use).
func SharedServerDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	dir := filepath.Join(home, ".beads", "shared-server")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("cannot create shared server directory %s: %w", dir, err)
	}
	return dir, nil
}

// SharedDoltDir returns the dolt data directory for the shared server.
// Returns ~/.beads/shared-server/dolt/ (created on first use).
func SharedDoltDir() (string, error) {
	serverDir, err := SharedServerDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(serverDir, "dolt")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("cannot create shared dolt directory %s: %w", dir, err)
	}
	return dir, nil
}

// resolveServerDir returns the canonical server directory for dolt state files.
// In shared server mode, returns ~/.beads/shared-server/ instead of the
// project's .beads/ directory.
func resolveServerDir(beadsDir string) string {
	if IsSharedServerMode() {
		dir, err := SharedServerDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: shared server directory unavailable, using per-project mode: %v\n", err)
			return beadsDir
		}
		return dir
	}
	return beadsDir
}

// ResolveServerDir is the exported version of resolveServerDir.
// CLI commands use this to resolve the server directory before calling
// Start, Stop, or IsRunning.
func ResolveServerDir(beadsDir string) string {
	return resolveServerDir(beadsDir)
}

// ResolveDoltDir returns the dolt data directory for the given beadsDir.
// It checks the BEADS_DOLT_DATA_DIR env var and metadata.json for a custom
// dolt_data_dir, falling back to the default .beads/dolt/ path.
//
// Note: we check for metadata.json existence before calling configfile.Load
// to avoid triggering the config.json → metadata.json migration side effect,
// which would create files in the .beads/ directory unexpectedly.
func ResolveDoltDir(beadsDir string) string {
	// Shared server mode: use centralized dolt data directory
	if IsSharedServerMode() {
		dir, err := SharedDoltDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: shared dolt directory unavailable, using per-project mode: %v\n", err)
		} else {
			return dir
		}
	}

	// Check env var first (highest priority)
	if d := os.Getenv("BEADS_DOLT_DATA_DIR"); d != "" {
		if filepath.IsAbs(d) {
			return d
		}
		return filepath.Join(beadsDir, d)
	}
	// Only load config if metadata.json exists (avoids legacy migration side effect)
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if _, err := os.Stat(metadataPath); err == nil {
		if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
			return cfg.DatabasePath(beadsDir)
		}
	}
	return filepath.Join(beadsDir, "dolt")
}

// Config holds the server configuration.
type Config struct {
	BeadsDir string     // Path to .beads/ directory
	Port     int        // MySQL protocol port (0 = allocate ephemeral port on Start)
	Host     string     // Bind address (default: 127.0.0.1)
	Mode     ServerMode // Server ownership mode (Owned, External, Embedded)
}

// State holds runtime information about a managed server.
type State struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	DataDir string `json:"data_dir"`
}

// file paths within .beads/
func pidPath(beadsDir string) string  { return filepath.Join(beadsDir, PIDFileName) }
func logPath(beadsDir string) string  { return filepath.Join(beadsDir, "dolt-server.log") }
func lockPath(beadsDir string) string { return filepath.Join(beadsDir, "dolt-server.lock") }
func portPath(beadsDir string) string { return filepath.Join(beadsDir, PortFileName) }

// MaxDoltServers is the hard ceiling on concurrent dolt sql-server processes.
// Allows up to 3 (e.g., multiple projects).
func maxDoltServers() int {
	return 3
}

// allocateEphemeralPort asks the OS for a free TCP port on host.
// It binds to port 0, reads the assigned port, and closes the listener.
// The caller should pass the returned port to dolt sql-server promptly
// to minimize the TOCTOU window.
func allocateEphemeralPort(host string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, fmt.Errorf("allocating ephemeral port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// isPortAvailable checks if a TCP port is available for binding.
func isPortAvailable(host string, port int) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// reclaimPort ensures an explicit (user-configured) port is available for use.
// Only called for explicit ports (env var, config.yaml, metadata.json).
// If the port is busy:
//   - If our dolt server (same data dir) → return its PID for adoption
//   - If a stale/orphan dolt sql-server holds it → kill it and reclaim
//   - If another project's dolt or a non-dolt process → return error
//
// Returns (adoptPID, nil) when an existing server should be adopted.
// Returns (0, nil) when the port is free for a new server.
// Returns (0, err) when the port can't be used.
func reclaimPort(host string, port int, beadsDir string) (adoptPID int, err error) {
	if isPortAvailable(host, port) {
		return 0, nil // port is free
	}

	// Port is busy — find out what's using it
	pid := findPIDOnPort(port)
	if pid == 0 {
		// Can't identify the process; port may be in TIME_WAIT or transient use.
		// Wait briefly and retry.
		time.Sleep(2 * time.Second)
		if isPortAvailable(host, port) {
			return 0, nil
		}
		return 0, fmt.Errorf("port %d is busy but cannot identify the process.\n\nCheck with: %s", port, fmt.Sprintf(portConflictHint, port))
	}

	// Check if it's a dolt sql-server process
	if !isDoltProcess(pid) {
		return 0, fmt.Errorf("port %d is in use by a non-dolt process (PID %d).\n\nFree the port or configure a different one with: bd dolt set port <port>", port, pid)
	}

	// It's a dolt process. Check if it's one we should adopt.

	// Check if the process is using our data directory (CWD matches our dolt dir).
	// dolt sql-server is started with cmd.Dir = doltDir, so CWD is the data dir.
	doltDir := ResolveDoltDir(beadsDir)
	if isProcessInDir(pid, doltDir) {
		return pid, nil // our server — adopt it
	}

	// Another beads project's Dolt server is on this port.
	return 0, fmt.Errorf("port %d is in use by another project's dolt server (PID %d).\n\nFree the port or use a different one with: bd dolt set port <port>", port, pid)
}

// countDoltProcesses returns the number of running dolt sql-server processes.
func countDoltProcesses() int { return len(listDoltProcessPIDs()) }

// isDoltProcess checks if a PID belongs to a running dolt sql-server.
func isDoltProcess(pid int) bool {
	for _, p := range listDoltProcessPIDs() {
		if p == pid {
			return true
		}
	}
	return false
}

// readPortFile reads the actual port from the port file, if it exists.
// Returns 0 if the file doesn't exist or is unreadable.
func readPortFile(beadsDir string) int {
	data, err := os.ReadFile(portPath(beadsDir))
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return port
}

// writePortFile records the actual port the server is listening on.
func writePortFile(beadsDir string, port int) error {
	return os.WriteFile(portPath(beadsDir), []byte(strconv.Itoa(port)), 0600)
}

// EnsurePortFile makes the repo-local port file match the connected server port.
// This is a best-effort repair path for upgraded repos that are missing
// .beads/dolt-server.port even though commands can still connect.
func EnsurePortFile(beadsDir string, port int) error {
	if beadsDir == "" || port <= 0 {
		return nil
	}
	existing := readPortFile(beadsDir)
	if existing == port {
		return nil
	}
	if existing > 0 {
		fmt.Fprintf(os.Stderr, "Info: updating port file %d → %d in %s\n", existing, port, beadsDir)
	}
	return writePortFile(beadsDir, port)
}

// ReadPortFile returns the port from the project's dolt-server.port file,
// or 0 if the file doesn't exist or is invalid. Exported for use by bd init
// to detect whether this project has its own running server (GH#2336).
func ReadPortFile(beadsDir string) int {
	return readPortFile(beadsDir)
}

// DefaultConfig returns config with sensible defaults.
// Priority: env var > port file > config.yaml / global config > metadata.json.
// Returns port 0 when no source provides a port, meaning Start() should
// allocate an ephemeral port from the OS.
//
// The port file (dolt-server.port) is written by Start() with the actual port
// the server is listening on. Consulting it here ensures that commands
// connecting to an already-running server use the correct port.
func DefaultConfig(beadsDir string) *Config {
	// In shared mode, use the shared server directory for port resolution
	if IsSharedServerMode() {
		if sharedDir, err := SharedServerDir(); err == nil {
			beadsDir = sharedDir
		}
	}

	cfg := &Config{
		BeadsDir: beadsDir,
		Host:     "127.0.0.1",
		Mode:     ResolveServerMode(beadsDir),
	}

	// Check env var override first (used by tests and manual overrides)
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			cfg.Port = port
			return cfg
		}
	}

	// Check the port file (gitignored, local-only) — this is the primary
	// persistent source. Start() writes the actual listening port here.
	// Elevated to top priority (after env var) to prevent git-tracked values
	// from causing cross-project data leakage (GH#2372).
	if p := readPortFile(beadsDir); 0 < p {
		cfg.Port = p
		return cfg
	}

	// Check config.yaml / global config (~/.config/bd/config.yaml) (GH#2073)
	// Note: project-level config.yaml dolt.port is git-tracked and could
	// propagate to collaborators. Prefer the gitignored port file above.
	if cfg.Port == 0 {
		if p := config.GetYamlConfig("dolt.port"); p != "" {
			if port, err := strconv.Atoi(p); err == nil && port > 0 {
				cfg.Port = port
			}
		}
	}

	// Deprecated: metadata.json DoltServerPort is git-tracked and propagates
	// to all contributors, causing cross-project data leakage (GH#2372).
	// Emit a one-time warning but still use the value as a fallback so
	// existing setups don't break silently.
	if cfg.Port == 0 {
		if metaCfg, err := configfile.Load(beadsDir); err == nil && metaCfg != nil {
			if metaCfg.DoltServerPort > 0 {
				fmt.Fprintf(os.Stderr, "Warning: dolt_server_port in metadata.json is deprecated (can cause cross-project data leakage).\n")
				fmt.Fprintf(os.Stderr, "  The port file (.beads/dolt-server.port) is now the primary source.\n")
				fmt.Fprintf(os.Stderr, "  Remove dolt_server_port from .beads/metadata.json to silence this warning.\n")
				cfg.Port = metaCfg.DoltServerPort
			}
		}
	}

	// Port 0 means "no configured port". In shared mode, use the fixed
	// shared server port. In per-project mode, Start() will allocate an
	// ephemeral port from the OS (GH#2098, GH#2372).
	if cfg.Port == 0 && IsSharedServerMode() {
		cfg.Port = DefaultSharedServerPort // 3308 - avoids orchestrator conflict on 3307
	}

	return cfg
}

// IsRunning checks if a managed server is running for this beadsDir.
// Returns a State with Running=true if a valid dolt process is found.
func IsRunning(beadsDir string) (*State, error) {
	data, err := os.ReadFile(pidPath(beadsDir))
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Running: false}, nil
		}
		return nil, fmt.Errorf("reading PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		// Corrupt PID file implies stale state; clear the port file too.
		_ = os.Remove(pidPath(beadsDir))
		_ = os.Remove(portPath(beadsDir))
		return &State{Running: false}, nil
	}

	// Check if process is alive
	if !isProcessAlive(pid) {
		// Process is dead — clear all tracked state for this server.
		_ = os.Remove(pidPath(beadsDir))
		_ = os.Remove(portPath(beadsDir))
		return &State{Running: false}, nil
	}

	// Verify it's actually a dolt sql-server process
	if !isDoltProcess(pid) {
		// PID was reused by another process
		_ = os.Remove(pidPath(beadsDir))
		_ = os.Remove(portPath(beadsDir))
		return &State{Running: false}, nil
	}

	// Read actual port from port file; fall back to config-derived port.
	port := readPortFile(beadsDir)
	if port == 0 {
		cfg := DefaultConfig(beadsDir)
		port = cfg.Port
	}
	if port == 0 {
		// Server is running but we can't determine its port (port file
		// missing, no explicit config). Stop the orphan so that callers
		// (EnsureRunning) trigger a fresh Start() with a new port file.
		fmt.Fprintf(os.Stderr, "Dolt server (PID %d) running but port unknown; stopping for restart\n", pid)
		if err := gracefulStop(pid, 5*time.Second); err != nil {
			// Best-effort kill
			if proc, findErr := os.FindProcess(pid); findErr == nil {
				_ = proc.Kill()
			}
		}
		_ = os.Remove(pidPath(beadsDir))
		return &State{Running: false}, nil
	}
	return &State{
		Running: true,
		PID:     pid,
		Port:    port,
		DataDir: ResolveDoltDir(beadsDir),
	}, nil
}

// EnsureRunning starts the server if it is not already running.
// This is the main auto-start entry point. Thread-safe via file lock.
// Returns the port the server is listening on.
//
// When metadata.json specifies an explicit dolt_server_port (indicating an
// external/shared server, e.g. managed by systemd), EnsureRunning will NOT
// start a new server. The external server's lifecycle is not bd's
// responsibility — starting a per-project server would conflict with (or
// kill) the shared server. See GH#2554.
func EnsureRunning(beadsDir string) (int, error) {
	port, _, err := EnsureRunningDetailed(beadsDir)
	return port, err
}

// EnsureRunningDetailed is like EnsureRunning but also reports whether a new
// server was started (startedByUs=true) vs. an already-running server was
// adopted (startedByUs=false). Callers that need to clean up auto-started
// servers (e.g. test teardown) should use this variant.
func EnsureRunningDetailed(beadsDir string) (port int, startedByUs bool, err error) {
	serverDir := resolveServerDir(beadsDir)

	// Inform when an orchestrator is also running on this machine
	if IsSharedServerMode() && os.Getenv("GT_ROOT") != "" {
		fmt.Fprintf(os.Stderr, "Info: Orchestrator detected (GT_ROOT set). Shared server uses port %d to avoid conflict.\n", DefaultSharedServerPort)
	}

	state, err := IsRunning(serverDir)
	if err != nil {
		return 0, false, err
	}
	if state.Running {
		_ = EnsurePortFile(serverDir, state.Port)
		return state.Port, false, nil
	}

	// If the server mode is External (explicit port in metadata.json,
	// shared server mode, etc.), do not start a per-project server —
	// it would conflict with the external one.
	mode := ResolveServerMode(beadsDir)
	if mode == ServerModeExternal {
		cfg := DefaultConfig(beadsDir)
		return 0, false, fmt.Errorf("Dolt server is not running on port %d, and auto-start is suppressed "+
			"because the server is externally managed (dolt.auto-start: false or explicit port configured).\n\n"+
			"Start the external server, or enable auto-start to allow bd to manage the server.\n"+
			"  To start manually: bd dolt start\n"+
			"  To check status: bd dolt status", cfg.Port)
	}

	s, err := Start(serverDir)
	if err != nil {
		return 0, false, err
	}
	return s.Port, true, nil
}

// Start explicitly starts a dolt sql-server for the project.
// Returns the State of the started server, or an error.
func Start(beadsDir string) (*State, error) {
	cfg := DefaultConfig(beadsDir)
	doltDir := ResolveDoltDir(beadsDir)

	// Acquire exclusive lock to prevent concurrent starts
	lockF, err := os.OpenFile(lockPath(beadsDir), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("creating lock file: %w", err)
	}
	defer lockF.Close()

	if err := lockfile.FlockExclusiveNonBlocking(lockF); err != nil {
		if lockfile.IsLocked(err) {
			// Another bd process is starting the server — wait for it
			if err := lockfile.FlockExclusiveBlocking(lockF); err != nil {
				return nil, fmt.Errorf("waiting for server start lock: %w", err)
			}
			defer func() { _ = lockfile.FlockUnlock(lockF) }()

			// Lock acquired — check if server is now running
			state, err := IsRunning(beadsDir)
			if err != nil {
				return nil, err
			}
			if state.Running {
				return state, nil
			}
			// Still not running — fall through to start it ourselves
		} else {
			return nil, fmt.Errorf("acquiring start lock: %w", err)
		}
	} else {
		defer func() { _ = lockfile.FlockUnlock(lockF) }()
	}

	// Re-check after acquiring lock (double-check pattern)
	if state, _ := IsRunning(beadsDir); state != nil && state.Running {
		return state, nil
	}

	// Clean up orphaned dolt sql-server processes INSIDE the lock.
	// This MUST happen under the lock to prevent a race where one process
	// kills a server that another process is in the middle of starting
	// (PID file not yet written). Without this, concurrent bd processes
	// can cause journal corruption (GH#2430).
	if killed, killErr := KillStaleServers(beadsDir); killErr == nil && len(killed) > 0 {
		fmt.Fprintf(os.Stderr, "Info: cleaned up %d orphaned dolt sql-server process(es)\n", len(killed))
	}

	// Ensure dolt binary exists
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		return nil, fmt.Errorf("dolt is not installed (not found in PATH)\n\nInstall from: https://docs.dolthub.com/introduction/installation")
	}

	// Ensure dolt identity is configured
	if err := ensureDoltIdentity(); err != nil {
		return nil, fmt.Errorf("configuring dolt identity: %w", err)
	}

	// Ensure dolt database directory is initialized
	if err := ensureDoltInit(doltDir); err != nil {
		return nil, fmt.Errorf("initializing dolt database: %w", err)
	}

	// Open log file
	logFile, err := os.OpenFile(logPath(beadsDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600) //nolint:gosec // G304: logPath derives from user-configured beadsDir
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	// Resolve the port to use. Explicit ports (env/config) go through
	// reclaimPort for conflict detection. Port 0 means ephemeral — allocate
	// a fresh port from the OS with retry for TOCTOU races.
	actualPort := cfg.Port
	explicitPort := actualPort > 0

	if explicitPort {
		// Explicit port: check for conflicts and adopt existing servers.
		adoptPID, reclaimErr := reclaimPort(cfg.Host, actualPort, beadsDir)
		if reclaimErr != nil {
			_ = logFile.Close()
			return nil, fmt.Errorf("cannot start dolt server on port %d: %w", actualPort, reclaimErr)
		}
		if adoptPID > 0 {
			_ = logFile.Close()
			_ = os.WriteFile(pidPath(beadsDir), []byte(strconv.Itoa(adoptPID)), 0600)
			_ = writePortFile(beadsDir, actualPort)
			return &State{Running: true, PID: adoptPID, Port: actualPort, DataDir: doltDir}, nil
		}
	}

	// Start dolt sql-server, with retry loop for ephemeral port TOCTOU.
	var pid int
	var lastErr error
	attempts := 1
	if !explicitPort {
		attempts = maxEphemeralPortAttempts
	}

	for i := range attempts {
		if !explicitPort {
			p, allocErr := allocateEphemeralPort(cfg.Host)
			if allocErr != nil {
				lastErr = allocErr
				continue
			}
			actualPort = p
		}

		cmd := exec.Command(doltBin, "sql-server", //nolint:gosec // doltBin is resolved from PATH, not user input
			"-H", cfg.Host,
			"-P", strconv.Itoa(actualPort),
			"-l", "debug",
		)
		cmd.Dir = doltDir
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.Stdin = nil
		cmd.SysProcAttr = procAttrDetached()

		if startErr := cmd.Start(); startErr != nil {
			lastErr = startErr
			if !explicitPort {
				continue // retry with a new ephemeral port
			}
			_ = logFile.Close()
			return nil, fmt.Errorf("starting dolt sql-server: %w", startErr)
		}

		pid = cmd.Process.Pid
		_ = cmd.Process.Release()

		// Quick check: did the process exit immediately (bind failure)?
		// Give it a moment to fail on port bind before proceeding.
		time.Sleep(200 * time.Millisecond)
		if !isProcessAlive(pid) {
			lastErr = fmt.Errorf("dolt sql-server exited immediately on port %d (attempt %d/%d)", actualPort, i+1, attempts)
			pid = 0
			if !explicitPort {
				continue
			}
			_ = logFile.Close()
			return nil, lastErr
		}

		lastErr = nil
		break
	}
	_ = logFile.Close()

	if lastErr != nil {
		return nil, fmt.Errorf("failed to start dolt server after %d attempts: %w\nCheck logs: %s",
			attempts, lastErr, logPath(beadsDir))
	}

	// Write PID and port files
	if err := os.WriteFile(pidPath(beadsDir), []byte(strconv.Itoa(pid)), 0600); err != nil {
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			_ = proc.Kill()
		}
		return nil, fmt.Errorf("writing PID file: %w", err)
	}
	if err := writePortFile(beadsDir, actualPort); err != nil {
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			_ = proc.Kill()
		}
		_ = os.Remove(pidPath(beadsDir))
		return nil, fmt.Errorf("writing port file: %w", err)
	}

	// Wait for server to accept connections
	if err := waitForReady(cfg.Host, actualPort, 10*time.Second); err != nil {
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			_ = proc.Kill()
		}
		_ = os.Remove(pidPath(beadsDir))
		_ = os.Remove(portPath(beadsDir))
		return nil, fmt.Errorf("server started (PID %d) but not accepting connections on port %d: %w\nCheck logs: %s",
			pid, actualPort, err, logPath(beadsDir))
	}

	return &State{
		Running: true,
		PID:     pid,
		Port:    actualPort,
		DataDir: doltDir,
	}, nil
}

// FlushWorkingSet connects to the running Dolt server and commits any uncommitted
// working set changes across all databases. This prevents data loss when the server
// is about to be stopped or restarted. Returns nil if there's nothing to flush or
// if the server is not reachable (best-effort).
func FlushWorkingSet(host string, port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dsn := fmt.Sprintf("root@tcp(%s:%d)/?parseTime=true", host, port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("flush: failed to open connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(10 * time.Second)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("flush: server not reachable: %w", err)
	}

	// List all databases, skipping system databases
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return fmt.Errorf("flush: failed to list databases: %w", err)
	}
	var databases []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		// Skip Dolt system databases
		if name == "information_schema" || name == "mysql" || name == "performance_schema" {
			continue
		}
		databases = append(databases, name)
	}
	_ = rows.Close()

	if len(databases) == 0 {
		return nil
	}

	var flushed int
	for _, dbName := range databases {
		// Check for uncommitted changes via dolt_status
		var hasChanges bool
		row := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) > 0 FROM `%s`.dolt_status", dbName))
		if err := row.Scan(&hasChanges); err != nil {
			// dolt_status may not exist for non-beads databases; skip
			continue
		}
		if !hasChanges {
			continue
		}

		// Commit all uncommitted changes
		_, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName))
		if err != nil {
			fmt.Fprintf(os.Stderr, "flush: failed to USE %s: %v\n", dbName, err)
			continue
		}
		_, err = db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'auto-flush: commit working set before server stop')")
		if err != nil {
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "nothing to commit") || strings.Contains(errStr, "no changes") {
				continue
			}
			fmt.Fprintf(os.Stderr, "flush: failed to commit %s: %v\n", dbName, err)
			continue
		}
		flushed++
	}

	if flushed > 0 {
		fmt.Fprintf(os.Stderr, "Flushed working set for %d database(s) before server stop\n", flushed)
	}
	return nil
}

// Stop gracefully stops the managed server and its idle monitor.
// Stop is idempotent: when the server is already stopped it returns
// ErrServerNotRunning after cleaning up any leftover state files.
// Callers should use errors.Is(err, ErrServerNotRunning) to distinguish
// this expected condition from real failures.
func Stop(beadsDir string) error {
	return StopWithForce(beadsDir, false)
}

// StopWithForce is like Stop but with an optional force flag.
func StopWithForce(beadsDir string, force bool) error {

	state, err := IsRunning(beadsDir)
	if err != nil {
		return err
	}
	if !state.Running {
		// Server not running — still clean up any leftover state files
		// so bd dolt status won't report stale state (GH#2670).
		// Join cleanup errors with the sentinel so callers can still use
		// errors.Is(err, ErrServerNotRunning) while operators see filesystem issues.
		cleanupErr := cleanupStateFiles(beadsDir)
		return errors.Join(ErrServerNotRunning, cleanupErr)
	}

	// Flush uncommitted working set changes before stopping the server.
	// This prevents data loss when changes have been written but not yet committed.
	cfg := DefaultConfig(beadsDir)
	if flushErr := FlushWorkingSet(cfg.Host, state.Port); flushErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not flush working set before stop: %v\n", flushErr)
	}

	if err := gracefulStop(state.PID, 5*time.Second); err != nil {
		return errors.Join(err, cleanupStateFiles(beadsDir))
	}
	return cleanupStateFiles(beadsDir)
}

// cleanupStateFiles removes all server state files (PID and port).
// Returns a joined error for non-NotExist removal failures so callers
// can surface filesystem problems while still treating "already clean"
// as success. Logs non-NotExist errors at debug level (GH#2670).
func cleanupStateFiles(beadsDir string) error {
	var errs []error
	for _, path := range []string{pidPath(beadsDir), portPath(beadsDir)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			debug.Logf("failed to remove server state file %s: %v", path, err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// LogPath returns the path to the server log file.
func LogPath(beadsDir string) string {
	return logPath(beadsDir)
}

// killStaleServersForDir finds and kills orphan dolt sql-server processes for
// the current repo's Dolt data directory that are not tracked by the canonical
// PID file. Only processes that beads started (tracked via the PID file) are
// eligible for cleanup. Externally-managed servers are never killed.
//
// A process is considered "external" (never kill) when any of:
//   - ResolveServerMode() returns ServerModeExternal (explicit port, shared server, etc.)
//   - No PID file exists (beads has no record of starting a server)
func killStaleServersForDir(beadsDir string, allPIDs []int, inDir func(int, string) bool, kill func(int) error) ([]int, error) {
	if len(allPIDs) == 0 {
		return nil, nil
	}

	// If auto-start is disabled the server is externally managed (e.g., by
	// systemd or a manual bd dolt start), so we must not kill any processes.
	// IsAutoStartDisabled covers the BEADS_DOLT_AUTO_START env var and
	// dolt.auto-start config; ResolveServerMode covers explicit port/shared
	// server/embedded configurations. Both indicate "not our server" (GH#2641).
	if IsAutoStartDisabled() || ResolveServerMode(beadsDir) == ServerModeExternal {
		return nil, nil
	}

	serverDir := resolveServerDir(beadsDir)

	// Read the canonical PID from the PID file. If there is no PID file,
	// beads has no record of having started a server for this directory,
	// so there is nothing stale to clean up. This prevents killing
	// externally-started servers (systemd, other repos sharing a data dir).
	var canonicalPID int
	if data, readErr := os.ReadFile(pidPath(serverDir)); readErr == nil {
		if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid > 0 {
			canonicalPID = pid
		}
	}
	if canonicalPID == 0 {
		// No valid PID file → no beads-owned server to compare against.
		// Nothing is stale from our perspective.
		return nil, nil
	}

	// The canonical PID itself is alive and tracked — never kill it.
	// Only kill OTHER dolt processes in our data dir (orphans from a
	// previous beads-started server that lost its PID file tracking).
	ownedDoltDir := ResolveDoltDir(serverDir)

	var killed []int
	for _, pid := range allPIDs {
		if pid == os.Getpid() {
			continue
		}
		if pid == canonicalPID {
			continue // preserve canonical server
		}
		if !inDir(pid, ownedDoltDir) {
			continue // preserve other repos' Dolt servers
		}
		if err := kill(pid); err == nil {
			killed = append(killed, pid)
		}
	}
	return killed, nil
}

// KillStaleServers finds and kills orphan dolt sql-server processes for the
// current repo's Dolt data directory that are not tracked by the canonical PID
// file. Returns the PIDs of killed processes.
//
// When auto-start is disabled (BEADS_DOLT_AUTO_START=0 or dolt.auto-start:
// false), this function is a no-op — the dolt server is externally managed
// and must not be killed by bd (GH#2641).
func KillStaleServers(beadsDir string) ([]int, error) {
	if IsAutoStartDisabled() {
		return nil, nil
	}
	allPIDs := listDoltProcessPIDs()
	return killStaleServersForDir(
		beadsDir,
		allPIDs,
		isProcessInDir,
		func(pid int) error {
			proc, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return proc.Kill()
		},
	)
}

// waitForReady polls TCP until the server accepts connections.
func waitForReady(host string, port int, timeout time.Duration) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond) //nolint:gosec // G704: addr is built from internal host+port, not user input
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("timeout after %s waiting for server at %s", timeout, addr)
}

// ensureDoltIdentity sets dolt global user identity from git config if not already set.
func ensureDoltIdentity() error {
	// Check if dolt identity is already configured
	nameCmd := exec.Command("dolt", "config", "--global", "--get", "user.name")
	if out, err := nameCmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return nil // Already configured
	}

	// Try to get identity from git
	gitName := "beads"
	gitEmail := "beads@localhost"

	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			gitName = name
		}
	}
	if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
		if email := strings.TrimSpace(string(out)); email != "" {
			gitEmail = email
		}
	}

	if out, err := exec.Command("dolt", "config", "--global", "--add", "user.name", gitName).CombinedOutput(); err != nil {
		return fmt.Errorf("setting dolt user.name: %w\n%s", err, out)
	}
	if out, err := exec.Command("dolt", "config", "--global", "--add", "user.email", gitEmail).CombinedOutput(); err != nil {
		return fmt.Errorf("setting dolt user.email: %w\n%s", err, out)
	}

	return nil
}

// bdDoltMarker is a file written after ensureDoltInit successfully creates a
// dolt database. Its absence in an existing .dolt/ directory indicates the
// database was created by a pre-0.56 bd version (which used embedded mode).
// Those databases are incompatible with the current server-only architecture.
const bdDoltMarker = ".bd-dolt-ok"

// ensureDoltInit initializes a dolt database directory if .dolt/ doesn't exist.
// If .dolt/ exists, seeds the .bd-dolt-ok marker for existing working databases.
// See GH#2137 for background on pre-0.56 database compatibility.
func ensureDoltInit(doltDir string) error {
	if err := os.MkdirAll(doltDir, 0750); err != nil {
		return fmt.Errorf("creating dolt directory: %w", err)
	}

	dotDolt := filepath.Join(doltDir, ".dolt")
	markerPath := filepath.Join(doltDir, bdDoltMarker)

	if _, err := os.Stat(dotDolt); err == nil {
		// .dolt/ exists — seed the marker if missing.
		// This is the non-destructive path: we just mark existing databases
		// as known. The destructive recovery path (RecoverPreV56DoltDir) is
		// triggered separately during version upgrades.
		if _, markerErr := os.Stat(markerPath); os.IsNotExist(markerErr) {
			_ = os.WriteFile(markerPath, []byte("ok\n"), 0600) // Seed marker
		}
		return nil // Already initialized
	}

	cmd := exec.Command("dolt", "init")
	cmd.Dir = doltDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dolt init: %w\n%s", err, out)
	}

	// Write version marker so future runs know this database is compatible
	_ = os.WriteFile(markerPath, []byte("ok\n"), 0600)

	return nil
}

// RecoverPreV56DoltDir removes and reinitializes a dolt database that was
// created by a pre-0.56 bd version. Call this during version upgrade detection
// (e.g., from autoMigrateOnVersionBump when previousVersion < 0.56).
//
// Pre-0.56 databases used embedded Dolt mode with a different Dolt library
// version that may produce nil DoltDB values, causing panics (GH#2137).
// The data is unrecoverable — the fix is to start fresh.
//
// Returns true if recovery was performed, false if not needed.
func RecoverPreV56DoltDir(doltDir string) (bool, error) {
	dotDolt := filepath.Join(doltDir, ".dolt")
	if _, err := os.Stat(dotDolt); os.IsNotExist(err) {
		return false, nil // No .dolt/ directory — nothing to recover
	}

	markerPath := filepath.Join(doltDir, bdDoltMarker)
	if _, err := os.Stat(markerPath); err == nil {
		return false, nil // Marker exists — database is from 0.56+
	}

	fmt.Fprintf(os.Stderr, "Detected dolt database from an older bd version (pre-0.56).\n")
	fmt.Fprintf(os.Stderr, "Rebuilding dolt database at %s ...\n", doltDir)

	if err := os.RemoveAll(dotDolt); err != nil {
		return false, fmt.Errorf("cannot remove old dolt database at %s: %w\n\n"+
			"Manually delete %s and retry", dotDolt, err, dotDolt)
	}

	// Reinitialize
	if err := ensureDoltInit(doltDir); err != nil {
		return true, fmt.Errorf("recovery: %w", err)
	}

	return true, nil
}

// IsPreV56DoltDir returns true if doltDir contains a .dolt/ directory that
// was NOT created by bd 0.56+ (missing .bd-dolt-ok marker). These databases
// were created by the old embedded Dolt mode and may be incompatible.
// Used by doctor checks to detect potentially problematic dolt databases.
func IsPreV56DoltDir(doltDir string) bool {
	dotDolt := filepath.Join(doltDir, ".dolt")
	if _, err := os.Stat(dotDolt); os.IsNotExist(err) {
		return false // No .dolt/ at all
	}
	markerPath := filepath.Join(doltDir, bdDoltMarker)
	_, err := os.Stat(markerPath)
	return os.IsNotExist(err)
}
