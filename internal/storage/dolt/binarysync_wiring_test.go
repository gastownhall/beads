package dolt

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestNewServerMode_SyncsTrackedServerBinaryBeforeApplicationQueries pins
// the AC-1 store wiring (GH#6064): an eligible server-mode open reaches the
// server-binary hook after its dial succeeded and before the application's
// connection is used — proven by the open failing with the hook's own error
// when the hook refuses — and hands it the open's endpoint and restart
// permission.
func TestNewServerMode_SyncsTrackedServerBinaryBeforeApplicationQueries(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "") // keep the real breaker (see circuit_test.go)

	const port = 45678
	origDial, origSync := dialServer, syncServerBinary
	t.Cleanup(func() { dialServer, syncServerBinary = origDial, origSync })
	dialServer = func(string, string, time.Duration) (net.Conn, error) {
		c, _ := net.Pipe()
		return c, nil
	}
	refusal := errors.New("binary sync refused")
	var got *doltserver.BinarySyncRequest
	syncServerBinary = func(req doltserver.BinarySyncRequest) error {
		got = &req
		return refusal
	}

	beadsDir := t.TempDir()
	cfg := &Config{
		Database:   "binary_sync_wiring",
		BeadsDir:   beadsDir,
		Path:       filepath.Join(beadsDir, "dolt"),
		ServerHost: "127.0.0.1",
		ServerPort: port,
		AutoStart:  true,
	}
	_, err := newServerMode(context.Background(), cfg)
	if !errors.Is(err, refusal) {
		t.Fatalf("an open whose server-binary sync refused = %v, want the hook's own error (%v)", err, refusal)
	}
	if got == nil {
		t.Fatal("the open never reached the server-binary hook")
	}
	if got.BeadsDir != beadsDir || got.Host != "127.0.0.1" || got.Port != port {
		t.Errorf("hook request = %+v, want beads dir %s, host 127.0.0.1, port %d", got, beadsDir, port)
	}
	if !got.AllowRestart {
		t.Error("an eligible open (writable, auto-start on) must carry restart permission")
	}
}
// TestMaybeSyncServerBinarySkipsEndpointsBdDoesNotTrack is AC-7 (GH#6064):
// the store-open hook only ever speaks about a server bd tracks and may
// launch on this machine. A socket, TLS, gateway or proxied endpoint is not
// the workspace's tracked owned server; a remote host — the IPv6 loopback
// included, bd's server listens on 127.0.0.1 only — and port 0 are not a
// server bd could have started here; and in test mode the dolt on PATH is
// the test's. None of those opens reach the hook: no comparison, no
// lifecycle change, no output. The eligible baseline still does.
func TestMaybeSyncServerBinarySkipsEndpointsBdDoesNotTrack(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")

	origSync := syncServerBinary
	t.Cleanup(func() { syncServerBinary = origSync })
	var called bool
	syncServerBinary = func(doltserver.BinarySyncRequest) error {
		called = true
		return nil
	}

	owned := func() *Config {
		return &Config{ServerHost: "127.0.0.1", ServerPort: 45678}
	}
	cases := []struct {
		name string
		cfg  *Config
	}{
		{"unix socket endpoint", func() *Config { c := owned(); c.ServerSocket = "/tmp/dolt.sock"; return c }()},
		{"TLS endpoint", func() *Config { c := owned(); c.ServerTLS = true; return c }()},
		{"gateway endpoint", func() *Config { c := owned(); c.Gateway = true; return c }()},
		{"proxied server endpoint", func() *Config { c := owned(); c.ProxiedServer = true; return c }()},
		{"no server port", func() *Config { c := owned(); c.ServerPort = 0; return c }()},
		{"remote host", func() *Config { c := owned(); c.ServerHost = "db.example.com"; return c }()},
		{"IPv6 loopback host", func() *Config { c := owned(); c.ServerHost = "::1"; return c }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			if err := maybeSyncServerBinary(tc.cfg, t.TempDir()); err != nil {
				t.Fatalf("an excluded open must not fail: %v", err)
			}
			if called {
				t.Error("the open reached the server-binary hook, want no comparison, no lifecycle change, no output")
			}
		})
	}

	t.Run("test mode", func(t *testing.T) {
		t.Setenv("BEADS_TEST_MODE", "1")
		called = false
		if err := maybeSyncServerBinary(owned(), t.TempDir()); err != nil {
			t.Fatalf("an excluded open must not fail: %v", err)
		}
		if called {
			t.Error("the open reached the server-binary hook, want no comparison, no lifecycle change, no output")
		}
	})

	t.Run("eligible owned endpoint still reaches the hook", func(t *testing.T) {
		called = false
		if err := maybeSyncServerBinary(owned(), t.TempDir()); err != nil {
			t.Fatalf("the eligible baseline: %v", err)
		}
		if !called {
			t.Error("the eligible owned endpoint never reached the server-binary hook")
		}
	})
}
