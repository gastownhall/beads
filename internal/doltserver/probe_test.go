package doltserver

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMySQLGreeting is a minimal, well-formed-enough MySQL handshake packet
// (4-byte header: 3-byte length little-endian + 1-byte sequence, followed by
// payload bytes) used by these tests to simulate a dolt sql-server greeting.
var fakeMySQLGreeting = []byte{0x08, 0x00, 0x00, 0x00, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a}

// TestWaitForReadyEmitsRST proves waitForReady connects exactly once against
// a server that greets immediately, and that the probe connection is drained
// before Close() (so the TCP stack sends FIN, not RST). See
// gastownhall/beads#4132, #4133.
func TestWaitForReadyEmitsRST(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	var connects atomic.Int32
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			connects.Add(1)
			go func(c net.Conn) {
				_, _ = c.Write(fakeMySQLGreeting)
				time.Sleep(20 * time.Millisecond)
				_ = c.Close()
			}(conn)
		}
	}()

	time.Sleep(50 * time.Millisecond) // let goroutine bind

	if err := waitForReady("127.0.0.1", port, 5*time.Second); err != nil {
		t.Fatalf("waitForReady: %v", err)
	}

	time.Sleep(100 * time.Millisecond) // let the accept goroutine finish counting

	got := connects.Load()
	if got != 1 {
		t.Errorf("waitForReady made %d TCP connections (expected 1)", got)
	}
	t.Logf("waitForReady made %d TCP connections (drained, no RST)", got)
}

// TestWaitForReadyRepollsUntilGreeted is the F7 regression test: a listener
// that accepts TCP connections but never writes anything is a "TCP-accepting
// but mute" server — not ready. waitForReady must not treat dial success
// alone as readiness; it must keep polling until a greeting arrives or the
// deadline passes.
func TestWaitForReadyRepollsUntilGreeted(t *testing.T) {
	t.Run("mute listener: keeps polling, returns timeout error", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		port := ln.Addr().(*net.TCPAddr).Port

		var accepts atomic.Int32
		go func() {
			for {
				conn, acceptErr := ln.Accept()
				if acceptErr != nil {
					return
				}
				accepts.Add(1)
				// Accept but never write and never close promptly — mute peer.
				go func(c net.Conn) {
					time.Sleep(1 * time.Second)
					_ = c.Close()
				}(conn)
			}
		}()

		err = waitForReady("127.0.0.1", port, 700*time.Millisecond)
		if err == nil {
			t.Fatalf("waitForReady returned nil for a mute (accepting, never-greeting) server; want a timeout error")
		}
		if got := accepts.Load(); got < 2 {
			t.Errorf("waitForReady accepted only %d connection(s) before giving up; want re-polling (>=2) since dial-success-without-greeting must not be treated as ready", got)
		}
	})

	t.Run("delayed greeting: succeeds once the greeting arrives", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		port := ln.Addr().(*net.TCPAddr).Port

		var callCount atomic.Int32
		go func() {
			for {
				conn, acceptErr := ln.Accept()
				if acceptErr != nil {
					return
				}
				n := callCount.Add(1)
				go func(c net.Conn, attempt int32) {
					defer c.Close()
					if attempt < 2 {
						// First accept(s): mute, so the probe times out its read
						// and waitForReady must re-poll instead of declaring victory.
						time.Sleep(600 * time.Millisecond)
						return
					}
					_, _ = c.Write(fakeMySQLGreeting)
					time.Sleep(20 * time.Millisecond)
				}(conn, n)
			}
		}()

		if err := waitForReady("127.0.0.1", port, 5*time.Second); err != nil {
			t.Fatalf("waitForReady: %v", err)
		}
		if got := callCount.Load(); got < 2 {
			t.Errorf("expected waitForReady to re-poll past the mute accept, got only %d accept(s)", got)
		}
	})
}

// TestDrainAndCloseProbe unit-tests DrainAndCloseProbe directly against a
// greeting peer and a mute peer.
func TestDrainAndCloseProbe(t *testing.T) {
	t.Run("greeting peer returns true", func(t *testing.T) {
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
			_, _ = conn.Write(fakeMySQLGreeting)
			time.Sleep(20 * time.Millisecond)
			_ = conn.Close()
		}()

		conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}

		start := time.Now()
		greeted := DrainAndCloseProbe(conn)
		elapsed := time.Since(start)

		if !greeted {
			t.Errorf("DrainAndCloseProbe returned false for a peer that sent a greeting")
		}
		if elapsed > 150*time.Millisecond {
			t.Errorf("DrainAndCloseProbe took %v against a greeting peer; want well under 150ms", elapsed)
		}
	})

	t.Run("mute peer returns false within ~150ms", func(t *testing.T) {
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
			// Mute: never write, hold the connection open past the probe's
			// own read-deadline window so the timeout path is exercised.
			time.Sleep(500 * time.Millisecond)
			_ = conn.Close()
		}()

		conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}

		start := time.Now()
		greeted := DrainAndCloseProbe(conn)
		elapsed := time.Since(start)

		if greeted {
			t.Errorf("DrainAndCloseProbe returned true for a mute peer")
		}
		if elapsed > 150*time.Millisecond {
			t.Errorf("DrainAndCloseProbe took %v against a mute peer; want within ~150ms (100ms read deadline)", elapsed)
		}
	})
}

// TestProbeSQLServer covers the dial-failure path (unreachable address) and
// the success path (greeting observed) at the ProbeSQLServer level.
func TestProbeSQLServer(t *testing.T) {
	t.Run("unreachable address returns non-nil err", func(t *testing.T) {
		// Port 0 dial-time resolves to "invalid port" on most platforms;
		// use an ephemeral port we bind then immediately release, which is
		// racy to be listening on but reliably closed for our purposes.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		greeted, err := ProbeSQLServer("tcp", addr, 200*time.Millisecond)
		if err == nil {
			t.Fatalf("expected dial error against a closed port, got greeted=%v, err=nil", greeted)
		}
		if greeted {
			t.Errorf("expected greeted=false alongside a dial error")
		}
	})

	t.Run("greeting peer returns greeted=true, err=nil", func(t *testing.T) {
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
			_, _ = conn.Write(fakeMySQLGreeting)
			time.Sleep(20 * time.Millisecond)
			_ = conn.Close()
		}()

		greeted, err := ProbeSQLServer("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatalf("ProbeSQLServer: %v", err)
		}
		if !greeted {
			t.Errorf("expected greeted=true")
		}
	})
}
