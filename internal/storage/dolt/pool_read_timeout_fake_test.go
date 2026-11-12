package dolt

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// The loopback leg of GH#5258's evidence. The scripted-driver tests in
// pool_read_timeout_policy_test.go pin the classification policy; this one
// proves it on the REAL pinned go-sql-driver/mysql over a real TCP socket,
// against a server that does exactly what a dolt server wedged behind the
// pool's per-I/O read deadline does from the wire: it completes login and
// transaction setup promptly, receives the SELECT — and then answers
// nothing. The client's DSN readTimeout (the same knob the store's
// poolReadDeadline mirrors) is what cuts the read off, so this test also
// pins the driver-level mechanics the classification's "possible" (not
// confirmed) verdict rests on: the read dies as ErrInvalidConn with the
// timeout identity already dropped, the socket is closed raw (no COM_QUIT),
// and the failure is terminal — the SELECT is never resubmitted at any
// layer, storage retry or database/sql reconnection.

const (
	// loopbackReadDeadline is the per-I/O read deadline under test, set in
	// BOTH the client DSN and the store's poolReadDeadline, as the real
	// pooled store's construction does (effectivePoolReadDeadline).
	loopbackReadDeadline = 200 * time.Millisecond
	// loopbackBound bounds every wait in the test: the withheld read is cut
	// off at loopbackReadDeadline by the driver, so nothing here may take
	// anywhere near loopbackBound.
	loopbackBound = 5 * time.Second
)

// poolReadLoopback is a TCP MySQL server speaking just enough protocol for
// the real client to log in and run one read transaction: a
// mysql_native_password handshake (the caps cmd/bd's dolt_handshake_fake_test
// pins), an OK for auth, OKs for transaction and housekeeping statements —
// and, for the first SELECT, a withhold: it records the statement and answers
// nothing until the client's own read deadline fires and the client closes
// the socket. Everything else a client sends is recorded as `other`, so a
// future driver or DSN change that issues unmodelled connect-time setup
// fails the test loudly instead of wedging it.
type poolReadLoopback struct {
	ln       net.Listener
	scramble [20]byte

	mu         sync.Mutex
	conns      int
	txBegins   int
	selects    []string
	other      []string
	withheldAt time.Time
	closeSeen  string
	closeOnce  sync.Once
	closed     chan struct{}
}

func newPoolReadLoopback(t *testing.T) *poolReadLoopback {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &poolReadLoopback{ln: ln, closed: make(chan struct{})}
	for i := range f.scramble {
		f.scramble[i] = byte(0x21 + i) // printable, never 0
	}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *poolReadLoopback) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *poolReadLoopback) serve() {
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

// clientClose waits for the client to close the withheld connection and
// returns how long the withhold lasted. The driver's readPacket failure path
// closes the socket raw, so the wait ends at the client's read deadline; a
// wait that outlives bound fails the test rather than hanging it.
func (f *poolReadLoopback) clientClose(t *testing.T, bound time.Duration) time.Duration {
	t.Helper()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-f.closed:
	case <-timer.C:
		t.Fatalf("the withheld SELECT was never answered and the client never closed the socket within %s", bound)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Since(f.withheldAt)
}

// snapshot returns the wire record: connections accepted, transactions
// begun, SELECTs submitted, and any statement the loopback does not model.
func (f *poolReadLoopback) snapshot() (conns, txBegins int, selects, other []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns, f.txBegins,
		append([]string(nil), f.selects...),
		append([]string(nil), f.other...)
}

func (f *poolReadLoopback) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.other = append(f.other, s)
}

func (f *poolReadLoopback) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(loopbackBound))

	// CLIENT_LONG_PASSWORD | CONNECT_WITH_DB | PROTOCOL_41 | TRANSACTIONS |
	// SECURE_CONNECTION | PLUGIN_AUTH
	const caps = 0x1 | 0x8 | 0x200 | 0x2000 | 0x8000 | 0x80000
	var hs bytes.Buffer
	hs.WriteByte(10)
	hs.WriteString("8.0.33-loopback\x00")
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
	if writeLoopPacket(conn, 0, hs.Bytes()) != nil {
		return
	}
	if _, err := readLoopPacket(conn); err != nil {
		f.note(fmt.Sprintf("handshake response failed: %v", err))
		return
	}
	// OK: header, no affected rows, no last-insert id, autocommit, no warnings.
	if writeLoopPacket(conn, 2, []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}) != nil {
		return
	}

	for {
		payload, err := readLoopPacket(conn)
		if err != nil {
			f.note(fmt.Sprintf("read failed outside the withhold: %v", err))
			return
		}
		if len(payload) == 0 {
			f.note("empty command packet")
			return
		}
		switch payload[0] {
		case 0x01: // COM_QUIT
			return
		case 0x03: // COM_QUERY
			query := string(payload[1:])
			switch {
			case query == "START TRANSACTION":
				f.mu.Lock()
				f.txBegins++
				f.mu.Unlock()
				if writeLoopPacket(conn, 1, []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}) != nil {
					return
				}
			case strings.HasPrefix(query, "SELECT"):
				f.mu.Lock()
				f.selects = append(f.selects, query)
				f.withheldAt = time.Now()
				f.mu.Unlock()
				// WITHHOLD: answer nothing. Block until the client's DSN
				// readTimeout fires and the driver closes the socket raw
				// (readPacket's failure path sends no COM_QUIT), which is the
				// bounded client EOF this test exists to pin.
				var one [1]byte
				n, err := conn.Read(one[:])
				f.mu.Lock()
				if n > 0 {
					f.closeSeen = fmt.Sprintf("client sent %d bytes during the withhold", n)
				} else if errors.Is(err, io.EOF) {
					f.closeSeen = "EOF"
				} else {
					f.closeSeen = fmt.Sprintf("%v", err)
				}
				f.mu.Unlock()
				f.closeOnce.Do(func() { close(f.closed) })
				return
			default:
				f.note("unexpected statement: " + query)
				if writeLoopPacket(conn, 1, []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}) != nil {
					return
				}
			}
		default:
			f.note(fmt.Sprintf("unexpected command %#x", payload[0]))
			return
		}
	}
}

