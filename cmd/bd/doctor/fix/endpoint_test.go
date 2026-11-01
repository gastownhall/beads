package fix

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

func fixEndpointWorkspace(t *testing.T) (string, *configfile.Config) {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_PASSWORD", "BEADS_DOLT_AUTO_START", "BEADS_DOLT_SERVER_USER",
		"BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DOLT_DATA_DIR",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(home, "no-credentials"))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "fix_endpoint"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	return beadsDir, cfg
}

// closedLocalPorts are n distinct 127.0.0.1 ports nothing listens on: a dial
// of one fails at once with an error naming it.
func closedLocalPorts(t *testing.T, n int) []int {
	t.Helper()
	// Every listener stays open until all n are picked, so no two ports
	// are the same.
	listeners := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range listeners {
		_ = l.Close()
	}
	return ports
}

// TestServerMetadataReconcileNeverDialsAnUnresolvedPort covers the reconcile
// bd bootstrap runs first, and bd doctor --fix runs, on a server-mode
// workspace: its catalog read dials the endpoint every store open dials.
//
//   - On a fresh clone of a `bd init --server` project no port is named and
//     no server runs. When the next command starts bd's own server there is
//     nothing to reconcile yet; otherwise the reconcile refuses with the
//     store's advice. It used to dial 127.0.0.1:0 and fail the whole
//     bootstrap with "dial tcp 127.0.0.1:0: connect: connection refused".
//   - When only the legacy BEADS_DOLT_PORT names the server, the catalog is
//     read at that port, over a port file's, as the store dials it. The
//     reconcile's guard took that port while its dial took DefaultConfig's,
//     which leaves the variable out, so it dialed 127.0.0.1:0, or read the
//     catalog of the port file's server, which no store open dials there.
func TestServerMetadataReconcileNeverDialsAnUnresolvedPort(t *testing.T) {
	ports := closedLocalPorts(t, 2)
	legacyAddr, portFileAddr := "127.0.0.1:"+strconv.Itoa(ports[0]), "127.0.0.1:"+strconv.Itoa(ports[1])
	deadSocket := filepath.Join(t.TempDir(), "dolt.sock")
	for _, tc := range []struct {
		name      string
		autoStart string
		stopped   bool // the data directory already holds a database
		legacy    bool // BEADS_DOLT_PORT names the server at legacyAddr
		portFile  bool // the port file names the server at portFileAddr
		socket    bool // only a unix socket, which nothing listens on, names the server
		refuse    bool // the reconcile must fail without dialing anything it resolved
		wantErr   string
	}{
		{name: "fresh clone"},
		{name: "auto-start off", autoStart: "0", wantErr: "dolt.auto-start is off"},
		// A stopped server whose data directory holds databases is not a
		// fresh clone: say the repair could not run instead of skipping it.
		{name: "stopped server with databases", stopped: true, wantErr: "this workspace's Dolt server is not running"},
		// Nothing listens at either port, so the dial's error names the
		// one the reconcile dialed.
		{name: "legacy BEADS_DOLT_PORT only", legacy: true, wantErr: legacyAddr},
		{name: "legacy BEADS_DOLT_PORT over the port file", legacy: true, portFile: true, wantErr: legacyAddr},
		// A socket-only server is deliberately still refused, as before: the
		// catalog read stays TCP-only, so bd bootstrap stops at the reconcile
		// and writes nothing. Its later steps cannot reach a socket-only
		// server yet (the store's library open ignores metadata.json's
		// dolt_server_socket, and the jsonl-import, init and restore actions
		// open an embedded store), so letting the reconcile pass would report
		// a bootstrap that left the workspace broken.
		{name: "unix socket only, still refused", socket: true, refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, _ := fixEndpointWorkspace(t)
			t.Setenv("BEADS_DOLT_AUTO_START", tc.autoStart)
			if tc.stopped {
				if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "fix_endpoint", ".dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.legacy {
				t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(ports[0]))
			}
			if tc.portFile {
				if err := doltserver.EnsurePortFile(beadsDir, ports[1]); err != nil {
					t.Fatal(err)
				}
			}
			if tc.socket {
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", deadSocket)
			}
			_, msg, err := ResolveAuthoritativeServerMetadata(filepath.Dir(beadsDir), false)
			if tc.refuse {
				if err == nil {
					t.Fatalf("reconcile = %q, nil; want it to refuse a socket-only server", msg)
				}
				if strings.Contains(err.Error(), deadSocket) {
					t.Errorf("reconcile error = %v: it dialed the socket %s; it must keep refusing a socket-only server", err, deadSocket)
				}
				// A socket-only endpoint is Dialable, so the TCP-only catalog
				// read dialed 127.0.0.1:0, and that failed dial passed for the
				// refusal.
				if strings.Contains(err.Error(), "127.0.0.1:0") || !strings.Contains(err.Error(), "only a unix socket names") {
					t.Errorf("reconcile error = %v, want the socket-only refusal and no dial of port 0", err)
				}
				return
			}
			if tc.wantErr == "" {
				if err != nil || msg != "" {
					t.Errorf("reconcile = %q, %v; want nothing to reconcile before bd's server first starts", msg, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "127.0.0.1:0") {
				t.Errorf("reconcile error = %v, want %q and no dial of port 0", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), portFileAddr) {
				t.Errorf("reconcile error = %v: it read the catalog at the port file's %s, which no store open dials", err, portFileAddr)
			}
		})
	}
}

