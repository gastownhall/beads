package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
)

// DataNetwork is the MySQL driver network for authenticated proxy connections.
const DataNetwork = "bdproxy"

// dataAuthPrefix starts the first line every data-port connection sends.
// Version note: a proxy started by an older bd does not accept this
// handshake, and a client older than it does not send it, so a
// mixed-version pair cannot connect. GetCreateDatabaseProxyServerEndpoint
// detects a pre-upgrade proxy — its pidfile predates the control_proto
// marker — that refuses the MAC'd IDENT handshake while holding
// proxy.lock, and stops it via its verified pidfile before starting the
// upgraded proxy.
const dataAuthPrefix = "BDPROXY1 "

var dataAddressRoots sync.Map

func init() {
	mysql.RegisterDialContext(DataNetwork, dialData)
}

// RegisterDataAddress associates a proxy endpoint with its workspace rootDir
// so the bdproxy MySQL dialer can authenticate data-port connections.
func RegisterDataAddress(rootDir string, endpoint Endpoint) {
	addr := net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	dataAddressRoots.Store(addr, rootDir)
}

func registerDataAddress(rootDir string, endpoint Endpoint) {
	RegisterDataAddress(rootDir, endpoint)
}

func dialData(ctx context.Context, addr string) (net.Conn, error) {
	value, ok := dataAddressRoots.Load(addr)
	if !ok {
		return nil, fmt.Errorf("dbproxy: unknown data address %q", addr)
	}
	rootDir, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("dbproxy: invalid data address registration for %q", addr)
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("dbproxy: invalid data address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return nil, fmt.Errorf("dbproxy: invalid data port in %q", addr)
	}
	// The address was registered only after readAndDial or spawn verified
	// the proxy that owns it, so the pidfile is not re-read on every pool
	// dial — the proxy removes proxy.pid first at shutdown, and a dial in
	// flight would otherwise race the removal into a bogus permanent
	// refusal. A token minted for this port is useless to a listener that
	// cannot answer the handshake, so a stale registration is still safe.
	secret, err := identity.ReadSecret(rootDir)
	if err != nil {
		return nil, fmt.Errorf("dbproxy: read data authentication secret: %w", err)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	line := []byte(dataAuthPrefix + identity.DataAuthTokenForPort(secret, port) + "\n")
	n, err := conn.Write(line)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if n != len(line) {
		_ = conn.Close()
		return nil, io.ErrShortWrite
	}
	return conn, nil
}
