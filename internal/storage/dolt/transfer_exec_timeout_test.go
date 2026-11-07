package dolt

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// TestTransferExecTimeoutDuration verifies BEADS_DOLT_TRANSFER_TIMEOUT
// parsing (GH#6375): positive duration strings and bare seconds are honored;
// unset, invalid and non-positive values fall back to the compiled-in 5m
// default, the same ladder BEADS_CLI_TRANSFER_TIMEOUT already uses for the
// dolt-CLI transfer route.
func TestTransferExecTimeoutDuration(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unset", "", transferExecTimeout},
		{"duration string", "20m", 20 * time.Minute},
		{"bare seconds", "90", 90 * time.Second},
		{"whitespace is trimmed", " 15m ", 15 * time.Minute},
		{"invalid text", "soon", transferExecTimeout},
		{"zero falls back", "0", transferExecTimeout},
		{"negative falls back", "-5s", transferExecTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transferExecTimeoutEnv, tt.raw)
			if got := transferExecTimeoutDuration(); got != tt.want {
				t.Fatalf("transferExecTimeoutDuration() with %q = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// fakeTransferServer is a loopback MySQL server that speaks just enough of
// the wire protocol to carry a real go-sql-driver connection through login,
// transaction/session setup (BEGIN, SET, COMMIT) and prepared-statement
// execution — and then refuses to answer one chosen transfer statement. Every
// reply is an OK packet except the statement whose SQL contains target, whose
// reply is withheld until the peer goes away. That withheld silence is what a
// transfer read deadline has to survive: at base the one-shot connection's
// ReadTimeout is a hardcoded 5m, so nothing but the caller's context can end
// the wait (GH#6375). The handshake encoding follows the precedent of
// cmd/bd/dolt_handshake_fake_test.go; unlike that fake this one accepts the
// login and keeps serving commands.
type fakeTransferServer struct {
	ln net.Listener
	// target names the transfer statement (substring match on the SQL text)
	// whose reply is withheld.
	target string
	// scriptErrs maps statement substrings to error messages the fake answers
	// with instead of OK, letting a scripted statement fail fast the way
	// DOLT_PULL does when branch tracking is unconfigured.
	scriptErrs map[string]string
	// reached receives the withheld statement's SQL when the fake sees it.
	reached chan string

	connsMu sync.Mutex
	open    map[net.Conn]struct{}
	handled sync.WaitGroup
}

func newFakeTransferServer(t *testing.T, target string, scriptErrs map[string]string) *fakeTransferServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeTransferServer{
		ln:         ln,
		target:     target,
		scriptErrs: scriptErrs,
		reached:    make(chan string, 8),
		open:       make(map[net.Conn]struct{}),
	}
	go f.serve()
	t.Cleanup(f.close)
	return f
}

// dsn is the DSN a store uses to reach the fake.
func (f *fakeTransferServer) dsn() string {
	return "root@tcp(" + f.ln.Addr().String() + ")/beads"
}

func (f *fakeTransferServer) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.connsMu.Lock()
		f.open[conn] = struct{}{}
		f.connsMu.Unlock()
		f.handled.Add(1)
		go f.handle(conn)
	}
}

// close stops the listener, closes every accepted socket — releasing any
// handler parked withholding a reply — and waits for the handlers to finish.
func (f *fakeTransferServer) close() {
	_ = f.ln.Close()
	f.connsMu.Lock()
	for c := range f.open {
		_ = c.Close()
	}
	f.connsMu.Unlock()
	f.handled.Wait()
}

func (f *fakeTransferServer) handle(conn net.Conn) {
	defer func() {
		f.connsMu.Lock()
		delete(f.open, conn)
		f.connsMu.Unlock()
		_ = conn.Close()
		f.handled.Done()
	}()
	// Bound every fake exchange well above any deadline the tests configure;
	// a bug that leaks a wait past its test is cut off here, and t.Cleanup's
	// close still wins when the peer is gone.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// A mysql_native_password handshake, exactly like the login fake in
	// cmd/bd; the login response is consumed but never validated.
	var scramble [20]byte
	for i := range scramble {
		scramble[i] = byte(0x21 + i) // printable, never 0
	}
	if xferWritePacket(conn, 0, xferHandshake(scramble)) != nil {
		return
	}
	if _, seq, err := xferReadPacket(conn); err != nil || seq != 1 {
		return
	}
	if xferWritePacket(conn, 2, xferOKPacket()) != nil {
		return
	}

	stmts := make(map[uint32]string) // prepared statement id -> SQL text
	var nextStmtID uint32
	for {
		payload, seq, err := xferReadPacket(conn)
		if err != nil || len(payload) == 0 {
			return
		}
		switch payload[0] {
		case 0x01: // COM_QUIT
			return
		case 0x03: // COM_QUERY (SET/BEGIN/COMMIT and the dolt_remotes probe)
			if f.respond(conn, seq+1, string(payload[1:])) {
				return
			}
		case 0x16: // COM_STMT_PREPARE (every CALL with ? placeholders)
			query := string(payload[1:])
			nextStmtID++
			stmts[nextStmtID] = query
			n := strings.Count(query, "?")
			s := seq + 1
			if xferWritePacket(conn, s, xferPrepareOKPacket(nextStmtID, n)) != nil {
				return
			}
			s++
			for i := 0; i < n; i++ {
				if xferWritePacket(conn, s, xferParamDefPacket()) != nil {
					return
				}
				s++
			}
			if n > 0 {
				if xferWritePacket(conn, s, xferEOFPacket()) != nil {
					return
				}
			}
		case 0x17: // COM_STMT_EXECUTE
			if len(payload) < 5 {
				return
			}
			id := binary.LittleEndian.Uint32(payload[1:5])
			if f.respond(conn, seq+1, stmts[id]) {
				return
			}
		case 0x19, 0x1d: // COM_STMT_CLOSE / COM_STMT_RESET: no reply expected
		default:
			_ = xferWritePacket(conn, seq+1, xferOKPacket())
		}
	}
}