// TestFixConnectionDialsTheEndpointTheStoreDials covers the connection every
// bd doctor --fix SQL fix opens (openDoltDB, through openFixDB): it dials the
// server the store dials, which plain bd doctor diagnoses. Nothing listens at
// any address here, so the error names the one dialed. It took DefaultConfig's
// port, which leaves the legacy BEADS_DOLT_PORT out, so with only that
// variable naming the server every SQL fix was skipped as unreachable at
// 127.0.0.1:0, or ran against a port file's server (#7841 review).
// BEADS_DOLT_SERVER_PORT is the control. A socket-only server is deliberately
// not dialed, as before (see openFixDB).
func TestFixConnectionDialsTheEndpointTheStoreDials(t *testing.T) {
	ports := closedLocalPorts(t, 3)
	legacyAddr, portFileAddr, envAddr := "127.0.0.1:"+strconv.Itoa(ports[0]), "127.0.0.1:"+strconv.Itoa(ports[1]), "127.0.0.1:"+strconv.Itoa(ports[2])
	for _, tc := range []struct {
		name     string
		env      map[string]string
		portFile bool
		want     string
	}{
		{name: "legacy BEADS_DOLT_PORT only", env: map[string]string{"BEADS_DOLT_PORT": strconv.Itoa(ports[0])}, want: legacyAddr},
		{name: "legacy BEADS_DOLT_PORT over the port file", env: map[string]string{"BEADS_DOLT_PORT": strconv.Itoa(ports[0])}, portFile: true, want: legacyAddr},
		{name: "BEADS_DOLT_SERVER_PORT", env: map[string]string{"BEADS_DOLT_SERVER_PORT": strconv.Itoa(ports[2])}, want: envAddr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, _ := fixEndpointWorkspace(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if tc.portFile {
				if err := doltserver.EnsurePortFile(beadsDir, ports[1]); err != nil {
					t.Fatal(err)
				}
			}
			db, _, err := openDoltDB(beadsDir)
			if err == nil {
				_ = db.Close()
				t.Fatalf("openDoltDB connected; nothing listens at %s", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "127.0.0.1:0") {
				t.Errorf("fix connection error = %v, want it to name %s, the endpoint the store dials", err, tc.want)
			}
			if strings.Contains(err.Error(), portFileAddr) {
				t.Errorf("fix connection error = %v: it dialed the port file's %s, which no store open dials", err, portFileAddr)
			}
		})
	}
}

// TestScanConnectionNeverDialsAnUnresolvedPort covers the connection bd
// doctor's read-only scans open (OpenScanDB, through openScanDB) for a
// workspace whose server neither a port nor a socket names, as on a fresh
// clone of a `bd init --server` project before any server runs: it is
// refused with the store's advice (doltserver.ErrUnresolvedPort), never
// dialed at 127.0.0.1:0. Six checks read through it; four of them read
// through the store's open before, which refuses there too.
func TestScanConnectionNeverDialsAnUnresolvedPort(t *testing.T) {
	beadsDir, _ := fixEndpointWorkspace(t)
	db, err := OpenScanDB(filepath.Dir(beadsDir))
	if err == nil {
		_ = db.Close()
		t.Fatal("OpenScanDB connected; neither a port nor a socket names the server")
	}
	if !errors.Is(err, doltserver.ErrUnresolvedPort) || strings.Contains(err.Error(), "127.0.0.1:0") {
		t.Errorf("OpenScanDB error = %v, want the store's unresolved-port refusal and no dial of 127.0.0.1:0", err)
	}
}
