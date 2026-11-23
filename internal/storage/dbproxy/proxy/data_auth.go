package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
)

// DataNetwork is the MySQL driver network for authenticated proxy connections.
const DataNetwork = "bdproxy"

// Every data-port connection starts with a BDPROXY1 exchange, before any
// MySQL byte, that authenticates each end to the other under the proxy
// secret (the owner-only proxy.secret, rotated on every proxy start):
//
//	client: "BDPROXY1 <token> <nonce>\n"  token = identity.DataAuthTokenForPort(secret, port)
//	                                      nonce = dataNonceBytes random bytes, hex
//	proxy:  "BDPROXY1 <proof>\n"          proof = identity.DataServerProof(secret, port, nonce)
//
// The proxy closes a connection whose hello does not carry the token, sends
// it nothing, and never dials its backend for it. The client hands the
// connection to the MySQL driver only after the proof checks out: the
// address it dials was verified when it was registered, but the proxy there
// can exit (idle timeout, crash, bd dolt stop, an upgrade replacing it), and
// another local user can then bind the freed port. A long-lived client (bd
// serve never re-resolves its endpoint) would otherwise run its whole MySQL
// session, every write and every result it trusts, against that listener.
//
// Version note: a proxy started by an older bd does not accept this
// handshake, and a client older than it does not send it, so a
// mixed-version pair cannot connect. GetCreateDatabaseProxyServerEndpoint
// detects a pre-upgrade proxy — its pidfile predates the control_proto
// marker — that refuses the MAC'd IDENT handshake while holding
// proxy.lock, and stops it via its verified pidfile before starting the
// upgraded proxy.
const (
	dataAuthPrefix = "BDPROXY1 "
	dataNonceBytes = 16
	// dataTokenHexLen is the hex length of a SHA-256 HMAC.
	dataTokenHexLen = 2 * 32
	// dataHelloLen is the fixed length of the client's line, which lets the
	// proxy read exactly that many bytes and never anything of the session.
	dataHelloLen = len(dataAuthPrefix) + dataTokenHexLen + 1 + 2*dataNonceBytes + 1
	// dataProofLen is the fixed length of the proxy's answer.
	dataProofLen = len(dataAuthPrefix) + dataTokenHexLen + 1
	// dataAuthTimeout bounds each side's wait for the other's line.
	dataAuthTimeout = 5 * time.Second
)

// errDataAuth reports that the listener at a registered data address did not
// prove it holds the workspace's proxy secret.
var errDataAuth = errors.New("dbproxy: data port authentication failed")

// dataAuth is the proxy's side of the BDPROXY1 exchange.
type dataAuth struct {
	secret string
	port   int
	token  string
}

func newDataAuth(secret string, port int) dataAuth {
	return dataAuth{secret: secret, port: port, token: identity.DataAuthTokenForPort(secret, port)}
}

// accept reads conn's hello and, when it carries the token, answers it with
// the proof. It reports false, having written nothing, for any other first
// dataHelloLen bytes (or none within dataAuthTimeout); the caller closes the
// connection. err is non-nil only when conn's deadline cannot be managed.
func (a dataAuth) accept(conn net.Conn) (ok bool, err error) {
	if a.secret == "" {
		return false, nil
	}
	if err := conn.SetDeadline(time.Now().Add(dataAuthTimeout)); err != nil {
		return false, err
	}
	hello := make([]byte, dataHelloLen)
	if _, err := io.ReadFull(conn, hello); err != nil {
		return false, nil
	}
	nonceAt := len(dataAuthPrefix) + dataTokenHexLen + 1
	want := []byte(dataAuthPrefix + a.token + " ")
	if subtle.ConstantTimeCompare(hello[:nonceAt], want) != 1 || hello[dataHelloLen-1] != '\n' {
		return false, nil
	}
	nonce, err := hex.DecodeString(string(hello[nonceAt : dataHelloLen-1]))
	if err != nil || len(nonce) != dataNonceBytes {
		return false, nil
	}
	if _, err := io.WriteString(conn, dataAuthPrefix+identity.DataServerProof(a.secret, a.port, nonce)+"\n"); err != nil {
		return false, nil
	}
	return true, conn.SetDeadline(time.Time{})
}

// clientDataHandshake runs the client's side of the BDPROXY1 exchange on
// conn, a fresh connection to the proxy data port port: it sends the hello
// and returns nil only once the answer proves the listener holds secret.
// ctx's deadline (at most dataAuthTimeout) and cancellation bound the wait.
// conn's deadline is cleared again on success; the caller closes conn on
// error.
func clientDataHandshake(ctx context.Context, conn net.Conn, secret string, port int) error {
	nonce := make([]byte, dataNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("dbproxy: generate data handshake nonce: %w", err)
	}
	deadline := time.Now().Add(dataAuthTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	// A canceled ctx unblocks the exchange at once rather than at deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	err := exchangeDataHandshake(conn, secret, port, nonce)
	if !stop() {
		// ctx ended during the exchange; its deadline may be poisoned.
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	return conn.SetDeadline(time.Time{})
}

func exchangeDataHandshake(conn net.Conn, secret string, port int, nonce []byte) error {
	hello := dataAuthPrefix + identity.DataAuthTokenForPort(secret, port) + " " + hex.EncodeToString(nonce) + "\n"
	if _, err := io.WriteString(conn, hello); err != nil {
		return fmt.Errorf("dbproxy: send data handshake to %s: %w", conn.RemoteAddr(), err)
	}
	got := make([]byte, dataProofLen)
	if _, err := io.ReadFull(conn, got); err != nil {
		// A proxy that rejected the hello (a secret rotated by a restart
		// since this read it, say) closes without answering; a retry
		// re-reads the secret.
		return fmt.Errorf("dbproxy: read data handshake answer from %s: %w", conn.RemoteAddr(), err)
	}
	want := []byte(dataAuthPrefix + identity.DataServerProof(secret, port, nonce) + "\n")
	if !hmac.Equal(got, want) {
		return fmt.Errorf("%w: the listener at %s did not prove it holds this workspace's proxy secret, so it is not this workspace's proxy (it may have exited and another process taken its port); refusing to send it any data",
			errDataAuth, conn.RemoteAddr())
	}
	return nil
}

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
	// refusal. What the registration cannot vouch for is the process that
	// listens there now, so every connection makes it prove the secret
	// (clientDataHandshake) before the driver sends it anything.
	secret, err := identity.ReadSecret(rootDir)
	if err != nil {
		return nil, fmt.Errorf("dbproxy: read data authentication secret: %w", err)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if err := clientDataHandshake(ctx, conn, secret, port); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
