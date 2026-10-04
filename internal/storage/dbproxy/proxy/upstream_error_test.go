package proxy

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// serveThroughProxy runs handleConn for every connection accepted on a fresh
// loopback listener and returns its address. reportOutage stands in for an
// external backend (the fakes here are not *server.ExternalDoltServer).
func serveThroughProxy(t *testing.T, backend tcpBlackholeBackend, reportOutage bool) string {
	t.Helper()
	p := NewProxyServer(ProxyOpts{Server: backend, Stats: &Stats{}})
	p.logger = log.Default()
	p.reportUpstreamOutage = reportOutage
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = p.handleConn(context.Background(), conn) }()
		}
	}()
	return ln.Addr().String()
}

// pingThroughProxy pings the proxy with the real MySQL driver and returns the
// error and how long it took.
func pingThroughProxy(t *testing.T, addr string) (time.Duration, error) {
	t.Helper()
	db, err := sql.Open("mysql", "root@tcp("+addr+")/?timeout=5s")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err = db.PingContext(ctx)
	return time.Since(start), err
}

func requireUpstreamUnreachable(t *testing.T, err error, elapsed time.Duration, wantInMessage string) {
	t.Helper()
	var me *mysql.MySQLError
	if !errors.As(err, &me) {
		t.Fatalf("ping error = %v (%T), want *mysql.MySQLError", err, err)
	}
	if me.Number != upstreamUnreachableErrno || string(me.SQLState[:]) != upstreamUnreachableSQLState {
		t.Fatalf("MySQL error = %d/%s, want %d/%s", me.Number, me.SQLState, upstreamUnreachableErrno, upstreamUnreachableSQLState)
	}
	if !strings.Contains(me.Message, "upstream Dolt server unreachable") || !strings.Contains(me.Message, wantInMessage) {
		t.Fatalf("message %q lacks upstream context %q", me.Message, wantInMessage)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("unreachable upstream took %s to report", elapsed)
	}
}

// A refused upstream (nothing listening) is answered with a MySQL error the
// driver reports as permanent, instead of a bare close the uow bootstrap
// would retry for 30s.
func TestHandleConnRefusedUpstreamAnswersWithMySQLError(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()

	addr := serveThroughProxy(t, tcpBlackholeBackend{address: deadAddr}, true)
	elapsed, err := pingThroughProxy(t, addr)
	requireUpstreamUnreachable(t, err, elapsed, "connection refused")
}

// A front that accepts and immediately closes (its own upstream is gone)
// never sends the greeting; the proxy names the endpoint instead of letting
// the client see an unexplained EOF.
func TestHandleConnUpstreamClosingBeforeGreetingAnswersWithMySQLError(t *testing.T) {
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = front.Close() })
	go func() {
		for {
			conn, err := front.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	addr := serveThroughProxy(t, tcpBlackholeBackend{address: front.Addr().String()}, true)
	elapsed, err := pingThroughProxy(t, addr)
	requireUpstreamUnreachable(t, err, elapsed, front.Addr().String())
}

// A backend that spoke and then dropped the connection is not an unreachable
// upstream: the client must see exactly the backend's bytes and a plain EOF,
// so the bootstrap ping still treats the drop as transient (#6003).
func TestHandleConnUpstreamDropAfterGreetingStaysPlainClose(t *testing.T) {
	greeting := []byte("\x01\x00\x00\x00\x0a")
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = front.Close() })
	go func() {
		conn, err := front.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write(greeting)
		_ = conn.Close()
	}()

	addr := serveThroughProxy(t, tcpBlackholeBackend{address: front.Addr().String()}, true)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(greeting) {
		t.Fatalf("client received %q, want only the backend's %q", got, greeting)
	}
}

// A managed (non-external) backend keeps the bare close on a refused dial, so
// the client's bootstrap retry still covers a sidecar that is refused
// transiently.
func TestHandleConnManagedBackendRefusalStaysPlainClose(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()
	if reportsUpstreamOutage(tcpBlackholeBackend{address: deadAddr}) {
		t.Fatal("a non-external backend must not report upstream outages")
	}

	addr := serveThroughProxy(t, tcpBlackholeBackend{address: deadAddr}, false)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("managed backend refusal sent %q, want a bare close", got)
	}
}

// requireUpstreamErrorPacket asserts that got is exactly one MySQL ERR packet
// carrying the proxy's upstream-unreachable error.
func requireUpstreamErrorPacket(t *testing.T, got []byte) {
	t.Helper()
	if len(got) < 13 {
		t.Fatalf("response %q is too short for an ERR packet", got)
	}
	payloadLen := int(got[0]) | int(got[1])<<8 | int(got[2])<<16
	if payloadLen != len(got)-4 || got[3] != 0 || got[4] != 0xff {
		t.Fatalf("response is not a single sequence-0 ERR packet: % x", got)
	}
	if errno := int(got[5]) | int(got[6])<<8; errno != upstreamUnreachableErrno {
		t.Fatalf("errno = %d, want %d", errno, upstreamUnreachableErrno)
	}
	if string(got[7:13]) != "#"+upstreamUnreachableSQLState {
		t.Fatalf("SQL state marker = %q, want #%s", got[7:13], upstreamUnreachableSQLState)
	}
	if msg := string(got[13:]); !strings.Contains(msg, "upstream Dolt server unreachable") {
		t.Fatalf("message %q lacks upstream context", msg)
	}
}

func TestUpstreamErrorPacketTruncatesMessage(t *testing.T) {
	pkt := upstreamErrorPacket(strings.Repeat("x", 4*upstreamErrorMaxMessage))
	payloadLen := int(pkt[0]) | int(pkt[1])<<8 | int(pkt[2])<<16
	if payloadLen != len(pkt)-4 || payloadLen != 9+upstreamErrorMaxMessage {
		t.Fatalf("payload length %d (packet %d), want %d", payloadLen, len(pkt), 9+upstreamErrorMaxMessage)
	}
	if pkt[3] != 0 || pkt[4] != 0xff || pkt[7] != '#' {
		t.Fatalf("malformed ERR packet header % x", pkt[:8])
	}
}
