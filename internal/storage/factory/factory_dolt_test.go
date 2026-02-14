//go:build cgo

package factory

import (
	"context"
	"testing"
)

func TestServerModeNoFallback(t *testing.T) {
	// When server mode is configured with an unreachable host,
	// the factory should return an error, NOT silently fall back to embedded mode.
	opts := Options{
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: 19999, // port nothing is listening on
		Database:   "testdb",
	}

	ctx := context.Background()
	_, err := NewWithOptions(ctx, "dolt", "/tmp/beads-test-no-fallback", opts)
	if err == nil {
		t.Fatal("expected error when server is unreachable, got nil (silent fallback to embedded mode?)")
	}
	t.Logf("got expected error: %v", err)
}
