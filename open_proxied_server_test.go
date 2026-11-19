package beads_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads"
)

// These tests run in both the cgo and CGO_ENABLED=0 builds: the proxied-server
// arm is shared verbatim (openProxiedServer in beads.go), and GH#8323 was a
// different wrong answer in each build — a new, empty embedded store under
// cgo, the embedded-Dolt-requires-CGO error without it.

// proxiedBestAvailableOpens are the opens that route a proxied-server
// workspace through the bd proxy, keyed by subtest name.
var proxiedBestAvailableOpens = map[string]func(context.Context, string) (beads.Storage, error){
	"OpenBestAvailable": beads.OpenBestAvailable,
	"OpenBestAvailableWith": func(ctx context.Context, beadsDir string) (beads.Storage, error) {
		return beads.OpenBestAvailableWith(ctx, beadsDir, beads.OpenOptions{})
	},
}

// isolateProxiedOpenEnv pins the env a proxied open test runs under: PATH
// holds neither bd nor dolt, so no proxy can be started; the server-arm env a
// regression could fall into names a port nobody listens on and never
// auto-starts; and HOME is the test's own.
func isolateProxiedOpenEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_PROXIED_SERVER_ROOT_PATH", "")
}

// writeProxiedWorkspace builds <root>/.beads declaring dolt_mode in file
// (metadata.json, or the legacy config.json). With external set it also
// writes a proxied-server sidecar naming an external upstream at a closed
// loopback port and a server root outside the workspace, which reaches the
// proxy spawn without a dolt binary. Without it the workspace is
// managed-local, with a stand-in root at .beads/dolt.
func writeProxiedWorkspace(t *testing.T, file, doltMode string, external bool) (root, beadsDir string) {
	t.Helper()
	root = t.TempDir()
	beadsDir = filepath.Join(root, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","dolt_mode":` + strconv.Quote(doltMode) + `,"dolt_database":"beads_sdk"}`
	if err := os.WriteFile(filepath.Join(beadsDir, file), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	if external {
		sidecar := `{"root_path":` + strconv.Quote(t.TempDir()) +
			`,"external":{"host":"127.0.0.1","port":` + strconv.Itoa(closedProxiedLoopbackPort(t)) + `}}`
		if err := os.WriteFile(filepath.Join(beadsDir, "proxied_server_client_info.json"), []byte(sidecar), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, beadsDir
}

// closedProxiedLoopbackPort returns a 127.0.0.1 port that was free a moment
// ago and is closed now.
func closedProxiedLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// requireNoEmbeddedStore fails when an open created the embedded store or a
// SQLite file: AC 1, on success or failure.
func requireNoEmbeddedStore(t *testing.T, beadsDir, what string) {
	t.Helper()
	for _, name := range []string{"embeddeddolt", "beads.db"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s created .beads/%s for a proxied-server workspace (stat error: %v)", what, name, err)
		}
	}
}

func openProxiedClosing(t *testing.T, open func(context.Context, string) (beads.Storage, error), beadsDir string) error {
	t.Helper()
	store, err := open(t.Context(), beadsDir)
	if store != nil {
		_ = store.Close()
		t.Fatalf("opened a proxied-server workspace with no proxy and no bd to start one (err=%v)", err)
	}
	return err
}

// putFakeDoltOnPath puts a `dolt` on PATH that answers `dolt version` at
// the required minimum and does nothing else. A managed-local open probes
// dolt before it looks for a proxy, so without one it fails on the probe and
// never reaches the proxy lookup this suite is about.
func putFakeDoltOnPath(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake dolt is a POSIX shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'dolt version 2.6.0'\n"
	if err := os.WriteFile(filepath.Join(dir, "dolt"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: the stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// TestOpenBestAvailableProxiedDoesNotCreateEmbeddedStore pins GH#8323 AC 1, 3
// and 8: a proxied-server workspace is opened through the proxy arm in either
// build, so with no proxy running and no bd on PATH the open fails with
// ErrProxyUnavailable, creates no embedded store, and never answers with the
// embedded-Dolt-requires-CGO error. The managed-local layout gets a fake dolt
// on PATH (it probes dolt first), and is held to the same sentinel.
func TestOpenBestAvailableProxiedDoesNotCreateEmbeddedStore(t *testing.T) {
	for _, external := range []bool{true, false} {
		layout := "managed"
		if external {
			layout = "external"
		}
		for _, file := range []string{"metadata.json", "config.json"} {
			for _, spelling := range []string{"proxied-server", "Proxied-Server"} {
				for name, open := range proxiedBestAvailableOpens {
					t.Run(layout+"/"+file+"/"+spelling+"/"+name, func(t *testing.T) {
						isolateProxiedOpenEnv(t)
						if !external {
							putFakeDoltOnPath(t)
						}
						_, beadsDir := writeProxiedWorkspace(t, file, spelling, external)
						err := openProxiedClosing(t, open, beadsDir)
						requireNoEmbeddedStore(t, beadsDir, name)
						if err == nil {
							t.Fatalf("%s returned neither a store nor an error", name)
						}
						if strings.Contains(err.Error(), "CGO") {
							t.Fatalf("%s error = %v, want the proxied arm's error, not the embedded arm's", name, err)
						}
						if !errors.Is(err, beads.ErrProxyUnavailable) {
							t.Fatalf("%s error = %v, want errors.Is ErrProxyUnavailable", name, err)
						}
					})
				}
			}
		}
	}
}

// TestOpenBestAvailableProxiedRefusesPerOpenOptions pins that the per-open
// option refusals hold on the proxied arm exactly as on the Dolt server arm:
// they configure registered backends' transports and mean nothing to a local
// proxy, so each is refused before the proxy is touched.
func TestOpenBestAvailableProxiedRefusesPerOpenOptions(t *testing.T) {
	tests := []struct {
		name string
		opts beads.OpenOptions
		want error
	}{
		{name: "Credential", opts: beads.OpenOptions{Credential: fixtureCredential{}}, want: beads.ErrCredentialWithoutOpenWith},
		{name: "HTTPClient", opts: beads.OpenOptions{HTTPClient: &http.Client{}}, want: beads.ErrHTTPClientWithoutOpenWith},
		{name: "UserAgent", opts: beads.OpenOptions{UserAgent: "test-agent/1.0"}, want: beads.ErrUserAgentWithoutOpenWith},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateProxiedOpenEnv(t)
			_, beadsDir := writeProxiedWorkspace(t, "metadata.json", "proxied-server", true)
			err := openProxiedClosing(t, func(ctx context.Context, dir string) (beads.Storage, error) {
				return beads.OpenBestAvailableWith(ctx, dir, tt.opts)
			}, beadsDir)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			requireNoEmbeddedStore(t, beadsDir, tt.name)
		})
	}
}

// envForcedServerModes are the two env settings that make IsDoltServerMode
// true ahead of dolt_mode on a proxied-server workspace.
var envForcedServerModes = []string{"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER"}

// TestOpenBestAvailableProxiedAtNamedServerPortKeepsServerArm pins the #8476
// named-port exemption: an env that forces server mode and names the port in
// BEADS_DOLT_SERVER_PORT, as Gas City's proxied-native lane does against a bd
// 1.3.x proxy, still takes the Dolt server arm and dials that port, ahead of
// the proxied arm. The named port is closed and is not the port 1 the
// isolated env otherwise names, so a dial failure there shows the server arm
// dialed the port the env named. The open never reaches the proxy.
func TestOpenBestAvailableProxiedAtNamedServerPortKeepsServerArm(t *testing.T) {
	for _, env := range envForcedServerModes {
		for name, open := range proxiedBestAvailableOpens {
			t.Run(env+"/"+name, func(t *testing.T) {
				isolateProxiedOpenEnv(t)
				t.Setenv(env, "1")
				port := strconv.Itoa(closedProxiedLoopbackPort(t))
				t.Setenv("BEADS_DOLT_SERVER_PORT", port)
				_, beadsDir := writeProxiedWorkspace(t, "metadata.json", "proxied-server", true)
				err := openProxiedClosing(t, open, beadsDir)
				if errors.Is(err, beads.ErrProxyUnavailable) {
					t.Fatalf("%s with %s=1 and BEADS_DOLT_SERVER_PORT=%s: error = %v, want the server arm's error, not the proxied arm's", name, env, port, err)
				}
				if err == nil || !strings.Contains(err.Error(), "unreachable at 127.0.0.1:"+port+":") {
					t.Fatalf("%s with %s=1 and BEADS_DOLT_SERVER_PORT=%s: error = %v, want the server arm's dial failure at that port", name, env, port, err)
				}
				requireNoEmbeddedStore(t, beadsDir, name)
			})
		}
	}
}

// TestOpenBestAvailableProxiedEnvForcedWithoutNamedPortUsesProxy pins the
// other side of the exemption: forced server mode without a positive
// BEADS_DOLT_SERVER_PORT would dial a port bd resolves itself, never the
// proxy's, so the open goes through the proxy arm (ErrProxyUnavailable here)
// and dials nothing. Neither a BEADS_DOLT_SERVER_PORT that is not a positive
// port nor the legacy BEADS_DOLT_PORT names one.
func TestOpenBestAvailableProxiedEnvForcedWithoutNamedPortUsesProxy(t *testing.T) {
	ports := []struct {
		name       string
		serverPort string
		legacyPort bool
	}{
		{name: "no-port", serverPort: ""},
		{name: "server-port-zero", serverPort: "0"},
		{name: "server-port-not-a-port", serverPort: "not-a-port"},
		{name: "legacy-port-only", serverPort: "", legacyPort: true},
	}
	for _, env := range envForcedServerModes {
		for _, port := range ports {
			for name, open := range proxiedBestAvailableOpens {
				t.Run(env+"/"+port.name+"/"+name, func(t *testing.T) {
					isolateProxiedOpenEnv(t)
					t.Setenv(env, "1")
					t.Setenv("BEADS_DOLT_SERVER_PORT", port.serverPort)
					legacy := ""
					if port.legacyPort {
						legacy = strconv.Itoa(closedProxiedLoopbackPort(t))
					}
					t.Setenv("BEADS_DOLT_PORT", legacy)
					_, beadsDir := writeProxiedWorkspace(t, "metadata.json", "proxied-server", true)
					err := openProxiedClosing(t, open, beadsDir)
					if !errors.Is(err, beads.ErrProxyUnavailable) {
						t.Fatalf("%s with %s=1 (%s): error = %v, want errors.Is ErrProxyUnavailable", name, env, port.name, err)
					}
					if strings.Contains(err.Error(), "unreachable at") {
						t.Fatalf("%s with %s=1 (%s) dialed a server-arm port: %v", name, env, port.name, err)
					}
					requireNoEmbeddedStore(t, beadsDir, name)
				})
			}
		}
	}
}
