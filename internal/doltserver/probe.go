package doltserver

import (
	"net"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// DrainAndCloseProbe drains the MySQL handshake greeting (if any) from conn
// before closing it, then closes the connection, so the close is a clean
// FIN instead of the RST that aborts dolt's MySQL handshake. See
// doltutil.DrainAndCloseProbe (and gastownhall/beads#4132, #4133) for why
// draining matters.
//
// This is the compatibility forwarder for existing callers and tests; the
// implementation moved to doltutil (a leaf package both testutil and the
// dbproxy packages can import) so every probe site shares one drain.
//
// Returns whether the first read observed any greeting bytes before the
// connection was closed.
func DrainAndCloseProbe(conn net.Conn) bool {
	return doltutil.DrainAndCloseProbe(conn)
}

// ProbeSQLServer dials network/addr with the given timeout and, on success,
// drains and closes the connection via DrainAndCloseProbe.
//
// err != nil means the address was unreachable within timeout. When err is
// nil, greeted reports whether a MySQL handshake greeting was observed
// before the probe connection closed — a dial-succeeded-but-mute server
// (TCP accepting, MySQL engine not yet writing) reports greeted == false.
func ProbeSQLServer(network, addr string, timeout time.Duration) (greeted bool, err error) {
	return doltutil.ProbeSQLServer(network, addr, timeout)
}
