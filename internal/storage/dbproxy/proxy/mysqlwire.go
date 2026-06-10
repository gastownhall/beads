package proxy

// MySQL wire-protocol helpers for the session-pooling proxy.
//
// The proxy terminates the client handshake itself (skip-auth: clients are
// loopback-only and already trusted by today's design), then borrows a
// pre-authenticated backend connection from a pool. After the handshake phase
// completes on both sides the data path is byte-transparent (io.Copy), exactly
// as the non-pooling proxy does today — command-phase packets do not depend on
// connection id or salt, only on the negotiated capability flags, which the
// proxy keeps in parity between the two sides (see pool keying).
//
// Byte layouts are hand-rolled rather than borrowed from dolthub/vitess because
// vitess's handshake builders are unexported and its *mysql.Conn buffers
// internally, which is incompatible with handing the raw net.Conn to io.Copy.
// References: MySQL Client/Server Protocol; go-sql-driver/mysql packets.go;
// dolthub/vitess go/mysql/{server,client,conn}.go.

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// MySQL capability flags (subset we care about). Values match the protocol
// constants used by every MySQL-compatible client/server.
const (
	capLongPassword     uint32 = 1 << 0
	capFoundRows        uint32 = 1 << 1
	capLongFlag         uint32 = 1 << 2
	capConnectWithDB    uint32 = 1 << 3
	capProtocol41       uint32 = 1 << 9
	capSSL              uint32 = 1 << 11
	capTransactions     uint32 = 1 << 13
	capSecureConnection uint32 = 1 << 15
	capMultiStatements  uint32 = 1 << 16
	capMultiResults     uint32 = 1 << 17
	capPluginAuth       uint32 = 1 << 19
	capConnectAttrs     uint32 = 1 << 20
	capPluginAuthLenenc uint32 = 1 << 21
	capDeprecateEOF     uint32 = 1 << 24
)

// MySQL command bytes (command-phase first byte).
const (
	comQuery           byte = 0x03
	comInitDB          byte = 0x02
	comPing            byte = 0x0e
	comResetConnection byte = 0x1f
)

const (
	mysqlNativePassword = "mysql_native_password"
	// serverCapabilities is what the proxy advertises to clients. It is a
	// superset; the client narrows it in its response and the proxy mirrors
	// the client's effective set onto the backend, keeping wire parity.
	serverCapabilities = capLongPassword | capFoundRows | capLongFlag |
		capConnectWithDB | capProtocol41 | capTransactions |
		capSecureConnection | capMultiStatements | capMultiResults |
		capPluginAuth | capPluginAuthLenenc | capDeprecateEOF
	serverCharsetUTF8MB4 = 0x2d // utf8mb4_general_ci
	statusAutocommit     = 0x0002
)

var errProtocol = errors.New("mysqlwire: protocol error")

// ---- packet framing -------------------------------------------------------

// readPacket reads one MySQL packet payload from r, returning the payload and
// the sequence id of the (last) frame. It transparently reassembles packets
// split across 16MiB frames.
func readPacket(r io.Reader) (payload []byte, seq byte, err error) {
	var hdr [4]byte
	for {
		if _, err = io.ReadFull(r, hdr[:]); err != nil {
			return nil, 0, err
		}
		n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
		seq = hdr[3]
		if n > 0 {
			buf := make([]byte, n)
			if _, err = io.ReadFull(r, buf); err != nil {
				return nil, 0, err
			}
			payload = append(payload, buf...)
		}
		if n < 0xffffff { // last frame of this packet
			return payload, seq, nil
		}
	}
}

// writePacket frames payload as a single MySQL packet with the given sequence
// id and writes it. Payloads larger than 16MiB are not expected during the
// handshake/control exchanges this helper is used for.
func writePacket(w io.Writer, seq byte, payload []byte) error {
	n := len(payload)
	if n >= 0xffffff {
		return fmt.Errorf("mysqlwire: control packet too large (%d bytes)", n)
	}
	hdr := []byte{byte(n), byte(n >> 8), byte(n >> 16), seq}
	if _, err := w.Write(append(hdr, payload...)); err != nil {
		return err
	}
	return nil
}

// ---- length-encoded integers ---------------------------------------------

func appendLenencInt(b []byte, v uint64) []byte {
	switch {
	case v < 251:
		return append(b, byte(v))
	case v < 1<<16:
		return append(b, 0xfc, byte(v), byte(v>>8))
	case v < 1<<24:
		return append(b, 0xfd, byte(v), byte(v>>8), byte(v>>16))
	default:
		var tmp [8]byte
		binary.LittleEndian.PutUint64(tmp[:], v)
		return append(append(b, 0xfe), tmp[:]...)
	}
}

