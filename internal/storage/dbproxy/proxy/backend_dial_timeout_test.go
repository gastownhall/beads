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

func TestHandleConnBoundsBlockedBackendDial(t *testing.T) {
	p := NewProxyServer(ProxyOpts{Server: blockingBackend{}})
	p.logger = log.Default()
	client, peer := net.Pipe()
	defer peer.Close()
	start := time.Now()
	err := p.handleConn(context.Background(), client)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > backendDialTimeout+time.Second {
		t.Fatalf("blocked dial took %s", elapsed)
	}
	_, _ = io.Copy(io.Discard, peer)
}
