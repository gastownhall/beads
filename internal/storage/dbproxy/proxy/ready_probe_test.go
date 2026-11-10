package proxy

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// greetingBackend is a backend whose every Dial returns one end of a
// net.Pipe whose other end writes a MySQL greeting and then waits for the
// probe to close. A pipe write completes only once the other side reads it,
// so the peer's result says whether the probe consumed the greeting before
// closing. Running reports the backend as up for the first runningCalls calls
// to Running, or always when runningCalls is negative.
type greetingBackend struct {
	runningCalls int32
	calls        atomic.Int32
	peers        chan error
}

func (*greetingBackend) ID(context.Context) string                          { return "greeting" }
func (*greetingBackend) DSN(context.Context, string, string, string) string { return "" }
func (*greetingBackend) Start(context.Context) error                        { return nil }
func (*greetingBackend) Stop(context.Context) error                         { return nil }

func (b *greetingBackend) Running(context.Context) bool {
	n := b.calls.Add(1)
	return b.runningCalls < 0 || n <= b.runningCalls
}

func (b *greetingBackend) Dial(context.Context) (net.Conn, error) {
	probe, peer := net.Pipe()
	go func() {
		defer peer.Close()
		_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := peer.Write([]byte{0x08, 0x00, 0x00, 0x00, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a}); err != nil {
			b.peers <- err
			return
		}
		_, err := peer.Read(make([]byte, 1))
		if err == io.EOF {
			err = nil
		}
		b.peers <- err
	}()
	return probe, nil
}

// TestWaitForServerReadyDrainsBackendGreeting proves the proxy's startup
// readiness probe reads the backend's greeting before closing the probe
// connection (#5589), and still re-checks that the backend runs after it.
func TestWaitForServerReadyDrainsBackendGreeting(t *testing.T) {
	t.Run("running backend", func(t *testing.T) {
		b := &greetingBackend{runningCalls: -1, peers: make(chan error, 16)}
		if err := waitForServerReady(context.Background(), b, 5*time.Second); err != nil {
			t.Fatalf("waitForServerReady: %v", err)
		}
		select {
		case err := <-b.peers:
			if err != nil {
				t.Fatalf("backend greeting not consumed before close: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("backend peer never finished")
		}
		if got := b.calls.Load(); got != 2 {
			t.Errorf("Running called %d times; want 2 (before and after the probe)", got)
		}
	})

	t.Run("backend exits during the probe", func(t *testing.T) {
		b := &greetingBackend{runningCalls: 1, peers: make(chan error, 16)}
		if err := waitForServerReady(context.Background(), b, 300*time.Millisecond); err == nil {
			t.Fatal("waitForServerReady = nil for a backend that exited after the dial; want an error")
		}
		select {
		case err := <-b.peers:
			if err != nil {
				t.Fatalf("backend greeting not consumed before close: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("backend peer never finished")
		}
	})
}