// readLenencInt reads a length-encoded integer from p starting at off,
// returning the value and the new offset.
func readLenencInt(p []byte, off int) (val uint64, next int, err error) {
	if off >= len(p) {
		return 0, off, errProtocol
	}
	switch first := p[off]; {
	case first < 251:
		return uint64(first), off + 1, nil
	case first == 0xfc:
		if off+3 > len(p) {
			return 0, off, errProtocol
		}
		return uint64(p[off+1]) | uint64(p[off+2])<<8, off + 3, nil
	case first == 0xfd:
		if off+4 > len(p) {
			return 0, off, errProtocol
		}
		return uint64(p[off+1]) | uint64(p[off+2])<<8 | uint64(p[off+3])<<16, off + 4, nil
	case first == 0xfe:
		if off+9 > len(p) {
			return 0, off, errProtocol
		}
		return binary.LittleEndian.Uint64(p[off+1 : off+9]), off + 9, nil
	default:
		return 0, off, errProtocol
	}
}

// ---- server-side handshake (proxy → client) ------------------------------

// clientHandshake holds the parts of a client's HandshakeResponse41 the proxy
// needs to mirror onto a backend connection.
type clientHandshake struct {
	capabilities uint32 // effective caps (intersection with what we advertised)
	database     string
	username     string
}

// serverGreeting builds a HandshakeV10 packet advertising mysql_native_password
// with the given 20-byte salt.
func serverGreeting(connID uint32, salt []byte) []byte {
	caps := serverCapabilities
	p := make([]byte, 0, 128)
	p = append(p, 10) // protocol version
	p = append(p, "beads-proxy"...)
	p = append(p, 0) // server version NUL
	var idb [4]byte
	binary.LittleEndian.PutUint32(idb[:], connID)
	p = append(p, idb[:]...)
	p = append(p, salt[:8]...) // auth-plugin-data-part-1
	p = append(p, 0)           // filler
	p = append(p, byte(caps), byte(caps>>8))
	p = append(p, serverCharsetUTF8MB4)
	p = append(p, byte(statusAutocommit), byte(statusAutocommit>>8))
	p = append(p, byte(caps>>16), byte(caps>>24))
	p = append(p, 21)                  // auth-plugin-data len (20 salt + NUL)
	p = append(p, make([]byte, 10)...) // reserved
	p = append(p, salt[8:20]...)       // auth-plugin-data-part-2 (12 bytes)
	p = append(p, 0)                   // NUL terminator for part-2
	p = append(p, mysqlNativePassword...)
	p = append(p, 0)
	return p
}

// parseClientHandshakeResponse extracts capabilities, username and database
// from a HandshakeResponse41 payload.
func parseClientHandshakeResponse(p []byte) (clientHandshake, error) {
	var h clientHandshake
	if len(p) < 32 {
		return h, errProtocol
	}
	h.capabilities = binary.LittleEndian.Uint32(p[0:4])
	if h.capabilities&capProtocol41 == 0 {
		return h, fmt.Errorf("%w: client did not request PROTOCOL_41", errProtocol)
	}
	off := 32 // 4 caps + 4 maxpkt + 1 charset + 23 reserved
	// username (NUL-terminated)
	end := indexByte(p, off, 0)
	if end < 0 {
		return h, errProtocol
	}
	h.username = string(p[off:end])
	off = end + 1
	// auth-response
	if h.capabilities&capPluginAuthLenenc != 0 {
		alen, next, err := readLenencInt(p, off)
		if err != nil {
			return h, err
		}
		off = next + int(alen)
	} else if h.capabilities&capSecureConnection != 0 {
		if off >= len(p) {
			return h, errProtocol
		}
		alen := int(p[off])
		off += 1 + alen
	} else {
		end = indexByte(p, off, 0)
		if end < 0 {
			return h, errProtocol
		}
		off = end + 1
	}
	if off > len(p) {
		return h, errProtocol
	}
	// database
	if h.capabilities&capConnectWithDB != 0 {
		end = indexByte(p, off, 0)
		if end < 0 {
			return h, errProtocol
		}
		h.database = string(p[off:end])
	}
	return h, nil
}

