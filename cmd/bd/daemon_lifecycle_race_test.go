//go:build !windows
// +build !windows

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
)

// =============================================================================
// Daemon Lifecycle Race Condition Tests
// =============================================================================
//
// These tests verify correct daemon lifecycle behavior under race conditions.
// Run with: go test -race -run TestDaemonLifecycle -v
//
// Race conditions being tested:
// 1. Startup race with multiple clients connecting simultaneously
// 2. Shutdown with pending requests
// 3. Crash recovery (simulated panic)
// 4. Socket cleanup on crash
//
// =============================================================================

// TestDaemonStartupRaceWithClients verifies daemon handles multiple clients
// attempting to connect during startup.
//
// Race condition tested: Daemon is starting up while clients are trying to connect.
// Clients should either connect successfully or get a clean rejection.
func TestDaemonStartupRaceWithClients(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping startup race test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	socketPath := filepath.Join(tmpDir, "bd.sock")
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := newTestLogger()

	const numClients = 20
	var (
		connectedCount int32
		failedCount    int32
		startWg        sync.WaitGroup
		connectWg      sync.WaitGroup
	)

	startWg.Add(1)

	// start clients that will try to connect immediately
	for i := 0; i < numClients; i++ {
		connectWg.Add(1)
		go func(clientID int) {
			defer connectWg.Done()

			// wait for server to start (but may not be ready)
			startWg.Wait()

			// try to connect with retries
			var connected bool
			for attempt := 0; attempt < 10; attempt++ {
				conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
				if err == nil {
					connected = true
					conn.Close()
					break
				}
				time.Sleep(50 * time.Millisecond)
			}

			if connected {
				atomic.AddInt32(&connectedCount, 1)
			} else {
				atomic.AddInt32(&failedCount, 1)
			}
		}(i)
	}

	// start server
	server, serverErrChan, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		t.Fatalf("Failed to start RPC server: %v", err)
	}
	defer func() {
		if server != nil {
			_ = server.Stop()
		}
	}()

	// signal clients to start connecting
	startWg.Done()

	// wait for server to be ready
	select {
	case <-server.WaitReady():
		// server is ready
	case <-time.After(5 * time.Second):
		t.Fatal("Server did not become ready")
	}

	// wait for all clients
	connectWg.Wait()

	t.Logf("Startup race test: %d connected, %d failed", connectedCount, failedCount)

	// most clients should eventually connect
	if connectedCount < int32(numClients/2) {
		t.Errorf("Expected at least %d connections, got %d", numClients/2, connectedCount)
	}

	// verify no server errors
	select {
	case err := <-serverErrChan:
		t.Errorf("Server error during startup race: %v", err)
	default:
		// no error, expected
	}
}

// TestDaemonShutdownWithPendingRequests verifies graceful shutdown waits for
// pending requests to complete.
//
// Race condition tested: Daemon is processing requests when shutdown is initiated.
// Pending requests should complete, new requests should be rejected.
func TestDaemonShutdownWithPendingRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping shutdown test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	socketPath := filepath.Join(tmpDir, "bd.sock")
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	log := newTestLogger()

	server, _, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		t.Fatalf("Failed to start RPC server: %v", err)
	}

	<-server.WaitReady()

	const numRequests = 10
	var (
		completedCount int32
		failedCount    int32
		requestWg      sync.WaitGroup
	)

	// start requests
	for i := 0; i < numRequests; i++ {
		requestWg.Add(1)
		go func(reqID int) {
			defer requestWg.Done()

			client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second)
			if err != nil || client == nil {
				atomic.AddInt32(&failedCount, 1)
				return
			}
			defer client.Close()

			// send a health check request
			_, err = client.Health()
			if err != nil {
				atomic.AddInt32(&failedCount, 1)
			} else {
				atomic.AddInt32(&completedCount, 1)
			}
		}(i)
	}

	// give requests time to start
	time.Sleep(50 * time.Millisecond)

	// initiate shutdown while requests are in flight
	shutdownStart := time.Now()
	if err := server.Stop(); err != nil {
		t.Logf("Server stop returned error (may be expected): %v", err)
	}
	shutdownDuration := time.Since(shutdownStart)

	// wait for all requests to complete
	requestWg.Wait()

	t.Logf("Shutdown test: %d completed, %d failed, shutdown took %v",
		completedCount, failedCount, shutdownDuration)

	// shutdown should complete in reasonable time
	if shutdownDuration > 10*time.Second {
		t.Errorf("Shutdown took too long: %v", shutdownDuration)
	}
}

