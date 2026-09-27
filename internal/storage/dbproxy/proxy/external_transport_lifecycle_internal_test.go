package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// unixSocketPath returns a bindable Unix socket path. t.TempDir() embeds the
// full test name, which on macOS sits under a long /var/folders/... root and
// overflows the ~104-byte sun_path limit, so bind fails with "invalid
// argument". A short unnamed temp dir stays well inside the limit.
func unixSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bdproxy")
	if err != nil {
		t.Fatalf("temp dir for unix socket: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "u.sock")
}

// externalTransportFixture is a deliberately small real TCP/Unix listener
// used to exercise the production proxy transport. It withholds all bytes in
// blackhole mode and echoes bytes in echo mode. The open set lets tests prove
// that the proxy closes the upstream socket instead of relying on fixture
// cleanup to hide a leak.
type externalTransportFixture struct {
	t         *testing.T
	network   string
	address   string
	listener  net.Listener
	blackhole bool

	mu        sync.Mutex
	open      map[net.Conn]struct{}
	accepted  chan net.Conn
	doneCh    chan struct{}
	closeOnce sync.Once
}

func newExternalTransportFixture(t *testing.T, network, address string, blackhole bool) *externalTransportFixture {
	t.Helper()
	if network == "unix" {
		_ = os.Remove(address)
	}
	ln, err := net.Listen(network, address)
	if err != nil {
		t.Fatalf("listen upstream %s %q: %v", network, address, err)
	}
	f := &externalTransportFixture{
		t:         t,
		network:   network,
		address:   ln.Addr().String(),
		listener:  ln,
		blackhole: blackhole,
		open:      make(map[net.Conn]struct{}),
		accepted:  make(chan net.Conn, 16),
		doneCh:    make(chan struct{}),
	}
	go f.acceptLoop()
	t.Cleanup(func() { f.Close() })
	return f
}

func (f *externalTransportFixture) acceptLoop() {
	defer close(f.doneCh)
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.open[conn] = struct{}{}
		f.mu.Unlock()
		select {
		case f.accepted <- conn:
		default:
			_ = conn.Close()
		}
		go f.serve(conn)
	}
}

func (f *externalTransportFixture) serve(conn net.Conn) {
	defer func() {
		f.mu.Lock()
		delete(f.open, conn)
		f.mu.Unlock()
		_ = conn.Close()
	}()
	if f.blackhole {
		// Reading, without writing a protocol response, models an upstream
		// that accepted the transport but never completes a SQL handshake.
		var b [1]byte
		_, _ = conn.Read(b[:])
		return
	}
	_, _ = io.Copy(conn, conn)
}

func (f *externalTransportFixture) nextAccepted(timeout time.Duration) net.Conn {
	f.t.Helper()
	select {
	case conn := <-f.accepted:
		return conn
	case <-time.After(timeout):
		f.t.Fatalf("upstream %s did not accept a connection within %s", f.network, timeout)
		return nil
	}
}

func (f *externalTransportFixture) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.open)
}

func (f *externalTransportFixture) Close() {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		listener := f.listener
		conns := make([]net.Conn, 0, len(f.open))
		for conn := range f.open {
			conns = append(conns, conn)
		}
		f.mu.Unlock()
		_ = listener.Close()
		for _, conn := range conns {
			_ = conn.Close()
		}
		select {
		case <-f.doneCh:
		case <-time.After(2 * time.Second):
			f.t.Errorf("upstream %s accept loop did not stop", f.network)
		}
		if f.network == "unix" {
			_ = os.Remove(f.address)
		}
	})
}

func (f *externalTransportFixture) reopen(t *testing.T, blackhole bool) *externalTransportFixture {
	t.Helper()
	address := f.address
	f.Close()
	return newExternalTransportFixture(t, f.network, address, blackhole)
}

func externalServerConfig(t *testing.T, network, address string) configfile.ExternalDoltConfig {
	t.Helper()
	if network == "unix" {
		return configfile.ExternalDoltConfig{Socket: address}
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split upstream address %q: %v", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse upstream port %q: %v", portText, err)
	}
	return configfile.ExternalDoltConfig{Host: host, Port: port}
}

