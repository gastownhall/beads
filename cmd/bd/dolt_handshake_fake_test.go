package main

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // G505: mysql_native_password is SHA-1 by definition; this fake verifies it
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// handshakeLogin is what one client presented to a fakeMySQLLogin listener.
type handshakeLogin struct {
	User string
	// Auth is the mysql_native_password response to the listener's scramble.
	Auth []byte
}

// fakeMySQLLogin is a TCP listener that speaks just enough of the MySQL
// protocol to see who logs in: it sends a mysql_native_password handshake,
// records the user and auth response a client answers with, and refuses the
// login with "Access denied". A connection that reads the handshake and
// closes (a reachability probe) records no login; Conns counts it.
type fakeMySQLLogin struct {
	ln       net.Listener
	scramble [20]byte
	mu       sync.Mutex
	logins   []handshakeLogin
	conns    int
}

// newFakeMySQLLogin starts a fakeMySQLLogin on 127.0.0.1, closed with t.
func newFakeMySQLLogin(t *testing.T) *fakeMySQLLogin {
	t.Helper()
	return newFakeMySQLLoginOn(t, "tcp", "127.0.0.1:0")
}

// newFakeMySQLLoginOn starts a fakeMySQLLogin listening on network and addr
// (a unix socket path for "unix"), closed with t.
func newFakeMySQLLoginOn(t *testing.T, network, addr string) *fakeMySQLLogin {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeMySQLLogin{ln: ln}
	for i := range f.scramble {
		f.scramble[i] = byte(0x21 + i) // printable, never 0
	}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeMySQLLogin) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

// Logins are the logins recorded so far.
func (f *fakeMySQLLogin) Logins() []handshakeLogin {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]handshakeLogin(nil), f.logins...)
}

// Conns is how many connections f has accepted so far, a reachability
// probe's included.
func (f *fakeMySQLLogin) Conns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// PresentsPassword reports whether login's auth response is password's
// mysql_native_password answer to f's scramble (empty for no password).
func (f *fakeMySQLLogin) PresentsPassword(login handshakeLogin, password string) bool {
	if password == "" {
		return len(login.Auth) == 0
	}
	h1 := sha1.Sum([]byte(password)) //nolint:gosec // G401: see import
	h2 := sha1.Sum(h1[:])            //nolint:gosec // G401: see import
	h3 := sha1.Sum(append(f.scramble[:], h2[:]...))
	want := make([]byte, len(h1))
	for i := range h1 {
		want[i] = h1[i] ^ h3[i]
	}
	return bytes.Equal(login.Auth, want)
}

func (f *fakeMySQLLogin) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns++
		f.mu.Unlock()
		go f.handle(conn)
	}
}

func (f *fakeMySQLLogin) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	// CLIENT_LONG_PASSWORD | CONNECT_WITH_DB | PROTOCOL_41 | TRANSACTIONS |
	// SECURE_CONNECTION | PLUGIN_AUTH
	const caps = 0x1 | 0x8 | 0x200 | 0x2000 | 0x8000 | 0x80000
	var hs bytes.Buffer
	hs.WriteByte(10)
	hs.WriteString("8.0.33-fake\x00")
	_ = binary.Write(&hs, binary.LittleEndian, uint32(1))
	hs.Write(f.scramble[:8])
	hs.WriteByte(0)
	_ = binary.Write(&hs, binary.LittleEndian, uint16(caps&0xffff))
	hs.WriteByte(0x21)
	_ = binary.Write(&hs, binary.LittleEndian, uint16(2))
	_ = binary.Write(&hs, binary.LittleEndian, uint16(caps>>16))
	hs.WriteByte(21)
	hs.Write(make([]byte, 10))
	hs.Write(f.scramble[8:])
	hs.WriteByte(0)
	hs.WriteString("mysql_native_password\x00")
	if writeMySQLPacket(conn, 0, hs.Bytes()) != nil {
		return
	}
	payload, err := readMySQLPacket(conn)
	if err != nil || len(payload) < 32 {
		return
	}
	rest := payload[32:] // capabilities, max packet, collation, filler
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return
	}
	login := handshakeLogin{User: string(rest[:end])}
	rest = rest[end+1:]
	if len(rest) > 0 && int(rest[0]) < 251 && len(rest) > int(rest[0]) {
		login.Auth = append([]byte(nil), rest[1:1+int(rest[0])]...)
	}
	f.mu.Lock()
	f.logins = append(f.logins, login)
	f.mu.Unlock()
	var denied bytes.Buffer
	denied.WriteByte(0xff)
	_ = binary.Write(&denied, binary.LittleEndian, uint16(1045))
	denied.WriteString("#28000Access denied for user '" + login.User + "' (fake)")
	_ = writeMySQLPacket(conn, 2, denied.Bytes())
}

func writeMySQLPacket(w io.Writer, seq byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	_, err := w.Write(append(header, payload...))
	return err
}

func readMySQLPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	payload := make([]byte, n)
	_, err := io.ReadFull(r, payload)
	return payload, err
}
