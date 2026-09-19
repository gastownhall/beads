package dolt

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

// ResolveSocketTransport implements a socket-first / TCP-fallback policy for
// Dolt server connections (gt-28itz). These tests stub dialProbe so they run
// without a live Dolt server.

type probeCall struct {
	network string
	addr    string
}

func withStubbedDialProbe(t *testing.T, reachable map[string]bool) *[]probeCall {
	t.Helper()
	orig := dialProbe
	t.Cleanup(func() { dialProbe = orig })
	var calls []probeCall
	dialProbe = func(network, addr string, _ time.Duration) error {
		calls = append(calls, probeCall{network: network, addr: addr})
		if reachable[network+"|"+addr] {
			return nil
		}
		return errors.New("stub: unreachable " + network + " " + addr)
	}
	return &calls
}

// No socket configured → pure TCP, no probing, returns "".
func TestResolveSocketTransport_NoSocket(t *testing.T) {
	calls := withStubbedDialProbe(t, map[string]bool{})
	got := ResolveSocketTransport("", "127.0.0.1", 52756, 100*time.Millisecond)
	if got != "" {
		t.Errorf("expected empty socket (TCP), got %q", got)
	}
	if len(*calls) != 0 {
		t.Errorf("expected no probes without a configured socket, got %v", *calls)
	}
}

// Socket is live → keep using the socket.
func TestResolveSocketTransport_SocketUp(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{
		"unix|/tmp/mysql.sock": true,
	})
	got := ResolveSocketTransport("/tmp/mysql.sock", "127.0.0.1", 52756, 100*time.Millisecond)
	if got != "/tmp/mysql.sock" {
		t.Errorf("expected socket kept, got %q", got)
	}
}

// Socket down but TCP up → fall back to TCP (return ""). This is the gt-28itz
// regression: /tmp/mysql.sock absent while the server is reachable on :52756.
func TestResolveSocketTransport_SocketDownTCPUp_FallsBack(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{
		"tcp|" + net.JoinHostPort("127.0.0.1", "52756"): true,
		// unix socket intentionally absent from the reachable set
	})
	got := ResolveSocketTransport("/tmp/mysql.sock", "127.0.0.1", 52756, 100*time.Millisecond)
	if got != "" {
		t.Errorf("expected TCP fallback (empty socket), got %q", got)
	}
}

// Socket down AND TCP down → keep the socket so the normal error path reports
// the outage with its socket-specific hint (no silent transport swap).
func TestResolveSocketTransport_BothDown_KeepsSocket(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{})
	got := ResolveSocketTransport("/tmp/mysql.sock", "127.0.0.1", 52756, 100*time.Millisecond)
	if got != "/tmp/mysql.sock" {
		t.Errorf("expected socket kept (both down), got %q", got)
	}
}

// Socket down and no usable TCP port → keep socket (cannot fall back).
func TestResolveSocketTransport_SocketDownNoPort_KeepsSocket(t *testing.T) {
	withStubbedDialProbe(t, map[string]bool{
		"tcp|" + net.JoinHostPort("127.0.0.1", "0"): true, // should never be probed
	})
	got := ResolveSocketTransport("/tmp/mysql.sock", "127.0.0.1", 0, 100*time.Millisecond)
	if got != "/tmp/mysql.sock" {
		t.Errorf("expected socket kept (no TCP port), got %q", got)
	}
}

// Socket fallback is wired in newServerMode before its fail-fast connection
// dial. Keep this test on the connection-error path so it needs no live Dolt
// server while still proving that server-mode configuration is normalized.
func TestNewServerMode_SocketDownTCPUpFallsBack(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	const socket = "/tmp/beads-test-mysql.sock"
	const host = "127.0.0.1"
	const port = 1 // expected to be unreachable for the eventual real connection dial
	tcpAddr := net.JoinHostPort(host, "1")
	calls := withStubbedDialProbe(t, map[string]bool{
		"tcp|" + tcpAddr: true,
	})
	cfg := &Config{
		Database:     "test_socket_fallback_wiring",
		ServerHost:   host,
		ServerPort:   port,
		ServerSocket: socket,
		AutoStart:    false,
	}

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected connection error from unreachable TCP endpoint")
	}
	if cfg.ServerSocket != "" {
		t.Fatalf("expected server mode to select TCP fallback, socket = %q", cfg.ServerSocket)
	}
	wantCalls := []probeCall{
		{network: "unix", addr: socket},
		{network: "tcp", addr: tcpAddr},
	}
	if !reflect.DeepEqual(*calls, wantCalls) {
		t.Errorf("probe calls = %v, want %v", *calls, wantCalls)
	}
}

func TestNewServerMode_NoSocketDoesNotProbe(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	calls := withStubbedDialProbe(t, map[string]bool{})
	cfg := &Config{
		Database:   "test_socket_fallback_no_socket",
		ServerHost: "127.0.0.1",
		ServerPort: 1, // expected to be unreachable for the eventual real connection dial
		AutoStart:  false,
	}

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected connection error from unreachable TCP endpoint")
	}
	if len(*calls) != 0 {
		t.Errorf("expected no probes without a configured socket, got %v", *calls)
	}
}
