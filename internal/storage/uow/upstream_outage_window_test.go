package uow

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

func proxyOutageErr() error {
	return &mysql.MySQLError{Number: proxy.UpstreamErrorNumber, Message: proxy.UpstreamErrorPrefix + "upstream Dolt server unreachable: dial tcp 127.0.0.1:1: connection refused"}
}

func shortOutageWindow(t *testing.T, d time.Duration) {
	t.Helper()
	prev := upstreamOutageRetryWindow
	upstreamOutageRetryWindow = d
	t.Cleanup(func() { upstreamOutageRetryWindow = prev })
}

func TestPingWithRetryRetriesTheProxyOutageReportOnlyBriefly(t *testing.T) {
	t.Run("upstream back inside the window recovers", func(t *testing.T) {
		shortOutageWindow(t, 2*time.Second)
		p := &fakePinger{t: t, errs: []error{proxyOutageErr(), proxyOutageErr(), nil}}
		if err := pingWithRetry(context.Background(), p, testPingBackOff(), testPingAttemptTimeout); err != nil {
			t.Fatalf("pingWithRetry() error = %v, want recovery inside the outage window", err)
		}
		if p.calls != 3 {
			t.Fatalf("PingContext called %d times, want 3", p.calls)
		}
	})

	t.Run("outage for the whole window fails at its end", func(t *testing.T) {
		const window = 300 * time.Millisecond
		shortOutageWindow(t, window)
		var calls atomic.Int32
		p := pingerFunc(func(context.Context) error { calls.Add(1); return proxyOutageErr() })
		bo := testPingBackOff()
		bo.InitialInterval = 50 * time.Millisecond
		bo.MaxElapsedTime = 30 * time.Second // the full bootstrap budget must not apply
		start := time.Now()
		err := pingWithRetry(context.Background(), p, bo, testPingAttemptTimeout)
		elapsed := time.Since(start)
		if !proxy.IsUpstreamOutageError(err) {
			t.Fatalf("pingWithRetry() error = %v, want the proxy's outage report", err)
		}
		if elapsed < window || elapsed > window+500*time.Millisecond {
			t.Fatalf("gave up after %s, want just past the %s window", elapsed, window)
		}
		if calls.Load() < 2 {
			t.Fatalf("PingContext called %d times, want the report retried inside the window", calls.Load())
		}
	})

	t.Run("other transient errors keep the full budget", func(t *testing.T) {
		shortOutageWindow(t, time.Nanosecond)
		p := &fakePinger{t: t, errs: []error{mysql.ErrInvalidConn, mysql.ErrInvalidConn, nil}}
		if err := pingWithRetry(context.Background(), p, testPingBackOff(), testPingAttemptTimeout); err != nil {
			t.Fatalf("pingWithRetry() error = %v, want nil", err)
		}
	})
}

// TestPingWithRetryKeepsBackingOffAfterTheOutageWindowForOtherTransientErrors
// keeps the contributor's regression name (PR #7196): one upstream-outage
// report arms the outage window, and every later attempt fails with an
// ordinary transient error. Those later waits must follow the wrapped
// exponential backoff; before the fix each was clamped to the now-past
// outage deadline — zero — and the retry spun at CPU rate until the rest of
// the budget was spent (#7183). MaxInterval is pinned below the budget so
// only Stop can end a zero-wait loop: an interval allowed to grow past the
// budget would stop after a few immediate calls and pass a loose count even
// against the bug.
func TestPingWithRetryKeepsBackingOffAfterTheOutageWindowForOtherTransientErrors(t *testing.T) {
	shortOutageWindow(t, 10*time.Millisecond)
	var calls atomic.Int64
	p := pingerFunc(func(context.Context) error {
		if calls.Add(1) == 1 {
			return proxyOutageErr()
		}
		return mysql.ErrInvalidConn
	})
	bo := testPingBackOff()
	bo.RandomizationFactor = 0
	bo.InitialInterval = 20 * time.Millisecond
	bo.MaxInterval = 50 * time.Millisecond
	bo.MaxElapsedTime = 500 * time.Millisecond
	err := pingWithRetry(context.Background(), p, bo, testPingAttemptTimeout)
	if !errors.Is(err, mysql.ErrInvalidConn) {
		t.Fatalf("pingWithRetry() error = %v, want the invalid-connection sentinel", err)
	}
	if n := calls.Load(); n > 30 {
		t.Fatalf("PingContext called %d times in a 500ms budget, want a backoff (<= 30 calls)", n)
	}
}

// TestPingWithRetryOrdinaryTransientErrorInsideTheWindowKeepsItsOwnInterval:
// an ordinary transient error between the outage report and recovery must
// wait the wrapped policy's own next interval. Before the fix that wait was
// clamped to the outage window's remainder (~100ms here) instead of the
// policy's 300ms second interval.
func TestPingWithRetryOrdinaryTransientErrorInsideTheWindowKeepsItsOwnInterval(t *testing.T) {
	shortOutageWindow(t, 300*time.Millisecond)
	var at []time.Time
	p := pingerFunc(func(context.Context) error {
		at = append(at, time.Now())
		switch len(at) {
		case 1:
			return proxyOutageErr()
		case 2:
			return mysql.ErrInvalidConn
		default:
			return nil
		}
	})
	bo := testPingBackOff()
	bo.RandomizationFactor = 0
	bo.InitialInterval = 200 * time.Millisecond
	// The default 1.5 multiplier makes the second interval 300ms.
	bo.MaxElapsedTime = 30 * time.Second
	if err := pingWithRetry(context.Background(), p, bo, testPingAttemptTimeout); err != nil {
		t.Fatalf("pingWithRetry() error = %v, want nil", err)
	}
	if len(at) != 3 {
		t.Fatalf("PingContext called %d times, want 3", len(at))
	}
	if gap := at[2].Sub(at[1]); gap < 250*time.Millisecond {
		t.Fatalf("wait after the ordinary transient error was %s, want the policy's own ~300ms interval", gap)
	}
}

