package server

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitreadyGreeting is a minimal, well-formed-enough MySQL handshake packet
// (4-byte header: 3-byte length little-endian + 1-byte sequence, followed by
// payload bytes), simulating the handshake a dolt sql-server writes the
// moment a connection is accepted.
var waitreadyGreeting = []byte{0x08, 0x00, 0x00, 0x00, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a}

// waitReadyTestServer builds a DoltServer for the waitReady probe tests
// without starting a child: the greeting comes from the test's own listener,
// ownership is decided by the injected hook, and the errgroup context stands
// for a live child.
func waitReadyTestServer(t *testing.T, port int) *DoltServer {
	t.Helper()
	s, _ := newFakeDoltServerAtLevel(t, "greet", port, "warning")
	s.egCtx = context.Background()
	s.pid = os.Getpid()
	s.listenerOwner = func(int, int) doltserver.Ownership { return doltserver.OwnershipOwned }
	return s
}

// TestDoltServerWaitReadyDrainsGreeting pins the #5589 fix for the managed
// backend readiness probe: when waitReady's dial succeeds, it must consume
// the backend's MySQL greeting before closing its probe connection, so the
// close is a FIN and not the RST that aborts dolt's handshake (#4132/#4133)
// — without weakening the ready-line/ownership condition that makes the
// dial count.
func TestDoltServerWaitReadyDrainsGreeting(t *testing.T) {
	t.Run("drains the greeting before closing", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		port := ln.Addr().(*net.TCPAddr).Port

		result := make(chan string, 1)
		go func() {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				result <- "accept: " + acceptErr.Error()
				return
			}
			defer func() { _ = conn.Close() }()
			buf := make([]byte, 64)
			// Like a dolt still preparing its handshake: a draining probe
			// keeps the connection open (it waits up to 100ms for the
			// greeting); a slam-closing probe has already hung up.
			_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			if _, readErr := conn.Read(buf); readErr == nil || !errors.Is(readErr, os.ErrDeadlineExceeded) {
				result <- "probe hung up before the server sent its greeting (read: " + errString(readErr) + ")"
				return
			}
			if _, writeErr := conn.Write(waitreadyGreeting); writeErr != nil {
				result <- "write greeting: " + writeErr.Error()
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, readErr := conn.Read(buf); readErr != nil && !errors.Is(readErr, io.EOF) {
				result <- "probe did not close cleanly after the greeting (read: " + readErr.Error() + ")"
				return
			}
			result <- ""
		}()

		s := waitReadyTestServer(t, port)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.waitReady(ctx, doltserver.NewStartupWatch(nil, port)); err != nil {
			t.Fatalf("waitReady: %v", err)
		}
		select {
		case msg := <-result:
			if msg != "" {
				t.Fatalf("greeting peer: %s", msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("greeting peer never finished")
		}
	})

	t.Run("mute peer is still ready", func(t *testing.T) {
		// Under a quiet log level the ownership condition (not a greeting)
		// makes the dial count, so a listener that accepts but never greets
		// is ready; the drain only adds its bounded wait.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		port := ln.Addr().(*net.TCPAddr).Port

		muteDone := make(chan struct{})
		go func() {
			defer close(muteDone)
			for {
				conn, acceptErr := ln.Accept()
				if acceptErr != nil {
					return
				}
				go func(c net.Conn) {
					timer := time.NewTimer(2 * time.Second)
					defer timer.Stop()
					<-timer.C
					_ = c.Close()
				}(conn)
			}
		}()

		s := waitReadyTestServer(t, port)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, s.waitReady(ctx, doltserver.NewStartupWatch(nil, port)))
	})

	t.Run("refused connection is not ready", func(t *testing.T) {
		// A port nothing listens on: the dial keeps failing and waitReady
		// reports not-ready after the (shortened) startup timeout.
		old := startReadyTimeout
		startReadyTimeout = 300 * time.Millisecond
		t.Cleanup(func() { startReadyTimeout = old })

		port := freeTestPort(t)
		s := waitReadyTestServer(t, port)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := s.waitReady(ctx, doltserver.NewStartupWatch(nil, port))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "listener not ready")
	})
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
