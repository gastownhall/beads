package doltserver

import (
	"net"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// DrainAndCloseProbe drains the MySQL handshake greeting (if any) from conn
// before closing it, then closes the connection. It forwards to
// doltutil.DrainAndCloseProbe, which documents why and is importable by
// packages (such as internal/testutil) that cannot import doltserver.
//
// Returns whether the first read observed any greeting bytes before the
// connection was closed.
func DrainAndCloseProbe(conn net.Conn) bool {
	return doltutil.DrainAndCloseProbe(conn)
}

// ProbeSQLServer dials network/addr with the given timeout and, on success,
// drains and closes the connection via DrainAndCloseProbe. It forwards to
// doltutil.ProbeSQLServer.
//
// err != nil means the address was unreachable within timeout. When err is
// nil, greeted reports whether a MySQL handshake greeting was observed
// before the probe connection closed.
func ProbeSQLServer(network, addr string, timeout time.Duration) (greeted bool, err error) {
	return doltutil.ProbeSQLServer(network, addr, timeout)
}