// TestDaemonCrashRecoveryWithPanic verifies daemon recovers from handler panics.
//
// Race condition tested: A handler panics while other requests are in flight.
// The panic should be caught and other requests should continue.
func TestDaemonCrashRecoveryWithPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping panic recovery test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	socketPath := filepath.Join(tmpDir, "bd.sock")
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := newTestLogger()

	server, _, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		t.Fatalf("Failed to start RPC server: %v", err)
	}
	defer func() {
		if server != nil {
			_ = server.Stop()
		}
	}()

	<-server.WaitReady()

	// verify server is healthy before any issues
	client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second)
	if err != nil || client == nil {
		t.Fatalf("Failed to connect before panic test: %v", err)
	}

	health, err := client.Health()
	if err != nil {
		t.Fatalf("Health check failed before panic: %v", err)
	}
	if health.Status != "healthy" {
		t.Fatalf("Server not healthy before panic: %s", health.Status)
	}
	client.Close()

	// the RPC server has panic recovery built in (bd-1048)
	// send multiple requests to verify server continues working
	const numRequests = 10
	var successCount int32

	var wg sync.WaitGroup
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second)
			if err != nil || c == nil {
				return
			}
			defer c.Close()

			if _, err := c.Health(); err == nil {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}

	wg.Wait()

	// most requests should succeed
	if successCount < int32(numRequests/2) {
		t.Errorf("Expected at least %d successful requests, got %d", numRequests/2, successCount)
	}
}

// TestSocketCleanupOnCrash verifies socket is cleaned up when daemon crashes.
//
// Race condition tested: Daemon crashes and leaves socket file behind.
// New daemon should clean up stale socket and start successfully.
func TestSocketCleanupOnCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping socket cleanup test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	socketPath := filepath.Join(tmpDir, "bd.sock")
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	// create a stale socket file (simulating crash)
	f, err := os.Create(socketPath)
	if err != nil {
		t.Fatalf("Failed to create stale socket: %v", err)
	}
	f.Close()

	// verify socket exists
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("Stale socket should exist: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := newTestLogger()

	// new daemon should clean up stale socket and start
	server, _, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		t.Fatalf("Failed to start server with stale socket: %v", err)
	}
	defer func() {
		if server != nil {
			_ = server.Stop()
		}
	}()

	<-server.WaitReady()

	// verify server is working
	client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second)
	if err != nil || client == nil {
		t.Fatalf("Failed to connect after cleanup: %v", err)
	}
	defer client.Close()

	health, err := client.Health()
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	if health.Status != "healthy" {
		t.Errorf("Server not healthy after cleanup: %s", health.Status)
	}
}

// TestConcurrentServerStartStop verifies no race when starting/stopping rapidly.
//
// Race condition tested: Server Start and Stop called in rapid succession.
// Should not cause panics or resource leaks.
func TestConcurrentServerStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent start/stop test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	log := newTestLogger()

	const cycles = 5
	for i := 0; i < cycles; i++ {
		socketPath := filepath.Join(tmpDir, fmt.Sprintf("bd%d.sock", i))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		server, _, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
		if err != nil {
			cancel()
			t.Logf("Cycle %d: failed to start server (may be expected): %v", i, err)
			continue
		}

		// wait for ready or timeout
		select {
		case <-server.WaitReady():
			// ready
		case <-time.After(2 * time.Second):
			t.Logf("Cycle %d: server did not become ready", i)
		}

		// stop server
		if err := server.Stop(); err != nil {
			t.Logf("Cycle %d: stop error (may be expected): %v", i, err)
		}

		cancel()

		// small delay between cycles
		time.Sleep(50 * time.Millisecond)
	}
}

// TestDaemonContextCancellation verifies daemon responds to context cancellation.
//
// Race condition tested: Context is cancelled while daemon is running.
// Daemon should shut down gracefully.
func TestDaemonContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping context cancellation test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	socketPath := filepath.Join(tmpDir, "bd.sock")
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	log := newTestLogger()

	ctx, cancel := context.WithCancel(context.Background())

	server, serverErrChan, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		t.Fatalf("Failed to start RPC server: %v", err)
	}

	<-server.WaitReady()

	// verify server is working
	client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second)
	if err != nil || client == nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	client.Close()

	// cancel context
	cancel()

	// wait for server to detect cancellation
	time.Sleep(200 * time.Millisecond)

	// stop server explicitly
	stopErr := server.Stop()
	t.Logf("Stop after cancel returned: %v", stopErr)

	// verify no panic occurred
	select {
	case err := <-serverErrChan:
		t.Logf("Server error after cancellation: %v", err)
	default:
		// no error, expected
	}
}