func indexByte(p []byte, from int, b byte) int {
	for i := from; i < len(p); i++ {
		if p[i] == b {
			return i
		}
	}
	return -1
}

// okPacket builds a minimal OK packet for the negotiated capabilities.
func okPacket(caps uint32) []byte {
	p := make([]byte, 0, 16)
	p = append(p, 0x00)       // OK header
	p = appendLenencInt(p, 0) // affected rows
	p = appendLenencInt(p, 0) // last insert id
	if caps&capProtocol41 != 0 {
		p = append(p, byte(statusAutocommit), byte(statusAutocommit>>8))
		p = append(p, 0, 0) // warnings
	} else if caps&capTransactions != 0 {
		p = append(p, byte(statusAutocommit), byte(statusAutocommit>>8))
	}
	return p
}

// acceptClient performs the server side of the handshake against an incoming
// client connection, accepting any credentials (skip-auth). It returns the
// negotiated client parameters needed to drive a matching backend.
func acceptClient(conn net.Conn, connID uint32, deadline time.Time) (clientHandshake, error) {
	if !deadline.IsZero() {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	salt := makeSalt(connID)
	if err := writePacket(conn, 0, serverGreeting(connID, salt)); err != nil {
		return clientHandshake{}, fmt.Errorf("mysqlwire: write greeting: %w", err)
	}
	resp, seq, err := readPacket(conn)
	if err != nil {
		return clientHandshake{}, fmt.Errorf("mysqlwire: read handshake response: %w", err)
	}
	// Reject TLS upgrade requests: the proxy is loopback-only and does not
	// terminate TLS. A client that set CLIENT_SSL would send a short SSL
	// request packet and expect a TLS handshake next; fail clearly instead.
	h, err := parseClientHandshakeResponse(resp)
	if err != nil {
		return clientHandshake{}, err
	}
	if h.capabilities&capSSL != 0 {
		return clientHandshake{}, fmt.Errorf("%w: client requested TLS, unsupported by pooling proxy", errProtocol)
	}
	// Mirror only the capabilities we also advertised.
	h.capabilities &= serverCapabilities
	if err := writePacket(conn, seq+1, okPacket(h.capabilities)); err != nil {
		return clientHandshake{}, fmt.Errorf("mysqlwire: write auth OK: %w", err)
	}
	return h, nil
}

func makeSalt(connID uint32) []byte {
	// Deterministic, non-secret salt: auth is skipped, the salt only needs to
	// be 20 well-formed non-NUL bytes. Math/rand is intentionally avoided
	// (unavailable / nondeterministic); the value is irrelevant to security
	// here because the proxy accepts any auth response.
	salt := make([]byte, 20)
	for i := range salt {
		salt[i] = byte(1 + (int(connID)+i*7)%250) // 1..250, never NUL
	}
	return salt
}

// ---- client-side handshake (proxy → dolt backend) ------------------------

// backendHandshake performs the client side of the MySQL handshake against a
// freshly dialed backend connection, negotiating exactly caps so the resulting
// session is wire-compatible with the client the proxy will forward. user is
// typically "root" with an empty password (loopback dolt).
func backendHandshake(conn net.Conn, caps uint32, database, user, password string, deadline time.Time) error {
	if !deadline.IsZero() {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	greeting, _, err := readPacket(conn)
	if err != nil {
		return fmt.Errorf("mysqlwire: read backend greeting: %w", err)
	}
	salt, plugin, err := parseServerGreeting(greeting)
	if err != nil {
		return err
	}

	authResp := scramble(plugin, password, salt)
	resp := buildHandshakeResponse41(caps, database, user, authResp, plugin)
	if err := writePacket(conn, 1, resp); err != nil {
		return fmt.Errorf("mysqlwire: write backend handshake response: %w", err)
	}
	return readBackendAuthResult(conn, password, salt)
}

// parseServerGreeting extracts the 20-byte salt and auth plugin name from a
// backend HandshakeV10 packet.
func parseServerGreeting(p []byte) (salt []byte, plugin string, err error) {
	if len(p) < 1 || p[0] != 10 {
		return nil, "", fmt.Errorf("%w: unexpected protocol version", errProtocol)
	}
	off := 1
	end := indexByte(p, off, 0) // server version NUL
	if end < 0 {
		return nil, "", errProtocol
	}
	off = end + 1
	off += 4 // connection id
	if off+8 > len(p) {
		return nil, "", errProtocol
	}
	salt = append(salt, p[off:off+8]...)
	off += 8
	off++ // filler
	if off+2 > len(p) {
		return nil, "", errProtocol
	}
	capLow := uint32(p[off]) | uint32(p[off+1])<<8
	off += 2
	if off >= len(p) {
		// Pre-4.1 server: only lower caps present. Not expected from dolt.
		return salt, mysqlNativePassword, nil
	}
	off++    // charset
	off += 2 // status flags
	capHigh := uint32(p[off]) | uint32(p[off+1])<<8
	off += 2
	caps := capLow | capHigh<<16
	authDataLen := int(p[off])
	off++
	off += 10 // reserved
	if caps&capSecureConnection != 0 {
		part2 := authDataLen - 8
		if part2 < 13 {
			part2 = 13
		}
		// part2 includes a NUL terminator; copy len-1 meaningful bytes but be
		// defensive about bounds.
		take := part2
		if off+take > len(p) {
			take = len(p) - off
		}
		seg := p[off : off+take]
		// strip trailing NUL
		for len(seg) > 0 && seg[len(seg)-1] == 0 {
			seg = seg[:len(seg)-1]
		}
		salt = append(salt, seg...)
		off += take
	}
	plugin = mysqlNativePassword
	if caps&capPluginAuth != 0 && off < len(p) {
		end = indexByte(p, off, 0)
		if end < 0 {
			end = len(p)
		}
		plugin = string(p[off:end])
	}
	return salt, plugin, nil
}

// buildHandshakeResponse41 builds the client HandshakeResponse41 packet.
func buildHandshakeResponse41(caps uint32, database, user string, authResp []byte, plugin string) []byte {
	// Always set the auth-related caps we rely on; never request TLS.
	caps |= capProtocol41 | capSecureConnection | capPluginAuth
	caps &^= capSSL
	if database != "" {
		caps |= capConnectWithDB
	} else {
		caps &^= capConnectWithDB
	}
	caps &^= capConnectAttrs // we send no connection attributes

	p := make([]byte, 0, 64)
	var capb [4]byte
	binary.LittleEndian.PutUint32(capb[:], caps)
	p = append(p, capb[:]...)
	var maxpkt [4]byte
	binary.LittleEndian.PutUint32(maxpkt[:], 1<<24-1)
	p = append(p, maxpkt[:]...)
	p = append(p, serverCharsetUTF8MB4)
	p = append(p, make([]byte, 23)...) // reserved
	p = append(p, user...)
	p = append(p, 0)
	if caps&capPluginAuthLenenc != 0 {
		p = appendLenencInt(p, uint64(len(authResp)))
		p = append(p, authResp...)
	} else {
		p = append(p, byte(len(authResp)))
		p = append(p, authResp...)
	}
	if caps&capConnectWithDB != 0 {
		p = append(p, database...)
		p = append(p, 0)
	}
	if caps&capPluginAuth != 0 {
		p = append(p, plugin...)
		p = append(p, 0)
	}
	return p
}

// readBackendAuthResult reads the backend's reply to our handshake response,
// handling OK, ERR, AuthSwitchRequest and caching_sha2 fast-auth flows for the
// empty-password case.
func readBackendAuthResult(conn net.Conn, password string, salt []byte) error {
	for {
		pkt, seq, err := readPacket(conn)
		if err != nil {
			return fmt.Errorf("mysqlwire: read backend auth result: %w", err)
		}
		if len(pkt) == 0 {
			return fmt.Errorf("%w: empty backend auth packet", errProtocol)
		}
		switch pkt[0] {
		case 0x00: // OK
			return nil
		case 0xff: // ERR
			return backendErr(pkt)
		case 0xfe: // AuthSwitchRequest
			plugin, switchSalt := parseAuthSwitch(pkt)
			authResp := scramble(plugin, password, switchSalt)
			if err := writePacket(conn, seq+1, authResp); err != nil {
				return fmt.Errorf("mysqlwire: write auth switch response: %w", err)
			}
		case 0x01: // AuthMoreData (caching_sha2_password)
			// Empty password fast path: server sends 0x01 0x03 (fast auth
			// success) then OK, or 0x01 0x04 (full auth) which over a plaintext
			// loopback channel expects the cleartext password + NUL. We only
			// support empty passwords for the managed loopback dolt.
			if len(pkt) >= 2 && pkt[1] == 0x04 {
				if password != "" {
					return fmt.Errorf("%w: caching_sha2 full auth with password unsupported", errProtocol)
				}
				if err := writePacket(conn, seq+1, []byte{0x00}); err != nil {
					return fmt.Errorf("mysqlwire: write cleartext auth: %w", err)
				}
			}
			// otherwise (0x03 fast success) loop to read the following OK.
		default:
			return fmt.Errorf("%w: unexpected backend auth packet 0x%02x", errProtocol, pkt[0])
		}
	}
}

func parseAuthSwitch(pkt []byte) (plugin string, salt []byte) {
	off := 1
	end := indexByte(pkt, off, 0)
	if end < 0 {
		return mysqlNativePassword, nil
	}
	plugin = string(pkt[off:end])
	off = end + 1
	seg := pkt[off:]
	for len(seg) > 0 && seg[len(seg)-1] == 0 {
		seg = seg[:len(seg)-1]
	}
	return plugin, seg
}

func backendErr(pkt []byte) error {
	if len(pkt) < 3 {
		return fmt.Errorf("%w: malformed backend error", errProtocol)
	}
	code := uint16(pkt[1]) | uint16(pkt[2])<<8
	msg := pkt[3:]
	if len(msg) > 6 && msg[0] == '#' {
		msg = msg[6:] // skip '#' + 5-byte sqlstate
	}
	return fmt.Errorf("mysqlwire: backend rejected handshake (%d): %s", code, string(msg))
}

// scramble computes the auth response for the given plugin and salt. For
// mysql_native_password with an empty password the result is empty.
func scramble(plugin, password string, salt []byte) []byte {
	if password == "" {
		return nil
	}
	switch plugin {
	case mysqlNativePassword, "":
		return nativeScramble(password, salt)
	default:
		// caching_sha2_password and others with a real password are not used
		// by the managed loopback dolt; empty-password path above covers it.
		return nativeScramble(password, salt)
	}
}

// nativeScramble implements mysql_native_password:
// SHA1(password) XOR SHA1(salt + SHA1(SHA1(password))).
func nativeScramble(password string, salt []byte) []byte {
	h1 := sha1.Sum([]byte(password))
	h2 := sha1.Sum(h1[:])
	h := sha1.New()
	_, _ = h.Write(salt)
	_, _ = h.Write(h2[:])
	mixed := h.Sum(nil)
	out := make([]byte, len(h1))
	for i := range h1 {
		out[i] = h1[i] ^ mixed[i]
	}
	return out
}

// ---- control commands on a pooled backend --------------------------------

// resetBackend sends COM_RESET_CONNECTION and waits for the OK reply. This is
// the isolation primitive: it discards session variables, open transactions,
// temp tables and prepared statements before the connection is reused by the
// next client. (database/sql's ResetSession does NOT send this — it only does
// a liveness check — which is why the proxy must send it explicitly.)
func resetBackend(ctx context.Context, conn net.Conn) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	if err := writePacket(conn, 0, []byte{comResetConnection}); err != nil {
		return fmt.Errorf("mysqlwire: write COM_RESET_CONNECTION: %w", err)
	}
	pkt, seq, err := readPacket(conn)
	if err != nil {
		return fmt.Errorf("mysqlwire: read reset reply: %w", err)
	}
	// The reply to a fresh command (seq 0) is always seq 1. If we instead read
	// a packet at some other sequence, the connection was misaligned — e.g. a
	// client was killed mid-result and unread result packets are still in the
	// stream. Treat that as a hard failure so the pool destroys the connection
	// rather than lending a corrupted one to the next borrower.
	if seq != 1 {
		return fmt.Errorf("%w: reset reply out of sequence (seq=%d) — connection misaligned", errProtocol, seq)
	}
	if len(pkt) > 0 && pkt[0] == 0xff {
		return backendErr(pkt)
	}
	if len(pkt) == 0 || pkt[0] != 0x00 {
		return fmt.Errorf("%w: unexpected reset reply (first byte 0x%02x)", errProtocol, firstByte(pkt))
	}
	return nil
}

func firstByte(p []byte) byte {
	if len(p) == 0 {
		return 0
	}
	return p[0]
}

// pingBackend sends COM_PING and waits for OK; used as a cheap liveness check
// before lending a pooled connection.
func pingBackend(ctx context.Context, conn net.Conn) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	if err := writePacket(conn, 0, []byte{comPing}); err != nil {
		return err
	}
	pkt, _, err := readPacket(conn)
	if err != nil {
		return err
	}
	if len(pkt) > 0 && pkt[0] == 0xff {
		return backendErr(pkt)
	}
	return nil
}
