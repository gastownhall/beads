package beads_test

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads"
)

// These tests extend the proxied-server refusal tests in
// open_backend_selection_test.go (GH#8323, GH#6655) with the cases those leave
// open, on the same helpers and contract.

// envForcedServerModeSpellings are the env settings that make
// configfile.IsDoltServerMode true ahead of dolt_mode, one row per spelling it
// accepts: BEADS_DOLT_SHARED_SERVER takes "true" as well as "1".
var envForcedServerModeSpellings = []struct{ name, serverMode, sharedServer string }{
	{"BEADS_DOLT_SERVER_MODE=1", "1", ""},
	{"BEADS_DOLT_SHARED_SERVER=1", "", "1"},
	{"BEADS_DOLT_SHARED_SERVER=true", "", "true"},
}

// countingLoopbackListener listens on a free 127.0.0.1 port and counts the
// connections it accepts, closing each at once. stop closes the listener and
// waits for its accept loop, so the count is final once stop returns; it is
// safe to call more than once and also runs at cleanup.
func countingLoopbackListener(t *testing.T) (port int, accepted *atomic.Int32, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepted = new(atomic.Int32)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	var once sync.Once
	stop = func() { once.Do(func() { _ = ln.Close(); wg.Wait() }) }
	t.Cleanup(stop)
	return ln.Addr().(*net.TCPAddr).Port, accepted, stop
}

// TestOpenBestAvailableProxiedServerRefusalDialsNothing pins that an
// env-forced open the refusal covers is refused before it dials anything, for
// every spelling that forces server mode. BEADS_DOLT_SERVER_PORT is unset and
// the legacy BEADS_DOLT_PORT names a test-owned listener: that is the port the
// server arm resolves for such an open, so a refusal that came after the dial,
// or none, connects to it. The listener must see no connection, the open must
// return no store and ErrProxiedServerUnsupported, and the workspace must not
// change.
func TestOpenBestAvailableProxiedServerRefusalDialsNothing(t *testing.T) {
	for _, env := range envForcedServerModeSpellings {
		for name, open := range bestAvailableOpens {
			t.Run(env.name+"/"+name, func(t *testing.T) {
				isolateProxiedServerEnv(t, env.serverMode, env.sharedServer)
				t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
				port, accepted, stop := countingLoopbackListener(t)
				t.Setenv("BEADS_DOLT_SERVER_PORT", "")
				t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(port))
				root, beadsDir := writeProxiedServerWorkspace(t, "metadata.json", "dolt", "proxied-server")
				before := workspaceTreeDigest(t, root)

				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				store, err := open(ctx, beadsDir)
				if store != nil {
					_ = store.Close()
				}
				stop()
				if n := accepted.Load(); n != 0 {
					t.Errorf("%s with %s dialed the legacy BEADS_DOLT_PORT listener %d time(s) (err=%v)", name, env.name, n, err)
				}
				if store != nil {
					t.Errorf("%s with %s returned a store for a proxied-server workspace (err=%v)", name, env.name, err)
				}
				if !errors.Is(err, beads.ErrProxiedServerUnsupported) {
					t.Fatalf("%s with %s error = %v, want errors.Is ErrProxiedServerUnsupported", name, env.name, err)
				}
				if after := workspaceTreeDigest(t, root); !maps.Equal(after, before) {
					t.Fatalf("%s with %s changed the workspace: before %v, after %v", name, env.name, before, after)
				}
			})
		}
	}
}

// TestOpenBestAvailableWithProxiedServerRefusalPrecedesEveryOption pins the
// refusal's precedence over each per-open option Dolt refuses, not only the
// UserAgent that bestAvailableOpens carries: a proxied-server workspace cannot
// be opened whatever the options are, so OpenBestAvailableWith returns no store
// and ErrProxiedServerUnsupported for a Credential, an HTTPClient and a
// UserAgent alike, and the error does not also match the option's own
// sentinel.
func TestOpenBestAvailableWithProxiedServerRefusalPrecedesEveryOption(t *testing.T) {
	isolateProxiedServerEnv(t, "", "")
	tests := []struct {
		name      string
		opts      beads.OpenOptions
		optionErr error
	}{
		{name: "Credential", opts: beads.OpenOptions{Credential: fixtureCredential{}}, optionErr: beads.ErrCredentialWithoutOpenWith},
		{name: "HTTPClient", opts: beads.OpenOptions{HTTPClient: &http.Client{}}, optionErr: beads.ErrHTTPClientWithoutOpenWith},
		{name: "UserAgent", opts: beads.OpenOptions{UserAgent: "test-agent/1.0"}, optionErr: beads.ErrUserAgentWithoutOpenWith},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, beadsDir := writeProxiedServerWorkspace(t, "metadata.json", "dolt", "proxied-server")
			openWith := func(ctx context.Context, beadsDir string) (beads.Storage, error) {
				return beads.OpenBestAvailableWith(ctx, beadsDir, tt.opts)
			}
			gotStore, err := openClosing(t, openWith, beadsDir)
			if gotStore {
				t.Errorf("OpenBestAvailableWith with a %s returned a store for a proxied-server workspace (err=%v)", tt.name, err)
			}
			if !errors.Is(err, beads.ErrProxiedServerUnsupported) {
				t.Fatalf("OpenBestAvailableWith with a %s error = %v, want errors.Is ErrProxiedServerUnsupported", tt.name, err)
			}
			if errors.Is(err, tt.optionErr) {
				t.Fatalf("OpenBestAvailableWith with a %s error = %v, must not also match the option sentinel %v", tt.name, err, tt.optionErr)
			}
		})
	}
}

// TestOpenBestAvailableProxiedServerRefusalSparesStaleEmbeddedTree pins the
// GH#6655 case the absent-tree tests leave open: a workspace that moved to
// proxied-server mode can still hold the .beads/embeddeddolt tree it used while
// embedded, whose issues are stale. A cgo open used to serve that tree as the
// workspace's data. Every best-available open must refuse with
// ErrProxiedServerUnsupported and leave the tree, like the rest of the
// workspace, exactly as it was.
func TestOpenBestAvailableProxiedServerRefusalSparesStaleEmbeddedTree(t *testing.T) {
	isolateProxiedServerEnv(t, "", "")
	for name, open := range bestAvailableOpens {
		t.Run(name, func(t *testing.T) {
			root, beadsDir := writeProxiedServerWorkspace(t, "metadata.json", "dolt", "proxied-server")
			marker := filepath.Join(beadsDir, "embeddeddolt", "beads_legacy", "stale-marker")
			if err := os.MkdirAll(filepath.Dir(marker), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("issues as of the last embedded run\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := workspaceTreeDigest(t, root)
			gotStore, err := openClosing(t, open, beadsDir)
			if gotStore {
				t.Errorf("%s returned a store for a proxied-server workspace with a stale embedded tree (err=%v)", name, err)
			}
			if !errors.Is(err, beads.ErrProxiedServerUnsupported) {
				t.Fatalf("%s error = %v, want errors.Is ErrProxiedServerUnsupported", name, err)
			}
			if after := workspaceTreeDigest(t, root); !maps.Equal(after, before) {
				t.Fatalf("%s changed the workspace or its stale embedded tree: before %v, after %v", name, before, after)
			}
		})
	}
}
