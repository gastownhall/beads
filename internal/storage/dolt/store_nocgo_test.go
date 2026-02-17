//go:build !cgo

package dolt

import (
	"context"
	"errors"
	"testing"
)

func TestNoCGO_EmbeddedModeReturnsError(t *testing.T) {
	_, err := New(context.Background(), &Config{
		Path: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for embedded mode without CGO")
	}
	if !errors.Is(err, errNoCGO) {
		t.Fatalf("expected errNoCGO, got: %v", err)
	}
}

func TestNoCGO_ServerModeDoesNotReturnCGOError(t *testing.T) {
	// Server mode should NOT return errNoCGO — the connection will fail
	// because there's no server running, but it should be a network error,
	// not a CGO error.
	_, err := New(context.Background(), &Config{
		Path:       t.TempDir(),
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: 13307, // unlikely to be in use
	})
	if err == nil {
		t.Fatal("expected error (no server running), got nil")
	}
	if errors.Is(err, errNoCGO) {
		t.Fatalf("server mode should not return errNoCGO, got: %v", err)
	}
	// The error should be a connection error (server unreachable)
	t.Logf("got expected non-CGO error: %v", err)
}
