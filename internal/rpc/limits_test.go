package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func dialTestConn(t *testing.T, socketPath string) net.Conn {
	conn, err := dialRPC(socketPath, time.Second)
	if err != nil {
		t.Fatalf("failed to dial %s: %v", socketPath, err)
	}
	return conn
}

func TestConnectionLimits(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	socketPath := newTestSocketPath(t)

	// Set low connection limit for testing
	os.Setenv("BEADS_DAEMON_MAX_CONNS", "5")
	defer os.Unsetenv("BEADS_DAEMON_MAX_CONNS")

	srv := NewServer(socketPath, store, tmpDir, dbPath)
	if srv.maxConns != 5 {
		t.Fatalf("expected maxConns=5, got %d", srv.maxConns)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			t.Logf("server error: %v", err)
		}
	}()

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)
	defer srv.Stop()

	// Open maxConns connections and hold them
	var wg sync.WaitGroup
	connections := make([]net.Conn, srv.maxConns)

	for i := 0; i < srv.maxConns; i++ {
		conn := dialTestConn(t, socketPath)
		connections[i] = conn

		// Send a long-running ping to keep connection busy
		wg.Add(1)
		go func(c net.Conn, _ int) {
			defer wg.Done()
			req := Request{
				Operation: OpPing,
			}
			data, _ := json.Marshal(req)
			c.Write(append(data, '\n'))

			// Read response
			reader := bufio.NewReader(c)
			_, _ = reader.ReadBytes('\n')
		}(conn, i)
	}

	// Wait for all connections to be active
	time.Sleep(200 * time.Millisecond)

	// Verify active connection count
	activeConns := atomic.LoadInt32(&srv.activeConns)
	if activeConns != int32(srv.maxConns) {
		t.Errorf("expected %d active connections, got %d", srv.maxConns, activeConns)
	}

	// Try to open one more connection - should be rejected
	extraConn := dialTestConn(t, socketPath)
	defer extraConn.Close()

	// Send request on extra connection
	req := Request{Operation: OpPing}
	data, _ := json.Marshal(req)
	extraConn.Write(append(data, '\n'))

	// Set short read timeout to detect rejection
	extraConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	reader := bufio.NewReader(extraConn)
	_, err = reader.ReadBytes('\n')

	// Connection should be closed (EOF or timeout)
	if err == nil {
		t.Error("expected extra connection to be rejected, but got response")
	}

	// Close existing connections
	for _, conn := range connections {
		conn.Close()
	}
	wg.Wait()

	// Wait for connection cleanup
	time.Sleep(100 * time.Millisecond)

	// Now should be able to connect again
	newConn := dialTestConn(t, socketPath)
	defer newConn.Close()

	req = Request{Operation: OpPing}
	data, _ = json.Marshal(req)
	newConn.Write(append(data, '\n'))

	reader = bufio.NewReader(newConn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !resp.Success {
		t.Error("expected successful ping after connection cleanup")
	}
}

func TestRequestTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	socketPath := newTestSocketPath(t)

	// Set very short timeout for testing
	os.Setenv("BEADS_DAEMON_REQUEST_TIMEOUT", "100ms")
	defer os.Unsetenv("BEADS_DAEMON_REQUEST_TIMEOUT")

	srv := NewServer(socketPath, store, tmpDir, dbPath)
	if srv.requestTimeout != 100*time.Millisecond {
		t.Fatalf("expected timeout=100ms, got %v", srv.requestTimeout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			t.Logf("server error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	defer srv.Stop()

	conn := dialTestConn(t, socketPath)
	defer conn.Close()

	// Send partial request and wait for timeout
	conn.Write([]byte(`{"operation":"ping"`)) // Incomplete JSON

	// Wait longer than timeout
	time.Sleep(200 * time.Millisecond)

	// Attempt to read - connection should have been closed or timed out
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Error("expected connection to be closed due to timeout")
	}
}

func TestHealthResponseIncludesLimits(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	os.Setenv("BEADS_DAEMON_MAX_CONNS", "50")
	defer os.Unsetenv("BEADS_DAEMON_MAX_CONNS")

	srv := NewServer(socketPath, store, tmpDir, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			t.Logf("server error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	defer srv.Stop()

	conn := dialTestConn(t, socketPath)
	defer conn.Close()

	req := Request{Operation: OpHealth}
	data, _ := json.Marshal(req)
	conn.Write(append(data, '\n'))

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !resp.Success {
		t.Fatalf("health check failed: %s", resp.Error)
	}

	var health HealthResponse
	if err := json.Unmarshal(resp.Data, &health); err != nil {
		t.Fatalf("failed to unmarshal health response: %v", err)
	}

	// Verify limit fields are present
	if health.MaxConns != 50 {
		t.Errorf("expected MaxConns=50, got %d", health.MaxConns)
	}

	if health.ActiveConns < 0 {
		t.Errorf("expected ActiveConns>=0, got %d", health.ActiveConns)
	}

	// No need to check MemoryAllocMB < 0 since it's uint64

	t.Logf("Health: %d/%d connections, %d MB memory", health.ActiveConns, health.MaxConns, health.MemoryAllocMB)
}
