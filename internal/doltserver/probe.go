package doltserver

import (
	"context"
	"net"
	"time"
)

// loopbackGreetingWindow is how long DrainAndCloseProbe waits for the
// greeting. A server on this host writes it almost as soon as it accepts.
const loopbackGreetingWindow = 100 * time.Millisecond

// DrainAndCloseProbe drains the MySQL handshake greeting (if any) from conn
// before closing it, then closes the connection.
//
// A bare Close() on a connection that hasn't read the server's handshake
// packet causes the OS to send a TCP RST instead of a clean FIN. Dolt's
// sql-server interprets that RST as an aborted MySQL handshake; enough of
// them in a short window (e.g. from repeated readiness/circuit-breaker
// probes) can crash the dolt sql-server process. Reading the greeting first
// — even partially — lets the TCP stack close cleanly instead. See
// gastownhall/beads#4132 and #4133.
//
// The greeting wait is sized for a loopback server; probe a server that may
// be remote with DrainAndCloseProbeContext.
//
// Returns whether the first read observed any greeting bytes before the
// connection was closed.
func DrainAndCloseProbe(conn net.Conn) bool {
	return drainAndCloseProbe(context.Background(), conn, time.Now().Add(loopbackGreetingWindow))
}

// DrainAndCloseProbeContext is DrainAndCloseProbe for a server that may be
// remote, whose greeting trails the dial by a network round trip that can
// outlast the loopback window and make a healthy server look mute. The
// greeting wait runs until ctx's deadline instead, or the loopback window
// when ctx has none, and ends early if ctx is canceled.
func DrainAndCloseProbeContext(ctx context.Context, conn net.Conn) bool {
	greetBy, ok := ctx.Deadline()
	if !ok {
		greetBy = time.Now().Add(loopbackGreetingWindow)
	}
	return drainAndCloseProbe(ctx, conn, greetBy)
}

func drainAndCloseProbe(ctx context.Context, conn net.Conn, greetBy time.Time) bool {
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(greetBy)
	// Register after setting the deadline so a cancellation, even one that
	// already happened, lands last and cuts the wait short.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	buf := make([]byte, 1024)
	// io.Reader may return data together with an error (a peer that greets
	// and closes can yield io.EOF alongside the bytes); the bytes still count.
	if n, _ := conn.Read(buf); n == 0 {
		return false
	}

	// Drain any remaining bytes so the FIN close doesn't race a still-writing peer.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	for {
		if _, e := conn.Read(buf); e != nil {
			break
		}
	}
	return true
}

// ProbeSQLServer dials network/addr with the given timeout and, on success,
// drains and closes the connection via DrainAndCloseProbe.
//
// err != nil means the address was unreachable within timeout. When err is
// nil, greeted reports whether a MySQL handshake greeting was observed
// before the probe connection closed — a dial-succeeded-but-mute server
// (TCP accepting, MySQL engine not yet writing) reports greeted == false.
func ProbeSQLServer(network, addr string, timeout time.Duration) (greeted bool, err error) {
	conn, dialErr := net.DialTimeout(network, addr, timeout)
	if dialErr != nil {
		return false, dialErr
	}
	return DrainAndCloseProbe(conn), nil
}