type runningExternalProxy struct {
	proxy    *proxyServer
	cancel   context.CancelFunc
	done     chan error
	finished chan struct{}
	root     string
	port     int
}

func startExternalProxy(t *testing.T, fixture *externalTransportFixture) *runningExternalProxy {
	t.Helper()
	root := t.TempDir()
	upstream, err := server.NewExternalDoltServer(externalServerConfig(t, fixture.network, fixture.address))
	if err != nil {
		t.Fatalf("new external Dolt server: %v", err)
	}
	port := freeTCPPortForInternalTest(t)
	p := NewProxyServer(ProxyOpts{
		RootDir:     root,
		Port:        port,
		IdleTimeout: IdleTimeoutNever,
		Server:      upstream,
		Stats:       &Stats{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- p.ListenAndServe(ctx)
	}()
	waitForInternalPid(t, root, 5*time.Second)
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Errorf("proxy did not stop during cleanup")
		}
	})
	return &runningExternalProxy{proxy: p, cancel: cancel, done: done, finished: finished, root: root, port: port}
}

func freeTCPPortForInternalTest(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate proxy port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func waitForInternalPid(t *testing.T, root string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pf, err := pidfile.Read(root, PIDFileName)
		if err == nil && pf != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("proxy pidfile did not appear within %s", timeout)
}

func eventuallyInternal(t *testing.T, timeout time.Duration, check func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("eventually: %s", message)
}

func dialInternalProxy(t *testing.T, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	return conn
}

func consumeReadiness(t *testing.T, fixture *externalTransportFixture) {
	t.Helper()
	conn := fixture.nextAccepted(5 * time.Second)
	if conn == nil {
		return
	}
	eventuallyInternal(t, 2*time.Second, func() bool { return fixture.openCount() == 0 }, "readiness upstream connection closed")
}

func TestExternalProxyEstablishedTunnelClientCloseDrainsTCPAndUnix(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		network := network
		t.Run(network, func(t *testing.T) {
			if network == "unix" && runtime.GOOS == "windows" {
				t.Skip("Unix sockets are unavailable on Windows")
			}
			address := ""
			if network == "unix" {
				address = unixSocketPath(t)
			}
			fixture := newExternalTransportFixture(t, network, address, true)
			running := startExternalProxy(t, fixture)
			consumeReadiness(t, fixture)
			client := dialInternalProxy(t, running.port)
			defer client.Close()
			fixture.nextAccepted(5 * time.Second)
			client.Close()
			eventuallyInternal(t, 2*time.Second, func() bool {
				return running.proxy.activeConns.Load() == 0 && fixture.openCount() == 0
			}, "client close did not drain proxy and upstream connections")
			snapshot := running.proxy.stats.Snapshot()
			if snapshot.BackendDialAttempts < 1 || snapshot.BackendDialSuccess < 1 || snapshot.HandledConns < 1 {
				t.Fatalf("stats = %+v, want handled client tunnel", snapshot)
			}
			select {
			case err := <-running.done:
				t.Fatalf("proxy exited before explicit cancellation: %v", err)
			default:
			}
			running.cancel()
			select {
			case <-running.done:
			case <-time.After(2 * time.Second):
				t.Fatal("proxy did not stop after cancellation")
			}
		})
	}
}

func TestExternalProxyShutdownClosesEstablishedTCPAndUnixTunnels(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		network := network
		t.Run(network, func(t *testing.T) {
			address := ""
			if network == "unix" {
				address = unixSocketPath(t)
			}
			fixture := newExternalTransportFixture(t, network, address, true)
			running := startExternalProxy(t, fixture)
			consumeReadiness(t, fixture)
			client := dialInternalProxy(t, running.port)
			fixture.nextAccepted(5 * time.Second)
			running.cancel()
			select {
			case <-running.done:
			case <-time.After(2 * time.Second):
				t.Fatal("proxy shutdown exceeded bounded timeout")
			}
			_ = client.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			if _, err := client.Read(b[:]); err == nil {
				t.Fatal("client read succeeded after proxy shutdown")
			}
			_ = client.Close()
			eventuallyInternal(t, 2*time.Second, func() bool {
				return running.proxy.activeConns.Load() == 0 && fixture.openCount() == 0
			}, "proxy shutdown did not drain established tunnel")
			if got := running.proxy.stats.Snapshot().BackendStopCalls; got != 1 {
				t.Fatalf("backend stop calls = %d, want 1", got)
			}
		})
	}
}

