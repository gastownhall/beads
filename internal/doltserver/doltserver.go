// Package doltserver manages the lifecycle of a local dolt sql-server process
// for standalone beads users. It provides transparent auto-start so that
// `bd init` and `bd <command>` work without manual server management.
//
// Each beads project gets its own dolt server on a deterministic port derived
// from the project path (hash → range 13307–14307). Users with explicit port
// config in metadata.json always use that port instead.
//
// Server state files (PID, log, lock) live in the .beads/ directory.
package doltserver

import (
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
)

// Port range for auto-derived ports.
const (
	portRangeBase = 13307
	portRangeSize = 1000
)

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
func pidPath(beadsDir string) string  { return filepath.Join(beadsDir, "dolt-server.pid") }
func logPath(beadsDir string) string  { return filepath.Join(beadsDir, "dolt-server.log") }
func lockPath(beadsDir string) string { return filepath.Join(beadsDir, "dolt-server.lock") }

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

// DefaultConfig returns config with sensible defaults.
// Checks metadata.json for an explicit port first, falls back to DerivePort.
func DefaultConfig(beadsDir string) *Config {
	cfg := &Config{
		BeadsDir: beadsDir,
		Host:     "127.0.0.1",
	}

	// Check if user configured an explicit port
	if metaCfg, err := configfile.Load(beadsDir); err == nil && metaCfg != nil {
		if metaCfg.DoltServerPort > 0 {
			cfg.Port = metaCfg.DoltServerPort
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
	process, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(pidPath(beadsDir))
		return &State{Running: false}, nil
	}

	if err := process.Signal(syscall.Signal(0)); err != nil {
		// Process is dead — stale PID file
		_ = os.Remove(pidPath(beadsDir))
		return &State{Running: false}, nil
	}

	// Verify it's actually a dolt sql-server process
	if !isDoltProcess(pid) {
		// PID was reused by another process
		_ = os.Remove(pidPath(beadsDir))
		return &State{Running: false}, nil
	}

	cfg := DefaultConfig(beadsDir)
	return &State{
		Running: true,
		PID:     pid,
		Port:    cfg.Port,
		DataDir: filepath.Join(beadsDir, "dolt"),
	}, nil
}

// EnsureRunning starts the server if it is not already running.
// This is the main auto-start entry point. Thread-safe via file lock.
// Returns the port the server is listening on.
func EnsureRunning(beadsDir string) (int, error) {
	state, err := IsRunning(beadsDir)
	if err != nil {
		return 0, err
	}
	if state.Running {
		return state.Port, nil
	}

	s, err := Start(beadsDir)
	if err != nil {
		return 0, err
	}
	return s.Port, nil
}

// Start explicitly starts a dolt sql-server for the project.
// Returns the State of the started server, or an error.
func Start(beadsDir string) (*State, error) {
	cfg := DefaultConfig(beadsDir)
	doltDir := filepath.Join(beadsDir, "dolt")

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
	logFile, err := os.OpenFile(logPath(beadsDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	// Start dolt sql-server
	cmd := exec.Command(doltBin, "sql-server",
		"-H", cfg.Host,
		"-P", strconv.Itoa(cfg.Port),
	)
	cmd.Dir = doltDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// New process group so server survives bd exit
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("starting dolt sql-server: %w", err)
	}
	logFile.Close()

	pid := cmd.Process.Pid

	// Write PID file
	if err := os.WriteFile(pidPath(beadsDir), []byte(strconv.Itoa(pid)), 0600); err != nil {
		// Best effort — kill the server if we can't track it
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("writing PID file: %w", err)
	}

	// Release the process handle so it outlives us
	_ = cmd.Process.Release()

	// Wait for server to accept connections
	if err := waitForReady(cfg.Host, cfg.Port, 10*time.Second); err != nil {
		// Server started but not responding — clean up
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
		_ = os.Remove(pidPath(beadsDir))
		return nil, fmt.Errorf("server started (PID %d) but not accepting connections on port %d: %w\nCheck logs: %s",
			pid, cfg.Port, err, logPath(beadsDir))
	}

	return &State{
		Running: true,
		PID:     pid,
		Port:    cfg.Port,
		DataDir: doltDir,
	}, nil
}

// Stop gracefully stops the managed server.
// Sends SIGTERM, waits up to 5 seconds, then SIGKILL.
func Stop(beadsDir string) error {
	state, err := IsRunning(beadsDir)
	if err != nil {
		return err
	}
	if !state.Running {
		return fmt.Errorf("Dolt server is not running")
	}

	process, err := os.FindProcess(state.PID)
	if err != nil {
		_ = os.Remove(pidPath(beadsDir))
		return fmt.Errorf("finding process %d: %w", state.PID, err)
	}

	// Send SIGTERM for graceful shutdown
	if err := process.Signal(syscall.SIGTERM); err != nil {
		_ = os.Remove(pidPath(beadsDir))
		return fmt.Errorf("sending SIGTERM to PID %d: %w", state.PID, err)
	}

	// Wait for graceful shutdown (up to 5 seconds)
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if err := process.Signal(syscall.Signal(0)); err != nil {
			// Process has exited
			_ = os.Remove(pidPath(beadsDir))
			return nil
		}
	}

	// Still running — force kill
	_ = process.Signal(syscall.SIGKILL)
	time.Sleep(100 * time.Millisecond)
	_ = os.Remove(pidPath(beadsDir))

	return nil
}

// LogPath returns the path to the server log file.
func LogPath(beadsDir string) string {
	return logPath(beadsDir)
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

// isDoltProcess verifies that a PID belongs to a dolt sql-server process.
func isDoltProcess(pid int) bool {
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	cmdline := strings.TrimSpace(string(output))
	return strings.Contains(cmdline, "dolt") && strings.Contains(cmdline, "sql-server")
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
