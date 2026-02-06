//go:build cgo
// Package dolt implements the storage interface using Dolt (versioned MySQL-compatible database).
//
// This file implements the dolt sql-server management for federation mode.
// When federation is enabled, we run dolt sql-server instead of the embedded driver
// to enable multi-writer support and expose the remotesapi for peer-to-peer sync.
package dolt

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultSQLPort is the default port for dolt sql-server MySQL protocol
	DefaultSQLPort = 3306
	// DefaultRemotesAPIPort is the default port for dolt remotesapi (peer-to-peer sync)
	DefaultRemotesAPIPort = 8080
	// ServerStartTimeout is how long to wait for server to start
	ServerStartTimeout = 30 * time.Second
	// ServerStopTimeout is how long to wait for graceful shutdown
	ServerStopTimeout = 10 * time.Second
)

// ServerConfig holds configuration for the dolt sql-server
type ServerConfig struct {
	DataDir        string // Path to Dolt database directory
	SQLPort        int    // MySQL protocol port (default: 3306)
	RemotesAPIPort int    // remotesapi port for peer sync (default: 8080)
	Host           string // Host to bind to (default: 127.0.0.1)
	LogFile        string // Log file for server output (optional)
	User           string // MySQL user (default: root)
	ReadOnly       bool   // Start in read-only mode
}

// Server manages a dolt sql-server process
type Server struct {
	cfg     ServerConfig
	cmd     *exec.Cmd
	mu      sync.Mutex
	running bool
	pidFile string
	logFile *os.File // Track log file for cleanup
}

// NewServer creates a new dolt sql-server manager
func NewServer(cfg ServerConfig) *Server {
	if cfg.SQLPort == 0 {
		cfg.SQLPort = DefaultSQLPort
	}
	if cfg.RemotesAPIPort == 0 {
		cfg.RemotesAPIPort = DefaultRemotesAPIPort
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	return &Server{
		cfg:     cfg,
		pidFile: filepath.Join(cfg.DataDir, "dolt-server.pid"),
	}
}

// Start starts the dolt sql-server process
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server already running")
	}

	// Check if ports are available
	if err := s.checkPortAvailable(s.cfg.SQLPort); err != nil {
		return fmt.Errorf("SQL port %d not available: %w", s.cfg.SQLPort, err)
	}
	if err := s.checkPortAvailable(s.cfg.RemotesAPIPort); err != nil {
		return fmt.Errorf("remotesapi port %d not available: %w", s.cfg.RemotesAPIPort, err)
	}

	// Build command args
	// Note: --user was removed in recent dolt versions, users are created with CREATE USER
	args := []string{
		"sql-server",
		"--host", s.cfg.Host,
		"--port", strconv.Itoa(s.cfg.SQLPort),
		"--remotesapi-port", strconv.Itoa(s.cfg.RemotesAPIPort),
		"--no-auto-commit", // Let the application manage commits
	}

	if s.cfg.ReadOnly {
		args = append(args, "--readonly")
	}

	// Create command
	// #nosec G204 -- dolt binary is fixed; args are derived from internal config.
	s.cmd = exec.CommandContext(ctx, "dolt", args...)
	s.cmd.Dir = s.cfg.DataDir

	// Set up process group for clean shutdown (Unix-only; no-op on Windows).
	setDoltServerSysProcAttr(s.cmd)

	// Set up logging
	if s.cfg.LogFile != "" {
		logFile, err := os.OpenFile(s.cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		s.logFile = logFile // Track for cleanup on Stop()
		s.cmd.Stdout = logFile
		s.cmd.Stderr = logFile
	} else {
		// Discard output if no log file specified
		s.cmd.Stdout = nil
		s.cmd.Stderr = nil
	}

	// Start the server
	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start dolt sql-server: %w", err)
	}

	// Write PID file
	if err := os.WriteFile(s.pidFile, []byte(strconv.Itoa(s.cmd.Process.Pid)), 0600); err != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to write dolt server PID file: %v\n", err)
	}

	// Wait for server to be ready
	if err := s.waitForReady(ctx); err != nil {
		// Server failed to start, clean up
		_ = s.cmd.Process.Kill()
		_ = os.Remove(s.pidFile)
		if s.logFile != nil {
			_ = s.logFile.Close()
			s.logFile = nil
		}
		return fmt.Errorf("server failed to become ready: %w", err)
	}

	s.running = true
	return nil
}

