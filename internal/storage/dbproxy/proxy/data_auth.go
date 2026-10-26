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
// mixed-version pair cannot connect. A still-running pre-upgrade proxy
// holds proxy.lock, so after upgrading bd the old proxy must be stopped
// (via proxy.pid) before a new managed proxy takes over.
const dataAuthPrefix = "BDPROXY1 "

var dataAddressRoots sync.Map

func init() {
	mysql.RegisterDialContext(DataNetwork, dialData)
}

func registerDataAddress(rootDir string, endpoint Endpoint) {
	addr := net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	dataAddressRoots.Store(addr, rootDir)
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
	secret, err := identity.ReadSecret(rootDir)
	if err != nil {
		return nil, fmt.Errorf("dbproxy: read data authentication secret: %w", err)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	line := []byte(dataAuthPrefix + identity.DataAuthToken(secret) + "\n")
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