func writeLoopPacket(w io.Writer, seq byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	_, err := w.Write(append(header, payload...))
	return err
}

func readLoopPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	payload := make([]byte, n)
	_, err := io.ReadFull(r, payload)
	return payload, err
}

func TestPoolReadTimeoutClosesSocketOnce(t *testing.T) {
	f := newPoolReadLoopback(t)

	// The real pinned driver against the loopback. maxAllowedPacket in the
	// DSN skips the connect-time SELECT @@max_allowed_packet (connector.go:
	// only an unset MaxAllowedPacket queries it), and no charset or arbitrary
	// params are passed, so the connection is ready the moment auth answers
	// OK — anything else shows up in the loopback's `other` record and fails
	// the assertions below.
	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%d)/beads?readTimeout=%s&maxAllowedPacket=67108864&interpolateParams=true",
		f.port(), loopbackReadDeadline)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{db: db, poolReadDeadline: loopbackReadDeadline}

	ctx, cancel := context.WithTimeout(context.Background(), loopbackBound)
	defer cancel()

	start := time.Now()
	txErr := store.withReadTx(ctx, func(tx *sql.Tx) error { return scanOneRow(ctx, tx) })
	elapsed := time.Since(start)

	if txErr == nil {
		t.Fatal("withReadTx succeeded against a server that withholds the SELECT")
	}
	if errors.Is(txErr, context.DeadlineExceeded) {
		t.Fatalf("the outer test deadline fired before the pool read deadline classified the failure: %v", txErr)
	}
	typed := readTimeoutFrom(t, txErr)
	if typed.Confirmed {
		t.Errorf("the driver leaves ErrInvalidConn (no retained timeout identity), so the classification must stay \"possible\": %v", txErr)
	}
	if typed.Timeout != loopbackReadDeadline {
		t.Errorf("typed.Timeout = %s, want the configured %s", typed.Timeout, loopbackReadDeadline)
	}
	assertReadTimeoutMessage(t, txErr, "read transaction", false, loopbackReadDeadline, "invalid connection")
	if elapsed < loopbackReadDeadline {
		t.Errorf("the read was cut off after %s, before the configured deadline %s fired", elapsed, loopbackReadDeadline)
	}
	if elapsed > loopbackBound {
		t.Errorf("the classified failure returned after %s, outside the %s bound: the deadline, not the outer context, must end the read", elapsed, loopbackBound)
	}

	// Bounded client EOF: the driver closed the socket at the deadline.
	withheld := f.clientClose(t, loopbackBound)
	if withheld < loopbackReadDeadline {
		t.Errorf("the client closed the withheld connection after only %s, before its read deadline %s", withheld, loopbackReadDeadline)
	}
	if withheld > loopbackBound {
		t.Errorf("the withhold lasted %s, past the %s bound", withheld, loopbackBound)
	}
	f.mu.Lock()
	closeSeen := f.closeSeen
	f.mu.Unlock()
	if closeSeen != "EOF" {
		t.Errorf("the withheld connection's client close was recorded as %q, want a clean EOF (the driver closes the socket raw, no COM_QUIT)", closeSeen)
	}

	// No resubmission at any layer: one connection, one transaction, exactly
	// one SELECT, and no unmodelled statements.
	conns, txBegins, selects, other := f.snapshot()
	if conns != 1 {
		t.Errorf("the client opened %d connections, want 1: a resubmission via database/sql reconnection or a storage retry both show up here", conns)
	}
	if txBegins != 1 {
		t.Errorf("START TRANSACTION submitted %d times, want 1", txBegins)
	}
	if len(selects) != 1 || selects[0] != "SELECT 1" {
		t.Errorf("SELECT submitted as %v, want exactly one \"SELECT 1\": the typed failure must not be replayed against the same deadline", selects)
	}
	if len(other) > 0 {
		t.Errorf("the session produced statements the loopback does not model: %v", other)
	}
}
