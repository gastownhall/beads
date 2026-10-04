package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// A client of the proxy only ever sees the proxy's own listener, which is up,
// so a plain close is all it learns when the backend behind it is gone: the
// MySQL driver reports "invalid connection" / unexpected EOF, and the uow
// bootstrap ping classifies that as a transient drop and retries it for its
// whole 30s budget (#6003). For an external upstream that is down — refused,
// socket path gone, or a front (stunnel, socat, load balancer) that accepts
// and immediately closes — that turns every bd command into a ~20-30s stall
// that ends in an unhelpful message.
//
// Instead the proxy answers such a connection itself with a MySQL ERR packet
// in place of the server greeting, the same way a real server refuses a
// connection before the handshake (1040 Too many connections, 1129 host
// blocked). The driver surfaces it as a *mysql.MySQLError, which the
// bootstrap retry treats as permanent, so the command fails at once and says
// which upstream is unreachable. Only shapes that prove the upstream is not
// serving qualify; a dial timeout or a connection dropped after the greeting
// keeps the plain close, and with it the client's transient retry.
//
// External backends only. A managed backend (local Dolt sidecar) is owned by
// the proxy, and its dial can be refused transiently while bd is still
// driving it — a proxied `bd init` was observed hitting exactly that, which
// the client's bootstrap retry absorbs. There a refusal is not evidence of
// an outage the user must act on, so those backends keep the plain close.

const (
	// upstreamUnreachableErrno is CR_CONN_HOST_ERROR ("Can't connect to
	// MySQL server"), the code a MySQL client itself reports for this
	// condition; nothing in beads classifies it as retryable.
	upstreamUnreachableErrno    = 2003
	upstreamUnreachableSQLState = "HY000"
	upstreamErrorMaxMessage     = 1024
	upstreamErrorWriteTimeout   = time.Second
)

// reportsUpstreamOutage reports whether the proxy should answer an
// unreachable upstream with a MySQL error for this backend.
func reportsUpstreamOutage(s server.DatabaseServer) bool {
	_, external := s.(*server.ExternalDoltServer)
	return external
}

// isUpstreamUnreachableDialError reports whether a backend dial failure proves
// the upstream is not serving at all, as opposed to slow (timeout) or a local
// resource problem.
func isUpstreamUnreachableDialError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH)
}

// upstreamErrorPacket encodes a MySQL ERR packet (sequence id 0, protocol-41
// SQL state marker) carrying msg.
func upstreamErrorPacket(msg string) []byte {
	if len(msg) > upstreamErrorMaxMessage {
		msg = msg[:upstreamErrorMaxMessage]
	}
	payload := make([]byte, 0, 9+len(msg))
	payload = append(payload, 0xff)
	payload = binary.LittleEndian.AppendUint16(payload, upstreamUnreachableErrno)
	payload = append(payload, '#')
	payload = append(payload, upstreamUnreachableSQLState...)
	payload = append(payload, msg...)
	pkt := make([]byte, 4, 4+len(payload))
	pkt[0] = byte(len(payload))
	pkt[1] = byte(len(payload) >> 8)
	pkt[2] = byte(len(payload) >> 16)
	pkt[3] = 0 // sequence id: the server's first packet
	return append(pkt, payload...)
}

// writeUpstreamUnreachable sends the client an ERR packet explaining that the
// upstream behind this proxy is unreachable. Best effort: the caller closes
// the connection either way.
func writeUpstreamUnreachable(client net.Conn, cause string) error {
	_ = client.SetWriteDeadline(time.Now().Add(upstreamErrorWriteTimeout))
	_, err := client.Write(upstreamErrorPacket(fmt.Sprintf("beads db proxy: upstream Dolt server unreachable: %s", cause)))
	return err
}

// backendClosedBeforeGreeting describes a backend connection that reached EOF
// before the server greeting, naming the endpoint the proxy dialed.
func backendClosedBeforeGreeting(backend net.Conn) string {
	if ra := backend.RemoteAddr(); ra != nil && ra.String() != "" {
		return fmt.Sprintf("%s %s closed the connection before sending the MySQL greeting", ra.Network(), ra)
	}
	return "it closed the connection before sending the MySQL greeting"
}