// respond answers one client statement with OK, a scripted error, or nothing
// at all. It returns true when the reply was withheld and the connection is
// now parked until the peer goes away.
func (f *fakeTransferServer) respond(conn net.Conn, seq byte, query string) bool {
	if strings.Contains(query, "dolt_remotes") {
		// The store's pool probing remotes (ListRemotes on the way to
		// isS3Remote): any refusal leaves isS3Remote false, the
		// credential-free path.
		_ = xferWritePacket(conn, seq, xferErrPacket(1146, "42S02", "table dolt_remotes doesn't exist (fake)"))
		return false
	}
	for frag, msg := range f.scriptErrs {
		if strings.Contains(query, frag) {
			_ = xferWritePacket(conn, seq, xferErrPacket(1, "HY000", msg))
			return false
		}
	}
	if strings.Contains(query, f.target) {
		select {
		case f.reached <- query:
		default:
		}
		_, _ = io.Copy(io.Discard, conn) // withhold: swallow bytes until the peer goes away
		return true
	}
	_ = xferWritePacket(conn, seq, xferOKPacket())
	return false
}

// xferHandshake builds a protocol-41 mysql_native_password handshake v10.
func xferHandshake(scramble [20]byte) []byte {
	// CLIENT_LONG_PASSWORD | CONNECT_WITH_DB | PROTOCOL_41 | TRANSACTIONS |
	// SECURE_CONNECTION | PLUGIN_AUTH
	const caps = 0x1 | 0x8 | 0x200 | 0x2000 | 0x8000 | 0x80000
	var hs bytes.Buffer
	hs.WriteByte(10)
	hs.WriteString("8.0.33-fake\x00")
	_ = binary.Write(&hs, binary.LittleEndian, uint32(1))
	hs.Write(scramble[:8])
	hs.WriteByte(0)
	_ = binary.Write(&hs, binary.LittleEndian, uint16(caps&0xffff))
	hs.WriteByte(0x21)
	_ = binary.Write(&hs, binary.LittleEndian, uint16(2))
	_ = binary.Write(&hs, binary.LittleEndian, uint16(caps>>16))
	hs.WriteByte(21)
	hs.Write(make([]byte, 10))
	hs.Write(scramble[8:])
	hs.WriteByte(0)
	hs.WriteString("mysql_native_password\x00")
	return hs.Bytes()
}

// xferOKPacket is an OK reply: zero affected rows / insert id, autocommit.
func xferOKPacket() []byte {
	return []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
}

// xferEOFPacket terminates a parameter-definition block.
func xferEOFPacket() []byte {
	return []byte{0xfe, 0x00, 0x00, 0x02, 0x00}
}

// xferErrPacket is an ERR reply: code, SQL state, message.
func xferErrPacket(code uint16, state, msg string) []byte {
	var b bytes.Buffer
	b.WriteByte(0xff)
	_ = binary.Write(&b, binary.LittleEndian, code)
	b.WriteString("#" + state + msg)
	return b.Bytes()
}

// xferPrepareOKPacket is COM_STMT_PREPARE's reply: no result-set columns,
// numParams parameter definitions to follow.
func xferPrepareOKPacket(stmtID uint32, numParams int) []byte {
	var b bytes.Buffer
	b.WriteByte(0x00)
	_ = binary.Write(&b, binary.LittleEndian, stmtID)
	_ = binary.Write(&b, binary.LittleEndian, uint16(0))
	_ = binary.Write(&b, binary.LittleEndian, uint16(numParams))
	b.WriteByte(0x00)
	_ = binary.Write(&b, binary.LittleEndian, uint16(0))
	return b.Bytes()
}

