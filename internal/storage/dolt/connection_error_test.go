package dolt

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestConnectionError_CleanMessage verifies that when the Dolt server is
// unreachable (connection refused), the error message uses the clean format
// "cannot connect to dolt server at <host>:<port>" rather than a raw driver
// error or stack trace. This is the user-facing contract from the user story:
//
//	Given beads is connected to a remote dolt server
//	And the SQL connection is down
//	When I run any bd command
//	Then I see "Cannot connect to dolt server at <host>:<port>"
//	And NOT a hang or raw stack trace
func TestConnectionError_CleanMessage(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	t.Setenv("BEADS_DOLT_AUTO_START", "0") // prevent auto-start
	t.Setenv("BEADS_DOLT_SERVER_PORT", "") // don't let env override our port
	t.Setenv("BEADS_DOLT_PORT", "")        // don't let legacy env override either

	// Find a port that nothing is listening on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate ephemeral port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // close immediately so nothing is listening

	tmpDir := t.TempDir()

	cfg := &Config{
		Path:       tmpDir + "/dolt",
		BeadsDir:   tmpDir,
		ServerHost: "127.0.0.1",
		ServerPort: port,
		ServerUser: "root",
		Database:   "testdb",
		AutoStart:  false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err = New(ctx, cfg)
	if err == nil {
		t.Fatal("expected error when connecting to non-listening port, got nil")
	}

	msg := err.Error()

	// The error message must start with a clean, user-friendly prefix.
	wantPrefix := "cannot connect to dolt server at"
	if !strings.Contains(strings.ToLower(msg), wantPrefix) {
		t.Errorf("error message should contain %q, got:\n%s", wantPrefix, msg)
	}

	// Must include the host:port so the user knows which server failed.
	wantAddr := "127.0.0.1"
	if !strings.Contains(msg, wantAddr) {
		t.Errorf("error message should contain address %q, got:\n%s", wantAddr, msg)
	}

	// Must complete within the timeout (not hang).
	select {
	case <-ctx.Done():
		t.Fatal("connection attempt timed out — the error should have been immediate")
	default:
		// good — we got here before the timeout
	}
}

// TestConnectionError_ReasonableTimeout verifies that a connection attempt
// to an unreachable server completes within a reasonable time (not hanging).
func TestConnectionError_ReasonableTimeout(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	// Use a port that will refuse connections immediately.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate ephemeral port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	tmpDir := t.TempDir()

	cfg := &Config{
		Path:       tmpDir + "/dolt",
		BeadsDir:   tmpDir,
		ServerHost: "127.0.0.1",
		ServerPort: port,
		ServerUser: "root",
		Database:   "testdb",
		AutoStart:  false,
	}

	start := time.Now()

	ctx := context.Background()
	_, err = New(ctx, cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected connection error, got nil")
	}

	// Should fail fast (well under 10 seconds for a refused connection).
	if elapsed > 5*time.Second {
		t.Errorf("connection error took %v — should complete within 5s", elapsed)
	}
}