type pingerFunc func(context.Context) error

func (f pingerFunc) PingContext(ctx context.Context) error { return f(ctx) }

// TestOpenDBRidesOutAnUpstreamThatReturnsInsideTheOutageWindow plays the db
// proxy in front of an external upstream that is briefly unreachable: the
// first connections get the proxy's real outage packet, and 300ms after the
// first one the upstream "comes back" (a minimal MySQL server answers). One
// openDB call — one bd command's bootstrap — must succeed without the caller
// retrying.
func TestOpenDBRidesOutAnUpstreamThatReturnsInsideTheOutageWindow(t *testing.T) {
	front := newFlappingFront(t, 300*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	db, err := openDB(ctx, "root@tcp("+front.addr+")/?timeout=5s")
	if err != nil {
		t.Fatalf("openDB() error = %v, want recovery once the upstream returned", err)
	}
	_ = db.Close()
	if front.refused.Load() == 0 {
		t.Fatal("the command never hit the outage; the test proved nothing")
	}
	if elapsed := time.Since(start); elapsed > upstreamOutageRetryWindow+time.Second {
		t.Fatalf("recovery took %s", elapsed)
	}
}

func TestOpenDBFailsFastWhenTheUpstreamStaysUnreachable(t *testing.T) {
	front := newFlappingFront(t, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	_, err := openDB(ctx, "root@tcp("+front.addr+")/?timeout=5s")
	elapsed := time.Since(start)
	if !proxy.IsUpstreamOutageError(err) {
		t.Fatalf("openDB() error = %v, want the proxy's outage report", err)
	}
	if elapsed > upstreamOutageRetryWindow+time.Second {
		t.Fatalf("an unreachable upstream took %s to fail, want about %s", elapsed, upstreamOutageRetryWindow)
	}
}

// flappingFront answers with the db proxy's outage packet until downFor has
// passed since its first connection, then serves as a minimal MySQL server.
type flappingFront struct {
	addr    string
	refused atomic.Int32
}

func newFlappingFront(t *testing.T, downFor time.Duration) *flappingFront {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &flappingFront{addr: ln.Addr().String()}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	var once sync.Once
	var upAt time.Time
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			once.Do(func() { upAt = time.Now().Add(downFor) })
			if time.Now().Before(upAt) {
				f.refused.Add(1)
				_, _ = conn.Write(proxy.UpstreamOutagePacket("upstream Dolt server unreachable: dial tcp 127.0.0.1:1: connection refused"))
				_ = conn.Close()
				continue
			}
			wg.Add(1)
			go func() { defer wg.Done(); serveMinimalMySQL(conn) }()
		}
	}()
	return f
}

// serveMinimalMySQL speaks just enough of the server side of the MySQL
// protocol for go-sql-driver to connect and ping: a greeting, OK to any
// authentication, and OK to every command until COM_QUIT or EOF.
func serveMinimalMySQL(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	const (
		clientLongPassword = 1 << 0
		clientProtocol41   = 1 << 9
		clientTransactions = 1 << 13
		clientSecureConn   = 1 << 15
		clientPluginAuth   = 1 << 19
	)
	caps := uint32(clientLongPassword | clientProtocol41 | clientTransactions | clientSecureConn | clientPluginAuth)
	g := []byte{0x0a}
	g = append(g, "8.0.0-fake\x00"...)
	g = binary.LittleEndian.AppendUint32(g, 1)
	g = append(g, "abcdefgh"...) // auth data part 1
	g = append(g, 0)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps))
	g = append(g, 0x21) // utf8_general_ci
	g = binary.LittleEndian.AppendUint16(g, 0x0002)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps>>16))
	g = append(g, 21)
	g = append(g, make([]byte, 10)...)
	g = append(g, "ijklmnopqrst\x00"...) // auth data part 2
	g = append(g, "mysql_native_password\x00"...)
	ok := []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}

	write := func(seq byte, payload []byte) error {
		hdr := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
		_, err := conn.Write(append(hdr, payload...))
		return err
	}
	read := func() (byte, []byte, error) {
		var hdr [4]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return 0, nil, err
		}
		body := make([]byte, int(hdr[0])|int(hdr[1])<<8|int(hdr[2])<<16)
		_, err := io.ReadFull(conn, body)
		return hdr[3], body, err
	}

	if write(0, g) != nil {
		return
	}
	seq, _, err := read() // handshake response
	if err != nil || write(seq+1, ok) != nil {
		return
	}
	for {
		seq, body, err := read()
		if err != nil || len(body) == 0 || body[0] == 0x01 { // EOF or COM_QUIT
			return
		}
		if write(seq+1, ok) != nil {
			return
		}
	}
}
