package pool

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/sqltypes"
)

// backend is one persistent connection to Dolt, held across many client
// sessions. The win of the pooler is that Dolt sees a fixed set of these
// regardless of how many short-lived clients connect and disconnect.
//
// A backend may be borrowed (pinned to a client connection) or idle (in the
// pool). The keepalive loop must be able to ping EITHER without racing the
// client's queries, so all wire I/O on a backend is serialized by mu. The
// client handler and the keepalive both take mu for the duration of one
// command/ping, which keeps a long-idle BORROWED backend alive (a client conn
// that go-sql-driver holds open but isn't actively querying) without ever
// dropping its session state.
type backend struct {
	id int

	mu    sync.Mutex
	conn  *mysql.Conn // vitess client conn; full handshake done once at warmup
	curDB string      // currently-USEd database, so we only switch when needed
}

const resetIOTimeout = 5 * time.Second

// connParams builds vitess ConnParams for dialing Dolt with NO default
// database. Connecting with no DB means COM_RESET_CONNECTION restores the
// backend to a clean "no DB" state; the pooler applies each client's database
// per checkout via ComInitDB.
func connParams(cfg Config) (*mysql.ConnParams, error) {
	cp := &mysql.ConnParams{Uname: cfg.User}
	if cfg.Socket != "" {
		cp.UnixSocket = cfg.Socket
		return cp, nil
	}
	host, port, err := splitHostPort(cfg.Addr)
	if err != nil {
		return nil, err
	}
	cp.Host = host
	cp.Port = port
	return cp, nil
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("pool: parse addr %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("pool: parse port in %q: %w", addr, err)
	}
	return host, port, nil
}

func (b *backend) close() {
	if b != nil && b.conn != nil {
		b.conn.Close()
	}
}

// exec runs a text query on the backend, serialized against keepalive pings.
func (b *backend) exec(query string, maxrows int) (*sqltypes.Result, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn.ExecuteFetch(query, maxrows, true)
}

// ping checks backend liveness, serialized against client queries. Used by the
// keepalive loop and the borrow-time liveness check.
func (b *backend) ping() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn.Ping()
}

// useDB ensures the backend's default database matches the client's request.
// It is a no-op when the database is already current, which keeps the common
// case (all clients on the same DB) free of redundant USE round-trips.
func (b *backend) useDB(db string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if db == "" || db == b.curDB {
		return nil
	}
	if _, err := b.conn.ExecuteFetch("USE `"+escapeIdent(db)+"`", 1, false); err != nil {
		return err
	}
	b.curDB = db
	return nil
}

func escapeIdent(s string) string {
	return strings.ReplaceAll(s, "`", "``")
}

// reset clears all session state so the backend is safe for the next client.
//
// It sends a raw COM_RESET_CONNECTION (0x1f) packet directly on the underlying
// net.Conn. The vitess client Conn does not expose a method to send this, but
// its `.Conn` (net.Conn) field is exported. Empirically Dolt's
// COM_RESET_CONNECTION clears: default DB (-> connect-time DB, i.e. none here),
// user vars, session vars (autocommit), AND the Dolt active branch — fully
// restoring a clean session.
//
// Only safe to call at a session boundary with no ExecuteFetch in flight, i.e.
// when vitess's read buffer for this conn is fully drained.
func (b *backend) reset() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	nc := b.conn.Conn
	// COM_RESET_CONNECTION: 4-byte header (len=1, seq=0) + 1-byte payload 0x1f.
	pkt := []byte{0x01, 0x00, 0x00, 0x00, 0x1f}
	if err := nc.SetWriteDeadline(time.Now().Add(resetIOTimeout)); err != nil {
		return err
	}
	if _, err := nc.Write(pkt); err != nil {
		return err
	}
	if err := nc.SetReadDeadline(time.Now().Add(resetIOTimeout)); err != nil {
		return err
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(nc, hdr); err != nil {
		return fmt.Errorf("read reset reply header: %w", err)
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	body := make([]byte, n)
	if _, err := io.ReadFull(nc, body); err != nil {
		return fmt.Errorf("read reset reply body: %w", err)
	}
	_ = nc.SetReadDeadline(time.Time{})
	_ = nc.SetWriteDeadline(time.Time{})
	if len(body) > 0 && body[0] == 0xff {
		msg := ""
		if len(body) > 3 {
			msg = string(body[3:])
		}
		return fmt.Errorf("COM_RESET_CONNECTION error: %s", msg)
	}
	b.curDB = "" // reset restored to connect-time DB (none)
	return nil
}
