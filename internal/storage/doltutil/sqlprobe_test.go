package doltutil

import (
	"net"
	"testing"
	"time"
)

// fakeMySQLGreeting is a minimal, well-formed-enough MySQL handshake packet
// (4-byte header: 3-byte length little-endian + 1-byte sequence, followed by
// payload bytes) used by these tests to simulate a dolt sql-server greeting.
var fakeMySQLGreeting = []byte{0x08, 0x00, 0x00, 0x00, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a}

// TestDrainAndCloseProbe covers the shared drain implementation directly:
// the greeting peer's bytes are consumed before the close (so the close is a
// clean FIN, not the RST that aborts dolt's handshake — gastownhall/beads
// #4132, #4133), and a mute peer returns false within the bounded drain
// window. internal/doltserver's probe tests cover the same behavior through
// its compatibility forwarders.
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
// the success path (greeting observed) of the shared probe helper.
func TestProbeSQLServer(t *testing.T) {
	t.Run("unreachable address returns non-nil err", func(t *testing.T) {
		// Use an ephemeral port we bind then immediately release, which is
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
