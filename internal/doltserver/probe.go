package doltserver

import (
	"net"
	"time"
)

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
// Returns whether the first read observed any greeting bytes before the
// connection was closed.
func DrainAndCloseProbe(conn net.Conn) bool {
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	greeted := err == nil && n > 0
	if !greeted {
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