// xferParamDefPacket is one parameter-definition packet, the same shape as a
// column definition; the driver only counts these.
func xferParamDefPacket() []byte {
	var b bytes.Buffer
	for _, s := range []string{"def", "", "", "", "?", ""} {
		b.WriteByte(byte(len(s)))
		b.WriteString(s)
	}
	b.WriteByte(0x0c) // length of the fixed fields that follow
	b.Write([]byte{0x3f, 0x00})             // charset: binary
	b.Write([]byte{0x00, 0x00, 0x00, 0x00}) // column length
	b.WriteByte(0xfd)                       // type: VAR_STRING
	b.Write([]byte{0x00, 0x00})             // flags
	b.WriteByte(0x00)                       // decimals
	b.Write([]byte{0x00, 0x00})             // filler
	return b.Bytes()
}

func xferWritePacket(conn net.Conn, seq byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	_, err := conn.Write(append(header, payload...))
	return err
}

func xferReadPacket(conn net.Conn) (payload []byte, seq byte, err error) {
	var header [4]byte
	if _, err = io.ReadFull(conn, header[:]); err != nil {
		return nil, 0, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	payload = make([]byte, n)
	if _, err = io.ReadFull(conn, payload); err != nil {
		return nil, 0, err
	}
	return payload, header[3], nil
}

// TestTransferReadDeadlineBoundsTransfer proves the transfer read deadline
// knob reaches the connections that actually run server-side transfers
// (GH#6375). With BEADS_DOLT_TRANSFER_TIMEOUT=1s and a server that completes
// login and session setup but goes silent on the transfer statement, every
// SQL transfer path must fail on its own 1s read deadline — long before the
// 5s safety context — with the driver's invalid-connection sentinel in the
// error chain. At base the deadline is a hardcoded 5m, so it is the caller's
// context that expires instead and the sentinel never appears.
func TestTransferReadDeadlineBoundsTransfer(t *testing.T) {
	const safety = 5 * time.Second
	// The scripted pull error sends the initial DOLT_PULL down the GH#3144
	// fetch+merge fallback, the same way a database without branch tracking
	// does.
	const trackingErr = "fatal: did not specify a branch to pull"
	pull := func(ctx context.Context, s *DoltStore) error {
		_, err := s.pullWithAutoResolveUnchecked(ctx, "origin", "CALL DOLT_PULL(?, ?)", "origin", "main")
		return err
	}
	cases := []struct {
		name       string
		target     string
		scriptErrs map[string]string
		run        func(ctx context.Context, s *DoltStore) error
	}{
		{"sql push", "DOLT_PUSH", nil, func(ctx context.Context, s *DoltStore) error {
			return s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH(?, ?)", "origin", "main")
		}},
		{"federation push", "DOLT_PUSH", nil, func(ctx context.Context, s *DoltStore) error {
			return s.execWithLongTimeout(ctx, "CALL DOLT_PUSH(?, ?)", "peer", "main:main")
		}},
		{"federation fetch", "DOLT_FETCH", nil, func(ctx context.Context, s *DoltStore) error {
			return s.execWithLongTimeout(ctx, "CALL DOLT_FETCH(?)", "peer")
		}},
		{"initial pull", "DOLT_PULL", nil, pull},
		{"pull fallback fetch", "DOLT_FETCH", map[string]string{"DOLT_PULL": trackingErr}, pull},
		{"pull fallback merge", "DOLT_MERGE", map[string]string{"DOLT_PULL": trackingErr}, pull},
		{"tracking-ref refresh", "DOLT_FETCH", nil, func(ctx context.Context, s *DoltStore) error {
			return s.refreshTrackingRef(ctx, "origin")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(transferExecTimeoutEnv, "1s")
			fake := newFakeTransferServer(t, tc.target, tc.scriptErrs)
			pool, err := sql.Open("mysql", fake.dsn())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			s := &DoltStore{connStr: fake.dsn(), db: pool, branch: "main"}

			ctx, cancel := context.WithTimeout(context.Background(), safety)
			defer cancel()
			start := time.Now()
			err = tc.run(ctx, s)
			elapsed := time.Since(start)

			select {
			case got := <-fake.reached:
				if !strings.Contains(got, tc.target) {
					t.Fatalf("withheld statement %q does not name %q", got, tc.target)
				}
			default:
				t.Fatal("the transfer statement never reached the server")
			}
			if !errors.Is(err, mysql.ErrInvalidConn) {
				t.Fatalf("error = %v, want mysql.ErrInvalidConn in the chain (the read deadline firing)", err)
			}
			if ctx.Err() != nil {
				t.Fatalf("safety context expired after %v; the 1s transfer deadline did not bound the read", elapsed.Round(time.Millisecond))
			}
			if elapsed < 900*time.Millisecond {
				t.Fatalf("returned after only %v; the failure predates the 1s read deadline", elapsed.Round(time.Millisecond))
			}
		})
	}
}
