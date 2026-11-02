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