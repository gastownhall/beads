package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// A client of the proxy only ever sees the proxy's own listener, which is up,
// so a plain close is all it learns when the external upstream behind it is
// not serving: the MySQL driver reports "invalid connection" / unexpected
// EOF, and the uow bootstrap ping classifies that as a transient drop and
// retries it for its whole 30s budget (#6003). For an upstream that is down
// that turns every bd command into a ~20-30s stall ending in an unhelpful
// message.
//
// Instead the proxy answers such a connection itself with a MySQL ERR packet
// in place of the server greeting, naming the upstream and what happened.
// The client (internal/storage/uow, via IsUpstreamOutageError) retries this
// error only for a short window (about a second), long enough to ride out an
// endpoint that is flapping or rebinding, short enough that a real outage
// fails within a couple of seconds instead of thirty. Two shapes qualify:
//
//   - The dial was refused, the unix socket path is gone, or the host or
//     network is unreachable. Nothing is accepting connections there right
//     now.
//   - The upstream accepted and then closed without sending a byte. A MySQL
//     server speaks first, so it did not serve this connection. That is NOT
//     proof it is down: Dolt does exactly this when max_connections and
//     back_log are full, when a queued connection times out, and while it
//     shuts down for a restart; an L4 front (socat, stunnel, a TCP load
//     balancer, kubectl port-forward) does it when its own target is gone.
//     The message says so, and the client's short retry window is what
//     covers the transient cases.
//
// A dial timeout, a reset, or a connection dropped after the greeting keeps
// the plain close, and with it the client's full transient retry.
//
// External backends only. A managed backend (local Dolt sidecar) is owned by
// the proxy, and its dial can be refused transiently while bd is still
// driving it, which the client's full bootstrap retry absorbs; those keep the
// plain close.
//
// The packet is not byte-for-byte what a real server sends before the
// handshake. MySQL omits the '#'+SQLSTATE marker there (no capabilities are
// negotiated yet); this packet keeps it because dolthub/vitess's client
// always expects it, while go-sql-driver accepts either form. The mysql CLI
// will therefore show "#HY000" at the start of the message text. 2003
// (CR_CONN_HOST_ERROR, "Can't connect to MySQL server") is a client-side
// code that no real server sends; it is chosen because it describes the
// condition, and the "beads db proxy:" prefix is what marks it as ours.

const (
	// UpstreamErrorNumber and UpstreamErrorPrefix identify the ERR packet
	// the proxy sends for an unreachable external upstream; see
	// IsUpstreamOutageError.
	UpstreamErrorNumber = 2003
	UpstreamErrorPrefix = "beads db proxy: "

	upstreamErrorSQLState     = "HY000"
	upstreamErrorMaxMessage   = 1024
	upstreamErrorWriteTimeout = time.Second
)

// IsUpstreamOutageError reports whether err is the proxy's own report that
// its external upstream is unreachable (see the comment above), as opposed to
// an error from the Dolt server itself.
func IsUpstreamOutageError(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == UpstreamErrorNumber &&
		strings.HasPrefix(me.Message, UpstreamErrorPrefix)
}

// reportsUpstreamOutage reports whether the proxy should answer an
// unreachable upstream with a MySQL error for this backend.
func reportsUpstreamOutage(s server.DatabaseServer) bool {
	_, external := s.(*server.ExternalDoltServer)
	return external
}

// isUpstreamUnreachableDialError reports whether a backend dial failure means
// nothing is accepting connections at the upstream right now, as opposed to
// slow (timeout) or a local resource problem.
func isUpstreamUnreachableDialError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH)
}

// dialFailureMessage describes a refused/unreachable dial as
// "dial <network> <address>: <reason>", without the Go call-site wrapping the
// error carries.
func dialFailureMessage(err error) string {
	var opErr *net.OpError
	var errno syscall.Errno
	if errors.As(err, &opErr) && opErr.Addr != nil && errors.As(err, &errno) {
		return fmt.Sprintf("upstream Dolt server unreachable: %s %s %s: %v", opErr.Op, opErr.Net, opErr.Addr, errno)
	}
	return "upstream Dolt server unreachable: " + err.Error()
}

// closedBeforeGreetingMessage describes a backend connection that reached EOF
// before the server greeting, naming the endpoint the proxy dialed.
func closedBeforeGreetingMessage(backend net.Conn) string {
	where := ""
	if ra := backend.RemoteAddr(); ra != nil && ra.String() != "" {
		where = fmt.Sprintf(" at %s %s", ra.Network(), ra)
	}
	return "upstream Dolt server" + where + " closed the connection before the MySQL greeting " +
		"(down, restarting, or at its connection limit)"
}

// upstreamErrorPacket encodes a MySQL ERR packet (sequence id 0, with the
// '#'+SQLSTATE marker) carrying msg, truncated on a UTF-8 boundary.
func upstreamErrorPacket(msg string) []byte {
	if len(msg) > upstreamErrorMaxMessage {
		cut := upstreamErrorMaxMessage
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	payload := make([]byte, 0, 9+len(msg))
	payload = append(payload, 0xff)
	payload = binary.LittleEndian.AppendUint16(payload, UpstreamErrorNumber)
	payload = append(payload, '#')
	payload = append(payload, upstreamErrorSQLState...)
	payload = append(payload, msg...)
	pkt := make([]byte, 4, 4+len(payload))
	pkt[0] = byte(len(payload))
	pkt[1] = byte(len(payload) >> 8)
	pkt[2] = byte(len(payload) >> 16)
	pkt[3] = 0 // sequence id: the server's first packet
	return append(pkt, payload...)
}

// UpstreamOutagePacket returns the ERR packet the proxy sends for an
// unreachable external upstream, with detail as the message after
// UpstreamErrorPrefix. Exported so client-side tests can play the proxy.
func UpstreamOutagePacket(detail string) []byte {
	return upstreamErrorPacket(UpstreamErrorPrefix + detail)
}

// writeUpstreamOutage sends the client the proxy's upstream-outage ERR
// packet. Best effort: the caller closes the connection either way.
func (p *proxyServer) writeUpstreamOutage(client net.Conn, detail string) {
	p.stats.IncUpstreamErrorReported()
	p.tracef("handleConn(%s) reporting upstream outage: %s", client.RemoteAddr(), detail)
	_ = client.SetWriteDeadline(time.Now().Add(upstreamErrorWriteTimeout))
	_, _ = client.Write(UpstreamOutagePacket(detail))
}
