package testutil

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// TestWaitForServerDrainsGreetingBeforeClose asserts that WaitForServer's
// readiness probe waits for and drains a MySQL server's greeting before it
// closes the connection, like doltserver.ProbeSQLServer does. A dial followed
// by a bare Close() hangs up on dolt before (or while) it writes its greeting,
// which dolt sees as an aborted handshake/RST (gastownhall/beads#4132, #5589).
func TestWaitForServerDrainsGreetingBeforeClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	greeting := []byte{0x08, 0x00, 0x00, 0x00, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a}
	result := make(chan string, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			result <- "accept: " + acceptErr.Error()
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		// Like a dolt still preparing its handshake: a draining probe keeps
		// the connection open (it waits up to 100ms for the greeting); a
		// slam-closing probe has already hung up.
		_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if _, readErr := conn.Read(buf); !errors.Is(readErr, os.ErrDeadlineExceeded) {
			result <- "probe hung up before the server sent its greeting (read: " + errString(readErr) + ")"
			return
		}
		if _, writeErr := conn.Write(greeting); writeErr != nil {
			result <- "write greeting: " + writeErr.Error()
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, readErr := conn.Read(buf); !errors.Is(readErr, io.EOF) {
			result <- "probe did not close cleanly after the greeting (read: " + errString(readErr) + ")"
			return
		}
		result <- ""
	}()

	if !WaitForServer(port, 5*time.Second) {
		t.Fatalf("WaitForServer(%d) = false; want true", port)
	}
	select {
	case msg := <-result:
		if msg != "" {
			t.Fatalf("WaitForServer readiness probe: %s", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server side never finished")
	}
}

// TestWaitForServerMutePeerIsReady keeps WaitForServer's contract of TCP
// acceptance: a listener that never greets still counts as ready.
func TestWaitForServerMutePeerIsReady(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Read(make([]byte, 1))
	}()

	if !WaitForServer(ln.Addr().(*net.TCPAddr).Port, 5*time.Second) {
		t.Fatal("WaitForServer = false for a listening mute peer; want true")
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
