//go:build cgo

package dolt

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Watchdog constants
const (
	watchdogCheckInterval   = 10 * time.Second
	watchdogQueryTimeout    = 2 * time.Second
	watchdogMaxRestarts     = 3
	watchdogBackoffInterval = 60 * time.Second
)

// watchdogState tracks health transitions for logging
type watchdogState struct {
	healthy      bool
	restartCount int
	lastFailure  time.Time
	backingOff   bool
}

// startWatchdog begins background health monitoring for server mode.
// It periodically checks server health and attempts restart on failure.
func (s *DoltStore) startWatchdog(cfg *Config) {
	if !s.serverMode || cfg.DisableWatchdog {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.watchdogCancel = cancel
	s.watchdogDone = make(chan struct{})

	go s.watchdogLoop(ctx, cfg)
}

// watchdogLoop is the main health check loop.
func (s *DoltStore) watchdogLoop(ctx context.Context, cfg *Config) {
	defer close(s.watchdogDone)

	state := &watchdogState{healthy: true}
	ticker := time.NewTicker(watchdogCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.watchdogCheck(ctx, cfg, state)
		}
	}
}

// watchdogCheck performs a single health check cycle.
func (s *DoltStore) watchdogCheck(ctx context.Context, cfg *Config, state *watchdogState) {
	// If backing off after repeated failures, use longer interval
	if state.backingOff {
		if time.Since(state.lastFailure) < watchdogBackoffInterval {
			return
		}
		// Reset backoff — try again
		state.backingOff = false
		state.restartCount = 0
	}

	healthy := s.isServerHealthy(cfg)

	// Log health transitions
	if healthy && !state.healthy {
		fmt.Fprintf(os.Stderr, "watchdog: server recovered (healthy)\n")
		state.healthy = true
		state.restartCount = 0
	} else if !healthy && state.healthy {
		fmt.Fprintf(os.Stderr, "watchdog: server unhealthy, attempting recovery\n")
		state.healthy = false
	}

	if healthy {
		return
	}

	// Server is unhealthy — attempt restart
	state.restartCount++
	state.lastFailure = time.Now()

	if state.restartCount > watchdogMaxRestarts {
		fmt.Fprintf(os.Stderr, "watchdog: max restart attempts (%d) exceeded, backing off to %v checks\n",
			watchdogMaxRestarts, watchdogBackoffInterval)
		state.backingOff = true
		return
	}

	fmt.Fprintf(os.Stderr, "watchdog: restart attempt %d/%d\n", state.restartCount, watchdogMaxRestarts)

	if err := s.attemptServerRestart(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: restart failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "watchdog: server restarted successfully\n")
		state.healthy = true
		state.restartCount = 0
	}
}

// isServerHealthy checks both TCP connectivity and query responsiveness.
func (s *DoltStore) isServerHealthy(cfg *Config) bool {
	host := cfg.ServerHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.ServerPort
	if port == 0 {
		port = DefaultSQLPort
	}

	// TCP probe (fast, catches process death)
	if !isServerListening(host, port) {
		return false
	}

	// Query probe (catches hung server)
	queryCtx, cancel := context.WithTimeout(context.Background(), watchdogQueryTimeout)
	defer cancel()

	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()

	if db == nil {
		return false
	}

	var result int
	err := db.QueryRowContext(queryCtx, "SELECT 1").Scan(&result)
	return err == nil && result == 1
}

// attemptServerRestart tries to restart the Dolt server and reconnect.
func (s *DoltStore) attemptServerRestart(ctx context.Context, cfg *Config) error {
	// Clean up stale PID file if the process is dead
	cleanStalePID(cfg.Path)

	// Create a new server instance and start it
	serverCfg := ServerConfig{
		DataDir: cfg.Path,
		SQLPort: cfg.ServerPort,
		Host:    cfg.ServerHost,
	}
	if serverCfg.SQLPort == 0 {
		serverCfg.SQLPort = DefaultSQLPort
	}
	if serverCfg.Host == "" {
		serverCfg.Host = "127.0.0.1"
	}

	server := NewServer(serverCfg)
	if err := server.Start(ctx); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	// Reconnect the connection pool
	if err := s.reconnect(cfg); err != nil {
		return fmt.Errorf("server started but reconnect failed: %w", err)
	}

	return nil
}

// reconnect creates a new database connection after server restart.
func (s *DoltStore) reconnect(cfg *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Close existing connection (may already be dead)
	if s.db != nil {
		_ = s.db.Close()
	}

	db, connStr, err := openServerConnection(context.Background(), cfg)
	if err != nil {
		return err
	}

	// Test the new connection
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return fmt.Errorf("ping failed after reconnect: %w", err)
	}

	s.db = db
	s.connStr = connStr
	return nil
}

// cleanStalePID removes the PID file if the server process is dead.
func cleanStalePID(dataDir string) {
	pidFile := dataDir + "/dolt-server.pid"
	data, err := os.ReadFile(pidFile) // #nosec G304 -- pidFile is derived from internal dataDir
	if err != nil {
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = os.Remove(pidFile)
		return
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(pidFile)
		return
	}

	if !processMayBeAlive(process) {
		_ = os.Remove(pidFile)
	}
}

// stopWatchdog cancels the watchdog goroutine and waits for it to finish.
func (s *DoltStore) stopWatchdog() {
	if s.watchdogCancel != nil {
		s.watchdogCancel()
	}
	if s.watchdogDone != nil {
		// Wait with timeout to avoid hanging on Close()
		select {
		case <-s.watchdogDone:
		case <-time.After(5 * time.Second):
		}
	}
}
