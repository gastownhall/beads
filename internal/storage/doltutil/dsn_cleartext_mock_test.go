package doltutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// A minimal MySQL wire-protocol server that forces an AuthSwitchRequest to
// mysql_clear_password, the way a Warpgate MySQL listener does, so these
// tests can drive the real go-sql-driver/mysql client against it. Capability
// flag bit positions mirror go-sql-driver/mysql's unexported const.go.

const (
	mysqlCapLongFlag             = 1 << 2
	mysqlCapConnectWithDB        = 1 << 3
	mysqlCapProtocol41           = 1 << 9
	mysqlCapSSL                  = 1 << 11
	mysqlCapTransactions         = 1 << 13
	mysqlCapSecureConnection     = 1 << 15
	mysqlCapMultiResults         = 1 << 17
	mysqlCapPluginAuth           = 1 << 19
	mysqlCapPluginAuthLenEncData = 1 << 21
	mysqlCapDeprecateEOF         = 1 << 24
	mysqlCapMySQL                = 1 << 0
)

func mockServerCapabilities() uint32 {
	return mysqlCapMySQL | mysqlCapLongFlag | mysqlCapConnectWithDB | mysqlCapProtocol41 |
		mysqlCapSSL | mysqlCapTransactions | mysqlCapSecureConnection | mysqlCapMultiResults |
		mysqlCapPluginAuth | mysqlCapPluginAuthLenEncData | mysqlCapDeprecateEOF
}

