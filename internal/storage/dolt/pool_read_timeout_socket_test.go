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

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

// withholdingMySQL is a loopback server that speaks enough of the MySQL
// protocol for the real driver to log in (mysql_native_password, no
// password) and run setup statements, each answered OK. It never answers a
// SELECT: the connection that sent one is held open, and the server records
// when the client closes it. It reproduces a server that accepted a query and
// went quiet, without reproducing why (GH#5258).
type withholdingMySQL struct {
	ln net.Listener

	mu      sync.Mutex
	selects []string
	// clientClosed receives once per withheld SELECT whose connection the
	// client closed.
	clientClosed chan struct{}
}

func newWithholdingMySQL(t *testing.T) *withholdingMySQL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &withholdingMySQL{ln: ln, clientClosed: make(chan struct{}, 16)}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *withholdingMySQL) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

// Selects returns the SELECT statements the server received.
func (f *withholdingMySQL) Selects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.selects...)
}

func (f *withholdingMySQL) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *withholdingMySQL) handle(conn net.Conn) {
	defer conn.Close()
	// Bound a leaked connection; the test itself finishes in seconds.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	// CLIENT_LONG_PASSWORD | PROTOCOL_41 | TRANSACTIONS | SECURE_CONNECTION |
	// PLUGIN_AUTH
	const caps = 0x1 | 0x200 | 0x2000 | 0x8000 | 0x80000
	scramble := []byte("abcdefghijklmnopqrst")
	var hs bytes.Buffer
	hs.WriteByte(10)
	hs.WriteString("8.0.33-withholding-fake\x00")
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
	if writeFakeMySQLPacket(conn, 0, hs.Bytes()) != nil {
		return
	}
	if _, err := readFakeMySQLPacket(conn); err != nil {
		return
	}
	if writeFakeMySQLPacket(conn, 2, fakeMySQLOK()) != nil {
		return
	}
	for {
		payload, err := readFakeMySQLPacket(conn)
		if err != nil || len(payload) == 0 {
			return
		}
		switch payload[0] {
		case 0x01: // COM_QUIT
			return
		case 0x03: // COM_QUERY
			query := string(payload[1:])
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") {
				f.mu.Lock()
				f.selects = append(f.selects, query)
				f.mu.Unlock()
				// Withhold the answer until the client gives up on the socket.
				_, _ = io.Copy(io.Discard, conn)
				f.clientClosed <- struct{}{}
				return
			}
		}
		if writeFakeMySQLPacket(conn, 1, fakeMySQLOK()) != nil {
			return
		}
	}
}

// fakeMySQLOK is an OK packet: no rows affected, no insert id, autocommit
// status, no warnings.
func fakeMySQLOK() []byte { return []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00} }

func writeFakeMySQLPacket(w io.Writer, seq byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	_, err := w.Write(append(header, payload...))
	return err
}

func readFakeMySQLPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	payload := make([]byte, n)
	_, err := io.ReadFull(r, payload)
	return payload, err
}

// TestPoolReadTimeoutClosesSocketOnce drives the real pinned MySQL driver
// through withReadTx against a server that completes login and the read
// transaction's setup but never answers the SELECT. With a 200ms pool read
// deadline the driver closes that connection, and the store must stop there:
// one SELECT reaches the server, not one per retry (GH#5258), and the caller
// gets the typed, unconfirmed read-timeout error.
func TestPoolReadTimeoutClosesSocketOnce(t *testing.T) {
	const readDeadline = 200 * time.Millisecond
	f := newWithholdingMySQL(t)
	cfg := &Config{ServerHost: "127.0.0.1", ServerPort: f.port(), ServerUser: "root", PoolReadTimeout: readDeadline}
	dsn := buildServerDSN(cfg, "")
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &DoltStore{db: db, connStr: dsn, serverMode: true, poolReadTimeout: poolReadTimeoutFromDSN(dsn)}

	// Generous next to the deadline, short next to the retry budget (30s),
	// so a replaying store fails here on its SELECT count, not on time.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = s.withReadTx(ctx, func(tx *sql.Tx) error {
		var v int
		if err := tx.QueryRowContext(ctx, "SELECT 1").Scan(&v); err != nil {
			return fmt.Errorf("withheld select: %w", err)
		}
		return nil
	})

	if got := f.Selects(); len(got) != 1 {
		t.Errorf("server received %d SELECTs, want 1 (a timed-out read is not resubmitted): %q", len(got), got)
	}
	select {
	case <-f.clientClosed:
	case <-time.After(2 * time.Second):
		t.Error("client did not close the connection whose SELECT timed out")
	}

	var rte *storage.ReadTimeoutError
	if !errors.As(err, &rte) {
		t.Fatalf("withReadTx error = %v (%T), want *storage.ReadTimeoutError", err, err)
	}
	if rte.Confirmed {
		t.Errorf("Confirmed = true; the driver's invalid connection only suggests a deadline: %v", err)
	}
	if rte.Timeout != readDeadline {
		t.Errorf("Timeout = %s, want the DSN's %s", rte.Timeout, readDeadline)
	}
	if !errors.Is(err, mysql.ErrInvalidConn) {
		t.Errorf("error %v does not unwrap to mysql.ErrInvalidConn", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "withheld select") || !strings.Contains(msg, "possible pool read timeout (configured 200ms)") {
		t.Errorf("error %q lacks the operation or the possible-timeout diagnosis", msg)
	}
}
