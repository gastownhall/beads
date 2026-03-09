// Package doltserver manages the lifecycle of a local dolt sql-server process.
// It provides transparent auto-start so that `bd init` and `bd <command>` work
// without manual server management.
//
// The default port is 3307 (configfile.DefaultDoltServerPort),
// matching shared Homebrew Dolt servers. If another project's Dolt server already
// occupies port 3307, Start falls back to DerivePort for per-project isolation
// (hash-derived, range 13307–14306). Users with explicit port config in
// metadata.json or BEADS_DOLT_SERVER_PORT env var always use that port instead.
//
// Anti-proliferation: the server enforces one-server-one-port. If the canonical
// port is busy, the server identifies and handles the occupant rather than
// silently starting on another port.
//
// Server state files (PID, log, lock) live in the .beads/ directory.
package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
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
	"github.com/steveyegge/beads/internal/lockfile"
)

// Port range for auto-derived ports.
const (
	portRangeBase = 13307
	portRangeSize = 1000
)

// resolveServerDir returns the canonical server directory for dolt state files.
// Returns beadsDir unchanged.
func resolveServerDir(beadsDir string) string {
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
	BeadsDir string // Path to .beads/ directory
	Port     int    // MySQL protocol port (0 = use DefaultDoltServerPort 3307)
	Host     string // Bind address (default: 127.0.0.1)
}

// State holds runtime information about a managed server.
type State struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	DataDir string `json:"data_dir"`
}

// file paths within .beads/
func pidPath(beadsDir string) string      { return filepath.Join(beadsDir, "dolt-server.pid") }
func logPath(beadsDir string) string      { return filepath.Join(beadsDir, "dolt-server.log") }
func lockPath(beadsDir string) string     { return filepath.Join(beadsDir, "dolt-server.lock") }
func portPath(beadsDir string) string     { return filepath.Join(beadsDir, "dolt-server.port") }
func activityPath(beadsDir string) string { return filepath.Join(beadsDir, "dolt-server.activity") }
func monitorPidPath(beadsDir string) string {
	return filepath.Join(beadsDir, "dolt-monitor.pid")
}

// MaxDoltServers is the hard ceiling on concurrent dolt sql-server processes.
// Allows up to 3 (e.g., multiple projects).
func maxDoltServers() int {
	return 3
}

// ErrPortOccupiedByOtherProject is returned by reclaimPort when the canonical
// port is held by another beads project's Dolt server (different data dir).
// Start uses this to fall back to DerivePort for per-project isolation.
var ErrPortOccupiedByOtherProject = fmt.Errorf("port occupied by another project's dolt server")

// fallbackPort returns the DerivePort value for a beadsDir, used when the
// default port (3307) is occupied by another project's Dolt server.
func fallbackPort(beadsDir string) int {
	return DerivePort(beadsDir)
}

// DerivePort computes a stable port from the beadsDir path.
// Maps to range 13307–14306 (1000 ports) to avoid common service ports.
// The port is deterministic: same path always yields the same port.
//
// The 1000-port hash space means collisions become likely around 9+
// concurrent projects (~3.9% probability via the birthday paradox with
// fnv32a % 1000). This is acceptable because reclaimPort() in Start()
// detects when another project's server already occupies the derived
// port and falls back gracefully — hash collisions cause a retry, not
// a failure.
func DerivePort(beadsDir string) int {
	abs, err := filepath.Abs(beadsDir)
	if err != nil {
		abs = beadsDir
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(abs))
	return portRangeBase + int(h.Sum32()%uint32(portRangeSize))
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

// reclaimPort ensures the canonical port is available for use.
// If the port is busy:
//   - If our dolt server (same data dir) → return its PID for adoption
//   - If a stale/orphan dolt sql-server holds it → kill it and reclaim
//   - If a non-dolt process holds it → return error (don't silently use another port)
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
	// Don't kill it — return a sentinel so Start can fall back to DerivePort.
	return 0, ErrPortOccupiedByOtherProject
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
	if readPortFile(beadsDir) == port {
		return nil
	}
	return writePortFile(beadsDir, port)
}

