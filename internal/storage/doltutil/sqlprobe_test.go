package doltutil

import (
	"io"
	"net"
	"testing"
	"time"
)

// fakeMySQLGreeting stands in for a dolt sql-server handshake packet.
var fakeMySQLGreeting = []byte{0x08, 0x00, 0x00, 0x00, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a, 0x0a}

// TestDrainAndCloseProbeConsumesGreetingBeforeClose proves DrainAndCloseProbe
// reads the peer's greeting before it closes. A net.Pipe write completes only
// once the other side reads it, so a probe that closes without reading makes
// the peer's greeting write fail.
func TestDrainAndCloseProbeConsumesGreetingBeforeClose(t *testing.T) {
	probe, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })

	result := make(chan error, 1)
	go func() {
		_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := peer.Write(fakeMySQLGreeting); err != nil {
			result <- err
			return
		}
		_, err := peer.Read(make([]byte, 1))
		if err == io.EOF {
			err = nil
		}
		result <- err
	}()

	if !DrainAndCloseProbe(probe) {
		t.Error("DrainAndCloseProbe = false for a greeting peer; want true")
	}
	if err := <-result; err != nil {
		t.Fatalf("peer: %v", err)
	}
}

// TestProbeSQLServerReportsGreeting covers both ProbeSQLServer outcomes: a
// dial error for a closed address and greeted for a greeting listener.
func TestProbeSQLServerReportsGreeting(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddr := closed.Addr().String()
	_ = closed.Close()
	if greeted, err := ProbeSQLServer("tcp", closedAddr, 200*time.Millisecond); err == nil || greeted {
		t.Errorf("ProbeSQLServer(closed) = (%v, %v); want (false, dial error)", greeted, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write(fakeMySQLGreeting)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Read(make([]byte, 1))
	}()
	greeted, err := ProbeSQLServer("tcp", ln.Addr().String(), time.Second)
	if err != nil || !greeted {
		t.Errorf("ProbeSQLServer(greeting) = (%v, %v); want (true, nil)", greeted, err)
	}
}
