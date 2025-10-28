package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// Start starts the RPC server and listens for connections
func (s *Server) Start(_ context.Context) error {
	if err := s.ensureSocketDir(); err != nil {
		return fmt.Errorf("failed to ensure socket directory: %w", err)
	}

	if err := s.removeOldSocket(); err != nil {
		return fmt.Errorf("failed to remove old socket: %w", err)
	}

	listener, err := listenRPC(s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to initialize RPC listener: %w", err)
	}
	s.listener = listener

	// Set socket permissions to 0600 for security (owner only)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(s.socketPath, 0600); err != nil {
			_ = listener.Close()
			return fmt.Errorf("failed to set socket permissions: %w", err)
		}
	}

	// Store listener under lock
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	// Signal that server is ready to accept connections
	close(s.readyChan)

	go s.handleSignals()

	// Ensure cleanup is signaled when this function returns
	defer close(s.doneChan)

	// Accept connections using listener
	for {
		// Get listener under lock
		s.mu.RLock()
		listener := s.listener
		s.mu.RUnlock()

		conn, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			shutdown := s.shutdown
			s.mu.Unlock()
			if shutdown {
				return nil
			}
			return fmt.Errorf("failed to accept connection: %w", err)
		}

		// Try to acquire connection slot (non-blocking)
		select {
		case s.connSemaphore <- struct{}{}:
			// Acquired slot, handle connection
			s.metrics.RecordConnection()
			go func(c net.Conn) {
				defer func() { <-s.connSemaphore }() // Release slot
				atomic.AddInt32(&s.activeConns, 1)
				defer atomic.AddInt32(&s.activeConns, -1)
				s.handleConnection(c)
			}(conn)
		default:
			// Max connections reached, reject immediately
			s.metrics.RecordRejectedConnection()
			_ = conn.Close()
		}
	}
}

// WaitReady waits for the server to be ready to accept connections
func (s *Server) WaitReady() <-chan struct{} {
	return s.readyChan
}

// Stop stops the RPC server and cleans up resources
func (s *Server) Stop() error {
	var err error
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.shutdown = true
		s.mu.Unlock()

		// Signal cleanup goroutine to stop
		close(s.shutdownChan)

		// Close storage
		if s.storage != nil {
			if closeErr := s.storage.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close default storage: %v\n", closeErr)
			}
		}

		// Close listener under lock
		s.mu.Lock()
		listener := s.listener
		s.listener = nil
		s.mu.Unlock()

		if listener != nil {
			if closeErr := listener.Close(); closeErr != nil {
				err = fmt.Errorf("failed to close listener: %w", closeErr)
				return
			}
		}

		if removeErr := s.removeOldSocket(); removeErr != nil {
			err = fmt.Errorf("failed to remove socket: %w", removeErr)
		}
	})

	// Wait for Start() goroutine to finish cleanup (with timeout)
	select {
	case <-s.doneChan:
		// Cleanup completed
	case <-time.After(5 * time.Second):
		// Timeout waiting for cleanup - continue anyway
	}

	return err
}

func (s *Server) ensureSocketDir() error {
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Best-effort tighten permissions if directory already existed
	_ = os.Chmod(dir, 0700) // #nosec G302 - 0700 is secure (user-only access)
	return nil
}

func (s *Server) removeOldSocket() error {
	if _, err := os.Stat(s.socketPath); err == nil {
		// Socket exists - check if it's stale before removing
		// Try to connect to see if a daemon is actually using it
		conn, err := dialRPC(s.socketPath, 500*time.Millisecond)
		if err == nil {
			// Socket is active - another daemon is running
			_ = conn.Close()
			return fmt.Errorf("socket %s is in use by another daemon", s.socketPath)
		}

		// Socket is stale - safe to remove
		if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Server) handleSignals() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, serverSignals...)
	<-sigChan
	_ = s.Stop()
}

func (s *Server) handleConnection(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
		// Set read deadline for the next request
		if err := conn.SetReadDeadline(time.Now().Add(s.requestTimeout)); err != nil {
			return
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			resp := Response{
				Success: false,
				Error:   fmt.Sprintf("invalid request: %v", err),
			}
			s.writeResponse(writer, resp)
			continue
		}

		// Set write deadline for the response
		if err := conn.SetWriteDeadline(time.Now().Add(s.requestTimeout)); err != nil {
			return
		}

		resp := s.handleRequest(&req)
		s.writeResponse(writer, resp)
	}
}

func (s *Server) writeResponse(writer *bufio.Writer, resp Response) {
	data, _ := json.Marshal(resp)
	_, _ = writer.Write(data)
	_ = writer.WriteByte('\n')
	_ = writer.Flush()
}

func (s *Server) handleShutdown(_ *Request) Response {
	// Schedule shutdown in a goroutine so we can return a response first
	go func() {
		time.Sleep(100 * time.Millisecond) // Give time for response to be sent
		if err := s.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "Error during shutdown: %v\n", err)
		}
	}()

	return Response{
		Success: true,
		Data:    json.RawMessage(`{"message":"Daemon shutting down"}`),
	}
}