// DefaultConfig returns config with sensible defaults.
// Priority: env var > metadata.json > config.yaml / global config > port file > DerivePort.
//
// The port file (dolt-server.port) is written by Start() with the actual port
// the server is listening on. Consulting it here ensures that commands
// connecting to an already-running server use the correct port — even when
// Start() fell back to DerivePort because another project occupied the default
// port.
func DefaultConfig(beadsDir string) *Config {
	cfg := &Config{
		BeadsDir: beadsDir,
		Host:     "127.0.0.1",
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

	if cfg.Port == 0 {
		cfg.Port = DerivePort(beadsDir)
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
		// Corrupt PID file — clean up
		_ = os.Remove(pidPath(beadsDir))
		return &State{Running: false}, nil
	}

	// Check if process is alive
	if !isProcessAlive(pid) {
		// Process is dead — stale PID file
		_ = os.Remove(pidPath(beadsDir))
		return &State{Running: false}, nil
	}

	// Verify it's actually a dolt sql-server process
	if !isDoltProcess(pid) {
		// PID was reused by another process
		_ = os.Remove(pidPath(beadsDir))
		_ = os.Remove(portPath(beadsDir))
		return &State{Running: false}, nil
	}

	// Read actual port from port file; fall back to config-derived port
	port := readPortFile(beadsDir)
	if port == 0 {
		cfg := DefaultConfig(beadsDir)
		port = cfg.Port
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
func EnsureRunning(beadsDir string) (int, error) {
	serverDir := resolveServerDir(beadsDir)

	state, err := IsRunning(serverDir)
	if err != nil {
		return 0, err
	}
	if state.Running {
		_ = EnsurePortFile(serverDir, state.Port)
		// Touch activity file so idle monitor knows we're active
		touchActivity(serverDir)
		return state.Port, nil
	}

	s, err := Start(serverDir)
	if err != nil {
		return 0, err
	}
	touchActivity(serverDir)
	return s.Port, nil
}

// touchActivity updates the activity file timestamp.
func touchActivity(beadsDir string) {
	_ = os.WriteFile(activityPath(beadsDir), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0600)
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

	// Reclaim the canonical port. If another project's Dolt holds it,
	// fall back to a hash-derived port for per-project isolation.
	actualPort := cfg.Port
	adoptPID, reclaimErr := reclaimPort(cfg.Host, actualPort, beadsDir)
	if reclaimErr != nil {
		if errors.Is(reclaimErr, ErrPortOccupiedByOtherProject) {
			// Another project's Dolt server is on the default port —
			// use a hash-derived port for this project instead.
			fmt.Fprintf(os.Stderr, "Port %d occupied by another project's Dolt server; falling back to port %d\n", actualPort, fallbackPort(beadsDir))
			actualPort = fallbackPort(beadsDir)
			adoptPID, reclaimErr = reclaimPort(cfg.Host, actualPort, beadsDir)
			if reclaimErr != nil {
				_ = logFile.Close()
				return nil, fmt.Errorf("cannot start dolt server on fallback port %d: %w", actualPort, reclaimErr)
			}
		} else {
			_ = logFile.Close()
			return nil, fmt.Errorf("cannot start dolt server on port %d: %w", actualPort, reclaimErr)
		}
	}
	if adoptPID > 0 {
		// Existing server is ours (same data dir) — adopt it
		_ = logFile.Close()
		_ = os.WriteFile(pidPath(beadsDir), []byte(strconv.Itoa(adoptPID)), 0600)
		_ = writePortFile(beadsDir, actualPort)
		touchActivity(beadsDir)
		forkIdleMonitor(beadsDir)
		return &State{Running: true, PID: adoptPID, Port: actualPort, DataDir: doltDir}, nil
	}

	// Start dolt sql-server
	cmd := exec.Command(doltBin, "sql-server", //nolint:gosec // G702: doltBin is resolved from PATH, not user input
		"-H", cfg.Host,
		"-P", strconv.Itoa(actualPort),
	)
	cmd.Dir = doltDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// New process group so server survives bd exit
	cmd.SysProcAttr = procAttrDetached()

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("starting dolt sql-server: %w", err)
	}
	_ = logFile.Close()

	pid := cmd.Process.Pid

	// Write PID and port files
	if err := os.WriteFile(pidPath(beadsDir), []byte(strconv.Itoa(pid)), 0600); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("writing PID file: %w", err)
	}
	if err := writePortFile(beadsDir, actualPort); err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(pidPath(beadsDir))
		return nil, fmt.Errorf("writing port file: %w", err)
	}

	// Release the process handle so it outlives us
	_ = cmd.Process.Release()

	// Wait for server to accept connections
	if err := waitForReady(cfg.Host, actualPort, 10*time.Second); err != nil {
		// Server started but not responding — clean up
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			_ = proc.Kill()
		}
		_ = os.Remove(pidPath(beadsDir))
		_ = os.Remove(portPath(beadsDir))
		return nil, fmt.Errorf("server started (PID %d) but not accepting connections on port %d: %w\nCheck logs: %s",
			pid, actualPort, err, logPath(beadsDir))
	}

	// Touch activity and fork idle monitor
	touchActivity(beadsDir)
	forkIdleMonitor(beadsDir)

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
		return fmt.Errorf("Dolt server is not running")
	}

	// Flush uncommitted working set changes before stopping the server.
	// This prevents data loss when changes have been written but not yet committed.
	cfg := DefaultConfig(beadsDir)
	if flushErr := FlushWorkingSet(cfg.Host, state.Port); flushErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not flush working set before stop: %v\n", flushErr)
	}

	if err := gracefulStop(state.PID, 5*time.Second); err != nil {
		cleanupStateFiles(beadsDir)
		return err
	}
	cleanupStateFiles(beadsDir)
	return nil
}