// Stop stops the dolt sql-server process gracefully
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}

	// Best-effort graceful shutdown (platform-specific).
	_ = terminateProcess(s.cmd.Process)

	// Wait for graceful shutdown with timeout
	done := make(chan error, 1)
	go func() {
		_, err := s.cmd.Process.Wait()
		done <- err
	}()

	select {
	case <-done:
		// Process exited
	case <-time.After(ServerStopTimeout):
		// Force kill
		_ = s.cmd.Process.Kill()
		<-done // Wait for process to be reaped
	}

	// Clean up PID file and log file
	_ = os.Remove(s.pidFile)
	if s.logFile != nil {
		_ = s.logFile.Close()
		s.logFile = nil
	}
	s.running = false
	s.cmd = nil

	return nil
}

// IsRunning returns true if the server is running
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// SQLPort returns the SQL port
func (s *Server) SQLPort() int {
	return s.cfg.SQLPort
}

// RemotesAPIPort returns the remotesapi port
func (s *Server) RemotesAPIPort() int {
	return s.cfg.RemotesAPIPort
}

// Host returns the host
func (s *Server) Host() string {
	return s.cfg.Host
}

// DSN returns the MySQL DSN for connecting to the server
func (s *Server) DSN(database string) string {
	return fmt.Sprintf("%s@tcp(%s:%d)/%s",
		s.cfg.User, s.cfg.Host, s.cfg.SQLPort, database)
}

// checkPortAvailable checks if a TCP port is available
func (s *Server) checkPortAvailable(port int) error {
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	_ = listener.Close()
	return nil
}

// waitForReady waits for the server to accept connections
func (s *Server) waitForReady(ctx context.Context) error {
	deadline := time.Now().Add(ServerStartTimeout)
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.SQLPort))

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Best-effort: if we can tell the process is dead, fail fast.
		if s.cmd.Process != nil && !processMayBeAlive(s.cmd.Process) {
			return fmt.Errorf("server process exited unexpectedly")
		}

		// Try to connect
		conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for server to start on %s", addr)
}

// GetRunningServerPID returns the PID of a running server from the PID file, or 0 if not running
func GetRunningServerPID(dataDir string) int {
	pidFile := filepath.Join(dataDir, "dolt-server.pid")
	// #nosec G304 -- pidFile is derived from internal dataDir.
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}

	// Check if process is actually running
	process, err := os.FindProcess(pid)
	if err != nil {
		return 0
	}

	// Best-effort liveness check (platform-specific).
	if !processMayBeAlive(process) {
		_ = os.Remove(pidFile)
		return 0
	}

	return pid
}

// StopServerByPID stops a dolt sql-server by its PID
func StopServerByPID(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	// Best-effort graceful shutdown (platform-specific).
	_ = terminateProcess(process)

	// Wait for graceful shutdown
	done := make(chan struct{})
	go func() {
		_, _ = process.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(ServerStopTimeout):
		// Force kill
		return process.Kill()
	}
}

// DetectRunningServer checks if a dolt sql-server is running on common ports.
// Returns (host, port, true) if a server is detected, or ("", 0, false) if not.
// Tries port 3307 first (Gas Town default), then 3306 (Dolt default).
func DetectRunningServer() (string, int, bool) {
	host := "127.0.0.1"
	// Try Gas Town default port first, then standard MySQL port
	for _, port := range []int{3307, 3306} {
		if isServerListening(host, port) {
			return host, port, true
		}
	}
	return "", 0, false
}

// isServerListening checks if a server is accepting connections on the given host:port.
func isServerListening(host string, port int) bool {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}