// writeMySQLPacket writes one packet: 3-byte length + 1-byte seq + payload.
func writeMySQLPacket(w io.Writer, seq byte, payload []byte) error {
	var hdr [4]byte
	l := len(payload)
	hdr[0] = byte(l)
	hdr[1] = byte(l >> 8)
	hdr[2] = byte(l >> 16)
	hdr[3] = seq
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// readMySQLPacket reads one packet's payload (no multi-packet reassembly;
// this mock never sends packets near the 16MB split threshold).
func readMySQLPacket(r io.Reader) (payload []byte, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	l := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	payload = make([]byte, l)
	if l > 0 {
		if _, err = io.ReadFull(r, payload); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

// buildInitialHandshakePacket advertises mysql_native_password as the
// default plugin; the mock always switches to mysql_clear_password via
// AuthSwitchRequest afterward, so the scramble bytes go unused.
func buildInitialHandshakePacket(caps uint32) []byte {
	var buf []byte
	buf = append(buf, 0x0a) // protocol version 10
	buf = append(buf, "8.0.34-beads-mock"...)
	buf = append(buf, 0x00)
	buf = append(buf, 0x01, 0x00, 0x00, 0x00) // connection id
	buf = append(buf, "12345678"...)          // auth-plugin-data-part-1 (8 bytes)
	buf = append(buf, 0x00)                   // filler
	var capLower [2]byte
	binary.LittleEndian.PutUint16(capLower[:], uint16(caps))
	buf = append(buf, capLower[:]...)
	buf = append(buf, 0x21)       // character set: utf8_general_ci
	buf = append(buf, 0x02, 0x00) // status flags: SERVER_STATUS_AUTOCOMMIT
	var capUpper [2]byte
	binary.LittleEndian.PutUint16(capUpper[:], uint16(caps>>16))
	buf = append(buf, capUpper[:]...)
	buf = append(buf, 21)                  // auth-plugin-data length (8 + 13)
	buf = append(buf, make([]byte, 10)...) // reserved
	buf = append(buf, "123456789012"...)   // auth-plugin-data-part-2 (12 bytes)
	buf = append(buf, 0x00)                // terminator
	buf = append(buf, "mysql_native_password"...)
	buf = append(buf, 0x00)
	return buf
}

func buildAuthSwitchRequest(plugin string) []byte {
	buf := []byte{0xfe} // AuthSwitchRequest indicator
	buf = append(buf, plugin...)
	buf = append(buf, 0x00)
	return buf
}

// minimalOKPacket: indicator, zero affected rows, zero insert id, autocommit
// status, zero warnings.
func minimalOKPacket() []byte {
	return []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
}

// minimalEOFAsOKPacket is the DeprecateEOF-era result-set terminator: an OK
// packet wearing the legacy 0xFE EOF-indicator byte.
func minimalEOFAsOKPacket() []byte {
	return []byte{0xfe, 0x00, 0x00, 0x02, 0x00}
}

// answerMaxAllowedPacketQuery answers "SELECT @@max_allowed_packet", which
// go-sql-driver/mysql issues right after auth whenever cfg.MaxAllowedPacket
// is 0 — which ServerDSN.String() always leaves it as. Without this the
// driver hangs waiting for a result set that never comes. clientDeprecateEOF
// is negotiated, so no separate EOF packet follows the column definition.
func answerMaxAllowedPacketQuery(rw io.ReadWriter) error {
	if _, err := readMySQLPacket(rw); err != nil { // the query itself; content unused
		return fmt.Errorf("read query: %w", err)
	}
	if err := writeMySQLPacket(rw, 1, []byte{0x01}); err != nil { // column count: 1
		return fmt.Errorf("write column count: %w", err)
	}
	// One column-definition packet; skipColumns discards it unparsed, so the
	// content only needs to look like a ColumnDefinition41.
	colDef := []byte{}
	colDef = append(colDef, 0x03, 'd', 'e', 'f') // catalog "def"
	for range 4 {                                // schema, table, org_table
		colDef = append(colDef, 0x00)
	}
	colDef = append(colDef, 0x11, 'm', 'a', 'x', '_', 'a', 'l', 'l', 'o', 'w', 'e', 'd', '_', 'p', 'a', 'c', 'k', 'e', 't')
	colDef = append(colDef, 0x00) // org_name
	if err := writeMySQLPacket(rw, 2, colDef); err != nil {
		return fmt.Errorf("write column definition: %w", err)
	}
	row := append([]byte{0x07}, "4194304"...) // one length-encoded-string row
	if err := writeMySQLPacket(rw, 3, row); err != nil {
		return fmt.Errorf("write row: %w", err)
	}
	if err := writeMySQLPacket(rw, 4, minimalEOFAsOKPacket()); err != nil {
		return fmt.Errorf("write result-set terminator: %w", err)
	}
	return nil
}

// serveClearPasswordAuthOnce drives the handshake far enough to force
// mysql_clear_password, then reports pass/fail by whether the client answers
// it. diag is buffered and non-blocking: errors here (t.Errorf from this
// goroutine after the subtest returns would panic the binary) go there
// instead, including the expected case where AllowCleartextPasswords=false
// makes the client bail without ever sending the cleartext packet.
func serveClearPasswordAuthOnce(conn net.Conn, tlsCfg *tls.Config, diag chan<- string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	note := func(format string, args ...any) {
		select {
		case diag <- fmt.Sprintf(format, args...):
		default:
		}
	}

	if err := writeMySQLPacket(conn, 0, buildInitialHandshakePacket(mockServerCapabilities())); err != nil {
		note("write initial handshake: %v", err)
		return
	}

	// seq 1: an SSLRequest (32-byte HandshakeResponse41 prefix, before the
	// TLS upgrade) or a full HandshakeResponse41 sent in the clear.
	firstResp, err := readMySQLPacket(conn)
	if err != nil {
		note("read first client packet: %v", err)
		return
	}

	var rw io.ReadWriter = conn
	nextSeq := byte(2)
	if len(firstResp) == 32 && tlsCfg != nil {
		tlsConn := tls.Server(conn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			note("TLS handshake: %v", err)
			return
		}
		rw = tlsConn
		if _, err := readMySQLPacket(rw); err != nil { // real HandshakeResponse41, over TLS
			note("read post-TLS HandshakeResponse41: %v", err)
			return
		}
		nextSeq = 3
	}

	if err := writeMySQLPacket(rw, nextSeq, buildAuthSwitchRequest("mysql_clear_password")); err != nil {
		note("write AuthSwitchRequest: %v", err)
		return
	}

	if _, err := readMySQLPacket(rw); err != nil {
		note("read cleartext password response (ok if AllowCleartextPasswords=false): %v", err)
		return
	}

	if err := writeMySQLPacket(rw, nextSeq+2, minimalOKPacket()); err != nil {
		note("write final OK packet: %v", err)
		return
	}

	if err := answerMaxAllowedPacketQuery(rw); err != nil {
		note("answer max_allowed_packet query: %v", err)
		return
	}
	note("handshake completed")
}

// startMockClearPasswordServer starts a one-shot listener, returning its
// address and a buffered channel of diagnostic notes for failure messages.
// tlsCfg may be nil when the test doesn't need TLS.
func startMockClearPasswordServer(t *testing.T, tlsCfg *tls.Config) (addr string, diag <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock server: listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if tcpLn, ok := ln.(*net.TCPListener); ok {
		_ = tcpLn.SetDeadline(time.Now().Add(15 * time.Second)) // never leak the Accept goroutine
	}
	diagCh := make(chan string, 16)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case diagCh <- fmt.Sprintf("Accept: %v", err):
			default:
			}
			return
		}
		serveClearPasswordAuthOnce(conn, tlsCfg, diagCh)
	}()
	return ln.Addr().String(), diagCh
}