// cleanupStateFiles removes all server state files.
func cleanupStateFiles(beadsDir string) {
	_ = os.Remove(pidPath(beadsDir))
	_ = os.Remove(portPath(beadsDir))
	_ = os.Remove(activityPath(beadsDir))
	stopIdleMonitor(beadsDir)
}

// LogPath returns the path to the server log file.
func LogPath(beadsDir string) string {
	return logPath(beadsDir)
}

// KillStaleServers finds and kills orphan dolt sql-server processes
// not tracked by the canonical PID file.
// Returns the PIDs of killed processes.
func KillStaleServers(beadsDir string) ([]int, error) {
	allPIDs := listDoltProcessPIDs()
	if len(allPIDs) == 0 {
		return nil, nil
	}

	// Collect canonical PIDs (ones we should NOT kill)
	canonicalPIDs := make(map[int]bool)
	serverDir := resolveServerDir(beadsDir)
	if serverDir != "" {
		if data, readErr := os.ReadFile(pidPath(serverDir)); readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid > 0 {
				canonicalPIDs[pid] = true
			}
		}
	}

	var killed []int
	for _, pid := range allPIDs {
		if pid == os.Getpid() {
			continue
		}
		if canonicalPIDs[pid] {
			continue // preserve canonical server
		}
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			_ = proc.Kill()
			killed = append(killed, pid)
		}
	}
	return killed, nil
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

// --- Idle monitor ---

// DefaultIdleTimeout is the default duration before the idle monitor stops the server.
const DefaultIdleTimeout = 30 * time.Minute

// MonitorCheckInterval is how often the idle monitor checks activity.
const MonitorCheckInterval = 60 * time.Second

// stopServerProcess stops the Dolt server process without touching the idle
// monitor's own state. This is used by the idle monitor to avoid killing itself
// when shutting down an idle server. It flushes the working set, gracefully
// stops the server, and removes server state files (PID, port) but leaves the
// monitor PID file and activity file intact so the monitor can continue running
// as a watchdog.
func stopServerProcess(beadsDir string) error {
	state, err := IsRunning(beadsDir)
	if err != nil {
		return err
	}
	if !state.Running {
		return nil // already stopped
	}

	// Flush uncommitted working set changes before stopping.
	cfg := DefaultConfig(beadsDir)
	if flushErr := FlushWorkingSet(cfg.Host, state.Port); flushErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not flush working set before stop: %v\n", flushErr)
	}

	if err := gracefulStop(state.PID, 5*time.Second); err != nil {
		_ = os.Remove(pidPath(beadsDir))
		_ = os.Remove(portPath(beadsDir))
		return err
	}
	_ = os.Remove(pidPath(beadsDir))
	_ = os.Remove(portPath(beadsDir))
	return nil
}

// forkIdleMonitor starts the idle monitor as a detached process.
// It runs `bd dolt idle-monitor --beads-dir=<dir>` in the background.
func forkIdleMonitor(beadsDir string) {
	// Don't fork if there's already a monitor running
	if isMonitorRunning(beadsDir) {
		return
	}

	bdBin, err := os.Executable()
	if err != nil {
		return // best effort
	}

	cmd := exec.Command(bdBin, "dolt", "idle-monitor", "--beads-dir", beadsDir)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = procAttrDetached()

	if err := cmd.Start(); err != nil {
		return // best effort
	}

	// Write monitor PID file
	_ = os.WriteFile(monitorPidPath(beadsDir), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
	_ = cmd.Process.Release()
}

// isMonitorRunning checks if the idle monitor process is alive.
func isMonitorRunning(beadsDir string) bool {
	data, err := os.ReadFile(monitorPidPath(beadsDir))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	return isProcessAlive(pid)
}

// stopIdleMonitor kills the idle monitor process if running.
func stopIdleMonitor(beadsDir string) {
	data, err := os.ReadFile(monitorPidPath(beadsDir))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = os.Remove(monitorPidPath(beadsDir))
		return
	}
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
	_ = os.Remove(monitorPidPath(beadsDir))
}

