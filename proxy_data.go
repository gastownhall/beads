package beads

import (
	"context"
	"net"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// ErrProxyDataAuth is wrapped by the error DialProxyData returns when the
// listener at the data port did not complete bd's proxy handshake under the
// proxy root's secret: it hung up on the hello, as a bd proxy does on a token
// from another secret, or it answered with a proof that does not verify. The
// listener has not shown it is the proxy that root names: the root may belong
// to another workspace, the proxy may have restarted and rotated its secret
// (a new dial reads the new one), or another process may now hold the port.
// A server-mode open through a proxy (BEADS_DOLT_SERVER_PROXY_ROOT) fails
// with it for the same reasons. Test for it with errors.Is.
var ErrProxyDataAuth = proxy.ErrDataAuth

// DialProxyData connects to the data port of a bd db-proxy at host and port
// and completes the proxy's BDPROXY1 handshake, which the data port requires
// before any MySQL byte. proxyRoot is the proxy's root directory, the one
// holding its proxy.secret; bd reads the secret there on every call, so the
// caller never handles it. The connection is returned only once the proxy
// has proven it holds that secret, ready for the MySQL session (the server
// speaks first, with its greeting); otherwise the error wraps
// ErrProxyDataAuth, or reports the failed dial or an unreadable secret. ctx
// bounds the dial and the handshake.
//
// host may be an IPv6 literal, bare or bracketed. To open a server-mode
// beads store through a proxy, point it at the data port
// (BEADS_DOLT_SERVER_HOST, BEADS_DOLT_SERVER_PORT) and set
// BEADS_DOLT_SERVER_PROXY_ROOT to the proxy root: every connection of the
// store then makes this handshake.
func DialProxyData(ctx context.Context, proxyRoot, host string, port int) (net.Conn, error) {
	return proxy.DialData(ctx, proxyRoot, proxy.Endpoint{Host: configfile.UnbracketHost(host), Port: port})
}
