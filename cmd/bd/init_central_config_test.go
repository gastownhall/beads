//go:build cgo

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestInitServerModeHonorsCentralServerConfig pins GH#3895's init contract:
// with no --server-* flag and no BEADS_DOLT_SERVER_* env, a server-mode
// init's store dials the endpoint the central server config names — host,
// port, user and TLS included — and starts no local dolt server, because
// the file points every workspace on the machine at a server bd never runs.
func TestInitServerModeHonorsCentralServerConfig(t *testing.T) {
	port := freeLoopbackPort(t)
	central := filepath.Join(t.TempDir(), "server.json")
	body := fmt.Sprintf(`{"dolt_server_host":"db.invalid","dolt_server_port":%d,"dolt_server_user":"central_user","dolt_server_tls":true}`, port)
	if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// End to end: against a dead central endpoint, init fails naming that
	// endpoint and launching nothing.
	f := newExternalInitFixture(t)
	f.env = append(f.env, "BEADS_CENTRAL_CONFIG="+central)
	res := f.run("", "init", "--server")

	requireNoLaunch(t, res)
	if !res.failed {
		t.Errorf("bd init succeeded with no server at the central endpoint:\n%s", res.output)
	}
	want := net.JoinHostPort("db.invalid", strconv.Itoa(port))
	if !strings.Contains(res.output, want) {
		t.Errorf("bd init output does not name the central endpoint %q:\n%s", want, res.output)
	}

	// The resolvers init's store reads: the four connection fields all come
	// from the central server config, and the port arrives with its source.
	for _, name := range []string{
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_USER", "BEADS_DOLT_SERVER_TLS",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_SHARED_SERVER",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("BEADS_CENTRAL_CONFIG", central)
	if got := initStoreServerHost(""); got != "db.invalid" {
		t.Errorf("initStoreServerHost() = %q, want db.invalid", got)
	}
	if got := initStoreServerUser(""); got != "central_user" {
		t.Errorf("initStoreServerUser() = %q, want central_user", got)
	}
	if !initDoltServerTLS() {
		t.Error("initDoltServerTLS() = false, want true from the central server config")
	}
	// At a bd host, the tail of initStoreServerPort returns the central
	// port, resolved, with the central-config source — the same
	// externally-managed mark --external's named port carries.
	gotPort, source, shared, resolved := initStoreServerPort(t.TempDir(), "127.0.0.1")
	if gotPort != port || source != doltserver.PortSourceCentralConfig || shared || !resolved {
		t.Errorf("initStoreServerPort() = (%d, %q, shared=%v, resolved=%v), want (%d, %q, false, true)",
			gotPort, source, shared, resolved, port, doltserver.PortSourceCentralConfig)
	}
	// bd treats the endpoint the central config supplies like --external's:
	// it never auto-starts a server for it.
	if !initCentralSuppliedEndpoint("", 0, doltserver.PortSourceCentralConfig) {
		t.Error("initCentralSuppliedEndpoint() = false, want true for a central-config endpoint")
	}
}