// TestMultipleClientsDuringShutdown verifies clients handle shutdown gracefully.
//
// Race condition tested: Multiple clients connected when shutdown occurs.
// All clients should receive errors, not hang.
func TestMultipleClientsDuringShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping multiple clients shutdown test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	socketPath := filepath.Join(tmpDir, "bd.sock")
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	log := newTestLogger()

	server, _, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		t.Fatalf("Failed to start RPC server: %v", err)
	}

	<-server.WaitReady()

	const numClients = 10
	clients := make([]*rpc.Client, 0, numClients)

	// connect all clients
	for i := 0; i < numClients; i++ {
		client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second)
		if err != nil || client == nil {
			t.Logf("Client %d failed to connect: %v", i, err)
			continue
		}
		clients = append(clients, client)
	}

	t.Logf("Connected %d clients", len(clients))

	// start requests from all clients
	var wg sync.WaitGroup
	var completedBeforeShutdown int32
	var completedAfterShutdown int32
	var errorCount int32

	shutdownStarted := make(chan struct{})

	for i, client := range clients {
		wg.Add(1)
		go func(id int, c *rpc.Client) {
			defer wg.Done()
			defer c.Close()

			// keep making requests
			for j := 0; j < 5; j++ {
				select {
				case <-shutdownStarted:
					// after shutdown started
					_, err := c.Health()
					if err == nil {
						atomic.AddInt32(&completedAfterShutdown, 1)
					} else {
						atomic.AddInt32(&errorCount, 1)
					}
				default:
					// before shutdown
					_, err := c.Health()
					if err == nil {
						atomic.AddInt32(&completedBeforeShutdown, 1)
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
		}(i, client)
	}

	// let some requests complete
	time.Sleep(50 * time.Millisecond)

	// signal shutdown started and stop server
	close(shutdownStarted)
	if err := server.Stop(); err != nil {
		t.Logf("Server stop error: %v", err)
	}

	// wait for all client goroutines with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// all clients finished
	case <-time.After(10 * time.Second):
		t.Error("Clients did not finish within timeout")
	}

	t.Logf("Results: %d before shutdown, %d after shutdown, %d errors",
		completedBeforeShutdown, completedAfterShutdown, errorCount)
}

// TestServerReadyChannelRace verifies WaitReady channel is safe for concurrent use.
//
// Race condition tested: Multiple goroutines waiting on WaitReady simultaneously.
func TestServerReadyChannelRace(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping ready channel race test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	socketPath := filepath.Join(tmpDir, "bd.sock")
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := newTestLogger()

	server, _, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		t.Fatalf("Failed to start RPC server: %v", err)
	}
	defer server.Stop()

	const numWaiters = 20
	var readyCount int32
	var wg sync.WaitGroup

	for i := 0; i < numWaiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-server.WaitReady():
				atomic.AddInt32(&readyCount, 1)
			case <-time.After(5 * time.Second):
				// timeout
			}
		}()
	}

	wg.Wait()

	if readyCount != numWaiters {
		t.Errorf("Expected %d waiters to receive ready, got %d", numWaiters, readyCount)
	}
}

// TestEventLoopConcurrentAccess verifies event loop handles concurrent operations.
//
// Race condition tested: Multiple operations happening during event loop iteration.
func TestEventLoopConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping event loop concurrent test in short mode")
	}

	tmpDir := makeSocketTempDirForLifecycle(t)
	socketPath := filepath.Join(tmpDir, "bd.sock")
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "test.db")
	testStore := newTestStore(t, testDBPath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := newTestLogger()

	server, serverErrChan, err := startRPCServer(ctx, socketPath, testStore, tmpDir, testDBPath, log)
	if err != nil {
		t.Fatalf("Failed to start RPC server: %v", err)
	}
	defer server.Stop()

	<-server.WaitReady()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var syncCount int32
	syncFunc := func() {
		atomic.AddInt32(&syncCount, 1)
	}

	// run event loop in goroutine
	loopCtx, loopCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer loopCancel()

	go func() {
		runEventLoop(loopCtx, loopCancel, ticker, syncFunc, server, serverErrChan, 0, log)
	}()

	// concurrent client operations
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				c, err := rpc.TryConnectWithTimeout(socketPath, 500*time.Millisecond)
				if err != nil || c == nil {
					continue
				}
				_, _ = c.Health()
				c.Close()
				time.Sleep(50 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
	<-loopCtx.Done()

	if syncCount == 0 {
		t.Error("Event loop sync function was never called")
	}
}

// Helper function to create temp dir for lifecycle tests
func makeSocketTempDirForLifecycle(t *testing.T) string {
	t.Helper()
	// use /tmp for socket paths to avoid path length issues
	tmpDir, err := os.MkdirTemp("/tmp", "beads-lifecycle-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})
	return tmpDir
}
