package proxy

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"testing"
	"time"
)

type blockingBackend struct{}

func (blockingBackend) ID(context.Context) string                          { return "blocking" }
func (blockingBackend) DSN(context.Context, string, string, string) string { return "" }
func (blockingBackend) Start(context.Context) error                        { return nil }
func (blockingBackend) Stop(context.Context) error                         { return nil }
func (blockingBackend) Running(context.Context) bool                       { return true }
func (blockingBackend) Dial(ctx context.Context) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func startAuthenticatedHandleConn(t *testing.T, p *proxyServer, ctx context.Context) (net.Conn, <-chan error) {
	t.Helper()
	p.dataToken = "test-token"
	client, peer := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- p.handleConn(ctx, client) }()
	if _, err := io.WriteString(peer, dataAuthPrefix+p.dataToken+"\n"); err != nil {
		t.Fatal(err)
	}
	return peer, done
}

func TestHandleConnBoundsBlockedBackendDial(t *testing.T) {
	p := NewProxyServer(ProxyOpts{Server: blockingBackend{}})
	p.logger = log.Default()
	peer, done := startAuthenticatedHandleConn(t, p, context.Background())
	defer peer.Close()
	start := time.Now()
	err := <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > backendDialTimeout+time.Second {
		t.Fatalf("blocked dial took %s", elapsed)
	}
}

func TestHandleConnCanceledClientDialClosesAndDrains(t *testing.T) {
	stats := &Stats{}
	p := NewProxyServer(ProxyOpts{Server: blockingBackend{}, Stats: stats})
	p.logger = log.Default()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	peer, done := startAuthenticatedHandleConn(t, p, ctx)
	defer peer.Close()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if got := p.activeConns.Load(); got != 0 {
		t.Fatalf("active connections = %d, want 0", got)
	}
	snapshot := stats.Snapshot()
	if snapshot.BackendDialAttempts != 1 || snapshot.BackendDialErrors != 1 || snapshot.HandledConns != 0 {
		t.Fatalf("stats = %+v, want one failed dial and no handled conns", snapshot)
	}
}

type tcpBlackholeBackend struct{ address string }

func (b tcpBlackholeBackend) ID(context.Context) string                          { return "tcp-blackhole" }
func (b tcpBlackholeBackend) DSN(context.Context, string, string, string) string { return b.address }
func (b tcpBlackholeBackend) Start(context.Context) error                        { return nil }
func (b tcpBlackholeBackend) Stop(context.Context) error                         { return nil }
func (b tcpBlackholeBackend) Running(context.Context) bool                       { return true }
func (b tcpBlackholeBackend) Dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{}
	return d.DialContext(ctx, "tcp", b.address)
}

func TestHandleConnTCPBlackholeIsBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() { conn, _ := listener.Accept(); accepted <- conn }()
	p := NewProxyServer(ProxyOpts{Server: tcpBlackholeBackend{address: listener.Addr().String()}, Stats: &Stats{}})
	p.logger = log.Default()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	peer, done := startAuthenticatedHandleConn(t, p, ctx)
	defer peer.Close()
	start := time.Now()
	err = <-done
	if conn := <-accepted; conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("blackhole connection unexpectedly completed without cancellation")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("blackhole dial exceeded bound")
	}
	if p.activeConns.Load() != 0 {
		t.Fatal("active connection leaked")
	}
}