func TestExternalProxyOutageAndRestoreReDialsTCPAndUnix(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		network := network
		t.Run(network, func(t *testing.T) {
			address := ""
			if network == "unix" {
				address = unixSocketPath(t)
			}
			fixture := newExternalTransportFixture(t, network, address, false)
			running := startExternalProxy(t, fixture)
			consumeReadiness(t, fixture)
			client := dialInternalProxy(t, running.port)
			fixture.nextAccepted(5 * time.Second)
			marker := []byte("proxy-recovery-marker")
			if _, err := client.Write(marker); err != nil {
				t.Fatalf("write healthy marker: %v", err)
			}
			got := make([]byte, len(marker))
			if _, err := io.ReadFull(client, got); err != nil {
				t.Fatalf("read healthy marker: %v", err)
			}
			if string(got) != string(marker) {
				t.Fatalf("healthy echo = %q, want %q", got, marker)
			}
			_ = client.Close()
			eventuallyInternal(t, 2*time.Second, func() bool { return running.proxy.activeConns.Load() == 0 }, "healthy tunnel did not close")

			fixture = fixture.reopen(t, false)
			// Close the restored listener to create a deterministic outage, then
			// rebind it in the same test so the proxy must re-dial the endpoint.
			fixture.Close()
			failed := dialInternalProxy(t, running.port)
			_ = failed.SetReadDeadline(time.Now().Add(2 * time.Second))
			var b [1]byte
			if _, err := failed.Read(b[:]); err == nil {
				t.Fatal("outage client unexpectedly received data")
			}
			_ = failed.Close()
			eventuallyInternal(t, 2*time.Second, func() bool {
				return running.proxy.stats.Snapshot().BackendDialErrors >= 1
			}, "proxy did not record outage dial error")

			fixture = newExternalTransportFixture(t, network, fixture.address, false)
			recovered := dialInternalProxy(t, running.port)
			defer recovered.Close()
			fixture.nextAccepted(5 * time.Second)
			if _, err := recovered.Write(marker); err != nil {
				t.Fatalf("write restored marker: %v", err)
			}
			got = make([]byte, len(marker))
			if _, err := io.ReadFull(recovered, got); err != nil {
				t.Fatalf("read restored marker: %v", err)
			}
			if string(got) != string(marker) {
				t.Fatalf("restored echo = %q, want %q", got, marker)
			}
			snapshot := running.proxy.stats.Snapshot()
			if snapshot.BackendDialSuccess < 2 || snapshot.HandledConns < 2 {
				t.Fatalf("recovery stats = %+v, want readiness, healthy, and restored dials", snapshot)
			}
		})
	}
}

func TestExternalProxyUnavailableStartupIsBoundedForTCPAndUnix(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		network := network
		t.Run(network, func(t *testing.T) {
			address := ""
			if network == "tcp" {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				address = ln.Addr().String()
				_ = ln.Close()
			} else {
				address = unixSocketPath(t)
			}
			upstream, err := server.NewExternalDoltServer(externalServerConfig(t, network, address))
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			port := freeTCPPortForInternalTest(t)
			p := NewProxyServer(ProxyOpts{RootDir: root, Port: port, Server: upstream, Stats: &Stats{}})
			ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
			defer cancel()
			start := time.Now()
			err = p.ListenAndServe(ctx)
			if time.Since(start) > 2*time.Second {
				t.Fatalf("unavailable startup exceeded bound: %s", time.Since(start))
			}
			if err == nil || (!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) && !containsInternal(err.Error(), "database server not ready")) {
				t.Fatalf("unavailable startup error = %v", err)
			}
			pf, readErr := pidfile.Read(root, PIDFileName)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if pf != nil {
				t.Fatalf("unavailable startup published pidfile: %+v", pf)
			}
			if got := p.stats.Snapshot().BackendStopCalls; got != 1 {
				t.Fatalf("backend stop calls = %d, want 1", got)
			}
		})
	}
}

func containsInternal(value, needle string) bool {
	return strings.Contains(value, needle)
}