// generateSelfSignedTLSConfig returns a throwaway server TLS config.
// Test-only: production always uses real verification (ServerDSN.String()
// sets tls=true/false, never skip-verify); the client side of these tests
// opts out of verification since this cert has no real trust chain.
func generateSelfSignedTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "beads-cleartext-mock"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create self-signed certificate: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  priv,
		}},
	}
}

// dialViaServerDSN parses a DSN built by ServerDSN.String() and opens a
// (lazy) *sql.DB. skipTLSVerify lets the test trust the mock's self-signed
// cert; production callers never set it.
func dialViaServerDSN(t *testing.T, dsn string, skipTLSVerify bool) *sql.DB {
	t.Helper()
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("mysql.ParseDSN(%q): %v", dsn, err)
	}
	if skipTLSVerify && cfg.TLS != nil {
		cfg.TLS.InsecureSkipVerify = true
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("mysql.NewConnector: %v", err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// drainDiag collects the mock server's buffered notes for a failure message.
func drainDiag(diag <-chan string) string {
	var notes []string
	for {
		select {
		case n := <-diag:
			notes = append(notes, n)
		default:
			return strings.Join(notes, "; ")
		}
	}
}

// TestServerDSN_ClearTextPasswordAuth_RefusedWithoutFlag: bd's default DSN
// shape (AllowCleartextPasswords false) is refused by a server that demands
// mysql_clear_password — the failure this change fixes.
func TestServerDSN_ClearTextPasswordAuth_RefusedWithoutFlag(t *testing.T) {
	addr, diag := startMockClearPasswordServer(t, nil)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split mock server addr %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse mock server port %q: %v", portStr, err)
	}

	dsn := ServerDSN{
		Host:     host,
		Port:     port,
		User:     "root",
		Password: "s3cret",
		Timeout:  5 * time.Second,
	}.String()

	db := dialViaServerDSN(t, dsn, false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = db.Conn(ctx)
	if err == nil {
		t.Fatal("expected connecting without AllowCleartextPasswords to fail, got nil error")
	}
	if !strings.Contains(err.Error(), "clear text authentication") {
		t.Fatalf("expected a clear-text-password refusal (mysql.ErrCleartextPassword), got: %v (mock server notes: %s)",
			err, drainDiag(diag))
	}
}

// TestServerDSN_ClearTextPasswordAuth_SucceedsWithFlagAndTLS: TLS +
// AllowCleartextPasswords completes the handshake against the same server.
func TestServerDSN_ClearTextPasswordAuth_SucceedsWithFlagAndTLS(t *testing.T) {
	tlsCfg := generateSelfSignedTLSConfig(t)
	addr, diag := startMockClearPasswordServer(t, tlsCfg)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split mock server addr %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse mock server port %q: %v", portStr, err)
	}

	dsn := ServerDSN{
		Host:                    host,
		Port:                    port,
		User:                    "root",
		Password:                "s3cret",
		Timeout:                 5 * time.Second,
		TLS:                     true,
		AllowCleartextPasswords: true,
	}.String()

	if !strings.Contains(dsn, "tls=true") || !strings.Contains(dsn, "allowCleartextPasswords=true") {
		t.Fatalf("test DSN missing expected params; got %q", dsn)
	}

	db := dialViaServerDSN(t, dsn, true) // skip verify: mock cert is self-signed

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("expected AllowCleartextPasswords+TLS to complete the handshake, got: %v (mock server notes: %s)",
			err, drainDiag(diag))
	}
	_ = conn.Close()
}
