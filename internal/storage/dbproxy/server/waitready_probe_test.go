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
	"github.com/stretchr/testify/require"
)

// greetingListener listens on a free loopback port and serves one connection
// the way a dolt still preparing its handshake does: it reads nothing for
// 50ms, then greets and waits for the probe to close. The returned channel
// carries "" when the probe stayed open for the greeting and then closed
// cleanly, otherwise what went wrong.
func greetingListener(t *testing.T) (int, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	result := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			result <- "accept: " + err.Error()
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if _, err := conn.Read(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
			result <- "probe hung up before the greeting (read: " + errText(err) + ")"
			return
		}
		if _, err := conn.Write([]byte{0x08, 0x00, 0x00, 0x00, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a}); err != nil {
			result <- "write greeting: " + err.Error()
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(buf); !errors.Is(err, io.EOF) {
			result <- "probe did not close cleanly after the greeting (read: " + errText(err) + ")"
			return
		}
		result <- ""
	}()
	return ln.Addr().(*net.TCPAddr).Port, result
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// TestDoltServerWaitReadyDrainsGreeting proves DoltServer.waitReady reads
// dolt's greeting before closing its probe connection (#5589), under both of
// its readiness conditions: the ready line (log_level info) and listener
// ownership (log_level warning).
func TestDoltServerWaitReadyDrainsGreeting(t *testing.T) {
	for _, tc := range []struct {
		name, level string
	}{
		{"ready line", "info"},
		{"owned listener", "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port, result := greetingListener(t)
			s, _ := newFakeDoltServerAtLevel(t, "", port, tc.level)
			s.egCtx = context.Background()
			s.pid = os.Getpid()
			owned := 0
			s.listenerOwner = func(int, int) doltserver.Ownership {
				owned++
				return doltserver.OwnershipOwned
			}
			watch := doltserver.NewStartupWatch(nil, port)
			if tc.level == "info" {
				_, _ = watch.Write([]byte(doltserver.DoltReadyLine + "\n"))
			}

			require.NoError(t, s.waitReady(context.Background(), watch))
			if tc.level == "warning" {
				require.Equal(t, 1, owned, "the quiet level must still prove listener ownership")
			}
			select {
			case msg := <-result:
				require.Empty(t, msg, "waitReady probe")
			case <-time.After(5 * time.Second):
				t.Fatal("listener never finished")
			}
		})
	}
}