// ReadActivityTime reads the last activity timestamp from the activity file.
// Returns zero time if the file doesn't exist or is unreadable.
func ReadActivityTime(beadsDir string) time.Time {
	data, err := os.ReadFile(activityPath(beadsDir))
	if err != nil {
		return time.Time{}
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

// RunIdleMonitor is the main loop for the idle monitor sidecar process.
// It checks the activity file periodically and stops the server if idle
// for longer than the configured timeout. After stopping an idle server,
// the monitor continues running as a watchdog: if new activity appears
// (e.g. a bd command calls EnsureRunning and touches the activity file),
// the monitor restarts the server. The monitor only exits after an
// additional full idle timeout passes with no new activity.
//
// If the server crashed but activity is recent, the monitor restarts it
// (watchdog behavior).
//
// idleTimeout of 0 means monitoring is disabled (exits immediately).
func RunIdleMonitor(beadsDir string, idleTimeout time.Duration) {
	if idleTimeout == 0 {
		return
	}

	// Single-instance enforcement: acquire an exclusive lock on the monitor
	// lock file. If another monitor is already running, exit immediately.
	// This prevents the accumulation bug (GH#2367) where Start() called from
	// within the monitor's watchdog restart would fork yet another monitor.
	monitorLockPath := monitorPidPath(beadsDir) + ".lock"
	var monitorLock *os.File
	if f, err := os.OpenFile(monitorLockPath, os.O_CREATE|os.O_RDWR, 0600); err == nil { //nolint:gosec // G304: path derived from trusted beadsDir
		if lockErr := lockfile.FlockExclusiveNonBlocking(f); lockErr != nil {
			_ = f.Close()
			return // another monitor holds the lock — exit silently
		}
		monitorLock = f
	}
	// Keep lock held for lifetime of this process. Clean up on exit.
	defer func() {
		_ = os.Remove(monitorPidPath(beadsDir))
		if monitorLock != nil {
			_ = lockfile.FlockUnlock(monitorLock)
			_ = monitorLock.Close()
			_ = os.Remove(monitorLockPath)
		}
	}()

	// Write our PID now that we hold the lock
	_ = os.WriteFile(monitorPidPath(beadsDir), []byte(strconv.Itoa(os.Getpid())), 0600)

	// Tracks when we stopped the server for idle timeout. Zero means we
	// haven't performed an idle shutdown (or the server was restarted since).
	var idleShutdownAt time.Time

	for {
		time.Sleep(MonitorCheckInterval)

		state, err := IsRunning(beadsDir)
		if err != nil {
			continue
		}

		lastActivity := ReadActivityTime(beadsDir)
		idleDuration := time.Since(lastActivity)

		if state.Running {
			idleShutdownAt = time.Time{} // server is up, clear idle-shutdown tracking

			// Server is running — check if idle
			if !lastActivity.IsZero() && idleDuration > idleTimeout {
				// Idle too long — stop the server but keep monitoring.
				// Use stopServerProcess (not Stop) to avoid killing ourselves.
				_ = stopServerProcess(beadsDir)
				idleShutdownAt = time.Now()
			}
		} else {
			// Server is NOT running
			if !idleShutdownAt.IsZero() {
				// We stopped it for idle timeout. Check for new activity
				// (e.g. EnsureRunning touched the activity file).
				if !lastActivity.IsZero() && lastActivity.After(idleShutdownAt) {
					// New activity since we stopped — restart
					_, _ = Start(beadsDir)
					idleShutdownAt = time.Time{}
					continue
				}
				// No new activity yet. If we've been waiting longer than
				// another full idle timeout since shutdown, give up and exit.
				if time.Since(idleShutdownAt) > idleTimeout {
					_ = os.Remove(monitorPidPath(beadsDir))
					return
				}
				// Keep waiting for new activity
				continue
			}

			// Server is down but we didn't stop it (crash or external stop)
			if lastActivity.IsZero() || idleDuration > idleTimeout {
				// No recent activity — just exit
				_ = os.Remove(monitorPidPath(beadsDir))
				return
			}
			// Recent activity but server crashed — restart
			_, _ = Start(beadsDir)
		}
	}
}
