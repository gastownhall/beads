// Package doltserver manages the lifecycle of a local dolt sql-server process.
// It provides transparent auto-start so that `bd init` and `bd <command>` work
// without manual server management.
//
// Under Gas Town (GT_ROOT set, or detected via filesystem heuristic),
// all worktrees share a single server on port 3307.
// In standalone mode, each project gets a deterministic port derived from the
// project path (hash → range 13307–14307). Users with explicit port config in
// metadata.json always use that port instead.
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

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
)

// Port range for auto-derived ports.
const (
	portRangeBase = 13307
	portRangeSize = 1000
)

// GasTownPort is the fixed port used when running under Gas Town (GT_ROOT set).
// All worktrees share this single server instead of each getting a derived port.
const GasTownPort = 3307

// resolveServerDir returns the canonical server directory for dolt state files.
// Under Gas Town (GT_ROOT set), all server operations use $GT_ROOT/.beads/
// so that N worktrees share one server instead of spawning N servers.
// Outside Gas Town, returns beadsDir unchanged.
func resolveServerDir(beadsDir string) string {
	if gtRoot := os.Getenv("GT_ROOT"); gtRoot != "" {
		return filepath.Join(gtRoot, ".beads")
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
	Port     int    // MySQL protocol port (0 = auto-derive from path)
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
// Under Gas Town, only 1 is allowed. Standalone allows up to 3 (e.g., multiple projects).
func maxDoltServers() int {
	if IsDaemonManaged() {
		return 1
	}
	return 3
}

// DerivePort computes a stable port from the beadsDir path.
// Maps to range 13307–14306 to avoid common service ports.
// The port is deterministic: same path always yields the same port.
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
//   - If our dolt server (same data dir or daemon-managed) → return its PID for adoption
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

	// Under Gas Town, check the daemon PID file first
	if gtRoot := os.Getenv("GT_ROOT"); gtRoot != "" {
		daemonPidFile := filepath.Join(gtRoot, "daemon", "dolt.pid")
		if data, readErr := os.ReadFile(daemonPidFile); readErr == nil { //nolint:gosec // G304: path constructed from trusted GT_ROOT env
			if daemonPID, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && daemonPID == pid {
				return pid, nil // daemon-managed server — adopt it
			}
		}
	}

	// Check if the process is using our data directory (CWD matches our dolt dir).
	// dolt sql-server is started with cmd.Dir = doltDir, so CWD is the data dir.
	doltDir := ResolveDoltDir(beadsDir)
	if isProcessInDir(pid, doltDir) {
		return pid, nil // our server — adopt it
	}

	// It's an orphan/stale dolt server on our port — kill it
	fmt.Fprintf(os.Stderr, "Killing orphan dolt server (PID %d) on port %d\n", pid, port)
	_ = gracefulStop(pid, 5*time.Second)

	if isPortAvailable(host, port) {
		return 0, nil
	}
	return 0, fmt.Errorf("failed to reclaim port %d from orphan dolt server (PID %d)", port, pid)
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

// DefaultConfig returns config with sensible defaults.
// Priority: env var > metadata.json > Gas Town fixed port > hash-derived port.
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

	// Check if user configured an explicit port
	if metaCfg, err := configfile.Load(beadsDir); err == nil && metaCfg != nil {
		if metaCfg.DoltServerPort > 0 {
			cfg.Port = metaCfg.DoltServerPort
		}
	}

	if cfg.Port == 0 {
		// Under Gas Town, use fixed port so all worktrees share one server.
		if os.Getenv("GT_ROOT") != "" {
			cfg.Port = GasTownPort
		} else {
			cfg.Port = DerivePort(beadsDir)
		}
	}

	return cfg
}

// IsRunning checks if a managed server is running for this beadsDir.
// Returns a State with Running=true if a valid dolt process is found.
// Under Gas Town (GT_ROOT set), checks the daemon PID file first since the
// daemon writes to $GT_ROOT/daemon/dolt.pid, not .beads/dolt-server.pid.
func IsRunning(beadsDir string) (*State, error) {
	// Under Gas Town, check daemon PID file first — the daemon manages
	// the server and writes its PID to a different location.
	if gtRoot := os.Getenv("GT_ROOT"); gtRoot != "" {
		daemonPidFile := filepath.Join(gtRoot, "daemon", "dolt.pid")
		if data, readErr := os.ReadFile(daemonPidFile); readErr == nil { //nolint:gosec // G304: path constructed from trusted GT_ROOT env
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid > 0 {
				if isProcessAlive(pid) && isDoltProcess(pid) {
					port := readPortFile(beadsDir)
					if port == 0 {
						port = GasTownPort
					}
					return &State{
						Running: true,
						PID:     pid,
						Port:    port,
						DataDir: ResolveDoltDir(beadsDir),
					}, nil
				}
			}
		}
	}

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
// Under Gas Town (GT_ROOT set), resolves to the canonical server directory
// so all worktrees share one server.
// Returns the port the server is listening on.
func EnsureRunning(beadsDir string) (int, error) {
	serverDir := resolveServerDir(beadsDir)

	state, err := IsRunning(serverDir)
	if err != nil {
		return 0, err
	}
	if state.Running {
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

	// Process census: refuse to start if too many dolt servers already running
	if count := countDoltProcesses(); count >= maxDoltServers() {
		return nil, fmt.Errorf("too many dolt sql-server processes running (%d, max %d).\n\nKill orphans with: bd dolt killall\nList processes: %s", count, maxDoltServers(), processListHint)
	}

	// Open log file
	logFile, err := os.OpenFile(logPath(beadsDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600) //nolint:gosec // G304: logPath derives from user-configured beadsDir
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	// Reclaim the canonical port. Kill orphan dolt servers on it; fail if
	// a non-dolt process holds it. Never silently fall back to another port.
	actualPort := cfg.Port
	adoptPID, reclaimErr := reclaimPort(cfg.Host, actualPort, beadsDir)
	if reclaimErr != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("cannot start dolt server on port %d: %w", actualPort, reclaimErr)
	}
	if adoptPID > 0 {
		// Existing server is ours (same data dir or daemon-managed) — adopt it
		_ = logFile.Close()
		_ = os.WriteFile(pidPath(beadsDir), []byte(strconv.Itoa(adoptPID)), 0600)
		_ = writePortFile(beadsDir, actualPort)
		touchActivity(beadsDir)
		if !IsDaemonManagedFor(beadsDir) {
			forkIdleMonitor(beadsDir)
		}
		return &State{Running: true, PID: adoptPID, Port: actualPort, DataDir: doltDir}, nil
	}

	// Start dolt sql-server
	cmd := exec.Command(doltBin, "sql-server",
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

	// Touch activity and fork idle monitor (skip under Gas Town where
	// the daemon manages server lifecycle)
	touchActivity(beadsDir)
	if !IsDaemonManagedFor(beadsDir) {
		forkIdleMonitor(beadsDir)
	}

	return &State{
		Running: true,
		PID:     pid,
		Port:    actualPort,
		DataDir: doltDir,
	}, nil
}

// IsDaemonManaged returns true if the dolt server is managed by the Gas Town
// daemon. Checks GT_ROOT first, then falls back to filesystem heuristics
// that detect Gas Town structure from the working directory.
// This handles cases where GT_ROOT is not set but the process is running
// inside a Gas Town workspace (crew sessions, residual tmux sessions, etc.).
func IsDaemonManaged() bool {
	return isDaemonManaged("")
}

// IsDaemonManagedFor is like IsDaemonManaged but also checks the beadsDir
// path for Gas Town indicators. Use this when beadsDir is available.
func IsDaemonManagedFor(beadsDir string) bool {
	return isDaemonManaged(beadsDir)
}

func isDaemonManaged(beadsDir string) bool {
	if os.Getenv("GT_ROOT") != "" {
		return true
	}
	return isGasTownContext(beadsDir)
}

// gasTownPathSegments are directory names distinctive to Gas Town rig worktrees.
// A standalone beads project would never have these in its path.
var gasTownPathSegments = []string{
	"crew",
	"polecats",
	"refinery",
	"witness",
	"deacon",
	"mayor",
}

// gasTownRootMarkers are subdirectory names that identify a Gas Town root
// or rig directory. Presence of 2+ of these as siblings is definitive.
var gasTownRootMarkers = []string{
	"daemon",
	"deacon",
	"warrants",
	"mayor",
	"crew",
	"refinery",
}

// isGasTownContext detects Gas Town workspace from the working directory
// and optionally from the beadsDir path.
func isGasTownContext(beadsDir string) bool {
	if wd, err := os.Getwd(); err == nil {
		if HasGasTownPathSegment(wd) {
			return true
		}
		if walkUpForGasTownRoot(wd) {
			return true
		}
	}
	if beadsDir != "" {
		if HasGasTownPathSegment(beadsDir) {
			return true
		}
		if walkUpForGasTownRoot(filepath.Dir(beadsDir)) {
			return true
		}
	}
	return false
}

// HasGasTownPathSegment reports whether path contains a directory component
// that is distinctive to Gas Town workspaces.
func HasGasTownPathSegment(path string) bool {
	// Split into directory components to avoid substring false positives
	// (e.g., "screwdriver" should not match "crew").
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		for _, seg := range gasTownPathSegments {
			if part == seg {
				return true
			}
		}
	}
	return false
}

// isGasTownRoot checks if dir is a Gas Town root by looking for 2+
// distinctive Gas Town subdirectories as siblings.
func isGasTownRoot(dir string) bool {
	count := 0
	for _, marker := range gasTownRootMarkers {
		info, err := os.Stat(filepath.Join(dir, marker))
		if err == nil && info.IsDir() {
			count++
			if count >= 2 {
				return true
			}
		}
	}
	return false
}

// walkUpForGasTownRoot walks up from dir checking each ancestor
// (including dir itself) for Gas Town root markers.
func walkUpForGasTownRoot(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for {
		if isGasTownRoot(abs) {
			return true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return false
		}
		abs = parent
	}
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
// Under Gas Town (GT_ROOT set), refuses to stop the daemon-managed server
// unless force is true.
func Stop(beadsDir string) error {
	return StopWithForce(beadsDir, false)
}

// StopWithForce is like Stop but allows overriding the Gas Town daemon guard.
func StopWithForce(beadsDir string, force bool) error {
	if !force && IsDaemonManagedFor(beadsDir) {
		return fmt.Errorf("Dolt server is managed by the Gas Town daemon.\nUse 'gt dolt stop' instead, or pass --force to override.")
	}

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
// not tracked by the canonical PID file. Under Gas Town, the canonical
// server is at $GT_ROOT/.beads/ or $GT_ROOT/daemon/dolt.pid (daemon-managed);
// in standalone mode, beadsDir is used.
//
// Under Gas Town, if no canonical PID can be identified from either location,
// this function refuses to kill anything to avoid accidentally killing the
// daemon-managed server.
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
	// Under Gas Town, also check the daemon-managed PID file
	if gtRoot := os.Getenv("GT_ROOT"); gtRoot != "" {
		daemonPidFile := filepath.Join(gtRoot, "daemon", "dolt.pid")
		if data, readErr := os.ReadFile(daemonPidFile); readErr == nil { //nolint:gosec // G304: path constructed from trusted GT_ROOT env
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid > 0 {
				canonicalPIDs[pid] = true
			}
		}
	}

	// Under Gas Town, if we can't identify any canonical server, refuse to
	// kill anything. Without knowing which process is canonical, we'd kill
	// all dolt servers including the daemon-managed one.
	if IsDaemonManagedFor(beadsDir) && len(canonicalPIDs) == 0 {
		return nil, fmt.Errorf("under Gas Town but no canonical PID file found\n\nThe Dolt server is likely managed by the gt daemon. Use 'gt dolt' commands instead.\nTo force kill all dolt servers: pkill -f 'dolt sql-server'")
	}

	var killed []int
	for _, pid := range allPIDs {
		if pid == os.Getpid() {
			continue
		}
		if canonicalPIDs[pid] {
			continue // preserve canonical/daemon-managed server
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
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
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

// ensureDoltInit initializes a dolt database directory if .dolt/ doesn't exist.
func ensureDoltInit(doltDir string) error {
	if err := os.MkdirAll(doltDir, 0750); err != nil {
		return fmt.Errorf("creating dolt directory: %w", err)
	}

	dotDolt := filepath.Join(doltDir, ".dolt")
	if _, err := os.Stat(dotDolt); err == nil {
		return nil // Already initialized
	}

	cmd := exec.Command("dolt", "init")
	cmd.Dir = doltDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dolt init: %w\n%s", err, out)
	}

	return nil
}

// --- Idle monitor ---

// DefaultIdleTimeout is the default duration before the idle monitor stops the server.
const DefaultIdleTimeout = 30 * time.Minute

// MonitorCheckInterval is how often the idle monitor checks activity.
const MonitorCheckInterval = 60 * time.Second

// forkIdleMonitor starts the idle monitor as a detached process.
// It runs `bd dolt idle-monitor --beads-dir=<dir>` in the background.
// Under Gas Town, the idle monitor is not forked — the daemon handles lifecycle.
func forkIdleMonitor(beadsDir string) {
	// Under Gas Town, the daemon manages server lifecycle (health checks,
	// restart on crash, etc.). Don't fork a beads idle monitor that could
	// interfere by stopping the shared server.
	if IsDaemonManagedFor(beadsDir) {
		return
	}

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
// for longer than the configured timeout. If the server crashed but
// activity is recent, it restarts it (watchdog behavior).
//
// idleTimeout of 0 means monitoring is disabled (exits immediately).
// Under Gas Town, exits immediately — the daemon handles server lifecycle.
func RunIdleMonitor(beadsDir string, idleTimeout time.Duration) {
	if idleTimeout == 0 {
		return
	}
	// Belt and suspenders: don't run under Gas Town even if somehow forked.
	if IsDaemonManagedFor(beadsDir) {
		return
	}

	for {
		time.Sleep(MonitorCheckInterval)

		state, err := IsRunning(beadsDir)
		if err != nil {
			continue
		}

		lastActivity := ReadActivityTime(beadsDir)
		idleDuration := time.Since(lastActivity)

		if state.Running {
			// Server is running — check if idle
			if !lastActivity.IsZero() && idleDuration > idleTimeout {
				// Idle too long — stop the server and exit
				_ = Stop(beadsDir)
				return
			}
		} else {
			// Server is NOT running — watchdog behavior
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
