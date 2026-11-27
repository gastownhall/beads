package beads_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// proxyDataTestGreeting stands in for a MySQL server's initial handshake
// packet (protocol 10), longer than the proxy's handshake answer so a client
// that read it as one would see a wrong proof, not a short read.
var proxyDataTestGreeting = "J\x00\x00\x00\x0a8.0.33\x00" + strings.Repeat("\x01", 96)

// greetingListener accepts connections and greets each the way a MySQL
// server does, speaking first, then holds it open until the peer hangs up.
func greetingListener(t *testing.T) (host string, port int) {
	t.Helper()
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
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if _, err := io.WriteString(conn, proxyDataTestGreeting); err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// startTestProxy runs a real bd db-proxy rooted at a fresh directory in
// front of backendPort and returns its root and data port.
func startTestProxy(t *testing.T, backendPort int) (root string, port int) {
	t.Helper()
	root = t.TempDir()
	backend, err := server.NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: backendPort})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	p := proxy.NewProxyServer(proxy.ProxyOpts{RootDir: root, Server: backend})
	go func() { done <- p.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("proxy did not exit within 10s of cancel")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pf, err := pidfile.Read(root, proxy.PIDFileName)
		if err == nil && pf != nil {
			return root, pf.Port
		}
		select {
		case err := <-done:
			t.Fatalf("proxy exited before it published its pidfile: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("proxy pidfile not present within 10s")
	return "", 0
}

// TestDialProxyData pins the public client side of the proxy data port: with
// the proxy's root, DialProxyData completes the BDPROXY1 handshake and hands
// back a connection on which the backend's MySQL greeting arrives; under any
// other secret, or against a listener that is not a proxy, it fails with
// ErrProxyDataAuth before the caller can send a byte.
func TestDialProxyData(t *testing.T) {
	_, backendPort := greetingListener(t)
	root, port := startTestProxy(t, backendPort)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("the proxy's root", func(t *testing.T) {
		conn, err := beads.DialProxyData(ctx, root, "127.0.0.1", port)
		if err != nil {
			t.Fatalf("DialProxyData: %v", err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len(proxyDataTestGreeting))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("read the greeting through the proxy: %v", err)
		}
		if string(got) != proxyDataTestGreeting {
			t.Fatalf("greeting through the proxy = %q, want %q", got, proxyDataTestGreeting)
		}
	})

	t.Run("another root", func(t *testing.T) {
		other := t.TempDir()
		if _, err := identity.WriteSecret(other); err != nil {
			t.Fatal(err)
		}
		conn, err := beads.DialProxyData(ctx, other, "127.0.0.1", port)
		if !errors.Is(err, beads.ErrProxyDataAuth) {
			t.Fatalf("DialProxyData under another root's secret = %v, %v; want ErrProxyDataAuth", conn, err)
		}
	})

	t.Run("not a proxy", func(t *testing.T) {
		host, notProxyPort := greetingListener(t)
		conn, err := beads.DialProxyData(ctx, root, host, notProxyPort)
		if !errors.Is(err, beads.ErrProxyDataAuth) {
			t.Fatalf("DialProxyData to a MySQL-speaking listener = %v, %v; want ErrProxyDataAuth", conn, err)
		}
	})

	t.Run("no secret", func(t *testing.T) {
		conn, err := beads.DialProxyData(ctx, t.TempDir(), "127.0.0.1", port)
		if err == nil {
			_ = conn.Close()
			t.Fatal("DialProxyData with a root that holds no proxy.secret succeeded")
		}
		if errors.Is(err, beads.ErrProxyDataAuth) {
			t.Fatalf("an unreadable secret is not a refused handshake: %v", err)
		}
	})
}
