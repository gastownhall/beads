package proxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
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
	if !IsUpstreamOutageError(err) || string(me.SQLState[:]) != upstreamErrorSQLState {
		t.Fatalf("MySQL error = %d/%s %q, want the proxy's %d/%s upstream report", me.Number, me.SQLState, me.Message, UpstreamErrorNumber, upstreamErrorSQLState)
	}
	if !strings.Contains(me.Message, wantInMessage) {
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
	if !strings.Contains(err.Error(), "closed the connection before the MySQL greeting (down, restarting, or at its connection limit)") {
		t.Fatalf("zero-byte close must not be reported as a proven outage: %v", err)
	}
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
	if errno := int(got[5]) | int(got[6])<<8; errno != UpstreamErrorNumber {
		t.Fatalf("errno = %d, want %d", errno, UpstreamErrorNumber)
	}
	if string(got[7:13]) != "#"+upstreamErrorSQLState {
		t.Fatalf("SQL state marker = %q, want #%s", got[7:13], upstreamErrorSQLState)
	}
	if msg := string(got[13:]); !strings.HasPrefix(msg, UpstreamErrorPrefix+"upstream Dolt server") {
		t.Fatalf("message %q lacks the proxy prefix and upstream context", msg)
	}
}

// External backends enable the outage report through NewProxyServer; the
// handleConn tests above set the flag by hand on fakes.
func TestNewProxyServerReportsOutagesForExternalBackendsOnly(t *testing.T) {
	ext, err := server.NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	if !NewProxyServer(ProxyOpts{Server: ext}).reportUpstreamOutage {
		t.Fatal("an external backend must report upstream outages")
	}
	if NewProxyServer(ProxyOpts{Server: blockingBackend{}}).reportUpstreamOutage {
		t.Fatal("a non-external backend must not report upstream outages")
	}
}

// The dial report names network, address and reason, without the Go
// call-site prefixes ExternalDoltServer.Dial wraps the error in.
func TestDialFailureMessageDropsGoCallSites(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(dead.Addr().String())
	_ = dead.Close()
	port, _ := strconv.Atoi(portStr)
	ext, err := server.NewExternalDoltServer(configfile.ExternalDoltConfig{Host: host, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	_, dialErr := ext.Dial(context.Background())
	if dialErr == nil || !isUpstreamUnreachableDialError(dialErr) {
		t.Fatalf("dial error = %v, want a refusal", dialErr)
	}
	got := dialFailureMessage(dialErr)
	want := "upstream Dolt server unreachable: dial tcp " + dead.Addr().String() + ": connection refused"
	if got != want {
		t.Fatalf("dialFailureMessage = %q, want %q", got, want)
	}
}

func TestIsUpstreamOutageErrorRequiresTheProxyPrefix(t *testing.T) {
	if IsUpstreamOutageError(&mysql.MySQLError{Number: UpstreamErrorNumber, Message: "Can't connect to MySQL server"}) {
		t.Fatal("a 2003 without the proxy prefix is not the proxy's report")
	}
	if IsUpstreamOutageError(&mysql.MySQLError{Number: 1045, Message: UpstreamErrorPrefix + "x"}) {
		t.Fatal("only 2003 carries the proxy's report")
	}
	if !IsUpstreamOutageError(fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: UpstreamErrorNumber, Message: UpstreamErrorPrefix + "x"})) {
		t.Fatal("a wrapped proxy report must be recognized")
	}
}

func TestUpstreamErrorPacketTruncatesOnARuneBoundary(t *testing.T) {
	// "é" is two bytes; an odd prefix puts the byte limit mid-rune.
	pkt := upstreamErrorPacket("x" + strings.Repeat("é", upstreamErrorMaxMessage))
	if msg := pkt[13:]; !utf8.Valid(msg) || len(msg) > upstreamErrorMaxMessage {
		t.Fatalf("truncated message is %d bytes, valid UTF-8 = %v", len(msg), utf8.Valid(msg))
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
