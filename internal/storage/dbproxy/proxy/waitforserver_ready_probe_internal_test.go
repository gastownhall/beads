package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// fakeMySQLGreeting is a minimal, well-formed-enough MySQL handshake packet
// (4-byte header: 3-byte length little-endian + 1-byte sequence, followed by
// payload bytes), as the doltserver probe tests use, simulating the
// handshake a dolt sql-server writes the moment a connection is accepted.
var fakeMySQLGreeting = []byte{0x08, 0x00, 0x00, 0x00, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a}

// TestWaitForServerReadyDrainsBackendGreeting pins the #5589 fix for the
// proxy's backend readiness probe: waitForServerReady must consume the
// backend's MySQL greeting before closing its probe connection, so the close
// is a FIN and not the RST that aborts dolt's handshake (#4132/#4133). The
// TestDatabaseServerImpl pipe is synchronous — a greeting write only
// completes once the probe reads it — so a slam-closing probe fails the
// backend's write, while a draining probe lets it complete and observe a
// clean EOF.
func TestWaitForServerReadyDrainsBackendGreeting(t *testing.T) {
	t.Parallel()

	t.Run("drains the greeting before closing", func(t *testing.T) {
		t.Parallel()

		result := make(chan error, 1)
		ts := server.New()
		ts.Handler = func(backend net.Conn) {
			defer func() { _ = backend.Close() }()
			if _, err := backend.Write(fakeMySQLGreeting); err != nil {
				result <- fmt.Errorf("backend greeting write (probe closed without draining): %w", err)
				return
			}
			buf := make([]byte, 1)
			if _, err := backend.Read(buf); err != nil && !errors.Is(err, io.EOF) {
				result <- fmt.Errorf("backend read after greeting: %w", err)
				return
			}
			result <- nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ts.Start(ctx); err != nil {
			t.Fatalf("start backend: %v", err)
		}
		if err := waitForServerReady(ctx, ts, 2*time.Second); err != nil {
			t.Fatalf("waitForServerReady: %v", err)
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("backend peer: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("backend peer never observed the probe close")
		}
	})

	t.Run("re-checks Running after the dial", func(t *testing.T) {
		t.Parallel()

		// A backend that exits the moment it answers the dial must not be
		// declared ready: the post-dial Running re-check (which must happen
		// after the drain, not instead of it) turns an answered dial into an
		// error, so readiness never rides on the dial alone.
		ts := server.New()
		var dialed atomic.Bool
		backend := &flipAfterDialServer{TestDatabaseServerImpl: ts, dialed: &dialed}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := waitForServerReady(ctx, backend, 250*time.Millisecond)
		if err == nil {
			t.Fatal("waitForServerReady = nil for a backend that exited during the readiness check; the post-dial liveness re-check is missing")
		}
		if !dialed.Load() {
			t.Fatal("the backend was never dialed; the subtest exercised nothing")
		}
	})
}

// flipAfterDialServer reports Running until its first Dial, then not: it
// stands for a backend that exits the moment it answers the readiness probe.
type flipAfterDialServer struct {
	*server.TestDatabaseServerImpl
	dialed *atomic.Bool
}

func (s *flipAfterDialServer) Dial(ctx context.Context) (net.Conn, error) {
	s.dialed.Store(true)
	return s.TestDatabaseServerImpl.Dial(ctx)
}

func (s *flipAfterDialServer) Running(context.Context) bool {
	return !s.dialed.Load()
}
