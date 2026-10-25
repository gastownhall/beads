//go:build cgo

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestInitCloneRemoteHostOwnership pins bd init's remote clone
// (initCloneFromRemote) for every spelling of the host. Without --external
// it clones with the dolt CLI into .beads/dolt only for a host bd runs its
// server at (configfile.BdRunsServerAt, through initRemoteCloneMode). For
// any other host (the IPv6 loopback in any spelling, another loopback
// address, another machine) the clone goes through the server init's store
// opens next, as with --external, on the port that store dials
// (initServerCloneEndpoint). When nothing names a port for that server the
// clone refuses before dialing anything, wrapping
// dolt.ErrUnresolvedServerPort, rather than DOLT_CLONE onto whatever
// listens on a default port, and init gives the store's advice for that
// server (initUnresolvedClonePortAdvice): pin the port of a server bd does
// not run, or, under --external, name it. The server here does not hold the
// database yet (TestInitRemoteCloneGoesWhereTheStoreOpens covers one that
// does).
func TestInitCloneRemoteHostOwnership(t *testing.T) {
	const flagPort = 42001
	for _, tc := range []struct {
		name string
		host string
		// hostFlag: the host is --server-host; otherwise it is the caller's
		// own BEADS_DOLT_SERVER_HOST.
		hostFlag bool
		port     int // --server-port
		socket   string
		external bool
		// want is "cli" (dolt CLI clone into .beads/dolt), "server" or
		// "refuse".
		want     string
		wantPort int // for "server"; 0 = not asserted
	}{
		{name: "default host", host: "", want: "cli"},
		{name: "127.0.0.1", host: "127.0.0.1", hostFlag: true, want: "cli"},
		{name: "LOCALHOST", host: "LOCALHOST", hostFlag: true, want: "cli"},
		{name: "::1 no port", host: "::1", hostFlag: true, want: "refuse"},
		{name: "[::1] no port", host: "[::1]", hostFlag: true, want: "refuse"},
		{name: "0:0:0:0:0:0:0:1 no port", host: "0:0:0:0:0:0:0:1", hostFlag: true, want: "refuse"},
		{name: "::1 from the environment no port", host: "::1", want: "refuse"},
		{name: "::1 --server-port", host: "::1", hostFlag: true, port: flagPort, want: "server", wantPort: flagPort},
		{name: "127.0.0.2 no port", host: "127.0.0.2", hostFlag: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "remote --server-port", host: "db.example.com", hostFlag: true, port: flagPort, want: "server", wantPort: flagPort},
		{name: "::1 --external no port", host: "::1", hostFlag: true, external: true, want: "refuse"},
		{name: "[::1] --external no port", host: "[::1]", hostFlag: true, external: true, want: "refuse"},
		{name: "0:0:0:0:0:0:0:1 --external no port", host: "0:0:0:0:0:0:0:1", hostFlag: true, external: true, want: "refuse"},
		{name: "::1 from the environment --external no port", host: "::1", external: true, want: "refuse"},
		{name: "::1 --external --server-port", host: "::1", hostFlag: true, port: flagPort, external: true, want: "server", wantPort: flagPort},
		{name: "[::1] --external --server-port", host: "[::1]", hostFlag: true, port: flagPort, external: true, want: "server", wantPort: flagPort},
		{name: "::1 --external socket no port", host: "::1", hostFlag: true, socket: "/tmp/bd-test-dolt.sock", external: true, want: "server"},
		{name: "127.0.0.2 --external no port", host: "127.0.0.2", hostFlag: true, external: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "remote --external no port", host: "db.example.com", external: true, want: "server", wantPort: configfile.DefaultDoltServerPort},
		{name: "remote --external --server-port", host: "db.example.com", hostFlag: true, port: flagPort, external: true, want: "server", wantPort: flagPort},
		{name: "127.0.0.1 --external no port", host: "127.0.0.1", hostFlag: true, external: true, want: "refuse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clonePortTestEnv(t)
			// The store's test-mode guard turns a missing or production port
			// into 1; nothing here opens a store, and the ports compared are
			// the ones a real init dials.
			t.Setenv("BEADS_TEST_MODE", "")
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			writeClonePortFile(t, filepath.Join(beadsDir, "config.yaml"), "# Beads Config\n")
			// What promoteExplicitServerConnFlags does with --server-host,
			// --server-port and --server-socket before init clones.
			t.Setenv("BEADS_DOLT_SERVER_HOST", tc.host)
			if tc.port > 0 {
				t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(tc.port))
			}
			t.Setenv("BEADS_DOLT_SERVER_SOCKET", tc.socket)
			// A credential command would make the server a gateway, which
			// keeps the CLI clone on any host.
			t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "")
			loadClonePortConfig(t, beadsDir)

			var server *bootstrapServerProbeConfig
			cli := false
			origServer, origCLI, origCheck := remoteCloneViaServer, remoteCloneViaCLI, checkBootstrapServerDB
			t.Cleanup(func() {
				remoteCloneViaServer, remoteCloneViaCLI, checkBootstrapServerDB = origServer, origCLI, origCheck
			})
			// The server init clones through does not hold the database yet.
			checkBootstrapServerDB = func(bootstrapServerProbeConfig) bootstrapServerDBCheck {
				return bootstrapServerDBCheck{Reachable: true}
			}
			remoteCloneViaServer = func(_ context.Context, _, _ string, ep bootstrapServerProbeConfig) error {
				server = &ep
				return nil
			}
			remoteCloneViaCLI = func(context.Context, string, string, string) error {
				cli = true
				return nil
			}

			flagHost := ""
			if tc.hostFlag {
				flagHost = tc.host
			}
			cloneCfg := initTimeCloneConfig(true, flagHost, tc.port, tc.socket, "", "init_clone")
			_, err := initCloneFromRemote(t.Context(), beadsDir, "file:///nonexistent/remote", "init_clone", cloneCfg, true, tc.external, "")

			switch tc.want {
			case "cli":
				if err != nil || !cli || server != nil {
					t.Fatalf("err=%v cli=%v server=%+v, want a dolt CLI clone into .beads/dolt only", err, cli, server)
				}
				return
			case "refuse":
				if !errors.Is(err, dolt.ErrUnresolvedServerPort) {
					t.Fatalf("err = %v, want the unresolved-port refusal", err)
				}
				advice, wantAdvice := initUnresolvedClonePortAdvice(err, tc.external, cloneCfg.GetDoltServerHost()), "pin its port"
				if tc.external {
					wantAdvice = "--server-port"
				}
				if !strings.Contains(advice, wantAdvice) {
					t.Errorf("init's advice = %q, want it to say %q", advice, wantAdvice)
				}
				if cli || server != nil {
					t.Fatalf("cloned anyway (cli=%v server=%+v), want no clone", cli, server)
				}
				return
			}
			if err != nil || cli || server == nil {
				t.Fatalf("err=%v cli=%v server=%+v, want a clone on the server only", err, cli, server)
			}
			if server.host != tc.host && !(tc.host == "" && server.host == configfile.DefaultDoltServerHost) {
				t.Errorf("clone dialed host %q, want %q", server.host, tc.host)
			}
			if server.socket != tc.socket {
				t.Errorf("clone dialed socket %q, want %q", server.socket, tc.socket)
			}
			if tc.wantPort == 0 {
				return
			}
			if server.port != tc.wantPort {
				t.Errorf("clone dialed port %d, want %d", server.port, tc.wantPort)
			}
			storeCfg := &dolt.Config{BeadsDir: beadsDir}
			storeCfg.ServerPort, storeCfg.ServerPortSource, storeCfg.ServerPortSharedServer = initStoreServerPort(beadsDir)
			if tc.port > 0 {
				storeCfg.ServerPort, storeCfg.ServerPortSource = tc.port, doltserver.PortSourceCallerExplicit
			}
			dolt.ResolveServerPort(storeCfg)
			if storeCfg.ServerPort != server.port {
				t.Errorf("clone dialed port %d, but init's store dials %d", server.port, storeCfg.ServerPort)
			}
		})
	}
}
